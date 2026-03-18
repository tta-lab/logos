package logos

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/tta-lab/temenos/client"
)

// StepRole represents the role of a message step in the agent loop.
type StepRole string

const (
	StepRoleAssistant StepRole = "assistant"
	StepRoleCommand   StepRole = "command"
)

// DefaultMaxSteps is the fallback max steps when Config.MaxSteps is 0.
const DefaultMaxSteps = 30

// DefaultMaxTokens is the fallback max output tokens when Config.MaxTokens is 0.
const DefaultMaxTokens = 16384

// MaxXMLRetries is the number of times the loop will inject error feedback
// when a model outputs XML tool_call format instead of $ commands.
const MaxXMLRetries = 2

// MaxMultiCmdRetries is the number of times the loop will inject error feedback
// when a model outputs multiple $ commands in one turn.
const MaxMultiCmdRetries = 3

// Re-exported from temenos/client so consumers don't import temenos directly.
type (
	// AllowedPath specifies a filesystem path allowed in the sandbox.
	AllowedPath = client.AllowedPath
	// RunRequest is the request payload for sandboxed command execution.
	RunRequest = client.RunRequest
	// RunResponse is the response from sandboxed command execution.
	RunResponse = client.RunResponse
)

// CommandRunner executes a sandboxed command and returns the result.
// *client.Client satisfies this interface automatically.
type CommandRunner interface {
	Run(ctx context.Context, req RunRequest) (*RunResponse, error)
}

// Config holds everything needed to run one agent loop iteration.
type Config struct {
	Provider     fantasy.Provider
	Model        string
	SystemPrompt string
	MaxSteps     int // 0 means use default (DefaultMaxSteps)
	MaxTokens    int // 0 means use default (DefaultMaxTokens)
	Temenos      CommandRunner
	SandboxEnv   map[string]string // env vars passed to temenos per-request
	// AllowedPaths lists filesystem paths accessible during command execution.
	// Path validation (non-empty, absolute) is enforced by the temenos daemon.
	AllowedPaths []AllowedPath
}

// StepMessage represents one message generated during the agent loop.
type StepMessage struct {
	Role      StepRole
	Content   string
	Timestamp time.Time
}

// RunResult contains the agent's output after a loop completes.
type RunResult struct {
	Response string        // final text response (accumulated assistant text)
	Steps    []StepMessage // all messages generated (for persistence by caller)
}

// Callbacks holds optional streaming callbacks for the agent loop.
// All fields are nil-safe — unset callbacks are simply not called.
type Callbacks struct {
	// OnDelta is called with each text delta as the LLM streams its response.
	OnDelta func(text string)
	// OnCommandStart is called when a $ command is detected, before execution.
	OnCommandStart func(command string)
}

// Run executes the agent loop: prompt → LLM → $ commands → repeat.
// Stateless — the caller handles conversation persistence.
func Run(
	ctx context.Context,
	cfg Config,
	history []fantasy.Message,
	prompt string,
	cbs Callbacks,
) (*RunResult, error) {
	if cfg.Provider == nil {
		return nil, fmt.Errorf("logos: Config.Provider must not be nil")
	}
	if cfg.Temenos == nil {
		return nil, fmt.Errorf("logos: Config.Temenos must not be nil")
	}

	model, err := cfg.Provider.LanguageModel(ctx, cfg.Model)
	if err != nil {
		return nil, fmt.Errorf("get language model: %w", err)
	}

	maxSteps := cfg.MaxSteps
	if maxSteps <= 0 {
		maxSteps = DefaultMaxSteps
	}
	maxTokens := int64(cfg.MaxTokens)
	if maxTokens <= 0 {
		maxTokens = DefaultMaxTokens
	}

	// Build conversation: system prompt + history + user prompt.
	messages := make([]fantasy.Message, 0, len(history)+2)
	messages = append(messages, fantasy.NewSystemMessage(cfg.SystemPrompt))
	messages = append(messages, history...)
	messages = append(messages, fantasy.NewUserMessage(prompt))

	var (
		steps           []StepMessage
		responseText    strings.Builder
		xmlRetries      int
		multiCmdRetries int
	)

	for step := 0; step < maxSteps; step++ {
		fullText, streamErr := streamOneTurn(ctx, model, messages, maxTokens, func(text string) {
			if cbs.OnDelta != nil {
				cbs.OnDelta(text)
			}
		})
		if streamErr != nil {
			return nil, fmt.Errorf("stream turn %d: %w", step, streamErr)
		}

		preText, cmd, found := scanForCommand(fullText)
		steps = append(steps, StepMessage{Role: StepRoleAssistant, Content: fullText, Timestamp: time.Now().UTC()})

		if !found {
			if ContainsXMLToolCall(fullText) {
				if xmlRetries < MaxXMLRetries {
					xmlRetries++
					feedback := "Error: You used XML/structured tool_call format. This is not supported.\n" +
						"Use $ command format instead. Example: $ rg 'pattern' /path\n" +
						"Do NOT use <invoke>, <tool_call>, or XML tags. One command per $ line."
					steps = append(steps, StepMessage{Role: StepRoleCommand, Content: feedback, Timestamp: time.Now().UTC()})
					messages = append(messages, newAssistantMessage(fullText), fantasy.NewUserMessage(feedback))
					step-- // don't count XML correction against step budget
					continue
				}
				return &RunResult{Response: responseText.String(), Steps: steps},
					fmt.Errorf("logos: model persisted XML tool_call format after %d correction attempts", MaxXMLRetries)
			}
			responseText.WriteString(fullText)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		// Reject multi-command turns — tell the model to run one at a time.
		if countCommands(fullText) > 1 {
			if multiCmdRetries >= MaxMultiCmdRetries {
				return &RunResult{Response: responseText.String(), Steps: steps},
					fmt.Errorf("logos: model persisted multi-command output after %d correction attempts", MaxMultiCmdRetries)
			}
			multiCmdRetries++
			feedback := "Error: You wrote multiple $ commands in one message. " +
				"Only one command per message is supported.\n" +
				"Run one command, wait for its output, then run the next."
			steps = append(steps, StepMessage{Role: StepRoleCommand, Content: feedback, Timestamp: time.Now().UTC()})
			messages = append(messages, newAssistantMessage(fullText), fantasy.NewUserMessage(feedback))
			step-- // don't count multi-command correction against step budget
			continue
		}

		responseText.WriteString(preText)
		if cbs.OnCommandStart != nil {
			cbs.OnCommandStart(cmd.Args)
		}

		output := execCommand(ctx, cfg.Temenos, cmd.Args, cfg.SandboxEnv, cfg.AllowedPaths)
		steps = append(steps, StepMessage{Role: StepRoleCommand, Content: output, Timestamp: time.Now().UTC()})

		messages = append(messages, newAssistantMessage(fullText), fantasy.NewUserMessage(output))
	}

	return &RunResult{
		Response: responseText.String(),
		Steps:    steps,
	}, fmt.Errorf("logos: max steps (%d) reached", maxSteps)
}

// execCommand runs a shell command via the temenos daemon and returns formatted output.
func execCommand(
	ctx context.Context, tc CommandRunner, args string,
	env map[string]string, paths []AllowedPath,
) string {
	resp, err := tc.Run(ctx, RunRequest{
		Command:      args,
		Env:          env,
		AllowedPaths: paths,
	})
	if err != nil {
		slog.Warn("temenos exec failure", "args", args, "error", err)
		return fmt.Sprintf("execution error: %v", err)
	}

	output := resp.Stdout
	if resp.Stderr != "" {
		output += "\nSTDERR:\n" + resp.Stderr
	}
	if resp.ExitCode != 0 {
		output += fmt.Sprintf("\n(exit code: %d)", resp.ExitCode)
	}
	if output == "" {
		output = "(no output)"
	}
	return output
}

// scanForCommand finds the first $ command in text.
// If the command contains a heredoc (<<DELIM), captures lines through the
// closing delimiter. Otherwise captures only the $ line.
// Returns text before the command, the command, and whether one was found.
func scanForCommand(text string) (preText string, cmd Command, found bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		c, ok := ParseCommand(line)
		if !ok {
			continue
		}

		preText = strings.Join(lines[:i], "\n")
		if preText != "" {
			preText += "\n"
		}

		// Check for heredoc — if found, capture through closing delimiter
		if delim, hasHeredoc := heredocDelimiter(c.Args); hasHeredoc {
			// Scan remaining lines for the closing delimiter
			for j := i + 1; j < len(lines); j++ {
				if isHeredocClose(lines[j], delim) {
					// Capture from $ line through delimiter (inclusive)
					fullBlock := strings.Join(lines[i:j+1], "\n")
					c.Args = strings.TrimPrefix(strings.TrimSpace(fullBlock), "$ ")
					c.Raw = fullBlock
					return preText, c, true
				}
			}
			// No closing delimiter found — fall through to single-line capture
		}

		return preText, c, true
	}
	return text, Command{}, false
}

// countCommands returns the number of $ command lines in text,
// skipping $ lines that appear inside heredoc bodies.
func countCommands(text string) int {
	count := 0
	lines := strings.Split(text, "\n")
	var heredocDelim string // non-empty when inside a heredoc body

	for i, line := range lines {
		if heredocDelim != "" {
			// Inside heredoc body — check for closing delimiter
			if isHeredocClose(line, heredocDelim) {
				heredocDelim = ""
			}
			continue
		}

		c, ok := ParseCommand(line)
		if !ok {
			continue
		}
		count++

		// Check if this command starts a heredoc
		if delim, has := heredocDelimiter(c.Args); has {
			// Only skip heredoc body if closing delimiter exists in remaining lines
			for _, remaining := range lines[i+1:] {
				if isHeredocClose(remaining, delim) {
					heredocDelim = delim
					break
				}
			}
		}
	}
	return count
}

// streamOneTurn streams a single LLM response (no tools).
// Returns the full text and any error.
func streamOneTurn(
	ctx context.Context,
	model fantasy.LanguageModel,
	messages []fantasy.Message,
	maxTokens int64,
	onDelta func(string),
) (string, error) {
	stream, err := model.Stream(ctx, fantasy.Call{
		Prompt:          fantasy.Prompt(messages),
		MaxOutputTokens: &maxTokens,
	})
	if err != nil {
		return "", err
	}

	var fullText strings.Builder
	for part := range stream {
		switch part.Type {
		case fantasy.StreamPartTypeTextDelta:
			fullText.WriteString(part.Delta)
			onDelta(part.Delta)
		case fantasy.StreamPartTypeError:
			if part.Error != nil {
				return fullText.String(), part.Error
			}
		}
	}
	return fullText.String(), nil
}

// newAssistantMessage wraps text as a fantasy assistant message.
func newAssistantMessage(text string) fantasy.Message {
	return fantasy.Message{
		Role:    fantasy.MessageRoleAssistant,
		Content: []fantasy.MessagePart{fantasy.TextPart{Text: text}},
	}
}

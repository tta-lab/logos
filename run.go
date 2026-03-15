package logos

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/tta-lab/logos/sandbox"
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

// Config holds everything needed to run one agent loop iteration.
type Config struct {
	Provider     fantasy.Provider
	Model        string
	SystemPrompt string
	MaxSteps     int // 0 means use default (DefaultMaxSteps)
	MaxTokens    int // 0 means use default (DefaultMaxTokens)
	Sandbox      sandbox.Sandbox
	SandboxEnv   []string // extra env vars for sandbox
	AllowedPaths []string // converted to sandbox mounts
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
	if cfg.Sandbox == nil {
		return nil, fmt.Errorf("logos: Config.Sandbox must not be nil")
	}

	execCfg, err := buildExecConfig(cfg)
	if err != nil {
		return nil, err
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
		steps        []StepMessage
		responseText strings.Builder
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
			responseText.WriteString(fullText)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		responseText.WriteString(preText)
		if cbs.OnCommandStart != nil {
			cbs.OnCommandStart(cmd.Args)
		}

		output := execCommand(ctx, cfg.Sandbox, cmd.Args, execCfg)
		steps = append(steps, StepMessage{Role: StepRoleCommand, Content: output, Timestamp: time.Now().UTC()})

		assistantMsg := fantasy.Message{
			Role:    fantasy.MessageRoleAssistant,
			Content: []fantasy.MessagePart{fantasy.TextPart{Text: fullText}},
		}
		messages = append(messages, assistantMsg)
		messages = append(messages, fantasy.NewUserMessage(output))
	}

	return &RunResult{
		Response: responseText.String(),
		Steps:    steps,
	}, fmt.Errorf("logos: max steps (%d) reached", maxSteps)
}

// buildExecConfig validates AllowedPaths and constructs the sandbox ExecConfig.
func buildExecConfig(cfg Config) (*sandbox.ExecConfig, error) {
	var mounts []sandbox.Mount
	for _, p := range cfg.AllowedPaths {
		if p == "" || !filepath.IsAbs(p) {
			return nil, fmt.Errorf("logos: AllowedPaths entry %q must be a non-empty absolute path", p)
		}
		mounts = append(mounts, sandbox.Mount{Source: p, Target: p, ReadOnly: true})
	}
	return &sandbox.ExecConfig{Env: cfg.SandboxEnv, MountDirs: mounts}, nil
}

// execCommand runs a shell command in the sandbox and returns formatted output.
func execCommand(ctx context.Context, sb sandbox.Sandbox, args string, execCfg *sandbox.ExecConfig) string {
	stdout, stderr, exitCode, execErr := sb.Exec(ctx, args, execCfg)
	if execErr != nil {
		stdout = fmt.Sprintf("execution error: %v", execErr)
	}

	output := stdout
	if stderr != "" {
		output += "\nSTDERR:\n" + stderr
	}
	if exitCode != 0 {
		output += fmt.Sprintf("\n(exit code: %d)", exitCode)
	}
	if output == "" {
		output = "(no output)"
	}
	return output
}

// scanForCommand finds the first $ command in text.
// Returns text before the command, the command, and whether one was found.
func scanForCommand(text string) (preText string, cmd Command, found bool) {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if c, ok := ParseCommand(line); ok {
			preText = strings.Join(lines[:i], "\n")
			if preText != "" {
				preText += "\n"
			}
			return preText, c, true
		}
	}
	return text, Command{}, false
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

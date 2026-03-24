package logos

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/tta-lab/temenos/client"
)

// StepRole represents the role of a message step in the agent loop.
type StepRole string

const (
	StepRoleAssistant StepRole = "assistant" // LLM turn (with or without commands)
	StepRoleUser      StepRole = "user"      // human input
	StepRoleResult    StepRole = "result"    // command output fed back to LLM
)

// DefaultMaxSteps is the fallback max steps when Config.MaxSteps is 0.
const DefaultMaxSteps = 30

// DefaultMaxTokens is the fallback max output tokens when Config.MaxTokens is 0.
const DefaultMaxTokens = 16384

// MaxHallucinationRetries is the maximum number of tool call hallucination retries
// before Run() returns an error.
const MaxHallucinationRetries = 3

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
	Role               StepRole
	Content            string
	Reasoning          string // thinking block text (empty if no reasoning)
	ReasoningSignature string // provider signature for round-trip
	Timestamp          time.Time
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
	// OnCommandStart is called when a § command is detected, before execution.
	OnCommandStart func(command string)
	// OnCommandResult is called after a command executes with the command string,
	// raw combined stdout+stderr output (no exit code suffix), and the exit code.
	// exitCode is -1 if the sandbox itself failed to execute the command (temenos
	// transport error), in which case output contains the error description.
	OnCommandResult func(command string, output string, exitCode int)
	// OnRetry is called when a tool call hallucination (XML or bracket) is detected
	// and an "unprocessed" directive is injected. reason is "tool_call".
	OnRetry func(reason string, step int)
}

// Run executes the agent loop: prompt → LLM → § commands → repeat.
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
		steps              []StepMessage
		responseText       strings.Builder
		hallucinationCount int
	)

	for step := 0; step < maxSteps; step++ {
		onDelta := func(text string) {
			if cbs.OnDelta != nil {
				cbs.OnDelta(text)
			}
		}
		fullText, reasoning, reasoningSig, toolCallDetected, streamErr :=
			streamOneTurn(ctx, model, messages, maxTokens, onDelta)
		if streamErr != nil {
			return nil, fmt.Errorf("stream turn %d: %w", step, streamErr)
		}

		// Check tool call hallucination BEFORE appending to Steps.
		if toolCallDetected {
			hallucinationCount++
			if hallucinationCount > MaxHallucinationRetries {
				return nil, fmt.Errorf("logos: tool call hallucination not resolved after %d retries", MaxHallucinationRetries)
			}
			directive := hallucinationDirective(hallucinationCount)
			slog.Warn("tool call hallucination detected", "step", step, "attempt", hallucinationCount)
			if cbs.OnRetry != nil {
				cbs.OnRetry("tool_call", step)
			}
			// Record both the wrong output and feedback in Steps (for conversation restore).
			steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
			steps = append(steps, StepMessage{Role: StepRoleResult, Content: directive, Timestamp: time.Now().UTC()})
			aMsg := newAssistantMessage(fullText, reasoning, reasoningSig)
			messages = append(messages, aMsg, fantasy.NewUserMessage(directive))
			continue
		}

		preText, cmds := scanAllCommands(fullText)

		if len(cmds) == 0 {
			// Final answer — return
			steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
			responseText.WriteString(fullText)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		// Has commands — execute all sequentially
		steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
		responseText.WriteString(preText)

		// Execute each command via runAndNotify (fires OnCommandResult callback),
		// then format output for LLM with exit code suffix.
		cmdOutputs := executeCommands(ctx, cfg, cbs, cmds)
		if len(cmdOutputs) == 0 {
			// ctx was already cancelled before any command ran; next streamOneTurn will
			// return the context error and propagate it cleanly.
			continue
		}
		userContent := "<result>\n" + strings.Join(cmdOutputs, "\n") + "\n</result>"

		steps = append(steps, StepMessage{Role: StepRoleResult, Content: userContent, Timestamp: time.Now().UTC()})
		aMsg2 := newAssistantMessage(fullText, reasoning, reasoningSig)
		messages = append(messages, aMsg2, fantasy.NewUserMessage(userContent))
	}

	return &RunResult{
		Response: responseText.String(),
		Steps:    steps,
	}, fmt.Errorf("logos: max steps (%d) reached", maxSteps)
}

// hallucinationDirective returns a directive message for the model after detecting
// a tool call hallucination. Escalates in urgency on repeated attempts.
func hallucinationDirective(attempt int) string {
	if attempt <= 1 {
		return "(Unprocessed: your output contained a tool call format that is not supported. " +
			"This environment has no tool/function calling API. " +
			"To run a command, wrap it in a <cmd> block — e.g.:\n<cmd>\n§ ls -la\n</cmd>)"
	}
	return fmt.Sprintf("(Unprocessed: tool call format detected again (attempt %d). "+
		"There is NO tool calling API. The ONLY way to run commands is inside a <cmd> block. "+
		"Do NOT use XML tags, brackets, or structured format. Example:\n<cmd>\n§ ls -la\n§ cat file.go\n</cmd>)", attempt)
}

// newAssistantStep builds a StepMessage for an assistant turn, including optional
// reasoning fields. All three call sites share this helper to avoid repetition and
// ensure UTC timestamps are used consistently.
func newAssistantStep(content, reasoning, reasoningSig string) StepMessage {
	return StepMessage{
		Role:               StepRoleAssistant,
		Content:            content,
		Reasoning:          reasoning,
		ReasoningSignature: reasoningSig,
		Timestamp:          time.Now().UTC(),
	}
}

// runAndNotify executes a command and fires OnCommandResult.
// Returns raw output and exit code; callers format for LLM separately.
func runAndNotify(ctx context.Context, cfg Config, cbs Callbacks, args string) (string, int) {
	rawOutput, exitCode := execCommand(ctx, cfg.Temenos, args, cfg.SandboxEnv, cfg.AllowedPaths)
	if cbs.OnCommandResult != nil {
		cbs.OnCommandResult(args, rawOutput, exitCode)
	}
	return rawOutput, exitCode
}

// executeCommands runs each command sequentially, firing OnCommandStart per command,
// and returns formatted output parts ready for joining into a user message.
// Stops early if ctx is already cancelled before a command starts.
func executeCommands(ctx context.Context, cfg Config, cbs Callbacks, cmds []Command) []string {
	outputParts := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		if ctx.Err() != nil {
			break
		}
		if cbs.OnCommandStart != nil {
			cbs.OnCommandStart(cmd.Args)
		}
		rawOutput, exitCode := runAndNotify(ctx, cfg, cbs, cmd.Args)
		outputParts = append(outputParts, CommandPrefix+cmd.Args+"\n"+formatForLLM(rawOutput, exitCode))
	}
	return outputParts
}

// formatForLLM formats command output for the LLM message.
// Appends exit code suffix for non-zero exits; skips it for exitCode -1 (transport
// error) since rawOutput already contains the error description.
func formatForLLM(rawOutput string, exitCode int) string {
	if exitCode != 0 && exitCode != -1 {
		return rawOutput + fmt.Sprintf("\n(exit code: %d)", exitCode)
	}
	return rawOutput
}

// execCommand runs a shell command via the temenos daemon and returns the raw
// combined output and exit code. The "(no output)" sentinel is included in
// rawOutput when the command produces nothing. On temenos error, returns an
// error string and exitCode -1.
func execCommand(
	ctx context.Context, tc CommandRunner, args string,
	env map[string]string, paths []AllowedPath,
) (rawOutput string, exitCode int) {
	resp, err := tc.Run(ctx, RunRequest{
		Command:      args,
		Env:          env,
		AllowedPaths: paths,
	})
	if err != nil {
		slog.Error("temenos exec failure", "args", args, "error", err)
		return fmt.Sprintf("execution error: %v", err), -1
	}

	output := resp.Stdout
	if resp.Stderr != "" {
		output += "\nSTDERR:\n" + resp.Stderr
	}
	if output == "" {
		output = "(no output)"
	}
	return output, resp.ExitCode
}

// scanAllCommands extracts all § commands from text, in order.
// Commands are only recognized inside <cmd>...</cmd> blocks.
// Bare § lines outside blocks are treated as prose and ignored.
// Returns the text before the first <cmd> block and a slice of commands.
func scanAllCommands(text string) (preText string, cmds []Command) {
	remaining := text
	firstBlockIdx := strings.Index(remaining, "<cmd>")
	if firstBlockIdx == -1 {
		return text, nil // no commands
	}
	preText = remaining[:firstBlockIdx]

	for {
		openIdx := strings.Index(remaining, "<cmd>")
		if openIdx == -1 {
			break
		}
		closeIdx := strings.Index(remaining[openIdx:], "</cmd>")
		var blockContent string
		if closeIdx == -1 {
			// Unclosed block — take rest as block content and warn
			blockContent = remaining[openIdx+len("<cmd>"):]
			remaining = ""
			slog.Warn("logos: unclosed <cmd> block, executing commands from partial block")
		} else {
			blockContent = remaining[openIdx+len("<cmd>") : openIdx+closeIdx]
			remaining = remaining[openIdx+closeIdx+len("</cmd>"):]
		}

		// Parse § lines within the block, reusing heredoc logic.
		lines := strings.Split(blockContent, "\n")
		var heredocDelim string
		for i, line := range lines {
			if heredocDelim != "" {
				if isHeredocClose(line, heredocDelim) {
					heredocDelim = ""
				}
				continue
			}
			c, ok := ParseCommand(line)
			if !ok {
				continue
			}
			if delim, hasHeredoc := heredocDelimiter(c.Args); hasHeredoc {
				var body strings.Builder
				body.WriteString(c.Args)
				heredocDelim = delim
				for j := i + 1; j < len(lines); j++ {
					body.WriteString("\n" + lines[j])
					if isHeredocClose(lines[j], delim) {
						heredocDelim = ""
						break
					}
				}
				cmds = append(cmds, Command{Raw: c.Raw, Args: body.String()})
			} else {
				cmds = append(cmds, c)
			}
		}

		if remaining == "" {
			break
		}
	}
	return preText, cmds
}

// streamFilter sits between the LLM stream and OnDelta, filtering XML tool_call
// markers (suppress + retry), bracket tool_call markers (suppress + retry), and
// strip markers like </think> (tag-only removal — inter-tag content is passed through unchanged).
type streamFilter struct {
	delegate         func(string)
	buf              strings.Builder
	buffering        bool
	toolCallDetected bool
}

// Write processes a streaming delta. Fast path: no '<' or '[' → pass through immediately.
// Otherwise buffers from the first trigger character and checks for known markers.
func (f *streamFilter) Write(delta string) {
	if f.toolCallDetected {
		return
	}
	if !f.buffering {
		idxLT := strings.IndexByte(delta, '<')
		idxBR := strings.IndexByte(delta, '[')
		idx := firstNonNeg(idxLT, idxBR)
		if idx == -1 {
			f.delegate(delta)
			return
		}
		if idx > 0 {
			f.delegate(delta[:idx])
		}
		f.buf.Reset()
		f.buf.WriteString(delta[idx:])
		f.buffering = true
		f.checkBuffer()
		return
	}
	f.buf.WriteString(delta)
	f.checkBuffer()
}

// firstNonNeg returns the smaller of two non-negative integers, or the other
// if one is negative. Returns -1 if both are negative.
func firstNonNeg(a, b int) int {
	switch {
	case a < 0:
		return b
	case b < 0:
		return a
	default:
		if a < b {
			return a
		}
		return b
	}
}

// checkBuffer inspects the current buffer for known markers and acts accordingly.
func (f *streamFilter) checkBuffer() {
	bufStr := f.buf.String()

	// Tier 1a: XML tool_call — suppress entire stream, signal retry.
	for _, marker := range xmlToolCallMarkers {
		if strings.Contains(bufStr, marker) {
			f.toolCallDetected = true
			f.buf.Reset()
			f.buffering = false
			return
		}
	}

	// Tier 1b: Bracket tool_call (case-insensitive) — same suppression.
	if containsBracketToolCall(bufStr) {
		f.toolCallDetected = true
		f.buf.Reset()
		f.buffering = false
		return
	}

	// Tier 2: Strip markers — remove tag strings, flush surrounding text.
	// Note: only the marker strings themselves are removed; content between
	// opening and closing tags (e.g. between <think> and </think>) passes through.
	cleaned := bufStr
	stripped := false
	for _, marker := range stripMarkers {
		if strings.Contains(cleaned, marker) {
			cleaned = strings.ReplaceAll(cleaned, marker, "")
			stripped = true
		}
	}
	if stripped {
		if cleaned != "" {
			f.delegate(cleaned)
		}
		f.buf.Reset()
		f.buffering = false
		return
	}

	// Still could be a prefix of a known marker — keep buffering.
	if isPrefixOfAny(bufStr) {
		return
	}

	// Not a marker prefix — flush buffer.
	f.delegate(bufStr)
	f.buf.Reset()
	f.buffering = false
}

// Flush flushes any remaining buffered content when the stream ends.
func (f *streamFilter) Flush() {
	if f.buf.Len() > 0 && !f.toolCallDetected {
		f.delegate(f.buf.String())
	}
	f.buf.Reset()
	f.buffering = false
}

// cmdBlockFilter intercepts <cmd>...</cmd> blocks in the streaming output.
// Text outside blocks passes through to the delegate immediately. Block content
// is silently discarded — it is protocol (command invocations), not user-facing
// output. buf holds a partial tag boundary: either a partial <cmd> prefix (not
// in block) or the last few bytes of block content to detect a split </cmd>.
type cmdBlockFilter struct {
	delegate func(string)
	buf      strings.Builder
	inBlock  bool
}

func (f *cmdBlockFilter) Write(delta string) {
	// Prepend any buffered content from the previous call so we can match tags
	// that span delta boundaries. buf holds either a partial <cmd> prefix (not in
	// block) or the last few bytes of block content needed to detect a split </cmd>.
	if f.buf.Len() > 0 {
		delta = f.buf.String() + delta
		f.buf.Reset()
	}

	for len(delta) > 0 {
		if f.inBlock {
			// Inside <cmd> block — look for </cmd>
			idx := strings.Index(delta, "</cmd>")
			if idx == -1 {
				// No closing tag yet. Keep last len("</cmd>")-1 bytes buffered so a
				// split closing tag is detected on the next Write call. Discard rest.
				const closeTag = "</cmd>"
				tail := len(closeTag) - 1 // 5 bytes max needed for boundary
				if len(delta) > tail {
					delta = delta[len(delta)-tail:]
				}
				f.buf.WriteString(delta)
				return
			}
			// Found closing tag — discard block content, continue with remainder as prose
			f.inBlock = false
			delta = delta[idx+len("</cmd>"):]
			continue
		}
		// Outside block — look for <cmd>
		idx := strings.Index(delta, "<cmd>")
		if idx == -1 {
			// Check for partial <cmd> prefix at end of delta
			for plen := min(len("<cmd>")-1, len(delta)); plen > 0; plen-- {
				if strings.HasSuffix(delta, "<cmd>"[:plen]) {
					f.delegate(delta[:len(delta)-plen])
					f.buf.WriteString(delta[len(delta)-plen:])
					return
				}
			}
			f.delegate(delta)
			return
		}
		// Pass through text before <cmd>, then enter block mode (content is discarded)
		if idx > 0 {
			f.delegate(delta[:idx])
		}
		f.inBlock = true
		delta = delta[idx+len("<cmd>"):]
	}
}

func (f *cmdBlockFilter) Flush() {
	if f.buf.Len() > 0 {
		if f.inBlock {
			// Unclosed block — discard it (protocol content, not user output)
			slog.Warn("cmdBlockFilter: stream ended with unclosed <cmd> block", "buffered_len", f.buf.Len())
		} else {
			// Partial <cmd> prefix that never completed — forward it as prose
			f.delegate(f.buf.String())
		}
		f.buf.Reset()
		f.inBlock = false
	}
}

// isPrefixOfAny returns true if s is a prefix of any known marker.
// Used to determine whether to keep buffering when we see a partial trigger sequence.
func isPrefixOfAny(s string) bool {
	return hasPrefixInSlice(s, xmlToolCallMarkers) ||
		hasPrefixInSlice(s, stripMarkers) ||
		hasPrefixInSliceFold(s, bracketToolCallMarkers)
}

// hasPrefixInSliceFold returns true if any marker in the slice starts with the
// lowercase version of s. Used for case-insensitive bracket marker prefix matching.
func hasPrefixInSliceFold(s string, markers []string) bool {
	lower := strings.ToLower(s)
	for _, marker := range markers {
		if strings.HasPrefix(marker, lower) {
			return true
		}
	}
	return false
}

// hasPrefixInSlice returns true if any marker in the slice starts with s.
func hasPrefixInSlice(s string, markers []string) bool {
	for _, marker := range markers {
		if strings.HasPrefix(marker, s) {
			return true
		}
	}
	return false
}

// streamOneTurn streams a single LLM response (no tools).
// Returns the full unfiltered text, reasoning content, reasoning signature,
// whether a tool call hallucination was detected, and any error.
// Reasoning fields are empty for providers that don't emit thinking blocks.
// filter.Flush() is deferred so buffered content is always emitted, even on error.
func streamOneTurn(
	ctx context.Context,
	model fantasy.LanguageModel,
	messages []fantasy.Message,
	maxTokens int64,
	onDelta func(string),
) (text string, reasoning string, reasoningSig string, hallucinated bool, err error) {
	stream, streamErr := model.Stream(ctx, fantasy.Call{
		Prompt:          fantasy.Prompt(messages),
		MaxOutputTokens: &maxTokens,
	})
	if streamErr != nil {
		return "", "", "", false, streamErr
	}

	xmlFilter := &streamFilter{delegate: onDelta}
	cmdFilter := &cmdBlockFilter{delegate: xmlFilter.Write}
	// Order matters: cmdFilter.Flush() must run BEFORE xmlFilter.Flush().
	// cmdFilter.Flush() may emit buffered content to xmlFilter.Write,
	// so xmlFilter must still be accepting input. If reversed, that content is lost.
	defer func() { cmdFilter.Flush(); xmlFilter.Flush() }()

	var fullText strings.Builder
	var reasoningBuf strings.Builder

	for part := range stream {
		switch part.Type {
		case fantasy.StreamPartTypeTextDelta:
			fullText.WriteString(part.Delta)
			cmdFilter.Write(part.Delta)
		case fantasy.StreamPartTypeReasoningDelta:
			if part.Delta != "" {
				reasoningBuf.WriteString(part.Delta)
			}
			// Signature arrives as a ReasoningDelta with empty Delta and ProviderMetadata.
			if part.ProviderMetadata != nil {
				if meta, ok := part.ProviderMetadata[anthropic.Name]; ok {
					if rm, ok := meta.(*anthropic.ReasoningOptionMetadata); ok && rm.Signature != "" {
						reasoningSig = rm.Signature
					}
				}
			}
		case fantasy.StreamPartTypeError:
			if part.Error != nil {
				return fullText.String(), reasoningBuf.String(), reasoningSig, xmlFilter.toolCallDetected, part.Error
			}
		}
	}
	return fullText.String(), reasoningBuf.String(), reasoningSig, xmlFilter.toolCallDetected, nil
}

// newAssistantMessage wraps text (and optional reasoning) as a fantasy assistant message.
// If reasoning is non-empty, a ReasoningPart with the provider signature is prepended.
func newAssistantMessage(text, reasoning, signature string) fantasy.Message {
	var parts []fantasy.MessagePart
	if reasoning != "" {
		parts = append(parts, fantasy.ReasoningPart{
			Text: reasoning,
			ProviderOptions: fantasy.ProviderOptions{
				anthropic.Name: &anthropic.ReasoningOptionMetadata{
					Signature: signature,
				},
			},
		})
	}
	parts = append(parts, fantasy.TextPart{Text: text})
	return fantasy.Message{
		Role:    fantasy.MessageRoleAssistant,
		Content: parts,
	}
}

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

// CmdBlockOpen is the opening tag for command blocks: <cmd>...</cmd>.
const CmdBlockOpen = "<cmd>"

// CmdBlockClose is the closing tag for command blocks: <cmd>...</cmd>.
const CmdBlockClose = "</cmd>"

// Re-exported from temenos/client so consumers don't import temenos directly.
type (
	// AllowedPath specifies a filesystem path allowed in the sandbox.
	AllowedPath = client.AllowedPath
	// RunRequest is the request payload for single command execution.
	RunRequest = client.RunRequest
	// RunResponse is the response from single command execution.
	RunResponse = client.RunResponse
)

// CommandRunner executes a single command in the sandbox.
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
	// OnCommandResult is called after a command executes with the command string,
	// raw combined stdout+stderr output (no exit code suffix), and the exit code.
	OnCommandResult func(command string, output string, exitCode int)
	// OnRetry is called when a tool call hallucination (XML or bracket) is detected
	// and an "unprocessed" directive is injected. reason is "tool_call".
	OnRetry func(reason string, step int)
}

// Run executes the agent loop: prompt → LLM → <cmd> blocks → repeat.
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

		cmds := scanCommands(fullText)

		if len(cmds) == 0 {
			// Final answer — return
			steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
			responseText.WriteString(fullText)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		// Has commands — execute each via temenos
		steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
		responseText.WriteString(fullText)

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
			"To run a command, use a <cmd> block — e.g.:\n<cmd>\nls -la\n</cmd>)"
	}
	return fmt.Sprintf("(Unprocessed: tool call format detected again (attempt %d). "+
		"There is NO tool calling API. The ONLY way to run commands is inside a <cmd> block. "+
		"Do NOT use XML tags, brackets, or structured format. Example:\n"+
		"<cmd>\nls -la\n</cmd>\n<cmd>\ncat file.go\n</cmd>)", attempt)
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

// scanCommands extracts individual raw commands from <cmd>...</cmd> blocks in text.
// Uses depth-counting to handle nested <cmd> in command content correctly:
// - depth 0→1 = real open tag
// - depth 1→0 = real close tag
// - depth >1 = nested tag, treated as content
// Returns commands with leading/trailing whitespace trimmed.
// Text outside <cmd> blocks is ignored.
func scanCommands(text string) []string {
	var cmds []string
	depth := 0
	var buf strings.Builder

	for i := 0; i < len(text); {
		if depth == 0 {
			idx := strings.Index(text[i:], CmdBlockOpen)
			if idx == -1 {
				break
			}
			i += idx + len(CmdBlockOpen)
			depth = 1
			buf.Reset()
			continue
		}

		if depth >= 1 {
			// Look for </cmd>
			closeIdx := strings.Index(text[i:], CmdBlockClose)
			if closeIdx == -1 {
				break
			}

			// Check for <cmd> before this </cmd>
			nestedIdx := strings.Index(text[i:], CmdBlockOpen)

			if nestedIdx != -1 && nestedIdx < closeIdx {
				// Nested <cmd> found first — copy content before it AND the nested block
				nestedCloseIdx := strings.Index(text[i+nestedIdx:], CmdBlockClose)
				if nestedCloseIdx == -1 {
					// Malformed: nested <cmd> with no close, treat rest as content
					buf.WriteString(text[i:])
					break
				}
				// Copy: content before nested <cmd> + nested <cmd>...</cmd>
				buf.WriteString(text[i : i+nestedIdx+nestedCloseIdx+len(CmdBlockClose)])
				i += nestedIdx + nestedCloseIdx + len(CmdBlockClose)
				// Heredoc case: nested </cmd> was the last tag (i >= len).
				// Strip the nested </cmd> and emit.
				if i >= len(text) {
					emitLen := buf.Len() - len(CmdBlockClose)
					cmd := strings.TrimSpace(buf.String()[:emitLen])
					if cmd != "" {
						cmds = append(cmds, cmd)
					}
					depth = 0
					continue
				}
				// Otherwise: content or outer </cmd> follows. Continue scanning.
				// The outer </cmd> will be found in a subsequent iteration at depth 1.
				continue
			}

			// Real close tag — emit command
			if depth == 1 {
				buf.WriteString(text[i : i+closeIdx])
				cmd := strings.TrimSpace(buf.String())
				if cmd != "" {
					cmds = append(cmds, cmd)
				}
				i += closeIdx + len(CmdBlockClose)
				depth = 0
				continue
			}

			// depth >= 2: nested </cmd> — decrement depth and skip past it
			i += closeIdx + len(CmdBlockClose)
			depth--
			continue
		}
	}
	return cmds
}

// ParseMessage parses a raw message that may contain <cmd>...</cmd> blocks.
// Returns:
//   - commands: extracted command contents (for execution)
//   - prose: message with all <cmd> blocks stripped (for display to human)
//
// Nested <cmd> inside blocks are treated as content and preserved.
func ParseMessage(text string) (commands []string, prose string) {
	var cmdBuf strings.Builder
	var proseBuf strings.Builder
	depth := 0

	for i := 0; i < len(text); {
		if depth == 0 {
			// Look for next <cmd>
			idx := strings.Index(text[i:], CmdBlockOpen)
			if idx == -1 {
				proseBuf.WriteString(text[i:])
				break
			}
			proseBuf.WriteString(text[i : i+idx])
			i += idx + len(CmdBlockOpen)
			depth = 1
			cmdBuf.Reset()
			continue
		}

		// At depth >= 1: look for </cmd>
		closeIdx := strings.Index(text[i:], CmdBlockClose)
		if closeIdx == -1 {
			break
		}

		// Check for nested <cmd> before this </cmd>
		nestedIdx := strings.Index(text[i:], CmdBlockOpen)

		if nestedIdx != -1 && nestedIdx < closeIdx {
			// Nested <cmd> found first — it's content
			nestedCloseIdx := strings.Index(text[i+nestedIdx:], CmdBlockClose)
			if nestedCloseIdx == -1 {
				cmdBuf.WriteString(text[i:])
				break
			}
			// Copy content before nested <cmd> + the nested block
			cmdBuf.WriteString(text[i : i+nestedIdx+nestedCloseIdx+len(CmdBlockClose)])
			i += nestedIdx + nestedCloseIdx + len(CmdBlockClose)

			// Heredoc case: nested </cmd> was the last </cmd> in the string
			// Check if there are more </cmd> tags after current position
			remainingAfterNested := text[i:]
			nextCloseIdx := strings.Index(remainingAfterNested, CmdBlockClose)
			if nextCloseIdx == -1 {
				// No more </cmd> — this nested close is the outer close
				emitLen := cmdBuf.Len() - len(CmdBlockClose)
				cmd := strings.TrimSpace(cmdBuf.String()[:emitLen])
				if cmd != "" {
					commands = append(commands, cmd)
				}
				proseBuf.WriteString(remainingAfterNested)
				return commands, proseBuf.String()
			}
			// More </cmd> ahead — outer close is different, continue scanning
			continue
		}

		// Found </cmd> before any nested <cmd>
		if depth == 1 {
			cmdBuf.WriteString(text[i : i+closeIdx])
			cmd := strings.TrimSpace(cmdBuf.String())
			if cmd != "" {
				commands = append(commands, cmd)
			}
			i += closeIdx + len(CmdBlockClose)
			depth = 0
			continue
		}

		// depth >= 2: nested </cmd> — skip it
		i += closeIdx + len(CmdBlockClose)
		depth--
		continue
	}

	return commands, proseBuf.String()
}

// executeCommands sends each command to temenos individually and formats results.
// Fires OnCommandResult callback per command result.
//
// Transport errors are NOT surfaced via OnCommandResult — the error appears
// in the output text sent back to the LLM so it can retry.
func executeCommands(ctx context.Context, cfg Config, cbs Callbacks, cmds []string) []string {
	var outputParts []string
	for _, cmd := range cmds {
		if ctx.Err() != nil {
			break
		}

		resp, err := cfg.Temenos.Run(ctx, RunRequest{
			Command:      cmd,
			Env:          cfg.SandboxEnv,
			AllowedPaths: cfg.AllowedPaths,
		})
		if err != nil {
			slog.Error("temenos Run failure", "error", err)
			outputParts = append(outputParts, fmt.Sprintf("execution error: %v", err))
			continue
		}

		output := resp.Stdout
		if resp.Stderr != "" {
			output += "\nSTDERR:\n" + resp.Stderr
		}
		if output == "" {
			output = "(no output)"
		}
		if cbs.OnCommandResult != nil {
			cbs.OnCommandResult(cmd, output, resp.ExitCode)
		}

		formatted := cmd + "\n" + output
		if resp.ExitCode != 0 && resp.ExitCode != -1 {
			formatted += fmt.Sprintf("\n(exit code: %d)", resp.ExitCode)
		}
		outputParts = append(outputParts, formatted)
	}
	return outputParts
}

// streamFilter sits between the LLM stream and OnDelta, filtering XML tool_call
// markers (suppress + retry), bracket tool_call markers (suppress + retry), and
// strip markers like &#x3c;result&#x3e; (tag-only removal — inter-tag content
// passes through unchanged).
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

	// Tier 2: stripMarkers — silently remove without triggering retry.
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
// is buffered until </cmd> is seen, then emitted as a single complete
// <cmd>...</cmd> chunk. This lets consumers (TUI, iOS) receive and render
// command blocks atomically. buf holds either a partial <cmd> prefix (not in
// block) or accumulated block content waiting for </cmd>.
type cmdBlockFilter struct {
	delegate func(string)
	buf      strings.Builder
	inBlock  bool
}

func (f *cmdBlockFilter) Write(delta string) {
	// Prepend any buffered content from the previous call so we can match tags
	// that span delta boundaries. buf holds either a partial <cmd> prefix (not in
	// block) or all accumulated block content waiting for </cmd>.
	if f.buf.Len() > 0 {
		delta = f.buf.String() + delta
		f.buf.Reset()
	}

	for len(delta) > 0 {
		if f.inBlock {
			// Inside <cmd> block — look for </cmd>
			idx := strings.Index(delta, CmdBlockClose)
			if idx == -1 {
				// No closing tag yet — buffer all content; the buf prepend at the top
				// of Write will concatenate it with the next delta so </cmd> can be found.
				f.buf.WriteString(delta)
				return
			}
			// Found closing tag — emit complete block as one chunk, continue with remainder as prose
			f.delegate(CmdBlockOpen + delta[:idx] + CmdBlockClose)
			f.inBlock = false
			delta = delta[idx+len(CmdBlockClose):]
			continue
		}
		// Outside block — look for <cmd>
		idx := strings.Index(delta, CmdBlockOpen)
		if idx == -1 {
			// Check for partial <cmd> prefix at end of delta
			for plen := min(len(CmdBlockOpen)-1, len(delta)); plen > 0; plen-- {
				if strings.HasSuffix(delta, CmdBlockOpen[:plen]) {
					f.delegate(delta[:len(delta)-plen])
					f.buf.WriteString(delta[len(delta)-plen:])
					return
				}
			}
			f.delegate(delta)
			return
		}
		// Pass through text before <cmd>, then enter block mode (content buffered until </cmd>)
		if idx > 0 {
			f.delegate(delta[:idx])
		}
		f.inBlock = true
		delta = delta[idx+len(CmdBlockOpen):]
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

// firstNonNeg returns the first non-negative of a and b, or -1 if both are negative.
func firstNonNeg(a, b int) int {
	if a == -1 {
		return b
	}
	if b == -1 {
		return a
	}
	if a < b {
		return a
	}
	return b
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

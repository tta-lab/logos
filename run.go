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
	// RunBlockRequest is the request payload for batch block execution.
	RunBlockRequest = client.RunBlockRequest
	// RunBlockResponse is the response from batch block execution.
	RunBlockResponse = client.RunBlockResponse
	// CommandResult is one command's execution result within a block.
	CommandResult = client.CommandResult
)

// BlockRunner executes a block of commands in the sandbox.
// *client.Client satisfies this interface automatically.
type BlockRunner interface {
	RunBlock(ctx context.Context, req RunBlockRequest) (*RunBlockResponse, error)
}

// Config holds everything needed to run one agent loop iteration.
type Config struct {
	Provider     fantasy.Provider
	Model        string
	SystemPrompt string
	MaxSteps     int // 0 means use default (DefaultMaxSteps)
	MaxTokens    int // 0 means use default (DefaultMaxTokens)
	Temenos      BlockRunner
	SandboxEnv   map[string]string // env vars passed to temenos per-request
	// AllowedPaths lists filesystem paths accessible during command execution.
	// Path validation (non-empty, absolute) is enforced by the temenos daemon.
	AllowedPaths []AllowedPath
	// Prefix is the command prefix the LLM uses (e.g. "§ ").
	// Passed to RunBlock so temenos can parse commands. Defaults to "§ ".
	Prefix string
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
	// Fires once per CommandResult from the batch response.
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

		preText, blocks := scanBlocks(fullText)

		if len(blocks) == 0 {
			// Final answer — return
			steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
			responseText.WriteString(fullText)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		// Has command blocks — execute via temenos RunBlock
		steps = append(steps, newAssistantStep(fullText, reasoning, reasoningSig))
		responseText.WriteString(preText)

		cmdOutputs := executeBlocks(ctx, cfg, cbs, blocks)
		if len(cmdOutputs) == 0 {
			// ctx was already cancelled before any block ran; next streamOneTurn will
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

// executeBlocks sends each block to temenos via RunBlock and formats results.
// Fires OnCommandResult callback per command result from the batch.
//
// Design note: transport errors (RunBlock call fails entirely) are NOT surfaced
// via OnCommandResult. In the batch model, a transport error means we got zero
// results — there's no meaningful command/exitCode to report. The error appears
// in the output text sent back to the LLM so it can retry.
func executeBlocks(ctx context.Context, cfg Config, cbs Callbacks, blocks []string) []string {
	prefix := cfg.Prefix
	if prefix == "" {
		prefix = "§ " // default for backward compat
	}

	var outputParts []string
	for _, block := range blocks {
		if ctx.Err() != nil {
			break
		}

		resp, err := cfg.Temenos.RunBlock(ctx, RunBlockRequest{
			Block:        block,
			Prefix:       prefix,
			Env:          cfg.SandboxEnv,
			AllowedPaths: cfg.AllowedPaths,
		})
		if err != nil {
			slog.Error("temenos RunBlock failure", "error", err)
			outputParts = append(outputParts, fmt.Sprintf("execution error: %v", err))
			continue
		}

		if len(resp.Results) == 0 {
			slog.Warn("logos: RunBlock returned no results; possible prefix mismatch", "prefix", prefix)
			outputParts = append(outputParts, fmt.Sprintf("(block produced no results with prefix %q)", prefix))
			continue
		}

		for _, r := range resp.Results {
			output := r.Stdout
			if r.Stderr != "" {
				output += "\nSTDERR:\n" + r.Stderr
			}
			if output == "" {
				output = "(no output)"
			}
			if cbs.OnCommandResult != nil {
				cbs.OnCommandResult(r.Command, output, r.ExitCode)
			}

			formatted := prefix + r.Command + "\n" + output
			if r.ExitCode != 0 && r.ExitCode != -1 {
				formatted += fmt.Sprintf("\n(exit code: %d)", r.ExitCode)
			}
			outputParts = append(outputParts, formatted)
		}
	}
	return outputParts
}

// scanBlocks extracts raw content from <cmd>...</cmd> blocks in text.
// Returns the text before the first block and a slice of raw block contents.
// Bare text outside blocks is treated as prose.
func scanBlocks(text string) (preText string, blocks []string) {
	firstBlockIdx := strings.Index(text, "<cmd>")
	if firstBlockIdx == -1 {
		return text, nil
	}
	preText = text[:firstBlockIdx]
	remaining := text[firstBlockIdx:] // start from first block, avoiding a redundant search

	for {
		openIdx := strings.Index(remaining, "<cmd>")
		if openIdx == -1 {
			break
		}
		closeIdx := strings.Index(remaining[openIdx:], "</cmd>")
		var blockContent string
		if closeIdx == -1 {
			blockContent = remaining[openIdx+len("<cmd>"):]
			remaining = ""
			slog.Warn("logos: unclosed <cmd> block, sending partial block to temenos")
		} else {
			blockContent = remaining[openIdx+len("<cmd>") : openIdx+closeIdx]
			remaining = remaining[openIdx+closeIdx+len("</cmd>"):]
		}

		if blockContent != "" {
			blocks = append(blocks, blockContent)
		}

		if remaining == "" {
			break
		}
	}
	return preText, blocks
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
			idx := strings.Index(delta, "</cmd>")
			if idx == -1 {
				// No closing tag yet — buffer all content; the buf prepend at the top
				// of Write will concatenate it with the next delta so </cmd> can be found.
				f.buf.WriteString(delta)
				return
			}
			// Found closing tag — emit complete block as one chunk, continue with remainder as prose
			f.delegate("<cmd>" + delta[:idx] + "</cmd>")
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
		// Pass through text before <cmd>, then enter block mode (content buffered until </cmd>)
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

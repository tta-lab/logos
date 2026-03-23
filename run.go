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
	StepRoleAssistant StepRole = "assistant" // LLM turn with no commands (final answer)
	StepRoleUser      StepRole = "user"      // human input
	StepRoleCommand   StepRole = "command"   // LLM turn that contains § commands
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
		fullText, toolCallDetected, streamErr := streamOneTurn(ctx, model, messages, maxTokens, func(text string) {
			if cbs.OnDelta != nil {
				cbs.OnDelta(text)
			}
		})
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
			steps = append(steps, StepMessage{Role: StepRoleAssistant, Content: fullText, Timestamp: time.Now()})
			steps = append(steps, StepMessage{Role: StepRoleResult, Content: directive, Timestamp: time.Now()})
			messages = append(messages, newAssistantMessage(fullText), fantasy.NewUserMessage(directive))
			continue
		}

		preText, cmds := scanAllCommands(fullText)

		if len(cmds) == 0 {
			// Final answer — return
			steps = append(steps, StepMessage{Role: StepRoleAssistant, Content: fullText, Timestamp: time.Now().UTC()})
			responseText.WriteString(fullText)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		// Has commands — execute all sequentially
		steps = append(steps, StepMessage{Role: StepRoleCommand, Content: fullText, Timestamp: time.Now().UTC()})
		responseText.WriteString(preText)

		// Execute each command via runAndNotify (fires OnCommandResult callback),
		// then format output for LLM with exit code suffix.
		userContent := strings.Join(executeCommands(ctx, cfg, cbs, cmds), "\n")
		if userContent == "" {
			// ctx was already cancelled before any command ran; next streamOneTurn will
			// return the context error and propagate it cleanly.
			continue
		}

		steps = append(steps, StepMessage{Role: StepRoleResult, Content: userContent, Timestamp: time.Now().UTC()})
		messages = append(messages, newAssistantMessage(fullText), fantasy.NewUserMessage(userContent))
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
			"To run a command, write a plain-text line starting with § — e.g. § ls -la)"
	}
	return fmt.Sprintf("(Unprocessed: tool call format detected again (attempt %d). "+
		"There is NO tool calling API. The ONLY way to run commands is a line starting with § prefix. "+
		"Do NOT use any XML tags, bracket wrappers, or structured format. Example:\n§ ls -la\n§ cat file.go)", attempt)
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

// captureHeredoc captures the full heredoc block for a command starting at lines[startIdx].
// Returns the updated Command with Args/Raw set to the full block, and true if the
// closing delimiter was found. If not found, returns the original Command unchanged.
func captureHeredoc(lines []string, startIdx int, c Command, delim string) (Command, bool) {
	for j := startIdx + 1; j < len(lines); j++ {
		if isHeredocClose(lines[j], delim) {
			fullBlock := strings.Join(lines[startIdx:j+1], "\n")
			c.Args = strings.TrimPrefix(strings.TrimSpace(fullBlock), CommandPrefix)
			c.Raw = fullBlock
			return c, true
		}
	}
	return c, false
}

// scanForCommand finds the first § command in text.
// If the command contains a heredoc (<<DELIM), captures lines through the
// closing delimiter. Otherwise captures only the § line.
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

		if delim, hasHeredoc := heredocDelimiter(c.Args); hasHeredoc {
			if captured, ok := captureHeredoc(lines, i, c, delim); ok {
				return preText, captured, true
			}
			// No closing delimiter found — fall through to single-line capture
		}

		return preText, c, true
	}
	return text, Command{}, false
}

// scanAllCommands extracts all § commands from text, in order.
// Returns the text before the first command and a slice of commands.
// Heredoc bodies are captured as part of their parent command (not split).
// Unclosed heredocs fall back to single-line capture with a warning.
func scanAllCommands(text string) (preText string, cmds []Command) {
	lines := strings.Split(text, "\n")
	var heredocDelim string
	firstCmdIdx := -1

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

		if firstCmdIdx == -1 {
			firstCmdIdx = i
		}

		if delim, hasHeredoc := heredocDelimiter(c.Args); hasHeredoc {
			if captured, ok := captureHeredoc(lines, i, c, delim); ok {
				c = captured
				heredocDelim = delim
			} else {
				slog.Warn("logos: unclosed heredoc — falling back to single-line capture",
					"args", c.Args, "delimiter", delim)
			}
		}

		cmds = append(cmds, c)
	}

	if firstCmdIdx == -1 {
		return text, nil
	}
	preText = strings.Join(lines[:firstCmdIdx], "\n")
	if preText != "" {
		preText += "\n"
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

// cmdLineFilter buffers streaming text at line boundaries to suppress § command
// lines before they reach the delegate. Non-command text passes through immediately
// once the line prefix is determined.
//
// The § prefix (CommandPrefix from parse.go) is 3 bytes in UTF-8 (§ = 0xC2A7, then
// space = 0x20). We buffer at most the first few bytes of each line to make the decision.
type cmdLineFilter struct {
	delegate     func(string) // receives non-command text (typically streamFilter.Write)
	lineBuf      strings.Builder
	suppressing  bool   // true once current line confirmed as § command
	heredocDelim string // non-empty when inside a heredoc body (suppressing until close)
}

// Write processes a streaming delta, suppressing § command lines and heredoc bodies.
func (f *cmdLineFilter) Write(delta string) {
	for i := 0; i < len(delta); {
		// Heredoc body suppression — suppress all lines until closing delimiter.
		if f.heredocDelim != "" {
			nl := strings.IndexByte(delta[i:], '\n')
			if nl == -1 {
				// No newline — buffer for heredoc close check.
				f.lineBuf.WriteString(delta[i:])
				return
			}
			line := delta[i : i+nl]
			f.lineBuf.WriteString(line)
			fullLine := f.lineBuf.String()
			f.lineBuf.Reset() // reset before next iteration so lines don't accumulate
			i += nl + 1
			if isHeredocClose(fullLine, f.heredocDelim) {
				f.heredocDelim = ""
			}
			// Either way, suppress the line (body or closing delimiter).
			continue
		}

		if f.suppressing {
			// Scan for newline to end suppression of current § line.
			nl := strings.IndexByte(delta[i:], '\n')
			if nl == -1 {
				return // rest of delta is still the suppressed line
			}
			// Newline found — § line is done. Don't emit the \n either.
			f.suppressing = false
			i += nl + 1
			continue
		}

		nl := strings.IndexByte(delta[i:], '\n')
		if nl == -1 {
			// No newline — buffer remainder for prefix check.
			f.lineBuf.WriteString(delta[i:])
			// Check if we can already determine the line type.
			// Trim leading whitespace (matching ParseCommand behaviour) and only
			// decide once the trimmed content is long enough to be unambiguous.
			trimmed := strings.TrimSpace(f.lineBuf.String())
			if strings.HasPrefix(trimmed, CommandPrefix) {
				f.suppressing = true
				f.lineBuf.Reset()
			} else if len(trimmed) >= len(CommandPrefix) {
				// Trimmed content is long enough and not a command — flush buffer to delegate.
				f.delegate(f.lineBuf.String())
				f.lineBuf.Reset()
			}
			return
		}

		// Newline found at position nl (relative to delta[i:]).
		line := delta[i : i+nl]
		f.lineBuf.WriteString(line)
		fullLine := f.lineBuf.String()

		if strings.HasPrefix(strings.TrimSpace(fullLine), CommandPrefix) {
			// Check for heredoc in the complete command line.
			if delim, ok := heredocDelimiter(fullLine); ok {
				f.heredocDelim = delim
			}
			f.suppressing = false // defensive reset; suppressing is always false here (callers that set it exit via return)
			// Suppress entire line including \n.
		} else {
			f.delegate(fullLine + "\n")
		}
		f.lineBuf.Reset()
		i += nl + 1
	}
}

// Flush emits any buffered partial line that isn't a § command or heredoc content.
// Called when the stream ends (deferred in streamOneTurn).
func (f *cmdLineFilter) Flush() {
	if f.lineBuf.Len() > 0 && f.heredocDelim == "" && !f.suppressing &&
		!strings.HasPrefix(strings.TrimSpace(f.lineBuf.String()), CommandPrefix) {
		f.delegate(f.lineBuf.String())
	}
	f.lineBuf.Reset()
	f.suppressing = false
	f.heredocDelim = ""
}

// streamOneTurn streams a single LLM response (no tools).
// Returns the full unfiltered text, whether a tool call hallucination was detected, and any error.
// The filter suppresses tool call output from OnDelta and strips think tags.
// filter.Flush() is deferred so buffered content is always emitted, even on error.
func streamOneTurn(
	ctx context.Context,
	model fantasy.LanguageModel,
	messages []fantasy.Message,
	maxTokens int64,
	onDelta func(string),
) (string, bool, error) {
	stream, err := model.Stream(ctx, fantasy.Call{
		Prompt:          fantasy.Prompt(messages),
		MaxOutputTokens: &maxTokens,
	})
	if err != nil {
		return "", false, err
	}

	xmlFilter := &streamFilter{delegate: onDelta}
	cmdFilter := &cmdLineFilter{delegate: xmlFilter.Write}
	// Order matters: cmdFilter.Flush() must run BEFORE xmlFilter.Flush().
	// cmdFilter.Flush() may emit a buffered partial line to xmlFilter.Write,
	// so xmlFilter must still be accepting input. If reversed, that content is lost.
	defer func() { cmdFilter.Flush(); xmlFilter.Flush() }()
	var fullText strings.Builder
	for part := range stream {
		switch part.Type {
		case fantasy.StreamPartTypeTextDelta:
			fullText.WriteString(part.Delta)
			cmdFilter.Write(part.Delta)
		case fantasy.StreamPartTypeError:
			if part.Error != nil {
				return fullText.String(), xmlFilter.toolCallDetected, part.Error
			}
		}
	}
	return fullText.String(), xmlFilter.toolCallDetected, nil
}

// newAssistantMessage wraps text as a fantasy assistant message.
func newAssistantMessage(text string) fantasy.Message {
	return fantasy.Message{
		Role:    fantasy.MessageRoleAssistant,
		Content: []fantasy.MessagePart{fantasy.TextPart{Text: text}},
	}
}

package logos

import (
	"context"
	"errors"
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

// StopReason describes why a Turn (single Run() invocation) terminated.
// Used by the OnTurnEnd callback. Each value corresponds to exactly one
// exit path in Run().
type StopReason string

const (
	StopReasonFinal              StopReason = "final"               // model returned final answer (no <cmd>)
	StopReasonCanceled           StopReason = "canceled"            // ctx canceled (in stream OR after)
	StopReasonError              StopReason = "error"               // stream error (non-cancellation)
	StopReasonHallucinationLimit StopReason = "hallucination_limit" // MaxHallucinationRetries exceeded
	StopReasonMaxSteps           StopReason = "max_steps"           // MaxSteps exhausted
)

// Re-exported from temenos/client so consumers don't import temenos directly.
// commandRunner executes a single command.
// Both *client.Client and *localRunner satisfy this interface.
type commandRunner interface {
	Run(ctx context.Context, req client.RunRequest) (*client.RunResponse, error)
}

// Config holds everything needed to run one agent loop iteration.
type Config struct {
	Provider     fantasy.Provider
	Model        string
	SystemPrompt string
	MaxSteps     int               // 0 means use default (DefaultMaxSteps)
	MaxTokens    int               // 0 means use default (DefaultMaxTokens)
	Sandbox      bool              // true = require temenos sandbox; false = use local exec
	SandboxAddr  string            // temenos socket/address; empty = env fallback chain
	SandboxEnv   map[string]string // env vars passed to sandbox per-request
	// AllowedPaths lists filesystem paths accessible during command execution.
	// Path validation (non-empty, absolute) is enforced by the temenos daemon.
	// Note: localRunner (Sandbox=false) uses AllowedPaths[0].Path as the working
	// directory; additional entries are ignored (no RO enforcement).
	AllowedPaths []client.AllowedPath

	// runner is resolved by resolveRunner() before use. testRunner is for unit
	// tests in the same package (injected via withTestRunner helper).
	runner     commandRunner
	testRunner commandRunner
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
// Per-step token usage is reported via Callbacks.OnStepUsage.
type RunResult struct {
	Response string        // final text response (accumulated assistant text)
	Steps    []StepMessage // all messages generated (for persistence by caller)
}

// Callbacks holds optional streaming callbacks for the agent loop.
// All fields are nil-safe — unset callbacks are simply not called.
//
// Vocabulary:
//
//	Step — one iteration of the loop (one model call + optional cmd execution).
//	       Multiple steps per Turn.
//	Turn — one full agent response cycle. Exactly one Run() call.
//
// Firing order within a Turn:
//  1. OnStepStart(0)
//  2. OnDelta / OnReasoningDelta — interleaved during streaming
//  3. OnReasoningSignature       — once when reasoning block finalises
//  4. OnStepUsage(0, usage, meta) — once when the model stream finishes
//  5. OnCommandResult            — if a cmd block executed
//  6. OnStepEnd(0)
//     ... (steps repeat 1-6 with stepIdx incrementing)
//     N. OnTurnEnd(reason) — exactly once at Run() exit
type Callbacks struct {
	// OnStepStart fires before each model call. stepIdx is the zero-based step number.
	OnStepStart func(stepIdx int)
	// OnStepEnd fires after each step completes. stepIdx is the zero-based step number.
	OnStepEnd func(stepIdx int)
	// OnDelta is called with each text delta as the LLM streams its response.
	OnDelta func(text string)
	// OnReasoningDelta streams thinking content live as it arrives.
	OnReasoningDelta func(text string)
	// OnReasoningSignature fires once per step when the reasoning block is finalised.
	OnReasoningSignature func(signature string)
	// OnStepUsage fires once per step immediately after the model stream finishes,
	// before any command is executed. stepIdx is the zero-based step number.
	OnStepUsage func(stepIdx int, usage fantasy.Usage, meta fantasy.ProviderMetadata)
	// OnCommandResult is called after a command executes (or is blocked/runner errors).
	// command: the command that was run.
	// output: formatOneResult output on success; directive text on blocked commands (exitCode=-2);
	//          formatOneResult output with Err set on runner errors (exitCode=-1).
	// exitCode: 0 on success, -1 on runner error, -2 on blocked command.
	OnCommandResult func(command string, output string, exitCode int)
	// OnTurnEnd fires once when Run() exits. reason is a StopReason constant.
	OnTurnEnd func(reason StopReason)
}

// emitStepEnd fires the OnStepEnd callback with the step index, nil-safe.
func (cbs Callbacks) emitStepEnd(stepIdx int) {
	if cbs.OnStepEnd != nil {
		cbs.OnStepEnd(stepIdx)
	}
}

// emitTurnEnd fires the OnTurnEnd callback with a stop reason, nil-safe.
func (cbs Callbacks) emitTurnEnd(reason StopReason) {
	if cbs.OnTurnEnd != nil {
		cbs.OnTurnEnd(reason)
	}
}

// Run executes the agent loop: prompt → LLM → <cmd> blocks → repeat.
// Stateless — the caller handles conversation persistence.
// Complexity is inherent to switch + stream loop routing.
//
//nolint:gocyclo
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
	var err error
	cfg.runner, err = resolveRunner(&cfg)
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
		steps              []StepMessage
		responseText       strings.Builder
		hallucinationCount int
	)
	for step := 0; step < maxSteps; step++ {
		if cbs.OnStepStart != nil {
			cbs.OnStepStart(step)
		}
		onDelta := func(text string) {
			if cbs.OnDelta != nil {
				cbs.OnDelta(text)
			}
		}
		text, reasoning, reasoningSig, toolCallDetected, cmdResults, streamErr :=
			streamOneTurn(ctx, model, messages, maxTokens, cfg, cbs, step, onDelta)
		if streamErr != nil {
			// Surface cancellation directly so callers can errors.Is(err, context.Canceled).
			if isCancellation(streamErr) {
				cbs.emitStepEnd(step)
				cbs.emitTurnEnd(StopReasonCanceled)
				return &RunResult{
					Response: responseText.String(),
					Steps:    steps,
				}, streamErr
			}
			cbs.emitStepEnd(step)
			cbs.emitTurnEnd(StopReasonError)
			return nil, fmt.Errorf("stream turn %d: %w", step, streamErr)
		}
		// Defense in depth: even if streamOneTurn returned nil error (e.g. because a
		// worker exited on cancel without pushing a result), check ctx and bail out
		// before another LLM round-trip.
		if err := ctx.Err(); err != nil {
			cbs.emitStepEnd(step)
			cbs.emitTurnEnd(StopReasonCanceled)
			return &RunResult{
				Response: responseText.String(),
				Steps:    steps,
			}, err
		}

		// Check tool call hallucination BEFORE appending to Steps.
		if toolCallDetected {
			hallucinationCount++
			if hallucinationCount > MaxHallucinationRetries {
				cbs.emitStepEnd(step)
				cbs.emitTurnEnd(StopReasonHallucinationLimit)
				return nil, fmt.Errorf("logos: tool call hallucination not resolved after %d retries", MaxHallucinationRetries)
			}
			directive := hallucinationDirective(hallucinationCount)
			slog.Warn("tool call hallucination detected", "step", step, "attempt", hallucinationCount)
			// Record both the wrong output and feedback in Steps (for conversation restore).
			steps = append(steps, newAssistantStep(text, reasoning, reasoningSig))
			steps = append(steps, StepMessage{Role: StepRoleResult, Content: directive, Timestamp: time.Now().UTC()})
			aMsg := newAssistantMessage(text, reasoning, reasoningSig)
			messages = append(messages, aMsg, fantasy.NewUserMessage(directive))
			cbs.emitStepEnd(step)
			// turn continues; OnTurnEnd fires only at Run() exit (not per step).
			continue
		}

		// text is already the persist text (cmd block only for cmd steps; reply text for reply steps).
		steps = append(steps, newAssistantStep(text, reasoning, reasoningSig))
		responseText.WriteString(text)

		if len(cmdResults) == 0 {
			// Final answer — return
			cbs.emitStepEnd(step)
			cbs.emitTurnEnd(StopReasonFinal)
			return &RunResult{Response: responseText.String(), Steps: steps}, nil
		}

		userContent := "<result>\n" + strings.Join(cmdResults, "\n") + "\n</result>"

		steps = append(steps, StepMessage{Role: StepRoleResult, Content: userContent, Timestamp: time.Now().UTC()})
		aMsg2 := newAssistantMessage(text, reasoning, reasoningSig)
		messages = append(messages, aMsg2, fantasy.NewUserMessage(userContent))
		cbs.emitStepEnd(step)
	}

	cbs.emitTurnEnd(StopReasonMaxSteps)
	return &RunResult{
		Response: responseText.String(),
		Steps:    steps,
	}, fmt.Errorf("logos: max steps (%d) reached", maxSteps)
}

// StepsToMessages converts StepMessages back to fantasy.Messages for
// conversation round-tripping. Assistant steps produce ReasoningPart-first
// ordering (required by Anthropic). Result steps pass through the
// pre-formatted <result> envelope as a user message.
//
// Used by fn-agent and lenos to rehydrate a conversation history from
// persisted StepMessages when resuming an agent session.
func StepsToMessages(steps []StepMessage) []fantasy.Message {
	var msgs []fantasy.Message
	for _, s := range steps {
		switch s.Role {
		case StepRoleAssistant:
			msgs = append(msgs, newAssistantMessage(s.Content, s.Reasoning, s.ReasoningSignature))
		case StepRoleResult:
			msgs = append(msgs, fantasy.NewUserMessage(s.Content))
		}
	}
	return msgs
}

// isCancellation reports whether err is or wraps a context cancellation or deadline.
func isCancellation(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
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
		"<cmd>\nls -la\n</cmd>)", attempt)
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

// proseFilter filters hallucinated tool_call patterns from streaming output.
// Passes clean prose to delegate, suppresses content with tool_call markers.
type proseFilter struct {
	delegate         func(string)
	buf              strings.Builder
	buffering        bool
	toolCallDetected bool
}

// Write processes a streaming delta. Fast path: no '<' or '[' → pass through immediately.
// Otherwise buffers from the first trigger character and checks for known markers.
func (f *proseFilter) Write(delta string) {
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
func (f *proseFilter) checkBuffer() {
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
func (f *proseFilter) Flush() {
	if f.buf.Len() > 0 && !f.toolCallDetected {
		f.delegate(f.buf.String())
	}
	f.buf.Reset()
	f.buffering = false
}

// cmdBlockBuffer assembles <cmd>...</cmd> blocks from streaming deltas.
// Routes clean prose to proseDelegate and complete blocks to exec.
// Consumers can use exec=nil to get clean prose without execution.
type cmdBlockBuffer struct {
	proseDelegate func(string)       // For prose → hallucinationFilter → onDelta
	exec          func(block string) // For complete blocks → executor
	buf           strings.Builder
	depth         int // nested block depth (>0 means inside block)
}

func (f *cmdBlockBuffer) Write(delta string) {
	if f.buf.Len() > 0 {
		delta = f.buf.String() + delta
		f.buf.Reset()
	}

	for len(delta) > 0 {
		if f.depth > 0 {
			delta = f.writeInsideBlock(delta)
		} else {
			delta = f.writeOutsideBlock(delta)
		}
	}
}

func (f *cmdBlockBuffer) writeInsideBlock(delta string) string {
	nextOpen := strings.Index(delta, CmdBlockOpen)
	nextClose := strings.Index(delta, CmdBlockClose)
	if nextOpen == -1 && nextClose == -1 {
		f.buf.WriteString(delta)
		return ""
	}
	if nextOpen != -1 && (nextClose == -1 || nextOpen < nextClose) {
		f.buf.WriteString(delta[:nextOpen+len(CmdBlockOpen)])
		delta = delta[nextOpen+len(CmdBlockOpen):]
		f.depth++
		return delta
	}
	before := delta[:nextClose]
	remain := delta[nextClose+len(CmdBlockClose):]
	f.buf.WriteString(before)
	f.depth--
	if f.depth == 0 {
		block := CmdBlockOpen + f.buf.String() + CmdBlockClose
		f.emitBlock(block)
		f.buf.Reset()
		return remain
	}
	f.buf.WriteString(CmdBlockClose)
	return remain
}

func (f *cmdBlockBuffer) writeOutsideBlock(delta string) string {
	idx := strings.Index(delta, CmdBlockOpen)
	if idx == -1 {
		delta, _ = f.flushPartial(delta)
		if delta != "" && f.proseDelegate != nil {
			f.proseDelegate(delta)
		}
		return ""
	}
	if idx > 0 && f.proseDelegate != nil {
		f.proseDelegate(delta[:idx])
	}
	f.depth = 1
	return delta[idx+len(CmdBlockOpen):]
}

func (f *cmdBlockBuffer) flushPartial(delta string) (string, bool) {
	for plen := min(len(CmdBlockOpen)-1, len(delta)); plen > 0; plen-- {
		if strings.HasSuffix(delta, CmdBlockOpen[:plen]) {
			prose := delta[:len(delta)-plen]
			partial := delta[len(delta)-plen:]
			if f.proseDelegate != nil && prose != "" {
				f.proseDelegate(prose)
			}
			f.buf.WriteString(partial)
			return "", true
		}
	}
	return delta, false
}

func (f *cmdBlockBuffer) emitBlock(block string) {
	if f.proseDelegate != nil {
		f.proseDelegate(block)
	}
	if f.exec != nil {
		f.exec(block)
	}
}

func (f *cmdBlockBuffer) Flush() {
	if f.buf.Len() > 0 {
		if f.depth > 0 {
			slog.Warn("cmdBlockBuffer: stream ended with unclosed <cmd> block", "buffered_len", f.buf.Len())
		} else {
			if f.proseDelegate != nil {
				f.proseDelegate(f.buf.String())
			}
		}
		f.buf.Reset()
		f.depth = 0
	}
}

// isPrefixOfAny returns true if s is a prefix of any known marker.
// Used to determine whether to keep buffering when we see a partial trigger sequence.
func isPrefixOfAny(s string) bool {
	return hasPrefixInSlice(s, xmlToolCallMarkers) ||
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
// Returns the persist text (cmd block for cmd steps; reply text for reply steps),
// reasoning content, reasoning signature, whether a tool call hallucination was
// detected, command results, and any error.
// Reasoning fields are empty for providers that don't emit thinking blocks.
// cmdBlockBuffer.Flush() is deferred so buffered content is always emitted, even on error.
// Complexity is inherent to switch + stream loop routing.
//
//nolint:gocyclo
func streamOneTurn(
	ctx context.Context,
	model fantasy.LanguageModel,
	messages []fantasy.Message,
	maxTokens int64,
	cfg Config,
	cbs Callbacks,
	stepIdx int,
	onDelta func(string),
) (
	text string,
	reasoning string,
	reasoningSig string,
	hallucinated bool,
	cmdResults []string,
	err error,
) {
	stream, streamErr := model.Stream(ctx, fantasy.Call{
		Prompt:          fantasy.Prompt(messages),
		MaxOutputTokens: &maxTokens,
	})
	if streamErr != nil {
		return "", "", "", false, nil, streamErr
	}

	// Chain: hallucinationFilter → cmdBlockBuffer → onDelta
	// - hallucinationFilter catches tool_call hallucination
	// - cmdBlockBuffer routes clean prose to onDelta and complete blocks to exec
	hallucinationFilter := &proseFilter{delegate: func(s string) { onDelta(s) }}

	var (
		fullText      strings.Builder
		reasoningBuf  strings.Builder
		pendingCmd    string // at most one cmd
		capturedBlock string // full <cmd>...</cmd> block for the first cmd
		cmdSeen       bool   // suppress post-</cmd> text from onDelta
	)

	cmdFilter := &cmdBlockBuffer{
		proseDelegate: func(s string) {
			if cmdSeen {
				return
			}
			hallucinationFilter.Write(s)
		},
		exec: func(block string) {
			cmd := extractCmdFromBlock(block)
			if pendingCmd == "" {
				pendingCmd = cmd
				capturedBlock = block
			}
			cmdSeen = true
		},
	}
	// Order matters: cmdFilter.Flush() must run BEFORE hallucinationFilter.Flush().
	defer func() { cmdFilter.Flush(); hallucinationFilter.Flush() }()

	var (
		finishUsage fantasy.Usage
		finishMeta  fantasy.ProviderMetadata
	)

	for part := range stream {
		switch part.Type {
		case fantasy.StreamPartTypeTextDelta:
			fullText.WriteString(part.Delta)
			cmdFilter.Write(part.Delta)
		case fantasy.StreamPartTypeReasoningDelta:
			if part.Delta != "" {
				reasoningBuf.WriteString(part.Delta)
				if cbs.OnReasoningDelta != nil {
					cbs.OnReasoningDelta(part.Delta)
				}
			}
			// Signature arrives as a ReasoningDelta with empty Delta and ProviderMetadata.
			if part.ProviderMetadata != nil {
				if meta, ok := part.ProviderMetadata[anthropic.Name]; ok {
					if rm, ok := meta.(*anthropic.ReasoningOptionMetadata); ok && rm.Signature != "" {
						reasoningSig = rm.Signature
						if cbs.OnReasoningSignature != nil {
							cbs.OnReasoningSignature(rm.Signature)
						}
					}
				}
			}
		case fantasy.StreamPartTypeError:
			if part.Error != nil {
				// Fire OnStepUsage even on stream error if we received a Finish part.
				if (finishUsage != (fantasy.Usage{}) || finishMeta != nil) && cbs.OnStepUsage != nil {
					cbs.OnStepUsage(stepIdx, finishUsage, finishMeta)
				}
				return "", "", "", false, nil, part.Error
			}
		case fantasy.StreamPartTypeFinish:
			finishUsage = part.Usage
			finishMeta = part.ProviderMetadata
			if cbs.OnStepUsage != nil {
				cbs.OnStepUsage(stepIdx, finishUsage, finishMeta)
			}
		default:
			slog.Debug("streamOneTurn: unhandled stream part type", "type", part.Type)
		}
	}

	// Build persist text: cmd steps persist only the block; reply steps persist all text.
	rawText := fullText.String()
	var persistText string
	if pendingCmd != "" {
		persistText = capturedBlock
		// Warn on non-whitespace pre-cmd prose (streamed live but not persisted).
		if idx := strings.Index(rawText, CmdBlockOpen); idx > 0 {
			if pre := rawText[:idx]; strings.TrimSpace(pre) != "" {
				slog.Warn("logos: pre-cmd prose dropped from persistence", "len", len(pre))
			}
		}
		// Warn on non-whitespace post-cmd prose (not streamed to OnDelta, not persisted).
		if lastClose := strings.LastIndex(rawText, CmdBlockClose); lastClose >= 0 {
			if post := rawText[lastClose+len(CmdBlockClose):]; strings.TrimSpace(post) != "" {
				slog.Warn("logos: post-cmd prose dropped from persistence", "len", len(post))
			}
		}
	} else {
		persistText = rawText
	}

	// Execute single command (no goroutine pool).
	if pendingCmd == "" {
		return persistText, reasoningBuf.String(), reasoningSig,
			hallucinationFilter.toolCallDetected, nil, nil
	}

	return executeOneCommand(ctx, cfg, cbs, pendingCmd,
		persistText, reasoningBuf.String(), reasoningSig, hallucinationFilter.toolCallDetected)
}

// executeOneCommand runs a single command and returns results.
// Extracted from streamOneTurn to reduce cyclomatic complexity.
//
//nolint:unparam
func executeOneCommand(
	ctx context.Context,
	cfg Config,
	cbs Callbacks,
	pendingCmd string,
	persistText string,
	reasoningBuf string,
	reasoningSig string,
	hallucinated bool,
) (string, string, string, bool, []string, error) {
	if directive, ok := handleBlockedCommand(pendingCmd); ok {
		if cbs.OnCommandResult != nil {
			cbs.OnCommandResult(pendingCmd, directive, -2)
		}
		return persistText, reasoningBuf, reasoningSig, hallucinated,
			[]string{directive}, nil
	}

	resp, err := cfg.runner.Run(ctx, client.RunRequest{
		Command:      pendingCmd,
		Env:          cfg.SandboxEnv,
		AllowedPaths: cfg.AllowedPaths,
	})
	if err != nil {
		slog.Error("temenos Run failure", "error", err)
		result := formatOneResult(Result{Command: pendingCmd, Err: err})
		if cbs.OnCommandResult != nil {
			cbs.OnCommandResult(pendingCmd, result, -1)
		}
		return persistText, reasoningBuf, reasoningSig, hallucinated, []string{result}, err
	}

	if resp == nil {
		slog.Error("temenos Run returned nil response", "command", pendingCmd)
		err := errors.New("runner returned nil response")
		result := formatOneResult(Result{Command: pendingCmd, Err: err})
		if cbs.OnCommandResult != nil {
			cbs.OnCommandResult(pendingCmd, result, -1)
		}
		return persistText, reasoningBuf, reasoningSig, hallucinated, []string{result}, err
	}

	result := formatOneResult(Result{
		Command:  pendingCmd,
		Stdout:   resp.Stdout,
		Stderr:   resp.Stderr,
		ExitCode: resp.ExitCode,
	})

	if cbs.OnCommandResult != nil {
		cbs.OnCommandResult(pendingCmd, result, resp.ExitCode)
	}

	return persistText, reasoningBuf, reasoningSig, hallucinated, []string{result}, nil
}

// extractCmdFromBlock extracts the command content from a <cmd>...</cmd> block.
func extractCmdFromBlock(block string) string {
	return strings.TrimSpace(
		strings.TrimSuffix(
			strings.TrimPrefix(block, CmdBlockOpen),
			CmdBlockClose,
		),
	)
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

package logos

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
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

// runLoop holds the mutable state threaded through the agent loop.
type runLoop struct {
	messages           []fantasy.Message
	steps              []StepMessage
	responseText       strings.Builder
	hallucinationCount int
	cbs                Callbacks
}

// handleTurn processes a single LLM turn and appends results to state.
// Returns a non-nil error if the run should abort (hallucination limit).
// Returns a non-nil result when the loop is complete (no more commands).
func (l *runLoop) handleTurn(
	step int,
	fullText, reasoning, reasoningSig string,
	toolCallDetected bool,
	cmdResults []string,
) (*RunResult, error) {
	if toolCallDetected {
		l.hallucinationCount++
		if l.hallucinationCount > MaxHallucinationRetries {
			return nil, fmt.Errorf("logos: tool call hallucination not resolved after %d retries", MaxHallucinationRetries)
		}
		directive := hallucinationDirective(l.hallucinationCount)
		slog.Warn("tool call hallucination detected", "step", step, "attempt", l.hallucinationCount)
		if l.cbs.OnRetry != nil {
			l.cbs.OnRetry("tool_call", step)
		}
		assistantMsg := newAssistantMessage(fullText, reasoning, reasoningSig)
		l.steps = append(l.steps, newAssistantStep(fullText, reasoning, reasoningSig))
		l.steps = append(l.steps, StepMessage{Role: StepRoleResult, Content: directive, Timestamp: time.Now().UTC()})
		l.messages = append(l.messages, assistantMsg, fantasy.NewUserMessage(directive))
		return nil, nil
	}

	l.steps = append(l.steps, newAssistantStep(fullText, reasoning, reasoningSig))
	l.responseText.WriteString(fullText)

	if len(cmdResults) == 0 {
		return &RunResult{Response: l.responseText.String(), Steps: l.steps}, nil
	}

	userContent := "<result>\n" + strings.Join(cmdResults, "\n") + "\n</result>"
	assistantMsg := newAssistantMessage(fullText, reasoning, reasoningSig)
	l.steps = append(l.steps, StepMessage{Role: StepRoleResult, Content: userContent, Timestamp: time.Now().UTC()})
	l.messages = append(l.messages, assistantMsg, fantasy.NewUserMessage(userContent))
	return nil, nil
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

	loop := &runLoop{
		messages: messages,
		cbs:      cbs,
	}

	for step := 0; step < maxSteps; step++ {
		onDelta := func(text string) {
			if cbs.OnDelta != nil {
				cbs.OnDelta(text)
			}
		}
		fullText, reasoning, reasoningSig, toolCallDetected, cmdResults, streamErr :=
			streamOneTurn(ctx, model, loop.messages, maxTokens, cfg, cbs, onDelta)
		if streamErr != nil {
			if errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
				return &RunResult{Response: loop.responseText.String(), Steps: loop.steps}, streamErr
			}
			return nil, fmt.Errorf("stream turn %d: %w", step, streamErr)
		}
		if err := ctx.Err(); err != nil {
			return &RunResult{Response: loop.responseText.String(), Steps: loop.steps}, err
		}

		result, err := loop.handleTurn(step, fullText, reasoning, reasoningSig, toolCallDetected, cmdResults)
		if err != nil {
			return result, err
		}
		if result != nil {
			return result, nil
		}
	}

	return &RunResult{
		Response: loop.responseText.String(),
		Steps:    loop.steps,
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

// cmdResult holds a command execution result with its index for ordering.
type cmdResult struct {
	index    int
	output   string
	callback *callbackData // nil if no callback needed
}

type callbackData struct {
	command  string
	output   string
	exitCode int
}

// cmdExecutor runs commands in parallel via goroutines.
type cmdExecutor struct {
	ctx       context.Context
	cfg       Config
	cbs       Callbacks
	cmdCh     chan cmdTask
	resultsCh chan cmdResult
	wg        sync.WaitGroup
}

// cmdTask is a command with its index for ordering.
type cmdTask struct {
	index int
	cmd   string
}

// newCmdExecutor creates a command executor with the given number of workers.
func newCmdExecutor(ctx context.Context, cfg Config, cbs Callbacks, n int, resultsCh chan cmdResult) *cmdExecutor {
	e := &cmdExecutor{
		ctx:       ctx,
		cfg:       cfg,
		cbs:       cbs,
		cmdCh:     make(chan cmdTask, n),
		resultsCh: resultsCh,
	}
	for i := 0; i < n; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	return e
}

// submit sends a command to the executor with its index.
func (e *cmdExecutor) submit(index int, cmd string) {
	select {
	case e.cmdCh <- cmdTask{index: index, cmd: cmd}:
	case <-e.ctx.Done():
	}
}

// Done waits for all workers to finish and closes the results channel.
func (e *cmdExecutor) Done() {
	close(e.cmdCh)
	e.wg.Wait()
	close(e.resultsCh)
}

// worker runs a command execution goroutine.
func (e *cmdExecutor) worker() {
	defer e.wg.Done()
	for task := range e.cmdCh {
		select {
		case <-e.ctx.Done():
			return
		default:
		}

		if directive, ok := handleBlockedCommand(task.cmd); ok {
			e.resultsCh <- cmdResult{
				index:  task.index,
				output: task.cmd + "\n" + directive,
			}
			continue
		}

		resp, err := e.cfg.runner.Run(e.ctx, client.RunRequest{
			Command:      task.cmd,
			Env:          e.cfg.SandboxEnv,
			AllowedPaths: e.cfg.AllowedPaths,
		})
		if err != nil {
			// Cancellation propagates up via the main loop's ctx.Err() check.
			// Exit the worker without producing a fake cmd result so the LLM is
			// not re-prompted with a cancellation-as-output.
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return
			}
			slog.Error("temenos Run failure", "error", err)
			e.resultsCh <- cmdResult{
				index:  task.index,
				output: formatOneResult(Result{Command: task.cmd, Err: err}),
			}
			continue
		}

		output := resp.Stdout
		if resp.Stderr != "" {
			output += "\nSTDERR:\n" + resp.Stderr
		}
		if output == "" {
			output = "(no output)"
		}

		var cb *callbackData
		if e.cbs.OnCommandResult != nil {
			cb = &callbackData{command: task.cmd, output: output, exitCode: resp.ExitCode}
		}
		e.resultsCh <- cmdResult{
			index: task.index,
			output: formatOneResult(Result{
				Command:  task.cmd,
				Stdout:   resp.Stdout,
				Stderr:   resp.Stderr,
				ExitCode: resp.ExitCode,
			}),
			callback: cb,
		}
	}
}

// collectedResult holds a result and its callback data for ordered emission.
type collectedResult struct {
	output   string
	callback *callbackData
}

// collectOrderedResults waits for count results and returns them in order.
// Calls OnCommandResult callback for each result in index order on the main goroutine.
func collectOrderedResults(ctx context.Context, resultsCh <-chan cmdResult, count int, cbs Callbacks) []string {
	collected := make([]*collectedResult, count)
	for i := 0; i < count; i++ {
		// Exit early on cancellation so the caller (Run) sees ctx.Err() promptly
		// instead of waiting for results that will never arrive.
		if err := ctx.Err(); err != nil {
			return nil
		}
		result, ok := <-resultsCh
		if !ok {
			return nil
		}
		collected[result.index] = &collectedResult{output: result.output, callback: result.callback}
	}
	// Fire callbacks and build output in index order.
	outputs := make([]string, count)
	for i := 0; i < count; i++ {
		if collected[i] != nil {
			outputs[i] = collected[i].output
			if collected[i].callback != nil && cbs.OnCommandResult != nil {
				cb := collected[i].callback
				cbs.OnCommandResult(cb.command, cb.output, cb.exitCode)
			}
		}
	}
	return outputs
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
	if f.exec != nil {
		f.exec(block)
	}
	if f.proseDelegate != nil {
		f.proseDelegate(block)
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
// Returns the full unfiltered text, reasoning content, reasoning signature,
// whether a tool call hallucination was detected, command results, and any error.
// Reasoning fields are empty for providers that don't emit thinking blocks.
// cmdBlockBuffer.Flush() is deferred so buffered content is always emitted, even on error.
func streamOneTurn(
	ctx context.Context,
	model fantasy.LanguageModel,
	messages []fantasy.Message,
	maxTokens int64,
	cfg Config,
	cbs Callbacks,
	onDelta func(string),
) (text string, reasoning string, reasoningSig string, hallucinated bool, cmdResults []string, err error) {
	stream, streamErr := model.Stream(ctx, fantasy.Call{
		Prompt:          fantasy.Prompt(messages),
		MaxOutputTokens: &maxTokens,
	})
	if streamErr != nil {
		return "", "", "", false, nil, streamErr
	}

	// Parallel execution: resultsCh collects cmd results in order.
	// workers capped at 8; numCmds determined by atomic counter as cmds arrive.
	numCmds := atomic.Int64{}
	resultsCh := make(chan cmdResult, 10)
	workers := 8

	exec := newCmdExecutor(ctx, cfg, cbs, workers, resultsCh)

	// Chain: hallucinationFilter → cmdBlockBuffer → onDelta
	// - hallucinationFilter catches tool_call hallucination
	// - cmdBlockBuffer routes prose to onDelta and complete blocks to exec
	hallucinationFilter := &proseFilter{delegate: func(s string) { onDelta(s) }}
	cmdFilter := &cmdBlockBuffer{
		proseDelegate: hallucinationFilter.Write,
		exec: func(block string) {
			cmd := extractCmdFromBlock(block)
			idx := int(numCmds.Add(1) - 1)
			exec.submit(idx, cmd)
		},
	}
	// Order matters: cmdFilter.Flush() must run BEFORE hallucinationFilter.Flush().
	defer func() { cmdFilter.Flush(); hallucinationFilter.Flush() }()

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
				return fullText.String(), reasoningBuf.String(), reasoningSig, hallucinationFilter.toolCallDetected, nil, part.Error
			}
		}
	}

	exec.Done()
	results := collectOrderedResults(ctx, resultsCh, int(numCmds.Load()), cbs)
	if results == nil {
		return fullText.String(), reasoningBuf.String(), reasoningSig, hallucinationFilter.toolCallDetected, nil, ctx.Err()
	}
	return fullText.String(), reasoningBuf.String(), reasoningSig, hallucinationFilter.toolCallDetected, results, nil
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

package logos

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tta-lab/temenos/client"
)

// --- mocks ---

const mockProviderName = "mock"

type mockProvider struct {
	model fantasy.LanguageModel
}

func (p *mockProvider) Name() string { return mockProviderName }
func (p *mockProvider) LanguageModel(_ context.Context, _ string) (fantasy.LanguageModel, error) {
	return p.model, nil
}

type mockLanguageModel struct {
	responses []string // each call to Stream returns the next response
	call      int
}

func (m *mockLanguageModel) Provider() string { return mockProviderName }
func (m *mockLanguageModel) Model() string    { return mockProviderName }
func (m *mockLanguageModel) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModel) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModel) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModel) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	if m.call >= len(m.responses) {
		return nil, fmt.Errorf("mock: no more responses")
	}
	text := m.responses[m.call]
	m.call++
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: text})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})
	}, nil
}

// mockCommandRunner implements commandRunner for tests.
type mockCommandRunner struct {
	mu       sync.Mutex
	response client.RunResponse
	err      error
	calls    []client.RunRequest
}

func (m *mockCommandRunner) Run(_ context.Context, req client.RunRequest) (*client.RunResponse, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, req)
	if m.err != nil {
		return nil, m.err
	}
	resp := m.response
	return &resp, nil
}

// newTestTemenosServer starts a fake temenos HTTP server over a unix socket.
// Uses os.MkdirTemp with a short prefix to avoid macOS unix socket path length limit (104 chars).
func newTestTemenosServer(t *testing.T, handler http.HandlerFunc) commandRunner {
	t.Helper()
	dir, err := os.MkdirTemp("", "tm")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	sockPath := filepath.Join(dir, "t.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	tc, err := newClient(sockPath)
	require.NoError(t, err)
	return tc
}

// newCfg builds a minimal Config for tests (no runner — use withTestRunner to inject).
func newCfg(model fantasy.LanguageModel) Config {
	return Config{
		Provider: &mockProvider{model: model},
		Model:    "test",
	}
}

// withTestRunner returns cfg with its testRunner field set to r.
// This is the only way to inject a runner into Config in tests.
func withTestRunner(cfg Config, r commandRunner) Config {
	cfg.testRunner = r
	return cfg
}

// mockLanguageModelWithReasoning emits a reasoning block followed by text.
type mockLanguageModelWithReasoning struct {
	reasoning string
	signature string
	text      string
}

func (m *mockLanguageModelWithReasoning) Provider() string { return mockProviderName }
func (m *mockLanguageModelWithReasoning) Model() string    { return mockProviderName }
func (m *mockLanguageModelWithReasoning) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelWithReasoning) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelWithReasoning) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelWithReasoning) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	return mockReasoningStream(m.reasoning, m.signature, m.text), nil
}

// mockReasoningStream returns a fantasy.StreamResponse that emits reasoning and/or text content.
// Used to eliminate duplication between mockLanguageModelWithReasoning and mockLanguageModelWithReasoningAndText.
func mockReasoningStream(reasoning, signature, text string) fantasy.StreamResponse {
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"}) //nolint:errcheck
		if reasoning != "" {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: reasoning}) //nolint:errcheck
		}
		if signature != "" {
			yield(fantasy.StreamPart{ //nolint:errcheck
				Type: fantasy.StreamPartTypeReasoningDelta,
				ID:   "0",
				ProviderMetadata: fantasy.ProviderMetadata{
					anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: signature},
				},
			})
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"}) //nolint:errcheck
		if text != "" {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: text}) //nolint:errcheck
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish}) //nolint:errcheck
	}
}

// mockLanguageModelTwoTurnsReasoning emits reasoning+command on the first call,
// then plain text on the second call. Used to test reasoning capture on intermediate steps.
type mockLanguageModelTwoTurnsReasoning struct {
	reasoning string
	signature string
	call      int
}

func (m *mockLanguageModelTwoTurnsReasoning) Provider() string { return mockProviderName }
func (m *mockLanguageModelTwoTurnsReasoning) Model() string    { return mockProviderName }
func (m *mockLanguageModelTwoTurnsReasoning) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelTwoTurnsReasoning) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelTwoTurnsReasoning) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelTwoTurnsReasoning) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	turn := m.call
	m.call++
	if turn == 0 {
		reasoning := m.reasoning
		sig := m.signature
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"})                   //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: reasoning}) //nolint:errcheck
			yield(fantasy.StreamPart{                                                                        //nolint:errcheck
				Type: fantasy.StreamPartTypeReasoningDelta,
				ID:   "0",
				ProviderMetadata: fantasy.ProviderMetadata{
					anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: sig},
				},
			})
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"})                 //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "<cmd>\nls\n</cmd>"}) //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                                //nolint:errcheck
		}, nil
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "done"}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                   //nolint:errcheck
	}, nil
}

// --- tests ---

func TestRun_OneCommandThenDone(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n<cmd>\nls -la\n</cmd>",
		"Found the files.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "file1\nfile2", ExitCode: 0}}
	var resultCmds []string
	cbs := Callbacks{
		OnCommandResult: func(cmd string, output string, exitCode int) {
			resultCmds = append(resultCmds, cmd)
		},
	}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "list files", cbs)
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Found the files.")
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "ls -la", runner.calls[0].Command)
	assert.Equal(t, []string{"ls -la"}, resultCmds)
}

func TestRun_StreamingTextArrives(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	var deltas []string
	cbs := Callbacks{
		OnDelta: func(text string) { deltas = append(deltas, text) },
	}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "run", cbs)
	require.NoError(t, err)
	combined := strings.Join(deltas, "")
	assert.Contains(t, combined, "done")
}

func TestRun_TwoCommands_SequentialTurns(t *testing.T) {
	// Single-cmd protocol: each turn emits at most one <cmd> block.
	// Model sends commands across two separate turns.
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nls\n</cmd>",
		"<cmd>\npwd\n</cmd>",
		"done",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	var results []string
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			results = append(results, cmd)
		},
	}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls", "pwd"}, results)
}

func TestRun_CallbackFiresOnLastCommand(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "final answer"}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	var results []string
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			results = append(results, cmd)
		},
	}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, results)
	assert.Contains(t, result.Response, "final answer")
}

func TestRun_HeredocCommand_FullBlockSentToRunner(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\ncat <<'EOF'\nline1\nline2\nEOF\n</cmd>",
		"Created.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "write file", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Created.")
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "cat <<'EOF'\nline1\nline2\nEOF", runner.calls[0].Command)
}

func TestRun_HttpServer_JsonEncodingRoundtrip(t *testing.T) {
	var receivedReq client.RunRequest
	tc := newTestTemenosServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedReq))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.RunResponse{Stdout: "ok", ExitCode: 0}) //nolint:errcheck
	})
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	cfg := withTestRunner(newCfg(model), tc)
	_, err := Run(context.Background(), cfg, nil, "test", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "ls", receivedReq.Command)
}

func TestRun_CommandTransportError_ExitsWithError(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	runner := &mockCommandRunner{err: fmt.Errorf("socket closed")}
	var gotCmd, gotOutput string
	var gotExitCode int
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			gotCmd = cmd
			gotOutput = output
			gotExitCode = exitCode
		},
	}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "q", cbs)
	// Runner error is propagated to Run() error.
	require.Error(t, err)
	assert.Contains(t, err.Error(), "socket closed")
	// OnCommandResult fires with exitCode=-1 for runner errors.
	assert.True(t, gotCmd != "", "OnCommandResult should be called on transport error")
	assert.Contains(t, gotOutput, "execution error:", "output should contain error text")
	assert.Equal(t, -1, gotExitCode, "transport errors have exit code -1")
}

func TestRun_ReasoningCaptured(t *testing.T) {
	model := &mockLanguageModelWithReasoning{
		reasoning: "Let me think...",
		signature: "sig123",
		text:      "The answer is 42.",
	}
	runner := &mockCommandRunner{}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, "Let me think...", result.Steps[0].Reasoning)
	assert.Equal(t, "sig123", result.Steps[0].ReasoningSignature)
	assert.Equal(t, "The answer is 42.", result.Steps[0].Content)
}

func TestRun_NoReasoning_BackwardCompat(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"simple answer"}}
	runner := &mockCommandRunner{}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	assert.Empty(t, result.Steps[0].Reasoning)
	assert.Empty(t, result.Steps[0].ReasoningSignature)
}

func TestRun_OnDelta_IncludesCmdBlockContent(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Before block.\n<cmd>\nls\n</cmd>",
		"After command.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "file.txt", ExitCode: 0}}
	var deltas []string
	cbs := Callbacks{OnDelta: func(text string) { deltas = append(deltas, text) }}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "list files", cbs)
	require.NoError(t, err)
	combined := strings.Join(deltas, "")
	assert.Contains(t, combined, "Before block.")
	assert.Contains(t, deltas, "<cmd>\nls\n</cmd>", "block should be emitted as one atomic chunk")
}

func TestRun_ReasoningCaptured_WithCommand(t *testing.T) {
	model := &mockLanguageModelTwoTurnsReasoning{
		reasoning: "I should check files first.",
		signature: "sigABC",
	}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "main.go", ExitCode: 0}}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "find files", Callbacks{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Steps), 2)
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role)
	assert.Equal(t, "I should check files first.", result.Steps[0].Reasoning)
	assert.Equal(t, "sigABC", result.Steps[0].ReasoningSignature)
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
}

func TestRun_BlockedCommand_DirectiveFedBack(t *testing.T) {
	// Model issues sed -i in a <cmd> block — should not reach runner,
	// directive should appear in the result step fed back to the model.
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nsed -i 's/foo/bar/' file.go\n</cmd>",
		"I'll use src edit instead.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	var cmdResult string
	var exitCode int
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, ec int) {
			cmdResult = cmd
			exitCode = ec
		},
	}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "edit file", cbs)
	require.NoError(t, err)
	// Runner should NOT have been called
	assert.Empty(t, runner.calls, "blocked command should not reach runner")
	// OnCommandResult SHOULD fire for blocked commands (review feedback)
	assert.NotEmpty(t, cmdResult, "OnCommandResult should fire for blocked commands")
	assert.Equal(t, -2, exitCode, "blocked commands have exit code -2")
	// Directive should appear in the result step
	require.GreaterOrEqual(t, len(result.Steps), 2)
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
	assert.Contains(t, result.Steps[1].Content, "src edit")
	// Model should have continued after receiving the directive
	assert.Contains(t, result.Response, "I'll use src edit instead.")
}

func TestRun_ExitCodeFormatted(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nfalse\n</cmd>",
		"Got it.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stderr: "error", ExitCode: 1}}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	cmdStep := result.Steps[1]
	assert.Equal(t, StepRoleResult, cmdStep.Role)
	assert.Contains(t, cmdStep.Content, "(exit code: 1)")
}

// --- StepsToMessages tests ---

func TestStepsToMessages_AssistantWithReasoning(t *testing.T) {
	steps := []StepMessage{
		{
			Role:               StepRoleAssistant,
			Content:            "I'll check.",
			Reasoning:          "Let me think...",
			ReasoningSignature: "sig123",
			Timestamp:          time.Now().UTC(),
		},
	}
	msgs := StepsToMessages(steps)
	require.Len(t, msgs, 1)
	assert.Equal(t, fantasy.MessageRoleAssistant, msgs[0].Role)
	// ReasoningPart-first ordering: reasoning comes before text.
	parts := msgs[0].Content
	require.GreaterOrEqual(t, len(parts), 2)
	assert.Equal(t, fantasy.ContentTypeReasoning, parts[0].GetType())
}

func TestStepsToMessages_AssistantThenResult(t *testing.T) {
	steps := []StepMessage{
		{
			Role:               StepRoleAssistant,
			Content:            "<cmd>\nls\n</cmd>",
			Reasoning:          "",
			ReasoningSignature: "",
			Timestamp:          time.Now().UTC(),
		},
		{
			Role:      StepRoleResult,
			Content:   "<result>\nok\n</result>",
			Timestamp: time.Now().UTC(),
		},
	}
	msgs := StepsToMessages(steps)
	require.Len(t, msgs, 2)
	assert.Equal(t, fantasy.MessageRoleAssistant, msgs[0].Role)
	assert.Equal(t, fantasy.MessageRoleUser, msgs[1].Role)
}

func TestStepsToMessages_Mixed(t *testing.T) {
	steps := []StepMessage{
		{
			Role:               StepRoleAssistant,
			Content:            "I'll check.",
			Reasoning:          "thinking...",
			ReasoningSignature: "sigX",
			Timestamp:          time.Now().UTC(),
		},
		{
			Role:      StepRoleResult,
			Content:   "<result>\nfile.go\n</result>",
			Timestamp: time.Now().UTC(),
		},
		{
			Role:               StepRoleAssistant,
			Content:            "Found it.",
			Reasoning:          "",
			ReasoningSignature: "",
			Timestamp:          time.Now().UTC(),
		},
	}
	msgs := StepsToMessages(steps)
	require.Len(t, msgs, 3)
	assert.Equal(t, fantasy.MessageRoleAssistant, msgs[0].Role)
	assert.Equal(t, fantasy.MessageRoleUser, msgs[1].Role)
	assert.Equal(t, fantasy.MessageRoleAssistant, msgs[2].Role)
}

// TestRun_MultipleCmdBlocks_SecondIgnored verifies the single-cmd protocol:
// when the model emits multiple <cmd> blocks in one turn, only the first is
// executed and subsequent blocks are silently ignored.
func TestRun_MultipleCmdBlocks_SecondIgnored(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nls\n</cmd>\n<cmd>\npwd\n</cmd>",
		"Done.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	require.Len(t, runner.calls, 1, "only the first cmd block should execute")
	assert.Equal(t, "ls", runner.calls[0].Command)
}

// blockingRunner blocks inside Run until ctx is canceled, then returns ctx.Err().
// started is buffered(1) and signals the test the moment the runner has been entered,
// removing the need for a time.Sleep race.
type blockingRunner struct {
	started chan struct{}
}

func newBlockingRunner() *blockingRunner {
	return &blockingRunner{started: make(chan struct{}, 1)}
}

func (b *blockingRunner) Run(ctx context.Context, _ client.RunRequest) (*client.RunResponse, error) {
	select {
	case b.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestRun_CancelPropagates(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"I'll run a command.\n<cmd>\nsleep 60\n</cmd>\n",
	}}
	runner := newBlockingRunner()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, withTestRunner(newCfg(model), runner), nil, "do it", Callbacks{})
		done <- err
	}()

	// Deterministic sync: wait until the runner has entered Run before we cancel.
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner.Run was never entered within 2s — test harness is broken")
	}
	cancel()

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.Canceled, "Run must surface context.Canceled, got: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after cancel — cancellation was swallowed")
	}

	// No spurious second LLM turn.
	require.Equal(t, 1, model.call,
		"mock model should only have been called once — a second call means "+
			"cancellation was swallowed and the loop continued")
}

func TestRun_DeadlineExceeded_Propagates(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Running command.\n<cmd>\nsleep 60\n</cmd>\n",
	}}
	runner := newBlockingRunner()

	// Short deadline — let it fire naturally so ctx.Err() == DeadlineExceeded.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
		done <- err
	}()

	// Wait until the runner has started, then let the deadline fire on its own.
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("runner.Run was never entered within 2s — test harness is broken")
	}

	select {
	case err := <-done:
		require.Error(t, err)
		require.ErrorIs(t, err, context.DeadlineExceeded,
			"Run must surface context.DeadlineExceeded, got: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after deadline — cancellation was swallowed")
	}
}

// --- OnStepStart / OnStepEnd / OnTurnEnd callback tests ---

func TestRun_OnStepStart_OnStepEnd_Pairing(t *testing.T) {
	// 3 steps: step 0 + 1 have commands, step 2 is final answer.
	model := &mockLanguageModel{responses: []string{
		"<cmd>echo one</cmd>",
		"<cmd>echo two</cmd>",
		"final answer",
	}}
	runner := &mockCommandRunner{
		response: client.RunResponse{Stdout: "out", ExitCode: 0},
	}

	var invocations []string
	var startIdxs []int
	cbs := Callbacks{
		OnStepStart: func(idx int) {
			invocations = append(invocations, fmt.Sprintf("step_start:%d", idx))
			startIdxs = append(startIdxs, idx)
		},
		OnStepEnd: func(idx int) {
			invocations = append(invocations, fmt.Sprintf("step_end:%d", idx))
		},
		OnTurnEnd: func(reason StopReason) {
			invocations = append(invocations, fmt.Sprintf("turn_end:%s", reason))
		},
	}

	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", cbs)
	require.NoError(t, err)

	// Each step gets a start+end pair, then a single trailing turn_end.
	assert.Equal(t, []string{
		"step_start:0", "step_end:0",
		"step_start:1", "step_end:1",
		"step_start:2", "step_end:2",
		"turn_end:final",
	}, invocations)
	assert.Equal(t, []int{0, 1, 2}, startIdxs, "OnStepStart should receive correct step indices")
}

func TestRun_OnTurnEnd_AllReasons(t *testing.T) {
	runner := &mockCommandRunner{response: client.RunResponse{ExitCode: 0}}

	// Build hallucination-limit model: MaxHallucinationRetries+1 identical <tool_call> responses.
	hallocResp := "I will call the tool.\n<tool_call>\n{\"name\": \"bash\", \"arguments\": {}}\n</tool_call>\n"
	hallocResponses := make([]string, MaxHallucinationRetries+1)
	for i := range hallocResponses {
		hallocResponses[i] = hallocResp
	}

	tests := []struct {
		name       string
		setup      func() (fantasy.LanguageModel, Config)
		wantReason StopReason
		wantSteps  int // number of expected OnStepEnd calls
	}{
		{
			name: "final",
			setup: func() (fantasy.LanguageModel, Config) {
				m := &mockLanguageModel{responses: []string{"plain answer"}}
				return m, withTestRunner(newCfg(m), runner)
			},
			wantReason: StopReasonFinal,
			wantSteps:  1,
		},
		{
			name: "canceled_in_stream",
			setup: func() (fantasy.LanguageModel, Config) {
				m := &mockLanguageModel{responses: []string{"<cmd>sleep 60</cmd>"}}
				br := newBlockingRunner()
				cfg := withTestRunner(newCfg(m), br)
				return m, cfg
			},
			wantReason: StopReasonCanceled,
			wantSteps:  1,
		},
		{
			name: "error",
			setup: func() (fantasy.LanguageModel, Config) {
				m := &errorOnceModel{}
				return m, withTestRunner(newCfg(m), runner)
			},
			wantReason: StopReasonError,
			wantSteps:  1,
		},
		{
			name: "hallucination_limit",
			setup: func() (fantasy.LanguageModel, Config) {
				m := &mockLanguageModel{responses: hallocResponses}
				return m, newCfg(m)
			},
			wantReason: StopReasonHallucinationLimit,
			wantSteps:  MaxHallucinationRetries + 1, // one per attempt
		},
		{
			name: "max_steps",
			setup: func() (fantasy.LanguageModel, Config) {
				m := &mockLanguageModel{responses: []string{"<cmd>echo loop</cmd>", "<cmd>echo loop</cmd>"}}
				cfg := withTestRunner(newCfg(m), runner)
				cfg.MaxSteps = 2
				return m, cfg
			},
			wantReason: StopReasonMaxSteps,
			wantSteps:  2, // steps 0 and 1 both ran before hitting max
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cfg := tc.setup()

			var gotReason StopReason
			var stepEndCount int
			cbs := Callbacks{
				OnStepEnd: func(_ int) { stepEndCount++ },
				OnTurnEnd: func(reason StopReason) { gotReason = reason },
			}

			if tc.name == "canceled_in_stream" {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				cfg.MaxSteps = 30
				_, err := Run(ctx, cfg, nil, "go", cbs)
				require.Error(t, err)
				assert.Equal(t, tc.wantReason, gotReason)
				assert.Equal(t, tc.wantSteps, stepEndCount)
				return
			}

			if tc.name == "max_steps" {
				cfg.MaxSteps = 2
			}

			_, _ = Run(context.Background(), cfg, nil, "go", cbs)
			assert.Equal(t, tc.wantReason, gotReason, "OnTurnEnd should fire with expected reason")
			assert.Equal(t, tc.wantSteps, stepEndCount, "OnStepEnd should fire expected number of times")
		})
	}
}

// errorOnceModel returns an error on its first and only Stream call.
type errorOnceModel struct{}

func (m *errorOnceModel) Provider() string { return "error_once" }
func (m *errorOnceModel) Model() string    { return "error_once" }
func (m *errorOnceModel) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("stream error")
}
func (m *errorOnceModel) GenerateObject(_ context.Context, _ fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("stream error")
}
func (m *errorOnceModel) StreamObject(_ context.Context, _ fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("stream error")
}
func (m *errorOnceModel) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, fmt.Errorf("stream error")
}

func TestRun_HallucinationLimit_StepsAndTurnEnd(t *testing.T) {
	// MaxHallucinationRetries+1 identical <tool_call> responses exhausts the retry budget.
	resp := "I will call the tool.\n<tool_call>\n{\"name\": \"bash\", \"arguments\": {}}\n</tool_call>\n"
	responses := make([]string, MaxHallucinationRetries+1)
	for i := range responses {
		responses[i] = resp
	}
	model := &mockLanguageModel{responses: responses}

	var stepEnds []int
	var gotReason StopReason
	cbs := Callbacks{
		OnStepEnd: func(idx int) { stepEnds = append(stepEnds, idx) },
		OnTurnEnd: func(reason StopReason) { gotReason = reason },
	}

	_, err := Run(context.Background(), newCfg(model), nil, "go", cbs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hallucination")

	// Each hallucination attempt (including the final one) emits OnStepEnd.
	assert.Len(t, stepEnds, MaxHallucinationRetries+1)
	// turn_end fires exactly once with hallucination_limit.
	assert.Equal(t, StopReasonHallucinationLimit, gotReason)
}

func TestRun_CallbackOrder_WithinStep(t *testing.T) {
	// Model emits: reasoning delta, then text delta with a cmd, then we hit max steps.
	model := &mockLanguageModelWithReasoningAndText{
		reasoning: "thinking...",
		signature: "sig123",
		text:      "<cmd>echo hi</cmd>",
	}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "hi", ExitCode: 0}}

	var invocations []string
	cbs := Callbacks{
		OnStepStart:          func(idx int) { invocations = append(invocations, fmt.Sprintf("step_start:%d", idx)) },
		OnReasoningDelta:     func(s string) { invocations = append(invocations, "reasoning_delta:"+s) },
		OnDelta:              func(s string) { invocations = append(invocations, "delta:"+s) },
		OnReasoningSignature: func(s string) { invocations = append(invocations, "reasoning_signature:"+s) },
		OnStepUsage: func(idx int, _ fantasy.Usage, _ fantasy.ProviderMetadata) {
			invocations = append(invocations, fmt.Sprintf("step_usage:%d", idx))
		},
		OnCommandResult: func(string, string, int) { invocations = append(invocations, "command_result") },
		OnStepEnd:       func(idx int) { invocations = append(invocations, fmt.Sprintf("step_end:%d", idx)) },
		OnTurnEnd: func(reason StopReason) {
			invocations = append(invocations, fmt.Sprintf("turn_end:%s", reason))
		},
	}

	cfg := withTestRunner(newCfg(model), runner)
	cfg.MaxSteps = 1 // single step; model yields same cmd on every call
	_, err := Run(context.Background(), cfg, nil, "go", cbs)
	assert.Error(t, err, "Run should error with max steps (model yields indefinitely)")
	assert.Contains(t, err.Error(), "max steps")

	// Assert OnStepStart is first.
	assert.Equal(t, "step_start:0", invocations[0])

	// Verify intermediate labels appear between step_start and step_end.
	assert.Contains(t, invocations, "reasoning_delta:thinking...")
	assert.Contains(t, invocations, "reasoning_signature:sig123")
	assert.Contains(t, invocations, "step_usage:0")
	assert.Contains(t, invocations, "command_result")

	// Exactly one step_end and one turn_end.
	var stepEndCount, turnEndCount int
	for _, inv := range invocations {
		if strings.HasPrefix(inv, "step_end:") {
			stepEndCount++
		}
		if strings.HasPrefix(inv, "turn_end:") {
			turnEndCount++
		}
	}
	assert.Equal(t, 1, stepEndCount, "exactly one step_end expected")
	assert.Equal(t, 1, turnEndCount, "exactly one turn_end expected")

	// step_end must immediately precede turn_end (no events between them).
	stepEndIdx := -1
	turnEndIdx := -1
	for i, inv := range invocations {
		if strings.HasPrefix(inv, "step_end:") {
			stepEndIdx = i
		}
		if strings.HasPrefix(inv, "turn_end:") {
			turnEndIdx = i
		}
	}
	assert.Equal(t, stepEndIdx+1, turnEndIdx, "step_end must immediately precede turn_end")
	assert.Equal(t, "turn_end:max_steps", invocations[len(invocations)-1])
}

func TestRun_CallbackOrder_AcrossSteps(t *testing.T) {
	// 3 steps: cmd / cmd / final answer.
	model := &mockLanguageModel{responses: []string{
		"<cmd>echo one</cmd>",
		"<cmd>echo two</cmd>",
		"final answer",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "out", ExitCode: 0}}

	var invocations []string
	cbs := Callbacks{
		OnStepStart:          func(idx int) { invocations = append(invocations, fmt.Sprintf("step_start:%d", idx)) },
		OnReasoningDelta:     func(s string) { invocations = append(invocations, "reasoning_delta:"+s) },
		OnDelta:              func(s string) { invocations = append(invocations, "delta:"+s) },
		OnReasoningSignature: func(s string) { invocations = append(invocations, "reasoning_signature:"+s) },
		OnCommandResult:      func(string, string, int) { invocations = append(invocations, "command_result") },
		OnStepEnd:            func(idx int) { invocations = append(invocations, fmt.Sprintf("step_end:%d", idx)) },
		OnTurnEnd: func(reason StopReason) {
			invocations = append(invocations, fmt.Sprintf("turn_end:%s", reason))
		},
	}

	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", cbs)
	require.NoError(t, err)

	// Build expected sequence: 3 steps with pairing, then single trailing turn_end.
	// Steps 0+1 emit cmd blocks → OnDelta + OnCommandResult fire between step_start/step_end.
	// Step 2 is a final answer → only OnDelta fires before step_end.
	want := []string{
		"step_start:0", "delta:<cmd>echo one</cmd>", "command_result", "step_end:0",
		"step_start:1", "delta:<cmd>echo two</cmd>", "command_result", "step_end:1",
		"step_start:2", "delta:final answer", "step_end:2",
		"turn_end:final",
	}
	assert.Equal(t, want, invocations)

	// turn_end must be strictly last.
	last := invocations[len(invocations)-1]
	assert.Equal(t, "turn_end:final", last)
}

func TestRun_OnCommandResult_BlockedCmd(t *testing.T) {
	// Model emits sed -i — should be blocked by validate.go, directive fed back.
	model := &mockLanguageModel{responses: []string{
		"<cmd>sed -i 's/foo/bar/' file.go</cmd>",
		"I'll use src edit instead.",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}

	var gotCmd, gotOutput string
	var gotExitCode int
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			gotCmd = cmd
			gotOutput = output
			gotExitCode = exitCode
		},
	}

	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "edit file", cbs)
	require.NoError(t, err)

	// Runner must not be called for blocked commands.
	assert.Empty(t, runner.calls, "blocked command should not reach runner")

	// OnCommandResult must fire with the blocked directive and exit code -2.
	assert.Contains(t, gotCmd, "sed -i")
	assert.Contains(t, gotOutput, "src edit")
	assert.Equal(t, -2, gotExitCode, "blocked commands have exit code -2")

	// Model continued after receiving the directive.
	assert.Contains(t, result.Response, "I'll use src edit instead.")
}

func TestRun_OnCommandResult_NonZeroExitCode(t *testing.T) {
	// Model emits a command that exits non-zero; runner returns exit code 1.
	model := &mockLanguageModel{responses: []string{"<cmd>grep foo file.txt</cmd>", "done"}}
	runner := &mockCommandRunner{
		response: client.RunResponse{Stdout: "", Stderr: "file.txt: no such file or directory", ExitCode: 1},
	}

	var gotCmd, gotOutput string
	var gotExitCode int
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			gotCmd = cmd
			gotOutput = output
			gotExitCode = exitCode
		},
	}

	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "search", cbs)
	require.NoError(t, err)

	assert.Contains(t, gotCmd, "grep foo file.txt")
	// gotOutput is the formatOneResult output: includes STDERR and exit code annotation.
	assert.Contains(t, gotOutput, "file.txt: no such file or directory")
	assert.Contains(t, gotOutput, "(exit code: 1)")
	assert.Equal(t, 1, gotExitCode, "exit code should be propagated from runner")
	// Cmd step persists only the block; final-answer step appends "done".
	assert.Contains(t, result.Response, "<cmd>grep foo file.txt</cmd>")
}

type mockLanguageModelWithReasoningAndText struct {
	reasoning string
	signature string
	text      string
}

func (m *mockLanguageModelWithReasoningAndText) Provider() string { return mockProviderName }
func (m *mockLanguageModelWithReasoningAndText) Model() string    { return mockProviderName }
func (m *mockLanguageModelWithReasoningAndText) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, nil
}
func (m *mockLanguageModelWithReasoningAndText) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, nil
}
func (m *mockLanguageModelWithReasoningAndText) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}
func (m *mockLanguageModelWithReasoningAndText) Stream(
	_ context.Context, _ fantasy.Call,
) (fantasy.StreamResponse, error) {
	return mockReasoningStream(m.reasoning, m.signature, m.text), nil
}

// mockLanguageModelThreeReasoningDeltas emits three separate reasoning deltas, one signature, then text.
type mockLanguageModelThreeReasoningDeltas struct {
	sig  string
	text string
}

func (m *mockLanguageModelThreeReasoningDeltas) Provider() string { return mockProviderName }
func (m *mockLanguageModelThreeReasoningDeltas) Model() string    { return mockProviderName }
func (m *mockLanguageModelThreeReasoningDeltas) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, nil
}
func (m *mockLanguageModelThreeReasoningDeltas) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, nil
}
func (m *mockLanguageModelThreeReasoningDeltas) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}
func (m *mockLanguageModelThreeReasoningDeltas) Stream(
	_ context.Context, _ fantasy.Call,
) (fantasy.StreamResponse, error) {
	sig := m.sig
	txt := m.text
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"})                  //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: "think1"}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: "think2"}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: "think3"}) //nolint:errcheck
		yield(fantasy.StreamPart{                                                                       //nolint:errcheck
			Type: fantasy.StreamPartTypeReasoningDelta,
			ID:   "0",
			ProviderMetadata: fantasy.ProviderMetadata{
				anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: sig},
			},
		})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: txt}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                //nolint:errcheck
	}, nil
}

func TestRun_OnReasoningDelta_StreamsLive(t *testing.T) {
	model := &mockLanguageModelThreeReasoningDeltas{
		sig:  "sig",
		text: "answer",
	}

	var deltas []string
	cbs := Callbacks{
		OnReasoningDelta: func(s string) { deltas = append(deltas, s) },
	}

	_, err := Run(context.Background(), newCfg(model), nil, "go", cbs)
	require.NoError(t, err)

	// Three separate reasoning deltas arrive as three callback invocations.
	assert.Len(t, deltas, 3, "expected 3 reasoning delta callbacks")
	assert.Equal(t, []string{"think1", "think2", "think3"}, deltas)
}

func TestRun_OnReasoningSignature_FiresOnce(t *testing.T) {
	model := &mockLanguageModelWithReasoningAndText{
		reasoning: "thinking",
		signature: "sig_once",
		text:      "plain answer",
	}

	var sigs []string
	cbs := Callbacks{
		OnReasoningSignature: func(s string) { sigs = append(sigs, s) },
	}

	_, err := Run(context.Background(), newCfg(model), nil, "go", cbs)
	require.NoError(t, err)

	assert.Len(t, sigs, 1, "signature callback should fire exactly once")
	assert.Equal(t, "sig_once", sigs[0])
}

func TestRun_AllCallbacks_NilSafe(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>echo one</cmd>",
		"final answer",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "out", ExitCode: 0}}

	// Must not panic with all-nil callbacks across 2 steps (all 8 fields nil-safe).
	assert.NotPanics(t, func() {
		_, _ = Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	})
}

// mockLanguageModelWithUsage is like mockLanguageModel but emits custom usage in the finish event.
type mockLanguageModelWithUsage struct {
	model     string
	usage     []fantasy.Usage // indexed by call
	meta      fantasy.ProviderMetadata
	call      int
	maxCalls  int
	responses []string // indexed by call, defaults to "hello" if nil
}

// mockProviderData implements fantasy.ProviderOptionsData for tests.
type mockProviderData struct{}

func (mockProviderData) Options()                     {}
func (mockProviderData) MarshalJSON() ([]byte, error) { return []byte(`{}`), nil }
func (mockProviderData) UnmarshalJSON([]byte) error   { return nil }

func (m *mockLanguageModelWithUsage) Provider() string { return "mock" }
func (m *mockLanguageModelWithUsage) Model() string    { return m.model }
func (m *mockLanguageModelWithUsage) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelWithUsage) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelWithUsage) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModelWithUsage) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	if m.call >= m.maxCalls {
		return nil, fmt.Errorf("mock: no more responses")
	}
	call := m.call
	m.call++
	text := "hello"
	if m.responses != nil && call < len(m.responses) {
		text = m.responses[call]
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: text})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, Usage: m.usage[call], ProviderMetadata: m.meta})
	}, nil
}

func TestRun_UsagePopulated(t *testing.T) {
	wantMeta := fantasy.ProviderMetadata{"openai": mockProviderData{}}
	model := &mockLanguageModelWithUsage{
		model:    "test-model",
		usage:    []fantasy.Usage{{InputTokens: 42, OutputTokens: 100}},
		meta:     wantMeta,
		maxCalls: 1,
	}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}

	var gotStepIdx int
	var gotUsage fantasy.Usage
	var gotMeta fantasy.ProviderMetadata
	cbs := Callbacks{
		OnStepUsage: func(stepIdx int, usage fantasy.Usage, meta fantasy.ProviderMetadata) {
			gotStepIdx = stepIdx
			gotUsage = usage
			gotMeta = meta
		},
	}

	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "say hello", cbs)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, 0, gotStepIdx)
	assert.Equal(t, int64(42), gotUsage.InputTokens)
	assert.Equal(t, int64(100), gotUsage.OutputTokens)
	assert.Equal(t, wantMeta, gotMeta)
}

func TestRun_UsagePerStep(t *testing.T) {
	// Step 1: cmd → result, usage[0]. Step 2: final answer, usage[1].
	// OnStepUsage fires once per step with that step's usage.
	model := &mockLanguageModelWithUsage{
		model: "test-model",
		usage: []fantasy.Usage{
			{InputTokens: 1000, OutputTokens: 500},
			{InputTokens: 2000, OutputTokens: 600},
		},
		responses: []string{
			"<cmd>echo ok</cmd>", // step 0: triggers cmd execution
			"final answer",       // step 1: no cmd → done
		},
		maxCalls: 2,
	}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}

	type usageRecord struct {
		stepIdx int
		usage   fantasy.Usage
	}
	var records []usageRecord
	cbs := Callbacks{
		OnStepUsage: func(stepIdx int, usage fantasy.Usage, _ fantasy.ProviderMetadata) {
			records = append(records, usageRecord{stepIdx: stepIdx, usage: usage})
		},
	}

	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "run two steps", cbs)
	require.NoError(t, err)
	require.NotNil(t, result)

	require.Len(t, records, 2, "OnStepUsage fires once per step")
	assert.Equal(t, 0, records[0].stepIdx)
	assert.Equal(t, int64(1000), records[0].usage.InputTokens)
	assert.Equal(t, int64(500), records[0].usage.OutputTokens)
	assert.Equal(t, 1, records[1].stepIdx)
	assert.Equal(t, int64(2000), records[1].usage.InputTokens)
	assert.Equal(t, int64(600), records[1].usage.OutputTokens)
}

// --- Parser behavior tests ---

// captureWarnLogs installs a temporary slog handler that captures Warn-level records
// and returns the buffer. Restores the previous default logger on cleanup.
func captureWarnLogs(t *testing.T) *strings.Builder {
	t.Helper()
	var buf strings.Builder
	h := slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	t.Cleanup(func() { slog.SetDefault(old) })
	return &buf
}

func TestRun_PersistsOnlyCmdBlock_WhenCmdPresent(t *testing.T) {
	// Model writes pre-cmd prose then a cmd block — only the block is persisted.
	model := &mockLanguageModel{responses: []string{
		"Let me check first.\n<cmd>\nls\n</cmd>",
		"done",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	// The assistant step content must be only the cmd block, not the prose preamble.
	require.GreaterOrEqual(t, len(result.Steps), 1)
	assert.Equal(t, "<cmd>\nls\n</cmd>", result.Steps[0].Content)
}

func TestRun_PersistsReplyText_WhenNoCmd(t *testing.T) {
	// Reply step: no cmd → full reply text is persisted.
	model := &mockLanguageModel{responses: []string{"final answer"}}
	result, err := Run(context.Background(), newCfg(model), nil, "go", Callbacks{})
	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, "final answer", result.Steps[0].Content)
}

func TestRun_DropsPreCmdProse_LogsWarn(t *testing.T) {
	buf := captureWarnLogs(t)
	model := &mockLanguageModel{responses: []string{
		"I think I should check.\n<cmd>\nls\n</cmd>",
		"done",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "pre-cmd prose dropped", "slog.Warn should fire for non-whitespace pre-cmd prose")
}

func TestRun_DropsPostCmdProse_LogsWarn(t *testing.T) {
	buf := captureWarnLogs(t)
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nls\n</cmd>\nThis should be dropped.",
		"done",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, buf.String(), "post-cmd prose dropped", "slog.Warn should fire for non-whitespace post-cmd prose")
}

func TestRun_StreamsPreCmdProseLive(t *testing.T) {
	// Pre-cmd prose reaches OnDelta even though it is not persisted.
	model := &mockLanguageModel{responses: []string{
		"thinking...\n<cmd>\nls\n</cmd>",
		"done",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	var allDeltas []string
	cbs := Callbacks{OnDelta: func(s string) { allDeltas = append(allDeltas, s) }}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", cbs)
	require.NoError(t, err)
	combined := strings.Join(allDeltas, "")
	assert.Contains(t, combined, "thinking...")
	assert.Contains(t, combined, "<cmd>\nls\n</cmd>")
}

func TestRun_NoWarnOnWhitespaceAroundCmd(t *testing.T) {
	buf := captureWarnLogs(t)
	// Only whitespace/newlines around the cmd block — no Warn expected.
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nls\n</cmd>\n  \n",
		"done",
	}}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "ok", ExitCode: 0}}
	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	assert.NotContains(t, buf.String(), "post-cmd prose dropped")
}

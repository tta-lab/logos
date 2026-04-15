package logos

import (
	"context"
	"encoding/json"
	"fmt"
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

func TestRun_CommandTransportError_CallbackNotFired(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	runner := &mockCommandRunner{err: fmt.Errorf("socket closed")}
	var callbackFired bool
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			callbackFired = true
		},
	}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "q", cbs)
	require.NoError(t, err)
	assert.False(t, callbackFired, "OnCommandResult should NOT be called on transport error")
	require.Len(t, result.Steps, 3) // assistant, result, final
	assert.Contains(t, result.Steps[1].Content, "execution error:")
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

// --- stripPostCmdText tests ---

func TestStripPostCmdText_PureSpeech(t *testing.T) {
	input := "Done. Here's what I found."
	assert.Equal(t, input, stripPostCmdText(input))
}

func TestStripPostCmdText_PreamblePlusCmd(t *testing.T) {
	input := "Let me check.\n<cmd>\nls\n</cmd>"
	assert.Equal(t, input, stripPostCmdText(input))
}

func TestStripPostCmdText_PreambleCmdPostProse(t *testing.T) {
	input := "Checking.\n<cmd>\nls\n</cmd>\nfile.go"
	expected := "Checking.\n<cmd>\nls\n</cmd>"
	assert.Equal(t, expected, stripPostCmdText(input))
}

func TestStripPostCmdText_TwoCmdsBetweenProse(t *testing.T) {
	input := "Running both.\n<cmd>\na\n</cmd>\nfound a\n<cmd>\nb\n</cmd>"
	// LastIndex: keep everything up to and including the last </cmd>.
	expected := "Running both.\n<cmd>\na\n</cmd>\nfound a\n<cmd>\nb\n</cmd>"
	assert.Equal(t, expected, stripPostCmdText(input))
}

func TestStripPostCmdText_CmdBetweenPost(t *testing.T) {
	input := "A.\n<cmd>\nx\n</cmd>\nb\n<cmd>\ny\n</cmd>\nz"
	// LastIndex: keep everything up to the last </cmd>, preserving everything before.
	expected := "A.\n<cmd>\nx\n</cmd>\nb\n<cmd>\ny\n</cmd>"
	assert.Equal(t, expected, stripPostCmdText(input))
}

func TestStripPostCmdText_UnclosedCmd(t *testing.T) {
	// No </cmd> found — LastIndex returns -1, input returned unchanged.
	input := "text\n<cmd>\nls"
	assert.Equal(t, input, stripPostCmdText(input))
}

func TestStripPostCmdText_EmptyInput(t *testing.T) {
	assert.Equal(t, "", stripPostCmdText(""))
}

func TestStripPostCmdText_LeadingCmdNoPreamble(t *testing.T) {
	input := "<cmd>\nls\n</cmd>"
	assert.Equal(t, input, stripPostCmdText(input))
}

func TestStripPostCmdText_NestedCmdInHeredoc(t *testing.T) {
	// LastIndex ensures the outermost </cmd> is used.
	input := "<cmd>\ncat <<'EOF'\n</cmd>\nhello\nEOF\n</cmd>\npost"
	expected := "<cmd>\ncat <<'EOF'\n</cmd>\nhello\nEOF\n</cmd>"
	assert.Equal(t, expected, stripPostCmdText(input))
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
			Content:   "<result>\nls\nok\n</result>",
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
			Content:   "<result>\nls\nfile.go\n</result>",
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

func TestRun_HallucinationRetries_RespectsLimit(t *testing.T) {
	// Each response contains an XML tool call marker — triggers the hallucination path.
	hallucinatedResponse := "I will call the tool.\n<tool_call>\n{\"name\": \"bash\", \"arguments\": {}}\n</tool_call>\n"
	responses := make([]string, MaxHallucinationRetries+1)
	for i := range responses {
		responses[i] = hallucinatedResponse
	}
	model := &mockLanguageModel{responses: responses}

	var retryCalls []string
	cbs := Callbacks{
		OnRetry: func(reason string, step int) {
			retryCalls = append(retryCalls, reason)
		},
	}

	_, err := Run(context.Background(), newCfg(model), nil, "go", cbs)

	require.Error(t, err, "Run should return an error after exhausting hallucination retries")
	assert.Contains(t, err.Error(), "hallucination")
	assert.Len(t, retryCalls, MaxHallucinationRetries,
		"OnRetry should fire exactly MaxHallucinationRetries times")
	for _, reason := range retryCalls {
		assert.Equal(t, "tool_call", reason)
	}
}

// --- OnTurnStart / OnTurnEnd callback tests ---

func TestRun_OnTurnStart_OnTurnEnd_Pairing(t *testing.T) {
	// 3 turns: turn 0 + 1 have commands, turn 2 is final answer.
	model := &mockLanguageModel{responses: []string{
		"<cmd>echo one</cmd>",
		"<cmd>echo two</cmd>",
		"final answer",
	}}
	runner := &mockCommandRunner{
		response: client.RunResponse{Stdout: "out", ExitCode: 0},
	}

	var turns []string
	var startIdxs []int
	cbs := Callbacks{
		OnTurnStart: func(idx int) {
			turns = append(turns, fmt.Sprintf("start:%d", idx))
			startIdxs = append(startIdxs, idx)
		},
		OnTurnEnd: func(idx int, reason StopReason) { turns = append(turns, fmt.Sprintf("end:%d:%s", idx, reason)) },
	}

	_, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", cbs)
	require.NoError(t, err)

	assert.Equal(t, []string{
		"start:0", "end:0:tool_use",
		"start:1", "end:1:tool_use",
		"start:2", "end:2:end_turn",
	}, turns)
	// Assert the actual index values passed to OnTurnStart
	assert.Equal(t, []int{0, 1, 2}, startIdxs, "OnTurnStart should receive correct turn indices")
}

// mockLanguageModelTwoTurnCmd yields a command on turn 0, then plain text on turn 1.
type mockLanguageModelTwoTurnCmd struct {
	firstText string
	call      int
}

func (m *mockLanguageModelTwoTurnCmd) Provider() string { return mockProviderName }
func (m *mockLanguageModelTwoTurnCmd) Model() string    { return mockProviderName }
func (m *mockLanguageModelTwoTurnCmd) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, nil
}
func (m *mockLanguageModelTwoTurnCmd) GenerateObject(
	_ context.Context, _ fantasy.ObjectCall,
) (*fantasy.ObjectResponse, error) {
	return nil, nil
}
func (m *mockLanguageModelTwoTurnCmd) StreamObject(
	_ context.Context, _ fantasy.ObjectCall,
) (fantasy.ObjectStreamResponse, error) {
	return nil, nil
}
func (m *mockLanguageModelTwoTurnCmd) Stream(_ context.Context, _ fantasy.Call) (fantasy.StreamResponse, error) {
	turn := m.call
	m.call++
	if turn == 0 {
		txt := m.firstText
		return func(yield func(fantasy.StreamPart) bool) {
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: txt}) //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                //nolint:errcheck
		}, nil
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "final answer"}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                           //nolint:errcheck
	}, nil
}

func TestRun_OnTurnEnd_StopReason(t *testing.T) {
	runner := &mockCommandRunner{response: client.RunResponse{ExitCode: 0}}

	tests := []struct {
		name       string
		model      fantasy.LanguageModel
		wantReason string
		maxSteps   int
	}{
		{
			name:       "end_turn",
			model:      &mockLanguageModel{responses: []string{"plain answer"}},
			wantReason: "end_turn",
			maxSteps:   30,
		},
		{
			name:       "canceled_deadline",
			model:      &mockLanguageModel{responses: []string{"<cmd>sleep 60</cmd>"}},
			wantReason: "canceled",
			maxSteps:   30,
		},
		{
			name:       "error",
			model:      &errorOnceModel{},
			wantReason: "error",
			maxSteps:   30,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotReason string
			cbs := Callbacks{
				OnTurnEnd: func(_ int, reason StopReason) { gotReason = string(reason) },
			}

			if tc.name == "canceled_deadline" {
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				defer cancel()
				br := newBlockingRunner()
				cfg := withTestRunner(newCfg(tc.model), br)
				cfg.MaxSteps = tc.maxSteps
				_, err := Run(ctx, cfg, nil, "go", cbs)
				require.Error(t, err)
				assert.Equal(t, tc.wantReason, gotReason)
				return
			}

			cfg := withTestRunner(newCfg(tc.model), runner)
			cfg.MaxSteps = tc.maxSteps
			_, _ = Run(context.Background(), cfg, nil, "go", cbs)
			assert.Equal(t, tc.wantReason, gotReason)
		})
	}
}

// TestRun_OnTurnEnd_StopReason_ToolUse verifies tool_use reason when a command executes.
// Uses a slice so we can assert both turn 0 (tool_use) and turn 1 (end_turn) fire.
func TestRun_OnTurnEnd_StopReason_ToolUse(t *testing.T) {
	model := &mockLanguageModelTwoTurnCmd{firstText: "<cmd>echo hi</cmd>"}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "hi", ExitCode: 0}}

	var reasons []string
	cbs := Callbacks{
		OnTurnEnd: func(_ int, reason StopReason) { reasons = append(reasons, string(reason)) },
	}

	cfg := withTestRunner(newCfg(model), runner)
	cfg.MaxSteps = 30
	_, _ = Run(context.Background(), cfg, nil, "go", cbs)

	require.Len(t, reasons, 2, "expected two OnTurnEnd calls: turn 0=tool_use, turn 1=end_turn")
	assert.Equal(t, "tool_use", reasons[0], "turn 0 should fire tool_use")
	assert.Equal(t, "end_turn", reasons[1], "turn 1 should fire end_turn")
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

func TestRun_OnTurnEnd_ToolCallRetry(t *testing.T) {
	resp := "I will call the tool.\n<tool_call>\n{\"name\": \"bash\", \"arguments\": {}}\n</tool_call>\n"
	responses := make([]string, MaxHallucinationRetries+1)
	for i := range responses {
		responses[i] = resp
	}
	model := &mockLanguageModel{responses: responses}

	var endReasons []string
	cbs := Callbacks{
		OnTurnEnd: func(_ int, reason StopReason) { endReasons = append(endReasons, string(reason)) },
	}

	_, err := Run(context.Background(), newCfg(model), nil, "go", cbs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "hallucination")

	// MaxHallucinationRetries "tool_call_retry" events + 1 final "error" for exceeding limit.
	want := make([]string, MaxHallucinationRetries+1)
	for i := range want {
		if i < MaxHallucinationRetries {
			want[i] = "tool_call_retry"
		} else {
			want[i] = "error"
		}
	}
	assert.Equal(t, want, endReasons)
}

func TestRun_CallbackOrder_WithinTurn(t *testing.T) {
	// Model emits: reasoning delta, then text delta, then finish.
	model := &mockLanguageModelWithReasoningAndText{
		reasoning: "thinking...",
		signature: "sig123",
		text:      "<cmd>echo hi</cmd>",
	}
	runner := &mockCommandRunner{response: client.RunResponse{Stdout: "hi", ExitCode: 0}}

	var invocations []string
	cbs := Callbacks{
		OnTurnStart:          func(int) { invocations = append(invocations, "turn_start") },
		OnReasoningDelta:     func(s string) { invocations = append(invocations, "reasoning_delta:"+s) },
		OnDelta:              func(s string) { invocations = append(invocations, "delta:"+s) },
		OnReasoningSignature: func(s string) { invocations = append(invocations, "reasoning_signature:"+s) },
		OnCommandResult:      func(string, string, int) { invocations = append(invocations, "command_result") },
		OnTurnEnd: func(_ int, reason StopReason) {
			invocations = append(invocations, "turn_end:"+string(reason))
		},
	}

	cfg := withTestRunner(newCfg(model), runner)
	cfg.MaxSteps = 1 // single turn only; model yields same content on every call
	_, err := Run(context.Background(), cfg, nil, "go", cbs)
	assert.Error(t, err, "Run should error with max steps (model yields indefinitely)")
	assert.Contains(t, err.Error(), "max steps")

	// Assert OnTurnStart is first and OnTurnEnd is last.
	assert.Equal(t, "turn_start", invocations[0])
	last := invocations[len(invocations)-1]
	assert.True(t, strings.HasPrefix(last, "turn_end:"), "last invocation should be turn_end, got %s", last)

	// Verify all expected intermediate labels appear, in relative order.
	assert.Contains(t, invocations, "reasoning_delta:thinking...")
	assert.Contains(t, invocations, "reasoning_signature:sig123")
	assert.Contains(t, invocations, "command_result")
	assert.Contains(t, invocations, "turn_end:tool_use")
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

	// Must not panic with all-nil callbacks across 2 turns.
	assert.NotPanics(t, func() {
		_, _ = Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "go", Callbacks{})
	})
}

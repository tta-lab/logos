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
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/providers/anthropic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

// mockCommandRunner implements CommandRunner for tests.
type mockCommandRunner struct {
	response RunResponse
	err      error
	calls    []RunRequest
}

func (m *mockCommandRunner) Run(_ context.Context, req RunRequest) (*RunResponse, error) {
	m.calls = append(m.calls, req)
	if m.err != nil {
		return nil, m.err
	}
	resp := m.response
	return &resp, nil
}

// newTestTemenosServer starts a fake temenos HTTP server over a unix socket.
// Uses os.MkdirTemp with a short prefix to avoid macOS unix socket path length limit (104 chars).
func newTestTemenosServer(t *testing.T, handler http.HandlerFunc) CommandRunner {
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
	tc, err := NewClient(sockPath)
	require.NoError(t, err)
	return tc
}

func newCfg(model fantasy.LanguageModel, runner CommandRunner) Config {
	return Config{
		Provider: &mockProvider{model: model},
		Model:    "test",
		Temenos:  runner,
	}
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
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"})                                            //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: m.reasoning})                        //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", ProviderMetadata: fantasy.ProviderMetadata{ //nolint:errcheck
			anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: m.signature},
		}})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"})    //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: m.text}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})
	}, nil
}

// --- tests ---

func TestRun_OneCommandThenDone(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n<cmd>\nls -la\n</cmd>",
		"Found the files.",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "file1\nfile2", ExitCode: 0}}
	var resultCmds []string
	cbs := Callbacks{
		OnCommandResult: func(cmd string, output string, exitCode int) {
			resultCmds = append(resultCmds, cmd)
		},
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "list files", cbs)
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Found the files.")
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "ls -la", runner.calls[0].Command)
	assert.Equal(t, []string{"ls -la"}, resultCmds)
}

func TestRun_StreamingTextArrives(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	var deltas []string
	cbs := Callbacks{
		OnDelta: func(text string) { deltas = append(deltas, text) },
	}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "run", cbs)
	require.NoError(t, err)
	combined := strings.Join(deltas, "")
	assert.Contains(t, combined, "done")
}

func TestRun_CallbackFiresPerCommand(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nls\n</cmd>",
		"<cmd>\npwd\n</cmd>",
		"done",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	var results []string
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			results = append(results, cmd)
		},
	}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "go", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls", "pwd"}, results)
}

func TestRun_CallbackFiresOnLastCommand(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "final answer"}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	var results []string
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			results = append(results, cmd)
		},
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, results)
	assert.Contains(t, result.Response, "final answer")
}

func TestRun_MultiCommand_ExecutesAll(t *testing.T) {
	// Each <cmd> block is one command; multiple blocks = parallel execution.
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n<cmd>\npwd\n</cmd>\n<cmd>\nls -la\n</cmd>",
		"Found the files.",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	var resultCmds []string
	cbs := Callbacks{
		OnCommandResult: func(cmd string, output string, exitCode int) { resultCmds = append(resultCmds, cmd) },
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "check", cbs)
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Found the files.")
	require.Len(t, runner.calls, 2)
	assert.Equal(t, "pwd", runner.calls[0].Command)
	assert.Equal(t, "ls -la", runner.calls[1].Command)
	assert.Equal(t, []string{"pwd", "ls -la"}, resultCmds)
}

func TestRun_MultiCommand_ExitCodeFormatted(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\nfalse\n</cmd>\n<cmd>\necho ok\n</cmd>",
		"Got it.",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stderr: "error", ExitCode: 1}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	cmdStep := result.Steps[1]
	assert.Equal(t, StepRoleResult, cmdStep.Role)
	assert.Contains(t, cmdStep.Content, "(exit code: 1)")
}

func TestRun_MultiCommand_WithHeredoc(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\ncat <<'EOF'\nhello\nEOF\n</cmd>\n<cmd>\nls -la\n</cmd>",
		"Done.",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Done.")
	require.Len(t, runner.calls, 2)
	assert.Equal(t, "cat <<'EOF'\nhello\nEOF", runner.calls[0].Command)
	assert.Equal(t, "ls -la", runner.calls[1].Command)
}

func TestRun_ConsecutiveCommands_NoWarningInjected(t *testing.T) {
	// Verify no soft warning is injected regardless of consecutive command count.
	responses := []string{
		"<cmd>\necho 1\n</cmd>", "<cmd>\necho 2\n</cmd>", "<cmd>\necho 3\n</cmd>",
		"<cmd>\necho 4\n</cmd>", "<cmd>\necho 5\n</cmd>",
		"Halfway.",
		"<cmd>\necho 6\n</cmd>", "<cmd>\necho 7\n</cmd>", "<cmd>\necho 8\n</cmd>",
		"<cmd>\necho 9\n</cmd>", "<cmd>\necho 10\n</cmd>",
		"Done.",
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	for _, s := range result.Steps {
		if s.Role == StepRoleResult {
			assert.NotContains(t, s.Content, "without explaining",
				"no soft warning should be injected")
		}
	}
}

func TestRun_HeredocCommand_FullBlockSentToRunner(t *testing.T) {
	// Model issues a heredoc command — runner must receive the complete raw command.
	model := &mockLanguageModel{responses: []string{
		"<cmd>\ncat <<'EOF'\nline1\nline2\nEOF\n</cmd>",
		"Created.",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "ok", ExitCode: 0}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "write file", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Created.")
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "cat <<'EOF'\nline1\nline2\nEOF", runner.calls[0].Command)
}

// TestRun_HttpServer_JsonEncodingRoundtrip verifies that the real temenos client
// correctly encodes requests and decodes responses end-to-end over a unix socket.
func TestRun_HttpServer_JsonEncodingRoundtrip(t *testing.T) {
	var receivedReq RunRequest
	tc := newTestTemenosServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedReq))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RunResponse{Stdout: "ok", ExitCode: 0}) //nolint:errcheck
	})
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	cfg := newCfg(model, tc)
	_, err := Run(context.Background(), cfg, nil, "test", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "ls", receivedReq.Command)
}

func TestRun_CommandTransportError_CallbackNotFired(t *testing.T) {
	// Transport errors do NOT fire OnCommandResult — the error is surfaced to the
	// LLM via the <result> step.
	model := &mockLanguageModel{responses: []string{"<cmd>\nls\n</cmd>", "done"}}
	runner := &mockCommandRunner{err: fmt.Errorf("socket closed")}
	var callbackFired bool
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			callbackFired = true
		},
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
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
	result, err := Run(context.Background(), newCfg(model, runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, "Let me think...", result.Steps[0].Reasoning)
	assert.Equal(t, "sig123", result.Steps[0].ReasoningSignature)
	assert.Equal(t, "The answer is 42.", result.Steps[0].Content)
}

func TestRun_NoReasoning_BackwardCompat(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"simple answer"}}
	runner := &mockCommandRunner{}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	assert.Empty(t, result.Steps[0].Reasoning)
	assert.Empty(t, result.Steps[0].ReasoningSignature)
}

func TestRun_OnDelta_IncludesCmdBlockContent(t *testing.T) {
	// Verify that <cmd> block content IS passed to OnDelta as a complete atomic chunk.
	// Consumers (TUI, iOS) rely on receiving complete <cmd>...</cmd> blocks to render them.
	model := &mockLanguageModel{responses: []string{
		"Before block.\n<cmd>\nls\n</cmd>",
		"After command.",
	}}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "file.txt", ExitCode: 0}}
	var deltas []string
	cbs := Callbacks{OnDelta: func(text string) { deltas = append(deltas, text) }}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "list files", cbs)
	require.NoError(t, err)
	combined := strings.Join(deltas, "")
	assert.Contains(t, combined, "Before block.")
	// Block must arrive as one atomic chunk, not split across multiple OnDelta calls
	assert.Contains(t, deltas, "<cmd>\nls\n</cmd>", "block should be emitted as one atomic chunk")
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
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"})                                            //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: reasoning})                          //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", ProviderMetadata: fantasy.ProviderMetadata{ //nolint:errcheck
				anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: sig},
			}})
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

func TestRun_ReasoningCaptured_WithCommand(t *testing.T) {
	// Verify reasoning is captured on the intermediate (command-issuing) step, not just
	// the terminal step.
	model := &mockLanguageModelTwoTurnsReasoning{
		reasoning: "I should check files first.",
		signature: "sigABC",
	}
	runner := &mockCommandRunner{response: RunResponse{Stdout: "main.go", ExitCode: 0}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "find files", Callbacks{})
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(result.Steps), 2)
	// Step 0 is the command-issuing assistant step — must carry reasoning.
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role)
	assert.Equal(t, "I should check files first.", result.Steps[0].Reasoning)
	assert.Equal(t, "sigABC", result.Steps[0].ReasoningSignature)
	// Step 1 is the result step.
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
}

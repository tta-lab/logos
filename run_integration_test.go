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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- mocks ---

const mockProviderName = "mock"

type mockProvider struct {
	model *mockLanguageModel
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

// mockRunner implements CommandRunner for unit tests.
type mockRunner struct {
	response RunResponse
	err      error
	calls    []RunRequest
}

func (m *mockRunner) Run(_ context.Context, req RunRequest) (*RunResponse, error) {
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

func newCfg(model *mockLanguageModel, runner CommandRunner) Config {
	return Config{
		Provider: &mockProvider{model: model},
		Model:    "test",
		Temenos:  runner,
	}
}

// --- tests ---

func TestRun_NilProvider(t *testing.T) {
	cfg := Config{}
	_, err := Run(context.Background(), cfg, nil, "hello", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Provider must not be nil")
}

func TestRun_NilTemenos(t *testing.T) {
	cfg := Config{Provider: &mockProvider{model: &mockLanguageModel{}}}
	_, err := Run(context.Background(), cfg, nil, "hello", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Temenos must not be nil")
}

func TestRun_NoCommand_ReturnsImmediately(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"Here is the answer."}}
	runner := &mockRunner{}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Here is the answer.", result.Response)
	assert.Len(t, result.Steps, 1)
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role)
	assert.Empty(t, runner.calls) // temenos never called when no command issued
}

func TestRun_OneCommandThenDone(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n§ ls -la",
		"The files are: main.go",
	}}
	runner := &mockRunner{response: RunResponse{Stdout: "main.go\ngo.mod"}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "list files", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Let me check.\n", result.Response[:len("Let me check.\n")])
	assert.Contains(t, result.Response, "The files are: main.go")
	assert.Len(t, result.Steps, 3) // command, result, assistant
	assert.Equal(t, StepRoleCommand, result.Steps[0].Role)
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
	assert.True(t, strings.HasPrefix(result.Steps[1].Content, "§ "))
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "ls -la", runner.calls[0].Command) // exact command forwarded unchanged
}

func TestRun_MaxStepsExhausted(t *testing.T) {
	// Each LLM response contains a command, so loop never terminates naturally.
	responses := make([]string, 35)
	for i := range responses {
		responses[i] = "§ echo loop"
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockRunner{response: RunResponse{Stdout: "loop"}}
	cfg := newCfg(model, runner)
	cfg.MaxSteps = 3
	result, err := Run(context.Background(), cfg, nil, "go", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max steps")
	assert.Len(t, runner.calls, 3) // exactly MaxSteps commands executed
	assert.Len(t, result.Steps, 6) // 3 command + 3 result steps
}

func TestRun_SandboxNonZeroExitIncludedInOutput(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"§ false",
		"got it",
	}}
	runner := &mockRunner{response: RunResponse{Stderr: "error msg", ExitCode: 1}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
	assert.True(t, strings.HasPrefix(result.Steps[1].Content, "§ "))
	assert.Contains(t, result.Steps[1].Content, "(exit code: 1)")
	assert.Contains(t, result.Steps[1].Content, "error msg")
}

func TestRun_OnCommandStartCallback(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"§ ls", "done"}}
	runner := &mockRunner{response: RunResponse{Stdout: "file.go"}}
	var called []string
	cbs := Callbacks{OnCommandStart: func(cmd string) { called = append(called, cmd) }}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, called)
}

func TestRun_OnCommandResultCallback(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"§ echo hello", "done"}}
	runner := &mockRunner{response: RunResponse{Stdout: "hello", ExitCode: 0}}
	var events []string
	cbs := Callbacks{
		OnCommandStart: func(cmd string) { events = append(events, "start:"+cmd) },
		OnCommandResult: func(cmd, output string, exitCode int) {
			events = append(events, fmt.Sprintf("result:%s:%s:%d", cmd, output, exitCode))
		},
	}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err)
	require.Len(t, events, 2)
	assert.Equal(t, "start:echo hello", events[0])
	assert.Equal(t, "result:echo hello:hello:0", events[1])
}

func TestRun_OnCommandResultCallback_NonZeroExit(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"§ false", "done"}}
	runner := &mockRunner{response: RunResponse{Stderr: "err msg", ExitCode: 1}}
	var resultOutput string
	var resultExitCode int
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			resultOutput = output
			resultExitCode = exitCode
		},
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err)
	// callback receives raw output without exit code suffix
	// Stdout is empty so output starts with the STDERR separator
	assert.Equal(t, "\nSTDERR:\nerr msg", resultOutput)
	assert.Equal(t, 1, resultExitCode)
	// LLM-facing step content still includes exit code suffix
	assert.Contains(t, result.Steps[1].Content, "(exit code: 1)")
}

func TestRun_OnCommandResultCallback_TransportError(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"§ ls", "done"}}
	runner := &mockRunner{err: fmt.Errorf("socket closed")}
	var callbackOutput string
	var callbackExitCode int
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			callbackOutput = output
			callbackExitCode = exitCode
		},
	}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err) // transport failure is surfaced to LLM, not as Run() error
	assert.Equal(t, -1, callbackExitCode)
	assert.Contains(t, callbackOutput, "execution error:")
}

func TestRun_XMLRetry_RecoversToCommand(t *testing.T) { //nolint:dupl
	// Turn 1: model outputs XML (detected by streaming filter). Turn 2: corrects to ! command. Turn 3: done.
	model := &mockLanguageModel{responses: []string{
		"<invoke name=\"rg\"><parameter name=\"pattern\">foo</parameter></invoke>",
		"§ rg foo /path",
		"Found it.",
	}}
	runner := &mockRunner{response: RunResponse{Stdout: "foo.go:1: foo"}}

	var retryCalls []string
	cbs := Callbacks{
		OnRetry: func(reason string, step int) { retryCalls = append(retryCalls, reason) },
	}

	result, err := Run(context.Background(), newCfg(model, runner), nil, "find foo", cbs)
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Found it.")
	require.Len(t, runner.calls, 1) // command executed exactly once after recovery
	assert.Equal(t, "rg foo /path", runner.calls[0].Command)
	assert.Equal(t, []string{"xml_tool_call"}, retryCalls)

	// Steps: directive (result), ! rg turn (command), result (result), final (assistant)
	// XML assistant turn is NOT in Steps.
	assert.Len(t, result.Steps, 4)
	assert.Equal(t, StepRoleResult, result.Steps[0].Role)
	assert.Contains(t, result.Steps[0].Content, "Your previous output was rejected")
	assert.NotContains(t, result.Steps[0].Content, "<invoke")
	assert.Equal(t, StepRoleCommand, result.Steps[1].Role)
	assert.True(t, strings.HasPrefix(result.Steps[2].Content, "§ ")) // command output
	assert.Equal(t, StepRoleResult, result.Steps[2].Role)
	assert.Equal(t, StepRoleAssistant, result.Steps[3].Role)
	assert.Equal(t, "Found it.", result.Steps[3].Content)
}

func TestRun_XMLRetry_ConsumesNormalSteps(t *testing.T) {
	// Model always returns XML — each retry consumes a normal step, MaxSteps is the cap.
	xmlResponse := "<minimax:tool_call><invoke name=\"rg\"></invoke></minimax:tool_call>"
	responses := make([]string, 10)
	for i := range responses {
		responses[i] = xmlResponse
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockRunner{}

	var retryCalls []string
	cbs := Callbacks{
		OnRetry: func(reason string, step int) { retryCalls = append(retryCalls, reason) },
	}

	cfg := newCfg(model, runner)
	cfg.MaxSteps = 3

	result, err := Run(context.Background(), cfg, nil, "find", cbs)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max steps")
	assert.Empty(t, runner.calls) // runner never called — only XML responses
	assert.NotNil(t, result)      // result returned for observability
	assert.Len(t, retryCalls, 3)
	for _, r := range retryCalls {
		assert.Equal(t, "xml_tool_call", r)
	}
}

func TestRun_XMLRetry_ThinkTagStripped(t *testing.T) {
	// Model outputs think tags — tag strings are stripped, no retry triggered.
	// Note: only the tag markers are removed from OnDelta; inter-tag content
	// (e.g. "reasoning") passes through unchanged. Raw LLM output in Steps is unaffected.
	model := &mockLanguageModel{responses: []string{
		"<think>reasoning</think>Here is the result",
	}}
	runner := &mockRunner{}
	var deltaOutput string
	cbs := Callbacks{
		OnDelta: func(text string) { deltaOutput += text },
		OnRetry: func(reason string, step int) { panic("OnRetry should not be called") },
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err)
	// Full text in Steps still includes think tags (raw LLM output)
	assert.Contains(t, result.Steps[0].Content, "<think>")
	// OnDelta received text with tag markers stripped (not the inter-tag content)
	assert.NotContains(t, deltaOutput, "<think>")
	assert.Contains(t, deltaOutput, "Here is the result")
}

func TestRun_MultiCommand_ExecutesAll(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n§ pwd\n§ ls -la",
		"Found the files.",
	}}
	runner := &mockRunner{response: RunResponse{Stdout: "ok"}}
	var resultCmds []string
	cbs := Callbacks{
		OnCommandStart:  func(cmd string) {},
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
		"§ false\n§ echo ok",
		"Got it.",
	}}
	runner := &mockRunner{response: RunResponse{Stderr: "error", ExitCode: 1}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	cmdStep := result.Steps[1]
	assert.Equal(t, StepRoleResult, cmdStep.Role)
	assert.Contains(t, cmdStep.Content, "(exit code: 1)")
}

func TestRun_MultiCommand_WithHeredoc(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"§ cat <<'EOF'\nhello\nEOF\n§ ls -la",
		"Done.",
	}}
	runner := &mockRunner{response: RunResponse{Stdout: "ok"}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Done.")
	require.Len(t, runner.calls, 2)
	assert.Equal(t, "cat <<'EOF'\nhello\nEOF", runner.calls[0].Command)
	assert.Equal(t, "ls -la", runner.calls[1].Command)
}

func TestRun_MultiCommand_OnCommandStartPerCommand(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"§ pwd\n§ ls\n§ echo hi",
		"All done.",
	}}
	runner := &mockRunner{response: RunResponse{Stdout: "ok"}}
	var started []string
	cbs := Callbacks{OnCommandStart: func(cmd string) { started = append(started, cmd) }}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "go", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"pwd", "ls", "echo hi"}, started)
}

func TestRun_ConsecutiveCommands_SoftWarning(t *testing.T) {
	responses := make([]string, 12)
	for i := 0; i < 11; i++ {
		responses[i] = fmt.Sprintf("§ echo step%d", i)
	}
	responses[11] = "Done."
	model := &mockLanguageModel{responses: responses}
	runner := &mockRunner{response: RunResponse{Stdout: "ok"}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	var warningCount int
	for _, s := range result.Steps {
		if s.Role == StepRoleResult && strings.Contains(s.Content, "without explaining") {
			warningCount++
		}
	}
	assert.Equal(t, 1, warningCount, "soft warning should fire exactly once at SoftWarningThreshold")
}

func TestRun_ConsecutiveCommands_TextResponseTerminatesLoop(t *testing.T) {
	responses := []string{
		"§ echo 1", "§ echo 2", "§ echo 3", "§ echo 4", "§ echo 5",
		"Halfway.",
		"§ echo 6", "§ echo 7", "§ echo 8", "§ echo 9", "§ echo 10",
		"Done.",
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockRunner{response: RunResponse{Stdout: "ok"}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	for _, s := range result.Steps {
		if s.Role == StepRoleResult {
			assert.NotContains(t, s.Content, "without explaining",
				"no soft warning — counter resets on text")
		}
	}
}

func TestRun_HeredocCommand_FullBlockSentToRunner(t *testing.T) {
	// Model issues a heredoc command — runner must receive the complete multi-line block.
	model := &mockLanguageModel{responses: []string{
		"§ cat <<'EOF'\nline1\nline2\nEOF",
		"Created.",
	}}
	runner := &mockRunner{response: RunResponse{Stdout: "ok"}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "write file", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Created.")
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "cat <<'EOF'\nline1\nline2\nEOF", runner.calls[0].Command)
}

// TestRun_HttpServer_JsonEncodingRoundtrip verifies that the real temenos client
// correctly encodes requests and decodes responses end-to-end over a unix socket.
func TestRun_HttpServer_JsonEncodingRoundtrip(t *testing.T) {
	var receivedCmd string
	tc := newTestTemenosServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req RunRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedCmd = req.Command
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RunResponse{Stdout: "ok", ExitCode: 0}) //nolint:errcheck
	})
	model := &mockLanguageModel{responses: []string{"§ echo hi", "done"}}
	cfg := newCfg(model, tc)
	_, err := Run(context.Background(), cfg, nil, "test", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "echo hi", receivedCmd)
}

package logos

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tta-lab/temenos/client"
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
	response client.RunResponse
	err      error
	calls    []client.RunRequest
}

func (m *mockRunner) Run(_ context.Context, req client.RunRequest) (*client.RunResponse, error) {
	m.calls = append(m.calls, req)
	if m.err != nil {
		return nil, m.err
	}
	resp := m.response
	return &resp, nil
}

// newTestTemenosServer starts a fake temenos HTTP server over a unix socket.
// Uses os.MkdirTemp with a short prefix to avoid macOS unix socket path length limit (104 chars).
func newTestTemenosServer(t *testing.T, handler http.HandlerFunc) *client.Client {
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
	tc, err := client.New(sockPath)
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
		"Let me check.\n$ ls -la",
		"The files are: main.go",
	}}
	runner := &mockRunner{response: client.RunResponse{Stdout: "main.go\ngo.mod"}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "list files", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Let me check.\n", result.Response[:len("Let me check.\n")])
	assert.Contains(t, result.Response, "The files are: main.go")
	assert.Len(t, result.Steps, 3) // assistant, command, assistant
	assert.Equal(t, StepRoleCommand, result.Steps[1].Role)
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "ls -la", runner.calls[0].Command) // exact command forwarded unchanged
}

func TestRun_MaxStepsExhausted(t *testing.T) {
	// Each LLM response contains a command, so loop never terminates naturally.
	responses := make([]string, 35)
	for i := range responses {
		responses[i] = "$ echo loop"
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockRunner{response: client.RunResponse{Stdout: "loop"}}
	cfg := newCfg(model, runner)
	cfg.MaxSteps = 3
	result, err := Run(context.Background(), cfg, nil, "go", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max steps")
	assert.Len(t, runner.calls, 3) // exactly MaxSteps commands executed
	assert.Len(t, result.Steps, 6) // 3 assistant + 3 command steps
}

func TestRun_SandboxNonZeroExitIncludedInOutput(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"$ false",
		"got it",
	}}
	runner := &mockRunner{response: client.RunResponse{Stderr: "error msg", ExitCode: 1}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, StepRoleCommand, result.Steps[1].Role)
	assert.Contains(t, result.Steps[1].Content, "(exit code: 1)")
	assert.Contains(t, result.Steps[1].Content, "error msg")
}

func TestRun_OnCommandStartCallback(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"$ ls", "done"}}
	runner := &mockRunner{response: client.RunResponse{Stdout: "file.go"}}
	var called []string
	cbs := Callbacks{OnCommandStart: func(cmd string) { called = append(called, cmd) }}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, called)
}

// TestRun_HttpServer_JsonEncodingRoundtrip verifies that the real client.Client
// correctly encodes requests and decodes responses end-to-end over a unix socket.
func TestRun_HttpServer_JsonEncodingRoundtrip(t *testing.T) {
	var receivedCmd string
	tc := newTestTemenosServer(t, func(w http.ResponseWriter, r *http.Request) {
		var req client.RunRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		receivedCmd = req.Command
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.RunResponse{Stdout: "ok", ExitCode: 0}) //nolint:errcheck
	})
	model := &mockLanguageModel{responses: []string{"$ echo hi", "done"}}
	cfg := newCfg(model, tc)
	_, err := Run(context.Background(), cfg, nil, "test", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "echo hi", receivedCmd)
}

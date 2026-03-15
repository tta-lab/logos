package logos

import (
	"context"
	"fmt"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tta-lab/logos/sandbox"
)

// --- mocks ---

type mockProvider struct {
	model *mockLanguageModel
}

func (p *mockProvider) Name() string { return "mock" }
func (p *mockProvider) LanguageModel(_ context.Context, _ string) (fantasy.LanguageModel, error) {
	return p.model, nil
}

type mockLanguageModel struct {
	responses []string // each call to Stream returns the next response
	call      int
}

func (m *mockLanguageModel) Provider() string { return "mock" }
func (m *mockLanguageModel) Model() string    { return "mock" }
func (m *mockLanguageModel) Generate(_ context.Context, _ fantasy.Call) (*fantasy.Response, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModel) GenerateObject(_ context.Context, _ fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, fmt.Errorf("not implemented")
}
func (m *mockLanguageModel) StreamObject(_ context.Context, _ fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
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

type mockSandbox struct {
	output   string
	stderr   string
	exitCode int
	err      error
	calls    []string
}

func (s *mockSandbox) Exec(_ context.Context, command string, _ *sandbox.ExecConfig) (string, string, int, error) {
	s.calls = append(s.calls, command)
	return s.output, s.stderr, s.exitCode, s.err
}

func (s *mockSandbox) IsAvailable() bool { return true }

func newCfg(model *mockLanguageModel, sb sandbox.Sandbox) Config {
	return Config{
		Provider: &mockProvider{model: model},
		Model:    "test",
		Sandbox:  sb,
	}
}

// --- tests ---

func TestRun_NilProvider(t *testing.T) {
	cfg := Config{Sandbox: &mockSandbox{}}
	_, err := Run(context.Background(), cfg, nil, "hello", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Provider must not be nil")
}

func TestRun_NilSandbox(t *testing.T) {
	cfg := Config{Provider: &mockProvider{model: &mockLanguageModel{}}}
	_, err := Run(context.Background(), cfg, nil, "hello", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Sandbox must not be nil")
}

func TestRun_InvalidAllowedPath(t *testing.T) {
	model := &mockLanguageModel{}
	sb := &mockSandbox{}
	cfg := newCfg(model, sb)
	cfg.AllowedPaths = []string{"relative/path"}
	_, err := Run(context.Background(), cfg, nil, "hello", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "AllowedPaths entry")
}

func TestRun_NoCommand_ReturnsImmediately(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"Here is the answer."}}
	sb := &mockSandbox{}
	result, err := Run(context.Background(), newCfg(model, sb), nil, "question", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Here is the answer.", result.Response)
	assert.Len(t, result.Steps, 1)
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role)
	assert.Empty(t, sb.calls)
}

func TestRun_OneCommandThenDone(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n$ ls -la",
		"The files are: main.go",
	}}
	sb := &mockSandbox{output: "main.go\ngo.mod"}
	result, err := Run(context.Background(), newCfg(model, sb), nil, "list files", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Let me check.\n", result.Response[:len("Let me check.\n")])
	assert.Contains(t, result.Response, "The files are: main.go")
	assert.Len(t, result.Steps, 3) // assistant, command, assistant
	assert.Equal(t, StepRoleCommand, result.Steps[1].Role)
	assert.Equal(t, []string{"ls -la"}, sb.calls)
}

func TestRun_MaxStepsExhausted(t *testing.T) {
	// Each LLM response contains a command, so loop never terminates naturally.
	responses := make([]string, 35)
	for i := range responses {
		responses[i] = "$ echo loop"
	}
	model := &mockLanguageModel{responses: responses}
	sb := &mockSandbox{output: "loop"}
	cfg := newCfg(model, sb)
	cfg.MaxSteps = 3
	_, err := Run(context.Background(), cfg, nil, "go", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max steps")
	assert.Len(t, sb.calls, 3)
}

func TestRun_SandboxNonZeroExitIncludedInOutput(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"$ false",
		"got it",
	}}
	sb := &mockSandbox{output: "", stderr: "error msg", exitCode: 1}
	result, err := Run(context.Background(), newCfg(model, sb), nil, "run", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, StepRoleCommand, result.Steps[1].Role)
	assert.Contains(t, result.Steps[1].Content, "(exit code: 1)")
	assert.Contains(t, result.Steps[1].Content, "error msg")
}

func TestRun_OnCommandStartCallback(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"$ ls", "done"}}
	sb := &mockSandbox{output: "file.go"}
	var called []string
	cbs := Callbacks{OnCommandStart: func(cmd string) { called = append(called, cmd) }}
	_, err := Run(context.Background(), newCfg(model, sb), nil, "q", cbs)
	require.NoError(t, err)
	assert.Equal(t, []string{"ls"}, called)
}

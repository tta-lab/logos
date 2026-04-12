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
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"})                     //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: m.reasoning}) //nolint:errcheck
		yield(fantasy.StreamPart{                                                                          //nolint:errcheck
			Type: fantasy.StreamPartTypeReasoningDelta,
			ID:   "0",
			ProviderMetadata: fantasy.ProviderMetadata{
				anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: m.signature},
			},
		})
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"})    //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: m.text}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})
	}, nil
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
	var callbackFired bool
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			callbackFired = true
		},
	}
	result, err := Run(context.Background(), withTestRunner(newCfg(model), runner), nil, "edit file", cbs)
	require.NoError(t, err)
	// Runner should NOT have been called
	assert.Empty(t, runner.calls, "blocked command should not reach runner")
	// OnCommandResult should NOT fire
	assert.False(t, callbackFired, "OnCommandResult should not fire for blocked commands")
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

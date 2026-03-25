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

// mockBlockRunner implements BlockRunner for unit tests.
type mockBlockRunner struct {
	response RunBlockResponse
	err      error
	calls    []RunBlockRequest
}

func (m *mockBlockRunner) RunBlock(_ context.Context, req RunBlockRequest) (*RunBlockResponse, error) {
	m.calls = append(m.calls, req)
	if m.err != nil {
		return nil, m.err
	}
	resp := m.response
	return &resp, nil
}

// newTestTemenosServer starts a fake temenos HTTP server over a unix socket.
// Uses os.MkdirTemp with a short prefix to avoid macOS unix socket path length limit (104 chars).
func newTestTemenosServer(t *testing.T, handler http.HandlerFunc) BlockRunner {
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

func newCfg(model fantasy.LanguageModel, runner BlockRunner) Config {
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
	reasoning := m.reasoning
	sig := m.signature
	text := m.text
	return func(yield func(fantasy.StreamPart) bool) {
		// Emit reasoning start
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningStart, ID: "0"}) {
			return
		}
		// Emit reasoning delta
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningDelta, ID: "0", Delta: reasoning}) {
			return
		}
		// Emit signature via signature_delta pattern (empty Delta, ProviderMetadata with sig)
		if !yield(fantasy.StreamPart{
			Type: fantasy.StreamPartTypeReasoningDelta,
			ID:   "0",
			ProviderMetadata: fantasy.ProviderMetadata{
				anthropic.Name: &anthropic.ReasoningOptionMetadata{Signature: sig},
			},
		}) {
			return
		}
		// Emit reasoning end
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"}) {
			return
		}
		// Emit text delta
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: text}) {
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})
	}, nil
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
	runner := &mockBlockRunner{}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Here is the answer.", result.Response)
	assert.Len(t, result.Steps, 1)
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role)
	assert.Empty(t, runner.calls) // temenos never called when no command issued
}

func TestRun_OneCommandThenDone(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"Let me check.\n<cmd>\n§ ls -la\n</cmd>",
		"The files are: main.go",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "ls -la", Stdout: "main.go\ngo.mod"},
	}}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "list files", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "Let me check.\n", result.Response[:len("Let me check.\n")])
	assert.Contains(t, result.Response, "The files are: main.go")
	assert.Len(t, result.Steps, 3) // assistant, result, assistant
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role)
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
	assert.True(t, strings.HasPrefix(result.Steps[1].Content, "<result>\n§ "))
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "\n§ ls -la\n", runner.calls[0].Block) // raw block content forwarded
}

func TestRun_MaxStepsExhausted(t *testing.T) {
	// Each LLM response contains a command, so loop never terminates naturally.
	responses := make([]string, 35)
	for i := range responses {
		responses[i] = "<cmd>\n§ echo loop\n</cmd>"
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "echo loop", Stdout: "loop"},
	}}}
	cfg := newCfg(model, runner)
	cfg.MaxSteps = 3
	result, err := Run(context.Background(), cfg, nil, "go", Callbacks{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "max steps")
	assert.Len(t, runner.calls, 3) // exactly MaxSteps blocks executed
	assert.Len(t, result.Steps, 6) // 3 assistant + 3 result steps
}

func TestRun_SandboxNonZeroExitIncludedInOutput(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\n§ false\n</cmd>",
		"got it",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "false", Stderr: "error msg", ExitCode: 1},
	}}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
	assert.True(t, strings.HasPrefix(result.Steps[1].Content, "<result>\n§ "))
	assert.Contains(t, result.Steps[1].Content, "(exit code: 1)")
	assert.Contains(t, result.Steps[1].Content, "error msg")
}

func TestRun_OnCommandResultCallback(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\n§ echo hello\n</cmd>", "done"}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "echo hello", Stdout: "hello", ExitCode: 0},
	}}}
	var events []string
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			events = append(events, fmt.Sprintf("result:%s:%s:%d", cmd, output, exitCode))
		},
	}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err)
	require.Len(t, events, 1)
	assert.Equal(t, "result:echo hello:hello:0", events[0])
}

func TestRun_OnCommandResultCallback_NonZeroExit(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\n§ false\n</cmd>", "done"}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "false", Stderr: "err msg", ExitCode: 1},
	}}}
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

func TestRun_XMLRetry_RecoversToCommand(t *testing.T) { //nolint:dupl
	// Turn 1: model outputs XML (detected by streaming filter). Turn 2: corrects to § command. Turn 3: done.
	model := &mockLanguageModel{responses: []string{
		"<invoke name=\"rg\"><parameter name=\"pattern\">foo</parameter></invoke>",
		"<cmd>\n§ rg foo /path\n</cmd>",
		"Found it.",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "rg foo /path", Stdout: "foo.go:1: foo"},
	}}}

	var retryCalls []string
	cbs := Callbacks{
		OnRetry: func(reason string, step int) { retryCalls = append(retryCalls, reason) },
	}

	result, err := Run(context.Background(), newCfg(model, runner), nil, "find foo", cbs)
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Found it.")
	require.Len(t, runner.calls, 1) // block executed exactly once after recovery
	assert.Equal(t, "\n§ rg foo /path\n", runner.calls[0].Block)
	assert.Equal(t, []string{"tool_call"}, retryCalls)

	// Steps: bad_assistant (assistant), directive (result), rg turn (assistant), result (result), final (assistant)
	// Wrong assistant message IS now included in Steps for conversation restoration.
	assert.Len(t, result.Steps, 5)
	assert.Equal(t, StepRoleAssistant, result.Steps[0].Role) // the hallucinated XML output
	assert.Equal(t, StepRoleResult, result.Steps[1].Role)
	assert.Contains(t, result.Steps[1].Content, "Unprocessed")
	assert.NotContains(t, result.Steps[1].Content, "<invoke")
	assert.Equal(t, StepRoleAssistant, result.Steps[2].Role)
	assert.True(t, strings.HasPrefix(result.Steps[3].Content, "<result>\n§ ")) // command output
	assert.Equal(t, StepRoleResult, result.Steps[3].Role)
	assert.Equal(t, StepRoleAssistant, result.Steps[4].Role)
	assert.Equal(t, "Found it.", result.Steps[4].Content)
}

func TestRun_XMLRetry_ConsumesNormalSteps(t *testing.T) {
	// Model always returns XML — each retry consumes a normal step, MaxSteps is the cap.
	xmlResponse := "<minimax:tool_call><invoke name=\"rg\"></invoke></minimax:tool_call>"
	responses := make([]string, 10)
	for i := range responses {
		responses[i] = xmlResponse
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockBlockRunner{}

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
		assert.Equal(t, "tool_call", r)
	}
}

func TestRun_XMLRetry_ThinkTagStripped(t *testing.T) {
	// Model outputs think tags — tag strings are stripped, no retry triggered.
	// Note: only the tag markers are removed from OnDelta; inter-tag content
	// (e.g. "reasoning") passes through unchanged. Raw LLM output in Steps is unaffected.
	model := &mockLanguageModel{responses: []string{
		"<think>reasoning</think>Here is the result",
	}}
	runner := &mockBlockRunner{}
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
		"Let me check.\n<cmd>\n§ pwd\n§ ls -la\n</cmd>",
		"Found the files.",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "pwd", Stdout: "ok"},
		{Command: "ls -la", Stdout: "ok"},
	}}}
	var resultCmds []string
	cbs := Callbacks{
		OnCommandResult: func(cmd string, output string, exitCode int) { resultCmds = append(resultCmds, cmd) },
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "check", cbs)
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Found the files.")
	require.Len(t, runner.calls, 1) // one block with two commands = one RunBlock call
	assert.Equal(t, "\n§ pwd\n§ ls -la\n", runner.calls[0].Block)
	assert.Equal(t, []string{"pwd", "ls -la"}, resultCmds)
}

func TestRun_MultiCommand_ExitCodeFormatted(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\n§ false\n§ echo ok\n</cmd>",
		"Got it.",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "false", Stderr: "error", ExitCode: 1},
		{Command: "echo ok", Stderr: "error", ExitCode: 1},
	}}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "run", Callbacks{})
	require.NoError(t, err)
	cmdStep := result.Steps[1]
	assert.Equal(t, StepRoleResult, cmdStep.Role)
	assert.Contains(t, cmdStep.Content, "(exit code: 1)")
}

func TestRun_MultiCommand_WithHeredoc(t *testing.T) {
	model := &mockLanguageModel{responses: []string{
		"<cmd>\n§ cat <<'EOF'\nhello\nEOF\n§ ls -la\n</cmd>",
		"Done.",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "cat <<'EOF'\nhello\nEOF", Stdout: "ok"},
		{Command: "ls -la", Stdout: "ok"},
	}}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "go", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Done.")
	require.Len(t, runner.calls, 1) // one block with two commands = one RunBlock call
	assert.Equal(t, "\n§ cat <<'EOF'\nhello\nEOF\n§ ls -la\n", runner.calls[0].Block)
}

func TestRun_ConsecutiveCommands_NoWarningInjected(t *testing.T) {
	// Verify no soft warning is injected regardless of consecutive command count
	// (SoftWarningThreshold removed — no narration nagging).
	responses := []string{
		"<cmd>\n§ echo 1\n</cmd>", "<cmd>\n§ echo 2\n</cmd>", "<cmd>\n§ echo 3\n</cmd>",
		"<cmd>\n§ echo 4\n</cmd>", "<cmd>\n§ echo 5\n</cmd>",
		"Halfway.",
		"<cmd>\n§ echo 6\n</cmd>", "<cmd>\n§ echo 7\n</cmd>", "<cmd>\n§ echo 8\n</cmd>",
		"<cmd>\n§ echo 9\n</cmd>", "<cmd>\n§ echo 10\n</cmd>",
		"Done.",
	}
	model := &mockLanguageModel{responses: responses}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "echo", Stdout: "ok"},
	}}}
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
	// Model issues a heredoc command — runner must receive the complete raw block.
	model := &mockLanguageModel{responses: []string{
		"<cmd>\n§ cat <<'EOF'\nline1\nline2\nEOF\n</cmd>",
		"Created.",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "cat <<'EOF'\nline1\nline2\nEOF", Stdout: "ok"},
	}}}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "write file", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "Created.")
	require.Len(t, runner.calls, 1)
	assert.Equal(t, "\n§ cat <<'EOF'\nline1\nline2\nEOF\n", runner.calls[0].Block)
}

// TestRun_HttpServer_JsonEncodingRoundtrip verifies that the real temenos client
// correctly encodes requests and decodes responses end-to-end over a unix socket.
// Also verifies that Config.Prefix is forwarded correctly in the request.
func TestRun_HttpServer_JsonEncodingRoundtrip(t *testing.T) {
	var receivedReq RunBlockRequest
	tc := newTestTemenosServer(t, func(w http.ResponseWriter, r *http.Request) {
		require.NoError(t, json.NewDecoder(r.Body).Decode(&receivedReq))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(RunBlockResponse{Results: []CommandResult{ //nolint:errcheck
			{Command: "echo hi", Stdout: "ok", ExitCode: 0},
		}})
	})
	model := &mockLanguageModel{responses: []string{"<cmd>\n§ echo hi\n</cmd>", "done"}}
	cfg := newCfg(model, tc)
	_, err := Run(context.Background(), cfg, nil, "test", Callbacks{})
	require.NoError(t, err)
	assert.Equal(t, "\n§ echo hi\n", receivedReq.Block)
	assert.Equal(t, "§ ", receivedReq.Prefix) // default prefix forwarded to temenos
}

func TestRun_RunBlockTransportError_CallbackNotFired(t *testing.T) {
	// Verifies the documented behavioral change: transport errors (RunBlock fails entirely)
	// do NOT fire OnCommandResult — there's no meaningful command/exitCode to report.
	// The error text is still surfaced to the LLM via the <result> step.
	model := &mockLanguageModel{responses: []string{"<cmd>\n§ ls\n</cmd>", "done"}}
	runner := &mockBlockRunner{err: fmt.Errorf("socket closed")}
	var callbackFired bool
	cbs := Callbacks{
		OnCommandResult: func(cmd, output string, exitCode int) {
			callbackFired = true
		},
	}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "q", cbs)
	require.NoError(t, err) // transport failure is surfaced to LLM, not as Run() error
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
	runner := &mockBlockRunner{}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	require.Len(t, result.Steps, 1)
	assert.Equal(t, "Let me think...", result.Steps[0].Reasoning)
	assert.Equal(t, "sig123", result.Steps[0].ReasoningSignature)
	assert.Equal(t, "The answer is 42.", result.Steps[0].Content)
}

func TestRun_NoReasoning_BackwardCompat(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"simple answer"}}
	runner := &mockBlockRunner{}
	result, err := Run(context.Background(), newCfg(model, runner), nil, "question", Callbacks{})
	require.NoError(t, err)
	assert.Empty(t, result.Steps[0].Reasoning)
	assert.Empty(t, result.Steps[0].ReasoningSignature)
}
func TestRun_OnDelta_IncludesCmdBlockContent(t *testing.T) {
	// Verify that <cmd> block content IS passed to OnDelta as a complete atomic chunk.
	// Consumers (TUI, iOS) rely on receiving complete <cmd>...</cmd> blocks to render them.
	model := &mockLanguageModel{responses: []string{
		"Before block.\n<cmd>\n§ ls\n</cmd>",
		"After command.",
	}}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "ls", Stdout: "file.txt", ExitCode: 0},
	}}}
	var deltas []string
	cbs := Callbacks{OnDelta: func(text string) { deltas = append(deltas, text) }}
	_, err := Run(context.Background(), newCfg(model, runner), nil, "list files", cbs)
	require.NoError(t, err)
	combined := strings.Join(deltas, "")
	assert.Contains(t, combined, "Before block.")
	// Block must arrive as one atomic chunk, not split across multiple OnDelta calls
	assert.Contains(t, deltas, "<cmd>\n§ ls\n</cmd>", "block should be emitted as one atomic chunk")
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
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeReasoningEnd, ID: "0"})                   //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "<cmd>\n§ ls\n</cmd>"}) //nolint:errcheck
			yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                                  //nolint:errcheck
		}, nil
	}
	return func(yield func(fantasy.StreamPart) bool) {
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, Delta: "done"}) //nolint:errcheck
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish})                   //nolint:errcheck
	}, nil
}

func TestRun_ReasoningCaptured_WithCommand(t *testing.T) {
	// Verify reasoning is captured on the intermediate (command-issuing) step, not just
	// the terminal step. This exercises the newAssistantStep path inside the "has commands" branch.
	model := &mockLanguageModelTwoTurnsReasoning{
		reasoning: "I should check files first.",
		signature: "sigABC",
	}
	runner := &mockBlockRunner{response: RunBlockResponse{Results: []CommandResult{
		{Command: "ls", Stdout: "main.go", ExitCode: 0},
	}}}
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

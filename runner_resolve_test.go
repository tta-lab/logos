package logos

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tta-lab/temenos/client"
)

// newMockTemenosServer starts a fake temenos HTTP server over a unix socket
// that returns a 200 OK with a valid RunResponse.
func newMockTemenosServer(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "tm")
	require.NoError(t, err)
	t.Cleanup(func() { os.RemoveAll(dir) }) //nolint:errcheck
	sockPath := filepath.Join(dir, "t.sock")
	ln, err := net.Listen("unix", sockPath)
	require.NoError(t, err)
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req client.RunRequest
		json.NewDecoder(r.Body).Decode(&req) //nolint:errcheck
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(client.RunResponse{Stdout: "ok", ExitCode: 0}) //nolint:errcheck
	})}
	go srv.Serve(ln) //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	return sockPath
}

func TestResolveRunner_SandboxFalse(t *testing.T) {
	cfg := Config{Sandbox: false}
	r, err := resolveRunner(&cfg)
	require.NoError(t, err)
	assert.NotNil(t, r)
	// Verify it's a localRunner by checking it handles a real command.
	resp, err := r.Run(context.Background(), client.RunRequest{Command: "echo hello"})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", resp.Stdout)
	assert.Equal(t, 0, resp.ExitCode)
}

func TestResolveRunner_SandboxTrue_Reachable(t *testing.T) {
	sockPath := newMockTemenosServer(t)
	cfg := Config{Sandbox: true, SandboxAddr: sockPath}
	r, err := resolveRunner(&cfg)
	require.NoError(t, err)
	assert.NotNil(t, r)
	// Verify it works by running a command through it.
	resp, err := r.Run(context.Background(), client.RunRequest{Command: "echo hello"})
	require.NoError(t, err)
	assert.Equal(t, "ok", resp.Stdout)
}

func TestResolveRunner_SandboxTrue_RunFailsUnreachable(t *testing.T) {
	// newClient does not fail on unreachable socket — the connection is
	// deferred until Run(). We verify that Run() returns an error.
	cfg := Config{Sandbox: true, SandboxAddr: "/nonexistent/socket.sock"}
	r, err := resolveRunner(&cfg)
	require.NoError(t, err)
	assert.NotNil(t, r)
	// The actual connection error surfaces on Run.
	_, err = r.Run(context.Background(), client.RunRequest{Command: "echo hello"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "temenos")
}

// TestRun_Sandbox_False_UsesLocalRunner is an end-to-end test verifying
// that logos.Run uses the localRunner when Config.Sandbox is false.
func TestRun_Sandbox_False_UsesLocalRunner(t *testing.T) {
	model := &mockLanguageModel{responses: []string{"<cmd>\necho hello_world\n</cmd>", "done"}}
	cfg := Config{
		Provider:   &mockProvider{model: model},
		Model:      "test",
		Sandbox:    false,
		testRunner: nil, // no test runner — use real localRunner
	}
	result, err := Run(context.Background(), cfg, nil, "run", Callbacks{})
	require.NoError(t, err)
	assert.Contains(t, result.Response, "done")
}

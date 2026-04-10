package logos

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tta-lab/temenos/client"
)

func TestLocalRunner_BasicEcho(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "echo hello",
	})
	require.NoError(t, err)
	assert.Equal(t, "hello\n", resp.Stdout)
	assert.Equal(t, 0, resp.ExitCode)
	assert.Empty(t, resp.Stderr)
}

func TestLocalRunner_NonZeroExit(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "exit 3",
	})
	require.NoError(t, err)
	assert.Equal(t, 3, resp.ExitCode)
}

func TestLocalRunner_StderrCapture(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "printf err >&2",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "err")
}

func TestLocalRunner_CwdHonored(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "pwd",
		AllowedPaths: []client.AllowedPath{
			{Path: "/tmp"},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.ExitCode)
	assert.Equal(t, "/tmp\n", resp.Stdout)
}

func TestLocalRunner_EnvPassed(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "echo $FOO",
		Env:     map[string]string{"FOO": "bar"},
	})
	require.NoError(t, err)
	assert.Equal(t, "bar\n", resp.Stdout)
}

func TestLocalRunner_ContextCancellation(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	resp, err := r.Run(ctx, client.RunRequest{
		Command: "sleep 10",
	})
	assert.Error(t, err)
	assert.True(t, errors.Is(err, context.Canceled), "expected context.Canceled, got: %v", err)
	assert.Nil(t, resp)
}

func TestLocalRunner_EmptyCommand(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "",
	})
	require.NoError(t, err)
	assert.Equal(t, 0, resp.ExitCode)
}

func TestLocalRunner_WindowsError(t *testing.T) {
	if !isWindows {
		t.Skip("only relevant on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "echo hello",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unsupported on windows")
	assert.Nil(t, resp)
}

func TestLocalRunner_CommandNotFound(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	resp, err := r.Run(context.Background(), client.RunRequest{
		Command: "nosuchcmd",
	})
	require.NoError(t, err)
	assert.Equal(t, 127, resp.ExitCode)
	assert.Contains(t, resp.Stderr, "not found")
}

func TestLocalRunner_TimeoutDeadline(t *testing.T) {
	if isWindows {
		t.Skip("localRunner unsupported on windows")
	}
	r := &localRunner{}
	ctx := context.Background()
	// Timeout of 1 second on a command that sleeps for 10.
	resp, err := r.Run(ctx, client.RunRequest{
		Command: "sleep 10",
		Timeout: 1, // 1 second
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "deadline exceeded")
	assert.Nil(t, resp)
}

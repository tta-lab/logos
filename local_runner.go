package logos

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"runtime"
	"time"

	"github.com/tta-lab/temenos/client"
)

// localRunner executes commands by spawning /bin/bash directly.
// It is used when Config.Sandbox is false (local exec mode).
//
// Timeout is honored via context deadline wrapping the caller's ctx.
// AllowedPaths[0].Path is used as cmd.Dir; additional entries are ignored
// (no RO enforcement without OS-level primitives).
// Network and other sandbox fields are ignored — local exec has no sandbox concept.
type localRunner struct{}

// isWindows is true when running on Windows.
const isWindows = runtime.GOOS == "windows"

func (l *localRunner) Run(ctx context.Context, req client.RunRequest) (*client.RunResponse, error) {
	if isWindows {
		return nil, errors.New("logos: localRunner unsupported on windows")
	}

	// Honor Timeout via context deadline.
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(req.Timeout)*time.Second)
		defer cancel()
	}

	cmd := exec.CommandContext(ctx, "/bin/bash", "-c", req.Command)
	if len(req.AllowedPaths) > 0 {
		cmd.Dir = req.AllowedPaths[0].Path
	}
	if len(req.Env) > 0 {
		cmd.Env = envMapToSlice(req.Env)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err == nil {
		return &client.RunResponse{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: 0}, nil
	}
	// Context cancellation/deadline: return nil response with ctx.Err().
	// The nil response signals cancellation to the caller (matching temenos semantics).
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &client.RunResponse{
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitErr.ExitCode(),
		}, nil
	}
	return &client.RunResponse{Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1}, err
}

// envMapToSlice converts a map[string]string to a []string in "key=value" form.
func envMapToSlice(env map[string]string) []string {
	out := make([]string, 0, len(env))
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	return out
}

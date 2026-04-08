package logos

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner implements CommandRunner for testing.
type fakeRunner struct {
	sleepDur time.Duration
	err      error
	exitCode int
	stderr   string
	reqsMu   sync.Mutex
	reqs     []RunRequest
	maxCon   atomic.Int32
}

func (f *fakeRunner) Run(ctx context.Context, req RunRequest) (_ *RunResponse, _ error) {
	f.maxCon.Add(1)
	defer f.maxCon.Add(-1)

	if f.sleepDur > 0 {
		time.Sleep(f.sleepDur)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	f.reqsMu.Lock()
	f.reqs = append(f.reqs, req)
	f.reqsMu.Unlock()
	if f.err != nil {
		return nil, f.err
	}
	return &RunResponse{
		Stdout:   req.Command + " output",
		Stderr:   f.stderr,
		ExitCode: f.exitCode,
	}, nil
}

func (f *fakeRunner) MaxConcurrent() int32 { return f.maxCon.Load() }

func (f *fakeRunner) calls() []RunRequest {
	f.reqsMu.Lock()
	defer f.reqsMu.Unlock()
	out := make([]RunRequest, len(f.reqs))
	copy(out, f.reqs)
	return out
}

func TestExecuteBlocks(t *testing.T) {
	t.Run("zero cmds returns nil", func(t *testing.T) {
		runner := &fakeRunner{}
		results := ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, nil)
		assert.Nil(t, results)
	})

	t.Run("one cmd", func(t *testing.T) {
		runner := &fakeRunner{}
		results := ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, []string{"echo hi"})
		require.Len(t, results, 1)
		assert.Equal(t, "echo hi", results[0].Command)
		assert.Equal(t, "echo hi output", results[0].Stdout)
		assert.Equal(t, 0, results[0].ExitCode)
		assert.NoError(t, results[0].Err)
	})

	t.Run("results in input order despite completion order", func(t *testing.T) {
		runner := &fakeRunner{sleepDur: 50 * time.Millisecond}
		cmds := []string{"slowest", "medium", "fastest"}
		results := ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, cmds)
		require.Len(t, results, 3)
		assert.Equal(t, "slowest", results[0].Command)
		assert.Equal(t, "medium", results[1].Command)
		assert.Equal(t, "fastest", results[2].Command)
	})

	t.Run("runner error", func(t *testing.T) {
		runner := &fakeRunner{err: assert.AnError}
		results := ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, []string{"bad"})
		require.Len(t, results, 1)
		assert.Error(t, results[0].Err)
	})

	t.Run("non-zero exit code captured", func(t *testing.T) {
		runner := &fakeRunner{exitCode: 42}
		results := ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, []string{"false"})
		require.Len(t, results, 1)
		assert.Equal(t, 42, results[0].ExitCode)
	})

	t.Run("AllowedPaths propagated", func(t *testing.T) {
		runner := &fakeRunner{}
		paths := []AllowedPath{{Path: "/tmp", ReadOnly: true}}
		results := ExecuteBlocks(context.Background(), ExecConfig{
			Runner:       runner,
			AllowedPaths: paths,
		}, []string{"ls"})
		require.Len(t, results, 1)
		require.Len(t, runner.calls(), 1)
		assert.Equal(t, "/tmp", runner.calls()[0].AllowedPaths[0].Path)
	})

	t.Run("worker pool cap at 8", func(t *testing.T) {
		runner := &fakeRunner{sleepDur: 200 * time.Millisecond}
		cmds := make([]string, 20)
		for i := range cmds {
			cmds[i] = "sleep 1"
		}
		ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, cmds)
		assert.LessOrEqual(t, runner.MaxConcurrent(), int32(8), "worker pool capped at 8")
	})

	t.Run("ctx cancellation mid-batch", func(t *testing.T) {
		runner := &fakeRunner{sleepDur: 10 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		cmds := []string{"slow1", "slow2", "slow3"}
		results := ExecuteBlocks(ctx, ExecConfig{Runner: runner}, cmds)
		require.Len(t, results, 3)
		for _, r := range results {
			assert.Error(t, r.Err, "cancelled cmd should have ctx error")
		}
	})

	t.Run("goroutine leak on cancellation", func(t *testing.T) {
		runner := &fakeRunner{sleepDur: 10 * time.Second}
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		before := runtime.NumGoroutine()
		ExecuteBlocks(ctx, ExecConfig{Runner: runner}, []string{"slow"})
		cancel()
		time.Sleep(20 * time.Millisecond)
		after := runtime.NumGoroutine()
		assert.Equal(t, before, after, "no goroutine leak after cancellation")
	})

	t.Run("TimeoutSec propagated", func(t *testing.T) {
		runner := &fakeRunner{}
		ExecuteBlocks(context.Background(), ExecConfig{
			Runner:     runner,
			TimeoutSec: 120,
		}, []string{"test"})
		require.Len(t, runner.calls(), 1)
		assert.Equal(t, 120, runner.calls()[0].Timeout)
	})

	t.Run("Env propagated", func(t *testing.T) {
		runner := &fakeRunner{}
		ExecuteBlocks(context.Background(), ExecConfig{
			Runner: runner,
			Env:    map[string]string{"KEY": "value"},
		}, []string{"env_test"})
		require.Len(t, runner.calls(), 1)
		assert.Equal(t, "value", runner.calls()[0].Env["KEY"])
	})

	t.Run("Stderr preserved end-to-end", func(t *testing.T) {
		runner := &fakeRunner{stderr: "some error output"}
		results := ExecuteBlocks(context.Background(), ExecConfig{Runner: runner}, []string{"cmd"})
		require.Len(t, results, 1)
		assert.Equal(t, "some error output", results[0].Stderr)
		assert.Equal(t, "cmd", results[0].Command)
	})
}

func TestNewTemenosRunner(t *testing.T) {
	t.Run("empty socket path returns non-nil runner", func(t *testing.T) {
		runner, err := NewTemenosRunner("")
		assert.NoError(t, err)
		assert.NotNil(t, runner)
	})
}

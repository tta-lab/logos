package logos

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tta-lab/temenos/client"
)

// Result is one command's execution outcome, returned by ExecuteBlocks.
// Command is the raw bash content that was run (verbatim from the <cmd> block).
// Stdout/Stderr/ExitCode come from the sandbox runner. Err is non-nil only on
// runner-level failures (sandbox refusal, daemon unreachable, timeout) — a
// non-zero exit code from bash is NOT an Err, it's reported via ExitCode.
// ExitCode == -1 means the runner did not return a normal exit (timeout,
// sandbox error); Err will be non-nil in that case.
type Result struct {
	Command  string
	Stdout   string
	Stderr   string
	ExitCode int
	Err      error
}

// ExecConfig holds the knobs ExecuteBlocks needs to dispatch cmds to a runner.
// runner is required and set by NewExecConfig. Env, AllowedPaths, TimeoutSec
// are optional and map directly to temenos client.RunRequest fields.
type ExecConfig struct {
	runner       commandRunner
	Env          map[string]string
	AllowedPaths []client.AllowedPath
	TimeoutSec   int // maps to RunRequest.Timeout; 0 = daemon default (seconds)
}

// NewExecConfig creates an ExecConfig by resolving the runner from cfg.
// Uses localRunner if cfg.Sandbox is false; uses temenos client if true.
// Returns an error only if Sandbox is true but temenos is unreachable.
func NewExecConfig(cfg Config) (ExecConfig, error) {
	runner, err := resolveRunner(&cfg)
	if err != nil {
		return ExecConfig{}, err
	}
	return ExecConfig{runner: runner}, nil
}

// ExecuteBlocks runs each cmd concurrently against cfg.runner and returns
// results in the original order of cmds. Worker pool is capped at 8. Ctx
// cancellation stops new task submissions; already-submitted tasks run to
// completion (their Results will contain ctx.Err() on premature exit).
// Empty cmds returns nil.
//
// ExecuteBlocks panics if cfg.runner is nil.
func ExecuteBlocks(ctx context.Context, cfg ExecConfig, cmds []string) []Result {
	if len(cmds) == 0 {
		return nil
	}
	if cfg.runner == nil {
		panic("logos.ExecuteBlocks: cfg.runner is nil")
	}

	resultsCh := make(chan blockResult, len(cmds))
	workers := len(cmds)
	if workers > 8 {
		workers = 8
	}

	exec := newBlockExecutor(ctx, cfg, workers, resultsCh)
	for i, cmd := range cmds {
		exec.submit(i, cmd)
	}
	exec.Done()
	return collectOrderedBlocks(resultsCh, len(cmds))
}

// blockResult holds a command result with its index for ordering.
type blockResult struct {
	index    int
	cmd      string
	stdout   string
	stderr   string
	exitCode int
	err      error
}

// blockExecutor runs commands in parallel via goroutines for ExecuteBlocks.
type blockExecutor struct {
	ctx       context.Context
	cfg       ExecConfig
	cmdCh     chan blockTask
	resultsCh chan blockResult
	wg        sync.WaitGroup
}

// blockTask is a command with its index for ordering.
type blockTask struct {
	index int
	cmd   string
}

func newBlockExecutor(ctx context.Context, cfg ExecConfig, n int, resultsCh chan blockResult) *blockExecutor {
	e := &blockExecutor{
		ctx:       ctx,
		cfg:       cfg,
		cmdCh:     make(chan blockTask, n),
		resultsCh: resultsCh,
	}
	for i := 0; i < n; i++ {
		e.wg.Add(1)
		go e.worker()
	}
	return e
}

func (e *blockExecutor) submit(index int, cmd string) {
	select {
	case e.cmdCh <- blockTask{index: index, cmd: cmd}:
	case <-e.ctx.Done():
		// Task dropped — ctx was cancelled before submission. The caller will
		// receive fewer results than cmds; ExecuteBlocks callers handle this.
		slog.Warn("blockExecutor: task dropped due to cancelled context", "cmd", cmd)
	}
}

func (e *blockExecutor) Done() {
	close(e.cmdCh)
	e.wg.Wait()
	close(e.resultsCh)
}

func (e *blockExecutor) worker() {
	defer e.wg.Done()
	for task := range e.cmdCh {
		select {
		case <-e.ctx.Done():
			return
		default:
		}

		if directive, ok := handleBlockedCommand(task.cmd); ok {
			e.resultsCh <- blockResult{
				index: task.index,
				cmd:   task.cmd,
				err:   fmt.Errorf("blocked command %q: %s", task.cmd, directive),
			}
			continue
		}

		req := client.RunRequest{
			Command:      task.cmd,
			Env:          e.cfg.Env,
			AllowedPaths: e.cfg.AllowedPaths,
			Timeout:      e.cfg.TimeoutSec,
		}
		resp, err := e.cfg.runner.Run(e.ctx, req)
		if err != nil {
			e.resultsCh <- blockResult{index: task.index, cmd: task.cmd, err: err}
			continue
		}
		e.resultsCh <- blockResult{
			index:    task.index,
			cmd:      task.cmd,
			stdout:   resp.Stdout,
			stderr:   resp.Stderr,
			exitCode: resp.ExitCode,
		}
	}
}

// collectOrderedBlocks waits for count results and returns them in index order.
// If the results channel closes before all count results arrive (e.g. a goroutine
// panic), any unresolved slots are filled with a descriptive error rather than
// silently returning zero-valued Results.
func collectOrderedBlocks(resultsCh <-chan blockResult, count int) []Result {
	collected := make([]*blockResult, count)
	for i := 0; i < count; i++ {
		result, ok := <-resultsCh
		if !ok {
			// Channel closed early — fill remaining slots with a descriptive error.
			for j := i; j < count; j++ {
				collected[j] = &blockResult{
					err: fmt.Errorf("logos: result channel closed after %d of %d results", i, count),
				}
			}
			break
		}
		collected[result.index] = &result
	}
	results := make([]Result, count)
	for i := 0; i < count; i++ {
		if collected[i] != nil {
			results[i] = Result{
				Command:  collected[i].cmd,
				Stdout:   collected[i].stdout,
				Stderr:   collected[i].stderr,
				ExitCode: collected[i].exitCode,
				Err:      collected[i].err,
			}
		}
	}
	return results
}

# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is logos

logos is a Go library that implements a stateless agent loop. LLMs think in plain text and act via `§ ` prefixed shell commands inside `<cmd>` blocks — no tool schemas, no JSON. The loop is: prompt → LLM → scan for `<cmd>` block with `§ command` → execute in sandbox → feed `<result>` back → repeat.

## Key dependencies

- **fantasy** (`charm.land/fantasy`) — LLM provider abstraction (streaming, messages)
- **temenos** (`github.com/tta-lab/temenos`) — sandboxed command execution daemon (unix socket client)

## Commands

```bash
make test          # run all tests (go test -v ./...)
make fmt           # format with gofmt
make vet           # go vet
make lint          # golangci-lint (must be installed)
make tidy          # go mod tidy
```

Run a single test:
```bash
go test -v -run TestRun_OneCommandThenDone ./...
```

Pre-commit hooks (lefthook): fmt check, vet, lint — run in parallel.

## Architecture

This is a single-package library (`package logos`). All source is at the root.

- **run.go** — Core `Run()` function: the agent loop. Takes `Config` (provider, model, Sandbox, SandboxAddr, sandbox env), conversation history, a prompt, and streaming callbacks. Returns `RunResult` with accumulated response text and step messages. Internally uses `resolveRunner` to select either `localRunner` (unsandboxed `/bin/bash`) or temenos client, then executes commands via `commandRunner` interface.
- **local_runner.go** — `localRunner`: unsandboxed command execution via `os/exec`. Selected when `Config.Sandbox` is false.
- **runner_resolve.go** — `resolveRunner`: selects the appropriate `commandRunner` from `Config`.
- **client.go** — `newClient`: creates a temenos `*client.Client`.
- **parse.go** — `ParseCommand()`: detects lines starting with `§ ` (after optional whitespace) and extracts the command args.
- **prompt.go** — `BuildSystemPrompt()`: renders `system.md.tpl` (embedded via `//go:embed`) with runtime context.
- **system.md.tpl** — Go template for the system prompt. Instructs the LLM to wrap `§ ` commands in `<cmd>...</cmd>` blocks.
- **exec_blocks.go** — `ExecuteBlocks()` and `NewExecConfig()`: library-mode API for running parsed commands.

## Design principles

- **Stateless**: `Run()` takes history in, returns steps out. The caller owns persistence.
- **Multi-command blocks**: `scanAllCommands` extracts all `§ ` lines from `<cmd>...</cmd>` blocks; bare `§` outside blocks are prose and ignored.
- **Dual backend**: `Config.Sandbox` selects `localRunner` (false) or temenos client (true). `resolveRunner` handles the selection.
- **Internal runner surface**: `commandRunner`, `localRunner`, `newClient`, `resolveRunner` are all unexported. The only public entry points are `Run()` and `ExecuteBlocks()` + `NewExecConfig()`.

## Testing

- Unit tests use mock provider/runner (defined in `run_integration_test.go`)
- `local_runner_test.go`: unit tests for `localRunner`
- `runner_resolve_test.go`: unit tests for `resolveRunner` and e2e test for `Sandbox:false` path
- Integration test (`TestRun_HttpServer_JsonEncodingRoundtrip`) spins up a fake temenos HTTP server over a unix socket
- Uses `testify` for assertions

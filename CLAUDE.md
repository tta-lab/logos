# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What is logos

logos is a Go library that implements a stateless agent loop. LLMs think in plain text and act via `$ ` prefixed shell commands — no tool schemas, no JSON. The loop is: prompt → LLM → scan for `$ command` → execute in sandbox → feed output back → repeat.

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

- **run.go** — Core `Run()` function: the agent loop. Takes `Config` (provider, model, temenos client, sandbox env), conversation history, a prompt, and streaming callbacks. Returns `RunResult` with accumulated response text and step messages. Internally uses `scanForCommand` to detect `$ ` lines, executes them via `CommandRunner` interface, and feeds output back as the next user message.
- **parse.go** — `ParseCommand()`: detects lines starting with `$ ` (after optional whitespace) and extracts the command args.
- **prompt.go** — `BuildSystemPrompt()`: renders `system.md.tpl` (embedded via `//go:embed`) with runtime context (working dir, platform, date, available commands). Consumers append their own instructions after the base prompt.
- **system.md.tpl** — Go template for the system prompt. Instructs the LLM to use `$ ` prefix for commands.

## Design principles

- **Stateless**: `Run()` takes history in, returns steps out. The caller owns persistence.
- **One command per turn**: `scanForCommand` finds the first `$ ` line and stops; remaining text after it is ignored.
- **CommandRunner interface**: `temenos/client.Client` satisfies it, but tests use mock implementations.

## Testing

- Unit tests use mock provider/runner (defined in `run_integration_test.go`)
- Integration test (`TestRun_HttpServer_JsonEncodingRoundtrip`) spins up a fake temenos HTTP server over a unix socket
- Uses `testify` for assertions

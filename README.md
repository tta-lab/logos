# logos

Stateless agent loop for Go. LLMs think in plain text and act via shell commands inside `<cmd>` blocks — no tool schemas, no JSON.

```
prompt → LLM → scan <cmd> blocks → execute → feed <result> back → repeat
```

## Backends

logos supports two command execution backends, selected by `Config.Sandbox`:

- **`Sandbox: false`** — local exec via `/bin/bash`. No daemon required. Commands run directly on the host. Useful for development and environments where a sandbox daemon is unavailable.
- **`Sandbox: true`** — sandboxed exec via [temenos](https://github.com/tta-lab/temenos). Commands run in a restricted environment. Requires a running temenos daemon. Set `SandboxAddr` to override the socket path (empty uses `TEMENOS_LISTEN_ADDR` → `TEMENOS_SOCKET_PATH` → `~/.temenos/daemon.sock`).

## Library Mode

For callers that already have an assistant message in hand and want to run
its `<cmd>` blocks without driving a full agent loop, use the library API:

```go
import (
    "context"

    "github.com/tta-lab/logos"
)

func dispatch(ctx context.Context, assistantMsg string) (string, error) {
    cmds := logos.ParseCmdBlocks(assistantMsg)
    if len(cmds) == 0 {
        return "", nil
    }
    cfg, err := logos.NewExecConfig(logos.Config{
        Sandbox: true, // or false for local exec
        Env: map[string]string{"MY_VAR": "value"},
        AllowedPaths: []client.AllowedPath{
            {Path: "/ro/project", ReadOnly: true},
            {Path: "/rw/workspace", ReadOnly: false},
        },
        TimeoutSec: 120,
    })
    if err != nil {
        return "", err
    }
    results := logos.ExecuteBlocks(ctx, cfg, cmds)
    return logos.FormatResults(results), nil
}
```

Use `logos.StripCmdBlocks` to get the prose portion of the message when you want
to display the assistant text to a human without the tool calls.

## Install

```bash
go get github.com/tta-lab/logos
```

## Usage

```go
result, err := logos.Run(ctx, logos.Config{
    Provider:     provider,     // fantasy.Provider (LLM abstraction)
    Model:        "claude-sonnet-4-6",
    SystemPrompt: systemPrompt,
    Sandbox:      true,        // true = temenos sandbox, false = local exec
    SandboxAddr:  "",         // optional; empty uses env fallback
    SandboxEnv:   map[string]string{"HOME": "/app"},
    AllowedPaths: []client.AllowedPath{
        {Path: "/app", ReadOnly: false},
    },
}, history, "read main.go and explain what it does", logos.Callbacks{
    OnDelta: func(text string) {
        fmt.Print(text) // stream to terminal
    },
    // Step lifecycle — fires for each model call:
    OnStepStart: func(stepIdx int) { fmt.Printf("step %d start\n", stepIdx) },
    OnStepEnd:   func(stepIdx int) { fmt.Printf("step %d done\n", stepIdx) },
    // Per-turn termination — fires exactly once at Run() exit:
    OnTurnEnd: func(reason logos.StopReason) { fmt.Printf("done: %s\n", reason) },
})
```

The LLM responds in plain text. When it wants to act, it wraps commands in a `<cmd>` block:

```
Let me check the file structure first.

<cmd>
ls -la /app
</cmd>
```

logos detects the commands, executes them in the configured backend, and feeds the output back wrapped in `<result>`. The loop continues until the LLM responds without any `<cmd>` blocks.

## How it works

1. **`Run()`** takes config, conversation history, a prompt, and streaming callbacks
2. Each turn, the LLM streams a response
3. **`ParseCmdBlocks()`** extracts the contents of each `<cmd>` block from an assistant message
4. Commands run via the configured backend (localRunner or temenos)
5. Output wrapped in `<result>` becomes the next user message; loop repeats
6. When the LLM responds with no `<cmd>` blocks, the loop ends and returns `RunResult`

## Key types

| Type | Purpose |
|------|---------|
| `Config` | Provider, model, Sandbox/SandboxAddr, sandbox env, allowed paths |
| `RunResult` | Final response text + all step messages |
| `StepMessage` | One message in the loop (assistant text, with optional reasoning, or command output) |
| `Callbacks` | Per-step hooks (`OnStepStart`, `OnStepEnd`, `OnDelta`, `OnReasoningDelta`, `OnReasoningSignature`, `OnCommandResult`) plus per-turn hook (`OnTurnEnd` with `StopReason`). One Turn = one `Run()` call; multiple Steps per Turn. |
| `StopReason` | Why `Run()` terminated: `final` / `canceled` / `error` / `hallucination_limit` / `max_steps` |
| `ParseCmdBlocks` | Extract `<cmd>` block contents from a complete assistant message |
| `ExecuteBlocks` | Run parsed commands concurrently, return `[]Result` |
| `FormatResults` | Render `[]Result` as a `<result>` wrap for the model |
| `NewExecConfig` | Create an `ExecConfig` from `Config` (selects runner) |
| `Result` | One command's execution outcome (Command, Stdout, Stderr, ExitCode, Err) |
| `ExecConfig` | Execution knobs: Env, AllowedPaths, TimeoutSec |

## System prompt

`BuildSystemPrompt()` renders an embedded template with runtime context (working dir, platform, date, available commands). Consumers typically append their own instructions after the base prompt:

```go
base, _ := logos.BuildSystemPrompt(logos.PromptData{
    WorkingDir: "/app",
    Platform:   "linux",
    Date:       "2026-03-16",
    Commands:   availableCommands,
})
systemPrompt := base + "\n\n" + customInstructions
```

## Design

- **Stateless** — `Run()` takes history in, returns steps out. The caller owns persistence.
- **Single-cmd protocol** — each LLM turn emits at most one `<cmd>` block; chain commands with `&&`, `;`, or `|` inside one block.
- **Dual backend** — sandbox (temenos) or local exec (`/bin/bash`), selected via `Config.Sandbox`.
- **Provider-agnostic** — uses [fantasy](https://charm.land/fantasy) for LLM abstraction.
- **Reasoning round-trip** — thinking blocks (Anthropic extended thinking) captured in `StepMessage.Reasoning` and `ReasoningSignature` for conversation restoration.

## Dependencies

- [fantasy](https://charm.land/fantasy) — LLM provider abstraction (streaming, messages)
- [temenos](https://github.com/tta-lab/temenos) — sandboxed command execution daemon

## License

MIT

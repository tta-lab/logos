# logos

Stateless agent loop for Go. LLMs think in plain text and act via `§ ` prefixed shell commands inside `<cmd>` blocks — no tool schemas, no JSON.

```
prompt → LLM → scan <cmd> blocks for "§ command" → execute in sandbox → feed <result> back → repeat
```

## Install

```bash
go get github.com/tta-lab/logos
```

## Usage

```go
result, err := logos.Run(ctx, logos.Config{
    Provider:     provider,        // fantasy.Provider (LLM abstraction)
    Model:        "claude-sonnet-4-6",
    SystemPrompt: systemPrompt,
    Temenos:      temenosClient,   // sandboxed command runner
    SandboxEnv:   map[string]string{"HOME": "/app"},
    AllowedPaths: []client.AllowedPath{
        {Path: "/app", Permission: "rw"},
    },
}, history, "read main.go and explain what it does", logos.Callbacks{
    OnDelta: func(text string) {
        fmt.Print(text) // stream to terminal
    },
    OnCommandStart: func(cmd string) {
        fmt.Printf("\n> %s\n", cmd)
    },
})
```

The LLM responds in plain text. When it wants to act, it wraps commands in a `<cmd>` block:

```
Let me check the file structure first.

<cmd>
§ ls -la /app
</cmd>
```

logos detects the commands, executes them in a [temenos](https://github.com/tta-lab/temenos) sandbox, and feeds the output back wrapped in `<result>`. The loop continues until the LLM responds without any `<cmd>` blocks.

## How it works

1. **`Run()`** takes config, conversation history, a prompt, and streaming callbacks
2. Each turn, the LLM streams a response
3. **`scanAllCommands()`** extracts all `§ ` lines from `<cmd>...</cmd>` blocks
4. Commands run via the `CommandRunner` interface (temenos sandbox)
5. Output wrapped in `<result>` becomes the next user message; loop repeats
6. When the LLM responds with no `<cmd>` blocks, the loop ends and returns `RunResult`

## Key types

| Type | Purpose |
|------|---------|
| `Config` | Provider, model, temenos client, sandbox env, allowed paths |
| `RunResult` | Final response text + all step messages |
| `StepMessage` | One message in the loop (assistant text, with optional reasoning, or command output) |
| `Callbacks` | Optional `OnDelta` and `OnCommandStart` streaming hooks |
| `CommandRunner` | Interface for command execution — temenos satisfies it |

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
- **Multi-command blocks** — all `§ ` lines inside a `<cmd>` block run sequentially; bare `§` outside blocks are prose and ignored.
- **Sandboxed** — commands execute in [temenos](https://github.com/tta-lab/temenos), not on the host.
- **Provider-agnostic** — uses [fantasy](https://charm.land/fantasy) for LLM abstraction.
- **Reasoning round-trip** — thinking blocks (Anthropic extended thinking) captured in `StepMessage.Reasoning` and `ReasoningSignature` for conversation restoration.

## Dependencies

- [fantasy](https://charm.land/fantasy) — LLM provider abstraction (streaming, messages)
- [temenos](https://github.com/tta-lab/temenos) — sandboxed command execution daemon

## License

MIT

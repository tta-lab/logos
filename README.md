# logos

Stateless agent loop for Go. LLMs think in plain text and act via `$ ` prefixed shell commands — no tool schemas, no JSON.

```
prompt → LLM → scan for "$ command" → execute in sandbox → feed output back → repeat
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

The LLM responds in plain text. When it wants to act, it writes a `$ ` line:

```
Let me check the file structure first.

$ ls -la /app
```

logos detects the command, executes it in a [temenos](https://github.com/tta-lab/temenos) sandbox, and feeds the output back as the next user message. The loop continues until the LLM responds without a command.

## How it works

1. **`Run()`** takes config, conversation history, a prompt, and streaming callbacks
2. Each turn, the LLM streams a response
3. **`scanForCommand()`** finds the first `$ ` line — one command per turn
4. The command runs via the `CommandRunner` interface (temenos sandbox)
5. Output becomes the next user message; loop repeats
6. When the LLM responds with no command, the loop ends and returns `RunResult`

## Key types

| Type | Purpose |
|------|---------|
| `Config` | Provider, model, temenos client, sandbox env, allowed paths |
| `RunResult` | Final response text + all step messages |
| `StepMessage` | One message in the loop (assistant text or command output) |
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
- **One command per turn** — finds the first `$ ` line and stops; text after is ignored.
- **Sandboxed** — commands execute in [temenos](https://github.com/tta-lab/temenos), not on the host.
- **Provider-agnostic** — uses [fantasy](https://charm.land/fantasy) for LLM abstraction.

## Dependencies

- [fantasy](https://charm.land/fantasy) — LLM provider abstraction (streaming, messages)
- [temenos](https://github.com/tta-lab/temenos) — sandboxed command execution daemon

## License

MIT

# Changelog

## Unreleased

### Breaking Changes

The following public types and fields have been removed from the public API:

- **`logos.CommandRunner`** — unexported as `commandRunner`. Use `Config.Sandbox` to select the backend.
- **`logos.NewClient`** — unexported as `newClient`. Use `Config{Sandbox: true}`.
- **`logos.NewTemenosRunner`** — deleted. Use `Config{Sandbox: true}`.
- **`logos.RunRequest`** — deleted type alias; use `temenos/client.RunRequest` directly.
- **`logos.RunResponse`** — deleted type alias; use `temenos/client.RunResponse` directly.
- **`logos.AllowedPath`** — deleted type alias; use `temenos/client.AllowedPath` directly.
- **`logos.Config.Temenos`** — deleted. Use `Config.Sandbox` and `Config.SandboxAddr`.

### New Features

- **`Config.Sandbox bool`** — when `true`, commands run in the temenos sandbox; when `false`, commands run locally via `/bin/bash`.
- **`Config.SandboxAddr string`** — optional temenos socket/address; empty uses env fallback chain.
- **`Config.SandboxEnv map[string]string`** — env vars passed per-request to the sandbox (unchanged).
- **`NewExecConfig(cfg Config)`** — new public factory for `ExecConfig` that calls `resolveRunner` internally.
- **`localRunner`** — new internal type for unsandboxed local command execution.

### Migration Guide

Before:
```go
runner, _ := logos.NewTemenosRunner("")
logos.Run(ctx, logos.Config{
    Temenos: runner,
    // ...
}, ...)
```

After:
```go
logos.Run(ctx, logos.Config{
    Sandbox: true, // or false for local exec
    // SandboxAddr: "", // optional
}, ...)
```

Before (library mode):
```go
runner, _ := logos.NewTemenosRunner("")
logos.ExecuteBlocks(ctx, logos.ExecConfig{
    Runner: runner,
    // ...
}, ...)
```

After:
```go
cfg, _ := logos.NewExecConfig(logos.Config{Sandbox: true})
logos.ExecuteBlocks(ctx, cfg, ...)
```

Before (path types):
```go
AllowedPaths: []logos.AllowedPath{{Path: "/tmp", ReadOnly: true}}
```

After:
```go
AllowedPaths: []client.AllowedPath{{Path: "/tmp", ReadOnly: true}}
```

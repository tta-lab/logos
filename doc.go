// Package logos provides a reusable stateless agent loop.
//
// Run() executes one agent loop iteration: prompt → LLM → <cmd> blocks → response.
// The caller provides conversation history, a system prompt, and streaming callbacks.
// No persistence — the caller receives StepMessages and handles storage.
//
// Library mode: for callers that already have an assistant message in hand and want
// to run its <cmd> blocks without driving a full agent loop, use ParseCmdBlocks +
// ExecuteBlocks + FormatResults. NewExecConfig creates an ExecConfig from Config,
// selecting the appropriate runner (local or sandbox) based on Config.Sandbox.
//
// Plane: shared
package logos

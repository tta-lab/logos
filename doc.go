// Package logos provides a reusable stateless agent loop.
//
// Run() executes one agent loop iteration: prompt → LLM → tool calls → response.
// The caller provides conversation history, a system prompt, tools, and an optional
// sandbox env. No persistence — the caller receives StepMessages and handles storage.
//
// Library mode: for callers that already have an assistant message in hand and want
// to run its <cmd> blocks without driving a full agent loop, use ParseCmdBlocks +
// ExecuteBlocks + FormatResults. NewTemenosRunner creates a CommandRunner with no
// temenos import required in consumer code.
//
// Plane: shared
package logos

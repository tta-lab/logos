package logos

import (
	"regexp"
)

// toolCallXMLRe matches XML-style tool call hallucinations (case-insensitive).
var toolCallXMLRe = regexp.MustCompile(`(?i)</?(?:tool_call|minimax:tool_call|function_call|tool_use|invoke)\b[^>]*>`)

// toolCallBracketRe matches bracket-style tool call hallucinations (case-insensitive).
var toolCallBracketRe = regexp.MustCompile(`(?i)\[/?(?:tool_?call|function_?call|tool_?use|invoke)\]`)

// ContainsToolCallHallucination returns true if text contains tool call patterns
// produced by models that hallucinate structured formats — XML tags (e.g.
// <tool_call>) or bracket delimiters (e.g. [TOOL_CALL]...[/TOOL_CALL]).
// Standalone utility — internal detection is handled by streamFilter during streaming.
func ContainsToolCallHallucination(text string) bool {
	return toolCallXMLRe.MatchString(text) || toolCallBracketRe.MatchString(text)
}

// xmlToolCallMarkers are substrings that indicate a model used XML tool_call
// format. Only includes patterns specific enough to avoid false positives
// on prose mentioning XML concepts.
var xmlToolCallMarkers = []string{
	"<tool_call>",
	"</tool_call>",
	"<tool_call ",
	"<minimax:tool_call>",
	"</minimax:tool_call>",
	"<minimax:tool_call ",
	"<invoke name=",
	"<function_call>",
	"<function_call ",
}

// bracketToolCallMarkers for case-insensitive prefix matching in streaming filter.
var bracketToolCallMarkers = []string{
	"[tool_call]",
	"[/tool_call]",
	"[function_call]",
	"[/function_call]",
	"[tool_use]",
	"[/tool_use]",
	"[invoke]",
	"[/invoke]",
}

// containsBracketToolCall returns true if text contains bracket-style tool call patterns.
func containsBracketToolCall(text string) bool {
	return toolCallBracketRe.MatchString(text)
}

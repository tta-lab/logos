package logos

import (
	"regexp"
	"strings"
)

// CommandPrefix is the prefix for agent commands in LLM output.
const CommandPrefix = "§ "

// Command represents a parsed § command from assistant output.
type Command struct {
	Raw  string // full original line (e.g. "§ ls -la")
	Args string // everything after "§ " (e.g. "ls -la")
}

// ParseCommand checks if a line is a § command.
// Returns the command and true if the line starts with "§ ".
// Returns zero Command and false otherwise.
func ParseCommand(line string) (Command, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, CommandPrefix) {
		return Command{}, false
	}
	args := strings.TrimPrefix(trimmed, CommandPrefix)
	if args == "" {
		return Command{}, false
	}
	return Command{
		Raw:  trimmed,
		Args: args,
	}, true
}

// isHeredocClose reports whether line closes a heredoc with the given delimiter.
// Handles plain close (TrimSpace) and <<- indented close (TrimRight tabs).
func isHeredocClose(line, delim string) bool {
	return strings.TrimSpace(line) == delim || strings.TrimRight(line, "\t") == delim
}

// heredocDelimiter extracts the delimiter from a heredoc operator in a command string.
// Handles: <<EOF, <<'EOF', <<"EOF", <<-EOF, <<-'EOF', <<- 'EOF'
// Returns the delimiter and true if found, or empty string and false if no heredoc.
func heredocDelimiter(cmdArgs string) (string, bool) {
	// Find << operator (may appear after pipes or other shell constructs)
	idx := strings.Index(cmdArgs, "<<")
	if idx == -1 {
		return "", false
	}

	rest := cmdArgs[idx+2:]
	// Skip optional "-" (for <<- which strips leading tabs)
	rest = strings.TrimPrefix(rest, "-")
	// Take the first word-like token, stripping quotes
	rest = strings.TrimSpace(rest)

	if rest == "" {
		return "", false
	}

	// Strip surrounding quotes if present
	if strings.HasPrefix(rest, "'") && strings.Contains(rest[1:], "'") {
		end := strings.Index(rest[1:], "'") + 1
		return rest[1:end], true
	}
	if strings.HasPrefix(rest, "\"") && strings.Contains(rest[1:], "\"") {
		end := strings.Index(rest[1:], "\"") + 1
		return rest[1:end], true
	}

	// Unquoted: take until whitespace, newline, or end of string
	delim := strings.FieldsFunc(rest, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '|' || r == '&' || r == ')'
	})
	if len(delim) == 0 {
		return "", false
	}
	return delim[0], true
}

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

// stripMarkers are substrings to silently strip from streaming output.
// Used to remove thinking tag leaks without triggering a retry.
var stripMarkers = []string{
	"</think>",
	"<think>",
}

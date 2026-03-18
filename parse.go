package logos

import "strings"

// CommandPrefix is the prefix for agent commands in LLM output.
const CommandPrefix = "! "

// Command represents a parsed ! command from assistant output.
type Command struct {
	Raw  string // full original line (e.g. "! ls -la")
	Args string // everything after "! " (e.g. "ls -la")
}

// ParseCommand checks if a line is a ! command.
// Returns the command and true if the line starts with "! ".
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

// ContainsXMLToolCall returns true if text contains XML tool_call patterns
// produced by models that default to structured format (e.g. minimax).
// Standalone utility — internal detection is handled by streamFilter during streaming.
func ContainsXMLToolCall(text string) bool {
	for _, marker := range xmlToolCallMarkers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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

// stripMarkers are substrings to silently strip from streaming output.
// Used to remove thinking tag leaks without triggering a retry.
var stripMarkers = []string{
	"</think>",
	"<think>",
}

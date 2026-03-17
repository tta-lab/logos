package logos

import "strings"

// Command represents a parsed $ command from assistant output.
type Command struct {
	Raw  string // full original line (e.g. "$ ls -la")
	Args string // everything after "$ " (e.g. "ls -la")
}

// ParseCommand checks if a line is a $ command.
// Returns the command and true if the line starts with "$ ".
// Returns zero Command and false otherwise.
func ParseCommand(line string) (Command, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "$ ") {
		return Command{}, false
	}
	args := strings.TrimPrefix(trimmed, "$ ")
	if args == "" {
		return Command{}, false
	}
	return Command{
		Raw:  trimmed,
		Args: args,
	}, true
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
// Used to detect wrong output format and trigger error feedback.
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
	"<invoke name=",
	"<tool_call>",
	"<minimax:tool_call>",
}

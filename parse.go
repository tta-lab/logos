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

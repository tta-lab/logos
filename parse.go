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

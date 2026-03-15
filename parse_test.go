package logos

import "testing"

func TestParseCommand(t *testing.T) {
	tests := []struct {
		line     string
		wantArgs string
		wantOK   bool
	}{
		{"$ ls -la", "ls -la", true},
		{"$ rg 'pattern' /dir", "rg 'pattern' /dir", true},
		{"$ logos read /path/to/file.go", "logos read /path/to/file.go", true},
		{"$ logos search \"golang context\"", "logos search \"golang context\"", true},
		{"  $ ls", "ls", true},   // leading whitespace OK
		{"$", "", false},         // just dollar, no space
		{"$ ", "", false},        // dollar + space, no command
		{"echo hello", "", false}, // no $ prefix
		{"", "", false},          // empty
		{"# $ ls", "", false},    // commented out
		{"$ls", "", false},       // no space after $
	}

	for _, tt := range tests {
		cmd, ok := ParseCommand(tt.line)
		if ok != tt.wantOK {
			t.Errorf("ParseCommand(%q): got ok=%v, want %v", tt.line, ok, tt.wantOK)
			continue
		}
		if ok && cmd.Args != tt.wantArgs {
			t.Errorf("ParseCommand(%q): got args=%q, want %q", tt.line, cmd.Args, tt.wantArgs)
		}
	}
}

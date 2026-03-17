package logos

import "testing"

func TestContainsXMLToolCall(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"minimax format", "<minimax:tool_call>\n<invoke name=\"rg\">", true},
		{"generic invoke", "<invoke name=\"cat\">", true},
		{"tool_call tag", "<tool_call>\nsome content\n</tool_call>", true},
		{"normal dollar cmd", "$ ls -la\nsome output", false},
		{"plain text", "Here is the answer to your question.", false},
		{"invoke in prose", "We invoke the function by calling...", false},
		{"closing invoke in prose", "The </invoke> tag closes the block.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsXMLToolCall(tt.input); got != tt.want {
				t.Errorf("ContainsXMLToolCall() = %v, want %v", got, tt.want)
			}
		})
	}
}

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
		{"  $ ls", "ls", true},    // leading whitespace OK
		{"$", "", false},          // just dollar, no space
		{"$ ", "", false},         // dollar + space, no command
		{"echo hello", "", false}, // no $ prefix
		{"", "", false},           // empty
		{"# $ ls", "", false},     // commented out
		{"$ls", "", false},        // no space after $
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

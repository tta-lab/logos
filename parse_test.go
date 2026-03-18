package logos

import "testing"

func TestHeredocDelimiter(t *testing.T) {
	tests := []struct {
		args      string
		wantDelim string
		wantOK    bool
	}{
		// Standard forms
		{"cat <<EOF", "EOF", true},
		{"cat <<'EOF'", "EOF", true},
		{"cat <<\"EOF\"", "EOF", true},
		// Dash (tab-stripping) forms
		{"cat <<-EOF", "EOF", true},
		{"cat <<-'MARKER'", "MARKER", true},
		{"cat <<- 'PLANEOF'", "PLANEOF", true},  // dash + space + quoted
		{"cat <<-\"PLANEOF\"", "PLANEOF", true}, // dash + double-quoted
		// With pipes and redirects
		{"cat <<'EOF' | wc -l", "EOF", true},
		{"cat <<'EOF' > out.txt", "EOF", true},
		// No heredoc
		{"ls -la", "", false},
		{"echo hello", "", false},
		// Edge cases
		{"cat <<", "", false}, // no delimiter after <<
	}

	for _, tt := range tests {
		t.Run(tt.args, func(t *testing.T) {
			delim, ok := heredocDelimiter(tt.args)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if delim != tt.wantDelim {
				t.Errorf("delim = %q, want %q", delim, tt.wantDelim)
			}
		})
	}
}

func TestContainsXMLToolCall(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"minimax format", "<minimax:tool_call>\n<invoke name=\"rg\">", true},
		{"generic invoke", "<invoke name=\"cat\">", true},
		{"tool_call tag", "<tool_call>\nsome content\n</tool_call>", true},
		{"closing tool_call tag", "</tool_call>", true},
		{"tool_call with attributes", "<tool_call name=\"foo\">", true},
		{"closing invoke tag", "</invoke>", false}, // bare closing tag — not a marker (prose false-positive risk)
		{"function_call tag", "<function_call>", true},
		{"closing function_call tag", "</function_call>", false}, // bare closing tag — not a marker
		{"function_call with attributes", "<function_call name=\"foo\">", true},
		{"minimax closing tag", "</minimax:tool_call>", true},
		{"normal bang cmd", "! ls -la\nsome output", false},
		{"plain text", "Here is the answer to your question.", false},
		{"invoke in prose", "We invoke the function by calling...", false},
		{"closing invoke in prose", "The </invoke> tag closes the block.", false}, // not a marker
		{"old dollar prefix not detected", "$ ls -la\nsome output", false},
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
		{"! ls -la", "ls -la", true},
		{"! rg 'pattern' /dir", "rg 'pattern' /dir", true},
		{"! logos read /path/to/file.go", "logos read /path/to/file.go", true},
		{"! logos search \"golang context\"", "logos search \"golang context\"", true},
		{"  ! ls", "ls", true},    // leading whitespace OK
		{"!", "", false},          // just bang, no space
		{"! ", "", false},         // bang + space, no command
		{"echo hello", "", false}, // no ! prefix
		{"", "", false},           // empty
		{"# ! ls", "", false},     // commented out
		{"!ls", "", false},        // no space after !
		// Old prefix must NOT be parsed as command
		{"$ ls -la", "", false},     // old dollar prefix no longer recognized
		{"$ rg pattern", "", false}, // old dollar prefix no longer recognized
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

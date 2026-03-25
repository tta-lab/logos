package logos

import "testing"

func TestContainsToolCallHallucination(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		// XML patterns
		{"minimax format", "<minimax:tool_call>\n<invoke name=\"rg\">", true},
		{"generic invoke", "<invoke name=\"cat\">", true},
		{"tool_call tag", "<tool_call>\nsome content\n</tool_call>", true},
		{"closing tool_call tag", "</tool_call>", true},
		{"tool_call with attributes", "<tool_call name=\"foo\">", true},
		{"closing invoke tag", "</invoke>", true}, // regex matches closing tags too
		{"function_call tag", "<function_call>", true},
		{"closing function_call tag", "</function_call>", true}, // regex matches closing tags
		{"function_call with attributes", "<function_call name=\"foo\">", true},
		{"minimax closing tag", "</minimax:tool_call>", true},
		// Bracket patterns
		{"bracket TOOL_CALL", "[TOOL_CALL]{tool => 'shell'}[/TOOL_CALL]", true},
		{"bracket lowercase", "[tool_call]content[/tool_call]", true},
		{"bracket mixed case", "[Tool_Call]content[/Tool_Call]", true},
		{"bracket opening only", "[TOOL_CALL]partial content", true},
		{"bracket closing only", "[/TOOL_CALL]", true},
		{"bracket function_call", "[FUNCTION_CALL]content[/FUNCTION_CALL]", true},
		{"bracket tool_use", "[TOOL_USE]content[/TOOL_USE]", true},
		{"bracket invoke", "[INVOKE]content[/INVOKE]", true},
		{"bracket no underscore toolcall", "[TOOLCALL]content", true},
		{"harmless brackets", "[some other content]", false},
		{"markdown link", "[click here](https://example.com)", false},
		{"json array", "[1, 2, 3]", false},
		// Not hallucinations
		{"normal § cmd", "§ ls -la\nsome output", false},
		{"plain text", "Here is the answer to your question.", false},
		{"invoke in prose", "We invoke the function by calling...", false},
		{"old dollar prefix not detected", "$ ls -la\nsome output", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ContainsToolCallHallucination(tt.input); got != tt.want {
				t.Errorf("ContainsToolCallHallucination() = %v, want %v", got, tt.want)
			}
		})
	}
}

package logos

import (
	"testing"
)

func TestScanForCommand(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		wantPre string
		wantCmd string
		wantOK  bool
	}{
		// Basic cases
		{
			name:    "no command",
			text:    "Here is my analysis of the code.",
			wantPre: "Here is my analysis of the code.",
			wantOK:  false,
		},
		{
			name:    "command at start",
			text:    "$ ls -la",
			wantPre: "",
			wantCmd: "ls -la",
			wantOK:  true,
		},
		{
			name:    "text before command",
			text:    "Let me check the files.\n$ ls -la",
			wantPre: "Let me check the files.\n",
			wantCmd: "ls -la",
			wantOK:  true,
		},
		{
			name:    "multiline text before command",
			text:    "First line.\nSecond line.\n$ rg pattern /dir",
			wantPre: "First line.\nSecond line.\n",
			wantCmd: "rg pattern /dir",
			wantOK:  true,
		},
		{
			name:    "empty text",
			text:    "",
			wantPre: "",
			wantOK:  false,
		},
		// Trailing prose — must NOT be captured
		{
			name:    "command with trailing prose not captured",
			text:    "$ ls -la\nI expect this to show files.",
			wantPre: "",
			wantCmd: "ls -la",
			wantOK:  true,
		},
		// Multi-command — only first returned (rejection handled in Run loop)
		{
			name:    "only first command taken",
			text:    "$ cmd1\n$ cmd2",
			wantPre: "",
			wantCmd: "cmd1",
			wantOK:  true,
		},
		// Heredoc cases
		{
			name:    "heredoc captured",
			text:    "$ cat <<'EOF'\nline1\nline2\nEOF",
			wantPre: "",
			wantCmd: "cat <<'EOF'\nline1\nline2\nEOF",
			wantOK:  true,
		},
		{
			name:    "text before heredoc",
			text:    "Let me write.\n$ cat <<'EOF'\ncontent\nEOF",
			wantPre: "Let me write.\n",
			wantCmd: "cat <<'EOF'\ncontent\nEOF",
			wantOK:  true,
		},
		{
			name:    "heredoc with pipe",
			text:    "$ cat <<'EOF' | wc -l\nhello\nworld\nEOF",
			wantPre: "",
			wantCmd: "cat <<'EOF' | wc -l\nhello\nworld\nEOF",
			wantOK:  true,
		},
		{
			name:    "dash heredoc with tabs",
			text:    "$ cat <<-'END'\n\thello\n\tworld\nEND",
			wantPre: "",
			wantCmd: "cat <<-'END'\n\thello\n\tworld\nEND",
			wantOK:  true,
		},
		{
			name:    "heredoc with dollar line in body",
			text:    "$ cat <<'EOF'\n$ not_a_command\nsome text\nEOF",
			wantPre: "",
			wantCmd: "cat <<'EOF'\n$ not_a_command\nsome text\nEOF",
			wantOK:  true,
		},
		{
			name:    "unclosed heredoc falls through to single line",
			text:    "$ cat <<'EOF'\nline1\nline2\nno closing",
			wantPre: "",
			wantCmd: "cat <<'EOF'",
			wantOK:  true,
		},
		{
			name:    "dash heredoc with space before delimiter",
			text:    "$ cat <<- 'PLANEOF'\ncontent\nPLANEOF",
			wantPre: "",
			wantCmd: "cat <<- 'PLANEOF'\ncontent\nPLANEOF",
			wantOK:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preText, cmd, found := scanForCommand(tt.text)
			if found != tt.wantOK {
				t.Errorf("found = %v, want %v", found, tt.wantOK)
			}
			if preText != tt.wantPre {
				t.Errorf("preText = %q, want %q", preText, tt.wantPre)
			}
			if tt.wantOK && cmd.Args != tt.wantCmd {
				t.Errorf("cmd.Args = %q, want %q", cmd.Args, tt.wantCmd)
			}
		})
	}
}

func TestScanAllCommands(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantPre  string
		wantCmds []string
	}{
		{"no commands", "Just text.", "Just text.", nil},
		{"one command", "$ ls -la", "", []string{"ls -la"}},
		{"two commands", "$ pwd\n$ ls -la", "", []string{"pwd", "ls -la"}},
		{"text before commands", "Let me check.\n$ pwd\n$ ls", "Let me check.\n", []string{"pwd", "ls"}},
		{"heredoc counts as one", "$ cat <<'EOF'\nline1\nEOF\n$ ls", "", []string{"cat <<'EOF'\nline1\nEOF", "ls"}},
		{"dollar in heredoc body ignored", "$ cat <<'EOF'\n$ fake\nEOF", "", []string{"cat <<'EOF'\n$ fake\nEOF"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			preText, cmds := scanAllCommands(tt.text)
			if preText != tt.wantPre {
				t.Errorf("preText = %q, want %q", preText, tt.wantPre)
			}
			var gotArgs []string
			for _, c := range cmds {
				gotArgs = append(gotArgs, c.Args)
			}
			if len(gotArgs) != len(tt.wantCmds) {
				t.Errorf("got %d commands, want %d: %v", len(gotArgs), len(tt.wantCmds), gotArgs)
				return
			}
			for i := range gotArgs {
				if gotArgs[i] != tt.wantCmds[i] {
					t.Errorf("cmd[%d] = %q, want %q", i, gotArgs[i], tt.wantCmds[i])
				}
			}
		})
	}
}

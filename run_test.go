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

func TestCountCommands(t *testing.T) {
	tests := []struct {
		name string
		text string
		want int
	}{
		// Basic counting
		{"no commands", "Just some text.", 0},
		{"one command", "$ ls -la", 1},
		{"two commands", "$ pwd\n$ ls -la", 2},
		{"three commands", "$ pwd\n$ ls\n$ cat file.go", 3},
		{"command with text", "Let me check.\n$ ls -la", 1},
		{"text between commands", "$ pwd\nsome text\n$ ls -la", 2},
		{"empty", "", 0},
		// Heredoc-aware counting
		{"heredoc with dollar in body", "$ cat <<'EOF'\n$ not_a_command\nEOF", 1},
		{"heredoc then real command", "$ cat <<'EOF'\nbody\nEOF\n$ ls", 2},
		{"dash heredoc with dollar in body", "$ cat <<-'EOF'\n\t$ fake\nEOF", 1},
		{"unclosed heredoc then command", "$ cat <<'EOF'\nno close\n$ ls", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := countCommands(tt.text); got != tt.want {
				t.Errorf("countCommands() = %d, want %d", got, tt.want)
			}
		})
	}
}

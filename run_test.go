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
			name:    "only first command taken",
			text:    "$ cmd1\n$ cmd2",
			wantPre: "",
			wantCmd: "cmd1",
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

package logos

import (
	"testing"
)

func TestParseCmdBlocks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []string
	}{
		{
			name:  "empty text",
			input: "",
			want:  nil,
		},
		{
			name:  "no cmd blocks",
			input: "hello world",
			want:  nil,
		},
		{
			name:  "single block",
			input: "prefix <cmd>ls</cmd> suffix",
			want:  []string{"ls"},
		},
		{
			name:  "multiple blocks in order",
			input: "<cmd>ls</cmd> prose <cmd>pwd</cmd>",
			want:  []string{"ls", "pwd"},
		},
		{
			name:  "block with heredoc inside",
			input: "<cmd>cat <<'EOF'\nfoo\nEOF</cmd>",
			want:  []string{"cat <<'EOF'\nfoo\nEOF"},
		},
		{
			name:  "block content with leading/trailing whitespace",
			input: "<cmd>  ls  </cmd>",
			want:  []string{"ls"},
		},
		{
			name:  "unclosed block at EOF",
			input: "<cmd>test",
			want:  nil,
		},
		{
			name:  "prose sandwich",
			input: "hello <cmd>ls</cmd> there <cmd>pwd</cmd> bye",
			want:  []string{"ls", "pwd"},
		},
		{
			name:  "block at very start",
			input: "<cmd>ls</cmd> after",
			want:  []string{"ls"},
		},
		{
			name:  "block at very end",
			input: "before <cmd>ls</cmd>",
			want:  []string{"ls"},
		},
		{
			name:  "truncated close tag",
			input: "<cmd>test</",
			want:  nil,
		},
		{
			name:  "bare close tag before open",
			input: "</cmd>text<cmd>valid</cmd>",
			want:  []string{"valid"},
		},
		{
			name:  "nested-ish first close wins",
			input: "<cmd>outer<cmd>inner</cmd>more</cmd>",
			want:  []string{"outer<cmd>inner"},
		},
		{
			name:  "interleaved blocks reverse order",
			input: "<cmd>a</cmd></cmd>prose<cmd>b</cmd>",
			want:  []string{"a", "b"},
		},
		{
			name:  "empty block",
			input: "<cmd></cmd>",
			want:  []string{""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseCmdBlocks(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("ParseCmdBlocks() = %v, want %v", got, tt.want)
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("ParseCmdBlocks()[%d] = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStripCmdBlocks(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "empty text",
			input: "",
			want:  "",
		},
		{
			name:  "no cmd blocks",
			input: "hello world",
			want:  "hello world",
		},
		{
			name:  "single block",
			input: "prefix <cmd>ls</cmd> suffix",
			want:  "prefix  suffix",
		},
		{
			name:  "multiple blocks",
			input: "hello <cmd>ls</cmd> there <cmd>pwd</cmd> bye",
			want:  "hello  there  bye",
		},
		{
			name:  "block at very start",
			input: "<cmd>ls</cmd> after",
			want:  " after",
		},
		{
			name:  "block at very end",
			input: "before <cmd>ls</cmd>",
			want:  "before ",
		},
		{
			name:  "unclosed block at EOF",
			input: "before <cmd>unclosed",
			want:  "before ",
		},
		{
			name:  "multiple adjacent blocks no extra blank lines",
			input: "hello<cmd>a</cmd><cmd>b</cmd>bye",
			want:  "hellobye",
		},
		{
			name:  "blank lines between blocks collapsed to one",
			input: "start\n\n<cmd>ls</cmd>\n\n<cmd>pwd</cmd>\n\nend",
			want:  "start\nend",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripCmdBlocks(tt.input)
			if got != tt.want {
				t.Errorf("StripCmdBlocks() = %q, want %q", got, tt.want)
			}
		})
	}
}

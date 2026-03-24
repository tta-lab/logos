package logos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestScanAllCommands(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantPre  string
		wantCmds []string
	}{
		{"no commands", "Just text.", "Just text.", nil},
		{"one command in block", "<cmd>\n§ ls -la\n</cmd>", "", []string{"ls -la"}},
		{"two commands in block", "<cmd>\n§ pwd\n§ ls -la\n</cmd>", "", []string{"pwd", "ls -la"}},
		{"text before block", "Let me check.\n<cmd>\n§ pwd\n§ ls\n</cmd>", "Let me check.\n", []string{"pwd", "ls"}},
		{"heredoc in block", "<cmd>\n§ cat <<'EOF'\nline1\nEOF\n§ ls\n</cmd>", "", []string{"cat <<'EOF'\nline1\nEOF", "ls"}},
		{"bare § outside block ignored", "§ ls -la", "§ ls -la", nil},
		{"text after block", "before<cmd>\n§ ls\n</cmd>after", "before", []string{"ls"}},
		{"empty block", "<cmd></cmd>", "", nil},
		{"multiple blocks", "<cmd>\n§ ls\n</cmd>text<cmd>\n§ pwd\n</cmd>", "", []string{"ls", "pwd"}},
		{"unclosed block", "<cmd>\n§ ls", "", []string{"ls"}},
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

// --- streamFilter tests ---

func TestStreamFilter_FastPath_NoAngle(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("hello world")
	f.Flush()
	if len(got) != 1 || got[0] != "hello world" {
		t.Errorf("got %v, want [hello world]", got)
	}
	if f.toolCallDetected {
		t.Error("toolCallDetected should be false")
	}
}

func TestStreamFilter_Tier1_XMLToolCall(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("<tool_call>echo hello</tool_call>")
	f.Flush()
	if len(got) != 0 {
		t.Errorf("expected no output, got %v", got)
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true")
	}
}

func TestStreamFilter_Tier1_SplitAcrossDeltas(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("<tool_")
	f.Write("call>")
	f.Flush()
	if len(got) != 0 {
		t.Errorf("expected no output, got %v", got)
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true")
	}
}

func TestStreamFilter_Tier2_ThinkTagStripped(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("</think>Here is the result")
	f.Flush()
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "Here is the result" {
		t.Errorf("got %q, want %q", combined, "Here is the result")
	}
	if f.toolCallDetected {
		t.Error("toolCallDetected should be false for think tag")
	}
}

func TestStreamFilter_Tier2_ThinkTagSplit(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("</thi")
	f.Write("nk>result")
	f.Flush()
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "result" {
		t.Errorf("got %q, want %q", combined, "result")
	}
	if f.toolCallDetected {
		t.Error("toolCallDetected should be false")
	}
}

func TestStreamFilter_HarmlessAngle_NotDetected(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("<p>some content</p>")
	f.Flush()
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "<p>some content</p>" {
		t.Errorf("got %q, want %q", combined, "<p>some content</p>")
	}
	if f.toolCallDetected {
		t.Error("toolCallDetected should be false")
	}
}

func TestStreamFilter_BufferAtStreamEnd(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("text<")
	f.Flush() // trailing < should be flushed
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "text<" {
		t.Errorf("got %q, want %q", combined, "text<")
	}
}

func TestStreamFilter_Mixed_ThinkAndText(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("before")
	f.Write("</think>")
	f.Write("after")
	f.Flush()
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "beforeafter" {
		t.Errorf("got %q, want %q", combined, "beforeafter")
	}
}

// --- cmdBlockFilter tests ---

func TestCmdBlockFilter_PassThrough(t *testing.T) {
	var got []string
	f := &cmdBlockFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("hello world")
	f.Flush()
	assert.Equal(t, []string{"hello world"}, got)
}

func TestCmdBlockFilter_BlockBuffered(t *testing.T) {
	var got []string
	f := &cmdBlockFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("before<cmd>\n§ ls\n</cmd>after")
	f.Flush()
	// <cmd> block content is swallowed; only prose reaches the delegate
	combined := strings.Join(got, "")
	assert.Equal(t, "beforeafter", combined)
	assert.NotContains(t, combined, "<cmd>")
	assert.NotContains(t, combined, "§ ls")
}

func TestCmdBlockFilter_SplitAcrossDeltas(t *testing.T) {
	var got []string
	f := &cmdBlockFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("text<cm")
	f.Write("d>\n§ ls\n</cm")
	f.Write("d>more")
	f.Flush()
	combined := strings.Join(got, "")
	// Block content swallowed; prose before and after passes through in order
	assert.True(t, strings.HasPrefix(combined, "text"), "expected 'text' before 'more', got: %q", combined)
	assert.True(t, strings.HasSuffix(combined, "more"), "expected 'more' at end, got: %q", combined)
	assert.NotContains(t, combined, "<cmd>")
	assert.NotContains(t, combined, "§ ls")
}

func TestCmdBlockFilter_UnclosedBlock(t *testing.T) {
	var got []string
	f := &cmdBlockFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("text<cmd>\n§ ls")
	f.Flush()
	combined := strings.Join(got, "")
	// Prose before the block passes through; unclosed block is discarded
	assert.Contains(t, combined, "text")
	assert.NotContains(t, combined, "<cmd>")
	assert.NotContains(t, combined, "§ ls")
}

func TestCmdBlockFilter_MultipleBlocksInOneWrite(t *testing.T) {
	var got []string
	f := &cmdBlockFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("before<cmd>\n§ ls\n</cmd>middle<cmd>\n§ pwd\n</cmd>after")
	f.Flush()
	combined := strings.Join(got, "")
	// Only prose passes through; both blocks are swallowed
	assert.Contains(t, combined, "before")
	assert.Contains(t, combined, "middle")
	assert.Contains(t, combined, "after")
	assert.NotContains(t, combined, "<cmd>")
	assert.NotContains(t, combined, "§ ls")
	assert.NotContains(t, combined, "§ pwd")
}

func TestCmdBlockFilter_EmptyBlock(t *testing.T) {
	var got []string
	f := &cmdBlockFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd></cmd>")
	f.Flush()
	// Empty block produces no output
	assert.Empty(t, got)
}

func TestStreamFilter_BracketToolCall(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("[TOOL_CALL]{tool => 'shell', args => { --command 'ls' }}[/TOOL_CALL]")
	f.Flush()
	if len(got) != 0 {
		t.Errorf("expected no output, got %v", got)
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true")
	}
}

func TestStreamFilter_BracketToolCall_CaseInsensitive(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("[tool_call]some content[/tool_call]")
	f.Flush()
	if len(got) != 0 {
		t.Errorf("expected no output, got %v", got)
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true for lowercase bracket")
	}
}

func TestStreamFilter_BracketToolCall_SplitAcrossDeltas(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("[TOOL_")
	f.Write("CALL]")
	f.Flush()
	if len(got) != 0 {
		t.Errorf("expected no output, got %v", got)
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true for split bracket marker")
	}
}

func TestStreamFilter_HarmlessBracket_NotDetected(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("[some content]")
	f.Flush()
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "[some content]" {
		t.Errorf("got %q, want %q", combined, "[some content]")
	}
	if f.toolCallDetected {
		t.Error("toolCallDetected should be false for harmless bracket")
	}
}

func TestStreamFilter_TextBeforeBracketToolCall(t *testing.T) {
	var got []string
	f := &streamFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("some text before [TOOL_CALL]bad[/TOOL_CALL]")
	f.Flush()
	combined := ""
	for _, s := range got {
		combined += s
	}
	if combined != "some text before " {
		t.Errorf("got %q, want %q", combined, "some text before ")
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true")
	}
}

func TestHallucinationDirective(t *testing.T) {
	d1 := hallucinationDirective(1)
	if !strings.Contains(d1, "Unprocessed") {
		t.Errorf("attempt 1 directive should contain 'Unprocessed', got: %q", d1)
	}
	d2 := hallucinationDirective(2)
	if !strings.Contains(d2, "attempt 2") {
		t.Errorf("attempt 2 directive should contain 'attempt 2', got: %q", d2)
	}
	d3 := hallucinationDirective(3)
	if !strings.Contains(d3, "attempt 3") {
		t.Errorf("attempt 3 directive should contain 'attempt 3', got: %q", d3)
	}
}

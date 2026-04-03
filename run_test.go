package logos

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- proseFilter tests ---

func TestStreamFilter_FastPath_NoAngle(t *testing.T) {
	var got []string
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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

func TestStreamFilter_HarmlessAngle_NotDetected(t *testing.T) {
	var got []string
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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

// --- cmdBlockBuffer tests ---

func TestCmdBlockFilter_PassThrough(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("hello world")
	f.Flush()
	assert.Equal(t, []string{"hello world"}, got)
}

func TestCmdBlockFilter_BlockBuffered(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("before<cmd>\nls\n</cmd>after")
	f.Flush()
	// Block is emitted as a single chunk alongside prose
	assert.Contains(t, got, "before")
	assert.Contains(t, got, "<cmd>\nls\n</cmd>")
	assert.Contains(t, got, "after")
	// Verify order: "before" comes before the block, block comes before "after"
	beforeIdx, blockIdx, afterIdx := -1, -1, -1
	for i, s := range got {
		switch s {
		case "before":
			beforeIdx = i
		case "<cmd>\nls\n</cmd>":
			blockIdx = i
		case "after":
			afterIdx = i
		}
	}
	require.GreaterOrEqual(t, beforeIdx, 0, "\"before\" not found in delegate calls")
	require.GreaterOrEqual(t, blockIdx, 0, "block chunk not found in delegate calls")
	require.GreaterOrEqual(t, afterIdx, 0, "\"after\" not found in delegate calls")
	assert.Less(t, beforeIdx, blockIdx)
	assert.Less(t, blockIdx, afterIdx)
}

func TestCmdBlockFilter_SplitAcrossDeltas(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("text<cm")
	f.Write("d>\nls\n</cm")
	f.Write("d>more")
	f.Flush()
	combined := strings.Join(got, "")
	// Block emitted as one atomic chunk after closing tag is assembled across deltas
	assert.Contains(t, combined, "text")
	assert.Contains(t, got, "<cmd>\nls\n</cmd>", "block should be emitted as one atomic chunk")
	assert.Contains(t, combined, "more")
	assert.True(t, strings.HasPrefix(combined, "text"), "expected 'text' first, got: %q", combined)
	assert.True(t, strings.HasSuffix(combined, "more"), "expected 'more' at end, got: %q", combined)
}

func TestCmdBlockFilter_UnclosedBlock(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("text<cmd>\nls")
	f.Flush()
	combined := strings.Join(got, "")
	// Prose before the block passes through; unclosed block is discarded
	assert.Contains(t, combined, "text")
	assert.NotContains(t, combined, "<cmd>")
	assert.NotContains(t, combined, "ls")
}

func TestCmdBlockFilter_MultipleBlocksInOneWrite(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("before<cmd>\nls\n</cmd>middle<cmd>\npwd\n</cmd>after")
	f.Flush()
	combined := strings.Join(got, "")
	// Both blocks and all prose are emitted
	assert.Contains(t, combined, "before")
	assert.Contains(t, combined, "<cmd>\nls\n</cmd>")
	assert.Contains(t, combined, "middle")
	assert.Contains(t, combined, "<cmd>\npwd\n</cmd>")
	assert.Contains(t, combined, "after")
}

func TestCmdBlockFilter_EmptyBlock(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd></cmd>")
	f.Flush()
	// Empty block is emitted as <cmd></cmd>
	assert.Equal(t, []string{"<cmd></cmd>"}, got)
}

func TestCmdBlockFilter_BlockEmittedAsOneChunk(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd>\nls\n</cmd>")
	f.Flush()
	// Exactly one delegate call with the complete block content
	require.Len(t, got, 1)
	assert.Equal(t, "<cmd>\nls\n</cmd>", got[0])
}

func TestCmdBlockFilter_ClosingTagSplitAcrossDeltas(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd>\nls\n</cm")
	f.Write("d>after")
	f.Flush()
	combined := strings.Join(got, "")
	assert.Contains(t, combined, "<cmd>\nls\n</cmd>")
	assert.Contains(t, combined, "after")
}

func TestCmdBlockFilter_TwoConsecutiveBlocks(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd>\nls\n</cmd><cmd>\npwd\n</cmd>")
	f.Flush()
	// Both blocks emitted as separate delegate calls
	assert.Contains(t, got, "<cmd>\nls\n</cmd>")
	assert.Contains(t, got, "<cmd>\npwd\n</cmd>")
}

func TestCmdBlockFilter_BlockAtStreamStart(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd>\nls\n</cmd>after")
	f.Flush()
	// No empty-string delegate call before the block
	for _, s := range got {
		assert.NotEqual(t, "", s, "unexpected empty-string delegate call")
	}
	assert.Contains(t, got, "<cmd>\nls\n</cmd>")
	assert.Contains(t, got, "after")
}

func TestCmdBlockFilter_CompleteBlockThenUnclosed(t *testing.T) {
	var got []string
	f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
	f.Write("<cmd>\nls\n</cmd><cmd>\npwd")
	f.Flush()
	// First block emitted; second (unclosed) is discarded
	assert.Contains(t, got, "<cmd>\nls\n</cmd>")
	for _, s := range got {
		assert.NotContains(t, s, "pwd", "unclosed block content should be discarded")
	}
}

func TestStreamFilter_BracketToolCall(t *testing.T) {
	var got []string
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
	f.Write("[TOOL_CALL]{tool => 'shell', args => { --command 'ls' }}[/TOOL_CALL]")
	f.Flush()
	if len(got) != 0 {
		t.Errorf("expected no output, got %v", got)
	}
	if !f.toolCallDetected {
		t.Error("toolCallDetected should be true")
	}
}

func TestCmdBlockBuffer_NestedBlocks(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		wantProse  []string
		wantBlocks []string
	}{
		{
			"nested heredoc",
			"text <cmd>cat <<EOF\nhello <cmd>nested</cmd>\nEOF</cmd> more",
			[]string{"text ", " more"},
			[]string{"<cmd>cat <<EOF\nhello <cmd>nested</cmd>\nEOF</cmd>"},
		},
		{
			"content after nested",
			"pre <cmd>start <cmd>nested</cmd> middle</cmd> post",
			[]string{"pre ", " post"},
			[]string{"<cmd>start <cmd>nested</cmd> middle</cmd>"},
		},
		{
			"echo nested",
			"<cmd>echo hello <cmd>world</cmd></cmd>",
			[]string{},
			[]string{"<cmd>echo hello <cmd>world</cmd></cmd>"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			f := &cmdBlockBuffer{proseDelegate: func(s string) { got = append(got, s) }}
			f.Write(tt.input)
			f.Flush()

			prose, blocks := []string{}, []string{}
			for _, s := range got {
				if strings.HasPrefix(s, "<cmd>") {
					blocks = append(blocks, s)
				} else {
					prose = append(prose, s)
				}
			}

			assert.Equal(t, tt.wantProse, prose, "prose mismatch")
			assert.Equal(t, tt.wantBlocks, blocks, "blocks mismatch")
		})
	}
}

func TestStreamFilter_BracketToolCall_CaseInsensitive(t *testing.T) {
	var got []string
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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
	f := &proseFilter{delegate: func(s string) { got = append(got, s) }}
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

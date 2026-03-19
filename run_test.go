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
			text:    "§ ls -la",
			wantPre: "",
			wantCmd: "ls -la",
			wantOK:  true,
		},
		{
			name:    "text before command",
			text:    "Let me check the files.\n§ ls -la",
			wantPre: "Let me check the files.\n",
			wantCmd: "ls -la",
			wantOK:  true,
		},
		{
			name:    "multiline text before command",
			text:    "First line.\nSecond line.\n§ rg pattern /dir",
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
			text:    "§ ls -la\nI expect this to show files.",
			wantPre: "",
			wantCmd: "ls -la",
			wantOK:  true,
		},
		// Multi-command — only first returned (rejection handled in Run loop)
		{
			name:    "only first command taken",
			text:    "§ cmd1\n§ cmd2",
			wantPre: "",
			wantCmd: "cmd1",
			wantOK:  true,
		},
		// Heredoc cases
		{
			name:    "heredoc captured",
			text:    "§ cat <<'EOF'\nline1\nline2\nEOF",
			wantPre: "",
			wantCmd: "cat <<'EOF'\nline1\nline2\nEOF",
			wantOK:  true,
		},
		{
			name:    "text before heredoc",
			text:    "Let me write.\n§ cat <<'EOF'\ncontent\nEOF",
			wantPre: "Let me write.\n",
			wantCmd: "cat <<'EOF'\ncontent\nEOF",
			wantOK:  true,
		},
		{
			name:    "heredoc with pipe",
			text:    "§ cat <<'EOF' | wc -l\nhello\nworld\nEOF",
			wantPre: "",
			wantCmd: "cat <<'EOF' | wc -l\nhello\nworld\nEOF",
			wantOK:  true,
		},
		{
			name:    "dash heredoc with tabs",
			text:    "§ cat <<-'END'\n\thello\n\tworld\nEND",
			wantPre: "",
			wantCmd: "cat <<-'END'\n\thello\n\tworld\nEND",
			wantOK:  true,
		},
		{
			name:    "heredoc with bang line in body",
			text:    "§ cat <<'EOF'\n! not_a_command\nsome text\nEOF",
			wantPre: "",
			wantCmd: "cat <<'EOF'\n! not_a_command\nsome text\nEOF",
			wantOK:  true,
		},
		{
			name:    "unclosed heredoc falls through to single line",
			text:    "§ cat <<'EOF'\nline1\nline2\nno closing",
			wantPre: "",
			wantCmd: "cat <<'EOF'",
			wantOK:  true,
		},
		{
			name:    "dash heredoc with space before delimiter",
			text:    "§ cat <<- 'PLANEOF'\ncontent\nPLANEOF",
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
		{"one command", "§ ls -la", "", []string{"ls -la"}},
		{"two commands", "§ pwd\n§ ls -la", "", []string{"pwd", "ls -la"}},
		{"text before commands", "Let me check.\n§ pwd\n§ ls", "Let me check.\n", []string{"pwd", "ls"}},
		{"heredoc counts as one", "§ cat <<'EOF'\nline1\nEOF\n§ ls", "", []string{"cat <<'EOF'\nline1\nEOF", "ls"}},
		{"bang in heredoc body ignored", "§ cat <<'EOF'\n! fake\nEOF", "", []string{"cat <<'EOF'\n! fake\nEOF"}},
		{"unclosed heredoc fallback then command", "§ cat <<'EOF'\nno close\n§ ls", "", []string{"cat <<'EOF'", "ls"}},
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
	if f.xmlDetected {
		t.Error("xmlDetected should be false")
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
	if !f.xmlDetected {
		t.Error("xmlDetected should be true")
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
	if !f.xmlDetected {
		t.Error("xmlDetected should be true")
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
	if f.xmlDetected {
		t.Error("xmlDetected should be false for think tag")
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
	if f.xmlDetected {
		t.Error("xmlDetected should be false")
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
	if f.xmlDetected {
		t.Error("xmlDetected should be false")
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

// --- cmdLineFilter tests ---

func TestCmdLineFilter_PureProse(t *testing.T) {
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("Hello world\nMore text\n")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "Hello world\nMore text\n" {
		t.Errorf("got %q, want %q", combined, "Hello world\nMore text\n")
	}
}

func TestCmdLineFilter_SingleCmdLineSuppressed(t *testing.T) {
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("before\n§ flicknote get abc\nafter\n")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "before\nafter\n" {
		t.Errorf("got %q, want %q", combined, "before\nafter\n")
	}
}

func TestCmdLineFilter_CmdLineSplitAcrossDeltas(t *testing.T) {
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("text\n§")
	f.Write(" flicknote get abc\nmore text")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "text\nmore text" {
		t.Errorf("got %q, want %q", combined, "text\nmore text")
	}
}

func TestCmdLineFilter_PrefixBuffering(t *testing.T) {
	// delta is just "§" (2 bytes, less than CommandPrefix length of 3)
	// — stays buffered, not emitted until more data arrives
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("§")
	// no flush yet — nothing should be emitted
	if len(out) != 0 {
		t.Errorf("expected no output before flush, got %v", out)
	}
	// completing a non-command line
	f.Write("X regular text\n")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "§X regular text\n" {
		t.Errorf("got %q, want %q", combined, "§X regular text\n")
	}
}

func TestCmdLineFilter_MultipleCmdLinesSuppressed(t *testing.T) {
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("§ cmd1\n§ cmd2\n§ cmd3\n")
	f.Flush()
	if len(out) != 0 {
		t.Errorf("expected no output, got %v", out)
	}
}

func TestCmdLineFilter_FlushWithPartialCmdLine(t *testing.T) {
	// Stream ends mid § line — should be suppressed
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("§ flicknote get")
	f.Flush()
	if len(out) != 0 {
		t.Errorf("expected no output for partial § line, got %v", out)
	}
}

func TestCmdLineFilter_FlushWithPartialNonCmdLine(t *testing.T) {
	// Stream ends mid non-§ line — should be emitted
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("partial line without newline")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "partial line without newline" {
		t.Errorf("got %q, want %q", combined, "partial line without newline")
	}
}

func TestCmdLineFilter_CmdLineAtStartOfTurn(t *testing.T) {
	// § line at start with no preceding \n — still suppressed
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("§ ls -la\nresult text\n")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "result text\n" {
		t.Errorf("got %q, want %q", combined, "result text\n")
	}
}

func TestCmdLineFilter_LeadingSpaceCmdLineSuppressed(t *testing.T) {
	// Models occasionally indent § lines — should still be suppressed (matching ParseCommand).
	var out []string
	f := &cmdLineFilter{delegate: func(s string) { out = append(out, s) }}
	f.Write("prose\n  § indented-cmd\nafter\n")
	f.Flush()
	combined := ""
	for _, s := range out {
		combined += s
	}
	if combined != "prose\nafter\n" {
		t.Errorf("got %q, want %q", combined, "prose\nafter\n")
	}
}

func TestCmdLineFilter_InteractionWithXMLFilter(t *testing.T) {
	// § lines suppressed, XML markers trigger xmlDetected on inner filter
	var delegateOut []string
	xmlFilter := &streamFilter{delegate: func(s string) { delegateOut = append(delegateOut, s) }}
	cmdFilter := &cmdLineFilter{delegate: xmlFilter.Write}

	cmdFilter.Write("prose text\n§ some command\n<tool_call>bad</tool_call>")
	cmdFilter.Flush()
	xmlFilter.Flush()

	combined := ""
	for _, s := range delegateOut {
		combined += s
	}
	if combined != "prose text\n" {
		t.Errorf("got %q, want %q", combined, "prose text\n")
	}
	if !xmlFilter.xmlDetected {
		t.Error("xmlDetected should be true")
	}
}

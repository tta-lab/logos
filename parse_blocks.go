package logos

import (
	"strings"
)

// ParseCmdBlocks extracts the content of each <cmd>...</cmd> block from a
// complete assistant message. Returns contents in document order with
// surrounding whitespace trimmed. Nested blocks are not supported — the first
// </cmd> after a <cmd> closes the block. Unclosed blocks are silently
// dropped (an unclosed block at the end of the message is ignored with no
// error — callers who care can detect via strings.Contains themselves).
//
// This is the non-streaming sibling of the internal cmdBlockBuffer used by
// streamOneTurn. Use this when you have the full text; use cmdBlockBuffer
// when you have a stream of deltas.
func ParseCmdBlocks(text string) []string {
	var out []string
	i := 0
	for {
		open := strings.Index(text[i:], CmdBlockOpen)
		if open < 0 {
			return out
		}
		open += i + len(CmdBlockOpen)
		close := strings.Index(text[open:], CmdBlockClose)
		if close < 0 {
			return out
		}
		out = append(out, strings.TrimSpace(text[open:open+close]))
		i = open + close + len(CmdBlockClose)
	}
}

// StripCmdBlocks returns text with all <cmd>...</cmd> blocks (including the
// tags themselves) removed. Runs of blank lines left behind are collapsed to
// a single blank line. Unclosed blocks are stripped from their opening tag
// to the end of the string.
//
// Use this to prepare prose for display when the raw assistant message
// contains cmd blocks you don't want the human to see (e.g. forwarding to
// chat without showing the tool calls).
func StripCmdBlocks(text string) string {
	var builder strings.Builder
	i := 0
	for {
		open := strings.Index(text[i:], CmdBlockOpen)
		if open < 0 {
			builder.WriteString(text[i:])
			break
		}
		builder.WriteString(text[i : i+open])
		i += open + len(CmdBlockOpen)
		close := strings.Index(text[i:], CmdBlockClose)
		if close < 0 {
			break
		}
		i += close + len(CmdBlockClose)
	}

	// Collapse runs of blank lines into a single blank line.
	result := builder.String()
	// First normalize all runs of 2+ newlines to a single newline.
	for strings.Contains(result, "\n\n") {
		result = strings.ReplaceAll(result, "\n\n", "\n")
	}
	// Then split and re-join to remove any trailing newline.
	lines := strings.Split(result, "\n")
	var cleaned []string
	prevWasBlank := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		isBlank := trimmed == ""
		if isBlank {
			if !prevWasBlank {
				cleaned = append(cleaned, "")
			}
			prevWasBlank = true
		} else {
			cleaned = append(cleaned, line)
			prevWasBlank = false
		}
	}
	return strings.Join(cleaned, "\n")
}

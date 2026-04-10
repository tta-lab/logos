package logos

import (
	"log/slog"
	"regexp"
)

// blockedCmdPatterns detects in-place file editing commands that bypass
// the structured editing tool (src edit). Only the -i flag is matched —
// the clearest signal of in-place editing with near-zero false positives.
//
// Known limitation: heredoc content containing sed -i on its own line
// will false-positive (the (?m)^ anchor matches inside heredoc bodies).
// Acceptable for v1 — heredoc-embedded sed -i is extremely rare in practice.
var blockedCmdPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?m)(?:^|&&|\|\||;|\|)\s*sed\s+(?:-[a-zA-Z]*i|--in-place)`),
	regexp.MustCompile(`(?m)(?:^|&&|\|\||;|\|)\s*perl\s+(?:-[a-zA-Z]*i)`),
}

// ContainsBlockedCommand returns true if cmd contains a blocked in-place
// editing command (sed -i or perl -i).
func ContainsBlockedCommand(cmd string) bool {
	for _, re := range blockedCmdPatterns {
		if re.MatchString(cmd) {
			return true
		}
	}
	return false
}

// blockedCommandDirective returns feedback nudging the model toward src edit.
// Assumes src (organon) is available in PATH — valid for ttal/einai consumers.
func blockedCommandDirective(cmd string) string {
	return "Blocked: sed -i / perl -i is not allowed in this environment. " +
		"Use src edit for file modifications — e.g.:\n" +
		"<cmd>\nsrc edit <file>\n</cmd>\n" +
		"See src --help for usage."
}

// handleBlockedCommand checks for blocked commands and logs a warning if found.
// Returns the directive string and true if blocked, or "" and false otherwise.
func handleBlockedCommand(cmd string) (string, bool) {
	if !ContainsBlockedCommand(cmd) {
		return "", false
	}
	slog.Warn("blocked command detected", "cmd", cmd)
	return blockedCommandDirective(cmd), true
}

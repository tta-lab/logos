package logos

import (
	"fmt"
	"strings"
)

// formatOneResult renders a single Result as the per-entry body (without the
// outer <result> wrap). Exported-package-private — used by FormatResults and
// by the worker goroutine during streamOneTurn execution.
func formatOneResult(r Result) string {
	if r.Err != nil {
		return r.Command + "\n" + "execution error: " + r.Err.Error()
	}
	output := r.Stdout
	if r.Stderr != "" {
		output += "\nSTDERR:\n" + r.Stderr
	}
	if output == "" {
		output = "(no output)"
	}
	formatted := r.Command + "\n" + output
	if r.ExitCode != 0 && r.ExitCode != -1 {
		formatted += fmt.Sprintf("\n(exit code: %d)", r.ExitCode)
	}
	return formatted
}

// FormatResults renders a slice of Results as the single outer <result>...</result>
// wrap that ExecuteBlocks callers feed back to the model as a user message.
// Format matches what streamOneTurn produces internally:
//
//	<result>
//	<cmd-1-verbatim>
//	<stdout-1>
//	STDERR:                        ← only if stderr non-empty
//	<stderr-1>
//	(exit code: N)                 ← only if exit != 0 AND exit != -1
//
//	<cmd-2-verbatim>
//	...
//	</result>
//
// Entries are joined with a single "\n" between them. Empty results slice
// returns an empty string (no outer wrap).
func FormatResults(results []Result) string {
	if len(results) == 0 {
		return ""
	}
	parts := make([]string, len(results))
	for i, r := range results {
		parts[i] = formatOneResult(r)
	}
	return "<result>\n" + strings.Join(parts, "\n") + "\n</result>"
}

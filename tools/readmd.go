package tools

import (
	"context"
	"fmt"
	"os"

	"charm.land/fantasy"
)

// ReadMDParams are the input parameters for the read_md tool.
type ReadMDParams struct {
	FilePath string `json:"file_path" description:"Absolute path to the markdown file to read"`
	Tree     bool   `json:"tree,omitempty" description:"Force tree view (heading structure + char counts)"`
	Section  string `json:"section,omitempty" description:"Section ID to extract (use tree view first to see IDs)"`
	Full     bool   `json:"full,omitempty" description:"Force full content even for large files"`
}

// NewReadMDTool creates a markdown-aware file reader.
// Small files (≤ treeThreshold chars) return full content by default.
// Large files return a heading tree. Agent can override with tree/full/section flags.
func NewReadMDTool(allowedPaths []string, treeThreshold int) fantasy.AgentTool {
	if treeThreshold <= 0 {
		treeThreshold = defaultTreeThreshold
	}
	return fantasy.NewAgentTool(
		"read_md",
		schemaDescription(readMDDescription),
		func(ctx context.Context, params ReadMDParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if !isPathAllowed(params.FilePath, allowedPaths) {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Error: access denied: %q is not within an allowed directory", params.FilePath)), nil //nolint:lll
			}

			info, err := os.Stat(params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Error: %v", err)), nil
			}
			if info.IsDir() {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Error: %q is a directory, not a file", params.FilePath)), nil
			}

			source, err := os.ReadFile(params.FilePath)
			if err != nil {
				return fantasy.NewTextErrorResponse(fmt.Sprintf("Error: %v", err)), nil
			}

			headings := parseHeadings(source)
			assignIDs(headings)

			return renderMarkdownContent(source, headings, params.Section, params.Tree, params.Full, treeThreshold, "file", params.FilePath)
		},
	)
}

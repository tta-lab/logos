package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tta-lab/logos/tools"
)

var readMDFlags struct {
	tree          bool
	section       string
	full          bool
	treeThreshold int
}

var readMDCmd = &cobra.Command{
	Use:   "read-md <file>",
	Short: "Read a markdown file with heading tree and section extraction",
	Long: `Read a markdown file with intelligent rendering.

Small files show full content. Large files show a heading tree with section IDs.
Use --section to extract a specific section by ID.

Examples:
  logos read-md README.md                # auto: full or tree
  logos read-md README.md --tree         # heading tree with IDs
  logos read-md README.md --section 3K   # extract section by ID
  logos read-md README.md --full         # full content`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := tools.ReadMarkdown(
			args[0], readMDFlags.tree, readMDFlags.section, readMDFlags.full, readMDFlags.treeThreshold,
		)
		if err != nil {
			return err
		}
		fmt.Print(result.Content)
		return nil
	},
}

func init() {
	readMDCmd.Flags().BoolVar(&readMDFlags.tree, "tree", false, "Force tree view")
	readMDCmd.Flags().StringVar(&readMDFlags.section, "section", "", "Section ID to extract")
	readMDCmd.Flags().BoolVar(&readMDFlags.full, "full", false, "Force full content")
	readMDCmd.Flags().IntVar(&readMDFlags.treeThreshold, "tree-threshold", 5000, "Char count for auto tree mode")
	rootCmd.AddCommand(readMDCmd)
}

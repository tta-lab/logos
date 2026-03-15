package cli

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/tta-lab/logos/tools"
)

var readFlags struct {
	offset int
	limit  int
}

var readCmd = &cobra.Command{
	Use:   "read <file>",
	Short: tools.ReadCommand.Summary,
	Long:  tools.ReadCommand.Help,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		result, err := tools.ReadFile(args[0], readFlags.offset, readFlags.limit)
		if err != nil {
			return err
		}
		fmt.Print(result.Content)
		return nil
	},
}

func init() {
	readCmd.Flags().IntVar(&readFlags.offset, "offset", 0, "Line number to start reading from (0-based)")
	readCmd.Flags().IntVar(&readFlags.limit, "limit", 0, "Number of lines to read (default 2000)")
	rootCmd.AddCommand(readCmd)
}

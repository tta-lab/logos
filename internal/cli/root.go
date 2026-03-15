package cli

import "github.com/spf13/cobra"

// Version is set at build time via -ldflags.
var Version = "dev"

var rootCmd = &cobra.Command{
	Use:     "logos",
	Short:   "Agent tools — read files, fetch URLs, search the web",
	Version: Version,
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

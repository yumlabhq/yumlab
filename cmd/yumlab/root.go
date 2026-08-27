package main

import (
	"github.com/spf13/cobra"
)

// Exit codes. A scan that finds something exits non-zero so it can gate a
// pre-commit hook or a CI job without any server involved.
const (
	exitClean    = 0
	exitFindings = 1
	exitError    = 2
)

func newRootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "yumlab",
		Short: "Find what will break your GitHub Actions pipeline, before you push",
		Long: `Yumlab reads your workflow files and your repository configuration, and
reports what is going to fail, sorted by the minutes it will waste.

It never writes to your repository, has no backend, and sends no telemetry.
The only network calls it makes are to the GitHub API.`,
		Version:       resolveVersion(),
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.AddCommand(newScanCmd())
	return cmd
}

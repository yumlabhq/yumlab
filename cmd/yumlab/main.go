// Command yumlab scans GitHub Actions workflows and reports what will make a
// pipeline fail, before it is pushed.
package main

import (
	"fmt"
	"os"
)

// version is set at build time by goreleaser.
var version = "dev"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// Cobra has already printed the error for usage problems; this covers
		// the rest.
		fmt.Fprintln(os.Stderr, "yumlab:", err)
		os.Exit(exitError)
	}
}

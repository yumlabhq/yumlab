// Command yumlab scans GitHub Actions workflows and reports what will make a
// pipeline fail, before it is pushed.
package main

import (
	"fmt"
	"os"
	"runtime/debug"
)

// version is set at build time by goreleaser's ldflags. It keeps the default
// when the binary is built any other way, notably by `go install`.
var version = "dev"

// resolveVersion falls back to the version the Go toolchain recorded in the
// binary. `go install module/cmd@v0.1.0` does not run goreleaser, so without
// this every installed binary would report "dev".
func resolveVersion() string {
	if version != "dev" {
		return version
	}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return version
	}
	// Builds from a local checkout report "(devel)", which says less than "dev".
	if v := info.Main.Version; v != "" && v != "(devel)" {
		return v
	}
	return version
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		// Cobra has already printed the error for usage problems; this covers
		// the rest.
		fmt.Fprintln(os.Stderr, "yumlab:", err)
		os.Exit(exitError)
	}
}

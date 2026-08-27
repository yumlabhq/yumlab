package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/yumlabhq/yumlab/internal/github"
	"github.com/yumlabhq/yumlab/internal/report"
	"github.com/yumlabhq/yumlab/internal/scan"
)

type scanFlags struct {
	offline bool
	repo    string
	token   string
	baseURL string
	color   string
}

func newScanCmd() *cobra.Command {
	var flags scanFlags

	cmd := &cobra.Command{
		Use:   "scan [path]",
		Short: "Scan the workflows of a repository",
		Long: `Scan reads .github/workflows in the given directory (the current one by
default) and checks it against the repository's real configuration.

Checking secrets and variables requires a token that can list them, which in
practice means admin access on the repository. Without it Yumlab does not
guess: it reports those references as unverified and says which permission is
missing. Names you already know can be declared in .yumlab.yaml instead.

With --offline, no network call is made at all and only the static controls
run.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			return runScan(cmd, dir, flags)
		},
	}

	f := cmd.Flags()
	f.BoolVar(&flags.offline, "offline", false, "run only the controls that need no network access")
	f.StringVar(&flags.repo, "repo", "", "repository as owner/name (default: the origin remote)")
	f.StringVar(&flags.token, "token", "", "GitHub token (default: $GITHUB_TOKEN)")
	f.StringVar(&flags.baseURL, "api-url", "", "GitHub API base URL for GitHub Enterprise (default: $GITHUB_API_URL)")
	f.StringVar(&flags.color, "color", "auto", `colour output: "auto", "always" or "never"`)

	return cmd
}

func runScan(cmd *cobra.Command, dir string, flags scanFlags) error {
	root := github.FindRoot(dir)

	opts := scan.Options{
		Root:    root,
		Offline: flags.offline,
		Token:   firstNonEmpty(flags.token, os.Getenv("GITHUB_TOKEN"), os.Getenv("GH_TOKEN")),
		BaseURL: firstNonEmpty(flags.baseURL, os.Getenv("GITHUB_API_URL")),
	}

	if !flags.offline {
		repo, err := resolveRepository(flags.repo, dir)
		if err != nil {
			return err
		}
		opts.Repository = repo
	}

	colorOpt, err := parseColor(flags.color)
	if err != nil {
		return err
	}

	rep, err := scan.Run(cmd.Context(), opts)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	if err := report.WriteTerminal(out, rep, report.TerminalOptions{Color: colorOpt}); err != nil {
		return err
	}

	// A control that failed outright is an error, not a clean scan.
	for _, c := range rep.Controls {
		if c.Err != nil {
			return c.Err
		}
	}

	if rep.HasFindings() {
		os.Exit(exitFindings)
	}
	return nil
}

func resolveRepository(flag, dir string) (github.Repository, error) {
	if flag != "" {
		repo, err := github.ParseSlug(flag)
		if err != nil {
			return github.Repository{}, fmt.Errorf("--repo: %w", err)
		}
		repo.Source = "--repo"
		return repo, nil
	}
	repo, err := github.DetectRepository(dir)
	if err != nil {
		return github.Repository{}, err
	}
	return repo, nil
}

func parseColor(v string) (*bool, error) {
	yes, no := true, false
	switch v {
	case "auto":
		return nil, nil
	case "always":
		return &yes, nil
	case "never":
		return &no, nil
	}
	return nil, fmt.Errorf("--color: expected auto, always or never, got %q", v)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

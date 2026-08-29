// Package scan wires the pieces together: load workflows, read the repository
// state when allowed to, run the controls, and build a report.
package scan

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yumlabhq/yumlab/internal/config"
	"github.com/yumlabhq/yumlab/internal/controls"
	"github.com/yumlabhq/yumlab/internal/github"
	"github.com/yumlabhq/yumlab/internal/parse"
	"github.com/yumlabhq/yumlab/internal/report"
)

// Options configures a scan.
type Options struct {
	// Root is the repository root. Workflows are read from
	// <Root>/.github/workflows.
	Root string
	// Repository identifies the repository on GitHub. Ignored when Offline.
	Repository github.Repository
	// Token authenticates the API calls. Empty means no network work is
	// possible, which is treated exactly like Offline.
	Token string
	// BaseURL points at a GitHub Enterprise instance.
	BaseURL string
	// Offline disables every control that needs the network.
	Offline bool
}

// Run performs a scan and returns the report.
//
// Run only returns an error when the scan could not be performed at all. A
// missing permission is not such a case: it degrades the report instead, which
// is the documented behaviour.
func Run(ctx context.Context, opts Options) (*report.Report, error) {
	cfg, err := config.Load(opts.Root)
	if err != nil {
		return nil, err
	}

	workflows, loadErrors, err := parse.LoadDir(opts.Root)
	if err != nil {
		return nil, err
	}

	rep := &report.Report{
		Repository:    opts.Repository.String(),
		Offline:       opts.Offline,
		WorkflowCount: len(workflows),
		LoadErrors:    loadErrors,
		ParseGaps:     parseGaps(workflows),
	}
	if opts.Offline {
		rep.Repository = ""
	}

	enabled, skipped := controls.Selected(cfg, opts.Offline)
	for _, c := range skipped {
		rep.SkippedControls = append(rep.SkippedControls, report.ControlRun{ID: c.ID(), Title: c.Title()})
	}

	in := controls.Input{
		Workflows: workflows,
		Config:    cfg,
		Offline:   opts.Offline,
		Inventory: github.NewInventory(opts.Repository.Owner, opts.Repository.Name, "offline mode"),
	}

	// Only reach for the API if a control that needs it is going to run.
	if needsNetwork(enabled) {
		inv, note, err := inventory(ctx, opts, workflows)
		if err != nil {
			return nil, err
		}
		in.Inventory = inv
		if note != "" {
			rep.Notes = append(rep.Notes, note)
		}
	}

	if cfg.Path != "" {
		rep.Notes = append(rep.Notes, fmt.Sprintf("Using %s", cfg.Path))
	}

	for _, c := range enabled {
		res, err := c.Run(ctx, in)
		run := report.ControlRun{ID: c.ID(), Title: c.Title(), Coverage: res.Coverage}
		if err != nil {
			run.Err = fmt.Errorf("control %s: %w", c.ID(), err)
		}
		rep.Controls = append(rep.Controls, run)
		rep.Findings = append(rep.Findings, res.Findings...)
	}

	controls.SortFindings(rep.Findings)
	return rep, nil
}

// parseGaps turns the expressions the parser could not read into gaps.
//
// These belong to the scan, not to a control. An expression nobody can parse is
// invisible to every control at once, so leaving it to a control would mean it
// disappears entirely when that control is disabled or skipped — which is
// exactly what happens offline. Counting it here keeps the promise that
// anything unverified is announced rather than dropped.
func parseGaps(workflows []*parse.Workflow) []controls.Gap {
	byReason := map[string]*controls.Gap{}
	var order []string

	for _, w := range workflows {
		for _, u := range w.Unresolved {
			// The parser's own message names an offset that means nothing to a
			// user; the location already says where it is.
			reason := "the expression could not be parsed, so its references are unknown"
			gap, ok := byReason[reason]
			if !ok {
				gap = &controls.Gap{Reason: reason}
				byReason[reason] = gap
				order = append(order, reason)
			}
			gap.Refs = append(gap.Refs, strings.TrimSpace(u.Text))
			gap.Locs = append(gap.Locs, u.Loc)
		}
	}

	out := make([]controls.Gap, 0, len(order))
	for _, reason := range order {
		out = append(out, *byReason[reason])
	}
	return out
}

func needsNetwork(cs []controls.Control) bool {
	for _, c := range cs {
		if c.NeedsNetwork() {
			return true
		}
	}
	return false
}

// inventory reads the repository state. The returned note explains a degraded
// read to the user.
func inventory(ctx context.Context, opts Options, workflows []*parse.Workflow) (*github.Inventory, string, error) {
	if opts.Token == "" {
		return github.NewInventory(opts.Repository.Owner, opts.Repository.Name,
				"no GitHub token: set GITHUB_TOKEN to check secrets and variables"),
			"No GitHub token found. Set GITHUB_TOKEN to verify secrets, or run with --offline.", nil
	}

	client, err := github.New(opts.Repository.Owner, opts.Repository.Name, github.Options{
		Token:   opts.Token,
		BaseURL: opts.BaseURL,
	})
	if err != nil {
		return nil, "", err
	}

	inv, err := client.Inventory(ctx, environmentNames(workflows))
	if err != nil {
		return nil, "", fmt.Errorf("read repository state: %w", err)
	}
	return inv, "", nil
}

// environmentNames collects the deployment environments the workflows actually
// reference, so the scan does not read environments nobody uses.
func environmentNames(workflows []*parse.Workflow) []string {
	seen := map[string]bool{}
	for _, w := range workflows {
		for _, id := range w.JobOrder {
			for _, env := range w.Jobs[id].Environments {
				seen[env.Name] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

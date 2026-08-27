// Package controls holds the detection rules. Every control lives in its own
// file, implements the same interface, and can be enabled or disabled without
// touching any other package.
//
// Two rules apply to every control:
//
//   - Detection is deterministic. No model decides whether there is a problem.
//   - Anything that cannot be verified is reported as a Gap, never as a
//     Finding. A false "you are missing a secret" costs more trust than ten
//     missed problems.
package controls

import (
	"context"
	"sort"

	"github.com/yumlab/yumlab/internal/config"
	"github.com/yumlab/yumlab/internal/github"
	"github.com/yumlab/yumlab/internal/parse"
)

// Severity ranks a finding.
type Severity int

const (
	// SeverityError means the pipeline will fail.
	SeverityError Severity = iota
	// SeverityWarning means the pipeline will probably waste time.
	SeverityWarning
	// SeverityInfo is worth knowing but breaks nothing.
	SeverityInfo
)

func (s Severity) String() string {
	switch s {
	case SeverityError:
		return "error"
	case SeverityWarning:
		return "warning"
	case SeverityInfo:
		return "info"
	}
	return "unknown"
}

// Finding is one problem Yumlab is confident about.
type Finding struct {
	ControlID string
	Severity  Severity

	// Loc is the primary position, always file:line.
	Loc parse.Location
	// Others lists the remaining positions when the same problem appears more
	// than once. The estimate is not multiplied by them.
	Others []parse.Location

	// Message is one short actionable line: what and where.
	Message string
	// Detail explains why it matters and how to fix it.
	Detail string

	// WastedMinutes is the estimated cost. It is never zero on a finding.
	WastedMinutes int
}

// Count is the number of places this finding was seen.
func (f Finding) Count() int { return 1 + len(f.Others) }

// Gap is something the control could not verify. Gaps are the UNKNOWN output:
// they are counted and shown, and they never become findings.
type Gap struct {
	// Reason is user-facing and says what would unblock the check.
	Reason string
	// Refs are the references affected, for example "secrets.NPM_TOKEN".
	Refs []string
	// Locs mirrors Refs so the user can find them.
	Locs []parse.Location
}

// Count is the number of unverified references behind this gap.
func (g Gap) Count() int { return len(g.Refs) }

// Coverage is what a control managed to check and what it did not.
type Coverage struct {
	// Checked is the number of items the control verified conclusively.
	Checked int
	// Gaps groups everything it could not verify.
	Gaps []Gap
}

// Unverified is the total number of references behind all gaps.
func (c Coverage) Unverified() int {
	var n int
	for _, g := range c.Gaps {
		n += g.Count()
	}
	return n
}

// Input is everything a control gets to work with.
type Input struct {
	// Workflows are the parsed workflow files. Always present.
	Workflows []*parse.Workflow
	// Inventory is the repository state read from the API. In offline mode
	// every scope is marked as skipped, never as empty.
	Inventory *github.Inventory
	// Config is the .yumlab.yaml content, or the zero value.
	Config config.Config
	// Offline reports whether network controls were skipped.
	Offline bool
}

// Result is what a control returns.
type Result struct {
	Findings []Finding
	Coverage Coverage
}

// Control is the interface every rule implements.
type Control interface {
	// ID is the stable identifier used in reports and in the config file.
	ID() string
	// Title is a one-line description.
	Title() string
	// NeedsNetwork reports whether the control calls the GitHub API. Only
	// controls that do not can run in --offline mode or in a pre-commit hook.
	NeedsNetwork() bool
	// Run executes the control. It returns an error only when it cannot run at
	// all; anything it merely could not verify belongs in Coverage.Gaps.
	Run(ctx context.Context, in Input) (Result, error)
}

// All returns every registered control, in a stable order.
func All() []Control {
	return []Control{
		&GhostSecrets{},
	}
}

// Selected returns the controls enabled by cfg, dropping network controls when
// offline. It also reports which controls were skipped for needing the network,
// so the report can say so instead of silently checking less.
func Selected(cfg config.Config, offline bool) (enabled, skipped []Control) {
	for _, c := range All() {
		if !cfg.ControlEnabled(c.ID()) {
			continue
		}
		if offline && c.NeedsNetwork() {
			skipped = append(skipped, c)
			continue
		}
		enabled = append(enabled, c)
	}
	return enabled, skipped
}

// SortFindings orders findings by estimated wasted minutes, which is the order
// the report shows. Severity only breaks ties: the point of the product is the
// time lost, not the label.
func SortFindings(f []Finding) {
	sort.SliceStable(f, func(i, j int) bool {
		if f[i].WastedMinutes != f[j].WastedMinutes {
			return f[i].WastedMinutes > f[j].WastedMinutes
		}
		if f[i].Severity != f[j].Severity {
			return f[i].Severity < f[j].Severity
		}
		if f[i].Loc.File != f[j].Loc.File {
			return f[i].Loc.File < f[j].Loc.File
		}
		return f[i].Loc.Line < f[j].Loc.Line
	})
}

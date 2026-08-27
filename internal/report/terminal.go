package report

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/yumlabhq/yumlab/internal/controls"
	"github.com/yumlabhq/yumlab/internal/score"
)

// palette holds the escape sequences, empty when colour is disabled.
type palette struct {
	reset, bold, dim, red, yellow, cyan, green string
}

var colored = palette{
	reset:  "\033[0m",
	bold:   "\033[1m",
	dim:    "\033[2m",
	red:    "\033[31m",
	yellow: "\033[33m",
	cyan:   "\033[36m",
	green:  "\033[32m",
}

// TerminalOptions configures the terminal renderer.
type TerminalOptions struct {
	// Color forces colour on or off. When nil, colour is enabled if the output
	// is a terminal and NO_COLOR is unset.
	Color *bool
}

// WriteTerminal renders a report for a human reading a terminal.
func WriteTerminal(w io.Writer, r *Report, opts TerminalOptions) error {
	p := palette{}
	if useColor(w, opts) {
		p = colored
	}
	t := &termWriter{w: w, p: p}

	t.header(r)
	t.loadErrors(r)
	t.findings(r)
	t.unknown(r)
	t.summary(r)

	return t.err
}

func useColor(w io.Writer, opts TerminalOptions) bool {
	if opts.Color != nil {
		return *opts.Color
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	// Colour only when writing to a real terminal, so piped output and CI logs
	// stay clean.
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

type termWriter struct {
	w   io.Writer
	p   palette
	err error
}

func (t *termWriter) printf(format string, args ...any) {
	if t.err != nil {
		return
	}
	_, t.err = fmt.Fprintf(t.w, format, args...)
}

func (t *termWriter) header(r *Report) {
	p := t.p
	scope := "offline"
	if !r.Offline {
		scope = "repository, organization and environments"
	}

	target := r.Repository
	if target == "" {
		target = "current directory"
	}

	t.printf("\n%syumlab%s  %s  %s%d workflow%s · %s%s\n\n",
		p.bold, p.reset, target,
		p.dim, r.WorkflowCount, plural(r.WorkflowCount), scope, p.reset)
}

func (t *termWriter) loadErrors(r *Report) {
	if len(r.LoadErrors) == 0 {
		return
	}
	p := t.p
	t.printf("%s%sCould not parse%s\n", p.bold, p.yellow, p.reset)
	for _, e := range r.LoadErrors {
		t.printf("  %s\n    %s%v%s\n", e.Path, p.dim, e.Err, p.reset)
	}
	t.printf("\n")
}

func (t *termWriter) findings(r *Report) {
	p := t.p
	if len(r.Findings) == 0 {
		return
	}

	// Findings are ordered by estimated minutes wasted, not by severity: the
	// time lost is the point.
	sorted := make([]controls.Finding, len(r.Findings))
	copy(sorted, r.Findings)
	controls.SortFindings(sorted)

	for _, f := range sorted {
		marker, color := severityMarker(p, f.Severity)

		t.printf("  %s%s%s %s%s%s  %s%s%s\n",
			color, marker, p.reset,
			p.bold, f.Message, p.reset,
			p.dim, f.Loc.Short(), p.reset)

		t.printf("      %s~%s wasted%s\n",
			p.yellow, score.FormatMinutes(f.WastedMinutes), p.reset)

		for _, line := range wrap(f.Detail, 78) {
			t.printf("      %s%s%s\n", p.dim, line, p.reset)
		}

		if len(f.Others) > 0 {
			locs := make([]string, 0, len(f.Others))
			for _, l := range f.Others {
				locs = append(locs, l.Short())
			}
			t.printf("      %salso at %s%s\n", p.dim, strings.Join(locs, ", "), p.reset)
		}
		t.printf("\n")
	}
}

func severityMarker(p palette, s controls.Severity) (string, string) {
	switch s {
	case controls.SeverityError:
		return "✗", p.red
	case controls.SeverityWarning:
		return "!", p.yellow
	default:
		return "·", p.cyan
	}
}

// unknown renders the UNKNOWN section. It is printed whenever something could
// not be verified, including when there are no findings at all: "nothing found"
// and "nothing could be checked" must never look the same.
func (t *termWriter) unknown(r *Report) {
	p := t.p

	gaps := r.Gaps()
	if len(gaps) == 0 && len(r.SkippedControls) == 0 {
		return
	}

	// Merge identical reasons coming from different controls.
	merged := map[string]int{}
	var order []string
	for _, g := range gaps {
		if _, seen := merged[g.Reason]; !seen {
			order = append(order, g.Reason)
		}
		merged[g.Reason] += g.Count()
	}
	sort.SliceStable(order, func(i, j int) bool { return merged[order[i]] > merged[order[j]] })

	if total := r.Unverified(); total > 0 {
		t.printf("%s%sUNKNOWN%s  %s%d reference%s could not be verified%s\n",
			p.bold, p.yellow, p.reset, p.dim, total, plural(total), p.reset)
	} else {
		t.printf("%s%sUNKNOWN%s\n", p.bold, p.yellow, p.reset)
	}

	for _, reason := range order {
		n := merged[reason]
		t.printf("  %s%d%s  %s\n", p.bold, n, p.reset, reason)
	}

	for _, c := range r.SkippedControls {
		t.printf("  %s–%s  %s: not run, it needs network access\n", p.dim, p.reset, c.ID)
	}
	t.printf("\n")
}

func (t *termWriter) summary(r *Report) {
	p := t.p

	for _, note := range r.Notes {
		t.printf("%s%s%s\n", p.dim, note, p.reset)
	}
	if len(r.Notes) > 0 {
		t.printf("\n")
	}

	n := len(r.Findings)
	switch {
	// A scan that ran nothing must never look like a clean scan.
	case len(r.Controls) == 0:
		t.printf("%s· no control ran%s  %s%d skipped, they need network access%s\n\n",
			p.cyan, p.reset, p.dim, len(r.SkippedControls), p.reset)

	// Only a scan that verified everything earns the green tick.
	case n == 0 && r.Unverified() == 0 && r.Checked() > 0:
		t.printf("%s✓ %d reference%s checked, nothing will break%s\n\n",
			p.green, r.Checked(), plural(r.Checked()), p.reset)

	// Nothing found, but something could not be checked. This is not a clean
	// bill of health and must not be coloured like one.
	case n == 0:
		t.printf("%s· no findings%s  %s%d checked · %d unverified%s\n\n",
			p.cyan, p.reset, p.dim, r.Checked(), r.Unverified(), p.reset)

	default:
		t.printf("%s%s%d finding%s%s  %s~%s wasted per pipeline%s  %s%d checked · %d unverified%s\n\n",
			p.bold, p.red, n, plural(n), p.reset,
			p.yellow, score.FormatMinutes(r.TotalWastedMinutes()), p.reset,
			p.dim, r.Checked(), r.Unverified(), p.reset)
	}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// wrap breaks text into lines of at most width characters, on word boundaries.
func wrap(text string, width int) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}
	var (
		lines []string
		line  strings.Builder
	)
	for _, word := range words {
		if line.Len() > 0 && line.Len()+1+len(word) > width {
			lines = append(lines, line.String())
			line.Reset()
		}
		if line.Len() > 0 {
			line.WriteByte(' ')
		}
		line.WriteString(word)
	}
	if line.Len() > 0 {
		lines = append(lines, line.String())
	}
	return lines
}

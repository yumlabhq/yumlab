// Package report renders scan results.
//
// The report has three parts and they are all mandatory: what Yumlab found,
// what it could not verify, and how many minutes the findings are estimated to
// cost. Hiding the second part would make the first look more complete than it
// is.
package report

import (
	"github.com/yumlab/yumlab/internal/controls"
	"github.com/yumlab/yumlab/internal/parse"
)

// ControlRun is the outcome of one control.
type ControlRun struct {
	ID       string
	Title    string
	Coverage controls.Coverage
	// Err is set when the control could not run at all.
	Err error
}

// Report is everything a scan produced.
type Report struct {
	// Repository is "owner/name", empty in offline mode without detection.
	Repository string
	// Offline reports whether the scan ran without any network access.
	Offline bool
	// WorkflowCount is the number of workflow files that were parsed.
	WorkflowCount int
	// LoadErrors lists workflow files that could not be parsed.
	LoadErrors []parse.LoadError
	// SkippedControls lists controls that were not run because they need the
	// network and the scan was offline.
	SkippedControls []ControlRun

	Findings []controls.Finding
	Controls []ControlRun

	// Notes are scan-level remarks, such as why a scope could not be read.
	Notes []string
}

// TotalWastedMinutes sums the estimates of every finding.
func (r *Report) TotalWastedMinutes() int {
	var sum int
	for _, f := range r.Findings {
		sum += f.WastedMinutes
	}
	return sum
}

// Unverified counts every reference no control could check.
func (r *Report) Unverified() int {
	var n int
	for _, c := range r.Controls {
		n += c.Coverage.Unverified()
	}
	return n
}

// Checked counts every reference that was verified conclusively.
func (r *Report) Checked() int {
	var n int
	for _, c := range r.Controls {
		n += c.Coverage.Checked
	}
	return n
}

// Gaps returns every gap across controls, most affected first.
func (r *Report) Gaps() []controls.Gap {
	var out []controls.Gap
	for _, c := range r.Controls {
		out = append(out, c.Coverage.Gaps...)
	}
	return out
}

// HasFindings reports whether the scan should exit non-zero.
func (r *Report) HasFindings() bool { return len(r.Findings) > 0 }

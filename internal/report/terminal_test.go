package report

import (
	"bytes"
	"strings"
	"testing"

	"github.com/yumlab/yumlab/internal/controls"
	"github.com/yumlab/yumlab/internal/parse"
)

func render(t *testing.T, r *Report) string {
	t.Helper()
	var buf bytes.Buffer
	off := false
	if err := WriteTerminal(&buf, r, TerminalOptions{Color: &off}); err != nil {
		t.Fatalf("WriteTerminal: %v", err)
	}
	return buf.String()
}

func sampleReport() *Report {
	return &Report{
		Repository:    "acme/app",
		WorkflowCount: 3,
		Findings: []controls.Finding{
			{
				ControlID: "ghost-secrets",
				Severity:  controls.SeverityError,
				Loc:       parse.Location{File: ".github/workflows/release.yml", Line: 41, Col: 22},
				Others:    []parse.Location{{File: ".github/workflows/release.yml", Line: 48}},
				Message:   "secrets.AWS_DEPLOY_ROLE is not defined",
				Detail: "Referenced from job \"deploy\" (environment production). " +
					"Looked in: repository, organization, environment production.",
				WastedMinutes: 16,
			},
			{
				ControlID:     "ghost-secrets",
				Severity:      controls.SeverityError,
				Loc:           parse.Location{File: ".github/workflows/ci.yml", Line: 12},
				Message:       "vars.API_BASE is not defined",
				Detail:        "Referenced from job \"test\".",
				WastedMinutes: 16,
			},
		},
		Controls: []ControlRun{{
			ID:    "ghost-secrets",
			Title: "Secrets and variables referenced but never defined",
			Coverage: controls.Coverage{
				Checked: 7,
				Gaps: []controls.Gap{
					{
						Reason: "cannot read organization secrets: the token needs the \"Secrets\" repository permission (read)",
						Refs:   []string{"secrets.NPM_TOKEN", "secrets.SLACK_WEBHOOK"},
						Locs: []parse.Location{
							{File: ".github/workflows/ci.yml", Line: 4},
							{File: ".github/workflows/ci.yml", Line: 9},
						},
					},
					{
						Reason: "the secret name is computed at runtime",
						Refs:   []string{"secrets[format('{0}_KEY', env.REGION)]"},
						Locs:   []parse.Location{{File: ".github/workflows/deploy.yml", Line: 22}},
					},
				},
			},
		}},
	}
}

func TestTerminalShowsFindingsWithLocationsAndMinutes(t *testing.T) {
	out := render(t, sampleReport())

	for _, want := range []string{
		"secrets.AWS_DEPLOY_ROLE is not defined",
		".github/workflows/release.yml:41",
		"~16 min wasted",
		"also at .github/workflows/release.yml:48",
		"vars.API_BASE is not defined",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output is missing %q\n---\n%s", want, out)
		}
	}
}

// The UNKNOWN section is not optional: a report that hides what it could not
// check looks more complete than it is.
func TestTerminalShowsUnknownSection(t *testing.T) {
	out := render(t, sampleReport())

	if !strings.Contains(out, "UNKNOWN") {
		t.Fatalf("output has no UNKNOWN section\n---\n%s", out)
	}
	if !strings.Contains(out, "3 references could not be verified") {
		t.Errorf("output should count the unverified references\n---\n%s", out)
	}
	for _, want := range []string{
		"organization secrets",
		"computed at runtime",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("UNKNOWN section is missing %q\n---\n%s", want, out)
		}
	}
}

func TestTerminalSummaryTotalsMinutes(t *testing.T) {
	out := render(t, sampleReport())
	if !strings.Contains(out, "2 findings") {
		t.Errorf("summary should count the findings\n---\n%s", out)
	}
	if !strings.Contains(out, "~32 min wasted") {
		t.Errorf("summary should total the estimates (16 + 16)\n---\n%s", out)
	}
	if !strings.Contains(out, "7 checked · 3 unverified") {
		t.Errorf("summary should show coverage\n---\n%s", out)
	}
}

func TestTerminalCleanScan(t *testing.T) {
	r := &Report{
		Repository:    "acme/app",
		WorkflowCount: 2,
		Controls: []ControlRun{{
			ID:       "ghost-secrets",
			Coverage: controls.Coverage{Checked: 12},
		}},
	}
	out := render(t, r)
	if !strings.Contains(out, "✓ 12 references checked, nothing will break") {
		t.Errorf("a fully verified clean scan should say so\n---\n%s", out)
	}
	if strings.Contains(out, "UNKNOWN") {
		t.Errorf("no UNKNOWN section when everything was verified\n---\n%s", out)
	}
}

// Finding nothing while being unable to check most references is not a clean
// bill of health, and must not be presented as one.
func TestTerminalPartialScanIsNotPresentedAsClean(t *testing.T) {
	r := &Report{
		Repository:    "acme/app",
		WorkflowCount: 4,
		Controls: []ControlRun{{
			ID: "ghost-secrets",
			Coverage: controls.Coverage{
				Checked: 2,
				Gaps: []controls.Gap{{
					Reason: "no GitHub token: set GITHUB_TOKEN to check secrets and variables",
					Refs:   []string{"secrets.A", "secrets.B", "secrets.C"},
				}},
			},
		}},
	}
	out := render(t, r)

	if strings.Contains(out, "✓") {
		t.Errorf("a partial scan must not use the clean-scan tick\n---\n%s", out)
	}
	if !strings.Contains(out, "2 checked · 3 unverified") {
		t.Errorf("the summary must show what was left unchecked\n---\n%s", out)
	}
	if !strings.Contains(out, "UNKNOWN") {
		t.Errorf("the UNKNOWN section must be shown\n---\n%s", out)
	}
}

// A scan where no control ran must not read like a clean bill of health.
func TestTerminalNoControlRan(t *testing.T) {
	r := &Report{
		Offline:         true,
		WorkflowCount:   4,
		SkippedControls: []ControlRun{{ID: "ghost-secrets"}},
	}
	out := render(t, r)

	if strings.Contains(out, "no findings") || strings.Contains(out, "nothing will break") {
		t.Errorf("an empty scan must not claim to be clean\n---\n%s", out)
	}
	if !strings.Contains(out, "no control ran") {
		t.Errorf("output should say that no control ran\n---\n%s", out)
	}
	if !strings.Contains(out, "ghost-secrets: not run, it needs network access") {
		t.Errorf("output should name the skipped control\n---\n%s", out)
	}
}

func TestTerminalReportsUnparsableWorkflows(t *testing.T) {
	r := &Report{
		WorkflowCount: 1,
		LoadErrors: []parse.LoadError{
			{Path: ".github/workflows/broken.yml", Err: errString("yaml: line 4: did not find expected key")},
		},
		Controls: []ControlRun{{ID: "ghost-secrets"}},
	}
	out := render(t, r)
	if !strings.Contains(out, ".github/workflows/broken.yml") {
		t.Errorf("a workflow that failed to parse must be reported\n---\n%s", out)
	}
}

type errString string

func (e errString) Error() string { return string(e) }

func TestNoColorProducesNoEscapes(t *testing.T) {
	out := render(t, sampleReport())
	if strings.Contains(out, "\033[") {
		t.Errorf("colour disabled but output contains escape sequences:\n%q", out)
	}
}

func TestColorProducesEscapes(t *testing.T) {
	var buf bytes.Buffer
	on := true
	if err := WriteTerminal(&buf, sampleReport(), TerminalOptions{Color: &on}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\033[") {
		t.Error("colour enabled but output has no escape sequences")
	}
}

func TestReportAggregates(t *testing.T) {
	r := sampleReport()
	if got := r.TotalWastedMinutes(); got != 32 {
		t.Errorf("TotalWastedMinutes = %d, want 32", got)
	}
	if got := r.Unverified(); got != 3 {
		t.Errorf("Unverified = %d, want 3", got)
	}
	if got := r.Checked(); got != 7 {
		t.Errorf("Checked = %d, want 7", got)
	}
	if !r.HasFindings() {
		t.Error("HasFindings should be true")
	}
}

func TestWrap(t *testing.T) {
	lines := wrap("the quick brown fox jumps over the lazy dog", 12)
	for _, l := range lines {
		if len(l) > 12 {
			t.Errorf("line %q exceeds the width", l)
		}
	}
	if strings.Join(lines, " ") != "the quick brown fox jumps over the lazy dog" {
		t.Errorf("wrap lost or reordered words: %v", lines)
	}
	if wrap("", 10) != nil {
		t.Error("wrapping an empty string should give no lines")
	}
}

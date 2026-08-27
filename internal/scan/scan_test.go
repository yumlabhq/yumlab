package scan

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yumlab/yumlab/internal/controls"
	"github.com/yumlab/yumlab/internal/parse"
)

// repoWith builds a throwaway repository containing the given workflow files,
// copied from testdata.
func repoWith(t *testing.T, names ...string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "workflows", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// Offline mode must make no network call and reach no conclusion. The absence
// of a token here guarantees any API call would fail loudly.
func TestOfflineScanRunsNoNetworkControl(t *testing.T) {
	root := repoWith(t, "broken-secrets.yml", "healthy.yml")

	rep, err := Run(context.Background(), Options{Root: root, Offline: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if rep.WorkflowCount != 2 {
		t.Errorf("WorkflowCount = %d, want 2", rep.WorkflowCount)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("offline scan produced findings: %v", rep.Findings)
	}
	if len(rep.Controls) != 0 {
		t.Errorf("offline scan ran %d controls, want 0 in v0.1", len(rep.Controls))
	}
	if len(rep.SkippedControls) != 1 || rep.SkippedControls[0].ID != controls.GhostSecretsID {
		t.Errorf("offline scan should report ghost-secrets as skipped, got %v", rep.SkippedControls)
	}
	if rep.HasFindings() {
		t.Error("HasFindings should be false, the scan must exit clean")
	}
}

// Without a token the scan still runs, but concludes nothing and says why.
func TestScanWithoutTokenDegradesInsteadOfFailing(t *testing.T) {
	root := repoWith(t, "broken-secrets.yml")

	rep, err := Run(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Run without a token should not fail: %v", err)
	}

	if len(rep.Findings) != 0 {
		t.Errorf("no token means no conclusion, got findings: %v", rep.Findings)
	}
	if rep.Unverified() == 0 {
		t.Error("every reference should be reported as unverified")
	}

	var explained bool
	for _, n := range rep.Notes {
		if strings.Contains(n, "GITHUB_TOKEN") {
			explained = true
		}
	}
	if !explained {
		t.Errorf("the report should say a token is missing, notes = %v", rep.Notes)
	}
}

func TestScanReportsUnparsableWorkflows(t *testing.T) {
	root := repoWith(t, "healthy.yml")
	bad := filepath.Join(root, ".github", "workflows", "broken.yml")
	if err := os.WriteFile(bad, []byte("jobs: [unclosed\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(context.Background(), Options{Root: root, Offline: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.LoadErrors) != 1 {
		t.Fatalf("got %d load errors, want 1", len(rep.LoadErrors))
	}
	if rep.LoadErrors[0].Path != parse.WorkflowsDir+"/broken.yml" {
		t.Errorf("load error path = %q", rep.LoadErrors[0].Path)
	}
	if rep.WorkflowCount != 1 {
		t.Errorf("WorkflowCount = %d, want 1: a broken file must not stop the others", rep.WorkflowCount)
	}
}

func TestScanWithoutWorkflowsIsNotAnError(t *testing.T) {
	rep, err := Run(context.Background(), Options{Root: t.TempDir(), Offline: true})
	if err != nil {
		t.Fatalf("a repository with no workflows should scan cleanly: %v", err)
	}
	if rep.WorkflowCount != 0 || rep.HasFindings() {
		t.Errorf("expected an empty clean report, got %+v", rep)
	}
}

func TestScanLoadsConfig(t *testing.T) {
	root := repoWith(t, "healthy.yml")
	cfg := "controls:\n  ghost-secrets: false\n"
	if err := os.WriteFile(filepath.Join(root, ".yumlab.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}

	rep, err := Run(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(rep.Controls) != 0 || len(rep.SkippedControls) != 0 {
		t.Error("a control disabled in .yumlab.yaml should not run")
	}

	var mentioned bool
	for _, n := range rep.Notes {
		if strings.Contains(n, ".yumlab.yaml") {
			mentioned = true
		}
	}
	if !mentioned {
		t.Errorf("the report should say the config was used, notes = %v", rep.Notes)
	}
}

func TestScanRejectsBrokenConfig(t *testing.T) {
	root := repoWith(t, "healthy.yml")
	if err := os.WriteFile(filepath.Join(root, ".yumlab.yaml"), []byte("controls: [oops\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(context.Background(), Options{Root: root, Offline: true}); err == nil {
		t.Error("a malformed .yumlab.yaml should be an error, not silently ignored")
	}
}

func TestEnvironmentNamesCollectsOnlyReferencedEnvironments(t *testing.T) {
	src := `
on: push
jobs:
  a:
    environment: production
  b:
    environment:
      name: staging
  c:
    environment: production
  d:
    runs-on: ubuntu-latest
  e:
    environment: ${{ inputs.target }}
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	got := environmentNames([]*parse.Workflow{w})
	want := []string{"production", "staging"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("environmentNames = %v, want %v", got, want)
	}
}

package parse

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func load(t *testing.T, name string) *Workflow {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "workflows", name)
	w, err := LoadFile(path, "testdata/workflows/"+name)
	if err != nil {
		t.Fatalf("LoadFile(%s): %v", name, err)
	}
	return w
}

func TestParseJobsAndEnvironments(t *testing.T) {
	w := load(t, "broken-secrets.yml")

	if w.Name != "Release" {
		t.Errorf("Name = %q, want %q", w.Name, "Release")
	}
	want := []string{"build", "deploy", "notify"}
	if strings.Join(w.JobOrder, ",") != strings.Join(want, ",") {
		t.Errorf("JobOrder = %v, want %v", w.JobOrder, want)
	}

	deploy := w.Job("deploy")
	if deploy == nil {
		t.Fatal("job deploy not found")
	}
	if len(deploy.Environments) != 1 || deploy.Environments[0].Name != "production" {
		t.Errorf("deploy environments = %v, want [production]", deploy.Environments)
	}
	if deploy.EnvironmentDynamic {
		t.Error("deploy environment should not be dynamic")
	}
	if build := w.Job("build"); build == nil || len(build.Environments) != 0 {
		t.Errorf("build should have no environment, got %v", build.Environments)
	}
}

func TestParseAttributesReferencesToJobs(t *testing.T) {
	w := load(t, "broken-secrets.yml")

	// A name can legitimately appear several times, so index by name and job.
	seen := map[string]bool{}
	for _, r := range w.References {
		if r.Context == "secrets" || r.Context == "vars" {
			seen[r.Context+"."+r.Name()+"@"+r.JobID] = true
		}
	}

	tests := []struct {
		ref   string
		jobID string
	}{
		{"secrets.NPM_TOKEN", ""},      // top-level env block, outside any job
		{"secrets.NPM_TOKEN", "build"}, // and again in the publish step
		{"secrets.RELEASE_WEBHOOK", "build"},
		{"secrets.AWS_DEPLOY_ROLE", "deploy"},
		{"secrets.SENTRY_DSN", "deploy"},
		{"vars.AWS_REGION", "deploy"},
		{"secrets.SLACK_WEBHOOK", "notify"},
	}
	for _, tc := range tests {
		if !seen[tc.ref+"@"+tc.jobID] {
			t.Errorf("reference %s not found in job %q", tc.ref, tc.jobID)
		}
	}
}

// A finding must point at the exact line, so verify against the real file.
func TestReferencePositions(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "testdata", "workflows", "broken-secrets.yml"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(src), "\n")

	w := load(t, "broken-secrets.yml")
	if len(w.References) == 0 {
		t.Fatal("no references parsed")
	}

	for _, r := range w.References {
		if r.Loc.Line < 1 || r.Loc.Line > len(lines) {
			t.Errorf("%s: line %d out of range", r.Raw, r.Loc.Line)
			continue
		}
		line := lines[r.Loc.Line-1]
		if !strings.Contains(line, r.Raw) {
			t.Errorf("%s reported at %s, but that line is %q", r.Raw, r.Loc, line)
			continue
		}
		if r.Loc.Col > 0 {
			start := r.Loc.Col - 1
			if start+len(r.Raw) > len(line) || line[start:start+len(r.Raw)] != r.Raw {
				t.Errorf("%s: column %d does not point at the reference in %q", r.Raw, r.Loc.Col, line)
			}
		}
	}
}

// The `run: |` block is a literal scalar: positions inside it must still be exact.
func TestPositionInsideLiteralBlock(t *testing.T) {
	w := load(t, "broken-secrets.yml")
	var found bool
	for _, r := range w.References {
		if r.Name() == "RELEASE_WEBHOOK" {
			found = true
			// Second line of the `run: |` block, which starts at line 27.
			if r.Loc.Line != 28 {
				t.Errorf("RELEASE_WEBHOOK at line %d, want 28", r.Loc.Line)
			}
		}
	}
	if !found {
		t.Error("RELEASE_WEBHOOK reference not found")
	}
}

// `if:` accepts a bare expression with no ${{ }} wrapper.
func TestBareConditionIsScanned(t *testing.T) {
	w := load(t, "broken-secrets.yml")
	for _, r := range w.References {
		if r.Context == "secrets" && r.Name() == "SLACK_WEBHOOK" && strings.HasSuffix(r.Field, ".if") {
			return
		}
	}
	t.Error("secrets.SLACK_WEBHOOK in the bare `if:` expression was not found")
}

func TestDynamicReferencesAreMarked(t *testing.T) {
	w := load(t, "dynamic.yml")

	var dynamic, resolved int
	for _, r := range w.References {
		if r.Context != "secrets" {
			continue
		}
		if r.Dynamic {
			dynamic++
			if r.Name() != "" {
				t.Errorf("dynamic reference %q leaked the name %q", r.Raw, r.Name())
			}
		} else {
			resolved++
		}
	}
	if dynamic != 3 {
		t.Errorf("got %d dynamic secret references, want 3", dynamic)
	}
	if resolved != 0 {
		t.Errorf("got %d resolved secret references, want 0", resolved)
	}

	if job := w.Job("deploy"); job == nil || !job.EnvironmentDynamic {
		t.Error("deploy environment is an expression and should be marked dynamic")
	}
}

func TestReusableWorkflowDeclaredSecrets(t *testing.T) {
	w := load(t, "reusable.yml")

	if !w.Reusable {
		t.Error("workflow declares on.workflow_call, Reusable should be true")
	}
	for _, name := range []string{"DEPLOY_KEY", "EXTRA_TOKEN"} {
		if _, ok := w.CallSecrets[name]; !ok {
			t.Errorf("CallSecrets missing %q", name)
		}
	}
	if _, ok := w.CallSecrets["SOME_INHERITED_SECRET"]; ok {
		t.Error("SOME_INHERITED_SECRET is not declared and must not appear in CallSecrets")
	}
}

func TestReusableDetectedFromScalarAndSequence(t *testing.T) {
	tests := map[string]bool{
		"on: workflow_call\njobs: {}\n":         true,
		"on: [push, workflow_call]\njobs: {}\n": true,
		"on:\n  workflow_call:\njobs: {}\n":     true,
		"on: push\njobs: {}\n":                  false,
		"on:\n  push:\n    branches: [main]\n":  false,
	}
	for src, want := range tests {
		w, err := Parse("w.yml", []byte(src))
		if err != nil {
			t.Fatalf("Parse(%q): %v", src, err)
		}
		if w.Reusable != want {
			t.Errorf("Parse(%q).Reusable = %v, want %v", src, w.Reusable, want)
		}
	}
}

func TestHealthyWorkflowParses(t *testing.T) {
	w := load(t, "healthy.yml")
	names := map[string]bool{}
	for _, r := range w.References {
		names[r.Context+"."+r.Name()] = true
	}
	for _, want := range []string{"secrets.CODECOV_TOKEN", "vars.API_BASE", "matrix.node"} {
		if !names[want] {
			t.Errorf("reference %s not found in healthy.yml", want)
		}
	}
}

func TestParseRejectsNonMappingRoot(t *testing.T) {
	if _, err := Parse("w.yml", []byte("- a\n- b\n")); err == nil {
		t.Error("a sequence at the top level should be rejected")
	}
}

func TestParseEmptyFile(t *testing.T) {
	w, err := Parse("w.yml", nil)
	if err != nil {
		t.Fatalf("empty file: %v", err)
	}
	if len(w.References) != 0 {
		t.Errorf("empty file produced %d references", len(w.References))
	}
}

func TestParseInvalidYAML(t *testing.T) {
	if _, err := Parse("w.yml", []byte("a: [1, 2\n")); err == nil {
		t.Error("malformed YAML should return an error")
	}
}

func TestLoadDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ci.yml", "name: CI\non: push\njobs: {}\n")
	write("release.yaml", "name: Release\non: push\njobs: {}\n")
	write("notes.txt", "ignored")
	write("broken.yml", "a: [1, 2\n")

	workflows, bad, err := LoadDir(root)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(workflows) != 2 {
		t.Errorf("got %d workflows, want 2", len(workflows))
	}
	if len(bad) != 1 || bad[0].Path != ".github/workflows/broken.yml" {
		t.Errorf("got load errors %v, want one for broken.yml", bad)
	}
	for _, w := range workflows {
		if !strings.HasPrefix(w.Path, ".github/workflows/") {
			t.Errorf("display path %q should be repository-relative", w.Path)
		}
	}
}

func TestLoadDirMissingIsNotAnError(t *testing.T) {
	workflows, bad, err := LoadDir(t.TempDir())
	if err != nil {
		t.Fatalf("LoadDir on a repo without workflows: %v", err)
	}
	if len(workflows) != 0 || len(bad) != 0 {
		t.Errorf("got %d workflows and %d errors, want none", len(workflows), len(bad))
	}
}

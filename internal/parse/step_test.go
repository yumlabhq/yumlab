package parse

import (
	"strings"
	"testing"
)

func parseOne(t *testing.T, src string) *Workflow {
	t.Helper()
	w, err := Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return w
}

func TestParseSteps(t *testing.T) {
	w := parseOne(t, `
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0
      - name: Install
        run: |
          npm ci
          npm run build
      - uses: ./.github/actions/local
`)

	steps := w.Jobs["build"].Steps
	if len(steps) != 3 {
		t.Fatalf("got %d steps, want 3", len(steps))
	}

	if steps[0].Uses != "actions/checkout@v4" {
		t.Errorf("Uses = %q", steps[0].Uses)
	}
	if !steps[0].HasInput("fetch-depth") || steps[0].Input("fetch-depth") != "0" {
		t.Errorf("with: inputs not captured, got %q", steps[0].Input("fetch-depth"))
	}
	if steps[0].Run != "" {
		t.Errorf("a uses: step has no run script, got %q", steps[0].Run)
	}

	if steps[1].Name != "Install" {
		t.Errorf("Name = %q", steps[1].Name)
	}
	if !strings.Contains(steps[1].Run, "npm ci") {
		t.Errorf("Run should hold the whole script, got %q", steps[1].Run)
	}
	if steps[1].Uses != "" {
		t.Errorf("a run: step has no action, got %q", steps[1].Uses)
	}

	if steps[2].Uses != "./.github/actions/local" {
		t.Errorf("a local action should still be recorded, got %q", steps[2].Uses)
	}

	// Every step must be locatable, or a finding on it cannot be reported.
	for i, s := range steps {
		if s.Loc.File == "" || s.Loc.Line <= 0 {
			t.Errorf("step %d has no position: %+v", i, s.Loc)
		}
	}
}

// Whether the permissions block governs a step depends on which token it was
// handed. This is what stops the control firing on actions using a PAT.
func TestStepUsesDefaultToken(t *testing.T) {
	tests := []struct {
		with string
		want bool
	}{
		{"", true}, // no token input: GITHUB_TOKEN by convention
		{"token: ${{ secrets.GITHUB_TOKEN }}", true},      // the default, explicitly
		{"token: ${{ github.token }}", true},              // the same, written differently
		{"repo-token: ${{ secrets.GITHUB_TOKEN }}", true}, // another conventional name
		{"github-token: ${{ github.token }}", true},       //
		{"token: ${{ secrets.GH_PAT }}", false},           // a personal access token
		{"token: ${{ steps.app.outputs.token }}", false},  // a GitHub App token
		{"repo-token: ${{ secrets.OTHER }}", false},       //
		{"token: hardcoded", false},                       // not the default token
		{"fetch-depth: 0", true},                          // unrelated input
	}

	for _, tc := range tests {
		t.Run(tc.with, func(t *testing.T) {
			src := "on: push\njobs:\n  a:\n    steps:\n      - uses: some/action@v1\n"
			if tc.with != "" {
				src += "        with:\n          " + tc.with + "\n"
			}
			w := parseOne(t, src)
			got := w.Jobs["a"].Steps[0].UsesDefaultToken()
			if got != tc.want {
				t.Errorf("UsesDefaultToken() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParsePermissions(t *testing.T) {
	tests := []struct {
		name     string
		block    string
		declared bool
		dynamic  bool
		// scope -> level expected from Grants
		grants map[string]Access
	}{
		{
			name:     "absent",
			block:    "",
			declared: false,
		},
		{
			name:     "mapping",
			block:    "permissions:\n  contents: read\n  id-token: write\n",
			declared: true,
			grants:   map[string]Access{"contents": AccessRead, "id-token": AccessWrite, "packages": AccessNone},
		},
		{
			name:     "naming one scope sets the rest to none",
			block:    "permissions:\n  contents: read\n",
			declared: true,
			grants:   map[string]Access{"contents": AccessRead, "id-token": AccessNone},
		},
		{
			name:     "write-all names every scope",
			block:    "permissions: write-all\n",
			declared: true,
			grants:   map[string]Access{"contents": AccessWrite, "anything-at-all": AccessWrite},
		},
		{
			name:     "read-all grants no write",
			block:    "permissions: read-all\n",
			declared: true,
			grants:   map[string]Access{"contents": AccessRead},
		},
		{
			name:     "empty mapping grants nothing",
			block:    "permissions: {}\n",
			declared: true,
			grants:   map[string]Access{"contents": AccessNone},
		},
		{
			name:     "explicit none",
			block:    "permissions:\n  contents: none\n",
			declared: true,
			grants:   map[string]Access{"contents": AccessNone},
		},
		{
			name:     "an expression cannot be resolved",
			block:    "permissions:\n  contents: ${{ inputs.level }}\n",
			declared: true,
			dynamic:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := parseOne(t, "on: push\n"+tc.block+"jobs:\n  a:\n    steps:\n      - run: true\n")
			p := w.Permissions

			if p.Declared != tc.declared {
				t.Fatalf("Declared = %v, want %v", p.Declared, tc.declared)
			}
			if p.Dynamic != tc.dynamic {
				t.Errorf("Dynamic = %v, want %v", p.Dynamic, tc.dynamic)
			}
			for scope, want := range tc.grants {
				if got := p.Grants(scope); got != want {
					t.Errorf("Grants(%q) = %q, want %q", scope, got, want)
				}
			}
			if tc.declared && p.Loc.Line <= 0 {
				t.Error("a declared block must be locatable")
			}
		})
	}
}

func TestPermissionsAllows(t *testing.T) {
	w := parseOne(t, "on: push\npermissions:\n  contents: read\n  id-token: write\njobs:\n  a:\n    steps:\n      - run: true\n")
	p := w.Permissions

	cases := []struct {
		scope string
		want  Access
		ok    bool
	}{
		{"contents", AccessRead, true},   // read satisfies read
		{"contents", AccessWrite, false}, // read does not satisfy write
		{"id-token", AccessWrite, true},  // write satisfies write
		{"id-token", AccessRead, true},   // write also satisfies read
		{"packages", AccessRead, false},  // unnamed scope is none
	}
	for _, c := range cases {
		if got := p.Allows(c.scope, c.want); got != c.ok {
			t.Errorf("Allows(%q, %q) = %v, want %v", c.scope, c.want, got, c.ok)
		}
	}
}

// A job's block replaces the workflow's rather than merging with it. This is
// what makes a lost scope provable.
func TestEffectivePermissions(t *testing.T) {
	w := parseOne(t, `
on: push
permissions:
  contents: write
  id-token: write
jobs:
  inherits:
    steps:
      - run: true
  overrides:
    permissions:
      contents: read
    steps:
      - run: true
`)

	inherited := w.EffectivePermissions(w.Jobs["inherits"])
	if inherited.Grants("id-token") != AccessWrite {
		t.Error("a job with no block inherits the workflow's")
	}

	overridden := w.EffectivePermissions(w.Jobs["overrides"])
	if overridden.Grants("contents") != AccessRead {
		t.Errorf("contents = %q, want read", overridden.Grants("contents"))
	}
	if overridden.Grants("id-token") != AccessNone {
		t.Error("a job block replaces the workflow's, so id-token must be lost")
	}
}

func TestPermissionsScopes(t *testing.T) {
	w := parseOne(t, "on: push\npermissions:\n  id-token: write\n  contents: read\njobs:\n  a:\n    steps:\n      - run: true\n")
	got := strings.Join(w.Permissions.Scopes(), ",")
	if got != "contents,id-token" {
		t.Errorf("Scopes() = %q, want them sorted", got)
	}
}

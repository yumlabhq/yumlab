package controls

import (
	"context"
	"strings"
	"testing"

	"github.com/yumlabhq/yumlab/internal/parse"
)

func runPermissions(t *testing.T, in Input) Result {
	t.Helper()
	res, err := (&TokenPermissions{}).Run(context.Background(), in)
	if err != nil {
		t.Fatalf("TokenPermissions.Run: %v", err)
	}
	return res
}

func TestPermissionsNeedsNoNetwork(t *testing.T) {
	c := &TokenPermissions{}
	if c.NeedsNetwork() {
		t.Error("token-permissions must be static: it is what makes the pre-commit hook possible")
	}

	// It must therefore survive an inventory that read nothing at all.
	res := runPermissions(t, Input{
		Workflows: loadWorkflows(t, "permissions.yml"),
		Offline:   true,
	})
	if len(res.Findings) == 0 {
		t.Error("the control should still produce findings with no inventory")
	}
}

func TestPermissionsDetectsMissingScopes(t *testing.T) {
	res := runPermissions(t, Input{Workflows: loadWorkflows(t, "permissions.yml")})

	byJob := map[string]string{}
	for _, f := range res.Findings {
		byJob[f.Detail] = f.Message
	}

	var oidc, release bool
	for _, f := range res.Findings {
		switch {
		case strings.Contains(f.Message, "id-token"):
			oidc = true
			if !strings.Contains(f.Detail, `job "oidc-missing"`) {
				t.Errorf("id-token finding should name the oidc-missing job, got %q", f.Detail)
			}
		case strings.Contains(f.Message, "contents"):
			release = true
			if !strings.Contains(f.Detail, `job "release"`) {
				t.Errorf("contents finding should name the release job, got %q", f.Detail)
			}
		}
		if f.Loc.File == "" || f.Loc.Line <= 0 {
			t.Errorf("finding %q has no file:line", f.Message)
		}
		if f.WastedMinutes <= 0 {
			t.Errorf("finding %q has no estimate", f.Message)
		}
	}
	if !oidc {
		t.Error("missing id-token: write on an OIDC job was not detected")
	}
	if !release {
		t.Error("missing contents: write on a release job was not detected")
	}
	if len(res.Findings) != 2 {
		t.Errorf("got %d findings, want 2: %v", len(res.Findings), messages(res))
	}
}

// The cases that must stay silent. Each one would be a false positive.
func TestPermissionsStaysSilentWhenGranted(t *testing.T) {
	res := runPermissions(t, Input{Workflows: loadWorkflows(t, "permissions.yml")})

	for _, f := range res.Findings {
		for _, job := range []string{"oidc-granted", "static-credentials", "release-write-all", "unknown-action"} {
			if strings.Contains(f.Detail, `job "`+job+`"`) {
				t.Errorf("false positive on job %q: %s", job, f.Message)
			}
		}
	}
}

// The same action needs id-token only when configured for OIDC. Flagging it
// when it uses static keys would be a false positive.
func TestPermissionsIgnoresActionWithoutOIDCInputs(t *testing.T) {
	src := `
name: W
on: push
permissions:
  contents: read
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          aws-region: eu-west-3
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	res := runPermissions(t, Input{Workflows: []*parse.Workflow{w}})
	if len(res.Findings) != 0 {
		t.Errorf("no OIDC input means no id-token requirement, got %v", messages(res))
	}
}

// An action handed a token other than GITHUB_TOKEN is not governed by the
// permissions block. Found on nodejs/node, goreleaser and supabase while
// running the corpus: all three would have been false positives.
func TestPermissionsIgnoresStepsWithACustomToken(t *testing.T) {
	tokens := map[string]bool{
		"${{ secrets.GH_PAT }}":                false, // a PAT: not governed
		"${{ steps.app-token.outputs.token }}": false, // a GitHub App token
		"${{ secrets.GITHUB_TOKEN }}":          true,  // the default token
		"${{ github.token }}":                  true,  // the same, written differently
	}

	for token, wantFinding := range tokens {
		t.Run(token, func(t *testing.T) {
			src := `
name: W
on: push
permissions:
  contents: read
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - uses: peter-evans/create-pull-request@v6
        with:
          token: ` + token + `
`
			w, err := parse.Parse("w.yml", []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			res := runPermissions(t, Input{Workflows: []*parse.Workflow{w}})
			got := len(res.Findings) > 0
			if got != wantFinding {
				t.Errorf("token %s: finding = %v, want %v (%v)", token, got, wantFinding, messages(res))
			}
		})
	}
}

// Passing no token at all means the action gets GITHUB_TOKEN by convention, so
// the block does govern it.
func TestPermissionsAppliesWhenNoTokenInputIsGiven(t *testing.T) {
	src := `
name: W
on: push
permissions:
  contents: read
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - uses: peter-evans/create-pull-request@v6
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	res := runPermissions(t, Input{Workflows: []*parse.Workflow{w}})
	if len(res.Findings) == 0 {
		t.Error("with no token input the default applies, so the block governs the step")
	}
}

// No block at all: the grant comes from a repository setting we cannot see.
func TestPermissionsUndeclaredBlockIsAGap(t *testing.T) {
	res := runPermissions(t, Input{Workflows: loadWorkflows(t, "permissions-undeclared.yml")})

	if len(res.Findings) != 0 {
		t.Errorf("an undeclared block must never produce a finding, got %v", messages(res))
	}
	if res.Coverage.Unverified() != 1 {
		t.Errorf("Unverified = %d, want 1", res.Coverage.Unverified())
	}
	if len(res.Coverage.Gaps) != 1 || !strings.Contains(res.Coverage.Gaps[0].Reason, "repository default") {
		t.Errorf("the gap should explain that the default is unreadable, got %+v", res.Coverage.Gaps)
	}
}

// A job block replaces the workflow block entirely; it does not merge with it.
func TestPermissionsJobBlockReplacesWorkflowBlock(t *testing.T) {
	src := `
name: W
on: push
permissions:
  id-token: write
  contents: read
jobs:
  a:
    runs-on: ubuntu-latest
    # This block replaces the workflow's, so id-token is lost.
    permissions:
      contents: read
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::1:role/r
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	res := runPermissions(t, Input{Workflows: []*parse.Workflow{w}})

	if len(res.Findings) != 1 {
		t.Fatalf("a job block must replace, not merge: got %d findings %v", len(res.Findings), messages(res))
	}
	if !strings.Contains(res.Findings[0].Message, "id-token") {
		t.Errorf("Message = %q, want the lost id-token scope", res.Findings[0].Message)
	}
}

// An expression in the block cannot be resolved, so nothing may be concluded.
func TestPermissionsDynamicBlockIsAGap(t *testing.T) {
	src := `
name: W
on: push
jobs:
  a:
    runs-on: ubuntu-latest
    permissions:
      id-token: ${{ inputs.level }}
    steps:
      - uses: aws-actions/configure-aws-credentials@v4
        with:
          role-to-assume: arn:aws:iam::1:role/r
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	res := runPermissions(t, Input{Workflows: []*parse.Workflow{w}})

	if len(res.Findings) != 0 {
		t.Errorf("an unresolvable block must not produce findings, got %v", messages(res))
	}
	if res.Coverage.Unverified() != 1 {
		t.Errorf("Unverified = %d, want 1", res.Coverage.Unverified())
	}
}

func TestPermissionsShorthands(t *testing.T) {
	tests := []struct {
		name        string
		block       string
		wantFinding bool
	}{
		{"write-all grants everything", "permissions: write-all", false},
		{"read-all grants no write", "permissions: read-all", true},
		{"empty mapping grants nothing", "permissions: {}", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			src := "name: W\non: push\n" + tc.block + `
jobs:
  a:
    runs-on: ubuntu-latest
    steps:
      - uses: softprops/action-gh-release@v2
`
			w, err := parse.Parse("w.yml", []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			res := runPermissions(t, Input{Workflows: []*parse.Workflow{w}})
			got := len(res.Findings) > 0
			if got != tc.wantFinding {
				t.Errorf("%s: finding = %v, want %v (%v)", tc.block, got, tc.wantFinding, messages(res))
			}
		})
	}
}

func TestPermissionsControlIsRegistered(t *testing.T) {
	var found bool
	for _, c := range All() {
		if c.ID() == TokenPermissionsID {
			found = true
		}
	}
	if !found {
		t.Error("token-permissions is not registered in All()")
	}
}

func TestActionName(t *testing.T) {
	tests := map[string]string{
		"actions/checkout@v4":     "actions/checkout",
		"actions/checkout@abc123": "actions/checkout",
		"actions/checkout":        "actions/checkout",
		"./.github/actions/local": "",
		"docker://alpine:3":       "",
		"":                        "",
		"org/repo/sub/action@v1":  "org/repo/sub/action",
	}
	for in, want := range tests {
		if got := actionName(in); got != want {
			t.Errorf("actionName(%q) = %q, want %q", in, got, want)
		}
	}
}

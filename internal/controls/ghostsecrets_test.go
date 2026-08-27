package controls

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yumlab/yumlab/internal/config"
	"github.com/yumlab/yumlab/internal/github"
	"github.com/yumlab/yumlab/internal/parse"
)

func loadWorkflows(t *testing.T, names ...string) []*parse.Workflow {
	t.Helper()
	var out []*parse.Workflow
	for _, name := range names {
		path := filepath.Join("..", "..", "testdata", "workflows", name)
		w, err := parse.LoadFile(path, "testdata/workflows/"+name)
		if err != nil {
			t.Fatalf("LoadFile(%s): %v", name, err)
		}
		out = append(out, w)
	}
	return out
}

// fullAccess builds an inventory where every scope was read successfully.
func fullAccess(repoSecrets, orgSecrets, repoVars []string, envs map[string][]string) *github.Inventory {
	inv := github.NewInventory("acme", "app", "test")
	inv.RepoSecrets = github.NewNameSet(repoSecrets)
	inv.OrgSecrets = github.NewNameSet(orgSecrets)
	inv.RepoVariables = github.NewNameSet(repoVars)
	inv.OrgVariables = github.NewNameSet(nil)

	names := make([]string, 0, len(envs))
	for name, secrets := range envs {
		names = append(names, name)
		inv.EnvSecrets[name] = github.NewNameSet(secrets)
		inv.EnvVariables[name] = github.NewNameSet(nil)
	}
	inv.Environments = github.NewNameSet(names)
	return inv
}

func run(t *testing.T, in Input) Result {
	t.Helper()
	res, err := (&GhostSecrets{}).Run(context.Background(), in)
	if err != nil {
		t.Fatalf("GhostSecrets.Run: %v", err)
	}
	return res
}

func messages(res Result) []string {
	out := make([]string, 0, len(res.Findings))
	for _, f := range res.Findings {
		out = append(out, f.Message)
	}
	return out
}

func hasMessage(res Result, want string) bool {
	for _, m := range messages(res) {
		if m == want {
			return true
		}
	}
	return false
}

func TestDetectsMissingSecretsAndVariables(t *testing.T) {
	in := Input{
		Workflows: loadWorkflows(t, "broken-secrets.yml"),
		Inventory: fullAccess(
			[]string{"NPM_TOKEN"},  // repository secrets
			[]string{"SENTRY_DSN"}, // organization secrets
			nil,                    // repository variables
			map[string][]string{"production": {"AWS_DEPLOY_ROLE"}},
		),
	}
	res := run(t, in)

	// Missing everywhere.
	for _, want := range []string{
		"secrets.RELEASE_WEBHOOK is not defined",
		"secrets.SLACK_WEBHOOK is not defined",
		"vars.AWS_REGION is not defined",
	} {
		if !hasMessage(res, want) {
			t.Errorf("expected finding %q, got %v", want, messages(res))
		}
	}

	// Defined at some level, so not findings.
	for _, unwanted := range []string{
		"secrets.NPM_TOKEN is not defined",       // repository
		"secrets.SENTRY_DSN is not defined",      // organization
		"secrets.AWS_DEPLOY_ROLE is not defined", // production environment
		"secrets.GITHUB_TOKEN is not defined",    // provided by the runner
	} {
		if hasMessage(res, unwanted) {
			t.Errorf("false positive: %q", unwanted)
		}
	}

	for _, f := range res.Findings {
		if f.WastedMinutes <= 0 {
			t.Errorf("finding %q has no wasted-minutes estimate", f.Message)
		}
		if f.Loc.Line <= 0 || f.Loc.File == "" {
			t.Errorf("finding %q has no file:line", f.Message)
		}
	}
}

// An environment secret must not satisfy a job that does not declare that
// environment: the value simply is not there at runtime.
func TestEnvironmentSecretDoesNotLeakToOtherJobs(t *testing.T) {
	src := `
name: W
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: ./x.sh
        env:
          KEY: ${{ secrets.PROD_ONLY }}
  deploy:
    runs-on: ubuntu-latest
    environment: production
    steps:
      - run: ./y.sh
        env:
          KEY: ${{ secrets.PROD_ONLY }}
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}

	res := run(t, Input{
		Workflows: []*parse.Workflow{w},
		Inventory: fullAccess(nil, nil, nil, map[string][]string{"production": {"PROD_ONLY"}}),
	})

	if len(res.Findings) != 1 {
		t.Fatalf("got %d findings, want 1: %v", len(res.Findings), messages(res))
	}
	f := res.Findings[0]
	if f.Message != "secrets.PROD_ONLY is not defined" {
		t.Errorf("Message = %q", f.Message)
	}
	if !strings.Contains(f.Detail, `job "build"`) {
		t.Errorf("finding should point at the build job, got detail %q", f.Detail)
	}
}

// The central rule: an unreadable scope produces gaps, never findings.
//
// The blast radius of an unreadable scope is exactly the references that depend
// on it. Repository and organization scopes are consulted by every reference,
// so losing either silences all secret findings. A deployment environment is
// only consulted by the jobs that declare it, so losing it silences just those.
func TestUnreadableScopeProducesGapsNotFindings(t *testing.T) {
	tests := []struct {
		name string
		br   func(*github.Inventory)
		// silenced lists the secret names that must not be reported. nil means
		// no secret finding at all is allowed.
		silenced []string
	}{
		{
			name: "repository secrets",
			br: func(inv *github.Inventory) {
				inv.RepoSecrets = github.Unavailable(github.AccessDenied, "no Secrets permission")
			},
		},
		{
			name: "organization secrets",
			br: func(inv *github.Inventory) {
				inv.OrgSecrets = github.Unavailable(github.AccessDenied, "no admin:org scope")
			},
		},
		{
			name: "production environment secrets",
			br: func(inv *github.Inventory) {
				inv.EnvSecrets["production"] = github.Unavailable(github.AccessDenied, "no Environments permission")
			},
			// Only the deploy job declares environment: production.
			silenced: []string{"AWS_DEPLOY_ROLE", "SENTRY_DSN"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inv := fullAccess(nil, nil, nil, map[string][]string{"production": nil})
			tc.br(inv)

			res := run(t, Input{
				Workflows: loadWorkflows(t, "broken-secrets.yml"),
				Inventory: inv,
			})

			for _, f := range res.Findings {
				name, isSecret := strings.CutPrefix(f.Message, "secrets.")
				if !isSecret {
					continue
				}
				name = strings.TrimSuffix(name, " is not defined")
				if tc.silenced == nil {
					t.Errorf("with %s unreadable, no secret finding is allowed, got %q", tc.name, f.Message)
					continue
				}
				for _, s := range tc.silenced {
					if name == s {
						t.Errorf("with %s unreadable, %q must not be reported", tc.name, f.Message)
					}
				}
			}
			if res.Coverage.Unverified() == 0 {
				t.Errorf("with %s unreadable, references should be reported as unverified", tc.name)
			}
		})
	}
}

// Offline mode reads nothing, so it must conclude nothing.
func TestOfflineInventoryProducesNoFindings(t *testing.T) {
	res := run(t, Input{
		Workflows: loadWorkflows(t, "broken-secrets.yml", "healthy.yml"),
		Inventory: github.NewInventory("acme", "app", "offline mode"),
		Offline:   true,
	})

	if len(res.Findings) != 0 {
		t.Errorf("offline mode produced findings: %v", messages(res))
	}
	if res.Coverage.Unverified() == 0 {
		t.Error("offline mode should report every reference as unverified")
	}
}

func TestDynamicReferencesBecomeGaps(t *testing.T) {
	res := run(t, Input{
		Workflows: loadWorkflows(t, "dynamic.yml"),
		Inventory: fullAccess(nil, nil, nil, nil),
	})

	if len(res.Findings) != 0 {
		t.Errorf("computed secret names must never be findings, got %v", messages(res))
	}
	if res.Coverage.Unverified() != 3 {
		t.Errorf("got %d unverified references, want 3", res.Coverage.Unverified())
	}
}

func TestReusableWorkflowSecrets(t *testing.T) {
	res := run(t, Input{
		Workflows: loadWorkflows(t, "reusable.yml"),
		Inventory: fullAccess(nil, nil, nil, nil),
	})

	if len(res.Findings) != 0 {
		t.Errorf("a reusable workflow's secrets come from the caller, got %v", messages(res))
	}
	// DEPLOY_KEY is declared as a parameter and therefore verified;
	// SOME_INHERITED_SECRET is not, so it is unverified.
	if res.Coverage.Checked != 1 {
		t.Errorf("Checked = %d, want 1 (the declared DEPLOY_KEY)", res.Coverage.Checked)
	}
	if res.Coverage.Unverified() != 1 {
		t.Errorf("Unverified = %d, want 1 (the undeclared secret)", res.Coverage.Unverified())
	}
}

// A job whose environment is an expression cannot have its secrets resolved.
func TestDynamicEnvironmentIsAGap(t *testing.T) {
	src := `
name: W
on: push
jobs:
  deploy:
    runs-on: ubuntu-latest
    environment: ${{ github.ref_name }}
    steps:
      - run: ./y.sh
        env:
          KEY: ${{ secrets.DEPLOY_KEY }}
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, Input{
		Workflows: []*parse.Workflow{w},
		Inventory: fullAccess(nil, nil, nil, nil),
	})

	if len(res.Findings) != 0 {
		t.Errorf("an unresolvable environment must not yield findings, got %v", messages(res))
	}
	if res.Coverage.Unverified() != 1 {
		t.Errorf("Unverified = %d, want 1", res.Coverage.Unverified())
	}
}

// The declarative fallback: names listed in .yumlab.yaml count as existing,
// which is how a user without admin rights unblocks the control.
func TestDeclaredNamesSuppressFindings(t *testing.T) {
	workflows := loadWorkflows(t, "broken-secrets.yml")
	inv := fullAccess(nil, nil, nil, map[string][]string{"production": nil})

	before := run(t, Input{Workflows: workflows, Inventory: inv})
	if !hasMessage(before, "secrets.SLACK_WEBHOOK is not defined") {
		t.Fatalf("setup: expected SLACK_WEBHOOK to be missing, got %v", messages(before))
	}

	cfg := config.Config{Path: ".yumlab.yaml"}
	cfg.Secrets.Organization = []string{"SLACK_WEBHOOK"}
	cfg.Variables.Organization = []string{"AWS_REGION"}

	after := run(t, Input{Workflows: workflows, Inventory: inv, Config: cfg})
	if hasMessage(after, "secrets.SLACK_WEBHOOK is not defined") {
		t.Error("a secret declared in .yumlab.yaml must not be reported as missing")
	}
	if hasMessage(after, "vars.AWS_REGION is not defined") {
		t.Error("a variable declared in .yumlab.yaml must not be reported as missing")
	}
}

// Declaring names must not turn an unreadable scope into a readable one: it can
// only prove that a name exists, never that one is absent.
func TestDeclaredNamesDoNotMakeUnreadableScopesReadable(t *testing.T) {
	inv := fullAccess(nil, nil, nil, nil)
	inv.OrgSecrets = github.Unavailable(github.AccessDenied, "no permission")

	cfg := config.Config{Path: ".yumlab.yaml"}
	cfg.Secrets.Organization = []string{"SOMETHING_ELSE"}

	res := run(t, Input{
		Workflows: loadWorkflows(t, "broken-secrets.yml"),
		Inventory: inv,
		Config:    cfg,
	})
	for _, f := range res.Findings {
		if strings.HasPrefix(f.Message, "secrets.") {
			t.Errorf("organization scope is unreadable, no secret finding allowed, got %q", f.Message)
		}
	}
}

// An environment that provably does not exist holds no secrets, which is a
// conclusive answer rather than a gap.
func TestMissingEnvironmentIsConclusive(t *testing.T) {
	inv := fullAccess(nil, nil, nil, nil) // production is not configured
	inv.EnvSecrets["production"] = github.Unavailable(github.AccessMissing, "environment does not exist")
	inv.EnvVariables["production"] = github.Unavailable(github.AccessMissing, "environment does not exist")

	res := run(t, Input{
		Workflows: loadWorkflows(t, "broken-secrets.yml"),
		Inventory: inv,
	})
	if !hasMessage(res, "secrets.AWS_DEPLOY_ROLE is not defined") {
		t.Errorf("expected AWS_DEPLOY_ROLE to be reported, got %v", messages(res))
	}
}

// The same missing name in the same scope is one problem, not several.
func TestOccurrencesAreGroupedIntoOneFinding(t *testing.T) {
	src := `
name: W
on: push
jobs:
  build:
    runs-on: ubuntu-latest
    steps:
      - run: echo ${{ secrets.MISSING }}
      - run: echo ${{ secrets.MISSING }}
      - run: echo ${{ secrets.MISSING }}
`
	w, err := parse.Parse("w.yml", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	res := run(t, Input{
		Workflows: []*parse.Workflow{w},
		Inventory: fullAccess(nil, nil, nil, nil),
	})

	if len(res.Findings) != 1 {
		t.Fatalf("got %d findings, want 1", len(res.Findings))
	}
	f := res.Findings[0]
	if f.Count() != 3 {
		t.Errorf("Count = %d, want 3", f.Count())
	}
	if len(f.Others) != 2 {
		t.Errorf("Others = %d, want 2", len(f.Others))
	}
}

func TestHealthyWorkflowIsClean(t *testing.T) {
	res := run(t, Input{
		Workflows: loadWorkflows(t, "healthy.yml"),
		Inventory: fullAccess(
			[]string{"CODECOV_TOKEN"},
			nil,
			[]string{"API_BASE"},
			nil,
		),
	})
	if len(res.Findings) != 0 {
		t.Errorf("healthy workflow produced findings: %v", messages(res))
	}
	if res.Coverage.Unverified() != 0 {
		t.Errorf("healthy workflow produced %d unverified references", res.Coverage.Unverified())
	}
}

func TestSelectedDropsNetworkControlsWhenOffline(t *testing.T) {
	enabled, skipped := Selected(config.Config{}, true)
	for _, c := range enabled {
		if c.NeedsNetwork() {
			t.Errorf("control %q needs the network and must not run offline", c.ID())
		}
	}
	var found bool
	for _, c := range skipped {
		if c.ID() == GhostSecretsID {
			found = true
		}
	}
	if !found {
		t.Error("ghost-secrets needs the network and should be reported as skipped offline")
	}
}

func TestSelectedRespectsDisabledControls(t *testing.T) {
	cfg := config.Config{Controls: map[string]bool{GhostSecretsID: false}}
	enabled, skipped := Selected(cfg, false)
	if len(enabled) != 0 || len(skipped) != 0 {
		t.Errorf("a disabled control should neither run nor be reported as skipped")
	}
}

func TestSortFindingsByWastedMinutes(t *testing.T) {
	f := []Finding{
		{Message: "small", WastedMinutes: 5},
		{Message: "big", WastedMinutes: 40},
		{Message: "medium", WastedMinutes: 16},
	}
	SortFindings(f)
	want := []string{"big", "medium", "small"}
	for i, w := range want {
		if f[i].Message != w {
			t.Errorf("position %d = %q, want %q", i, f[i].Message, w)
		}
	}
}

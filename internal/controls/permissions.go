package controls

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yumlabhq/yumlab/internal/parse"
	"github.com/yumlabhq/yumlab/internal/score"
)

// TokenPermissionsID is the stable identifier of this control.
const TokenPermissionsID = "token-permissions"

// TokenPermissions reports jobs that use an action needing a permission their
// `permissions:` block does not grant.
//
// This is a static control: it needs no token and runs offline, which is what
// makes it usable in a pre-commit hook.
//
// It only reports the "not enough" direction, and only when the answer is
// provable. Two GitHub rules make that possible: a job's block replaces the
// workflow's rather than merging with it, and naming any scope sets every
// unnamed scope to none. So a declared block that omits a required scope will
// certainly fail at run time.
//
// When no block is declared at all, the effective permissions come from a
// repository or organization setting that only the API can reveal. That is
// reported as a gap, never as a finding. Guessing there would produce exactly
// the false positive the product cannot afford.
//
// Inferring what a job does from its `run:` scripts is deliberately out of
// scope. Only a curated table of actions whose requirements are certain is used.
type TokenPermissions struct{}

func (c *TokenPermissions) ID() string { return TokenPermissionsID }
func (c *TokenPermissions) NeedsNetwork() bool {
	return false
}

func (c *TokenPermissions) Title() string {
	return "Jobs missing a token permission the actions they run require"
}

// requirement is a permission an action needs to work at all.
type requirement struct {
	scope string
	level parse.Access
	// why explains the consequence in one clause, used in the finding.
	why string
}

// actionRequirements maps an action to the permissions it cannot work without.
//
// Deliberately small. Every entry must be certain: an action that only
// sometimes needs a scope belongs in the conditional table below, or nowhere.
// A wrong entry here is a false positive, which costs more than ten missed
// problems.
var actionRequirements = map[string][]requirement{
	"softprops/action-gh-release": {
		{"contents", parse.AccessWrite, "creating a release writes to the repository"},
	},
	"ncipollo/release-action": {
		{"contents", parse.AccessWrite, "creating a release writes to the repository"},
	},
	"peter-evans/create-pull-request": {
		{"contents", parse.AccessWrite, "it pushes a branch"},
		{"pull-requests", parse.AccessWrite, "it opens a pull request"},
	},
}

// actions/stale is deliberately absent. Whether it needs issues: write or
// pull-requests: write depends on days-before-issue-stale and
// days-before-pr-stale, which are routinely set to -1 to disable one side.
// Running the corpus found real repositories doing exactly that, so a blanket
// entry here would be a false positive. It belongs in the table only once those
// inputs are read.

// oidcActions need id-token: write, but only when configured for OIDC. Each
// entry names the input whose presence means OIDC is in use.
//
// This is the highest-value case in the control: without id-token: write the
// runner cannot mint the token, the login step fails, and the failure message
// points at the cloud provider rather than at the missing permission.
var oidcActions = map[string][]string{
	"aws-actions/configure-aws-credentials": {"role-to-assume"},
	"google-github-actions/auth":            {"workload_identity_provider"},
	"azure/login":                           {"client-id"},
	"hashicorp/vault-action":                {"method"},
}

const oidcScope = "id-token"

func (c *TokenPermissions) Run(ctx context.Context, in Input) (Result, error) {
	var (
		res     Result
		gaps    = newGapSet()
		checked int
	)

	for _, w := range in.Workflows {
		for _, id := range w.JobOrder {
			job := w.Jobs[id]
			needs := c.requirements(job)
			if len(needs) == 0 {
				continue
			}

			perms := w.EffectivePermissions(job)

			switch {
			case !perms.Declared:
				// The grant comes from a repository setting we cannot read
				// offline. Say so instead of assuming either way.
				for _, n := range needs {
					gaps.addAt(
						"no permissions block, so the grant comes from the repository default, which only the API can reveal",
						fmt.Sprintf("%s needs %s: %s", n.action, n.scope, n.level),
						n.loc)
				}

			case perms.Dynamic:
				for _, n := range needs {
					gaps.addAt(
						"the permissions block is an expression, so the grant cannot be resolved",
						fmt.Sprintf("%s needs %s: %s", n.action, n.scope, n.level),
						n.loc)
				}

			default:
				for _, n := range needs {
					checked++
					if perms.Allows(n.scope, n.level) {
						continue
					}
					res.Findings = append(res.Findings, c.finding(job, perms, n))
				}
			}
		}
	}

	res.Coverage = Coverage{Checked: checked, Gaps: gaps.list()}
	SortFindings(res.Findings)
	return res, nil
}

// need is one required permission, tied to the step that requires it.
type need struct {
	requirement
	action string
	loc    parse.Location
}

// requirements collects what a job's steps require. It walks the parsed YAML
// rather than the reference list, because what matters here is `uses:` and
// `with:`, not expressions.
func (c *TokenPermissions) requirements(job *parse.Job) []need {
	var out []need
	seen := map[string]bool{}

	for _, step := range job.Steps {
		name := actionName(step.Uses)
		if name == "" {
			continue
		}

		// An action handed a personal access token or a GitHub App token does not
		// act as GITHUB_TOKEN, so the permissions block says nothing about what
		// it may do. Concluding anything there is a false positive — the corpus
		// found several real repositories doing this.
		//
		// This exemption applies only to actions that call the GitHub API with a
		// token. OIDC below is unaffected: id-token: write is what lets the
		// runner mint the token in the first place, whatever the action is
		// later handed.
		if step.UsesDefaultToken() {
			for _, r := range actionRequirements[name] {
				key := name + "/" + r.scope
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, need{requirement: r, action: name, loc: step.Loc})
			}
		}

		// OIDC is only required when the action is configured for it.
		for _, input := range oidcActions[name] {
			if !step.HasInput(input) {
				continue
			}
			key := name + "/" + oidcScope
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, need{
				requirement: requirement{
					scope: oidcScope,
					level: parse.AccessWrite,
					why:   "it authenticates with OIDC, which needs a token the runner cannot mint without this",
				},
				action: name,
				loc:    step.Loc,
			})
		}
	}

	sort.SliceStable(out, func(i, j int) bool { return out[i].scope < out[j].scope })
	return out
}

func (c *TokenPermissions) finding(job *parse.Job, perms parse.Permissions, n need) Finding {
	granted := "nothing"
	if scopes := perms.Scopes(); len(scopes) > 0 {
		parts := make([]string, 0, len(scopes))
		for _, s := range scopes {
			parts = append(parts, fmt.Sprintf("%s: %s", s, perms.Grants(s)))
		}
		granted = strings.Join(parts, ", ")
	}

	where := fmt.Sprintf("job %q", job.ID)
	level := "workflow"
	if job.Permissions.Declared {
		level = "job"
	}

	return Finding{
		ControlID: TokenPermissionsID,
		Severity:  SeverityError,
		Loc:       n.loc,
		Message:   fmt.Sprintf("%s needs %s: %s, which is not granted", n.action, n.scope, n.level),
		Detail: fmt.Sprintf(
			"In %s, because %s. The %s permissions block at %s grants %s, and naming any scope sets every "+
				"other scope to none. Add %s: %s to it.",
			where, n.why, level, perms.Loc.Short(), granted, n.scope, n.level),
		WastedMinutes: score.MissingPermissionMinutes,
	}
}

// actionName strips the version from a `uses:` value, and ignores anything that
// is not a published action: local actions and docker:// references have no
// entry in the table anyway.
func actionName(uses string) string {
	if uses == "" || strings.HasPrefix(uses, ".") || strings.HasPrefix(uses, "docker://") {
		return ""
	}
	name, _, _ := strings.Cut(uses, "@")
	return name
}

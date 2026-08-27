package controls

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/yumlabhq/yumlab/internal/expr"
	"github.com/yumlabhq/yumlab/internal/github"
	"github.com/yumlabhq/yumlab/internal/parse"
	"github.com/yumlabhq/yumlab/internal/score"
)

// GhostSecretsID is the stable identifier of this control.
const GhostSecretsID = "ghost-secrets"

// builtinSecrets are provided by the runner. They are never declared by the
// user, so referencing them is always valid.
var builtinSecrets = map[string]bool{
	"GITHUB_TOKEN":                   true,
	"ACTIONS_RUNTIME_TOKEN":          true,
	"ACTIONS_RUNTIME_URL":            true,
	"ACTIONS_ID_TOKEN_REQUEST_TOKEN": true,
	"ACTIONS_ID_TOKEN_REQUEST_URL":   true,
	"ACTIONS_CACHE_URL":              true,
	"ACTIONS_RESULTS_URL":            true,
}

// GhostSecrets reports secrets.X and vars.X references that resolve to nothing.
//
// A reference resolves against the union of the scopes the job can read: the
// repository, the organization entries shared with it, and the deployment
// environment of the job when it has one. A finding is only produced when every
// one of those scopes was read successfully and none of them contains the name.
// If any scope could not be read, the reference becomes a gap.
type GhostSecrets struct{}

func (c *GhostSecrets) ID() string         { return GhostSecretsID }
func (c *GhostSecrets) Title() string      { return "Secrets and variables referenced but never defined" }
func (c *GhostSecrets) NeedsNetwork() bool { return true }

// kind distinguishes the two namespaces this control checks.
type kind struct {
	context string // "secrets" or "vars"
	label   string // "secret" or "variable"
}

var (
	secretKind   = kind{context: expr.CtxSecrets, label: "secret"}
	variableKind = kind{context: expr.CtxVars, label: "variable"}
)

// target groups every reference that resolves the same way. Two references to
// the same name from jobs with the same environment share a single finding,
// because fixing one fixes both.
type target struct {
	kind kind
	name string
	envs string // sorted environments of the job, "" for none
}

type occurrence struct {
	ref  parse.Reference
	job  *parse.Job
	envs []string
}

func (c *GhostSecrets) Run(ctx context.Context, in Input) (Result, error) {
	var (
		res     Result
		groups  = map[target][]occurrence{}
		order   []target
		gaps    = newGapSet()
		checked int
	)

	for _, w := range in.Workflows {
		for _, ref := range w.References {
			k, ok := kindOf(ref)
			if !ok {
				continue
			}

			// A bare `secrets` reference, as in toJSON(secrets), names nothing
			// to check.
			if !ref.Dynamic && ref.Name() == "" {
				continue
			}

			// A computed name cannot be resolved without running the workflow.
			if ref.Dynamic {
				gaps.add(fmt.Sprintf("the %s name is computed at runtime", k.label), ref)
				continue
			}

			if k == secretKind && builtinSecrets[ref.Name()] {
				checked++
				continue
			}

			// In a reusable workflow the caller supplies the secrets, so this
			// repository is not the authority on them.
			if w.Reusable && k == secretKind {
				if _, declared := w.CallSecrets[ref.Name()]; declared {
					checked++
					continue
				}
				gaps.add("reusable workflow: secrets may be supplied by the calling workflow", ref)
				continue
			}

			job := w.Job(ref.JobID)
			if job != nil && job.EnvironmentDynamic {
				gaps.add("the job's environment is an expression, so its secrets cannot be resolved", ref)
				continue
			}

			envs := environmentsOf(job)
			t := target{kind: k, name: ref.Name(), envs: strings.Join(envs, ",")}
			if _, seen := groups[t]; !seen {
				order = append(order, t)
			}
			groups[t] = append(groups[t], occurrence{ref: ref, job: job, envs: envs})
		}
	}

	for _, t := range order {
		occs := groups[t]
		set, scopes := c.resolve(in, t, occs[0].envs)

		switch {
		case set.Has(t.name):
			checked += len(occs)

		case !set.Access.Readable():
			reason := set.Reason
			if reason == "" {
				reason = "the scope could not be read"
			}
			for _, o := range occs {
				gaps.add(reason, o.ref)
			}

		default:
			checked += len(occs)
			res.Findings = append(res.Findings, c.finding(t, occs, scopes))
		}
	}

	res.Coverage = Coverage{Checked: checked, Gaps: gaps.list()}
	SortFindings(res.Findings)
	return res, nil
}

// resolve builds the set of names visible to a job and the human-readable list
// of the scopes that were consulted.
func (c *GhostSecrets) resolve(in Input, t target, envs []string) (github.NameSet, []string) {
	var (
		repo, org github.NameSet
		declared  []string
		scopes    []string
	)

	// Copy rather than append onto the config's own slices, which must stay
	// untouched across calls.
	if t.kind == secretKind {
		repo, org = in.Inventory.RepoSecrets, in.Inventory.OrgSecrets
		declared = append(declared, in.Config.Secrets.Repository...)
		declared = append(declared, in.Config.Secrets.Organization...)
	} else {
		repo, org = in.Inventory.RepoVariables, in.Inventory.OrgVariables
		declared = append(declared, in.Config.Variables.Repository...)
		declared = append(declared, in.Config.Variables.Organization...)
	}

	set := repo.Merge(org)
	scopes = append(scopes, "repository", "organization")

	for _, env := range envs {
		var envSet github.NameSet
		if t.kind == secretKind {
			envSet = in.Inventory.EnvironmentSecrets(env)
		} else {
			envSet = in.Inventory.EnvironmentVariables(env)
		}
		// An environment that provably does not exist holds nothing, which is a
		// conclusive answer rather than a gap.
		if envSet.Access == github.AccessMissing {
			envSet = github.NewNameSet(nil)
		}
		set = set.Merge(envSet)
		scopes = append(scopes, "environment "+env)

		if t.kind == secretKind {
			declared = append(declared, in.Config.Secrets.Environments[env]...)
		} else {
			declared = append(declared, in.Config.Variables.Environments[env]...)
		}
	}

	// Names declared in .yumlab.yaml count as existing. They do not make an
	// unreadable scope readable: they can only prove presence, never absence.
	if len(declared) > 0 {
		set = set.Merge(github.NewNameSet(declared))
		scopes = append(scopes, "declared in "+configLabel(in))
	}

	return set, scopes
}

func (c *GhostSecrets) finding(t target, occs []occurrence, scopes []string) Finding {
	primary := occs[0]

	var others []parse.Location
	for _, o := range occs[1:] {
		others = append(others, o.ref.Loc)
	}

	where := "no job"
	if primary.job != nil {
		where = fmt.Sprintf("job %q", primary.job.ID)
	}
	if len(primary.envs) > 0 {
		where += fmt.Sprintf(" (environment %s)", strings.Join(primary.envs, ", "))
	}

	detail := fmt.Sprintf("Referenced from %s. Looked in: %s.", where, strings.Join(scopes, ", "))

	// The most common cause by far: the value exists in an environment, but the
	// job never declares one, so it cannot see it.
	if t.kind == secretKind && len(primary.envs) == 0 {
		detail += " If it lives in a deployment environment, the job must declare that environment."
	}

	return Finding{
		ControlID:     GhostSecretsID,
		Severity:      SeverityError,
		Loc:           primary.ref.Loc,
		Others:        others,
		Message:       fmt.Sprintf("%s.%s is not defined", t.kind.context, t.name),
		Detail:        detail,
		WastedMinutes: score.GhostSecretMinutes,
	}
}

func kindOf(ref parse.Reference) (kind, bool) {
	switch ref.Context {
	case expr.CtxSecrets:
		return secretKind, true
	case expr.CtxVars:
		return variableKind, true
	}
	return kind{}, false
}

func environmentsOf(job *parse.Job) []string {
	if job == nil {
		return nil
	}
	names := make([]string, 0, len(job.Environments))
	for _, e := range job.Environments {
		names = append(names, e.Name)
	}
	sort.Strings(names)
	return names
}

func configLabel(in Input) string {
	if in.Config.Path != "" {
		return ".yumlab.yaml"
	}
	return "configuration"
}

// gapSet groups unverified references by reason so the report can say
// "3 references unverified (organization permission missing)" instead of
// listing the same sentence three times.
type gapSet struct {
	byReason map[string]*Gap
	order    []string
}

func newGapSet() *gapSet {
	return &gapSet{byReason: map[string]*Gap{}}
}

func (g *gapSet) add(reason string, ref parse.Reference) {
	gap, ok := g.byReason[reason]
	if !ok {
		gap = &Gap{Reason: reason}
		g.byReason[reason] = gap
		g.order = append(g.order, reason)
	}
	label := ref.String()
	if ref.Dynamic {
		label = ref.Raw
	}
	gap.Refs = append(gap.Refs, label)
	gap.Locs = append(gap.Locs, ref.Loc)
}

func (g *gapSet) list() []Gap {
	out := make([]Gap, 0, len(g.order))
	for _, reason := range g.order {
		out = append(out, *g.byReason[reason])
	}
	// Most affected first: that is the permission worth fixing.
	sort.SliceStable(out, func(i, j int) bool { return out[i].Count() > out[j].Count() })
	return out
}

// Package parse turns GitHub Actions workflow files into an internal model
// that keeps the line and column of everything it reports. A finding that
// cannot point at file:line is not actionable, so positions are not optional
// here.
//
// Parsing needs no token: workflow files are on disk.
package parse

import (
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/yumlabhq/yumlab/internal/expr"
)

// Location is a position inside a workflow file. Line and Col are 1-based.
type Location struct {
	File string
	Line int
	Col  int
}

func (l Location) String() string {
	if l.Col > 0 {
		return fmt.Sprintf("%s:%d:%d", l.File, l.Line, l.Col)
	}
	return fmt.Sprintf("%s:%d", l.File, l.Line)
}

// Short renders file:line, which is what the terminal report shows.
func (l Location) Short() string { return fmt.Sprintf("%s:%d", l.File, l.Line) }

// Reference is one context reference found in a workflow, located precisely
// and attributed to the job that contains it. The job matters because secrets
// resolve differently depending on the job's deployment environment.
type Reference struct {
	expr.Ref
	Loc   Location
	JobID string // empty when the reference sits outside any job
	Field string // dotted YAML path, e.g. jobs.deploy.steps[1].env.TOKEN
}

// Unresolved is an expression that could not be parsed or resolved statically.
// It is reported as UNKNOWN and must never be turned into a finding.
type Unresolved struct {
	expr.Unresolved
	Loc   Location
	JobID string
	Field string
}

// Environment is a deployment environment referenced by a job.
type Environment struct {
	Name string
	Loc  Location
}

// Access is the level granted to one permission scope.
type Access string

const (
	AccessNone  Access = "none"
	AccessRead  Access = "read"
	AccessWrite Access = "write"
)

// Permissions is a `permissions:` block.
//
// Two GitHub rules make this worth modelling precisely, because together they
// are what allows a missing scope to be proven rather than guessed:
//
//   - a job's block replaces the workflow's block entirely, it does not merge;
//   - naming any scope sets every scope that is not named to none.
//
// Declared separates "permissions: {}" (declared, grants nothing) from no block
// at all (not declared, the effective grant comes from a repository setting we
// cannot see offline).
type Permissions struct {
	// Declared is false when the workflow or job has no permissions block.
	Declared bool
	// Loc is where the block sits, meaningful only when Declared.
	Loc Location
	// Dynamic is set when the block is an expression we cannot resolve.
	Dynamic bool

	// scopes holds the levels that were named.
	scopes map[string]Access
	// all is set by the read-all and write-all shorthands, which name every
	// scope at once, including scopes GitHub may add later.
	all Access
}

// Grants reports the level for a scope. Because naming any scope sets the rest
// to none, an undeclared scope in a declared block is none.
func (p Permissions) Grants(scope string) Access {
	if lvl, ok := p.scopes[scope]; ok {
		return lvl
	}
	if p.all != "" {
		return p.all
	}
	return AccessNone
}

// Allows reports whether the block grants at least the wanted level.
func (p Permissions) Allows(scope string, want Access) bool {
	got := p.Grants(scope)
	if want == AccessWrite {
		return got == AccessWrite
	}
	return got == AccessRead || got == AccessWrite
}

// Scopes returns the named scopes, sorted.
func (p Permissions) Scopes() []string {
	out := make([]string, 0, len(p.scopes))
	for s := range p.scopes {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

// Step is one entry of a job's steps sequence. Only what the controls need is
// modelled: which action it runs and which inputs it was given.
type Step struct {
	Loc  Location
	Name string
	// Uses is the action reference, e.g. actions/checkout@v4. Empty for a
	// `run:` step.
	Uses string

	// inputs holds the step's `with:` mapping. Values matter: whether an action
	// is governed by the workflow's permissions depends on which token it was
	// handed, and that is written in the value.
	inputs map[string]string
}

// HasInput reports whether the step passed the named input under `with:`.
func (s Step) HasInput(name string) bool {
	_, ok := s.inputs[name]
	return ok
}

// Input returns the raw value of a `with:` input.
func (s Step) Input(name string) string { return s.inputs[name] }

// tokenInputs are the input names actions conventionally use to receive the
// token they authenticate with.
var tokenInputs = []string{"token", "repo-token", "github-token", "github_token"}

// UsesDefaultToken reports whether the step acts with the workflow's own
// GITHUB_TOKEN.
//
// This decides whether the `permissions:` block governs the step at all. An
// action handed a personal access token or a GitHub App token authenticates as
// something else entirely, and the block says nothing about what it may do —
// concluding anything there would be a false positive.
//
// A step that passes no token input at all gets GITHUB_TOKEN by convention, so
// the block does govern it.
func (s Step) UsesDefaultToken() bool {
	for _, name := range tokenInputs {
		v, ok := s.inputs[name]
		if !ok {
			continue
		}
		return referencesDefaultToken(v)
	}
	return true
}

// referencesDefaultToken reports whether an input value is the workflow's own
// token, written either as secrets.GITHUB_TOKEN or as github.token.
func referencesDefaultToken(value string) bool {
	for _, ref := range expr.Scan(value).Refs {
		switch {
		case ref.Context == expr.CtxSecrets && ref.Name() == "GITHUB_TOKEN":
			return true
		case ref.Context == expr.CtxGitHub && ref.Name() == "token":
			return true
		}
	}
	return false
}

// Job is one entry of the workflow's jobs mapping.
type Job struct {
	ID   string
	Name string
	Loc  Location

	// Steps are the job's steps, in order.
	Steps []Step

	// Environments lists the deployment environments whose secrets this job
	// can read. GitHub allows one environment per job; this stays a slice so
	// that a matrix over environments can be modelled later.
	Environments []Environment

	// EnvironmentDynamic is set when the environment name is an expression we
	// cannot resolve, for example environment: ${{ inputs.target }}. Secrets
	// for such a job cannot be verified.
	EnvironmentDynamic bool

	// Uses is set when the job calls a reusable workflow.
	Uses string

	// Permissions is the job's own permissions block, if it declares one.
	Permissions Permissions
}

// EffectivePermissions returns the permissions that actually apply to the job:
// its own block when it declares one, otherwise the workflow's.
func (w *Workflow) EffectivePermissions(job *Job) Permissions {
	if job != nil && job.Permissions.Declared {
		return job.Permissions
	}
	return w.Permissions
}

// Workflow is a parsed workflow file.
type Workflow struct {
	Path string // path as displayed, relative to the repository root
	Name string

	Jobs     map[string]*Job
	JobOrder []string

	References []Reference
	Unresolved []Unresolved

	// Reusable is true when the workflow declares on.workflow_call. Secrets in
	// a reusable workflow may be supplied by the caller, so they cannot be
	// verified against this repository alone.
	Reusable bool

	// CallSecrets holds the secret names declared under on.workflow_call.secrets.
	// They are parameters, not repository secrets.
	CallSecrets map[string]Environment

	// Permissions is the workflow-level permissions block, if it declares one.
	// It applies to every job that does not declare its own.
	Permissions Permissions

	Root  *yaml.Node
	lines []string
}

// Job returns the job with the given id.
func (w *Workflow) Job(id string) *Job {
	if w == nil {
		return nil
	}
	return w.Jobs[id]
}

// Parse builds a Workflow from the contents of a workflow file. displayPath is
// used verbatim in every reported location.
func Parse(displayPath string, src []byte) (*Workflow, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", displayPath, err)
	}

	w := &Workflow{
		Path:        displayPath,
		Jobs:        map[string]*Job{},
		CallSecrets: map[string]Environment{},
		Root:        &root,
		lines:       strings.Split(string(src), "\n"),
	}

	// An empty document parses into a node with no content.
	if len(root.Content) == 0 {
		return w, nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("parse %s: top level of a workflow must be a mapping", displayPath)
	}

	w.Name = scalarValue(mapValue(doc, "name"))
	w.Permissions = w.readPermissions(mapValue(doc, "permissions"))
	w.readTriggers(mapValue(doc, "on"))
	w.readJobs(mapValue(doc, "jobs"))
	w.walk(doc, walkCtx{field: "", jobID: ""})

	return w, nil
}

// readTriggers records whether the workflow is reusable and which secrets it
// declares as parameters.
func (w *Workflow) readTriggers(on *yaml.Node) {
	if on == nil {
		return
	}

	var call *yaml.Node
	switch on.Kind {
	case yaml.ScalarNode:
		w.Reusable = on.Value == "workflow_call"
	case yaml.SequenceNode:
		for _, item := range on.Content {
			if item.Kind == yaml.ScalarNode && item.Value == "workflow_call" {
				w.Reusable = true
			}
		}
	case yaml.MappingNode:
		if v, ok := mapEntry(on, "workflow_call"); ok {
			w.Reusable = true
			call = v
		}
	}

	if call == nil || call.Kind != yaml.MappingNode {
		return
	}
	secrets := mapValue(call, "secrets")
	if secrets == nil || secrets.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(secrets.Content); i += 2 {
		k := secrets.Content[i]
		w.CallSecrets[k.Value] = Environment{
			Name: k.Value,
			Loc:  Location{File: w.Path, Line: k.Line, Col: k.Column},
		}
	}
}

func (w *Workflow) readJobs(jobs *yaml.Node) {
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(jobs.Content); i += 2 {
		k, v := jobs.Content[i], jobs.Content[i+1]
		job := &Job{
			ID:  k.Value,
			Loc: Location{File: w.Path, Line: k.Line, Col: k.Column},
		}
		if v.Kind == yaml.MappingNode {
			job.Name = scalarValue(mapValue(v, "name"))
			job.Uses = scalarValue(mapValue(v, "uses"))
			job.Permissions = w.readPermissions(mapValue(v, "permissions"))
			job.Steps = w.readSteps(mapValue(v, "steps"))
			w.readEnvironment(job, mapValue(v, "environment"))
		}
		w.Jobs[job.ID] = job
		w.JobOrder = append(w.JobOrder, job.ID)
	}
}

// readSteps reads a job's steps sequence.
func (w *Workflow) readSteps(steps *yaml.Node) []Step {
	if steps == nil || steps.Kind != yaml.SequenceNode {
		return nil
	}

	out := make([]Step, 0, len(steps.Content))
	for _, n := range steps.Content {
		if n.Kind != yaml.MappingNode {
			continue
		}
		step := Step{
			Loc:    Location{File: w.Path, Line: n.Line, Col: n.Column},
			Name:   scalarValue(mapValue(n, "name")),
			Uses:   strings.TrimSpace(scalarValue(mapValue(n, "uses"))),
			inputs: map[string]string{},
		}
		// Point at the `uses:` line rather than the start of the step, so a
		// finding lands on the action that caused it.
		if u := mapValue(n, "uses"); u != nil {
			step.Loc = Location{File: w.Path, Line: u.Line, Col: u.Column}
		}
		if with := mapValue(n, "with"); with != nil && with.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(with.Content); i += 2 {
				step.inputs[with.Content[i].Value] = with.Content[i+1].Value
			}
		}
		out = append(out, step)
	}
	return out
}

// readPermissions reads a `permissions:` block.
//
// GitHub accepts three shapes: the shorthands read-all and write-all, an empty
// mapping which grants nothing, and a mapping of scope to level.
func (w *Workflow) readPermissions(n *yaml.Node) Permissions {
	if n == nil {
		return Permissions{}
	}

	p := Permissions{
		Declared: true,
		Loc:      Location{File: w.Path, Line: n.Line, Col: n.Column},
		scopes:   map[string]Access{},
	}

	switch n.Kind {
	case yaml.ScalarNode:
		if expr.HasExpression(n.Value) {
			p.Dynamic = true
			return p
		}
		// A shorthand names every scope at once. Rather than enumerate the
		// scopes GitHub happens to define today, record the level and let
		// Grants answer for any scope asked about.
		switch n.Value {
		case "read-all":
			p.all = AccessRead
		case "write-all":
			p.all = AccessWrite
		case "":
			// `permissions:` with nothing after it grants nothing.
		default:
			// An unrecognised shorthand is not something to guess about.
			p.Dynamic = true
		}

	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			if v.Kind != yaml.ScalarNode || expr.HasExpression(v.Value) {
				p.Dynamic = true
				continue
			}
			switch v.Value {
			case "read", "write", "none":
				p.scopes[k.Value] = Access(v.Value)
			default:
				p.Dynamic = true
			}
		}

	default:
		p.Dynamic = true
	}

	return p
}

func (w *Workflow) readEnvironment(job *Job, env *yaml.Node) {
	if env == nil {
		return
	}
	add := func(n *yaml.Node) {
		if n == nil || n.Kind != yaml.ScalarNode {
			return
		}
		// An environment named by an expression cannot be resolved, so the
		// job's environment secrets cannot be verified.
		if expr.HasExpression(n.Value) {
			job.EnvironmentDynamic = true
			return
		}
		job.Environments = append(job.Environments, Environment{
			Name: n.Value,
			Loc:  Location{File: w.Path, Line: n.Line, Col: n.Column},
		})
	}

	switch env.Kind {
	case yaml.ScalarNode:
		add(env)
	case yaml.MappingNode:
		add(mapValue(env, "name"))
	case yaml.SequenceNode:
		for _, item := range env.Content {
			add(item)
		}
	}
}

// walkCtx carries the position of the walker inside the document.
type walkCtx struct {
	field string
	jobID string
	key   string // key whose value we are visiting
}

func (c walkCtx) child(seg string) walkCtx {
	if c.field == "" {
		c.field = seg
	} else {
		c.field += "." + seg
	}
	c.key = seg
	return c
}

func (c walkCtx) index(i int) walkCtx {
	c.field = fmt.Sprintf("%s[%d]", c.field, i)
	return c
}

// walk visits every scalar in the document and scans it for expressions.
func (w *Workflow) walk(n *yaml.Node, ctx walkCtx) {
	switch n.Kind {
	case yaml.DocumentNode:
		for _, c := range n.Content {
			w.walk(c, ctx)
		}

	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			child := ctx.child(k.Value)
			// Keys directly under `jobs` name a job; everything below inherits it.
			if ctx.field == "jobs" {
				child.jobID = k.Value
			}
			w.walk(v, child)
		}

	case yaml.SequenceNode:
		for i, c := range n.Content {
			w.walk(c, ctx.index(i))
		}

	case yaml.ScalarNode:
		w.scan(n, ctx)

	case yaml.AliasNode:
		// Anchors are expanded by the resolver; the alias target is walked
		// through its own definition, so nothing to do here.
	}
}

func (w *Workflow) scan(n *yaml.Node, ctx walkCtx) {
	if n.Value == "" {
		return
	}

	var res expr.Result
	if ctx.key == "if" {
		// GitHub accepts `if:` with or without the ${{ }} wrapper.
		res = expr.ScanCondition(n.Value)
	} else {
		if !expr.HasExpression(n.Value) {
			return
		}
		res = expr.Scan(n.Value)
	}

	for _, r := range res.Refs {
		w.References = append(w.References, Reference{
			Ref:   r,
			Loc:   w.position(n, r.Offset, r.Raw),
			JobID: ctx.jobID,
			Field: ctx.field,
		})
	}
	for _, u := range res.Unresolved {
		w.Unresolved = append(w.Unresolved, Unresolved{
			Unresolved: u,
			Loc:        w.position(n, u.Offset, ""),
			JobID:      ctx.jobID,
			Field:      ctx.field,
		})
	}
}

// position maps an offset inside a scalar's value back to a line and column in
// the source file.
//
// yaml.v3 reports the position of the scalar's first token. For a literal block
// the value starts on the line after the `|` indicator and newlines are
// preserved, so the line can be computed exactly. Folded blocks reflow their
// content, so only the block's own line is trustworthy. In every case the
// column is recovered by locating raw in the source line, which also absorbs
// quoting and indentation.
func (w *Workflow) position(n *yaml.Node, off int, raw string) Location {
	loc := Location{File: w.Path, Line: n.Line, Col: n.Column}
	if off < 0 || off > len(n.Value) {
		return loc
	}

	newlines := strings.Count(n.Value[:off], "\n")
	switch n.Style {
	case yaml.LiteralStyle:
		loc.Line = n.Line + 1 + newlines
	case yaml.FoldedStyle:
		// Reflowed: fall back to a bounded search below.
		loc.Line = n.Line + 1
	default:
		loc.Line = n.Line + newlines
	}

	if raw == "" {
		return loc
	}

	// Prefer an exact hit on the computed line, then search the rest of the
	// scalar. Anything else keeps the node's own position.
	limit := n.Line + strings.Count(n.Value, "\n") + 2
	if col, ok := w.findInLine(loc.Line, raw); ok {
		loc.Col = col
		return loc
	}
	for l := loc.Line; l <= limit; l++ {
		if col, ok := w.findInLine(l, raw); ok {
			loc.Line = l
			loc.Col = col
			return loc
		}
	}
	return loc
}

func (w *Workflow) findInLine(line int, raw string) (int, bool) {
	if line < 1 || line > len(w.lines) {
		return 0, false
	}
	i := strings.Index(w.lines[line-1], raw)
	if i < 0 {
		return 0, false
	}
	return i + 1, true
}

func mapEntry(n *yaml.Node, key string) (*yaml.Node, bool) {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil, false
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1], true
		}
	}
	return nil, false
}

func mapValue(n *yaml.Node, key string) *yaml.Node {
	v, _ := mapEntry(n, key)
	return v
}

func scalarValue(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

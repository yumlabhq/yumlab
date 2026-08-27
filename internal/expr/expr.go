// Package expr parses the GitHub Actions expression language (${{ ... }}) and
// extracts the context references it contains: secrets.X, vars.X, env.X,
// matrix.X, needs.X.outputs.Y and friends.
//
// Two rules govern this package, and they come straight from the product
// principles: detection is purely deterministic, and anything that cannot be
// resolved statically is reported as unresolved rather than guessed. A caller
// must never turn an unresolved reference into a finding.
package expr

import "strings"

// Context names that can root a reference chain.
const (
	CtxSecrets  = "secrets"
	CtxVars     = "vars"
	CtxEnv      = "env"
	CtxMatrix   = "matrix"
	CtxNeeds    = "needs"
	CtxInputs   = "inputs"
	CtxGitHub   = "github"
	CtxSteps    = "steps"
	CtxJob      = "job"
	CtxJobs     = "jobs"
	CtxRunner   = "runner"
	CtxStrategy = "strategy"
)

var contexts = map[string]bool{
	CtxSecrets: true, CtxVars: true, CtxEnv: true, CtxMatrix: true,
	CtxNeeds: true, CtxInputs: true, CtxGitHub: true, CtxSteps: true,
	CtxJob: true, CtxJobs: true, CtxRunner: true, CtxStrategy: true,
}

// Ref is one reference to a context, for example secrets.NPM_TOKEN.
//
// Dynamic marks a reference whose path could not be fully resolved at rest,
// such as secrets[format('{0}_KEY', env.REGION)]. Path then holds only the
// segments resolved before the dynamic one, and callers must treat the
// reference as UNKNOWN.
type Ref struct {
	Context string   // root context, lowercased ("secrets")
	Path    []string // resolved segments after the root
	Dynamic bool     // true when a segment could not be resolved statically
	Raw     string   // source text of the reference
	Offset  int      // byte offset of the reference in the scanned string
	End     int      // byte offset just past the reference
}

// Name returns the first path segment, which for secrets and vars is the
// secret or variable name. It is empty for a dynamic or bare reference.
func (r Ref) Name() string {
	if len(r.Path) == 0 {
		return ""
	}
	return r.Path[0]
}

// String renders the reference the way a user writes it.
func (r Ref) String() string {
	var b strings.Builder
	b.WriteString(r.Context)
	for _, p := range r.Path {
		b.WriteString(".")
		b.WriteString(p)
	}
	if r.Dynamic {
		b.WriteString("[…]")
	}
	return b.String()
}

// Unresolved is an expression Yumlab could not parse. It never becomes a
// finding: it is counted and reported as UNKNOWN.
type Unresolved struct {
	Text   string // the expression source, without the ${{ }} delimiters
	Offset int    // byte offset of the expression in the scanned string
	Reason string
}

// Result is the outcome of scanning one string.
type Result struct {
	Refs       []Ref
	Unresolved []Unresolved
	Blocks     int // number of ${{ }} blocks found
}

// merge appends another result in place.
func (r *Result) merge(o Result) {
	r.Refs = append(r.Refs, o.Refs...)
	r.Unresolved = append(r.Unresolved, o.Unresolved...)
	r.Blocks += o.Blocks
}

// HasExpression reports whether s contains a ${{ }} block.
func HasExpression(s string) bool { return strings.Contains(s, "${{") }

// Scan extracts every reference from the ${{ }} blocks in s. Text outside the
// blocks is ignored. Offsets are relative to s.
func Scan(s string) Result {
	var res Result
	for _, b := range blocks(s) {
		res.Blocks++
		if !b.closed {
			res.Unresolved = append(res.Unresolved, Unresolved{
				Text:   b.body,
				Offset: b.bodyOff,
				Reason: "unterminated ${{ }} block",
			})
			continue
		}
		res.merge(parseOne(b.body, b.bodyOff))
	}
	return res
}

// ScanBare parses s as a single expression with no ${{ }} delimiters.
func ScanBare(s string) Result {
	if strings.TrimSpace(s) == "" {
		return Result{}
	}
	res := parseOne(s, 0)
	res.Blocks = 1
	return res
}

// ScanCondition scans an `if:` value, which GitHub accepts either wrapped in
// ${{ }} or bare.
func ScanCondition(s string) Result {
	if HasExpression(s) {
		return Scan(s)
	}
	return ScanBare(s)
}

func parseOne(body string, base int) Result {
	var res Result
	n, err := newParser(body, base).parseExpr()
	if err != nil {
		return Result{Unresolved: []Unresolved{{
			Text:   body,
			Offset: base,
			Reason: err.Error(),
		}}}
	}
	e := &extractor{src: body, base: base}
	e.walk(n)
	res.Refs = e.refs
	return res
}

// block is one ${{ ... }} occurrence.
type block struct {
	body    string
	bodyOff int
	closed  bool
}

// blocks splits s on ${{ }} boundaries. Braces inside single-quoted strings do
// not close a block, which matters for format('{0}}').
func blocks(s string) []block {
	var out []block
	for i := 0; i+2 < len(s); {
		start := strings.Index(s[i:], "${{")
		if start < 0 {
			break
		}
		start += i
		bodyOff := start + 3
		j := bodyOff
		inStr := false
		closed := false
		for j < len(s) {
			c := s[j]
			switch {
			case inStr:
				if c == '\'' {
					if j+1 < len(s) && s[j+1] == '\'' {
						j += 2
						continue
					}
					inStr = false
				}
			case c == '\'':
				inStr = true
			case c == '}' && j+1 < len(s) && s[j+1] == '}':
				closed = true
			}
			if closed {
				break
			}
			j++
		}
		if !closed {
			out = append(out, block{body: s[bodyOff:], bodyOff: bodyOff})
			break
		}
		out = append(out, block{body: s[bodyOff:j], bodyOff: bodyOff, closed: true})
		i = j + 2
	}
	return out
}

// extractor walks the AST and collects reference chains.
type extractor struct {
	src  string
	base int
	refs []Ref
}

func (e *extractor) text(off, end int) string {
	lo, hi := off-e.base, end-e.base
	if lo < 0 || hi > len(e.src) || lo > hi {
		return ""
	}
	return e.src[lo:hi]
}

func (e *extractor) walk(n node) {
	switch v := n.(type) {
	case nil:
		return

	case *identNode:
		if contexts[lower(v.name)] {
			e.refs = append(e.refs, Ref{
				Context: lower(v.name),
				Raw:     e.text(v.Off(), v.End()),
				Offset:  v.Off(),
				End:     v.End(),
			})
		}

	case *propNode, *indexNode:
		e.walkChain(n)

	case *callNode:
		for _, a := range v.args {
			e.walk(a)
		}

	case *unaryNode:
		e.walk(v.x)

	case *binaryNode:
		e.walk(v.x)
		e.walk(v.y)

	case *groupNode:
		e.walk(v.x)

	case *literalNode:
		// nothing to extract
	}
}

// walkChain resolves a dereference chain such as needs.build.outputs.sha. If it
// is rooted at a context, one Ref is emitted for the whole chain; otherwise the
// chain is walked as ordinary sub-expressions.
func (e *extractor) walkChain(n node) {
	root, segs := flatten(n)

	id, ok := root.(*identNode)
	if !ok || !contexts[lower(id.name)] {
		e.walk(root)
		for _, s := range segs {
			if s.sub != nil {
				e.walk(s.sub)
			}
		}
		return
	}

	ref := Ref{
		Context: lower(id.name),
		Raw:     e.text(n.Off(), n.End()),
		Offset:  n.Off(),
		End:     n.End(),
	}
	for _, s := range segs {
		// Sub-expressions inside an index are references in their own right:
		// secrets[env.NAME] contains a real env.NAME reference.
		if s.sub != nil {
			e.walk(s.sub)
		}
		if ref.Dynamic {
			continue
		}
		if !s.known {
			ref.Dynamic = true
			continue
		}
		ref.Path = append(ref.Path, s.name)
	}
	e.refs = append(e.refs, ref)
}

// segment is one step of a dereference chain. known is false when the segment
// is computed at runtime, in which case sub holds the index expression.
type segment struct {
	name  string
	known bool
	sub   node
}

// flatten unwinds a chain into its root and its segments, outermost last.
func flatten(n node) (node, []segment) {
	var segs []segment
	for {
		switch v := n.(type) {
		case *propNode:
			// A wildcard dereference selects every element: not resolvable.
			segs = append(segs, segment{name: v.name, known: v.name != "*"})
			n = v.target

		case *indexNode:
			if lit, ok := v.index.(*literalNode); ok {
				segs = append(segs, segment{name: lit.str, known: true})
			} else {
				segs = append(segs, segment{known: false, sub: v.index})
			}
			n = v.target

		default:
			reverse(segs)
			return n, segs
		}
	}
}

func reverse(s []segment) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

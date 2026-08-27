package expr

import (
	"strings"
	"testing"
)

// refString renders a reference the way the tests declare it, with a trailing
// "!" when the reference is dynamic.
func refString(r Ref) string {
	s := r.Context
	for _, p := range r.Path {
		s += "." + p
	}
	if r.Dynamic {
		s += "!"
	}
	return s
}

func refStrings(rs []Ref) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, refString(r))
	}
	return out
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestScanReferences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"simple secret", "${{ secrets.NPM_TOKEN }}", []string{"secrets.NPM_TOKEN"}},
		{"no spaces", "${{secrets.NPM_TOKEN}}", []string{"secrets.NPM_TOKEN"}},
		{"variable", "${{ vars.AWS_REGION }}", []string{"vars.AWS_REGION"}},
		{"env", "${{ env.NODE_VERSION }}", []string{"env.NODE_VERSION"}},
		{"matrix", "${{ matrix.node }}", []string{"matrix.node"}},
		{"needs output", "${{ needs.build.outputs.sha }}", []string{"needs.build.outputs.sha"}},
		{"job id with dash", "${{ needs.build-app.outputs.image-tag }}", []string{"needs.build-app.outputs.image-tag"}},
		{"steps output", "${{ steps.meta.outputs.tags }}", []string{"steps.meta.outputs.tags"}},
		{"github context", "${{ github.event.pull_request.number }}", []string{"github.event.pull_request.number"}},
		{"workflow_call input", "${{ inputs.environment }}", []string{"inputs.environment"}},

		{"two blocks", "${{ secrets.A }}-${{ secrets.B }}", []string{"secrets.A", "secrets.B"}},
		{"surrounded by text", "prefix ${{ secrets.A }} suffix", []string{"secrets.A"}},
		{"no expression at all", "npm ci", nil},
		{"empty", "", nil},

		{"string index", "${{ secrets['NPM_TOKEN'] }}", []string{"secrets.NPM_TOKEN"}},
		{"escaped quote in index", "${{ vars['IT''S'] }}", []string{"vars.IT'S"}},
		{"mixed index and property", "${{ needs['build'].outputs.sha }}", []string{"needs.build.outputs.sha"}},

		{"ternary style or", "${{ secrets.A || secrets.B }}", []string{"secrets.A", "secrets.B"}},
		{"and chain", "${{ secrets.A && vars.B }}", []string{"secrets.A", "vars.B"}},
		{"comparison", "${{ github.ref == 'refs/heads/main' }}", []string{"github.ref"}},
		{"negation", "${{ !cancelled() }}", nil},
		{"grouping", "${{ (secrets.A || secrets.B) && vars.C }}", []string{"secrets.A", "secrets.B", "vars.C"}},

		{"function call", "${{ format('{0}/{1}', github.repository, vars.TAG) }}", []string{"github.repository", "vars.TAG"}},
		{"nested calls", "${{ toJSON(fromJSON(vars.MATRIX)) }}", []string{"vars.MATRIX"}},
		{"hashFiles", "${{ hashFiles('**/package-lock.json') }}", nil},
		{"contains", "${{ contains(github.event.head_commit.message, 'skip ci') }}", []string{"github.event.head_commit.message"}},

		{"literals only", "${{ true && false }}", nil},
		{"numbers", "${{ 1 < 2 }}", nil},
		{"hex number", "${{ 0x1f }}", nil},
		{"float", "${{ 1.5 }}", nil},

		{"bare context", "${{ secrets }}", []string{"secrets"}},
		{"case insensitive context", "${{ SECRETS.NPM_TOKEN }}", []string{"secrets.NPM_TOKEN"}},
		{"unknown root is not a ref", "${{ foo.bar }}", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := refStrings(Scan(tc.in).Refs)
			if !equal(got, tc.want) {
				t.Errorf("Scan(%q) refs = %v, want %v", tc.in, got, tc.want)
			}
			if len(Scan(tc.in).Unresolved) != 0 {
				t.Errorf("Scan(%q) reported unresolved expressions, want none", tc.in)
			}
		})
	}
}

// Dynamic references must never be reported as a resolved name: that is how a
// false positive "missing secret" would be born.
func TestDynamicReferences(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"computed secret name", "${{ secrets[format('{0}_KEY', env.REGION)] }}", []string{"env.REGION", "secrets!"}},
		{"index by context value", "${{ secrets[env.SECRET_NAME] }}", []string{"env.SECRET_NAME", "secrets!"}},
		{"index by matrix", "${{ secrets[matrix.target] }}", []string{"matrix.target", "secrets!"}},
		{"wildcard", "${{ needs.*.outputs.sha }}", []string{"needs!"}},
		{"dynamic then property", "${{ secrets[matrix.x].y }}", []string{"matrix.x", "secrets!"}},
		{"concatenated name", "${{ vars[format('PREFIX_{0}', inputs.name)] }}", []string{"inputs.name", "vars!"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := refStrings(Scan(tc.in).Refs)
			if !equal(got, tc.want) {
				t.Errorf("Scan(%q) refs = %v, want %v", tc.in, got, tc.want)
			}
			for _, r := range Scan(tc.in).Refs {
				if r.Dynamic && r.Name() != "" {
					t.Errorf("dynamic ref %q leaked a resolved name %q", r.Raw, r.Name())
				}
			}
		})
	}
}

// Anything we cannot parse must land in Unresolved and produce no references.
func TestUnparsableExpressions(t *testing.T) {
	tests := []string{
		"${{ secrets. }}",
		"${{ secrets.NPM_TOKEN",
		"${{ ( secrets.A }}",
		"${{ }}",
		"${{ secrets.A && }}",
		`${{ "double quotes" }}`,
		"${{ 'unterminated }}",
		"${{ secrets.A secrets.B }}",
		"${{ @ }}",
	}

	for _, in := range tests {
		t.Run(in, func(t *testing.T) {
			res := Scan(in)
			if len(res.Unresolved) == 0 {
				t.Errorf("Scan(%q) = %v, want an unresolved expression", in, refStrings(res.Refs))
			}
			if len(res.Refs) != 0 {
				t.Errorf("Scan(%q) returned refs %v from an unparsable expression", in, refStrings(res.Refs))
			}
		})
	}
}

// A brace inside a string literal must not close the block.
func TestBracesInsideStrings(t *testing.T) {
	in := "${{ format('{0}}', secrets.A) }}"
	got := refStrings(Scan(in).Refs)
	want := []string{"secrets.A"}
	if !equal(got, want) {
		t.Errorf("Scan(%q) refs = %v, want %v", in, got, want)
	}
}

func TestOffsets(t *testing.T) {
	in := "value: ${{ secrets.NPM_TOKEN }} end"
	res := Scan(in)
	if len(res.Refs) != 1 {
		t.Fatalf("got %d refs, want 1", len(res.Refs))
	}
	r := res.Refs[0]
	if got := in[r.Offset:r.End]; got != "secrets.NPM_TOKEN" {
		t.Errorf("offsets select %q, want %q", got, "secrets.NPM_TOKEN")
	}
	if r.Raw != "secrets.NPM_TOKEN" {
		t.Errorf("Raw = %q, want %q", r.Raw, "secrets.NPM_TOKEN")
	}
}

func TestOffsetsAcrossMultipleBlocks(t *testing.T) {
	in := "a ${{ secrets.FIRST }} b ${{ vars.SECOND }}"
	res := Scan(in)
	if len(res.Refs) != 2 {
		t.Fatalf("got %d refs, want 2", len(res.Refs))
	}
	for _, want := range []string{"secrets.FIRST", "vars.SECOND"} {
		var found bool
		for _, r := range res.Refs {
			if in[r.Offset:r.End] == want {
				found = true
			}
		}
		if !found {
			t.Errorf("no ref whose offsets select %q", want)
		}
	}
}

func TestScanCondition(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{"github.event_name == 'push'", []string{"github.event_name"}},
		{"${{ github.event_name == 'push' }}", []string{"github.event_name"}},
		{"success() && secrets.DEPLOY_KEY != ''", []string{"secrets.DEPLOY_KEY"}},
		{"", nil},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := refStrings(ScanCondition(tc.in).Refs)
			if !equal(got, tc.want) {
				t.Errorf("ScanCondition(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestRefString(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"${{ secrets.A }}", "secrets.A"},
		{"${{ needs.build.outputs.sha }}", "needs.build.outputs.sha"},
		{"${{ secrets[matrix.x] }}", "secrets[…]"},
	}
	for _, tc := range tests {
		res := Scan(tc.in)
		var got string
		for _, r := range res.Refs {
			if r.Context == CtxSecrets || r.Context == CtxNeeds {
				got = r.String()
			}
		}
		if got != tc.want {
			t.Errorf("Scan(%q).String() = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Real-world shapes lifted from public workflows, to guard against regressions
// in the chain walker.
func TestRealWorldExpressions(t *testing.T) {
	tests := []struct {
		in   string
		want []string
	}{
		{
			"${{ secrets.DOCKERHUB_USERNAME }}/app:${{ github.sha }}",
			[]string{"secrets.DOCKERHUB_USERNAME", "github.sha"},
		},
		{
			"${{ github.event_name == 'push' && secrets.PROD_KEY || secrets.STAGING_KEY }}",
			[]string{"github.event_name", "secrets.PROD_KEY", "secrets.STAGING_KEY"},
		},
		{
			"${{ runner.os }}-node-${{ hashFiles('**/package-lock.json') }}",
			[]string{"runner.os"},
		},
		{
			"${{ fromJSON(needs.setup.outputs.matrix) }}",
			[]string{"needs.setup.outputs.matrix"},
		},
		{
			"${{ startsWith(github.ref, 'refs/tags/') && secrets.RELEASE_TOKEN != '' }}",
			[]string{"github.ref", "secrets.RELEASE_TOKEN"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			got := refStrings(Scan(tc.in).Refs)
			if !equal(got, tc.want) {
				t.Errorf("Scan(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestHasExpression(t *testing.T) {
	if !HasExpression("a ${{ b }}") {
		t.Error("HasExpression missed a block")
	}
	if HasExpression("plain text") {
		t.Error("HasExpression matched plain text")
	}
}

// The parser must not panic or hang on hostile input.
func TestNoPanicOnJunk(t *testing.T) {
	junk := []string{
		"${{", "${{}}", "${{ ${{ }} }}", "}}", "${{ [ }}", "${{ ((((((((((", "${{ ... }}",
		strings.Repeat("${{ secrets.A }}", 200),
		"${{ " + strings.Repeat("a.", 500) + "b }}",
	}
	for _, in := range junk {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("Scan(%q) panicked: %v", in, r)
				}
			}()
			Scan(in)
			ScanBare(in)
		}()
	}
}

package scan

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yumlabhq/yumlab/internal/github"
	"github.com/yumlabhq/yumlab/internal/report"
)

// stubAPI answers the handful of endpoints a scan needs, so the whole pipeline
// can be exercised without a real token.
func stubAPI(t *testing.T, routes map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/repos/acme/app", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"id": 1, "name": "app"}`)
	})
	for path, body := range routes {
		mux.HandleFunc("/api/v3/"+path, func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprint(w, body)
		})
	}
	mux.HandleFunc("/api/v3/", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message": "Not Found"}`)
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func secretsJSON(names ...string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`{"name": %q}`, n))
	}
	return fmt.Sprintf(`{"total_count": %d, "secrets": [%s]}`, len(names), strings.Join(parts, ","))
}

func variablesJSON(names ...string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`{"name": %q}`, n))
	}
	return fmt.Sprintf(`{"total_count": %d, "variables": [%s]}`, len(names), strings.Join(parts, ","))
}

// The full path: workflow files on disk, repository state from the API, a
// finding with a real file:line, and a rendered report.
func TestEndToEndProducesFindings(t *testing.T) {
	root := repoWith(t, "broken-secrets.yml")

	api := stubAPI(t, map[string]string{
		"repos/acme/app/actions/secrets":                   secretsJSON("NPM_TOKEN"),
		"repos/acme/app/actions/organization-secrets":      secretsJSON("SENTRY_DSN"),
		"repos/acme/app/actions/variables":                 variablesJSON(),
		"repos/acme/app/actions/organization-variables":    variablesJSON(),
		"repos/acme/app/environments":                      `{"total_count": 1, "environments": [{"name": "production"}]}`,
		"repositories/1/environments/production/secrets":   secretsJSON("AWS_DEPLOY_ROLE"),
		"repos/acme/app/environments/production/variables": variablesJSON(),
	})

	rep, err := Run(context.Background(), Options{
		Root:       root,
		Repository: github.Repository{Owner: "acme", Name: "app"},
		Token:      "test-token",
		BaseURL:    api.URL + "/",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	got := map[string]bool{}
	for _, f := range rep.Findings {
		got[f.Message] = true
		if f.Loc.File == "" || f.Loc.Line <= 0 {
			t.Errorf("finding %q has no file:line", f.Message)
		}
		if f.WastedMinutes <= 0 {
			t.Errorf("finding %q has no estimate", f.Message)
		}
	}

	for _, want := range []string{
		"secrets.RELEASE_WEBHOOK is not defined",
		"secrets.SLACK_WEBHOOK is not defined",
		"vars.AWS_REGION is not defined",
	} {
		if !got[want] {
			t.Errorf("missing finding %q, got %v", want, keys(got))
		}
	}

	// Defined somewhere the job can read, so not findings.
	for _, unwanted := range []string{
		"secrets.NPM_TOKEN is not defined",
		"secrets.SENTRY_DSN is not defined",
		"secrets.AWS_DEPLOY_ROLE is not defined",
		"secrets.GITHUB_TOKEN is not defined",
	} {
		if got[unwanted] {
			t.Errorf("false positive: %q", unwanted)
		}
	}

	if !rep.HasFindings() {
		t.Error("HasFindings should be true, the CLI must exit non-zero")
	}
	if rep.TotalWastedMinutes() <= 0 {
		t.Error("the report should total the wasted minutes")
	}

	var buf bytes.Buffer
	off := false
	if err := report.WriteTerminal(&buf, rep, report.TerminalOptions{Color: &off}); err != nil {
		t.Fatalf("WriteTerminal: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, ".github/workflows/broken-secrets.yml:") {
		t.Errorf("the rendered report should point at the workflow file\n---\n%s", out)
	}
	t.Logf("\n%s", out)
}

// The same scan, with the token unable to read organization secrets: no finding
// may survive, because absence can no longer be proven.
func TestEndToEndDegradesWhenOrganizationIsUnreadable(t *testing.T) {
	root := repoWith(t, "broken-secrets.yml")

	api := stubAPI(t, map[string]string{
		"repos/acme/app/actions/secrets":   secretsJSON("NPM_TOKEN"),
		"repos/acme/app/actions/variables": variablesJSON(),
		// organization-secrets and organization-variables fall through to 404.
		"repos/acme/app/environments":                      `{"total_count": 1, "environments": [{"name": "production"}]}`,
		"repositories/1/environments/production/secrets":   secretsJSON(),
		"repos/acme/app/environments/production/variables": variablesJSON(),
	})

	rep, err := Run(context.Background(), Options{
		Root:       root,
		Repository: github.Repository{Owner: "acme", Name: "app"},
		Token:      "test-token",
		BaseURL:    api.URL + "/",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(rep.Findings) != 0 {
		var msgs []string
		for _, f := range rep.Findings {
			msgs = append(msgs, f.Message)
		}
		t.Errorf("organization scope unreadable, no finding is allowed, got %v", msgs)
	}
	if rep.Unverified() == 0 {
		t.Error("the unverifiable references should be counted")
	}
	if rep.HasFindings() {
		t.Error("a degraded scan must exit clean, not fail the build")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

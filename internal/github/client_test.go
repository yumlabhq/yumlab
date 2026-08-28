package github

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const testRepoID = 4242

// apiServer is a stub GitHub API. Handlers are registered per path; any path
// without one answers 404, which is what GitHub does when a token cannot see a
// resource.
type apiServer struct {
	t        *testing.T
	mux      *http.ServeMux
	server   *httptest.Server
	requests map[string]int
}

func newAPIServer(t *testing.T) *apiServer {
	t.Helper()
	s := &apiServer{t: t, mux: http.NewServeMux(), requests: map[string]int{}}

	s.mux.HandleFunc("/api/v3/repos/acme/app", func(w http.ResponseWriter, r *http.Request) {
		s.count(r)
		fmt.Fprintf(w, `{"id": %d, "name": "app", "owner": {"login": "acme", "type": "Organization"}}`, testRepoID)
	})
	s.mux.HandleFunc("/api/v3/", func(w http.ResponseWriter, r *http.Request) {
		s.count(r)
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"message": "Not Found"}`)
	})

	s.server = httptest.NewServer(s.mux)
	t.Cleanup(s.server.Close)
	return s
}

func (s *apiServer) count(r *http.Request) {
	s.requests[r.URL.Path]++
}

// handle registers a JSON response for an API path, given without the /api/v3
// prefix.
func (s *apiServer) handle(path string, fn func(http.ResponseWriter, *http.Request)) {
	s.mux.HandleFunc("/api/v3/"+path, func(w http.ResponseWriter, r *http.Request) {
		s.count(r)
		fn(w, r)
	})
}

// status registers a path that always fails with the given HTTP status.
func (s *apiServer) status(path string, code int, message string) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		fmt.Fprintf(w, `{"message": %q}`, message)
	})
}

// secrets registers a secrets listing.
func (s *apiServer) secrets(path string, names ...string) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"total_count": %d, "secrets": [%s]}`, len(names), jsonNames(names))
	})
}

// variables registers a variables listing.
func (s *apiServer) variables(path string, names ...string) {
	s.handle(path, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"total_count": %d, "variables": [%s]}`, len(names), jsonNames(names))
	})
}

func (s *apiServer) environments(names ...string) {
	s.handle("repos/acme/app/environments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"total_count": %d, "environments": [%s]}`, len(names), jsonNames(names))
	})
}

func jsonNames(names []string) string {
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, fmt.Sprintf(`{"name": %q}`, n))
	}
	return strings.Join(parts, ",")
}

func (s *apiServer) client() *Client {
	s.t.Helper()
	c, err := New("acme", "app", Options{Token: "test-token", BaseURL: s.server.URL + "/"})
	if err != nil {
		s.t.Fatalf("New: %v", err)
	}
	return c
}

// The happy path: every scope readable, at every level.
func TestInventoryReadsEveryScope(t *testing.T) {
	s := newAPIServer(t)
	s.secrets("repos/acme/app/actions/secrets", "NPM_TOKEN")
	s.secrets("repos/acme/app/actions/organization-secrets", "SLACK_WEBHOOK")
	s.variables("repos/acme/app/actions/variables", "API_BASE")
	s.variables("repos/acme/app/actions/organization-variables", "AWS_REGION")
	s.environments("production")
	s.secrets(fmt.Sprintf("repositories/%d/environments/production/secrets", testRepoID), "AWS_ROLE")
	s.variables("repos/acme/app/environments/production/variables", "STACK")

	inv, err := s.client().Inventory(context.Background(), []string{"production"})
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}

	checks := []struct {
		what string
		set  NameSet
		name string
	}{
		{"repository secrets", inv.RepoSecrets, "NPM_TOKEN"},
		{"organization secrets", inv.OrgSecrets, "SLACK_WEBHOOK"},
		{"repository variables", inv.RepoVariables, "API_BASE"},
		{"organization variables", inv.OrgVariables, "AWS_REGION"},
		{"environment secrets", inv.EnvironmentSecrets("production"), "AWS_ROLE"},
		{"environment variables", inv.EnvironmentVariables("production"), "STACK"},
	}
	for _, c := range checks {
		if !c.set.Access.Readable() {
			t.Errorf("%s: Access = %v, want ok (%s)", c.what, c.set.Access, c.set.Reason)
			continue
		}
		if !c.set.Has(c.name) {
			t.Errorf("%s: missing %q, got %v", c.what, c.name, c.set.Names())
		}
	}
}

// The rule that matters: a 403 or a 404 becomes AccessDenied with an
// explanation, never an empty readable set, and never an error that aborts the
// whole scan.
func TestPermissionErrorsDegradeInsteadOfFailing(t *testing.T) {
	tests := []struct {
		name string
		code int
	}{
		{"forbidden", http.StatusForbidden},
		{"not found", http.StatusNotFound},
		{"unauthorized", http.StatusUnauthorized},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newAPIServer(t)
			s.status("repos/acme/app/actions/secrets", tc.code, "no access")
			s.secrets("repos/acme/app/actions/organization-secrets", "SHARED")
			s.variables("repos/acme/app/actions/variables")
			s.variables("repos/acme/app/actions/organization-variables")
			s.environments()

			inv, err := s.client().Inventory(context.Background(), nil)
			if err != nil {
				t.Fatalf("a missing permission must not fail the scan: %v", err)
			}

			if inv.RepoSecrets.Access != AccessDenied {
				t.Errorf("RepoSecrets.Access = %v, want AccessDenied", inv.RepoSecrets.Access)
			}
			if inv.RepoSecrets.Access.Readable() {
				t.Error("an unreadable scope must not be treated as readable")
			}
			if !strings.Contains(inv.RepoSecrets.Reason, "Secrets") {
				t.Errorf("Reason should name the missing permission, got %q", inv.RepoSecrets.Reason)
			}

			// The other scopes must still have been read.
			if !inv.OrgSecrets.Access.Readable() || !inv.OrgSecrets.Has("SHARED") {
				t.Error("one unreadable scope must not stop the others")
			}
		})
	}
}

func TestInventoryPaginates(t *testing.T) {
	s := newAPIServer(t)
	s.handle("repos/acme/app/actions/secrets", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("page") == "2" {
			fmt.Fprintf(w, `{"total_count": 2, "secrets": [%s]}`, jsonNames([]string{"SECOND"}))
			return
		}
		w.Header().Set("Link", fmt.Sprintf(`<%s/api/v3/repos/acme/app/actions/secrets?page=2>; rel="next"`, s.server.URL))
		fmt.Fprintf(w, `{"total_count": 2, "secrets": [%s]}`, jsonNames([]string{"FIRST"}))
	})
	s.secrets("repos/acme/app/actions/organization-secrets")
	s.variables("repos/acme/app/actions/variables")
	s.variables("repos/acme/app/actions/organization-variables")
	s.environments()

	inv, err := s.client().Inventory(context.Background(), nil)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if !inv.RepoSecrets.Has("FIRST") || !inv.RepoSecrets.Has("SECOND") {
		t.Errorf("pagination lost names, got %v", inv.RepoSecrets.Names())
	}
}

// An environment that is not configured holds nothing. That is a conclusive
// answer, so it is reported as missing rather than denied, and no request is
// wasted on it.
func TestUnknownEnvironmentIsMissingNotDenied(t *testing.T) {
	s := newAPIServer(t)
	s.secrets("repos/acme/app/actions/secrets")
	s.secrets("repos/acme/app/actions/organization-secrets")
	s.variables("repos/acme/app/actions/variables")
	s.variables("repos/acme/app/actions/organization-variables")
	s.environments("production")

	inv, err := s.client().Inventory(context.Background(), []string{"staging"})
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}

	got := inv.EnvironmentSecrets("staging")
	if got.Access != AccessMissing {
		t.Errorf("Access = %v, want AccessMissing", got.Access)
	}
	path := fmt.Sprintf("/api/v3/repositories/%d/environments/staging/secrets", testRepoID)
	if s.requests[path] != 0 {
		t.Error("no request should be made for an environment that does not exist")
	}
}

// If the repository itself cannot be read, the scan has nothing to stand on and
// must say so rather than produce an empty inventory.
func TestInventoryFailsWhenRepositoryIsUnreachable(t *testing.T) {
	s := newAPIServer(t) // /repos/acme/other is not registered, so it 404s
	c, err := New("acme", "other", Options{Token: "t", BaseURL: s.server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Inventory(context.Background(), nil); err == nil {
		t.Error("Inventory should fail when the repository cannot be read")
	}
}

// A repository owned by a user belongs to no organization. The organization
// scopes must come back as conclusively empty, not as denied: treating them as
// unreadable would poison the merged set and make every reference UNKNOWN,
// leaving Yumlab unable to say anything at all about a personal repository.
func TestUserOwnedRepositoryHasNoOrganizationScope(t *testing.T) {
	s := newAPIServer(t)
	s.mux.HandleFunc("/api/v3/repos/ousmane/side-project", func(w http.ResponseWriter, r *http.Request) {
		s.count(r)
		fmt.Fprint(w, `{"id": 99, "name": "side-project", "owner": {"login": "ousmane", "type": "User"}}`)
	})
	s.secrets("repos/ousmane/side-project/actions/secrets", "NPM_TOKEN")
	s.variables("repos/ousmane/side-project/actions/variables")
	s.handle("repos/ousmane/side-project/environments", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"total_count": 0, "environments": []}`)
	})
	// The organization endpoints are deliberately left unregistered: they must
	// never be called for a user-owned repository.

	c, err := New("ousmane", "side-project", Options{Token: "t", BaseURL: s.server.URL + "/"})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := c.Inventory(context.Background(), nil)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}

	for name, set := range map[string]NameSet{
		"OrgSecrets":   inv.OrgSecrets,
		"OrgVariables": inv.OrgVariables,
	} {
		if !set.Access.Readable() {
			t.Errorf("%s.Access = %v (%s), want a readable empty set", name, set.Access, set.Reason)
		}
		if set.Len() != 0 {
			t.Errorf("%s should be empty, got %v", name, set.Names())
		}
	}

	// The whole point: a missing secret is still conclusively missing.
	merged := inv.RepoSecrets.Merge(inv.OrgSecrets)
	if !merged.Access.Readable() {
		t.Fatal("merged set is unreadable, so no finding could ever be produced")
	}
	if !merged.Has("NPM_TOKEN") || merged.Has("ABSENT") {
		t.Errorf("merged set = %v, want exactly [NPM_TOKEN]", merged.Names())
	}

	for _, path := range []string{
		"/api/v3/repos/ousmane/side-project/actions/organization-secrets",
		"/api/v3/repos/ousmane/side-project/actions/organization-variables",
	} {
		if s.requests[path] != 0 {
			t.Errorf("called %s for a user-owned repository", path)
		}
	}
}

// A server error is not a permission problem and must be labelled differently,
// so the user does not go hunting for a token scope they already have.
func TestServerErrorIsNotReportedAsAPermissionProblem(t *testing.T) {
	s := newAPIServer(t)
	s.status("repos/acme/app/actions/secrets", http.StatusInternalServerError, "boom")
	s.secrets("repos/acme/app/actions/organization-secrets")
	s.variables("repos/acme/app/actions/variables")
	s.variables("repos/acme/app/actions/organization-variables")
	s.environments()

	inv, err := s.client().Inventory(context.Background(), nil)
	if err != nil {
		t.Fatalf("Inventory: %v", err)
	}
	if inv.RepoSecrets.Access != AccessError {
		t.Errorf("Access = %v, want AccessError", inv.RepoSecrets.Access)
	}
	if inv.RepoSecrets.Access.Readable() {
		t.Error("a failed read must never be readable")
	}
}

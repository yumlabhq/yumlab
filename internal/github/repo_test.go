package github

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseRemoteURL(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"https://github.com/yumlab/yumlab.git", "yumlab/yumlab"},
		{"https://github.com/yumlab/yumlab", "yumlab/yumlab"},
		{"http://github.com/yumlab/yumlab.git", "yumlab/yumlab"},
		{"git@github.com:yumlab/yumlab.git", "yumlab/yumlab"},
		{"git@github.com:yumlab/yumlab", "yumlab/yumlab"},
		{"ssh://git@github.com/yumlab/yumlab.git", "yumlab/yumlab"},
		{"git://github.com/yumlab/yumlab.git", "yumlab/yumlab"},
		{"https://user:token@github.com/yumlab/yumlab.git", "yumlab/yumlab"},
		{"https://github.example.com/yumlab/yumlab.git", "yumlab/yumlab"},
		{"https://github.com/yumlab/yumlab/", "yumlab/yumlab"},
		{"  git@github.com:yumlab/yumlab.git  ", "yumlab/yumlab"},
	}
	for _, tc := range tests {
		t.Run(tc.in, func(t *testing.T) {
			r, err := parseRemoteURL(tc.in)
			if err != nil {
				t.Fatalf("parseRemoteURL(%q): %v", tc.in, err)
			}
			if r.String() != tc.want {
				t.Errorf("parseRemoteURL(%q) = %q, want %q", tc.in, r, tc.want)
			}
		})
	}
}

func TestParseRemoteURLRejectsGarbage(t *testing.T) {
	for _, in := range []string{"", "notaurl", "https://github.com/", "github.com"} {
		if r, err := parseRemoteURL(in); err == nil {
			t.Errorf("parseRemoteURL(%q) = %q, want an error", in, r)
		}
	}
}

func TestParseSlug(t *testing.T) {
	r, err := ParseSlug("yumlab/yumlab")
	if err != nil || r.Owner != "yumlab" || r.Name != "yumlab" {
		t.Fatalf("ParseSlug = %v, %v", r, err)
	}
	for _, bad := range []string{"", "yumlab", "a/b/c", "/b", "a/"} {
		if _, err := ParseSlug(bad); err == nil {
			t.Errorf("ParseSlug(%q) should fail", bad)
		}
	}
}

func TestDetectRepositoryFromGitConfig(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")

	root := t.TempDir()
	gitDir := filepath.Join(root, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	config := `[core]
	repositoryformatversion = 0
[remote "upstream"]
	url = git@github.com:someone/else.git
[remote "origin"]
	url = git@github.com:yumlab/yumlab.git
	fetch = +refs/heads/*:refs/remotes/origin/*
`
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}

	// Detection must also work from a subdirectory.
	sub := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	r, err := DetectRepository(sub)
	if err != nil {
		t.Fatalf("DetectRepository: %v", err)
	}
	if r.String() != "yumlab/yumlab" {
		t.Errorf("DetectRepository = %q, want yumlab/yumlab", r)
	}
}

func TestDetectRepositoryPrefersEnvironment(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "octocat/hello-world")
	r, err := DetectRepository(t.TempDir())
	if err != nil {
		t.Fatalf("DetectRepository: %v", err)
	}
	if r.String() != "octocat/hello-world" {
		t.Errorf("DetectRepository = %q, want octocat/hello-world", r)
	}
	if r.Source != "GITHUB_REPOSITORY" {
		t.Errorf("Source = %q, want GITHUB_REPOSITORY", r.Source)
	}
}

func TestDetectRepositoryWithoutGit(t *testing.T) {
	t.Setenv("GITHUB_REPOSITORY", "")
	// t.TempDir sits under a directory that is not a git repository.
	if _, err := DetectRepository(t.TempDir()); err == nil {
		t.Error("DetectRepository should fail outside a git repository")
	}
}

func TestNameSetAccess(t *testing.T) {
	ok := NewNameSet([]string{"A", "B"})
	if !ok.Access.Readable() || !ok.Has("A") || ok.Has("C") {
		t.Error("NewNameSet should be readable and contain exactly its names")
	}

	denied := Unavailable(AccessDenied, "no permission")
	if denied.Access.Readable() {
		t.Error("a denied set must not be readable")
	}
	if denied.Has("A") {
		t.Error("a denied set has no names")
	}
}

// Merging with an unreadable scope must poison the result: a name missing from
// a half we could not read proves nothing.
func TestNameSetMergeKeepsUnreadable(t *testing.T) {
	ok := NewNameSet([]string{"A"})
	denied := Unavailable(AccessDenied, "no permission")

	merged := ok.Merge(denied)
	if merged.Access.Readable() {
		t.Error("merging a readable and an unreadable set must stay unreadable")
	}
	if !merged.Has("A") {
		t.Error("merge should keep known names even when unreadable")
	}
	if merged.Reason != "no permission" {
		t.Errorf("Reason = %q, want the unreadable side's reason", merged.Reason)
	}

	both := ok.Merge(NewNameSet([]string{"B"}))
	if !both.Access.Readable() || !both.Has("A") || !both.Has("B") {
		t.Error("merging two readable sets should give a readable union")
	}
}

func TestInventoryDefaultsAreSkippedNotEmpty(t *testing.T) {
	inv := NewInventory("o", "r", "offline")
	for name, set := range map[string]NameSet{
		"repo secrets": inv.RepoSecrets,
		"repo vars":    inv.RepoVariables,
		"org secrets":  inv.OrgSecrets,
		"org vars":     inv.OrgVariables,
		"environments": inv.Environments,
	} {
		if set.Access != AccessSkipped {
			t.Errorf("%s: Access = %v, want AccessSkipped", name, set.Access)
		}
	}
	if inv.EnvironmentSecrets("production").Access != AccessSkipped {
		t.Error("an unread environment must report AccessSkipped, not an empty readable set")
	}
}

func TestNewRequiresToken(t *testing.T) {
	if _, err := New("o", "r", Options{}); err == nil {
		t.Error("New without a token should fail")
	}
	if _, err := New("", "", Options{Token: "t"}); err == nil {
		t.Error("New without owner/repo should fail")
	}
}

package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, FileName), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadMissingFileIsNotAnError(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load without a config file: %v", err)
	}
	if cfg.Path != "" {
		t.Errorf("Path = %q, want empty", cfg.Path)
	}
	// The zero config must enable everything.
	if !cfg.ControlEnabled("ghost-secrets") {
		t.Error("controls should be enabled by default")
	}
}

func TestLoadDeclaredNames(t *testing.T) {
	root := writeConfig(t, `
secrets:
  repository:
    - CODECOV_TOKEN
  organization:
    - NPM_TOKEN
    - SLACK_WEBHOOK
  environments:
    production:
      - AWS_DEPLOY_ROLE
variables:
  organization:
    - AWS_REGION
`)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if !strings.HasSuffix(cfg.Path, FileName) {
		t.Errorf("Path = %q, should point at the config file", cfg.Path)
	}
	if len(cfg.Secrets.Repository) != 1 || cfg.Secrets.Repository[0] != "CODECOV_TOKEN" {
		t.Errorf("Secrets.Repository = %v", cfg.Secrets.Repository)
	}
	if len(cfg.Secrets.Organization) != 2 {
		t.Errorf("Secrets.Organization = %v, want 2 names", cfg.Secrets.Organization)
	}
	if got := cfg.Secrets.Environments["production"]; len(got) != 1 || got[0] != "AWS_DEPLOY_ROLE" {
		t.Errorf("Secrets.Environments[production] = %v", got)
	}
	if len(cfg.Variables.Organization) != 1 {
		t.Errorf("Variables.Organization = %v", cfg.Variables.Organization)
	}
}

// A control missing from the map stays enabled: the file turns things off, it
// is not an allow-list.
func TestControlEnabled(t *testing.T) {
	root := writeConfig(t, "controls:\n  ghost-secrets: false\n")
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ControlEnabled("ghost-secrets") {
		t.Error("a control set to false must be disabled")
	}
	if !cfg.ControlEnabled("some-future-control") {
		t.Error("a control absent from the map must stay enabled")
	}
}

func TestLoadRejectsMalformedYAML(t *testing.T) {
	root := writeConfig(t, "controls: [oops\n")
	if _, err := Load(root); err == nil {
		t.Error("a malformed config must be an error, not silently ignored")
	}
}

// The shipped example must stay loadable, or it stops being an example.
func TestExampleConfigParses(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", ".yumlab.example.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	root := writeConfig(t, string(src))

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("the example config does not parse: %v", err)
	}
	if len(cfg.Secrets.Organization) == 0 {
		t.Error("the example should declare organization secrets")
	}
	if !cfg.ControlEnabled("ghost-secrets") {
		t.Error("the example should leave ghost-secrets enabled")
	}
}

// Package config reads .yumlab.yaml.
//
// In v0.1 the file exists for one reason: the declarative fallback. Listing a
// repository's secrets requires admin access, and organization secrets require
// more than that. A developer who cannot get those permissions can instead
// declare the names they know about, which unblocks the secrets control without
// any privilege escalation. Declared names are treated exactly like names read
// from the API.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// FileName is the config file Yumlab looks for at the repository root.
const FileName = ".yumlab.yaml"

// Config is the parsed .yumlab.yaml.
type Config struct {
	// Path is where the config was loaded from, empty when there is none.
	Path string `yaml:"-"`

	// Controls enables or disables controls by id. A control not listed is
	// enabled.
	Controls map[string]bool `yaml:"controls"`

	// Secrets declares secret names that are known to exist but that the token
	// may not be allowed to list.
	Secrets Declared `yaml:"secrets"`

	// Variables does the same for Actions variables.
	Variables Declared `yaml:"variables"`
}

// Declared holds manually declared names per scope.
type Declared struct {
	Repository   []string            `yaml:"repository"`
	Organization []string            `yaml:"organization"`
	Environments map[string][]string `yaml:"environments"`
}

// ControlEnabled reports whether a control should run. Controls are enabled
// unless explicitly turned off.
func (c Config) ControlEnabled(id string) bool {
	if c.Controls == nil {
		return true
	}
	enabled, ok := c.Controls[id]
	return !ok || enabled
}

// Load reads .yumlab.yaml from the repository root. A missing file is not an
// error: it yields the zero config, which enables every control and declares
// nothing.
func Load(root string) (Config, error) {
	path := filepath.Join(root, FileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Config{}, nil
		}
		return Config{}, fmt.Errorf("read %s: %w", FileName, err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", FileName, err)
	}
	cfg.Path = path
	return cfg, nil
}

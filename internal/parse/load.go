package parse

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WorkflowsDir is where GitHub looks for workflow files.
const WorkflowsDir = ".github/workflows"

// LoadError records a workflow file that could not be read or parsed. A broken
// file is reported to the user, never silently dropped.
type LoadError struct {
	Path string
	Err  error
}

func (e LoadError) Error() string { return fmt.Sprintf("%s: %v", e.Path, e.Err) }
func (e LoadError) Unwrap() error { return e.Err }

// LoadFile parses a single workflow file. displayPath is the path shown in
// findings; when empty, path is used.
func LoadFile(path, displayPath string) (*Workflow, error) {
	if displayPath == "" {
		displayPath = path
	}
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read workflow: %w", err)
	}
	return Parse(filepath.ToSlash(displayPath), src)
}

// LoadDir parses every workflow file in <root>/.github/workflows.
//
// It returns the workflows it could parse and, separately, the files it could
// not. A missing workflows directory is not an error: it yields no workflows.
func LoadDir(root string) ([]*Workflow, []LoadError, error) {
	dir := filepath.Join(root, filepath.FromSlash(WorkflowsDir))

	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", WorkflowsDir, err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() || !isWorkflowFile(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	var (
		workflows []*Workflow
		bad       []LoadError
	)
	for _, name := range names {
		display := WorkflowsDir + "/" + name
		w, err := LoadFile(filepath.Join(dir, name), display)
		if err != nil {
			bad = append(bad, LoadError{Path: display, Err: err})
			continue
		}
		workflows = append(workflows, w)
	}
	return workflows, bad, nil
}

func isWorkflowFile(name string) bool {
	if strings.HasPrefix(name, ".") {
		return false
	}
	ext := strings.ToLower(filepath.Ext(name))
	return ext == ".yml" || ext == ".yaml"
}

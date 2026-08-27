package github

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Repository identifies the repository being scanned.
type Repository struct {
	Owner string
	Name  string
	// Source says where the identification came from, so the user can tell why
	// Yumlab is looking at a given repository.
	Source string
}

func (r Repository) String() string { return r.Owner + "/" + r.Name }

// ParseSlug reads an "owner/name" string.
func ParseSlug(s string) (Repository, error) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimSpace(s), ".git"), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Repository{}, fmt.Errorf("expected owner/repo, got %q", s)
	}
	return Repository{Owner: parts[0], Name: parts[1]}, nil
}

// DetectRepository works out which repository dir belongs to.
//
// In a GitHub Actions run, GITHUB_REPOSITORY is authoritative. Otherwise the
// origin remote in .git/config is used. Git itself is never invoked, so Yumlab
// works in containers that ship no git binary.
func DetectRepository(dir string) (Repository, error) {
	if slug := os.Getenv("GITHUB_REPOSITORY"); slug != "" {
		r, err := ParseSlug(slug)
		if err != nil {
			return Repository{}, fmt.Errorf("GITHUB_REPOSITORY: %w", err)
		}
		r.Source = "GITHUB_REPOSITORY"
		return r, nil
	}

	configPath, err := gitConfigPath(dir)
	if err != nil {
		return Repository{}, err
	}
	url, err := originURL(configPath)
	if err != nil {
		return Repository{}, err
	}
	r, err := parseRemoteURL(url)
	if err != nil {
		return Repository{}, err
	}
	r.Source = "git remote origin"
	return r, nil
}

// gitConfigPath finds the git config for dir, walking up to the repository root
// and following the gitdir pointer used by worktrees and submodules.
func gitConfigPath(dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %q: %w", dir, err)
	}

	for {
		gitPath := filepath.Join(abs, ".git")
		info, err := os.Stat(gitPath)
		switch {
		case err == nil && info.IsDir():
			return filepath.Join(gitPath, "config"), nil

		case err == nil:
			// A worktree or submodule: .git is a file holding "gitdir: <path>".
			target, err := readGitdirPointer(gitPath, abs)
			if err != nil {
				return "", err
			}
			// A linked worktree keeps its config in the main checkout.
			if common, err := os.ReadFile(filepath.Join(target, "commondir")); err == nil {
				target = filepath.Join(target, strings.TrimSpace(string(common)))
			}
			return filepath.Join(target, "config"), nil
		}

		parent := filepath.Dir(abs)
		if parent == abs {
			return "", fmt.Errorf("no git repository found at or above %q: pass --repo owner/name", dir)
		}
		abs = parent
	}
}

func readGitdirPointer(gitFile, base string) (string, error) {
	data, err := os.ReadFile(gitFile)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", gitFile, err)
	}
	line := strings.TrimSpace(string(data))
	target, ok := strings.CutPrefix(line, "gitdir:")
	if !ok {
		return "", fmt.Errorf("%s: expected a gitdir pointer", gitFile)
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(base, target)
	}
	return target, nil
}

// originURL extracts the origin remote URL from a git config file. The format
// is INI-like; only what is needed here is parsed.
func originURL(configPath string) (string, error) {
	f, err := os.Open(configPath)
	if err != nil {
		return "", fmt.Errorf("read git config: %w", err)
	}
	defer f.Close()

	var (
		inOrigin bool
		url      string
	)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") {
			inOrigin = strings.HasPrefix(line, `[remote "origin"]`)
			continue
		}
		if !inOrigin {
			continue
		}
		if key, value, ok := strings.Cut(line, "="); ok && strings.TrimSpace(key) == "url" {
			url = strings.TrimSpace(value)
		}
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("read git config: %w", err)
	}
	if url == "" {
		return "", fmt.Errorf("no origin remote in %s: pass --repo owner/name", configPath)
	}
	return url, nil
}

// parseRemoteURL handles the forms git uses for GitHub remotes:
//
//	https://github.com/owner/repo.git
//	git@github.com:owner/repo.git
//	ssh://git@github.com/owner/repo.git
//	git://github.com/owner/repo
func parseRemoteURL(raw string) (Repository, error) {
	s := strings.TrimSpace(raw)
	s = strings.TrimSuffix(s, "/")

	if scp, ok := strings.CutPrefix(s, "git@"); ok {
		if _, path, found := strings.Cut(scp, ":"); found {
			return slugFromPath(path, raw)
		}
	}
	for _, prefix := range []string{"https://", "http://", "ssh://", "git://"} {
		if rest, ok := strings.CutPrefix(s, prefix); ok {
			if _, path, found := strings.Cut(rest, "/"); found {
				return slugFromPath(path, raw)
			}
		}
	}
	return Repository{}, fmt.Errorf("cannot read owner/repo from remote %q: pass --repo owner/name", raw)
}

func slugFromPath(path, raw string) (Repository, error) {
	// Strip any userinfo left in an ssh:// URL and keep the last two segments,
	// which handles GitHub Enterprise paths with a prefix.
	path = strings.TrimSuffix(strings.Trim(path, "/"), ".git")
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		return Repository{}, fmt.Errorf("cannot read owner/repo from remote %q: pass --repo owner/name", raw)
	}
	owner, name := parts[len(parts)-2], parts[len(parts)-1]
	if owner == "" || name == "" {
		return Repository{}, fmt.Errorf("cannot read owner/repo from remote %q: pass --repo owner/name", raw)
	}
	return Repository{Owner: owner, Name: name}, nil
}

// FindRoot returns the repository root for dir, falling back to dir itself when
// there is no git repository. Workflow files are looked up from there.
func FindRoot(dir string) string {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir
	}
	for {
		if _, err := os.Stat(filepath.Join(abs, ".git")); err == nil {
			return abs
		}
		parent := filepath.Dir(abs)
		if parent == abs {
			return dir
		}
		abs = parent
	}
}

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const manifestName = "grove.json"

// Project groups worktrees of several repositories under <work_root>/<name>.
type Project struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Created     string      `json:"created"`
	Issues      []string    `json:"issues,omitempty"` // host/path#N refs or URLs
	Worktrees   []WorkEntry `json:"worktrees"`
}

// WorkEntry is one worktree inside a project.
type WorkEntry struct {
	Repo     string `json:"repo"` // host/path
	Dir      string `json:"dir"`  // relative to the project dir
	Branch   string `json:"branch"`
	Base     string `json:"base"`
	Issue    int    `json:"issue,omitempty"`
	IssueURL string `json:"issue_url,omitempty"`
	MR       int    `json:"mr,omitempty"`
	MRURL    string `json:"mr_url,omitempty"`
	Created  string `json:"created"`
}

var projectNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func (a *app) projectDir(name string) string { return filepath.Join(a.cfg.WorkRoot, name) }

func (a *app) loadProject(name string) (*Project, error) {
	if !projectNameRe.MatchString(name) {
		return nil, fmt.Errorf("invalid project name %q", name)
	}
	data, err := os.ReadFile(filepath.Join(a.projectDir(name), manifestName))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("project %q not found under %s (grove work new %s)", name, a.cfg.WorkRoot, name)
		}
		return nil, err
	}
	var p Project
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse %s: %w", filepath.Join(a.projectDir(name), manifestName), err)
	}
	if p.Worktrees == nil {
		p.Worktrees = []WorkEntry{}
	}
	return &p, nil
}

func (a *app) saveProject(p *Project) error {
	dir := a.projectDir(p.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(dir, manifestName+".tmp")
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(dir, manifestName))
}

func (a *app) listProjects() ([]*Project, error) {
	entries, err := os.ReadDir(a.cfg.WorkRoot)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []*Project
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		p, err := a.loadProject(e.Name())
		if err != nil {
			continue // a directory without a manifest is not a project
		}
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// entry finds the worktree for a repo (host/path) or a dir name.
func (p *Project) entry(key string) *WorkEntry {
	for i := range p.Worktrees {
		w := &p.Worktrees[i]
		if w.Repo == key || w.Dir == key || strings.HasSuffix(w.Repo, "/"+key) {
			return w
		}
	}
	return nil
}

// locals returns the project's worktrees as LocalRepo (Dir absolute).
func (a *app) locals(p *Project) []LocalRepo {
	out := make([]LocalRepo, 0, len(p.Worktrees))
	for _, w := range p.Worktrees {
		r, err := ParseSpec(w.Repo, a.cfg, "")
		if err != nil {
			continue
		}
		out = append(out, LocalRepo{Repo: r, Dir: filepath.Join(a.projectDir(p.Name), w.Dir)})
	}
	return out
}

// worktreeDirName picks a directory name for a repo inside a project: the
// project name, or group-project if that is taken.
func (p *Project) worktreeDirName(r Repo) string {
	name := r.Name()
	taken := func(n string) bool {
		for _, w := range p.Worktrees {
			if w.Dir == n {
				return true
			}
		}
		return false
	}
	if !taken(name) {
		return name
	}
	parts := strings.Split(r.Path, "/")
	for i := len(parts) - 2; i >= 0; i-- {
		n := strings.Join(parts[i:], "-")
		if !taken(n) {
			return n
		}
	}
	return strings.ReplaceAll(r.Full(), "/", "-")
}

// ensureMain returns the main clone of r, cloning it if needed.
func (a *app) ensureMain(ctx context.Context, r Repo) (LocalRepo, error) {
	lr := LocalRepo{Repo: r, Dir: r.Dir(a.root())}
	if isGitRepo(lr.Dir) {
		return lr, nil
	}
	fmt.Fprintf(os.Stderr, "cloning %s into %s\n", r.Full(), lr.Dir)
	if _, err := a.cloneOne(ctx, lr, &cloneFlags{}); err != nil {
		return lr, err
	}
	return lr, nil
}

func refExists(ctx context.Context, main LocalRepo, ref string) bool {
	_, err := gitIn(ctx, main, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// defaultBase returns origin/<default branch> of the main clone.
func defaultBase(ctx context.Context, main LocalRepo) (string, error) {
	if out, err := gitIn(ctx, main, "symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		return strings.TrimSpace(string(out)), nil
	}
	for _, b := range []string{"origin/master", "origin/main"} {
		if refExists(ctx, main, b) {
			return b, nil
		}
	}
	return "", fmt.Errorf("cannot determine the default branch of %s (git remote set-head origin -a)", main.Full())
}

// addWorktree creates <dir> as a worktree of main on branch, creating the
// branch from base when it exists neither locally nor on origin.
// Returns whether the branch was newly created.
func (a *app) addWorktree(ctx context.Context, main LocalRepo, dir, branch, base string) (bool, error) {
	if out, err := gitIn(ctx, main, "fetch", "--prune", "origin"); err != nil {
		return false, fmt.Errorf("git fetch: %s", lastLine(string(out)))
	}
	if base == "" {
		var err error
		if base, err = defaultBase(ctx, main); err != nil {
			return false, err
		}
	} else if !strings.HasPrefix(base, "origin/") && refExists(ctx, main, "origin/"+base) {
		base = "origin/" + base
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return false, err
	}
	var args []string
	created := false
	switch {
	case refExists(ctx, main, "refs/heads/"+branch):
		args = []string{"worktree", "add", dir, branch}
	case refExists(ctx, main, "refs/remotes/origin/"+branch):
		args = []string{"worktree", "add", "--track", "-b", branch, dir, "origin/" + branch}
	default:
		args = []string{"worktree", "add", "-b", branch, dir, base}
		created = true
	}
	a.logf("git -C %s %s", main.Dir, strings.Join(args, " "))
	if out, err := gitIn(ctx, main, args...); err != nil {
		return false, fmt.Errorf("git %s: %s", strings.Join(args, " "), lastLine(string(out)))
	}
	return created, nil
}

// pushBranch pushes a branch for the first time (-u), with host push options.
func (a *app) pushBranch(ctx context.Context, wt LocalRepo, branch string, opts []string) error {
	args := append([]string{"push"}, opts...)
	args = append(args, "-u", "origin", branch)
	if out, err := gitIn(ctx, wt, args...); err != nil {
		return fmt.Errorf("git %s: %s", strings.Join(args, " "), lastLine(string(out)))
	}
	return nil
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

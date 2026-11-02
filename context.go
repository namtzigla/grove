package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Context describes where a command is being run from: which checkout, branch,
// and (if inside the work root) which work project and entry.
type Context struct {
	Dir        string     `json:"dir"`
	Toplevel   string     `json:"toplevel,omitempty"`
	Repo       *Repo      `json:"repo,omitempty"`
	Branch     string     `json:"branch,omitempty"`
	IsWorktree bool       `json:"is_worktree"`
	MainDir    string     `json:"main_dir,omitempty"`
	Project    string     `json:"project,omitempty"`
	Work       *WorkEntry `json:"work,omitempty"`
}

// detectContext inspects dir (default: cwd).
func (a *app) detectContext(dir string) (*Context, error) {
	if dir == "" {
		var err error
		if dir, err = os.Getwd(); err != nil {
			return nil, err
		}
	}
	c := &Context{Dir: dir}
	top, err := execOut(dir, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return c, nil // not in a git checkout
	}
	c.Toplevel = strings.TrimSpace(string(top))
	if common, err := execOut(dir, "git", "rev-parse", "--path-format=absolute", "--git-common-dir"); err == nil {
		cd := filepath.Clean(strings.TrimSpace(string(common)))
		if cd != filepath.Join(c.Toplevel, ".git") {
			c.IsWorktree = true
			c.MainDir = filepath.Dir(cd)
		}
	}
	if b, err := execOut(dir, "git", "branch", "--show-current"); err == nil {
		c.Branch = strings.TrimSpace(string(b))
	}
	if u, err := originURL(c.Toplevel); err == nil {
		if r, err := ParseSpec(u, a.cfg, ""); err == nil {
			c.Repo = &r
		}
	}
	if rel, err := filepath.Rel(a.cfg.WorkRoot, c.Toplevel); err == nil && !strings.HasPrefix(rel, "..") && rel != "." {
		name := strings.Split(filepath.ToSlash(rel), "/")[0]
		if p, err := a.loadProject(name); err == nil {
			c.Project = name
			for i := range p.Worktrees {
				if filepath.Join(a.projectDir(name), p.Worktrees[i].Dir) == c.Toplevel {
					c.Work = &p.Worktrees[i]
				}
			}
		}
	}
	return c, nil
}

// contextRepo resolves an optional repo spec: explicit, else the current checkout.
func (a *app) contextRepo(spec string) (Repo, error) {
	if spec != "" {
		return ParseSpec(spec, a.cfg, a.cfg.DefaultHost)
	}
	c, err := a.detectContext("")
	if err != nil {
		return Repo{}, err
	}
	if c.Repo == nil {
		return Repo{}, fmt.Errorf("not inside a git checkout with an origin remote; pass a repo (e.g. acme/api)")
	}
	return *c.Repo, nil
}

// contextRef resolves "repo#N", "N", "#N", an URL, or ["repo", "N"] to a repo and number.
func (a *app) contextRef(args []string) (Repo, int, error) {
	switch len(args) {
	case 1:
		spec, n, ok := parseRef(args[0])
		if !ok {
			return Repo{}, 0, fmt.Errorf("%q: expected a number, repo#N or an URL", args[0])
		}
		r, err := a.contextRepo(spec)
		return r, n, err
	case 2:
		_, n, ok := parseRef(args[1])
		if !ok {
			return Repo{}, 0, fmt.Errorf("%q: expected a number", args[1])
		}
		r, err := a.contextRepo(args[0])
		return r, n, err
	}
	return Repo{}, 0, fmt.Errorf("expected <repo> <N>, <repo>#<N>, <N> or an URL")
}

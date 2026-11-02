package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

const maxScanDepth = 12

// LocalRepo is a repository that exists on disk under the root.
type LocalRepo struct {
	Repo
	Dir string `json:"dir"`
}

func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }

// isHostDir decides whether a first-level directory under root is a host
// directory (github.com, gitlab.example.com, ...). Anything else (src/, tmp/) is
// ignored by ls; use `grove relocate` to move such clones into place.
func isHostDir(cfg *Config, name string) bool {
	return cfg.IsKnownHost(name) || strings.Contains(name, ".")
}

// ScanLocal walks <root>/<host>/... and returns every git checkout found.
// A directory containing .git stops the descent (nested repos are not listed).
func ScanLocal(root string, cfg *Config, hosts []string) ([]LocalRepo, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []LocalRepo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || !isHostDir(cfg, e.Name()) {
			continue
		}
		if len(hosts) > 0 && !slices.Contains(hosts, e.Name()) {
			continue
		}
		host := e.Name()
		err := walkRepos(filepath.Join(root, host), 0, func(dir string) {
			rel, _ := filepath.Rel(filepath.Join(root, host), dir)
			if rel == "." {
				return
			}
			out = append(out, LocalRepo{Repo: Repo{Host: host, Path: filepath.ToSlash(rel)}, Dir: dir})
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Full() < out[j].Full() })
	return out, nil
}

// walkRepos calls fn for every git checkout under dir, without descending
// into checkouts, hidden directories or node_modules.
func walkRepos(dir string, depth int, fn func(dir string)) error {
	if depth > maxScanDepth {
		return nil
	}
	if isGitRepo(dir) {
		fn(dir)
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "node_modules" {
			continue
		}
		if err := walkRepos(filepath.Join(dir, e.Name()), depth+1, fn); err != nil {
			return err
		}
	}
	return nil
}

func isGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git")) // dir or file (worktrees)
	return err == nil
}

// Filter selects repositories by host, glob/prefix pattern and regex.
type Filter struct {
	Patterns []string // "acme", "acme/api", "gitlab.example.com/infra/**", "*/api"
	Exclude  []string // same syntax, wins over Patterns
	Hosts    []string
	Regex    *regexp.Regexp
}

// Match reports whether r passes the filter. An empty filter matches everything.
func (f *Filter) Match(r Repo) bool {
	if len(f.Hosts) > 0 && !slices.Contains(f.Hosts, r.Host) {
		return false
	}
	if f.Regex != nil && !f.Regex.MatchString(r.Full()) {
		return false
	}
	for _, ex := range f.Exclude {
		if matchPattern(ex, r) {
			return false
		}
	}
	if len(f.Patterns) == 0 {
		return true
	}
	for _, p := range f.Patterns {
		if matchPattern(p, r) {
			return true
		}
	}
	return false
}

// matchPattern: a pattern matches a repo when it equals, is a path-prefix of,
// or glob-matches either "host/path" or "path". Globs: * (one segment),
// ** (any depth), ? (one char).
func matchPattern(p string, r Repo) bool {
	p = strings.Trim(strings.TrimSpace(p), "/")
	if p == "" {
		return false
	}
	for _, cand := range []string{r.Full(), r.Path} {
		if cand == p || strings.HasPrefix(cand, p+"/") {
			return true
		}
		if strings.ContainsAny(p, "*?") && globRe(p).MatchString(cand) {
			return true
		}
	}
	return false
}

var (
	globCacheMu sync.Mutex
	globCache   = map[string]*regexp.Regexp{}
)

func globRe(p string) *regexp.Regexp {
	globCacheMu.Lock()
	defer globCacheMu.Unlock()
	if re, ok := globCache[p]; ok {
		return re
	}
	var sb strings.Builder
	sb.WriteString("^")
	for i := 0; i < len(p); i++ {
		switch {
		case strings.HasPrefix(p[i:], "**/"):
			sb.WriteString("(?:.*/)?")
			i += 2
		case strings.HasPrefix(p[i:], "**"):
			sb.WriteString(".*")
			i++
		case p[i] == '*':
			sb.WriteString("[^/]*")
		case p[i] == '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(p[i])))
		}
	}
	sb.WriteString("$")
	re := regexp.MustCompile(sb.String())
	globCache[p] = re
	return re
}

// originURL returns the fetch URL of the "origin" remote of a checkout.
func originURL(dir string) (string, error) {
	out, err := execOut(dir, "git", "config", "--get", "remote.origin.url")
	if err != nil {
		return "", fmt.Errorf("no origin remote")
	}
	return strings.TrimSpace(string(out)), nil
}

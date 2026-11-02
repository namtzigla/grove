package main

import (
	"bufio"
	"fmt"
	"io"
	"net/url"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

// Repo identifies a repository by host and namespaced path (no .git suffix).
type Repo struct {
	Host string `json:"host"`
	Path string `json:"path"`
}

// Full returns "host/group/project".
func (r Repo) Full() string { return r.Host + "/" + r.Path }

// Name returns the last path segment.
func (r Repo) Name() string { return path.Base(r.Path) }

// Dir returns the local checkout directory under root.
func (r Repo) Dir(root string) string {
	return filepath.Join(root, r.Host, filepath.FromSlash(r.Path))
}

// CloneURL builds the URL to clone from, using the given protocol.
func (r Repo) CloneURL(proto string) string {
	if proto == "https" {
		return "https://" + r.Host + "/" + r.Path + ".git"
	}
	return "git@" + r.Host + ":" + r.Path + ".git"
}

var (
	schemeRe  = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9+.-]*://`)
	scpLikeRe = regexp.MustCompile(`^(?:[\w.-]+@)?([\w.-]+):(.+)$`)
)

// ParseSpec accepts any of:
//
//	git@host:group/project.git
//	ssh://git@host[:port]/group/project.git
//	https://host/group/project[.git]
//	host/group/project
//	group/project              (resolved against defaultHost)
func ParseSpec(s string, cfg *Config, defaultHost string) (Repo, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Repo{}, fmt.Errorf("empty repo spec")
	}
	var host, p string
	switch {
	case schemeRe.MatchString(s):
		u, err := url.Parse(s)
		if err != nil {
			return Repo{}, fmt.Errorf("parse %q: %w", s, err)
		}
		host, p = u.Hostname(), u.Path
	case scpLikeRe.MatchString(s):
		m := scpLikeRe.FindStringSubmatch(s)
		host, p = m[1], m[2]
	default:
		first, rest, found := strings.Cut(s, "/")
		if found && (cfg.IsKnownHost(first) || strings.Contains(first, ".")) {
			host, p = first, rest
		} else {
			host, p = defaultHost, s
		}
	}
	if host == "" {
		return Repo{}, fmt.Errorf("%q has no host; pass --host or set default_host in the config", s)
	}
	p = strings.Trim(p, "/")
	p = strings.TrimSuffix(p, ".git")
	p = strings.TrimSuffix(p, "/")
	if !strings.Contains(p, "/") {
		return Repo{}, fmt.Errorf("%q: expected group/project", s)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return Repo{}, fmt.Errorf("%q: invalid path segment %q", s, seg)
		}
	}
	return Repo{Host: host, Path: p}, nil
}

// ParseSpecs parses a list of specs; "-" means read more from stdin.
func ParseSpecs(args []string, stdin io.Reader, cfg *Config, defaultHost string) ([]Repo, error) {
	var out []Repo
	seen := map[string]bool{}
	add := func(r Repo) {
		if !seen[r.Full()] {
			seen[r.Full()] = true
			out = append(out, r)
		}
	}
	for _, a := range args {
		if a == "-" {
			rs, err := readSpecs(stdin, cfg, defaultHost)
			if err != nil {
				return nil, err
			}
			for _, r := range rs {
				add(r)
			}
			continue
		}
		r, err := ParseSpec(a, cfg, defaultHost)
		if err != nil {
			return nil, err
		}
		add(r)
	}
	return out, nil
}

// readSpecs reads one spec per line; blank lines and # comments are skipped and
// only the first whitespace-separated field is used, so the output of
// `grove ls`, `grove ls-remote` and `grove status` can be piped back in.
func readSpecs(r io.Reader, cfg *Config, defaultHost string) ([]Repo, error) {
	var out []Repo
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "{") { // NDJSON from --json output
			var rr Repo
			if err := jsonUnmarshal([]byte(line), &rr); err == nil && rr.Host != "" && rr.Path != "" {
				out = append(out, rr)
				continue
			}
		}
		field := strings.Fields(line)[0]
		repo, err := ParseSpec(field, cfg, defaultHost)
		if err != nil {
			return nil, err
		}
		out = append(out, repo)
	}
	return out, sc.Err()
}

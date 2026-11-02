package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// HostKind selects which CLI (gh or glab) is used to enumerate a host.
type HostKind string

const (
	KindGitHub HostKind = "github"
	KindGitLab HostKind = "gitlab"
)

// HostConfig describes one git hosting instance.
type HostConfig struct {
	Kind     HostKind `json:"kind"`
	Protocol string   `json:"protocol,omitempty"` // ssh (default) or https
	// NewRepo holds extra API fields sent when creating a repository on this
	// host (gitlab: POST /projects, github: POST /user|orgs/:org/repos), e.g.
	// {"shared_runners_enabled": true} or {"has_wiki": false}.
	NewRepo map[string]any `json:"new_repo,omitempty"`
	// InboundJobTokenScope (gitlab) sets the project's inbound CI job-token
	// restriction after creation (false = any project may use this one's artifacts).
	InboundJobTokenScope *bool `json:"inbound_job_token_scope,omitempty"`
}

// Config is the on-disk configuration ($XDG_CONFIG_HOME/grove/config.json).
type Config struct {
	Root        string                `json:"root"`                   // where clones live; default ~/dev
	WorkRoot    string                `json:"work_root,omitempty"`    // where work projects (grouped worktrees) live; default ~/dev/work
	DefaultHost string                `json:"default_host,omitempty"` // host used for bare "group/project" specs
	Hosts       map[string]HostConfig `json:"hosts"`
	Exclude     []string              `json:"exclude,omitempty"` // patterns skipped by ls-remote / sync, e.g. "gitlab.example.com/archive/**"
}

func defaultConfigPath() string {
	if p := os.Getenv("GROVE_CONFIG"); p != "" {
		return p
	}
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		base = filepath.Join(homeDir(), ".config")
	}
	return filepath.Join(base, "grove", "config.json")
}

func homeDir() string {
	if h, err := os.UserHomeDir(); err == nil {
		return h
	}
	return os.Getenv("HOME")
}

func expandHome(p string) string {
	if p == "~" {
		return homeDir()
	}
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(homeDir(), p[2:])
	}
	return p
}

// LoadConfig reads the config file (if present) and applies environment
// overrides (GROVE_ROOT, GITLAB_HOST).
func LoadConfig(path string) (*Config, error) {
	cfg := &Config{
		Root:     "~/dev",
		WorkRoot: "~/dev/work",
		Hosts:    map[string]HostConfig{"github.com": {Kind: KindGitHub}},
	}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var file Config
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&file); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		if file.Root != "" {
			cfg.Root = file.Root
		}
		if file.WorkRoot != "" {
			cfg.WorkRoot = file.WorkRoot
		}
		cfg.DefaultHost = file.DefaultHost
		cfg.Exclude = file.Exclude
		for h, hc := range file.Hosts {
			cfg.Hosts[h] = hc
		}
	case errors.Is(err, fs.ErrNotExist):
		// no config file: defaults
	default:
		return nil, err
	}
	if env := os.Getenv("GROVE_ROOT"); env != "" {
		cfg.Root = env
	}
	if env := os.Getenv("GROVE_WORK_ROOT"); env != "" {
		cfg.WorkRoot = env
	}
	if h := os.Getenv("GITLAB_HOST"); h != "" {
		if _, ok := cfg.Hosts[h]; !ok {
			cfg.Hosts[h] = HostConfig{Kind: KindGitLab}
		}
	}
	cfg.Root = expandHome(cfg.Root)
	cfg.WorkRoot = expandHome(cfg.WorkRoot)
	for h, hc := range cfg.Hosts {
		if hc.Kind == "" {
			hc.Kind = guessKind(h)
		}
		if hc.Protocol == "" {
			hc.Protocol = "ssh"
		}
		if hc.Protocol != "ssh" && hc.Protocol != "https" {
			return nil, fmt.Errorf("host %s: protocol must be ssh or https, got %q", h, hc.Protocol)
		}
		cfg.Hosts[h] = hc
	}
	return cfg, nil
}

func guessKind(host string) HostKind {
	switch {
	case strings.Contains(host, "github"):
		return KindGitHub
	case strings.Contains(host, "gitlab"):
		return KindGitLab
	}
	return ""
}

// HostKind returns the kind of a host, falling back to a name heuristic.
func (c *Config) HostKind(host string) HostKind {
	if hc, ok := c.Hosts[host]; ok && hc.Kind != "" {
		return hc.Kind
	}
	return guessKind(host)
}

// Protocol returns the clone protocol configured for a host.
func (c *Config) Protocol(host string) string {
	if hc, ok := c.Hosts[host]; ok && hc.Protocol != "" {
		return hc.Protocol
	}
	return "ssh"
}

// IsKnownHost reports whether the host is configured.
func (c *Config) IsKnownHost(host string) bool {
	_, ok := c.Hosts[host]
	return ok
}

// HostNames returns the configured hosts, sorted.
func (c *Config) HostNames() []string {
	out := make([]string, 0, len(c.Hosts))
	for h := range c.Hosts {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

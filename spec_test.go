package main

import "testing"

func testCfg() *Config {
	return &Config{
		Root:        "/r",
		DefaultHost: "gitlab.example.com",
		Hosts: map[string]HostConfig{
			"github.com":         {Kind: KindGitHub},
			"gitlab.example.com": {Kind: KindGitLab},
		},
	}
}

func TestParseSpec(t *testing.T) {
	cfg := testCfg()
	cases := []struct {
		in   string
		want string
	}{
		{"git@gitlab.example.com:acme/api.git", "gitlab.example.com/acme/api"},
		{"git@github.com:you/project.git", "github.com/you/project"},
		{"ssh://git@gitlab.example.com:2222/infra/deploy/dev-1.git", "gitlab.example.com/infra/deploy/dev-1"},
		{"https://github.com/you/project", "github.com/you/project"},
		{"https://gitlab.example.com/acme/api.git/", "gitlab.example.com/acme/api"},
		{"github.com/you/project", "github.com/you/project"},
		{"gitlab.example.com/infra/deploy/dev-1", "gitlab.example.com/infra/deploy/dev-1"},
		{"acme/api", "gitlab.example.com/acme/api"},
		{"  acme/api  ", "gitlab.example.com/acme/api"},
	}
	for _, c := range cases {
		r, err := ParseSpec(c.in, cfg, cfg.DefaultHost)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if r.Full() != c.want {
			t.Errorf("%q: got %s want %s", c.in, r.Full(), c.want)
		}
	}
	bad := []string{"", "api", "acme/../api", "github.com", "https://github.com/"}
	for _, b := range bad {
		if _, err := ParseSpec(b, cfg, cfg.DefaultHost); err == nil {
			t.Errorf("%q: expected error", b)
		}
	}
	if _, err := ParseSpec("acme/api", cfg, ""); err == nil {
		t.Errorf("bare spec without default host: expected error")
	}
}

func TestRepoDirAndURL(t *testing.T) {
	r := Repo{Host: "gitlab.example.com", Path: "infra/deploy/dev-1"}
	if got := r.Dir("/r"); got != "/r/gitlab.example.com/infra/deploy/dev-1" {
		t.Errorf("Dir: %s", got)
	}
	if got := r.CloneURL("ssh"); got != "git@gitlab.example.com:infra/deploy/dev-1.git" {
		t.Errorf("ssh: %s", got)
	}
	if got := r.CloneURL("https"); got != "https://gitlab.example.com/infra/deploy/dev-1.git" {
		t.Errorf("https: %s", got)
	}
}

func TestMatchPattern(t *testing.T) {
	r := Repo{Host: "gitlab.example.com", Path: "infra/deploy/dev-1"}
	yes := []string{"infra", "infra/", "infra/deploy", "gitlab.example.com/infra", "gitlab.example.com/infra/deploy/dev-1",
		"infra/*/dev-1", "**/dev-1", "gitlab.example.com/**", "infra/**", "*/deploy/*", "infra/deploy/dev-?"}
	no := []string{"inf", "infra/deploy/dev", "github.com/infra", "acme", "*/dev-1", "infra/*", "deploy"}
	for _, p := range yes {
		if !matchPattern(p, r) {
			t.Errorf("%q should match", p)
		}
	}
	for _, p := range no {
		if matchPattern(p, r) {
			t.Errorf("%q should not match", p)
		}
	}
}

func TestFilter(t *testing.T) {
	r1 := Repo{Host: "gitlab.example.com", Path: "acme/api"}
	r2 := Repo{Host: "github.com", Path: "you/repos"}
	f := &Filter{Patterns: []string{"acme", "you"}, Exclude: []string{"github.com/**"}}
	if !f.Match(r1) || f.Match(r2) {
		t.Errorf("exclude should win")
	}
	f = &Filter{Hosts: []string{"github.com"}}
	if f.Match(r1) || !f.Match(r2) {
		t.Errorf("host filter")
	}
}

func TestParseStatus(t *testing.T) {
	out := "# branch.oid abc\n# branch.head master\n# branch.upstream origin/master\n# branch.ab +2 -1\n1 .M N... 100644 100644 100644 x y a.go\n? b.go\n? c.go\n"
	st := parseStatus("x", []byte(out))
	if st.Branch != "master" || st.Ahead != 2 || st.Behind != 1 || st.Modified != 1 || st.Untracked != 2 {
		t.Errorf("got %+v", st)
	}
}

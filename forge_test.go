package main

import "testing"

func TestParseRef(t *testing.T) {
	cases := []struct {
		in   string
		spec string
		n    int
		ok   bool
	}{
		{"12", "", 12, true},
		{"#12", "", 12, true},
		{"!7", "", 7, true},
		{"acme/api#12", "acme/api", 12, true},
		{"gitlab.example.com/acme/api!3", "gitlab.example.com/acme/api", 3, true},
		{"https://gitlab.example.com/infra/deploy/dev-1/-/issues/5", "gitlab.example.com/infra/deploy/dev-1", 5, true},
		{"https://gitlab.example.com/acme/api/-/merge_requests/9/", "gitlab.example.com/acme/api", 9, true},
		{"https://github.com/you/project/pull/2", "github.com/you/project", 2, true},
		{"https://github.com/you/project/issues/4", "github.com/you/project", 4, true},
		{"acme/api", "", 0, false},
		{"", "", 0, false},
	}
	for _, c := range cases {
		spec, n, ok := parseRef(c.in)
		if ok != c.ok || spec != c.spec || n != c.n {
			t.Errorf("%q: got (%q,%d,%v) want (%q,%d,%v)", c.in, spec, n, ok, c.spec, c.n, c.ok)
		}
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"v2: initialize vfs and config modules without viper or cobra":                             "v2-initialize-vfs-and-config-modules-without-viper-or-cobra",
		"  Fix  (Widget) authorized-networks!! ":                                                   "fix-widget-authorized-networks",
		"Console: replace the signed call-count drift with two explicit facts and more words here": "console-replace-the-signed-call-count-drift-with-two",
	}
	for in, want := range cases {
		if got := slugify(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestWorktreeDirName(t *testing.T) {
	p := &Project{Worktrees: []WorkEntry{{Repo: "gitlab.example.com/acme/api", Dir: "api"}}}
	if got := p.worktreeDirName(Repo{Host: "gitlab.example.com", Path: "platform/api"}); got != "platform-api" {
		t.Errorf("collision: got %q", got)
	}
	if got := p.worktreeDirName(Repo{Host: "github.com", Path: "you/project"}); got != "project" {
		t.Errorf("plain: got %q", got)
	}
}

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Issue is a host-neutral issue.
type Issue struct {
	Ref       string   `json:"ref"` // host/path#N
	Repo      Repo     `json:"repo"`
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	State     string   `json:"state"` // open | closed
	URL       string   `json:"url"`
	Author    string   `json:"author"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	Body      string   `json:"body,omitempty"`
}

// MR is a host-neutral merge request / pull request.
type MR struct {
	Ref    string `json:"ref"` // host/path!N (gitlab) or host/path#N (github)
	Repo   Repo   `json:"repo"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	State  string `json:"state"` // open | merged | closed
	Draft  bool   `json:"draft"`
	URL    string `json:"url"`
	Source string `json:"source_branch"`
	Target string `json:"target_branch"`
	SHA    string `json:"sha"`
	Author string `json:"author"`
	// MergeStatus is the host's mergeability verdict (gitlab detailed_merge_status,
	// github mergeable_state): mergeable | checking | ci_must_pass | conflict | ...
	MergeStatus string `json:"merge_status,omitempty"`
	Body        string `json:"body,omitempty"`
}

// Pipeline is the latest CI run for a ref.
type Pipeline struct {
	ID     int    `json:"id"`
	Status string `json:"status"` // pending | running | success | failed | canceled | skipped | manual | none
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	URL    string `json:"url"`
}

// Terminal reports whether the pipeline will not change state on its own.
func (p *Pipeline) Terminal() bool {
	switch p.Status {
	case "success", "failed", "canceled", "skipped", "manual", "none":
		return true
	}
	return false
}

type IssueListOpts struct {
	State  string // open | closed | all
	Mine   bool
	Labels []string
	Limit  int
}

type MRListOpts struct {
	State  string // open | merged | closed | all
	Mine   bool
	Branch string
	Limit  int
}

type MergeOpts struct {
	WhenPipelineSucceeds bool
	KeepBranch           bool
	Squash               bool
	SHA                  string
}

// RepoOpts describes a repository to create.
type RepoOpts struct {
	Description   string
	Visibility    string // private | internal | public
	Init          bool   // initialize with a README so the default branch exists
	DefaultBranch string
}

// forge is what grove needs from a git hosting service.
type forge interface {
	CreateRepo(ctx context.Context, r Repo, o RepoOpts) (*RemoteRepo, error)
	Issue(ctx context.Context, r Repo, n int) (*Issue, error)
	Issues(ctx context.Context, r Repo, o IssueListOpts) ([]Issue, error)
	CreateIssue(ctx context.Context, r Repo, title, body string, labels []string, assignMe bool) (*Issue, error)
	MR(ctx context.Context, r Repo, n int) (*MR, error)
	MRs(ctx context.Context, r Repo, o MRListOpts) ([]MR, error)
	CreateMR(ctx context.Context, r Repo, src, dst, title, body string, draft, assignMe bool) (*MR, error)
	SetDraft(ctx context.Context, r Repo, n int, draft bool) (*MR, error)
	Merge(ctx context.Context, r Repo, n int, o MergeOpts) (*MR, error)
	Pipeline(ctx context.Context, r Repo, ref string) (*Pipeline, error)
	MRPipeline(ctx context.Context, r Repo, n int) (*Pipeline, error)
	DefaultBranch(ctx context.Context, r Repo) (string, error)
	PushOptions() []string // extra args for the first push of a branch
	MRWord() string        // "MR" or "PR"
}

func (a *app) forge(host string) (forge, error) {
	switch a.cfg.HostKind(host) {
	case KindGitLab:
		return &gitlab{host: host, a: a}, nil
	case KindGitHub:
		return &github{host: host, a: a}, nil
	}
	return nil, fmt.Errorf("host %s: unknown kind; add it to the config with \"kind\": \"github\" or \"gitlab\"", host)
}

// rawMarker prefixes a field value that must be sent typed (-F: JSON
// number/bool/array) instead of as a string (-f).
const rawMarker = "\x00"

func raw(v string) string { return rawMarker + v }

// mapFields turns a JSON-ish map into api fields: strings as -f, everything else typed.
func mapFields(m map[string]any) [][2]string {
	var out [][2]string
	for k, v := range m {
		if str, ok := v.(string); ok {
			out = append(out, [2]string{k, str})
			continue
		}
		b, err := json.Marshal(v)
		if err != nil {
			continue
		}
		out = append(out, [2]string{k, raw(string(b))})
	}
	return out
}

// apiCall runs `<tool> api --hostname H -X METHOD path -f k=v ...` and decodes JSON into out (nil to ignore).
func (a *app) apiCall(ctx context.Context, tool, host, method, path string, fields [][2]string, out any) error {
	args := []string{"api", "--hostname", host, "-X", method}
	for _, f := range fields {
		if strings.HasPrefix(f[1], rawMarker) {
			args = append(args, "-F", f[0]+"="+strings.TrimPrefix(f[1], rawMarker))
		} else {
			args = append(args, "-f", f[0]+"="+f[1])
		}
	}
	args = append(args, path)
	a.logf("%s %s", tool, strings.Join(args, " "))
	var body []byte
	err := streamTool(ctx, tool, args, func(r io.Reader) error {
		var err error
		body, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		return err
	}
	if out == nil || len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%s %s %s: decode: %w", tool, method, path, err)
	}
	return nil
}

// ---------------------------------------------------------------- refs

var (
	refRe = regexp.MustCompile(`^(.*?)(?:[#!])(\d+)$`)
	urlRe = regexp.MustCompile(`^https?://([^/]+)/(.+?)/(?:-/)?(issues|merge_requests|pull|pulls)/(\d+)/?$`)
)

// parseRef splits "repo#12", "repo!12", "#12", "12" or an issue/MR URL into
// a repo spec (may be empty = current repo) and a number.
func parseRef(s string) (spec string, n int, ok bool) {
	s = strings.TrimSpace(s)
	if m := urlRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[4])
		return m[1] + "/" + m[2], n, true
	}
	if n, err := strconv.Atoi(s); err == nil {
		return "", n, true
	}
	if m := refRe.FindStringSubmatch(s); m != nil {
		n, _ := strconv.Atoi(m[2])
		return m[1], n, true
	}
	return "", 0, false
}

// slugify turns an issue title into a branch-safe slug (GitLab style).
func slugify(s string) string {
	s = strings.ToLower(s)
	var sb strings.Builder
	lastDash := true
	for _, c := range s {
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			sb.WriteRune(c)
			lastDash = false
		default:
			if !lastDash {
				sb.WriteByte('-')
				lastDash = true
			}
		}
	}
	out := strings.Trim(sb.String(), "-")
	if len(out) > 60 {
		out = out[:60]
		if i := strings.LastIndex(out, "-"); i > 20 {
			out = out[:i]
		}
	}
	return out
}

// ---------------------------------------------------------------- gitlab

type gitlab struct {
	host string
	a    *app
	me   struct {
		once     sync.Once
		id       int
		username string
		err      error
	}
}

func (g *gitlab) MRWord() string        { return "MR" }
func (g *gitlab) PushOptions() []string { return []string{"-o", "ci.skip"} }
func (g *gitlab) pid(r Repo) string     { return "projects/" + url.PathEscape(r.Path) }
func (g *gitlab) api(ctx context.Context, method, path string, fields [][2]string, out any) error {
	return g.a.apiCall(ctx, "glab", g.host, method, path, fields, out)
}

func (g *gitlab) currentUser(ctx context.Context) (int, string, error) {
	g.me.once.Do(func() {
		var u struct {
			ID       int    `json:"id"`
			Username string `json:"username"`
		}
		g.me.err = g.api(ctx, "GET", "user", nil, &u)
		g.me.id, g.me.username = u.ID, u.Username
	})
	return g.me.id, g.me.username, g.me.err
}

type glIssue struct {
	IID         int    `json:"iid"`
	Title       string `json:"title"`
	State       string `json:"state"`
	WebURL      string `json:"web_url"`
	Description string `json:"description"`
	Labels      []string
	Assignees   []struct {
		Username string `json:"username"`
	}
	Author struct {
		Username string `json:"username"`
	}
}

func (g *gitlab) toIssue(r Repo, i glIssue) Issue {
	state := "open"
	if i.State == "closed" {
		state = "closed"
	}
	var as []string
	for _, x := range i.Assignees {
		as = append(as, x.Username)
	}
	if i.Labels == nil {
		i.Labels = []string{}
	}
	if as == nil {
		as = []string{}
	}
	return Issue{Ref: fmt.Sprintf("%s#%d", r.Full(), i.IID), Repo: r, Number: i.IID, Title: i.Title, State: state,
		URL: i.WebURL, Author: i.Author.Username, Labels: i.Labels, Assignees: as, Body: i.Description}
}

func (g *gitlab) Issue(ctx context.Context, r Repo, n int) (*Issue, error) {
	var i glIssue
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/issues/%d", g.pid(r), n), nil, &i); err != nil {
		return nil, err
	}
	out := g.toIssue(r, i)
	return &out, nil
}

func (g *gitlab) Issues(ctx context.Context, r Repo, o IssueListOpts) ([]Issue, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(limit(o.Limit)))
	q.Set("order_by", "created_at")
	switch o.State {
	case "", "open":
		q.Set("state", "opened")
	case "closed":
		q.Set("state", "closed")
	}
	if o.Mine {
		_, me, err := g.currentUser(ctx)
		if err != nil {
			return nil, err
		}
		q.Set("author_username", me)
	}
	if len(o.Labels) > 0 {
		q.Set("labels", strings.Join(o.Labels, ","))
	}
	var items []glIssue
	if err := g.api(ctx, "GET", g.pid(r)+"/issues?"+q.Encode(), nil, &items); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(items))
	for _, i := range items {
		out = append(out, g.toIssue(r, i))
	}
	return out, nil
}

func (g *gitlab) CreateIssue(ctx context.Context, r Repo, title, body string, labels []string, assignMe bool) (*Issue, error) {
	fields := [][2]string{{"title", title}, {"description", body}}
	if len(labels) > 0 {
		fields = append(fields, [2]string{"labels", strings.Join(labels, ",")})
	}
	if assignMe {
		id, _, err := g.currentUser(ctx)
		if err != nil {
			return nil, err
		}
		fields = append(fields, [2]string{"assignee_ids", raw("[" + strconv.Itoa(id) + "]")})
	}
	var i glIssue
	if err := g.api(ctx, "POST", g.pid(r)+"/issues", fields, &i); err != nil {
		return nil, err
	}
	out := g.toIssue(r, i)
	return &out, nil
}

type glMR struct {
	IID          int    `json:"iid"`
	Title        string `json:"title"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	WebURL       string `json:"web_url"`
	SourceBranch string `json:"source_branch"`
	TargetBranch string `json:"target_branch"`
	SHA          string `json:"sha"`
	Description  string `json:"description"`
	MergeStatus  string `json:"detailed_merge_status"`
	Author       struct {
		Username string `json:"username"`
	}
}

func (g *gitlab) toMR(r Repo, m glMR) MR {
	state := "open"
	switch m.State {
	case "merged":
		state = "merged"
	case "closed":
		state = "closed"
	}
	return MR{Ref: fmt.Sprintf("%s!%d", r.Full(), m.IID), Repo: r, Number: m.IID, Title: m.Title, State: state, Draft: m.Draft,
		URL: m.WebURL, Source: m.SourceBranch, Target: m.TargetBranch, SHA: m.SHA, Author: m.Author.Username,
		MergeStatus: m.MergeStatus, Body: m.Description}
}

func (g *gitlab) MR(ctx context.Context, r Repo, n int) (*MR, error) {
	var m glMR
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/merge_requests/%d", g.pid(r), n), nil, &m); err != nil {
		return nil, err
	}
	out := g.toMR(r, m)
	return &out, nil
}

func (g *gitlab) MRs(ctx context.Context, r Repo, o MRListOpts) ([]MR, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(limit(o.Limit)))
	q.Set("order_by", "created_at")
	switch o.State {
	case "", "open":
		q.Set("state", "opened")
	case "all":
	default:
		q.Set("state", o.State)
	}
	if o.Branch != "" {
		q.Set("source_branch", o.Branch)
	}
	if o.Mine {
		_, me, err := g.currentUser(ctx)
		if err != nil {
			return nil, err
		}
		q.Set("author_username", me)
	}
	var items []glMR
	if err := g.api(ctx, "GET", g.pid(r)+"/merge_requests?"+q.Encode(), nil, &items); err != nil {
		return nil, err
	}
	out := make([]MR, 0, len(items))
	for _, m := range items {
		out = append(out, g.toMR(r, m))
	}
	return out, nil
}

func (g *gitlab) CreateMR(ctx context.Context, r Repo, src, dst, title, body string, draft, assignMe bool) (*MR, error) {
	if draft && !strings.HasPrefix(title, "Draft:") {
		title = "Draft: " + title
	}
	fields := [][2]string{{"source_branch", src}, {"target_branch", dst}, {"title", title}, {"description", body},
		{"remove_source_branch", raw("true")}}
	if assignMe {
		id, _, err := g.currentUser(ctx)
		if err != nil {
			return nil, err
		}
		fields = append(fields, [2]string{"assignee_id", raw(strconv.Itoa(id))})
	}
	var m glMR
	if err := g.api(ctx, "POST", g.pid(r)+"/merge_requests", fields, &m); err != nil {
		return nil, err
	}
	out := g.toMR(r, m)
	return &out, nil
}

func (g *gitlab) SetDraft(ctx context.Context, r Repo, n int, draft bool) (*MR, error) {
	cur, err := g.MR(ctx, r, n)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(cur.Title, "Draft:"), "WIP:"))
	if draft {
		title = "Draft: " + title
	}
	var m glMR
	if err := g.api(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d", g.pid(r), n), [][2]string{{"title", title}}, &m); err != nil {
		return nil, err
	}
	out := g.toMR(r, m)
	return &out, nil
}

func (g *gitlab) Merge(ctx context.Context, r Repo, n int, o MergeOpts) (*MR, error) {
	fields := [][2]string{{"should_remove_source_branch", raw(strconv.FormatBool(!o.KeepBranch))}}
	if o.WhenPipelineSucceeds {
		fields = append(fields, [2]string{"merge_when_pipeline_succeeds", raw("true")})
	}
	if o.Squash {
		fields = append(fields, [2]string{"squash", raw("true")})
	}
	if o.SHA != "" {
		fields = append(fields, [2]string{"sha", o.SHA})
	}
	// GitLab answers 405 whenever the MR is not mergeable right now, which
	// includes the few seconds of "checking" after an update; retry those,
	// and explain any other verdict.
	for attempt := 0; ; attempt++ {
		var m glMR
		err := g.api(ctx, "PUT", fmt.Sprintf("%s/merge_requests/%d/merge", g.pid(r), n), fields, &m)
		if err == nil {
			out := g.toMR(r, m)
			return &out, nil
		}
		if !strings.Contains(err.Error(), "405") {
			return nil, err
		}
		cur, cerr := g.MR(ctx, r, n)
		if cerr != nil {
			return nil, err
		}
		switch cur.MergeStatus {
		case "checking", "unchecked", "preparing", "":
			if attempt < 8 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
				}
				continue
			}
		}
		return nil, fmt.Errorf("%s not mergeable: %s (pipeline/approvals/conflicts; see %s)", cur.Ref, cur.MergeStatus, cur.URL)
	}
}

type glPipeline struct {
	ID     int    `json:"id"`
	Status string `json:"status"`
	Ref    string `json:"ref"`
	SHA    string `json:"sha"`
	WebURL string `json:"web_url"`
}

func glToPipeline(p glPipeline) *Pipeline {
	status := p.Status
	switch p.Status {
	case "created", "waiting_for_resource", "preparing", "pending", "scheduled":
		status = "pending"
	}
	return &Pipeline{ID: p.ID, Status: status, Ref: p.Ref, SHA: p.SHA, URL: p.WebURL}
}

func (g *gitlab) Pipeline(ctx context.Context, r Repo, ref string) (*Pipeline, error) {
	var items []glPipeline
	q := url.Values{"ref": {ref}, "per_page": {"1"}, "order_by": {"id"}, "sort": {"desc"}}
	if err := g.api(ctx, "GET", g.pid(r)+"/pipelines?"+q.Encode(), nil, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return &Pipeline{Status: "none", Ref: ref}, nil
	}
	return glToPipeline(items[0]), nil
}

func (g *gitlab) MRPipeline(ctx context.Context, r Repo, n int) (*Pipeline, error) {
	var items []glPipeline
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/merge_requests/%d/pipelines?per_page=1", g.pid(r), n), nil, &items); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return &Pipeline{Status: "none"}, nil
	}
	return glToPipeline(items[0]), nil
}

func (g *gitlab) CreateRepo(ctx context.Context, r Repo, o RepoOpts) (*RemoteRepo, error) {
	nsPath, name := pathSplit(r.Path)
	var ns struct {
		ID int `json:"id"`
	}
	if err := g.api(ctx, "GET", "namespaces/"+url.PathEscape(nsPath), nil, &ns); err != nil {
		return nil, fmt.Errorf("namespace %s: %w", nsPath, err)
	}
	fields := [][2]string{{"name", name}, {"path", name}, {"namespace_id", raw(strconv.Itoa(ns.ID))},
		{"initialize_with_readme", raw(strconv.FormatBool(o.Init))}}
	if o.Visibility != "" {
		fields = append(fields, [2]string{"visibility", o.Visibility})
	}
	if o.Description != "" {
		fields = append(fields, [2]string{"description", o.Description})
	}
	if o.DefaultBranch != "" {
		fields = append(fields, [2]string{"default_branch", o.DefaultBranch})
	}
	hc := g.a.cfg.Hosts[g.host]
	fields = append(fields, mapFields(hc.NewRepo)...)
	var p glRepo
	if err := g.api(ctx, "POST", "projects", fields, &p); err != nil {
		return nil, err
	}
	if hc.InboundJobTokenScope != nil {
		path := fmt.Sprintf("projects/%d/job_token_scope", p.ID)
		if err := g.api(ctx, "PATCH", path, [][2]string{{"enabled", raw(strconv.FormatBool(*hc.InboundJobTokenScope))}}, nil); err != nil {
			fmt.Fprintf(os.Stderr, "warning: job_token_scope: %v\n", err)
		}
	}
	out := glToRemote(g.host, p)
	return &out, nil
}

func (g *gitlab) DefaultBranch(ctx context.Context, r Repo) (string, error) {
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.api(ctx, "GET", g.pid(r), nil, &p); err != nil {
		return "", err
	}
	return p.DefaultBranch, nil
}

// ---------------------------------------------------------------- github

type github struct {
	host string
	a    *app
	me   struct {
		once  sync.Once
		login string
		err   error
	}
}

func (g *github) MRWord() string        { return "PR" }
func (g *github) PushOptions() []string { return nil }
func (g *github) rp(r Repo) string      { return "repos/" + r.Path }
func (g *github) api(ctx context.Context, method, path string, fields [][2]string, out any) error {
	return g.a.apiCall(ctx, "gh", g.host, method, path, fields, out)
}

func (g *github) currentUser(ctx context.Context) (string, error) {
	g.me.once.Do(func() {
		var u struct {
			Login string `json:"login"`
		}
		g.me.err = g.api(ctx, "GET", "user", nil, &u)
		g.me.login = u.Login
	})
	return g.me.login, g.me.err
}

type ghIssue struct {
	Number      int       `json:"number"`
	Title       string    `json:"title"`
	State       string    `json:"state"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PullRequest *struct{} `json:"pull_request,omitempty"`
	Labels      []struct {
		Name string `json:"name"`
	}
	Assignees []struct {
		Login string `json:"login"`
	}
	User struct {
		Login string `json:"login"`
	}
}

func (g *github) toIssue(r Repo, i ghIssue) Issue {
	labels, as := []string{}, []string{}
	for _, l := range i.Labels {
		labels = append(labels, l.Name)
	}
	for _, x := range i.Assignees {
		as = append(as, x.Login)
	}
	return Issue{Ref: fmt.Sprintf("%s#%d", r.Full(), i.Number), Repo: r, Number: i.Number, Title: i.Title, State: i.State,
		URL: i.HTMLURL, Author: i.User.Login, Labels: labels, Assignees: as, Body: i.Body}
}

func (g *github) Issue(ctx context.Context, r Repo, n int) (*Issue, error) {
	var i ghIssue
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/issues/%d", g.rp(r), n), nil, &i); err != nil {
		return nil, err
	}
	out := g.toIssue(r, i)
	return &out, nil
}

func (g *github) Issues(ctx context.Context, r Repo, o IssueListOpts) ([]Issue, error) {
	q := url.Values{}
	// The issues endpoint also returns pull requests; over-fetch and trim.
	q.Set("per_page", strconv.Itoa(limit(limit(o.Limit)*2)))
	state := o.State
	if state == "" {
		state = "open"
	}
	q.Set("state", state)
	if o.Mine {
		me, err := g.currentUser(ctx)
		if err != nil {
			return nil, err
		}
		q.Set("creator", me)
	}
	if len(o.Labels) > 0 {
		q.Set("labels", strings.Join(o.Labels, ","))
	}
	var items []ghIssue
	if err := g.api(ctx, "GET", g.rp(r)+"/issues?"+q.Encode(), nil, &items); err != nil {
		return nil, err
	}
	out := make([]Issue, 0, len(items))
	for _, i := range items {
		if i.PullRequest != nil {
			continue
		}
		out = append(out, g.toIssue(r, i))
		if len(out) == limit(o.Limit) {
			break
		}
	}
	return out, nil
}

func (g *github) CreateIssue(ctx context.Context, r Repo, title, body string, labels []string, assignMe bool) (*Issue, error) {
	fields := [][2]string{{"title", title}, {"body", body}}
	for _, l := range labels {
		fields = append(fields, [2]string{"labels[]", l})
	}
	if assignMe {
		me, err := g.currentUser(ctx)
		if err != nil {
			return nil, err
		}
		fields = append(fields, [2]string{"assignees[]", me})
	}
	var i ghIssue
	if err := g.api(ctx, "POST", g.rp(r)+"/issues", fields, &i); err != nil {
		return nil, err
	}
	out := g.toIssue(r, i)
	return &out, nil
}

type ghPR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	HTMLURL string `json:"html_url"`
	Body    string `json:"body"`
	Head    struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
	} `json:"base"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	MergedAt       *string `json:"merged_at"`
	MergeableState string  `json:"mergeable_state"`
}

func (g *github) toMR(r Repo, p ghPR) MR {
	state := "open"
	switch {
	case p.Merged || p.MergedAt != nil:
		state = "merged"
	case p.State == "closed":
		state = "closed"
	}
	return MR{Ref: fmt.Sprintf("%s#%d", r.Full(), p.Number), Repo: r, Number: p.Number, Title: p.Title, State: state, Draft: p.Draft,
		URL: p.HTMLURL, Source: p.Head.Ref, Target: p.Base.Ref, SHA: p.Head.SHA, Author: p.User.Login,
		MergeStatus: p.MergeableState, Body: p.Body}
}

func (g *github) MR(ctx context.Context, r Repo, n int) (*MR, error) {
	var p ghPR
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/pulls/%d", g.rp(r), n), nil, &p); err != nil {
		return nil, err
	}
	out := g.toMR(r, p)
	return &out, nil
}

func (g *github) MRs(ctx context.Context, r Repo, o MRListOpts) ([]MR, error) {
	q := url.Values{}
	q.Set("per_page", strconv.Itoa(limit(o.Limit)))
	switch o.State {
	case "", "open":
		q.Set("state", "open")
	case "closed", "merged":
		q.Set("state", "closed")
	default:
		q.Set("state", "all")
	}
	if o.Branch != "" {
		owner, _, _ := strings.Cut(r.Path, "/")
		q.Set("head", owner+":"+o.Branch)
	}
	var items []ghPR
	if err := g.api(ctx, "GET", g.rp(r)+"/pulls?"+q.Encode(), nil, &items); err != nil {
		return nil, err
	}
	var me string
	if o.Mine {
		var err error
		if me, err = g.currentUser(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]MR, 0, len(items))
	for _, p := range items {
		m := g.toMR(r, p)
		if o.Mine && m.Author != me {
			continue
		}
		if o.State == "merged" && m.State != "merged" {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

func (g *github) CreateMR(ctx context.Context, r Repo, src, dst, title, body string, draft, assignMe bool) (*MR, error) {
	fields := [][2]string{{"title", title}, {"body", body}, {"head", src}, {"base", dst}}
	if draft {
		fields = append(fields, [2]string{"draft", raw("true")})
	}
	var p ghPR
	if err := g.api(ctx, "POST", g.rp(r)+"/pulls", fields, &p); err != nil {
		return nil, err
	}
	if assignMe {
		me, err := g.currentUser(ctx)
		if err == nil {
			_ = g.api(ctx, "PATCH", fmt.Sprintf("%s/issues/%d", g.rp(r), p.Number), [][2]string{{"assignees[]", me}}, nil)
		}
	}
	out := g.toMR(r, p)
	return &out, nil
}

func (g *github) SetDraft(ctx context.Context, r Repo, n int, draft bool) (*MR, error) {
	args := []string{"pr", "ready", strconv.Itoa(n), "-R", g.host + "/" + r.Path}
	if draft {
		args = append(args, "--undo")
	}
	if _, err := runCapture(ctx, "gh", args...); err != nil {
		return nil, err
	}
	return g.MR(ctx, r, n)
}

func (g *github) Merge(ctx context.Context, r Repo, n int, o MergeOpts) (*MR, error) {
	args := []string{"pr", "merge", strconv.Itoa(n), "-R", g.host + "/" + r.Path}
	if o.Squash {
		args = append(args, "--squash")
	} else {
		args = append(args, "--merge")
	}
	if !o.KeepBranch {
		args = append(args, "--delete-branch")
	}
	if o.WhenPipelineSucceeds {
		args = append(args, "--auto")
	}
	if o.SHA != "" {
		args = append(args, "--match-head-commit", o.SHA)
	}
	if _, err := runCapture(ctx, "gh", args...); err != nil {
		return nil, err
	}
	return g.MR(ctx, r, n)
}

func (g *github) Pipeline(ctx context.Context, r Repo, ref string) (*Pipeline, error) {
	var res struct {
		Total     int `json:"total_count"`
		CheckRuns []struct {
			Status     string `json:"status"`
			Conclusion string `json:"conclusion"`
			HeadSHA    string `json:"head_sha"`
			HTMLURL    string `json:"html_url"`
		} `json:"check_runs"`
	}
	if err := g.api(ctx, "GET", fmt.Sprintf("%s/commits/%s/check-runs?per_page=100", g.rp(r), url.PathEscape(ref)), nil, &res); err != nil {
		return nil, err
	}
	p := &Pipeline{Ref: ref, Status: "none"}
	if res.Total == 0 {
		return p, nil
	}
	p.SHA = res.CheckRuns[0].HeadSHA
	p.URL = fmt.Sprintf("https://%s/%s/commit/%s/checks", g.host, r.Path, p.SHA)
	status := "success"
	for _, c := range res.CheckRuns {
		if c.Status != "completed" {
			status = "running"
			break
		}
		switch c.Conclusion {
		case "failure", "timed_out", "action_required", "startup_failure":
			status = "failed"
		case "cancelled":
			if status == "success" {
				status = "canceled"
			}
		}
	}
	p.Status = status
	return p, nil
}

func (g *github) MRPipeline(ctx context.Context, r Repo, n int) (*Pipeline, error) {
	m, err := g.MR(ctx, r, n)
	if err != nil {
		return nil, err
	}
	return g.Pipeline(ctx, r, m.SHA)
}

func (g *github) CreateRepo(ctx context.Context, r Repo, o RepoOpts) (*RemoteRepo, error) {
	owner, name := pathSplit(r.Path)
	if strings.Contains(owner, "/") {
		return nil, fmt.Errorf("github repos are owner/name, got %s", r.Path)
	}
	me, err := g.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	endpoint := "orgs/" + owner + "/repos"
	if strings.EqualFold(owner, me) {
		endpoint = "user/repos"
	}
	private := o.Visibility != "public"
	fields := [][2]string{{"name", name}, {"private", raw(strconv.FormatBool(private))}, {"auto_init", raw(strconv.FormatBool(o.Init))}}
	if o.Description != "" {
		fields = append(fields, [2]string{"description", o.Description})
	}
	fields = append(fields, mapFields(g.a.cfg.Hosts[g.host].NewRepo)...)
	var res struct {
		FullName      string `json:"full_name"`
		SSHURL        string `json:"ssh_url"`
		CloneURL      string `json:"clone_url"`
		HTMLURL       string `json:"html_url"`
		DefaultBranch string `json:"default_branch"`
		Owner         struct {
			Type string `json:"type"`
		} `json:"owner"`
	}
	if err := g.api(ctx, "POST", endpoint, fields, &res); err != nil {
		return nil, err
	}
	if o.DefaultBranch != "" && o.Init && res.DefaultBranch != o.DefaultBranch {
		// Rename the auto-created branch, then make it the default.
		_ = g.api(ctx, "POST", fmt.Sprintf("repos/%s/branches/%s/rename", res.FullName, res.DefaultBranch), [][2]string{{"new_name", o.DefaultBranch}}, nil)
		res.DefaultBranch = o.DefaultBranch
	}
	return &RemoteRepo{Repo: Repo{Host: g.host, Path: res.FullName}, SSHURL: res.SSHURL, HTTPURL: res.CloneURL, WebURL: res.HTMLURL,
		DefaultBranch: res.DefaultBranch, OwnerKind: strings.ToLower(res.Owner.Type)}, nil
}

func (g *github) DefaultBranch(ctx context.Context, r Repo) (string, error) {
	var p struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := g.api(ctx, "GET", g.rp(r), nil, &p); err != nil {
		return "", err
	}
	return p.DefaultBranch, nil
}

// pathSplit splits "group/sub/name" into ("group/sub", "name").
func pathSplit(p string) (string, string) {
	i := strings.LastIndex(p, "/")
	if i < 0 {
		return "", p
	}
	return p[:i], p[i+1:]
}

func limit(n int) int {
	if n <= 0 {
		return 50
	}
	if n > 100 {
		return 100
	}
	return n
}

// runCapture runs a tool and returns its combined output, folding failure output into the error.
func runCapture(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := execIn(ctx, "", nil, name, args...)
	if err != nil {
		return out, fmt.Errorf("%s %s: %s", name, strings.Join(args, " "), lastLine(string(out)))
	}
	return out, nil
}

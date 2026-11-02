package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func newMRCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:     "mr",
		Aliases: []string{"pr"},
		Short:   "Merge requests / pull requests with one interface (repo and branch default to the current checkout)",
	}
	cmd.AddCommand(newMRLsCmd(a), newMRViewCmd(a), newMRNewCmd(a), newMRReadyCmd(a), newMRMergeCmd(a))
	return cmd
}

func printMR(m MR, asJSON, withBody bool) error {
	if asJSON {
		if !withBody {
			m.Body = ""
		}
		return json.NewEncoder(os.Stdout).Encode(m)
	}
	draft := ""
	if m.Draft {
		draft = " (draft)"
	}
	fmt.Printf("%s\t%s%s\t%s -> %s\t%s\t%s\n", m.Ref, m.State, draft, m.Source, m.Target, m.Title, m.URL)
	if withBody && strings.TrimSpace(m.Body) != "" {
		fmt.Println()
		fmt.Println(m.Body)
	}
	return nil
}

func newMRLsCmd(a *app) *cobra.Command {
	var state, branch string
	var mine, asJSON bool
	var limit int
	cmd := &cobra.Command{
		Use:   "ls [repo]",
		Short: "List MRs / PRs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.contextRepo(first(args))
			if err != nil {
				return err
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			items, err := f.MRs(cmd.Context(), r, MRListOpts{State: state, Mine: mine, Branch: branch, Limit: limit})
			if err != nil {
				return err
			}
			for _, m := range items {
				if err := printMR(m, asJSON, false); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "open", "open | merged | closed | all")
	cmd.Flags().StringVar(&branch, "branch", "", "only MRs from this source branch")
	cmd.Flags().BoolVar(&mine, "mine", false, "only MRs I authored")
	cmd.Flags().IntVar(&limit, "limit", 50, "max results")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (one object per line)")
	return cmd
}

// contextMR resolves args to an MR: explicit ref/number, or the open MR of the current branch.
func (a *app) contextMR(ctx context.Context, args []string) (forge, *MR, error) {
	if len(args) > 0 {
		r, n, err := a.contextRef(args)
		if err != nil {
			return nil, nil, err
		}
		f, err := a.forge(r.Host)
		if err != nil {
			return nil, nil, err
		}
		m, err := f.MR(ctx, r, n)
		return f, m, err
	}
	c, err := a.detectContext("")
	if err != nil {
		return nil, nil, err
	}
	if c.Repo == nil || c.Branch == "" {
		return nil, nil, fmt.Errorf("not on a branch of a checkout; pass <N>, <repo>#<N> or an URL")
	}
	f, err := a.forge(c.Repo.Host)
	if err != nil {
		return nil, nil, err
	}
	if c.Work != nil && c.Work.MR > 0 {
		m, err := f.MR(ctx, *c.Repo, c.Work.MR)
		return f, m, err
	}
	items, err := f.MRs(ctx, *c.Repo, MRListOpts{Branch: c.Branch, State: "open", Limit: 1})
	if err != nil {
		return nil, nil, err
	}
	if len(items) == 0 {
		return nil, nil, fmt.Errorf("no open %s for branch %s of %s", f.MRWord(), c.Branch, c.Repo.Full())
	}
	return f, &items[0], nil
}

func newMRViewCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "view [N | repo!N | url | repo N]",
		Short: "Show one MR (default: the open MR of the current branch)",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			_, m, err := a.contextMR(cmd.Context(), args)
			if err != nil {
				return err
			}
			return printMR(*m, asJSON, true)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newMRNewCmd(a *app) *cobra.Command {
	var repoSpec, branch, target, title, body, bodyFile string
	var issue int
	var draft, asJSON, noPush, noAssign bool
	cmd := &cobra.Command{
		Use:   "new [--issue N] --title T [-F plan.md]",
		Short: "Open an MR / PR for the current branch (pushing it first if needed)",
		Long: `Open an MR for --branch (default: current branch) of --repo (default: current
checkout) against --target (default: origin's default branch).

If the branch is not on origin yet it is pushed first with -u (and
"-o ci.skip" on GitLab, so the MR pipeline is the only one). With --issue the
description starts with "Closes #N" and the title defaults to the issue title.
An existing open MR for the branch is returned instead of creating a second one.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			c, err := a.detectContext("")
			if err != nil {
				return err
			}
			var r Repo
			if repoSpec != "" {
				if r, err = ParseSpec(repoSpec, a.cfg, a.cfg.DefaultHost); err != nil {
					return err
				}
			} else if c.Repo != nil {
				r = *c.Repo
			} else {
				return fmt.Errorf("not in a checkout; pass --repo")
			}
			if branch == "" {
				branch = c.Branch
			}
			if branch == "" {
				return fmt.Errorf("no branch; pass --branch")
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			if target == "" {
				if target, err = f.DefaultBranch(ctx, r); err != nil {
					return err
				}
			}
			if branch == target {
				return fmt.Errorf("branch %s is the target branch", branch)
			}
			if existing, _ := f.MRs(ctx, r, MRListOpts{Branch: branch, State: "open", Limit: 1}); len(existing) > 0 {
				fmt.Fprintf(os.Stderr, "%s already open for %s\n", f.MRWord(), branch)
				return printMR(existing[0], asJSON, false)
			}
			var iss *Issue
			if issue > 0 {
				if iss, err = f.Issue(ctx, r, issue); err != nil {
					return fmt.Errorf("issue #%d: %w", issue, err)
				}
				if title == "" {
					title = iss.Title
				}
			}
			if title == "" {
				if c.Toplevel != "" {
					if out, err := execOut(c.Toplevel, "git", "log", "-1", "--format=%s", branch); err == nil {
						title = strings.TrimSpace(string(out))
					}
				}
				if title == "" {
					return fmt.Errorf("--title is required")
				}
			}
			// Push if the branch is unknown to origin.
			if !noPush && c.Toplevel != "" && c.Repo != nil && c.Repo.Full() == r.Full() {
				wt := LocalRepo{Repo: r, Dir: c.Toplevel}
				_, _ = gitIn(ctx, wt, "fetch", "--prune", "origin")
				if !refExists(ctx, wt, "refs/remotes/origin/"+branch) {
					if err := a.pushBranch(ctx, wt, branch, f.PushOptions()); err != nil {
						return err
					}
					fmt.Fprintf(os.Stderr, "pushed %s\n", branch)
				}
			}
			desc, err := mrBody(body, bodyFile, iss)
			if err != nil {
				return err
			}
			m, err := f.CreateMR(ctx, r, branch, target, title, desc, draft, !noAssign)
			if err != nil {
				return err
			}
			if c.Work != nil && c.Work.MR == 0 {
				if p, err := a.loadProject(c.Project); err == nil {
					if e := p.entry(r.Full()); e != nil {
						e.MR, e.MRURL = m.Number, m.URL
						if iss != nil && e.Issue == 0 {
							e.Issue, e.IssueURL = iss.Number, iss.URL
						}
						_ = a.saveProject(p)
					}
				}
			}
			return printMR(*m, asJSON, false)
		},
	}
	cmd.Flags().StringVar(&repoSpec, "repo", "", "repository (default: current checkout)")
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "source branch (default: current)")
	cmd.Flags().StringVar(&target, "target", "", "target branch (default: origin's default branch)")
	cmd.Flags().IntVarP(&issue, "issue", "i", 0, "issue the MR closes")
	cmd.Flags().StringVarP(&title, "title", "t", "", "title (default: issue title, else last commit subject)")
	cmd.Flags().StringVar(&body, "body", "", "description text")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "description from a file (- for stdin)")
	cmd.Flags().BoolVar(&draft, "draft", false, "open as draft")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "do not push the branch")
	cmd.Flags().BoolVar(&noAssign, "no-assign", false, "do not assign to me")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newMRReadyCmd(a *app) *cobra.Command {
	var undo, asJSON bool
	cmd := &cobra.Command{
		Use:   "ready [N | repo!N | url]",
		Short: "Mark an MR ready for review (drop the Draft prefix); --undo to make it a draft again",
		Args:  cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			f, m, err := a.contextMR(cmd.Context(), args)
			if err != nil {
				return err
			}
			m, err = f.SetDraft(cmd.Context(), m.Repo, m.Number, undo)
			if err != nil {
				return err
			}
			return printMR(*m, asJSON, false)
		},
	}
	cmd.Flags().BoolVar(&undo, "undo", false, "mark as draft instead")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newMRMergeCmd(a *app) *cobra.Command {
	var wait, whenGreen, squash, keepBranch, asJSON bool
	var timeout time.Duration
	cmd := &cobra.Command{
		Use:   "merge [N | repo!N | url]",
		Short: "Merge an MR (default: the one for the current branch), sha-pinned to what you see now",
		Long: `Merge an MR. The merge is pinned to the MR's current head sha, so a push
that lands in between makes the merge fail instead of merging unseen code.

  --wait         poll CI (grove ci wait) and merge only when it is green
  --when-green   let the server merge when its pipeline succeeds (GitLab MWPS / gh --auto)
  --squash       squash instead of a merge commit
  --keep-branch  do not delete the source branch`,
		Args: cobra.MaximumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			f, m, err := a.contextMR(ctx, args)
			if err != nil {
				return err
			}
			if m.State != "open" {
				return fmt.Errorf("%s is %s", m.Ref, m.State)
			}
			if m.Draft {
				return fmt.Errorf("%s is a draft; run: grove mr ready %d", m.Ref, m.Number)
			}
			if wait {
				p, err := waitPipeline(ctx, f, m.Repo, func() (*Pipeline, error) { return f.MRPipeline(ctx, m.Repo, m.Number) }, timeout, 15*time.Second)
				if err != nil {
					return err
				}
				if p.Status != "success" {
					return fmt.Errorf("pipeline %s (%s); not merging", p.Status, p.URL)
				}
			}
			merged, err := f.Merge(ctx, m.Repo, m.Number, MergeOpts{WhenPipelineSucceeds: whenGreen, KeepBranch: keepBranch, Squash: squash, SHA: m.SHA})
			if err != nil {
				return err
			}
			return printMR(*merged, asJSON, false)
		},
	}
	cmd.Flags().BoolVar(&wait, "wait", false, "wait for the MR pipeline to succeed before merging")
	cmd.Flags().BoolVar(&whenGreen, "when-green", false, "merge when the pipeline succeeds (server side)")
	cmd.Flags().BoolVar(&squash, "squash", false, "squash merge")
	cmd.Flags().BoolVar(&keepBranch, "keep-branch", false, "keep the source branch")
	cmd.Flags().DurationVar(&timeout, "timeout", 45*time.Minute, "max time to wait with --wait")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

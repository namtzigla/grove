package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

func newWorkCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "work",
		Short: "Work projects: git worktrees of several repos grouped under <work_root>/<project>",
		Long: `A work project groups one worktree per repository under one directory:

  ~/dev/work/<project>/<repo-name>     worktree of ~/dev/<host>/<path> on a feature branch
  ~/dev/work/<project>/grove.json      manifest: repos, branches, issues, MRs

Typical flow (the MR-first workflow):
  grove work new feature-x --desc "feature X"
  grove work add feature-x platform/api --issue 42 -F plan.md
      -> clones if needed, fetches, creates branch 42-<issue-slug> from origin/master,
         adds the worktree, pushes it (ci.skip on GitLab), opens a draft MR that
         closes the issue with plan.md as description, records everything in grove.json
  cd (grove work path feature-x api)      # or: cd (grove work add ...)
  grove work status feature-x
  grove work git feature-x push
  grove mr merge --wait                          # from inside the worktree
  grove work rm feature-x api`,
	}
	cmd.AddCommand(newWorkNewCmd(a), newWorkAddCmd(a), newWorkLsCmd(a), newWorkRmCmd(a), newWorkPathCmd(a),
		newWorkStatusCmd(a), newWorkEachCmd(a), newWorkGitCmd(a), newWorkInfoCmd(a))
	return cmd
}

func newWorkNewCmd(a *app) *cobra.Command {
	var desc string
	var issues []string
	cmd := &cobra.Command{
		Use:   "new <project>",
		Short: "Create an empty work project",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if !projectNameRe.MatchString(name) {
				return fmt.Errorf("invalid project name %q (letters, digits, . _ -)", name)
			}
			if _, err := a.loadProject(name); err == nil {
				return fmt.Errorf("project %q already exists at %s", name, a.projectDir(name))
			}
			p := &Project{Name: name, Description: desc, Created: now(), Issues: issues, Worktrees: []WorkEntry{}}
			if err := a.saveProject(p); err != nil {
				return err
			}
			fmt.Println(a.projectDir(name))
			return nil
		},
	}
	cmd.Flags().StringVar(&desc, "desc", "", "one-line description")
	cmd.Flags().StringArrayVar(&issues, "issue", nil, "related issue refs/URLs to record (repeatable)")
	return cmd
}

func newWorkAddCmd(a *app) *cobra.Command {
	var (
		branch, base, dirName, title, body, bodyFile string
		issue                                        int
		noMR, noPush, wantMR, asJSON, draft, noDraft bool
	)
	cmd := &cobra.Command{
		Use:   "add <project> <repo-spec> [--issue N] [-b branch]",
		Short: "Add a repository worktree to a project (branch, push, MR)",
		Long: `Add a worktree of <repo> to <project>:

 1. clone the main checkout if missing, git fetch origin
 2. branch: -b, else <issue>-<slugified issue title>, else the project name
 3. git worktree add (reusing the branch if it exists locally or on origin,
    otherwise created from --base, default origin/<default branch>)
 4. push -u (with "-o ci.skip" on GitLab) unless --no-push
 5. with --issue (or --mr): open a draft MR/PR from the branch to the base,
    "Closes #N" plus --body/-F as description, assigned to you  (--no-mr to skip)
 6. record the entry in grove.json

Prints the worktree directory on stdout (JSON with --json).`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.loadProject(args[0])
			if err != nil {
				return err
			}
			r, err := ParseSpec(args[1], a.cfg, a.cfg.DefaultHost)
			if err != nil {
				return err
			}
			if e := p.entry(r.Full()); e != nil {
				return fmt.Errorf("%s is already in project %s at %s", r.Full(), p.Name, filepath.Join(a.projectDir(p.Name), e.Dir))
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			var iss *Issue
			if issue > 0 {
				if iss, err = f.Issue(ctx, r, issue); err != nil {
					return fmt.Errorf("issue #%d: %w", issue, err)
				}
			}
			if branch == "" {
				switch {
				case iss != nil:
					branch = fmt.Sprintf("%d-%s", iss.Number, slugify(iss.Title))
				default:
					branch = p.Name
				}
			}
			if dirName == "" {
				dirName = p.worktreeDirName(r)
			}
			dir := filepath.Join(a.projectDir(p.Name), dirName)
			if _, err := os.Stat(dir); err == nil {
				return fmt.Errorf("%s already exists", dir)
			}
			main, err := a.ensureMain(ctx, r)
			if err != nil {
				return err
			}
			created, err := a.addWorktree(ctx, main, dir, branch, base)
			if err != nil {
				return err
			}
			wt := LocalRepo{Repo: r, Dir: dir}
			baseName := base
			if baseName == "" {
				b, _ := defaultBase(ctx, main)
				baseName = strings.TrimPrefix(b, "origin/")
			}
			baseName = strings.TrimPrefix(baseName, "origin/")
			fmt.Fprintf(os.Stderr, "worktree %s on %s (base %s, new=%v)\n", dir, branch, baseName, created)

			onRemote := refExists(ctx, main, "refs/remotes/origin/"+branch)
			if !noPush && !onRemote {
				if err := a.pushBranch(ctx, wt, branch, f.PushOptions()); err != nil {
					return err
				}
				onRemote = true
				fmt.Fprintf(os.Stderr, "pushed %s\n", branch)
			}

			entry := WorkEntry{Repo: r.Full(), Dir: dirName, Branch: branch, Base: baseName, Created: now()}
			if iss != nil {
				entry.Issue, entry.IssueURL = iss.Number, iss.URL
				p.Issues = appendUnique(p.Issues, iss.Ref)
			}
			if (iss != nil || wantMR) && !noMR {
				if !onRemote {
					return fmt.Errorf("cannot open a %s without pushing the branch (drop --no-push or use --no-mr)", f.MRWord())
				}
				existing, _ := f.MRs(ctx, r, MRListOpts{Branch: branch, State: "open", Limit: 1})
				var mr *MR
				if len(existing) > 0 {
					mr = &existing[0]
					fmt.Fprintf(os.Stderr, "%s already open: %s\n", f.MRWord(), mr.URL)
				} else {
					if title == "" {
						if iss != nil {
							title = iss.Title
						} else {
							title = strings.ReplaceAll(branch, "-", " ")
						}
					}
					desc, err := mrBody(body, bodyFile, iss)
					if err != nil {
						return err
					}
					mr, err = f.CreateMR(ctx, r, branch, baseName, title, desc, draft || !noDraft, true)
					if err != nil {
						return err
					}
					fmt.Fprintf(os.Stderr, "%s created: %s\n", f.MRWord(), mr.URL)
				}
				entry.MR, entry.MRURL = mr.Number, mr.URL
			}
			p.Worktrees = append(p.Worktrees, entry)
			if err := a.saveProject(p); err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(os.Stdout).Encode(struct {
					Project string `json:"project"`
					Path    string `json:"path"`
					WorkEntry
				}{p.Name, dir, entry})
			}
			fmt.Println(dir)
			return nil
		},
	}
	cmd.Flags().StringVarP(&branch, "branch", "b", "", "branch name (default: <issue>-<slug> or the project name)")
	cmd.Flags().StringVar(&base, "base", "", "base branch for a new branch and MR target (default: origin's default branch)")
	cmd.Flags().StringVar(&dirName, "dir", "", "worktree directory name inside the project (default: repo name)")
	cmd.Flags().IntVarP(&issue, "issue", "i", 0, "issue number the work implements (names the branch, links the MR)")
	cmd.Flags().StringVar(&title, "title", "", "MR title (default: issue title)")
	cmd.Flags().StringVar(&body, "body", "", "MR description text")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "MR description from a file (the implementation plan)")
	cmd.Flags().BoolVar(&wantMR, "mr", false, "open an MR even without --issue (needs --title)")
	cmd.Flags().BoolVar(&noMR, "no-mr", false, "do not open an MR")
	cmd.Flags().BoolVar(&noPush, "no-push", false, "do not push the branch")
	cmd.Flags().BoolVar(&draft, "draft", false, "open the MR as draft (default)")
	cmd.Flags().BoolVar(&noDraft, "no-draft", false, "open the MR ready for review")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the recorded entry as JSON")
	return cmd
}

// mrBody composes an MR description: Closes #N, then the plan text.
func mrBody(body, bodyFile string, iss *Issue) (string, error) {
	if bodyFile != "" {
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			return "", err
		}
		body = string(data)
	}
	var sb strings.Builder
	if iss != nil {
		fmt.Fprintf(&sb, "Closes #%d\n", iss.Number)
	}
	if strings.TrimSpace(body) != "" {
		if sb.Len() > 0 {
			sb.WriteString("\n")
		}
		sb.WriteString(strings.TrimRight(body, "\n"))
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

func appendUnique(xs []string, s string) []string {
	for _, x := range xs {
		if x == s {
			return xs
		}
	}
	return append(xs, s)
}

// workArg loads the project named by args[0] (or the project of the cwd).
func (a *app) workArg(args []string) (*Project, []string, error) {
	if len(args) > 0 {
		p, err := a.loadProject(args[0])
		if err == nil {
			return p, args[1:], nil
		}
	}
	c, _ := a.detectContext("")
	if c != nil && c.Project != "" {
		p, err := a.loadProject(c.Project)
		return p, args, err
	}
	if len(args) > 0 {
		_, err := a.loadProject(args[0])
		return nil, nil, err
	}
	return nil, nil, fmt.Errorf("no project given and not inside one (%s)", a.cfg.WorkRoot)
}

func newWorkLsCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls [project]",
		Short: "List work projects, or the worktrees of one project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			enc := json.NewEncoder(os.Stdout)
			if len(args) == 0 {
				ps, err := a.listProjects()
				if err != nil {
					return err
				}
				for _, p := range ps {
					if asJSON {
						if err := enc.Encode(p); err != nil {
							return err
						}
						continue
					}
					fmt.Printf("%s\t%d worktrees\t%s\n", p.Name, len(p.Worktrees), p.Description)
				}
				return nil
			}
			p, err := a.loadProject(args[0])
			if err != nil {
				return err
			}
			for _, w := range p.Worktrees {
				if asJSON {
					if err := enc.Encode(w); err != nil {
						return err
					}
					continue
				}
				extra := ""
				if w.Issue > 0 {
					extra += fmt.Sprintf("\tissue #%d", w.Issue)
				}
				if w.MR > 0 {
					extra += fmt.Sprintf("\t%s", w.MRURL)
				}
				fmt.Printf("%s\t%s\t%s%s\n", w.Repo, filepath.Join(a.projectDir(p.Name), w.Dir), w.Branch, extra)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (one object per line)")
	return cmd
}

func newWorkInfoCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "info [project]",
		Short: "Print a project's manifest (grove.json) with absolute paths",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, err := a.workArg(args)
			if err != nil {
				return err
			}
			type entry struct {
				WorkEntry
				Path string `json:"path"`
			}
			out := struct {
				Name        string   `json:"name"`
				Description string   `json:"description,omitempty"`
				Path        string   `json:"path"`
				Created     string   `json:"created"`
				Issues      []string `json:"issues,omitempty"`
				Worktrees   []entry  `json:"worktrees"`
			}{p.Name, p.Description, a.projectDir(p.Name), p.Created, p.Issues, []entry{}}
			for _, w := range p.Worktrees {
				out.Worktrees = append(out.Worktrees, entry{w, filepath.Join(a.projectDir(p.Name), w.Dir)})
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		},
	}
}

func newWorkPathCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path <project> [repo]",
		Short: "Print the project directory, or the worktree directory of one repo",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.loadProject(args[0])
			if err != nil {
				return err
			}
			if len(args) == 1 {
				fmt.Println(a.projectDir(p.Name))
				return nil
			}
			e := p.entry(args[1])
			if e == nil {
				return fmt.Errorf("%s is not in project %s", args[1], p.Name)
			}
			fmt.Println(filepath.Join(a.projectDir(p.Name), e.Dir))
			return nil
		},
	}
}

func newWorkRmCmd(a *app) *cobra.Command {
	var force, deleteBranch bool
	cmd := &cobra.Command{
		Use:   "rm <project> [repo...]",
		Short: "Remove worktrees from a project (all of them, and the project, when no repo is given)",
		Long: `Runs "git worktree remove" in the main checkout for each selected worktree.
A dirty worktree is refused unless --force. The branch is kept unless
--delete-branch. Without repos, every worktree and the project directory
are removed.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			p, err := a.loadProject(args[0])
			if err != nil {
				return err
			}
			var targets []*WorkEntry
			if len(args) == 1 {
				for i := range p.Worktrees {
					targets = append(targets, &p.Worktrees[i])
				}
			}
			for _, key := range args[1:] {
				e := p.entry(key)
				if e == nil {
					return fmt.Errorf("%s is not in project %s", key, p.Name)
				}
				targets = append(targets, e)
			}
			failed := 0
			removed := map[string]bool{}
			for _, e := range targets {
				r, err := ParseSpec(e.Repo, a.cfg, "")
				if err != nil {
					return err
				}
				main := LocalRepo{Repo: r, Dir: r.Dir(a.root())}
				dir := filepath.Join(a.projectDir(p.Name), e.Dir)
				if err := a.removeWorktree(ctx, main, dir, e.Branch, force, deleteBranch); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "%s: %v\n", e.Repo, err)
					continue
				}
				removed[e.Repo] = true
				fmt.Printf("removed\t%s\t%s\n", e.Repo, dir)
			}
			var keep []WorkEntry
			for _, w := range p.Worktrees {
				if !removed[w.Repo] {
					keep = append(keep, w)
				}
			}
			p.Worktrees = keep
			if len(args) == 1 && failed == 0 {
				if err := os.RemoveAll(a.projectDir(p.Name)); err != nil {
					return err
				}
				fmt.Printf("removed\tproject\t%s\n", a.projectDir(p.Name))
			} else if err := a.saveProject(p); err != nil {
				return err
			}
			if failed > 0 {
				return errSomeFailed
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "remove even if the worktree has uncommitted changes")
	cmd.Flags().BoolVar(&deleteBranch, "delete-branch", false, "also delete the local branch (git branch -D)")
	return cmd
}

func (a *app) removeWorktree(ctx context.Context, main LocalRepo, dir, branch string, force, deleteBranch bool) error {
	if _, err := os.Stat(dir); err == nil {
		args := []string{"worktree", "remove"}
		if force {
			args = append(args, "--force")
		}
		args = append(args, dir)
		if out, err := gitIn(ctx, main, args...); err != nil {
			return fmt.Errorf("git %s: %s", strings.Join(args, " "), lastLine(string(out)))
		}
	}
	_, _ = gitIn(ctx, main, "worktree", "prune")
	if deleteBranch && branch != "" {
		if out, err := gitIn(ctx, main, "branch", "-D", branch); err != nil {
			return fmt.Errorf("git branch -D %s: %s", branch, lastLine(string(out)))
		}
	}
	return nil
}

func newWorkStatusCmd(a *app) *cobra.Command {
	var dirty, asJSON bool
	cmd := &cobra.Command{
		Use:   "status [project]",
		Short: "Status of every worktree in a project",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, _, err := a.workArg(args)
			if err != nil {
				return err
			}
			return a.printStatus(cmd.Context(), a.locals(p), dirty, asJSON)
		},
	}
	cmd.Flags().BoolVar(&dirty, "dirty", false, "only show worktrees with changes")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per line")
	return cmd
}

func newWorkEachCmd(a *app) *cobra.Command {
	var ef eachFlags
	cmd := &cobra.Command{
		Use:   "each <project> [--] <command> [args...]",
		Short: "Run a command in every worktree of a project",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.loadProject(args[0])
			if err != nil {
				return err
			}
			return a.runIn(cmd.Context(), a.locals(p), &ef, args[1:])
		},
	}
	cmd.Flags().SetInterspersed(false)
	ef.bind(cmd)
	return cmd
}

func newWorkGitCmd(a *app) *cobra.Command {
	var ef eachFlags
	cmd := &cobra.Command{
		Use:   "git <project> <git-args...>",
		Short: "Run git in every worktree of a project",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := a.loadProject(args[0])
			if err != nil {
				return err
			}
			return a.runIn(cmd.Context(), a.locals(p), &ef, append([]string{"git"}, args[1:]...))
		},
	}
	cmd.Flags().SetInterspersed(false)
	ef.bind(cmd)
	return cmd
}

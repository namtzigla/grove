package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newCreateCmd(a *app) *cobra.Command {
	var desc, visibility, defaultBranch, pushFrom string
	var initReadme, noClone, asJSON bool
	cmd := &cobra.Command{
		Use:   "create <spec>",
		Short: "Create a repository on GitHub / GitLab and clone it into place (or push an existing directory to it)",
		Long: `Create a repository named by <spec> (group/project on the default host,
host/group/project, or an URL-style spec), then clone it into
<root>/<host>/<path>.

  grove create acme/new-service --desc "..." --visibility internal --init
  grove create github.com/you/tool --visibility public --push-from ~/src/tool

Per-host defaults for new repositories come from the config:
  "hosts": {"gitlab.example.com": {"kind": "gitlab",
            "new_repo": {"shared_runners_enabled": true},
            "inbound_job_token_scope": false}}
new_repo fields are sent as-is with the create call; inbound_job_token_scope
(gitlab) is applied right after creation.

--push-from DIR adds the new repo as "origin" of an existing checkout and
pushes its current branch, instead of cloning. Move it into the layout
afterwards with "grove relocate --from <parent of DIR>".

Prints "<host/path>\t<web url>" (JSON with --json) and the clone status.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			r, err := ParseSpec(args[0], a.cfg, a.cfg.DefaultHost)
			if err != nil {
				return err
			}
			if pushFrom != "" && initReadme {
				return fmt.Errorf("--push-from and --init are exclusive (the push would not be a fast-forward)")
			}
			if pushFrom != "" {
				pushFrom = expandHome(pushFrom)
				if !isGitRepo(pushFrom) {
					return fmt.Errorf("%s is not a git checkout", pushFrom)
				}
				if u, err := originURL(pushFrom); err == nil {
					return fmt.Errorf("%s already has an origin remote (%s)", pushFrom, u)
				}
			}
			switch visibility {
			case "private", "internal", "public":
			default:
				return fmt.Errorf("--visibility must be private, internal or public")
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			if local := r.Dir(a.root()); isGitRepo(local) && pushFrom == "" {
				return fmt.Errorf("%s already exists locally; use --push-from %s if it is the content for the new repo", local, local)
			}
			created, err := f.CreateRepo(ctx, r, RepoOpts{Description: desc, Visibility: visibility, Init: initReadme, DefaultBranch: defaultBranch})
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(os.Stdout).Encode(created); err != nil {
					return err
				}
			} else {
				fmt.Printf("created\t%s\t%s\n", created.Full(), created.WebURL)
			}
			cloneURL := created.Repo.CloneURL(a.cfg.Protocol(r.Host))
			if pushFrom != "" {
				wt := LocalRepo{Repo: created.Repo, Dir: pushFrom}
				if out, err := gitIn(ctx, wt, "remote", "add", "origin", cloneURL); err != nil {
					return fmt.Errorf("git remote add: %s", lastLine(string(out)))
				}
				if out, err := gitIn(ctx, wt, "push", "-u", "origin", "HEAD"); err != nil {
					return fmt.Errorf("git push: %s", lastLine(string(out)))
				}
				fmt.Printf("pushed\t%s\t%s\n", created.Full(), pushFrom)
				if canonical := created.Repo.Dir(a.root()); strings.TrimRight(pushFrom, "/") != canonical {
					fmt.Fprintf(os.Stderr, "note: canonical location is %s (grove relocate --from %s --apply)\n", canonical, dirOf(pushFrom))
				}
				return nil
			}
			if noClone {
				return nil
			}
			lr := LocalRepo{Repo: created.Repo, Dir: created.Repo.Dir(a.root())}
			status, err := a.cloneOne(ctx, lr, &cloneFlags{})
			if err != nil {
				return err
			}
			fmt.Printf("%s\t%s\t%s\n", strings.TrimSpace(string(status)), created.Full(), lr.Dir)
			return nil
		},
	}
	cmd.Flags().StringVar(&desc, "desc", "", "description")
	cmd.Flags().StringVar(&visibility, "visibility", "private", "private | internal (gitlab) | public")
	cmd.Flags().StringVar(&defaultBranch, "default-branch", "", "default branch name (with --init)")
	cmd.Flags().BoolVar(&initReadme, "init", false, "initialize with a README so the default branch exists")
	cmd.Flags().StringVar(&pushFrom, "push-from", "", "existing checkout to push as the initial content (instead of cloning)")
	cmd.Flags().BoolVar(&noClone, "no-clone", false, "do not clone after creating")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the created repo as JSON")
	return cmd
}

func dirOf(p string) string {
	p = strings.TrimRight(p, "/")
	if i := strings.LastIndex(p, "/"); i > 0 {
		return p[:i]
	}
	return "."
}

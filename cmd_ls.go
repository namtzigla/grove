package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newLsCmd(a *app) *cobra.Command {
	var sel selectFlags
	var abs, asJSON, nul bool
	cmd := &cobra.Command{
		Use:   "ls [pattern...]",
		Short: "List cloned repositories (host/path, one per line)",
		Long: `List the repositories cloned under the root.

Patterns select by prefix or glob against "host/path" or "path":
  grove ls acme                       everything under acme/ on any host
  grove ls gitlab.example.com/infra/deploy
  grove ls '**/api' -H github.com
  grove ls -m 'tts|stt'`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := a.selectLocal(&sel, args)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			for _, r := range repos {
				switch {
				case asJSON:
					if err := enc.Encode(r); err != nil {
						return err
					}
				case abs:
					writeLine(r.Dir, nul)
				default:
					writeLine(r.Full(), nul)
				}
			}
			return nil
		},
	}
	sel.bind(cmd.Flags())
	cmd.Flags().BoolVar(&abs, "abs", false, "print absolute directories")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per line ({host,path,dir})")
	cmd.Flags().BoolVarP(&nul, "null", "0", false, "terminate lines with NUL (for xargs -0)")
	return cmd
}

func writeLine(s string, nul bool) {
	if nul {
		fmt.Fprint(os.Stdout, s, "\x00")
	} else {
		fmt.Fprintln(os.Stdout, s)
	}
}

// remoteFlags are shared by ls-remote and sync.
type remoteFlags struct {
	all       bool
	archived  bool
	noExclude bool
}

func (r *remoteFlags) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&r.all, "all", false, "gitlab: every visible project, not only ones you are a member of")
	cmd.Flags().BoolVar(&r.archived, "archived", false, "include archived repositories")
	cmd.Flags().BoolVar(&r.noExclude, "no-exclude", false, "ignore the exclude list from the config")
}

// selectRemote enumerates the selected hosts and applies filters + config excludes.
func (a *app) selectRemote(cmd *cobra.Command, sel *selectFlags, rf *remoteFlags, positional []string) ([]RemoteRepo, error) {
	hosts, err := a.remoteHosts(sel.hosts)
	if err != nil {
		return nil, err
	}
	f, err := sel.filter(positional)
	if err != nil {
		return nil, err
	}
	if !rf.noExclude {
		f.Exclude = append(f.Exclude, a.cfg.Exclude...)
	}
	a.logf("listing %v", hosts)
	all, err := ListRemoteAll(cmd.Context(), a.cfg, hosts, RemoteOpts{All: rf.all, Archived: rf.archived})
	if err != nil {
		return nil, err
	}
	var out []RemoteRepo
	for _, r := range all {
		if f.Match(r.Repo) {
			out = append(out, r)
		}
	}
	a.logf("selected %d of %d remote repos", len(out), len(all))
	return out, nil
}

func newLsRemoteCmd(a *app) *cobra.Command {
	var sel selectFlags
	var rf remoteFlags
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "ls-remote [pattern...]",
		Short: "List repositories you have access to on the configured hosts (via gh / glab)",
		Long: `Enumerate remote repositories with gh (github kind) and glab (gitlab kind).

GitHub: repositories you own, collaborate on, or that belong to your orgs.
GitLab: projects you are a member of (directly or via a group); --all lists
every project visible to you instead.

Pipe into clone:  grove ls-remote -H gitlab.example.com acme | grove clone -`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := a.selectRemote(cmd, &sel, &rf, args)
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			for _, r := range repos {
				if asJSON {
					if err := enc.Encode(r); err != nil {
						return err
					}
					continue
				}
				fmt.Println(r.Full())
			}
			return nil
		},
	}
	sel.bind(cmd.Flags())
	cmd.Flags().MarkHidden("stdin")
	rf.bind(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per line")
	return cmd
}

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

func newSyncCmd(a *app) *cobra.Command {
	var sel selectFlags
	var rf remoteFlags
	var cf cloneFlags
	var fetch, deleteStale bool
	cmd := &cobra.Command{
		Use:   "sync [pattern...] | -",
		Short: "Clone every remote repository that is missing locally (and optionally fetch the rest)",
		Long: `Enumerate the configured hosts (see ls-remote), clone what is not yet on
disk and, with --fetch, run git fetch --prune in clones that already exist.
Work starts as soon as the first page of results arrives and runs -P at a
time (default --jobs).

With "-" (or --stdin) the repo list is streamed from stdin instead of
being enumerated, one spec per line (URLs, host/path or group/project):
  grove ls-remote acme | grep -v legacy | grove sync --fetch -
  grove sync -P 16 - < repos.txt

Local clones that the enumeration no longer lists are reported as "stale"
(not computed in stdin mode). Nothing is deleted unless --delete-stale is given.

  grove sync -H gitlab.example.com            clone everything you are a member of
  grove sync acme payments --fetch               clone + fetch --prune two groups
  grove sync -n                             dry run

One line per repo on stdout: "<cloned|exists|fetched|stale|deleted|failed|would-clone|would-fetch>\t<host/path>".`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			useStdin := sel.stdin
			var pats []string
			for _, p := range args {
				if p == "-" {
					useStdin = true
				} else {
					pats = append(pats, p)
				}
			}
			f, err := sel.filter(pats)
			if err != nil {
				return err
			}
			if !rf.noExclude {
				f.Exclude = append(f.Exclude, a.cfg.Exclude...)
			}

			// Source: streamed enumeration of the hosts, or stdin.
			src := make(chan Repo)
			srcErr := make(chan error, 1)
			var hosts []string
			if useStdin {
				specs, parseErrs := a.specSource(ctx, []string{"-"}, os.Stdin, a.cfg.DefaultHost)
				go func() {
					defer close(src)
					for r := range specs {
						if f.Match(r) {
							src <- r
						}
					}
					if bad := <-parseErrs; bad > 0 {
						srcErr <- fmt.Errorf("%d specs unparsable", bad)
					}
					close(srcErr)
				}()
			} else {
				hosts, err = a.remoteHosts(sel.hosts)
				if err != nil {
					return err
				}
				a.logf("enumerating %v", hosts)
				go func() {
					defer close(src)
					err := ListRemoteStream(ctx, a.cfg, hosts, RemoteOpts{All: rf.all, Archived: rf.archived}, func(r RemoteRepo) {
						if f.Match(r.Repo) {
							src <- r.Repo
						}
					})
					if err != nil {
						srcErr <- err
					}
					close(srcErr)
				}()
			}

			// Workers: clone or fetch each repo as it arrives.
			var (
				mu     sync.Mutex
				wg     sync.WaitGroup
				seen   = map[string]bool{}
				counts = map[string]int{}
			)
			report := func(r Repo, status string, err error) {
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					counts["failed"]++
					fmt.Printf("failed\t%s\n", r.Full())
					fmt.Fprintf(os.Stderr, "%s: %v\n", r.Full(), err)
					return
				}
				counts[status]++
				fmt.Printf("%s\t%s\n", status, r.Full())
			}
			for i := 0; i < cf.jobs(a); i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for r := range src {
						mu.Lock()
						seen[r.Full()] = true
						mu.Unlock()
						lr := LocalRepo{Repo: r, Dir: r.Dir(a.root())}
						switch {
						case !isGitRepo(lr.Dir):
							out, err := a.cloneOne(ctx, lr, &cf)
							report(r, strings.TrimSpace(string(out)), err)
						case fetch && cf.dryRun:
							report(r, "would-fetch", nil)
						case fetch:
							out, err := gitIn(ctx, lr, "fetch", "--prune", "--quiet")
							if err != nil {
								err = fmt.Errorf("git fetch: %s", lastLine(string(out)))
							}
							report(r, "fetched", err)
						default:
							report(r, "exists", nil)
						}
					}
				}()
			}
			wg.Wait()
			sourceErr := <-srcErr
			if sourceErr != nil {
				fmt.Fprintf(os.Stderr, "sync: %v\n", sourceErr)
				counts["failed"]++
			}

			// Stale: local clones on the enumerated hosts that were not listed.
			// Skipped when enumeration failed (a partial list would mark everything stale).
			if !useStdin && sourceErr == nil {
				local, err := a.selectLocal(&sel, pats)
				if err != nil {
					return err
				}
				for _, l := range local {
					if seen[l.Full()] || !containsStr(hosts, l.Host) {
						continue
					}
					if deleteStale && !cf.dryRun {
						if err := os.RemoveAll(l.Dir); err != nil {
							report(l.Repo, "", err)
							continue
						}
						removeEmptyParents(l.Dir, filepath.Join(a.root(), l.Host))
						report(l.Repo, "deleted", nil)
					} else {
						report(l.Repo, "stale", nil)
					}
				}
			}
			fmt.Fprintf(os.Stderr, "sync: %d repos: %s\n", len(seen), summarize(counts))
			if counts["failed"] > 0 {
				return errSomeFailed
			}
			return nil
		},
	}
	sel.bind(cmd.Flags())
	rf.bind(cmd)
	cf.bind(cmd)
	cmd.Flags().BoolVar(&fetch, "fetch", false, "also run git fetch --prune in clones that already exist")
	cmd.Flags().BoolVar(&deleteStale, "delete-stale", false, "DELETE local clones that the remote no longer lists (use with care; try -n first)")
	return cmd
}

func summarize(counts map[string]int) string {
	order := []string{"cloned", "would-clone", "fetched", "would-fetch", "exists", "stale", "deleted", "failed"}
	var parts []string
	for _, k := range order {
		if counts[k] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[k], k))
		}
	}
	if len(parts) == 0 {
		return "nothing to do"
	}
	return strings.Join(parts, ", ")
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// removeEmptyParents removes empty directories from dir's parent up to (not including) stop.
func removeEmptyParents(dir, stop string) {
	for p := filepath.Dir(dir); p != stop && strings.HasPrefix(p, stop); p = filepath.Dir(p) {
		if err := os.Remove(p); err != nil { // fails when not empty
			return
		}
	}
}

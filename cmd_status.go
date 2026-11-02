package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

type repoStatus struct {
	Repo      string `json:"repo"`
	Branch    string `json:"branch"`
	Upstream  string `json:"upstream,omitempty"`
	Ahead     int    `json:"ahead"`
	Behind    int    `json:"behind"`
	Modified  int    `json:"modified"`
	Untracked int    `json:"untracked"`
	Error     string `json:"error,omitempty"`
}

func (s repoStatus) interesting() bool {
	return s.Error != "" || s.Ahead > 0 || s.Behind > 0 || s.Modified > 0 || s.Untracked > 0 || s.Branch == "(detached)"
}

func newStatusCmd(a *app) *cobra.Command {
	var sel selectFlags
	var dirty, asJSON bool
	cmd := &cobra.Command{
		Use:   "status [pattern...]",
		Short: "Show branch, ahead/behind and dirty state of each selected repository",
		Long: `One line per repository: host/path, branch, ahead/behind its upstream,
modified and untracked file counts. --dirty hides clean, in-sync repos.
Counts come from "git status --porcelain=v2 --branch" (no fetch is done).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			repos, err := a.selectLocal(&sel, args)
			if err != nil {
				return err
			}
			return a.printStatus(cmd.Context(), repos, dirty, asJSON)
		},
	}
	sel.bind(cmd.Flags())
	cmd.Flags().BoolVar(&dirty, "dirty", false, "only show repos that are dirty, ahead/behind, detached or errored")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print one JSON object per line")
	return cmd
}

// printStatus prints the status table (or NDJSON) for the given checkouts.
func (a *app) printStatus(ctx context.Context, repos []LocalRepo, dirty, asJSON bool) error {
	{
		{
			var rows []repoStatus
			forEach(ctx, repos, a.jobs, func(ctx context.Context, r LocalRepo) ([]byte, error) {
				return gitIn(ctx, r, "status", "--porcelain=v2", "--branch")
			}, func(res Result) bool {
				st := parseStatus(res.Repo.Full(), res.Out)
				if res.Err != nil {
					st.Error = lastLine(string(res.Out))
					if st.Error == "" {
						st.Error = res.Err.Error()
					}
				}
				rows = append(rows, st)
				return false
			})
			sortStatus(rows)
			enc := json.NewEncoder(os.Stdout)
			tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
			for _, s := range rows {
				if dirty && !s.interesting() {
					continue
				}
				if asJSON {
					if err := enc.Encode(s); err != nil {
						return err
					}
					continue
				}
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", s.Repo, s.Branch, s.syncCol(), s.dirtyCol())
			}
			return tw.Flush()
		}
	}
}

func (s repoStatus) syncCol() string {
	if s.Error != "" {
		return "error: " + s.Error
	}
	if s.Upstream == "" {
		return "no-upstream"
	}
	if s.Ahead == 0 && s.Behind == 0 {
		return "="
	}
	return fmt.Sprintf("+%d/-%d", s.Ahead, s.Behind)
}

func (s repoStatus) dirtyCol() string {
	if s.Modified == 0 && s.Untracked == 0 {
		return "clean"
	}
	var parts []string
	if s.Modified > 0 {
		parts = append(parts, fmt.Sprintf("M%d", s.Modified))
	}
	if s.Untracked > 0 {
		parts = append(parts, fmt.Sprintf("?%d", s.Untracked))
	}
	return strings.Join(parts, " ")
}

func sortStatus(rows []repoStatus) {
	for i := 1; i < len(rows); i++ { // small, stable insertion sort by repo
		for j := i; j > 0 && rows[j].Repo < rows[j-1].Repo; j-- {
			rows[j], rows[j-1] = rows[j-1], rows[j]
		}
	}
}

// parseStatus parses `git status --porcelain=v2 --branch` output.
func parseStatus(repo string, out []byte) repoStatus {
	st := repoStatus{Repo: repo}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			st.Branch = strings.TrimPrefix(line, "# branch.head ")
		case strings.HasPrefix(line, "# branch.upstream "):
			st.Upstream = strings.TrimPrefix(line, "# branch.upstream ")
		case strings.HasPrefix(line, "# branch.ab "):
			f := strings.Fields(strings.TrimPrefix(line, "# branch.ab "))
			if len(f) == 2 {
				st.Ahead, _ = strconv.Atoi(strings.TrimPrefix(f[0], "+"))
				st.Behind, _ = strconv.Atoi(strings.TrimPrefix(f[1], "-"))
			}
		case strings.HasPrefix(line, "? "):
			st.Untracked++
		case strings.HasPrefix(line, "1 "), strings.HasPrefix(line, "2 "), strings.HasPrefix(line, "u "):
			st.Modified++
		}
	}
	return st
}

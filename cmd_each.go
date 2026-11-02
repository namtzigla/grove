package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

type eachFlags struct {
	quiet    bool
	noHeader bool
	failFast bool
	dryRun   bool
}

func (e *eachFlags) bind(cmd *cobra.Command) {
	cmd.Flags().BoolVarP(&e.quiet, "quiet", "q", false, "only print output of repos where the command failed")
	cmd.Flags().BoolVar(&e.noHeader, "no-header", false, "do not print the '== host/path ==' header before each repo's output")
	cmd.Flags().BoolVar(&e.failFast, "fail-fast", false, "stop scheduling new repos after the first failure")
	cmd.Flags().BoolVarP(&e.dryRun, "dry-run", "n", false, "print the repos that would be visited")
}

func newEachCmd(a *app) *cobra.Command {
	var sel selectFlags
	var ef eachFlags
	cmd := &cobra.Command{
		Use:   "each [flags] [--] <command> [args...]",
		Short: "Run a command in every selected repository (cwd = the repo)",
		Long: `Run an arbitrary command in each selected clone, in parallel (-j).
Selection flags must come before the command. The command gets these
environment variables: GROVE_HOST, GROVE_PATH, GROVE_NAME, GROVE_DIR, GROVE_FULL.

  grove each -p acme -- git fetch --prune
  grove each -H github.com -- sh -c 'git rev-parse --abbrev-ref HEAD'
  grove ls -m 'tts' | grove each --stdin -- go build ./...

Output of each repo is printed as one block, prefixed by "== host/path ==".
Exit status is 1 if the command failed in any repo; failures are listed on stderr.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runEach(cmd.Context(), &sel, &ef, args)
		},
	}
	cmd.Flags().SetInterspersed(false)
	sel.bind(cmd.Flags())
	ef.bind(cmd)
	return cmd
}

func newGitCmd(a *app) *cobra.Command {
	var sel selectFlags
	var ef eachFlags
	cmd := &cobra.Command{
		Use:   "git [flags] [--] <git-args...>",
		Short: "Run a git command in every selected repository (shorthand for each -- git ...)",
		Long: `Shorthand for "grove each -- git <args>". Selection flags come first.

  grove git fetch --all --prune
  grove git -p acme -p payments checkout master
  grove git -H gitlab.example.com -q pull --ff-only
  grove git -m 'api|worker' log -1 --oneline`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return a.runEach(cmd.Context(), &sel, &ef, append([]string{"git"}, args...))
		},
	}
	cmd.Flags().SetInterspersed(false)
	sel.bind(cmd.Flags())
	ef.bind(cmd)
	return cmd
}

func (a *app) runEach(ctx context.Context, sel *selectFlags, ef *eachFlags, command []string) error {
	repos, err := a.selectLocal(sel, nil)
	if err != nil {
		return err
	}
	return a.runIn(ctx, repos, ef, command)
}

// runIn runs command in each of the given checkouts (see each / git / work each).
func (a *app) runIn(ctx context.Context, repos []LocalRepo, ef *eachFlags, command []string) error {
	if len(repos) == 0 {
		fmt.Fprintln(os.Stderr, "no repositories selected")
		return nil
	}
	if ef.dryRun {
		for _, r := range repos {
			fmt.Printf("%s\t%s\n", r.Full(), strings.Join(command, " "))
		}
		return nil
	}
	var failed []string
	forEach(ctx, repos, a.jobs, func(ctx context.Context, r LocalRepo) ([]byte, error) {
		return execIn(ctx, r.Dir, repoEnv(r), command[0], command[1:]...)
	}, func(res Result) bool {
		if res.Err != nil {
			failed = append(failed, res.Repo.Full())
		}
		if ef.quiet && res.Err == nil {
			return false
		}
		if !ef.noHeader {
			status := ""
			if res.Err != nil {
				status = "  FAILED: " + res.Err.Error()
			}
			fmt.Printf("== %s ==%s\n", res.Repo.Full(), status)
		}
		if len(res.Out) > 0 {
			os.Stdout.Write(res.Out)
			if res.Out[len(res.Out)-1] != '\n' {
				fmt.Println()
			}
		}
		return ef.failFast && res.Err != nil
	})
	if len(failed) > 0 {
		fmt.Fprintf(os.Stderr, "%d of %d repos failed:\n", len(failed), len(repos))
		for _, f := range failed {
			fmt.Fprintf(os.Stderr, "  %s\n", f)
		}
		return errSomeFailed
	}
	a.logf("%d repos ok", len(repos))
	return nil
}

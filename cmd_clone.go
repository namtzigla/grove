package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/spf13/cobra"
)

type cloneFlags struct {
	host     string
	protocol string
	dryRun   bool
	parallel int
	extra    []string
}

func (c *cloneFlags) bind(cmd *cobra.Command) {
	cmd.Flags().StringVar(&c.protocol, "protocol", "", "clone protocol: ssh or https (default: per-host config, ssh)")
	cmd.Flags().BoolVarP(&c.dryRun, "dry-run", "n", false, "print what would be cloned")
	cmd.Flags().IntVarP(&c.parallel, "parallel", "P", 0, "number of concurrent clones (default: --jobs)")
	cmd.Flags().StringArrayVar(&c.extra, "git-arg", nil, "extra argument passed to git clone, repeatable (e.g. --git-arg=--depth=1)")
}

func (c *cloneFlags) jobs(a *app) int {
	if c.parallel > 0 {
		return c.parallel
	}
	return a.jobs
}

func newCloneCmd(a *app) *cobra.Command {
	var cf cloneFlags
	cmd := &cobra.Command{
		Use:   "clone <spec>... | -",
		Short: "Clone repositories into <root>/<host>/<path> (idempotent, parallel)",
		Long: `Clone one or more repositories, -P at a time (default --jobs). A spec is any of

  git@gitlab.example.com:acme/api.git
  https://github.com/you/project
  github.com/you/project
  acme/api                      (uses --host or default_host from the config)

"-" (or --stdin) reads specs from stdin, one per line. Stdin is streamed:
clones start as soon as lines arrive, so a slow producer does not delay them.
  grove ls-remote acme | grove clone -P 8 -
  cat repos.txt | grove clone --host gitlab.example.com -

Existing clones are reported as "exists" and left alone. One line per repo on
stdout: "<cloned|exists|failed|would-clone>\t<host/path>".`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			stdin, _ := cmd.Flags().GetBool("stdin")
			if stdin && !containsStr(args, "-") {
				args = append(args, "-")
			}
			if len(args) == 0 {
				return fmt.Errorf("no repo specs given (pass specs, or - to read stdin)")
			}
			defHost := cf.host
			if defHost == "" {
				defHost = a.cfg.DefaultHost
			}
			specs, parseErrs := a.specSource(cmd.Context(), args, os.Stdin, defHost)
			return a.cloneAll(cmd.Context(), specs, parseErrs, &cf)
		},
	}
	cmd.Flags().StringVar(&cf.host, "host", "", "host for bare group/project specs")
	cmd.Flags().Bool("stdin", false, "read specs from stdin (same as a - argument)")
	cf.bind(cmd)
	return cmd
}

// specSource yields specs from args and, for "-", from stdin as lines arrive.
// Duplicates are dropped; unparsable lines are counted on parseErrs and
// reported on stderr. Both channels close when the input is exhausted.
func (a *app) specSource(ctx context.Context, args []string, stdin io.Reader, defHost string) (<-chan Repo, <-chan int) {
	out := make(chan Repo)
	errc := make(chan int, 1)
	go func() {
		defer close(out)
		defer close(errc)
		seen := map[string]bool{}
		bad := 0
		emit := func(raw string) {
			if strings.HasPrefix(raw, "{") { // NDJSON from --json output
				var r Repo
				if err := jsonUnmarshal([]byte(raw), &r); err == nil && r.Host != "" && r.Path != "" {
					raw = r.Full()
				}
			}
			field := strings.Fields(raw)
			if len(field) == 0 {
				return
			}
			r, err := ParseSpec(field[0], a.cfg, defHost)
			if err != nil {
				bad++
				fmt.Fprintf(os.Stderr, "skip: %v\n", err)
				return
			}
			if seen[r.Full()] {
				return
			}
			seen[r.Full()] = true
			select {
			case out <- r:
			case <-ctx.Done():
			}
		}
		for _, arg := range args {
			if arg != "-" {
				emit(arg)
				continue
			}
			sc := bufio.NewScanner(stdin)
			for sc.Scan() {
				if ctx.Err() != nil {
					break
				}
				line := strings.TrimSpace(sc.Text())
				if line == "" || strings.HasPrefix(line, "#") {
					continue
				}
				emit(line)
			}
			if err := sc.Err(); err != nil {
				bad++
				fmt.Fprintf(os.Stderr, "read stdin: %v\n", err)
			}
		}
		errc <- bad
	}()
	return out, errc
}

// cloneAll clones every spec from the channel with a pool of cf.jobs workers,
// starting each clone as soon as its spec is available. Returns errSomeFailed
// if any clone (or spec parse) failed.
func (a *app) cloneAll(ctx context.Context, specs <-chan Repo, parseErrs <-chan int, cf *cloneFlags) error {
	jobs := cf.jobs(a)
	a.logf("cloning with %d parallel jobs", jobs)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		total  int
		failed int
	)
	for i := 0; i < jobs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range specs {
				lr := LocalRepo{Repo: r, Dir: r.Dir(a.root())}
				status, err := a.cloneOne(ctx, lr, cf)
				mu.Lock()
				total++
				if err != nil {
					failed++
					fmt.Printf("failed\t%s\n", r.Full())
					fmt.Fprintf(os.Stderr, "%s: %v\n", r.Full(), err)
				} else {
					fmt.Printf("%s\t%s\n", strings.TrimSpace(string(status)), r.Full())
				}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	bad := <-parseErrs
	if failed > 0 || bad > 0 {
		fmt.Fprintf(os.Stderr, "clone: %d of %d failed, %d specs unparsable\n", failed, total, bad)
		return errSomeFailed
	}
	a.logf("clone: %d repos, all ok", total)
	return nil
}

// cloneOne returns a status word: exists | would-clone | cloned.
func (a *app) cloneOne(ctx context.Context, r LocalRepo, cf *cloneFlags) ([]byte, error) {
	if isGitRepo(r.Dir) {
		return []byte("exists"), nil
	}
	if entries, err := os.ReadDir(r.Dir); err == nil && len(entries) > 0 {
		return nil, fmt.Errorf("%s exists and is not a git checkout", r.Dir)
	}
	proto := cf.protocol
	if proto == "" {
		proto = a.cfg.Protocol(r.Host)
	}
	url := r.CloneURL(proto)
	if cf.dryRun {
		return []byte("would-clone"), nil
	}
	if err := os.MkdirAll(filepath.Dir(r.Dir), 0o755); err != nil {
		return nil, err
	}
	args := append([]string{"clone", "--quiet"}, cf.extra...)
	args = append(args, url, r.Dir)
	out, err := execIn(ctx, filepath.Dir(r.Dir), []string{"GIT_TERMINAL_PROMPT=0"}, "git", args...)
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("git clone %s: %s", url, lastLine(msg))
	}
	return []byte("cloned"), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

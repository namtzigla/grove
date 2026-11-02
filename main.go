// grove manages a collection of git clones laid out as
// <root>/<host>/<group...>/<project>, e.g. ~/dev/github.com/you/project
// or ~/dev/gitlab.example.com/acme/api.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"runtime"
	"syscall"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var version = "dev"

// exitError carries a process exit code without printing anything further.
type exitError struct{ code int }

func (e exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

var errSomeFailed = exitError{1}

type app struct {
	cfg      *Config
	cfgPath  string
	rootFlag string
	jobs     int
	verbose  bool
}

func (a *app) root() string { return a.cfg.Root }

func (a *app) logf(format string, args ...any) {
	if a.verbose {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{}
	cmd := newRootCmd(a)
	err := cmd.ExecuteContext(ctx)
	if err == nil {
		return
	}
	var ee exitError
	if errors.As(err, &ee) {
		os.Exit(ee.code)
	}
	fmt.Fprintln(os.Stderr, "grove:", err)
	os.Exit(2)
}

func newRootCmd(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "grove",
		Short: "Manage a tree of git clones laid out as <root>/<host>/<group>/<project>",
		Long: `grove keeps every clone under one root, keyed by host and namespace:

  ~/dev/github.com/<owner>/<project>
  ~/dev/gitlab.example.com/<group>/<subgroup>/<project>

It enumerates remotes with gh / glab, clones what is missing, and runs git
(or any command) across all or a subset of the local clones.

Output of ls / ls-remote / status is one repo per line and can be piped back
into clone / each / git / status via "-" or --stdin. Diagnostics go to stderr.
Exit status: 0 ok, 1 some repositories failed, 2 usage or setup error.

Config: $XDG_CONFIG_HOME/grove/config.json (or $GROVE_CONFIG), e.g.
  {"root": "~/dev", "work_root": "~/dev/work", "default_host": "gitlab.example.com",
   "hosts": {"github.com": {"kind": "github"},
             "gitlab.example.com": {"kind": "gitlab", "protocol": "ssh"}},
   "exclude": ["gitlab.example.com/archive/**"]}
Environment: GROVE_ROOT overrides root, GROVE_WORK_ROOT overrides work_root,
GITLAB_HOST adds a gitlab host.`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Name() == "completion" || cmd.Name() == "help" {
				return nil
			}
			if a.cfgPath == "" {
				a.cfgPath = defaultConfigPath()
			}
			cfg, err := LoadConfig(a.cfgPath)
			if err != nil {
				return err
			}
			if a.rootFlag != "" {
				cfg.Root = expandHome(a.rootFlag)
			}
			a.cfg = cfg
			a.logf("root=%s config=%s hosts=%v", cfg.Root, a.cfgPath, cfg.HostNames())
			return nil
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.rootFlag, "root", "", "root directory for clones (default: config root, GROVE_ROOT, or ~/dev)")
	pf.StringVar(&a.cfgPath, "config", "", "config file (default: $XDG_CONFIG_HOME/grove/config.json)")
	pf.IntVarP(&a.jobs, "jobs", "j", defaultJobs(), "parallel jobs")
	pf.BoolVarP(&a.verbose, "verbose", "v", false, "verbose diagnostics on stderr")

	root.AddCommand(
		newLsCmd(a),
		newLsRemoteCmd(a),
		newCloneCmd(a),
		newCreateCmd(a),
		newSyncCmd(a),
		newEachCmd(a),
		newGitCmd(a),
		newStatusCmd(a),
		newPathCmd(a),
		newRelocateCmd(a),
		newConfigCmd(a),
		newWorkCmd(a),
		newIssueCmd(a),
		newMRCmd(a),
		newCICmd(a),
		newContextCmd(a),
		newSkillCmd(a),
	)
	return root
}

func defaultJobs() int {
	n := runtime.NumCPU()
	if n > 16 {
		n = 16
	}
	if n < 4 {
		n = 4
	}
	return n
}

// selectFlags are the repository selection flags shared by local commands.
type selectFlags struct {
	patterns []string
	exclude  []string
	hosts    []string
	regex    string
	stdin    bool
}

func (s *selectFlags) bind(fs *pflag.FlagSet) {
	fs.StringArrayVarP(&s.patterns, "pattern", "p", nil, "select repos by prefix or glob (host/path or path), repeatable")
	fs.StringArrayVarP(&s.exclude, "exclude", "x", nil, "exclude repos by prefix or glob, repeatable")
	fs.StringArrayVarP(&s.hosts, "host", "H", nil, "restrict to host(s), repeatable")
	fs.StringVarP(&s.regex, "match", "m", "", "select repos whose host/path matches a regex")
	fs.BoolVar(&s.stdin, "stdin", false, "read the repo list from stdin (one spec per line) instead of scanning")
}

func (s *selectFlags) filter(extraPatterns []string) (*Filter, error) {
	f := &Filter{Patterns: append(append([]string{}, s.patterns...), extraPatterns...), Exclude: s.exclude, Hosts: s.hosts}
	if s.regex != "" {
		re, err := regexp.Compile(s.regex)
		if err != nil {
			return nil, fmt.Errorf("--match: %w", err)
		}
		f.Regex = re
	}
	return f, nil
}

// selectLocal returns the local repositories selected by flags and positional
// patterns. With --stdin (or a "-" pattern) the list comes from stdin instead
// of a scan; specs that are not cloned are reported on stderr and skipped.
func (a *app) selectLocal(s *selectFlags, positional []string) ([]LocalRepo, error) {
	useStdin := s.stdin
	var pats []string
	for _, p := range positional {
		if p == "-" {
			useStdin = true
		} else {
			pats = append(pats, p)
		}
	}
	f, err := s.filter(pats)
	if err != nil {
		return nil, err
	}
	var repos []LocalRepo
	if useStdin {
		specs, err := readSpecs(os.Stdin, a.cfg, a.cfg.DefaultHost)
		if err != nil {
			return nil, err
		}
		for _, r := range specs {
			dir := r.Dir(a.root())
			if !isGitRepo(dir) {
				fmt.Fprintf(os.Stderr, "skip %s: not cloned (%s)\n", r.Full(), dir)
				continue
			}
			repos = append(repos, LocalRepo{Repo: r, Dir: dir})
		}
	} else {
		repos, err = ScanLocal(a.root(), a.cfg, s.hosts)
		if err != nil {
			return nil, err
		}
	}
	var out []LocalRepo
	for _, r := range repos {
		if f.Match(r.Repo) {
			out = append(out, r)
		}
	}
	a.logf("selected %d of %d local repos", len(out), len(repos))
	return out, nil
}

// remoteHosts resolves --host to the hosts to enumerate (default: all configured).
func (a *app) remoteHosts(hosts []string) ([]string, error) {
	if len(hosts) == 0 {
		hosts = a.cfg.HostNames()
	}
	for _, h := range hosts {
		if a.cfg.HostKind(h) == "" {
			return nil, fmt.Errorf("host %s: unknown kind; add it to %s with \"kind\": \"github\" or \"gitlab\"", h, a.cfgPath)
		}
	}
	return hosts, nil
}

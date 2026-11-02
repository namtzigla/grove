package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func newCICmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ci",
		Short: "Pipeline / checks status for a branch or MR",
	}
	cmd.AddCommand(newCIStatusCmd(a, false), newCIStatusCmd(a, true))
	return cmd
}

func (a *app) pipelineSource(ctx context.Context, repoSpec, ref string, mr int) (forge, Repo, func() (*Pipeline, error), error) {
	c, err := a.detectContext("")
	if err != nil {
		return nil, Repo{}, nil, err
	}
	var r Repo
	if repoSpec != "" {
		if r, err = ParseSpec(repoSpec, a.cfg, a.cfg.DefaultHost); err != nil {
			return nil, Repo{}, nil, err
		}
	} else if c.Repo != nil {
		r = *c.Repo
	} else {
		return nil, Repo{}, nil, fmt.Errorf("not in a checkout; pass --repo")
	}
	f, err := a.forge(r.Host)
	if err != nil {
		return nil, Repo{}, nil, err
	}
	if mr == 0 && ref == "" && c.Work != nil && c.Work.MR > 0 && c.Repo != nil && c.Repo.Full() == r.Full() {
		mr = c.Work.MR
	}
	if mr > 0 {
		return f, r, func() (*Pipeline, error) { return f.MRPipeline(ctx, r, mr) }, nil
	}
	if ref == "" {
		ref = c.Branch
	}
	if ref == "" {
		return nil, Repo{}, nil, fmt.Errorf("no branch; pass --ref or --mr")
	}
	return f, r, func() (*Pipeline, error) { return f.Pipeline(ctx, r, ref) }, nil
}

func newCIStatusCmd(a *app, wait bool) *cobra.Command {
	var repoSpec, ref string
	var mr int
	var asJSON bool
	var timeout, interval time.Duration
	use, short := "status", "Show the latest pipeline for a branch (default: current) or --mr N"
	if wait {
		use, short = "wait", "Poll until the pipeline finishes; exit 0 only on success"
	}
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			f, r, get, err := a.pipelineSource(ctx, repoSpec, ref, mr)
			if err != nil {
				return err
			}
			var p *Pipeline
			if wait {
				p, err = waitPipeline(ctx, f, r, get, timeout, interval)
			} else {
				p, err = get()
			}
			if err != nil {
				return err
			}
			if asJSON {
				if err := json.NewEncoder(os.Stdout).Encode(p); err != nil {
					return err
				}
			} else {
				fmt.Printf("%s\t%s\t%s\t%s\n", p.Status, p.Ref, shortSHA(p.SHA), p.URL)
			}
			if wait && p.Status != "success" {
				return errSomeFailed
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repoSpec, "repo", "", "repository (default: current checkout)")
	cmd.Flags().StringVar(&ref, "ref", "", "branch or sha (default: current branch)")
	cmd.Flags().IntVar(&mr, "mr", 0, "MR / PR number instead of a ref")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	if wait {
		cmd.Flags().DurationVar(&timeout, "timeout", 45*time.Minute, "give up after this long")
		cmd.Flags().DurationVar(&interval, "interval", 15*time.Second, "poll interval")
	}
	return cmd
}

// waitPipeline polls get until the pipeline is terminal or timeout elapses.
// Progress goes to stderr.
func waitPipeline(ctx context.Context, f forge, r Repo, get func() (*Pipeline, error), timeout, interval time.Duration) (*Pipeline, error) {
	deadline := time.Now().Add(timeout)
	last := ""
	for {
		p, err := get()
		if err != nil {
			return nil, err
		}
		if p.Status == "none" {
			// Give CI a moment to register a pipeline after a push.
			if time.Now().After(deadline) {
				return p, nil
			}
		} else if p.Terminal() {
			fmt.Fprintf(os.Stderr, "pipeline %s: %s\n", p.Status, p.URL)
			return p, nil
		}
		if p.Status != last {
			fmt.Fprintf(os.Stderr, "pipeline %s %s\n", p.Status, p.URL)
			last = p.Status
		}
		if time.Now().After(deadline) {
			return p, fmt.Errorf("timed out after %s waiting for pipeline (%s)", timeout, p.Status)
		}
		select {
		case <-ctx.Done():
			return p, ctx.Err()
		case <-time.After(interval):
		}
	}
}

func shortSHA(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

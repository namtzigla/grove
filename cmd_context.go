package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newContextCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "context [dir]",
		Short: "Describe the current checkout: repo, branch, worktree, work project, issue, MR",
		Long: `Orientation for scripts and AI agents started inside a checkout or worktree.
Prints key/value lines, or one JSON object with --json:
  repo, branch, is_worktree, main_dir, project, work.{issue,mr,mr_url,...}`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.detectContext(first(args))
			if err != nil {
				return err
			}
			if asJSON {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(c)
			}
			if c.Toplevel == "" {
				fmt.Println("repo\t(not in a git checkout)")
				return nil
			}
			if c.Repo == nil {
				fmt.Println("repo\t(checkout without an origin remote)")
			} else {
				fmt.Printf("repo\t%s\n", c.Repo.Full())
			}
			fmt.Printf("dir\t%s\n", c.Toplevel)
			fmt.Printf("branch\t%s\n", c.Branch)
			fmt.Printf("worktree\t%v\n", c.IsWorktree)
			if c.IsWorktree {
				fmt.Printf("main_dir\t%s\n", c.MainDir)
			}
			if c.Project != "" {
				fmt.Printf("project\t%s\n", c.Project)
			}
			if c.Work != nil {
				if c.Work.Issue > 0 {
					fmt.Printf("issue\t#%d\t%s\n", c.Work.Issue, c.Work.IssueURL)
				}
				if c.Work.MR > 0 {
					fmt.Printf("mr\t%d\t%s\n", c.Work.MR, c.Work.MRURL)
				}
				fmt.Printf("base\t%s\n", c.Work.Base)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

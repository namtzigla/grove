package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func newIssueCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue",
		Short: "Issues on GitHub / GitLab with one interface (repo defaults to the current checkout)",
	}
	cmd.AddCommand(newIssueLsCmd(a), newIssueViewCmd(a), newIssueNewCmd(a))
	return cmd
}

func printIssue(i Issue, asJSON bool, withBody bool) error {
	if asJSON {
		if !withBody {
			i.Body = ""
		}
		return json.NewEncoder(os.Stdout).Encode(i)
	}
	fmt.Printf("%s\t%s\t%s\t%s\n", i.Ref, i.State, i.Title, i.URL)
	if withBody && strings.TrimSpace(i.Body) != "" {
		fmt.Println()
		fmt.Println(i.Body)
	}
	return nil
}

func newIssueLsCmd(a *app) *cobra.Command {
	var state string
	var mine, asJSON bool
	var labels []string
	var limit int
	cmd := &cobra.Command{
		Use:   "ls [repo]",
		Short: "List issues",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.contextRepo(first(args))
			if err != nil {
				return err
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			items, err := f.Issues(cmd.Context(), r, IssueListOpts{State: state, Mine: mine, Labels: labels, Limit: limit})
			if err != nil {
				return err
			}
			for _, i := range items {
				if err := printIssue(i, asJSON, false); err != nil {
					return err
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&state, "state", "open", "open | closed | all")
	cmd.Flags().BoolVar(&mine, "mine", false, "only issues I created")
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "filter by label (repeatable)")
	cmd.Flags().IntVar(&limit, "limit", 50, "max results")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON (one object per line)")
	return cmd
}

func newIssueViewCmd(a *app) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "view <N | repo#N | url> | <repo> <N>",
		Short: "Show one issue with its description",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, n, err := a.contextRef(args)
			if err != nil {
				return err
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			i, err := f.Issue(cmd.Context(), r, n)
			if err != nil {
				return err
			}
			return printIssue(*i, asJSON, true)
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newIssueNewCmd(a *app) *cobra.Command {
	var title, body, bodyFile string
	var labels []string
	var asJSON, noAssign bool
	cmd := &cobra.Command{
		Use:   "new [repo] --title T (-F file | --body text)",
		Short: "Create an issue (assigned to you unless --no-assign)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r, err := a.contextRepo(first(args))
			if err != nil {
				return err
			}
			f, err := a.forge(r.Host)
			if err != nil {
				return err
			}
			if bodyFile != "" {
				data, err := readFileOrStdin(bodyFile)
				if err != nil {
					return err
				}
				body = string(data)
			}
			i, err := f.CreateIssue(cmd.Context(), r, title, body, labels, !noAssign)
			if err != nil {
				return err
			}
			return printIssue(*i, asJSON, false)
		},
	}
	cmd.Flags().StringVarP(&title, "title", "t", "", "issue title (required)")
	cmd.Flags().StringVar(&body, "body", "", "description text")
	cmd.Flags().StringVarP(&bodyFile, "body-file", "F", "", "description from a file (- for stdin)")
	cmd.Flags().StringArrayVarP(&labels, "label", "l", nil, "label (repeatable)")
	cmd.Flags().BoolVar(&noAssign, "no-assign", false, "do not assign the issue to me")
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	_ = cmd.MarkFlagRequired("title")
	return cmd
}

func first(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return ""
}

func readFileOrStdin(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func newRelocateCmd(a *app) *cobra.Command {
	var from string
	var apply bool
	var hosts []string
	cmd := &cobra.Command{
		Use:   "relocate [--from DIR] [--apply]",
		Short: "Move existing clones into <root>/<host>/<path> based on their origin URL",
		Long: `Walk DIR (default: the root), read each checkout's origin remote and move
it to the canonical location under the root. Dry run by default; --apply
moves directories (same filesystem rename) and removes emptied parents.

  grove relocate --from ~/src          # preview
  grove relocate --from ~/src --apply

One line per checkout: "<move|ok|skip>\t<src>\t<dst>[\t<reason>]".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if from == "" {
				from = a.root()
			}
			from = expandHome(from)
			var dirs []string
			if err := walkRepos(from, 0, func(d string) { dirs = append(dirs, d) }); err != nil {
				return err
			}
			moved, failed := 0, 0
			for _, src := range dirs {
				url, err := originURL(src)
				if err != nil {
					fmt.Printf("skip\t%s\t-\t%s\n", src, err)
					continue
				}
				r, err := ParseSpec(url, a.cfg, "")
				if err != nil {
					fmt.Printf("skip\t%s\t-\t%s\n", src, err)
					continue
				}
				if len(hosts) > 0 && !containsStr(hosts, r.Host) {
					continue
				}
				dst := r.Dir(a.root())
				if src == dst {
					fmt.Printf("ok\t%s\t%s\n", src, dst)
					continue
				}
				if _, err := os.Stat(dst); err == nil {
					fmt.Printf("skip\t%s\t%s\tdestination exists\n", src, dst)
					continue
				}
				if !apply {
					fmt.Printf("move\t%s\t%s\n", src, dst)
					continue
				}
				if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "%s: %v\n", src, err)
					continue
				}
				if err := os.Rename(src, dst); err != nil {
					failed++
					fmt.Fprintf(os.Stderr, "%s: %v\n", src, err)
					continue
				}
				removeEmptyParents(src, from)
				moved++
				fmt.Printf("moved\t%s\t%s\n", src, dst)
			}
			if apply {
				fmt.Fprintf(os.Stderr, "relocate: %d moved, %d failed\n", moved, failed)
			} else {
				fmt.Fprintln(os.Stderr, "dry run; pass --apply to move")
			}
			if failed > 0 {
				return errSomeFailed
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "directory to scan for checkouts (default: the root)")
	cmd.Flags().BoolVar(&apply, "apply", false, "actually move directories")
	cmd.Flags().StringArrayVarP(&hosts, "host", "H", nil, "only relocate checkouts whose origin is on these host(s)")
	return cmd
}

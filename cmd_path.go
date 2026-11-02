package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newPathCmd(a *app) *cobra.Command {
	var host string
	var mustExist bool
	cmd := &cobra.Command{
		Use:   "path <spec>...",
		Short: "Print the local directory for a repo spec (for cd, scripts)",
		Long: `Resolve specs (URL, host/path or group/project) to local directories.

  cd (grove path acme/api)            # fish
  cd $(grove path git@gitlab.example.com:acme/api.git)`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			defHost := host
			if defHost == "" {
				defHost = a.cfg.DefaultHost
			}
			specs, err := ParseSpecs(args, os.Stdin, a.cfg, defHost)
			if err != nil {
				return err
			}
			missing := 0
			for _, r := range specs {
				dir := r.Dir(a.root())
				if mustExist && !isGitRepo(dir) {
					fmt.Fprintf(os.Stderr, "%s: not cloned (%s)\n", r.Full(), dir)
					missing++
					continue
				}
				fmt.Println(dir)
			}
			if missing > 0 {
				return errSomeFailed
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "host for bare group/project specs")
	cmd.Flags().BoolVarP(&mustExist, "exists", "e", false, "fail (exit 1) for specs that are not cloned")
	return cmd
}

func newConfigCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the effective configuration as JSON",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Fprintf(os.Stderr, "config file: %s\n", a.cfgPath)
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(a.cfg)
		},
	}
	return cmd
}

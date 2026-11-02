package main

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed skills/grove/SKILL.md
var skillMD string

// skillTargets maps an AI tool to the directories that hold its skills
// (Agent Skills format: <dir>/<name>/SKILL.md). "project" paths are relative
// to the current directory.
var skillTargets = map[string]struct{ user, project []string }{
	"claude":      {[]string{"~/.claude/skills"}, []string{".claude/skills"}},
	"antigravity": {[]string{"~/.gemini/config/skills", "~/.gemini/antigravity-cli/skills"}, []string{".agents/skills"}},
	"agents":      {[]string{"~/.agents/skills"}, []string{".agents/skills"}},
}

func newSkillCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "skill",
		Short: "The grove skill for AI coding agents (Agent Skills format): show or install SKILL.md",
		Long: `grove ships a skill that teaches AI coding agents (Claude Code, Antigravity,
and any tool following the Agent Skills format) how to use grove: layout,
work projects, the issue -> worktree -> MR -> merge workflow, pitfalls.

  grove skill show                 print SKILL.md
  grove skill install              install for every known tool, user scope
  grove skill install --for claude --project   into ./.claude/skills/grove/

Targets and directories:
  claude       ~/.claude/skills            ./.claude/skills
  antigravity  ~/.gemini/config/skills, ~/.gemini/antigravity-cli/skills   ./.agents/skills
  agents       ~/.agents/skills            ./.agents/skills   (vendor-neutral)`,
	}
	cmd.AddCommand(newSkillShowCmd(), newSkillInstallCmd())
	return cmd
}

func newSkillShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Print the skill (SKILL.md) to stdout",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, err := fmt.Fprint(os.Stdout, skillMD)
			return err
		},
	}
}

func newSkillInstallCmd() *cobra.Command {
	var targets []string
	var project bool
	var dir string
	cmd := &cobra.Command{
		Use:   "install",
		Short: "Write SKILL.md into the skills directory of each target tool",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			var dirs []string
			switch {
			case dir != "":
				dirs = []string{expandHome(dir)}
			default:
				if len(targets) == 0 {
					targets = []string{"claude", "antigravity", "agents"}
				}
				seen := map[string]bool{}
				for _, t := range targets {
					tg, ok := skillTargets[t]
					if !ok {
						return fmt.Errorf("unknown target %q (claude, antigravity, agents)", t)
					}
					list := tg.user
					if project {
						list = tg.project
					}
					for _, d := range list {
						d = expandHome(d)
						if !seen[d] {
							seen[d] = true
							dirs = append(dirs, d)
						}
					}
				}
			}
			for _, d := range dirs {
				path := filepath.Join(d, "grove", "SKILL.md")
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					return err
				}
				status := "installed"
				if old, err := os.ReadFile(path); err == nil {
					if string(old) == skillMD {
						status = "up-to-date"
					} else {
						status = "updated"
					}
				}
				if status != "up-to-date" {
					if err := os.WriteFile(path, []byte(skillMD), 0o644); err != nil {
						return err
					}
				}
				fmt.Printf("%s\t%s\n", status, path)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&targets, "for", nil, "target tool(s): claude, antigravity, agents (default: all)")
	cmd.Flags().BoolVar(&project, "project", false, "install into the current project (./.claude/skills, ./.agents/skills) instead of the user scope")
	cmd.Flags().StringVar(&dir, "dir", "", "install into this skills directory instead (<dir>/grove/SKILL.md)")
	return cmd
}

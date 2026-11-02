---
name: grove
description: Use the grove CLI for anything involving the user's git repositories across GitHub and GitLab - finding or cloning repos, the local repo layout (~/dev/<host>/<group>/<project>), git worktrees grouped per work project, creating issues, opening/merging merge requests or pull requests, checking CI pipelines, running git or any command across many repos. Triggers on: clone this repo, where is repo X, start working on issue N, create a branch/worktree for, open an MR/PR, is CI green, merge the MR, which repos are dirty, run X in every repo, create a new repository, sync my repos.
---

# grove

`grove` is the single CLI for the user's repositories on GitHub and GitLab.
Prefer it over raw `git clone`, `gh` and `glab` for the tasks below: it puts
things in the right place and records what it did. Run `grove <cmd> --help`
for flags. Every listing supports `--json` (one object per line); diagnostics
go to stderr; exit 1 means some repos failed, 2 means usage/setup error.

## Layout (never clone by hand elsewhere)

| what | where |
|------|-------|
| main clones | `<root>/<host>/<namespace>/<project>` (default root `~/dev`), e.g. `~/dev/gitlab.example.com/acme/api` |
| work projects | `<work_root>/<project>/<repo-name>` (default `~/dev/work`): one git worktree per repo, manifest `grove.json` |

`grove config` prints hosts, default host and roots. `grove path acme/api`
prints a clone's directory; `grove work path <project> <repo>` a worktree's.

## Orient first

```
grove context --json          # repo, branch, is_worktree, project, issue, mr, base of the cwd
grove work info               # the project the cwd belongs to: every repo, branch, issue, MR
grove work ls                 # all work projects
```

## Repo specs and refs

Repo: `group/project` (default host), `host/group/project`, `git@host:path.git`,
`https://host/path`. Issue/MR refs: `N`, `repo#N`, `repo!N`, or the URL.
Commands that take `[repo]` default to the current checkout; `mr`/`ci`
commands default to the current branch.

## Workflow for implementing an issue (MR-first)

1. Read the issue: `grove issue view acme/api#123` (or create one:
   `grove issue new acme/api -t "title" -F spec.md`).
2. Write the implementation plan to a file, then
   `grove work add <project> acme/api --issue 123 -F plan.md`
   (create the project first if needed: `grove work new <project> --desc "..."`).
   This clones if missing, fetches, creates branch `123-<slug>` from the
   default branch, adds the worktree, pushes it (with `-o ci.skip` on
   GitLab), opens a **draft** MR/PR `Closes #123` + plan, assigned to the
   user, and records it in `grove.json`. It prints the worktree directory:
   **do all work there**, never in the main clone and never on the default branch.
3. Commit incrementally and `git push`. Keep the repo's docs current.
4. `grove ci status` / `grove ci wait` (exit 0 only when green). Fix and push again.
5. `grove mr ready` (drop draft), then `grove mr merge --wait` (merges only
   after a green pipeline, pinned to the sha you saw). Never merge a draft.
   Read any infrastructure plan output before merging it.
6. `grove work rm <project> acme/api --delete-branch` (refuses if dirty;
   `--force` to override). `grove work rm <project>` removes a finished project.

A project may span several repos: run `work add` once per repo, then
`grove work status|git|each <project> ...` across all of its worktrees.

If a branch already exists without an MR: `grove mr new --issue N -F plan.md`
(pushes first if needed; returns the existing open MR instead of a duplicate).

## Cheat sheet

```
grove ls acme                              local clones under acme/ (prefix or glob; -H host, -m regex, -x exclude)
grove ls-remote -H gitlab.example.com      what exists remotely (membership); --all, --archived
grove clone acme/api acme/protos           idempotent; "-" streams specs from stdin; -P parallel
grove sync acme --fetch                    clone missing + fetch existing; reports stale (never deletes without --delete-stale)
grove create acme/new-service --init       new repo with the host's configured defaults, cloned into place
grove create acme/tool --push-from DIR     new repo from an existing local checkout
grove git -p acme fetch --prune            git across selected clones (-q: failures only)
grove each -p services -- go build ./...   any command; GROVE_HOST/PATH/NAME/DIR/FULL set, cwd = repo
grove status --dirty                       uncommitted / unpushed work
grove issue ls|view|new [repo]
grove mr ls|view|new|ready|merge [ref]     merge: --wait, --when-green, --squash, --keep-branch
grove ci status|wait [--ref B | --mr N]
grove relocate --from DIR [--apply]        move stray clones into the layout by origin URL (dry run by default)
```

## Errors you may see

- `not mergeable: ci_must_pass` - pipeline failed/running: fix, or `grove mr merge --wait`.
- `is a draft` - `grove mr ready` first.
- `worktree ... contains modified or untracked files` - commit, or `--force`.
- `no open MR for branch` - `grove mr new ...`.
- `host X: unknown kind` - add the host to `grove config` with kind github|gitlab.
- Bare `group/project` without a default host - pass `--host` or `host/group/project`.

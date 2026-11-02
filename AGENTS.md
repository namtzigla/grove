# Working with grove (for Claude Code and other AI tools)

`grove` is the one CLI you need to find, clone, branch, review and merge work
across the repositories on this machine. It wraps `git`, `gh` and `glab` with
a uniform, script-friendly interface. Run `grove <cmd> --help` for flags;
every listing has `--json` (one object per line) and diagnostics go to stderr.

## Layout

| what | where |
|------|-------|
| main clones | `~/dev/<host>/<namespace>/<project>` e.g. `~/dev/gitlab.example.com/acme/api`, `~/dev/github.com/you/project` |
| work projects (grouped worktrees) | `~/dev/work/<project>/<repo-name>` with the manifest `~/dev/work/<project>/grove.json` |

Do not clone by hand into other directories; use `grove clone <spec>` or
`grove work add`, which put things in the right place. `grove path acme/api`
prints the main clone directory; `grove work path <project> <repo>` the worktree.
`grove config` shows the configured hosts and roots.

## Orient yourself

```
grove context --json        # repo, branch, is_worktree, project, issue, mr, base
grove work info             # the whole project you are inside: every repo, branch, issue, MR
grove work ls               # all projects
```

## The expected workflow (MR-first)

1. **Issue first.** Work is tracked as an issue in the repo it changes.
   `grove issue new acme/api -t "..." -F spec.md` (or use an existing one; `grove issue view acme/api#123`).
2. **Plan in the MR before coding.** `grove work add <project> acme/api --issue 123 -F plan.md`
   does, in order: clone if missing, fetch, branch `123-<issue-slug>` from
   the default branch, `git worktree add`, first push with `-o ci.skip` (GitLab;
   avoids a duplicate pipeline), open a **draft MR** whose description is
   `Closes #123` + plan.md, assigned to you, and record it in `grove.json`.
   It prints the worktree directory: work there.
3. **Code in the worktree.** Commit incrementally, never on the default branch.
   Push normally (`git push`). Keep the repo's docs in step with the change.
4. **Check CI.** `grove ci status` (current branch / recorded MR) or `grove ci wait`
   (exit 0 only on success). Fix failures; push again.
5. **Ready + merge.** `grove mr ready` drops the draft prefix; `grove mr merge --wait`
   waits for a green pipeline and merges pinned to the sha you saw. Never
   merge a draft, and read any infrastructure plan output (terraform and the
   like) before merging it.
6. **Clean up.** `grove work rm <project> <repo> --delete-branch` removes the
   worktree (refuses if dirty; `--force` to override). `grove work rm <project>`
   removes everything of a finished project.

A project may span several repos: `grove work add` once per repo. `grove work
status|git|each <project> ...` run across all of its worktrees.

## Cheat sheet

```
grove ls acme                              local clones under acme/ (prefix or glob, -H host, -m regex)
grove ls-remote -H gitlab.example.com payments   what exists remotely (membership)
grove clone acme/api acme/protos           idempotent; "-" reads specs from stdin
grove create acme/new-service --init       new repo with the host's configured defaults, cloned into place
grove create acme/tool --push-from DIR     new repo from an existing local checkout
grove sync acme --fetch                    clone missing + fetch existing
grove git -p acme fetch --prune            run git across selected clones
grove each -p services -- go build ./...   run anything (GROVE_* env, cwd = repo)
grove status --dirty                       who has uncommitted / unpushed work
grove issue ls|view|new [repo]             repo defaults to the current checkout
grove mr ls|view|new|ready|merge [ref]     ref: N, repo!N, repo#N, or URL; default = current branch's MR
grove ci status|wait [--ref B | --mr N]
```

Repo specs accepted everywhere: `group/project` (the configured default host),
`github.com/owner/repo`, `git@host:path.git`, `https://host/path`.

## Things that go wrong

* `not mergeable: ci_must_pass` – the pipeline failed or is still running; use `grove mr merge --wait`.
* `is a draft` – run `grove mr ready` first.
* `worktree ... contains modified or untracked files` – commit or `--force`.
* `no open MR for branch` – `grove mr new --issue N -F plan.md` creates it (pushes if needed).
* GitLab issue URLs may look like `/-/work_items/N`; `#N` works everywhere in grove.

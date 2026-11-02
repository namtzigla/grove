# grove

Manage a tree of git clones laid out by host and namespace, group feature
worktrees per project, and drive issues, merge requests and CI on GitHub and
GitLab through one interface.

```
~/dev/github.com/<owner>/<project>            main clones
~/dev/gitlab.example.com/<group>/<project>
~/dev/work/<project>/<repo-name>              worktrees of a work project
```

`grove` enumerates what you can access with `gh` (GitHub) and `glab`
(GitLab), clones what is missing, runs `git` (or any command) across all or a
subset of the local clones, and wraps the issue → branch → MR → merge loop.
Everything prints one item per line so it composes with pipes, `xargs`, `grep`
and shell loops; `--json` prints one JSON object per line.

## Install

macOS (Homebrew cask from the personal tap, completions included):

```fish
brew install --cask namtzigla/tap/grove
```

Anywhere with Go (then install completions yourself):

```fish
go install github.com/namtzigla/grove@latest   # or: cd grove; go install .
grove completion fish > ~/.config/fish/completions/grove.fish
```

Linux binaries are attached to every [GitHub release](https://github.com/namtzigla/grove/releases).
Requires `git`, plus `gh` and/or `glab` logged in to the hosts you use.

## Config

`$XDG_CONFIG_HOME/grove/config.json` (`~/.config/grove/config.json`), or
`$GROVE_CONFIG`:

```json
{
  "root": "~/dev",
  "work_root": "~/dev/work",
  "default_host": "gitlab.example.com",
  "hosts": {
    "github.com":         { "kind": "github", "protocol": "ssh" },
    "gitlab.example.com": { "kind": "gitlab", "protocol": "ssh",
                            "new_repo": { "shared_runners_enabled": true },
                            "inbound_job_token_scope": false }
  },
  "exclude": ["gitlab.example.com/archive/**"]
}
```

| key            | meaning                                                                 |
|----------------|-------------------------------------------------------------------------|
| `root`         | where clones live (`GROVE_ROOT` and `--root` override it)               |
| `work_root`    | where work projects live (`GROVE_WORK_ROOT`); default `~/dev/work`      |
| `default_host` | host used for bare `group/project` specs                                |
| `hosts`        | `kind` is `github` or `gitlab`; `protocol` is `ssh` (default) or `https` |
| `hosts.*.new_repo` | extra API fields sent when `create` makes a repo on that host (e.g. `shared_runners_enabled`, `has_wiki`) |
| `hosts.*.inbound_job_token_scope` | gitlab: set the inbound CI job-token restriction right after `create` |
| `exclude`      | patterns that `ls-remote` / `sync` skip (`--no-exclude` to ignore)      |

`GITLAB_HOST` in the environment adds a gitlab host. `grove config` prints
the effective configuration.

## Commands

| command                      | what it does                                                        |
|------------------------------|---------------------------------------------------------------------|
| `ls [pattern...]`            | list local clones (`host/path`), `--abs`, `--json`, `-0`            |
| `ls-remote [pattern...]`     | list repos you can access via gh/glab, `--json`, `--all`, `--archived` |
| `clone <spec>... \| -`       | clone specs in parallel (`-P N`), `-` streams specs from stdin       |
| `create <spec>`              | create a repo on GitHub/GitLab, then clone it (or `--push-from DIR`) |
| `sync [pattern...] \| -`     | clone everything missing (streamed, `-P N`); `--fetch` existing; `-` reads stdin; report `stale` |
| `each [flags] -- <cmd>...`   | run a command in every selected clone, in parallel                  |
| `git [flags] <git-args>...`  | shorthand for `each -- git ...`                                     |
| `status [pattern...]`        | branch, ahead/behind, modified/untracked per clone, `--dirty`       |
| `path <spec>...`             | print the local directory for a spec                                |
| `relocate --from DIR`        | move clones into place by origin URL (dry run unless `--apply`)     |
| `config`                     | print effective config                                              |
| `work new\|add\|ls\|info\|path\|rm\|status\|each\|git` | work projects: worktrees of several repos grouped under `~/dev/work/<project>` |
| `issue ls\|view\|new [repo]`  | issues via gh/glab with one interface (repo defaults to the cwd)    |
| `mr ls\|view\|new\|ready\|merge [ref]` | MRs / PRs; ref defaults to the current branch's open MR        |
| `ci status\|wait`             | latest pipeline / checks for a branch or MR                         |
| `context`                    | repo, branch, worktree, project, issue, MR of the cwd (for agents)  |

### Repo specs

`clone`, `path`, `work add` and stdin lines accept any of:

```
git@gitlab.example.com:acme/api.git
ssh://git@gitlab.example.com/acme/api.git
https://github.com/you/project
github.com/you/project
acme/api                 # bare: uses --host, else default_host
```

Stdin lines may be the output of `ls`, `ls-remote`, `status` (first field is
used) or any `--json` output (`host` and `path` keys are read).

### Selecting repos

All local commands and `ls-remote` / `sync` share the same selection flags.
Positional patterns (or `-p`) match by path prefix or glob against `host/path`
or `path`:

```
acme                          everything under acme/ on any host
acme/api                      exactly that repo
gitlab.example.com/infra/deploy   subgroup on one host
'**/api'                      any repo named api
'infra/*/dev-1'               * = one segment, ** = any depth, ? = one char
```

| flag           | meaning                                               |
|----------------|-------------------------------------------------------|
| `-p PATTERN`   | select (repeatable; same as positional for ls/status) |
| `-x PATTERN`   | exclude (wins over select)                            |
| `-H HOST`      | restrict to host (repeatable)                         |
| `-m REGEX`     | regex against `host/path`                             |
| `--stdin`      | take the repo list from stdin instead of scanning     |
| `-j N`         | parallel jobs (global flag, default = CPUs, max 16)   |
| `-P N`         | `clone` / `sync` only: concurrent clones (overrides `-j`) |

## Examples

```fish
# clone everything I am a member of on GitLab, then keep it fresh
grove sync -H gitlab.example.com        # clones start while gh/glab are still paging
grove sync --fetch -P 16
grove ls-remote acme | grep -v legacy | grove sync --fetch -    # sync a filtered list from stdin

# clone a subset, or a hand-written list
grove clone acme/api acme/protos
grove ls-remote -H github.com you | grove clone -P 8 -    # streams: clones start as lines arrive
grove clone --host gitlab.example.com - < repos.txt

# create repos: on the default host, or elsewhere; clone into place or push an existing dir
grove create acme/new-service --desc "..." --visibility internal --init
grove create github.com/you/tool --visibility public --push-from ~/src/tool

# run git across repos
grove git fetch --all --prune
grove git -p acme -p payments checkout master
grove git -q pull --ff-only                    # -q: only show failures
grove git -m 'api|worker' log -1 --oneline

# run anything; GROVE_HOST/PATH/NAME/DIR/FULL are set, cwd is the repo
grove each -p services -- go build ./...
grove each --no-header -- sh -c 'echo $GROVE_FULL (git rev-parse --abbrev-ref HEAD)'

# what is dirty or unpushed?
grove status --dirty
grove status --json | jq -r 'select(.ahead > 0) | .repo'

# chain: fetch only the repos that are behind
grove git -q fetch; and grove status --json | jq -r 'select(.behind > 0) | .repo' | grove git --stdin pull --ff-only

# jump into a repo
cd (grove path acme/api)

# move an old flat layout (~/src/<group>/<repo>) into ~/dev/<host>/...
grove relocate --from ~/src          # preview
grove relocate --from ~/src --apply
```

## Work projects (grouped worktrees) and the MR-first flow

```fish
grove work new feature-x --desc "feature X"
grove work add feature-x platform/api --issue 42 -F plan.md
#  -> ~/dev/work/feature-x/api: worktree of ~/dev/gitlab.example.com/platform/api
#     on branch 42-<issue-slug>, pushed with -o ci.skip, draft MR "Closes #42" + plan.md
grove work add feature-x acme/consumer --issue 7 -F plan2.md
cd (grove work path feature-x api)
grove context                      # repo, branch, project, issue, MR
grove ci wait; and grove mr ready; and grove mr merge
grove work status feature-x        # all worktrees of the project
grove work rm feature-x --delete-branch
```

`grove.json` in the project directory records every worktree with its repo,
branch, base, issue and MR, so tools can resume where they left off
(`grove work info`). See AGENTS.md for the guidance given to AI tools.

## Output and exit codes

* Listings print one repo per line on stdout; diagnostics and summaries go to
  stderr. `--json` prints one JSON object per line (NDJSON) for `jq`.
* `clone` / `sync` print `<status>\t<host/path>` with status
  `cloned | exists | fetched | stale | deleted | failed | would-clone`.
* `each` / `git` print each repo's output as a block headed by
  `== host/path ==` (`--no-header` to drop it, `-q` to show failures only).
* Exit status: `0` ok, `1` some repos failed, `2` usage or setup error.
* `sync` reports `stale` only when it enumerated the hosts itself (not in
  stdin mode) and the enumeration succeeded. Nothing is deleted unless you
  pass `sync --delete-stale`.

## Layout rules

Only first-level directories under the root that are configured hosts or
contain a dot (`github.com`, `gitlab.example.com`) are scanned, so unrelated
folders like `~/dev/tmp` are ignored. A directory containing `.git` is a repo;
scanning does not descend into it, hidden directories or `node_modules`.

## Releasing

Releases are cut by tag. `.github/workflows/release.yml` runs GoReleaser
(`.goreleaser.yaml`), which builds darwin/linux binaries, publishes the GitHub
release with checksums, and commits the Homebrew cask into
`namtzigla/homebrew-tap` (`Casks/grove.rb`).

```fish
git tag v0.2.0; and git push origin v0.2.0
```

One-time setup:

1. The grove repo must be public (Homebrew downloads release tarballs anonymously).
2. Create the tap repo: `grove create github.com/namtzigla/homebrew-tap --visibility public --init --default-branch main`.
3. Create a fine-grained GitHub PAT with *Contents: read and write* on `homebrew-tap`
   and store it as the `TAP_GITHUB_TOKEN` secret of the grove repo
   (`gh secret set TAP_GITHUB_TOKEN -R namtzigla/grove`).

Until `TAP_GITHUB_TOKEN` exists the workflow still publishes the GitHub
release but skips the cask (`--skip=homebrew`); a local
`goreleaser release --clean` from the tagged commit then does the full job,
using `GITHUB_TOKEN` for the tap. Dry run without publishing:
`goreleaser release --snapshot --clean` (binaries land in `dist/`).

# docket

A terminal app that runs PR reviews through the `review-code` skill in a `claude` or `codex` session, then archives and cleans up once the review is submitted. See README.md for the user-facing description.

## Layout

```
cmd/docket/main.go          flags and the URL argument; the only file that imports both core and tui
internal/core/pr/           Ref{Org,Repo,Number}, ParseRef                         pure
internal/core/reposconf/    Parse, Resolve                                         pure, filesystem via callback
internal/core/tier/         Decide                                                 pure
internal/core/review/       Record, Event, State, Fold, Decide                     pure
internal/core/index/        Store: JSONL append under flock, Load, Compact (Watch in M4)
internal/core/config/       config.toml, DOCKET_HOME paths, review_code_dir, codex_sessions_dir
internal/core/exec/         Runner: Real, Fake
internal/core/engine/       Engine interface and Paths; claude.go and codex.go implement it
internal/core/gh/           GitHub interface; shells to `gh api`
internal/core/git/          Git interface; shells to `git`
internal/core/clone/        the tier-2 clone sequence
internal/core/session/      Service: Prepare, LaunchSpec, AfterExit, Submit, Notes, Abandon, Refresh
internal/tui/               root model, its own messages, keymap, styles; the only package that runs a CommandSpec
internal/tui/msg/           the intents the screens send up to the root
internal/tui/screens/       dashboard, newreview, submit, notes
```

`internal/tui/msg` holds only the intents a screen sends up, and it imports nothing
but Bubble Tea. The messages the root produces for itself live in
`internal/tui/msgs.go`, so the screens never depend on the service packages those
name. `internal/tui/deps_test.go` asserts both directions. Screens turn keys into
intents and run no commands, which is what makes their `Update` testable with
synthetic `tea.KeyPressMsg` values.

Dependency direction is `tui -> session -> {engine, gh, git, index, clone, tier, config}`.

## Rules

- Nothing under `internal/core` imports Charm or `internal/tui`. `internal/tui/deps_test.go` asserts this with `go list -deps`.
- `os/exec` is called in three places only: `internal/core/exec`'s `Real` runner, which runs the commands that need nothing but their output; `internal/tui`, which runs the one command that needs the terminal; and `cmd/docket` for the `LookPath` check at startup. Every other core package builds an `exec.CommandSpec{Path, Args, Dir, Unset}` and hands it over. `deps_test.go` asserts this by reading direct imports, because `go list -deps` reports `os/exec` for every core package: they all reach it transitively through `internal/core/exec`.
- Core packages reach the filesystem, git, and GitHub through interfaces, so tests use fakes instead of a real repo or network.
- Installed `review-code` paths are configuration with defaults, never hardcoded constants.
- docket never uses an Anthropic or OpenAI API key. It drives the `claude` and `codex` CLIs under the user's subscription.
- Pin exact versions of the Charm modules in go.mod. The v2 import paths are `charm.land/...`, not `github.com/charmbracelet/...`.
- A dry run is stopped in `internal/tui`, not in a runner. Most of what a dry run must not do is a write to the index or the filesystem, so no `exec.Runner` ever sees it. `session.Explain` and `session.ExplainResume` build what would run without recording anything, and the root model reports that instead of acting. `cmd/docket` is the one exception: `EnsureDirs` and `CompactIfNeeded` run before the TUI exists, so they read the flag themselves. Reading the index still creates the home directory and the lock file, because `Store.lock` opens it with `O_CREATE` on the shared path too, so a dry run records nothing rather than writing nothing.

## Verified facts about review-code

These were verified while planning and shape the design. Don't rediscover them.

- **review-code detects the repo from the working directory** (`~/.agents/skills/review-code/scripts/review-orchestrator.sh`, `handle_pr_review`). If the cwd is a git repo whose `gh repo view` owner/name matches the PR's org/repo, it takes the in-repo path: when `git branch --show-current` equals the PR's `headRefName`, agents read files directly (fast path); otherwise it fetches `pull/N/head` into `refs/review/pr-N` and agents read through `git show` (middle case). If the cwd is not that repo, it takes the cross-repo path: look up `org/repo` in `~/.agents/skills/review-code/repos.conf` (`helpers/repos-config.sh`, `resolve_local_clone`: case-insensitive key, tilde expansion, path must be a git repo), then `pr-worktree.sh provision` makes a blobless fetch and a detached worktree under `~/.agents/skills/review-code/.worktrees/<org>/<repo>/pr-<N>`, torn down at session end by `session-hooks/review-code-cleanup.sh`. No repos.conf entry means a diff-only review.
- **Review notes** live at `~/.agents/skills/review-code/.reviews/<org>/<repo>/pr-<N>.md` (`helpers/config-helpers.sh`, `get_review_root`). They persist independently of any worktree or clone, so docket records the path and never moves the file.
- **Submission is always explicit.** review-code's `create-draft-review.sh` posts a review with no `event`, which GitHub stores as `PENDING` with `submitted_at: null`. Submitting is `POST /repos/{o}/{r}/pulls/{n}/reviews/{id}/events` with `APPROVE`, `COMMENT`, or `REQUEST_CHANGES`. `~/.dotfiles/bin/pr-review.sh` (`fetch_pending_reviews`, `cmd_submit`) is the logic docket ports, except that docket paginates and that script does not.
- **CLI facts** (Claude Code 2.1.268, codex-cli 0.150.1): `claude [prompt]` starts an interactive session with the prompt as the first message; `--session-id <uuid>` sets the id; `-r/--resume <id>` resumes; `-c/--continue` resumes the most recent conversation in the cwd; `--bg` starts a background session and prints its id; `-p/--print` is headless. `codex [prompt]` is interactive, `codex exec` is headless, and the skill is invoked as `$review-code`. `-C/--cd <dir>` and `--add-dir <dir>` are accepted by plain `codex`, `codex exec`, and `codex resume` alike (verified on 0.150.1), so resuming keeps the same working root and writable directories as the start. Launched from inside a Claude session, codex needs `env -u CLAUDECODE -u CLAUDE_CONFIG_DIR`.
- Interactive `codex` has no session-id flag. `CaptureSessionID` scans `<codex_sessions_dir>/YYYY/MM/DD/rollout-*.jsonl` and reads the first line, which is a `session_meta` object. It matches `payload.cwd` against the record's directory, with both sides run through `EvalSymlinks` because codex records the resolved path. It filters on `payload.timestamp`, not the file's mtime: a rollout is appended to for as long as the session runs, so its mtime is the end of the session, not the start.
- The rollout path's date directories and filename timestamp are **local** time while `payload.timestamp` is UTC, so `scanDays` builds its candidate directories in local time.
- **docket's session is the oldest match, not the newest.** review-code dispatches every reviewer through `codex exec` from inside the running session and passes no `-C`, so each reviewer inherits the session's working directory and writes its own rollout carrying the same `cwd` and a later timestamp. Taking the newest match therefore hands back a reviewer's thread, and `codex exec resume <sub-agent id>` answers `cannot resume an unloaded multi-agent v2 sub-agent through its parent` (reproduced on 0.150.1). `CaptureSessionID` takes the first rollout at or after the launch. A resume is still captured, because `launch` stamps `StartedAt` again and the cutoff moves past the earlier session with it.
- `scanDays` covers the start day and the day after it, and nothing else. codex files a rollout under the local date the session began, and `launch` stamps `StartedAt` immediately before handing over the terminal, so the second day only covers a launch that crosses midnight.
- A `session_meta` payload carries both `id` and `session_id`. On a fresh session they are equal. A resumed session writes a new rollout whose `id` is its own and whose `session_id` is the thread it continues. Which of the two `codex resume` accepts after a resume is **unverified**: the check needs a working `codex` CLI, and the installed 0.150.1 is too old for this account's models (`gpt-6-astra` wants a newer CLI, and 0.150.1 cannot fall back to a model a ChatGPT account may use). docket stores `payload.id` and re-captures after **every** exit, which is correct under either answer as long as a rollout is resumable by its own id. Filtering sub-agents on `session_id != id` looks tempting and is wrong: a resumed interactive session has that shape too, so the filter would drop the very rollout the next resume needs. Verify the id question when the CLI is upgraded.
- Also unverified, and settled by the first real codex run: taking the oldest match assumes `codex resume` writes a new rollout when it resumes. If it writes none, the capture finds nothing and `captureSessionID` keeps the id it already had, so a plain resume is safe either way. The case that would mis-capture is a resume that writes no rollout **and** dispatches a review from inside itself, where the oldest match after the new `StartedAt` would then be the first reviewer.
- `CaptureSessionID` matches on the record's directory and a start time, and every tier-1 record runs in the one shared `Paths.Scratch`. Two docket instances reviewing under codex at the same time can therefore each capture the other's session, because the oldest rollout after a record's cutoff may be the other instance's. It takes all three to happen: the codex engine, tier 1, and overlapping instances, which one process cannot produce on its own because the TUI hands the terminal to a single session. M3's background reviews are what make it reachable, and a per-record working directory is the fix.
- The installed skill directory is a copy produced by `~/dev/haacked/review-code/install.sh`. The source is `github.com/haacked/review-code`.
- Supacode has no "tab without a worktree" primitive, so docket runs in any terminal and does not talk to Supacode.

## Bubble Tea v2 specifics

- `View()` returns `tea.View`. Alt screen, mouse mode, and bracketed paste are fields on it. The v1 `tea.WithAltScreen` program options are gone.
- Keys arrive as `tea.KeyPressMsg`, matched on `msg.String()`.
- `tea.ExecProcess(cmd, callback)` is retained. Its restore path re-enters the alt screen, restarts the renderer, and re-checks the window size.
- Keep `AltScreen` constant and `MouseMode` at `tea.MouseModeNone` so the restore after a child process is deterministic.
- Never cache width or height across an exec. A `WindowSizeMsg` follows the restore.
- Keep Bubble Tea's default signal handler and never set `Setpgid` on the child. The child must stay the foreground process group of the TTY.
- `teatest` v2 is only a pseudo-version. Test screens by calling `Update` with synthetic `tea.KeyPressMsg` values instead.

## Testing

`go test ./...`. The pure packages (`pr`, `reposconf`, `tier`, `review`, `index`) carry the bulk of the coverage. Use `t.TempDir()` and the `exec.Fake` runner rather than touching the network, the real `~/.docket`, or a real clone.

Two tests are worth knowing about. `internal/core/index/lock_test.go` races `Compact` against concurrent `Append` calls, and it fails when `Compact` drops its lock, so it is the test that actually covers the locking. `internal/core/session/smoke_test.go` runs against a real pull request and is skipped unless you name one:

```
DOCKET_SMOKE_PR=haacked/review-code#159 go test -count=1 -v -run Smoke ./internal/core/session/
```

That is how to check the one thing fakes cannot: that the clone sequence really lands on the head branch with files in the working tree.

## Where the milestones stand

M1 is in: the dashboard, the new review screen, the claude engine, post-session detection, archive with tier-2 cleanup, abandon, refresh, and `--dry-run`.

M2 is in: the submit screen (`s`), the notes viewer (`v`, with `e` for `$EDITOR`), and the codex engine with session id capture. `Submit` posts the event and then re-reads GitHub through the same `detect` the rest of the app uses, so archiving and the tier-2 cleanup have one code path rather than two. The submit screen leaves `APPROVE` out when the record's author is the signed-in user, because GitHub answers 422 to approving your own pull request.

Still to come: M3 (background reviews), M4 (index watching, keymap help, install docs). Startup already compacts the log when it carries more than five events per record.

# docket

docket is a terminal app for reviewing pull requests. You paste a PR URL, and docket starts a `claude` or `codex` session that runs the [review-code](https://github.com/haacked/review-code) skill against that PR. Once you submit the review, docket archives the record and deletes whatever it created.

**Status: early.** Starting a review in `claude` or `codex`, running it in the background under `claude`, reading what the session left on GitHub, submitting the review, reading and asking about the notes, reviewing a pull request again, starting reviews from the pull requests that request yours, cleaning up after a submitted review, and starting, following, and submitting reviews from an agent session are in place.

## The workflow it replaces

A review takes five manual steps today. Create a worktree for a scratch repository, run `/review-code <url> --draft` in it, read and discuss the review in the session, submit the review, then remember to delete the worktree. With docket you paste the URL, have the discussion, and submit. The discussion still happens in a real `claude` or `codex` session, because that is where a review gets useful.

## How it works

docket keeps one record per PR and moves it through `preparing`, `reviewing`, `drafted`, `submitted`, and `archived`. A review session that ends without posting anything new leaves the record `unreviewed`, which the dashboard lists under "No review posted". A background review that `claude` refused to start is `not_started`, listed under "Did not start". A record you added only to ask about an existing review starts as `drafted` when your draft is still pending and as `reviewed` when it is not. Apart from `x`, which stops the session, cleans up, and leaves the record `abandoned`, every transition comes from something docket observed, either a GitHub API response or a child process exit, never from what you said you would do. After the session exits, docket asks GitHub whether you have a review on that PR and whether it is still pending, then sets the state from the answer.

docket also asks whether the PR is still open. A record whose PR merged or closed is archived, unless you still have a pending review there: GitHub accepts a review on a merged PR, so that row stays, tagged `merged`, for you to submit or abandon. docket refuses to start a review on a PR that has already merged or closed. A record is only archived when docket reads GitHub for it: after a session exits, when a background review finishes, or when you press `r` or `R`.

Where docket launches the session depends on whether `review-code` already knows the repository, which it reads from `repos.conf`.

- **Repository listed in `repos.conf`.** docket makes no clone. It launches the session from its own scratch directory, so `review-code` takes its cross-repo path and provisions and tears down a worktree off your local clone, exactly as it does today.
- **Repository not listed.** docket makes a shallow clone of the PR head under `~/.docket/clones/<org>/<repo>/pr-<N>` and launches the session there, so `review-code` reads whole files instead of falling back to a diff-only review. docket deletes the clone once the review is submitted.

## Background reviews

A review runs in the background by default, so you can start several and keep working. `ctrl+b` on the new review screen runs it in this terminal instead, and `default_run = "terminal"` in `config.toml` makes the terminal the default:

```toml
default_run = "terminal"   # or "background", the default
```

docket asks `claude` every fifteen seconds how each session is doing. The line under each running row shows claude's one-line summary of what the session is doing and how many agents it is running. When a session is waiting for you, that line turns red and says what the session needs, and `enter` opens the session so you can answer it. When claude has not updated its summary for ten minutes, the line says how long the session has been quiet, which is your cue to open it and look. When a session finishes, docket reads GitHub for what it left and moves the record on, the same way it does after a session you sat through. review-code ends a background review once it has posted the draft, often by asking whether to submit it, and the session then waits on you rather than finishing. When your draft is on GitHub, docket moves that row to Drafted anyway, so `s` submits it from the dashboard, and `enter` still opens the session if you would rather answer it there. If you open a drafted row's session and leave it working, docket cannot tell whether the session is changing the review, so the row goes back to Reviewing until the session ends its turn. `s` still submits the draft meanwhile, and submitting leaves a session that is still working running.

`claude` runs a background session only in a directory you have trusted, and it can ask that question only in a terminal. When it refuses a review for that reason, docket hands the terminal to `claude` in that directory. Pick "Yes, I trust this folder", and docket starts the review again. Tier-1 reviews share one directory, so this happens once for all of them. Each tier-2 review clones into a directory of its own, so it happens once for each. A review that still does not start waits under "Did not start" with claude's reason, and `enter` starts it again.

`enter` puts a background session back on your terminal. While `claude` is still holding the session that means `claude attach`, and once the session is stopped it means `claude --resume`; docket picks the one that works. Leaving a session that is still working does not end the review, it goes back to running and docket keeps watching it.

A background session outlives the docket that started it, so quitting docket does not stop the review and starting docket again picks it back up. `x` stops the session before deleting anything, and it never runs `claude rm`, so the conversation survives.

Reviewing your own pull request works. review-code leaves the draft review out of one unless it is asked, so docket asks when the author is you.

Only `claude` runs background reviews. `codex` 0.150.1 has no background mode, so a `codex` review runs in this terminal whatever the default says, and the new review screen says so.

## Claude account

docket runs `claude` under the account that `CLAUDE_CONFIG_DIR` names in the shell you start it from, or under claude's default login when that is unset. To review under another account, sign that account in to a config directory of its own (run `CLAUDE_CONFIG_DIR=~/.claude-work claude` and then `/login`), and link `review-code` into that directory's `skills` folder, because a new config directory has none of your skills. Then name the directory in `config.toml`:

```toml
claude_config_dir = "~/.claude-work"
```

Each review keeps the account it started under, so a change to this setting applies only to new reviews. docket still resumes, attaches to, and stops the open ones under their own account. Each account keeps its own list of trusted directories, so the first background review under a new account shows the trust prompt again.

## Fix reviews

Some pull requests are better fixed than reviewed, such as the ones a bot opens. A fix review runs review-code with `--fix` instead of `--draft`: the session edits the code for the findings it can fix cleanly, lists what it skipped under "Fix Summary" in the notes, and posts nothing to GitHub. You then commit and push the fixes from the session, and approve the pull request from docket.

List the authors whose pull requests docket should fix in `config.toml`. A GitHub App matches as `app/<name>` or `<name>[bot]`:

```toml
fix_authors = ["app/posthog"]
```

On the new review screen, `ctrl+f` switches between fixing for those authors only, always fixing, and never fixing. A batch from the review requests screen fixes the pull requests by those authors.

A fix review of a pull request a bot opened adds you to its assignees before the session starts, unless you are already one, and so does reviewing it again with `u`. GitHub decides what counts as a bot, which is a GitHub App such as `app/posthog`; a regular account that a team uses as a bot does not count. When GitHub refuses the assignment, the review does not start and the row shows GitHub's answer.

review-code edits files only in a checkout of the head branch, so a fix review gets a checkout of its own. For a repo listed in review-code's `repos.conf`, docket adds a worktree of your clone under `~/.docket/worktrees`, on a local branch named like the head branch and tracking it, so `git push` needs no arguments. When your clone already has a branch of that name, docket clones the head instead, as it does for a repo `repos.conf` does not list. A pull request from a fork has no branch docket can push to by name, so it gets a draft review instead.

When the session ends, docket reads the checkout rather than GitHub. A row with changes that are not on GitHub waits under "Fixes to push", and `enter` opens its session so you can have the agent commit and push them. A row whose checkout matches GitHub waits under "Ready to approve", tagged "no changes" when the review fixed nothing. `s` on it posts a new review of the commit the fixes are on, with approve offered first, and archives the row. docket refuses that when the pull request has commits the fixes were not made on, and `u` reviews those. docket never deletes a checkout that holds changes that are not on GitHub: `x` on such a row says so and keeps it.

## Review requests

`i` on the dashboard lists the open pull requests waiting on your review. The first section holds the ones that name you, and each team you choose gets a section of its own. `t` on that screen lists the teams GitHub says you belong to: `space` checks a team, `enter` saves the checked ones and searches again, and `esc` goes back without saving. Listing your teams needs the `read:org` scope, which `gh auth refresh -s read:org` grants. The choice is saved to `config.toml`, which you can also edit by hand, for instance to add a team you do not belong to:

```toml
teams = ["PostHog/team-feature-flags"]
```

Saving from the teams screen changes only `teams` and keeps your other settings as the file has them, but comments in the file do not survive. Within each section, the pull requests assigned to you come first, then the ones assigned to nobody, and then a group for each other assignee in alphabetical order. A pull request with several assignees shows once: under you when you are one of them, and otherwise under the first assignee GitHub lists. Draft pull requests are hidden, and each section's heading says how many of its own it hides. `d` shows them, drawn faint, and `d` again hides them. A draft that shows can be marked and reviewed like any other row, and hiding the drafts drops their marks. A pull request that asks for both you and a team appears only under you. A team's section leaves out the pull requests you opened. It also leaves out one you have reviewed that has no commits since your review, even when the team is asked again. A reply in a review thread does not count as a review. When the author asks you again by name, the pull request shows in your own section. `space` marks rows, and `enter` starts every marked pull request as a background review, one after another, under the default engine when it has a background mode and under `claude` otherwise. Before starting any of them, docket checks each marked pull request for review notes or a review of yours on GitHub, which is what a pull request whose author asked you to review it again has. When it finds any, it lists them and asks once for all of them: `a` appends, which reviews what changed since your last review, `o` overwrites with a review of the whole pull request, `s` starts only the others, and `esc` goes back to the list with your marks kept. With nothing marked, `enter` opens the new review screen with the selected pull request filled in, so a single review can still run in this terminal or under `codex`. `o` opens the selected pull request on GitHub, so you can read it before you decide to review it. When docket already has an open record for a pull request, its row shows that record's state and cannot be marked, and `enter` on it opens that review, the same as on the dashboard. A team whose search fails shows GitHub's error under its heading, and the other sections still show.

The list is a GitHub search, which allows 30 requests a minute, so docket searches when the screen opens and when you press `r`, and never on a timer.

Review notes stay where `review-code` writes them, at `~/.agents/skills/review-code/.reviews/<org>/<repo>/pr-<N>.md`. docket records the path, and never moves or deletes the file.

docket drives the `claude` and `codex` CLIs under your existing subscription. It never uses an Anthropic or OpenAI API key.

Every `claude` session docket starts loads only your user settings (`--setting-sources user`). A pull request's own `.claude/settings.json` could define hooks that run commands on your machine, and its author controls that file, so docket leaves it out. Your own hooks, skills, and permissions still apply.

## From an agent session

`docket mcp` serves docket over the [Model Context Protocol](https://modelcontextprotocol.io) on stdin and stdout, so an agent session can start reviews, see where they stand, and submit them. Register it with `claude` from the directory whose sessions should have it:

```
claude mcp add docket -- docket mcp
```

`claude mcp add -s user docket -- docket mcp` registers it for every directory instead. The background review sessions docket starts get none of its tools either way, because they read what the pull request's author wrote and nobody is there to confirm a tool call. docket denies `mcp__docket` when it launches them, so keep the name `docket` when you register the server. The server's instructions tell an agent to confirm the event and the body with you before it submits.

It offers three tools:

- `start_review` starts a background review of a pull request, as the new review screen does. When the pull request already has review notes or a review of yours, it starts nothing and says what it found. The agent then asks you whether to append or overwrite and calls it again with your answer. `fix` set to true makes it a fix review and false a draft review; left out, `fix_authors` decides.
- `list_reviews` lists your open reviews and what each running session is doing. It asks `claude` about the running sessions and reads GitHub for the ones that finished, as the dashboard does every fifteen seconds, so a review whose draft is posted reads `drafted` even when no dashboard is open.
- `submit_review` submits a drafted review as `COMMENT`, `APPROVE`, or `REQUEST_CHANGES` and archives it. Leave the body out to keep the summary review-code posted with the draft. For a fix review whose fixes are pushed, it posts a new review of the commit they are on.

Every review the server starts runs in the background under `claude`, because the server has no terminal to give a session, and `codex` has no background mode. When `claude` refuses a directory nobody has trusted, `start_review` names the directory. Run `claude --setting-sources user /exit` there once, accept the prompt, and have the agent call `start_review` again. A plain `claude` would load the pull request's own `.claude/settings.json` and run its hooks. Or press `enter` on the row in docket, which asks the same question and then starts the review. A pull request that docket clones gets a directory of its own, so this can happen once for each of them. A session that stops to ask you something shows as waiting in `list_reviews`, and `enter` on its row in docket opens it so you can answer.

The server and the dashboard share the index, so a review started from an agent session shows up in a running docket, and the other way round. Abandoning a review, asking about the notes, and reviewing again are only on the dashboard. `start_review` refuses a pull request docket already has open, and when that review failed to set up or cannot start, the refusal tells the agent to have you abandon it with `x`. When an agent working in a review's clone submits that review, docket keeps the clone. The archive finishes when that session ends, if docket opened it, or when you press `r` on the row once the session is done. `R` leaves that row alone. `--dry-run` does not apply to `docket mcp`.

## Requirements

- Go 1.26, to build
- git
- `gh`, authenticated
- `claude`
- [review-code](https://github.com/haacked/review-code), installed

At startup docket checks that `git`, `gh`, and the engine's binary are on your PATH, so a missing tool fails before you start a review rather than inside one. It makes no network call until it needs one, and an authentication problem surfaces as the agent's own output.

## Commands

```
docket                                        open the dashboard
docket https://github.com/org/repo/pull/123   open the dashboard with that PR ready to review
docket --dry-run o/r#123                      say what would happen, start and record nothing
docket mcp                                    serve reviews to an agent session over MCP
```

A pull request can be a URL, `org/repo#123`, or a bare number once `default_repo` is set in `config.toml`.

Dashboard keys: `n` new review, `i` review requests, `enter` resume or open, `s` submit your pending review or review pushed fixes, `v` view the notes, `o` open the pull request on GitHub, `c` ask questions about the notes, `u` review again by appending to or overwriting the review, `x` abandon, `r` refresh the selected record from GitHub, `R` refresh every record, `a` show archived records, `?` show the full key reference, `q` quit.

On the new review screen, `tab` picks the engine, `ctrl+b` chooses between this terminal and the background, and `ctrl+f` chooses whether the review fixes the code (see Fix reviews). With the default settings, `enter` starts a `claude` review in the background and leaves you on the dashboard. When the pull request already has review notes or a review of yours on GitHub, the screen asks what to do with it: `v` adds it to the dashboard without reviewing again and opens a session to ask questions about the notes (if you have that session post a draft or submit the review, docket reads it from GitHub when the session ends), `a` re-reviews and appends to the notes, and `o` re-reviews from scratch. docket passes your answer to review-code as `--append` or `--overwrite`, so review-code never stops to ask, in the terminal or in the background. On the submit screen, `tab` picks the event and `ctrl+s` submits. The body starts with the summary review-code posted with the draft, so you can edit it before submitting. A body you leave untouched or empty keeps that summary as it is on GitHub. Approving is not offered on your own pull request, because GitHub refuses it. On the notes screen, `e` opens the file in `$EDITOR`. `o` on the dashboard, the notes screen, or the review requests screen opens the pull request in your browser. When your review is still a draft, the page opens on the Conversation tab at that draft, which GitHub shows there to you alone. It runs `$BROWSER` when that is set, and otherwise `open` on macOS or `xdg-open` elsewhere. docket does not give `$BROWSER` the terminal, so it must name a graphical browser. The notes follow your terminal's background, and `GLAMOUR_STYLE` overrides that with any glamour style name, such as `light`, `dracula`, or `notty`. `?` opens the full key reference from the dashboard or the notes screen; `esc` or `?` closes it again. New review and submit leave `?` out of their own hints, because each holds a free-text field where a `?` is something you might actually want to type.

State lives in `~/.docket`, which `DOCKET_HOME` or `--home` overrides. It holds an append-only `index.jsonl`, `clones/` for shallow clones, `worktrees/` for the worktrees of fix reviews, `scratch/` for the tier-1 launch directory, and `config.toml`. Two docket instances can run at once: every write appends one line while holding a lock, and nothing is rewritten in place. Each instance also polls the index file every couple of seconds, so a review started in one shows up on the other's dashboard shortly after.

## Build

The scripts in `bin/` follow PostHog's [scripts convention](https://posthog.com/handbook/engineering/conventions/scripts).

```
bin/setup     download the Go modules; run it once after cloning
bin/build     build ./docket
bin/start     run docket from source; arguments pass through, as in bin/start --dry-run o/r#123
bin/test      check formatting, run go vet, and run the tests; flags pass through to go test
bin/fmt       format the Go code, and the scripts in bin/ when shfmt is installed
bin/update    download the modules go.mod names, after a pull
bin/install   build docket and install it on your PATH; run it again after a pull
```

To put docket on your PATH, run `bin/install` from the clone, or `go install github.com/haacked/docket/cmd/docket@latest`.

## License

MIT. See [LICENSE.md](LICENSE.md).

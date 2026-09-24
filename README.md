# docket

docket is a terminal app for reviewing pull requests. You paste a PR URL, and docket starts a `claude` or `codex` session that runs the [review-code](https://github.com/haacked/review-code) skill against that PR. Once you submit the review, docket archives the record and deletes whatever it created.

**Status: early.** Starting a review in `claude` or `codex`, reading what the session left on GitHub, submitting the review, reading the notes in the app, and cleaning up after a submitted review are in place. So is running a review in the background, under `claude` only.

## The workflow it replaces

A review takes five manual steps today. Create a worktree for a scratch repository, run `/review-code <url> --draft` in it, read and discuss the review in the session, submit the review, then remember to delete the worktree. With docket you paste the URL, have the discussion, and submit. The discussion still happens in a real `claude` or `codex` session, because that is where a review gets useful.

## How it works

docket keeps one record per PR and moves it through `preparing`, `reviewing`, `drafted`, `submitted`, and `archived`. Every transition comes from something docket observed, either a GitHub API response or a child process exit, never from what you said you would do. After the session exits, docket asks GitHub whether you have a review on that PR and whether it is still pending, then sets the state from the answer.

docket also asks whether the PR is still open. A record whose PR merged or closed is archived, unless you still have a pending review there: GitHub accepts a review on a merged PR, so that row stays, tagged `merged`, for you to submit or abandon. docket refuses to start a review on a PR that has already merged or closed. A record is only archived when docket reads GitHub for it: after a session exits, when a background review finishes, or when you press `r` or `R`.

Where docket launches the session depends on whether `review-code` already knows the repository, which it reads from `repos.conf`.

- **Repository listed in `repos.conf`.** docket makes no clone. It launches the session from its own scratch directory, so `review-code` takes its cross-repo path and provisions and tears down a worktree off your local clone, exactly as it does today.
- **Repository not listed.** docket makes a shallow clone of the PR head under `~/.docket/clones/<org>/<repo>/pr-<N>` and launches the session there, so `review-code` reads whole files instead of falling back to a diff-only review. docket deletes the clone once the review is submitted.

## Background reviews

`ctrl+b` on the new review screen runs the review without the terminal, so you can start several and keep working. docket asks `claude` every fifteen seconds how each session is doing. The line under each running row shows claude's one-line summary of what the session is doing and how many agents it is running. When a session is waiting for you, that line turns red and says what the session needs, and `enter` opens the session so you can answer it. When claude has not updated its summary for ten minutes, the line says how long the session has been quiet, which is your cue to open it and look. When a session finishes, docket reads GitHub for what it left and moves the record on, the same way it does after a session you sat through.

`claude` runs a background session only in a directory you have trusted, and it can ask that question only in a terminal. When it refuses a review for that reason, docket hands the terminal to `claude` in that directory. Pick "Yes, I trust this folder", and docket starts the review again. Tier-1 reviews share one directory, so this happens once for all of them. Each tier-2 review clones into a directory of its own, so it happens once for each. A review that still does not start waits under "Did not start" with claude's reason, and `enter` starts it again.

`enter` puts a background session back on your terminal. While `claude` is still holding the session that means `claude attach`, and once the session is stopped it means `claude --resume`; docket picks the one that works. Leaving a session that is still working does not end the review, it goes back to running and docket keeps watching it.

A background session outlives the docket that started it, so quitting docket does not stop the review and starting docket again picks it back up. `x` stops the session before deleting anything, and it never runs `claude rm`, so the conversation survives.

Reviewing your own pull request works. review-code leaves the draft review out of one unless it is asked, so docket asks when the author is you.

Only `claude` runs background reviews. `codex` 0.150.1 has no background mode, so the toggle says so and stays off.

## Review requests

`i` on the dashboard lists the open pull requests waiting on your review. The first section holds the ones that name you, and each team listed in `config.toml` gets a section of its own:

```toml
teams = ["PostHog/team-feature-flags"]
```

A pull request that asks for both you and a team appears only under you. `space` marks rows, and `enter` starts every marked pull request as a background review, one after another, under the default engine when it has a background mode and under `claude` otherwise. With nothing marked, `enter` opens the new review screen with the selected pull request filled in, so a single review can still run in this terminal or under `codex`. When docket already has an open record for a pull request, its row shows that record's state and cannot be marked, and `enter` on it opens that review, the same as on the dashboard. A team whose search fails shows GitHub's error under its heading, and the other sections still show.

The list is a GitHub search, which allows 30 requests a minute, so docket searches when the screen opens and when you press `r`, and never on a timer.

Review notes stay where `review-code` writes them, at `~/.agents/skills/review-code/.reviews/<org>/<repo>/pr-<N>.md`. docket records the path, and never moves or deletes the file.

docket drives the `claude` and `codex` CLIs under your existing subscription. It never uses an Anthropic or OpenAI API key.

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
```

A pull request can be a URL, `org/repo#123`, or a bare number once `default_repo` is set in `config.toml`.

Dashboard keys: `n` new review, `i` review requests, `enter` resume or open, `s` submit a drafted review, `v` view the notes, `c` ask questions about the notes, `u` review again by appending to or overwriting the review, `x` abandon, `r` refresh the selected record from GitHub, `R` refresh every record, `a` show archived records, `?` show the full key reference, `q` quit.

On the new review screen, `tab` picks the engine and `ctrl+b` chooses between this terminal and the background. When the pull request already has review notes or a review of yours on GitHub, the screen asks what to do with it: `v` adds it to the dashboard without reviewing again and opens a session to ask questions about the notes (if you have that session post a draft or submit the review, docket reads it from GitHub when the session ends), `a` re-reviews and appends to the notes, and `o` re-reviews from scratch. docket passes your answer to review-code as `--append` or `--overwrite`, so review-code never stops to ask, in the terminal or in the background. On the submit screen, `tab` picks the event and `ctrl+s` submits. Approving is not offered on your own pull request, because GitHub refuses it. On the notes screen, `e` opens the file in `$EDITOR`. The notes follow your terminal's background, and `GLAMOUR_STYLE` overrides that with any glamour style name, such as `light`, `dracula`, or `notty`. `?` opens the full key reference from the dashboard or the notes screen; `esc` or `?` closes it again. New review and submit leave `?` out of their own hints, because each holds a free-text field where a `?` is something you might actually want to type.

State lives in `~/.docket`, which `DOCKET_HOME` or `--home` overrides. It holds an append-only `index.jsonl`, `clones/` for shallow clones, `scratch/` for the tier-1 launch directory, and `config.toml`. Two docket instances can run at once: every write appends one line while holding a lock, and nothing is rewritten in place. Each instance also polls the index file every couple of seconds, so a review started in one shows up on the other's dashboard shortly after.

## Build

The scripts in `bin/` follow PostHog's [scripts convention](https://posthog.com/handbook/engineering/conventions/scripts).

```
bin/setup     download the Go modules; run it once after cloning
bin/build     build ./docket
bin/start     run docket from source; arguments pass through, as in bin/start --dry-run o/r#123
bin/test      check formatting, run go vet, and run the tests; flags pass through to go test
bin/fmt       format the Go code, and the scripts in bin/ when shfmt is installed
bin/update    download the modules go.mod names, after a pull
```

To put docket on your PATH, run `go install ./cmd/docket` from the clone, or `go install github.com/haacked/docket/cmd/docket@latest`.

## License

MIT. See [LICENSE.md](LICENSE.md).

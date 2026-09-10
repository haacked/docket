# docket

docket is a terminal app for reviewing pull requests. You paste a PR URL, and docket starts a `claude` or `codex` session that runs the [review-code](https://github.com/haacked/review-code) skill against that PR. Once you submit the review, docket archives the record and deletes whatever it created.

**Status: not yet functional.** This repository holds scaffolding only. Nothing below runs yet.

## The workflow it replaces

A review takes five manual steps today. Create a worktree for a scratch repository, run `/review-code <url> --draft` in it, read and discuss the review in the session, submit the review, then remember to delete the worktree. With docket you paste the URL, have the discussion, and submit. The discussion still happens in a real `claude` or `codex` session, because that is where a review gets useful.

## How it works

docket keeps one record per PR and moves it through `preparing`, `reviewing`, `drafted`, `submitted`, and `archived`. Every transition comes from something docket observed, either a GitHub API response or a child process exit, never from what you said you would do. After the session exits, docket asks GitHub whether you have a review on that PR and whether it is still pending, then sets the state from the answer.

Where docket launches the session depends on whether `review-code` already knows the repository, which it reads from `repos.conf`.

- **Repository listed in `repos.conf`.** docket makes no clone. It launches the session from its own scratch directory, so `review-code` takes its cross-repo path and provisions and tears down a worktree off your local clone, exactly as it does today.
- **Repository not listed.** docket makes a shallow clone of the PR head under `~/.docket/clones/<org>/<repo>/pr-<N>` and launches the session there, so `review-code` reads whole files instead of falling back to a diff-only review. docket deletes the clone once the review is submitted.

Review notes stay where `review-code` writes them, at `~/.agents/skills/review-code/.reviews/<org>/<repo>/pr-<N>.md`. docket records the path, and never moves or deletes the file.

docket drives the `claude` and `codex` CLIs under your existing subscription. It never uses an Anthropic or OpenAI API key.

## Requirements

- Go 1.26, to build
- git
- `gh`, authenticated
- `claude`, `codex`, or both
- [review-code](https://github.com/haacked/review-code), installed

## Planned commands

```
docket                                        open the dashboard
docket https://github.com/org/repo/pull/123   open the dashboard and start a review of that PR
```

Planned dashboard keys: `n` new review, `enter` resume, `s` submit, `o` open notes, `x` abandon, `a` show archived records, `r` refresh from GitHub.

State lives in `~/.docket`, which `DOCKET_HOME` overrides. It holds an append-only `index.jsonl`, `clones/` for shallow clones, `scratch/` for the launch directory, and `config.toml`.

## License

MIT

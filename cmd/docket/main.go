// Command docket runs PR reviews through review-code in a claude or codex
// session, then archives and cleans up once the review is submitted.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"

	"github.com/haacked/docket/internal/core/clone"
	"github.com/haacked/docket/internal/core/config"
	"github.com/haacked/docket/internal/core/engine"
	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/git"
	"github.com/haacked/docket/internal/core/index"
	"github.com/haacked/docket/internal/core/session"
	"github.com/haacked/docket/internal/mcp"
	"github.com/haacked/docket/internal/tui"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "docket:", err)
		os.Exit(1)
	}
}

type flags struct {
	engine string
	home   string
	dryRun bool
	help   bool
	input  string
	mcp    bool
}

func run() error {
	opts, err := parseFlags(os.Args[1:])
	if err != nil {
		return err
	}
	if opts.help {
		fmt.Print(usage)
		return nil
	}

	paths, err := config.NewPaths(opts.home)
	if err != nil {
		return err
	}
	if !opts.dryRun {
		if err := paths.EnsureDirs(); err != nil {
			return err
		}
	}

	cfg, err := config.Load(paths.Config)
	if err != nil {
		return err
	}
	if opts.engine != "" {
		cfg.DefaultEngine = opts.engine
	}
	eng, err := engine.For(cfg.DefaultEngine)
	if err != nil {
		return err
	}
	// The server has no terminal to give a session. Every review it starts
	// therefore runs in the background.
	if opts.mcp {
		if eng, err = engine.For(engine.BackgroundName(eng.Name())); err != nil {
			return err
		}
	}
	if err := requireTools(eng.Binary()); err != nil {
		return err
	}

	store := index.New(paths.Index, paths.Lock)
	// Compaction rewrites the log, which is the one thing a dry run must not do.
	// It runs here rather than in internal/tui, so the flag is read here too.
	if !opts.dryRun {
		if _, err := store.CompactIfNeeded(); err != nil {
			return err
		}
	}

	runner := exec.Real{}
	gitCLI := git.New(runner)
	svc := &session.Service{
		Cfg:    cfg,
		Paths:  paths,
		Store:  store,
		GH:     gh.New(runner),
		Git:    gitCLI,
		Cloner: clone.New(gitCLI, paths),
		Runner: runner,
	}

	if opts.mcp {
		// claude starts a stdio server in the working directory of its session.
		if svc.CallerDir, err = os.Getwd(); err != nil {
			return err
		}
		return mcp.Serve(context.Background(), svc, eng.Name())
	}
	return tui.Run(tui.New(svc, cfg, opts.input, opts.dryRun))
}

func parseFlags(args []string) (flags, error) {
	var opts flags
	var rest []string

	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func(name string) (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s needs a value", name)
			}
			i++
			return args[i], nil
		}

		switch arg {
		case "-h", "--help":
			opts.help = true
		case "--dry-run":
			opts.dryRun = true
		case "--engine":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.engine = value
		case "--home":
			value, err := next(arg)
			if err != nil {
				return opts, err
			}
			opts.home = value
		default:
			if len(arg) > 1 && arg[0] == '-' {
				return opts, fmt.Errorf("unknown flag %s", arg)
			}
			rest = append(rest, arg)
		}
	}

	if len(rest) > 0 && rest[0] == "mcp" {
		opts.mcp = true
		rest = rest[1:]
		if len(rest) > 0 {
			return opts, errors.New("mcp takes no pull request")
		}
		// Every tool writes to the index or starts a session. A server that
		// reported either without doing it would mislead the agent.
		if opts.dryRun {
			return opts, errors.New("mcp does not run in a dry run")
		}
	}
	if len(rest) > 1 {
		return opts, fmt.Errorf("expected at most one pull request, got %d", len(rest))
	}
	if len(rest) == 1 {
		opts.input = rest[0]
	}
	return opts, nil
}

// requireTools fails early on a missing tool, rather than inside a session the
// user already started. It makes no network call, so the dashboard opens offline
// and an authentication problem surfaces as the agent's own output.
func requireTools(extra ...string) error {
	for _, bin := range append([]string{"git", "gh"}, extra...) {
		if _, err := osexec.LookPath(bin); err != nil {
			return fmt.Errorf("%s is not on PATH", bin)
		}
	}
	return nil
}

const usage = `docket runs a pull request review in a claude or codex session.

Usage:
  docket [flags] [pull request]
  docket [flags] mcp

The pull request can be a URL, org/repo#123, or a bare number when default_repo
is set in config.toml. Given one, docket opens the new review screen with the
field filled.

docket mcp serves the Model Context Protocol on stdin and stdout, so an agent
session can start background reviews, list them, and submit them. Register it
with: claude mcp add docket -- docket mcp

Flags:
  --engine <name>   agent to run the review in (default from config.toml)
  --home <dir>      docket's state directory (default $DOCKET_HOME or ~/.docket)
  --dry-run         report the commands that would run, start and record nothing
  -h, --help        show this help
`

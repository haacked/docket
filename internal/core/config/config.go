// Package config locates docket's own directory and reads config.toml.
package config

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// DefaultReviewCodeDir is where review-code's install.sh puts the skill. It is a
// default, not a constant docket depends on.
const DefaultReviewCodeDir = "~/.agents/skills/review-code"

// DefaultCodexSessionsDir is where codex records a session. docket reads it to
// recover the id of a session it just ran, because interactive codex takes no
// session id on the command line.
const DefaultCodexSessionsDir = "~/.codex/sessions"

// DefaultClaudeConfigDir is where claude keeps its login, its conversations,
// and its background jobs when CLAUDE_CONFIG_DIR is unset.
const DefaultClaudeConfigDir = "~/.claude"

// EngineClaude is the engine docket uses when config.toml names none.
const EngineClaude = "claude"

// The values of default_run. The new review screen starts a review where
// default_run says, unless the engine has no background mode.
const (
	RunBackground = "background"
	RunTerminal   = "terminal"
)

// Config is config.toml.
type Config struct {
	ReviewCodeDir    string `toml:"review_code_dir"`
	CodexSessionsDir string `toml:"codex_sessions_dir"`
	// ClaudeConfigDir is the CLAUDE_CONFIG_DIR a new review runs claude under.
	// The variable picks the account. Empty means claude's own default. docket then
	// unsets the variable rather than setting it to DefaultClaudeConfigDir,
	// because claude looks up a different keychain entry whenever it is set.
	ClaudeConfigDir string `toml:"claude_config_dir"`
	// ClaudeDefaultDir is DefaultClaudeConfigDir with the tilde expanded. Load
	// fills it in. The file never sets it.
	ClaudeDefaultDir string `toml:"-"`
	// ReviewCodeWorktreeDir is $REVIEW_CODE_WORKTREE_DIR with the tilde expanded,
	// or empty when it is unset. review-code keeps its worktrees and its
	// per-repository locks there when it is set. Load fills it in. The file never
	// sets it.
	ReviewCodeWorktreeDir string `toml:"-"`
	// ReviewCodeReviewDir is $REVIEW_CODE_REVIEW_DIR with the tilde expanded, or
	// empty when it is unset. review-code writes its notes there when it is set.
	// Load fills it in. The file never sets it.
	ReviewCodeReviewDir string `toml:"-"`
	DefaultEngine       string `toml:"default_engine"`
	DefaultRun          string `toml:"default_run"`
	GitHubUser          string `toml:"github_user"`
	DefaultRepo         string `toml:"default_repo"`
	// Teams are the "org/team" slugs whose review requests the requests screen
	// lists alongside the ones that name the user. The teams screen writes it.
	Teams []string `toml:"teams"`
	// FixAuthors are the pull request authors a new review fixes rather than
	// drafting a review for, such as "app/posthog". A GitHub App matches as
	// "app/<name>" or "<name>[bot]".
	FixAuthors []string `toml:"fix_authors"`
}

// Paths are the files and directories under DOCKET_HOME.
type Paths struct {
	Home    string
	Index   string
	Lock    string
	Clones  string
	Scratch string
	Config  string
	// Worktrees holds the tier-3 worktrees docket adds to repos.conf clones.
	Worktrees string
}

// NewPaths resolves DOCKET_HOME, falling back to ~/.docket.
func NewPaths(home string) (Paths, error) {
	home = cmp.Or(home, os.Getenv("DOCKET_HOME"))
	if home == "" {
		userHome, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, fmt.Errorf("no home directory: %w", err)
		}
		home = filepath.Join(userHome, ".docket")
	}
	home = ExpandHome(home)
	// The index stores these paths, so a relative home would resolve against
	// whatever directory docket happened to start in.
	abs, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, fmt.Errorf("resolve %s: %w", home, err)
	}
	home = abs
	return Paths{
		Home:      home,
		Index:     filepath.Join(home, "index.jsonl"),
		Lock:      filepath.Join(home, "index.lock"),
		Clones:    filepath.Join(home, "clones"),
		Scratch:   filepath.Join(home, "scratch"),
		Config:    filepath.Join(home, "config.toml"),
		Worktrees: filepath.Join(home, "worktrees"),
	}, nil
}

// EnsureDirs creates the directories docket writes to.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.Home, p.Clones, p.Scratch, p.Worktrees} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// ClaudeJobsDir is where claude's background service keeps a status file for
// each background session run under configDir. An empty configDir is claude's
// default account. The result is "" for that account on a Config that Load did
// not fill in.
func (c Config) ClaudeJobsDir(configDir string) string {
	dir := cmp.Or(configDir, c.ClaudeDefaultDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "jobs")
}

// Inside returns dir made absolute when it lies inside root, and an error when
// it is root itself or anywhere outside it. Callers delete what it returns, so
// root never counts.
func Inside(root, dir string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", root, err)
	}
	target, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dir, err)
	}
	rel, err := filepath.Rel(absRoot, target)
	if err != nil || rel == "." || !filepath.IsLocal(rel) {
		return "", fmt.Errorf("%s is not inside %s", target, absRoot)
	}
	return target, nil
}

// FixWorktreeDir is where the tier-3 worktree of one pull request lives.
// Config.WorktreeDir is review-code's own worktree, which is another directory.
func (p Paths) FixWorktreeDir(org, repo string, number int) string {
	return filepath.Join(p.Worktrees, org, repo, fmt.Sprintf("pr-%d", number))
}

// CloneDir is where a tier-2 clone of one pull request lives.
func (p Paths) CloneDir(org, repo string, number int) string {
	return filepath.Join(p.Clones, org, repo, fmt.Sprintf("pr-%d", number))
}

// Load reads config.toml and fills in defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Config{
		ReviewCodeDir:    DefaultReviewCodeDir,
		CodexSessionsDir: DefaultCodexSessionsDir,
		DefaultEngine:    EngineClaude,
		DefaultRun:       RunBackground,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return checkClaudeConfigDir(path, expand(cfg))
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	// This runs right after unmarshal, before expand fills in a default.
	// Unmarshal leaves an omitted key at the literal's default, so it always
	// passes here. An explicit empty string overwrites that default, so it
	// fails here.
	if cfg.DefaultRun != RunBackground && cfg.DefaultRun != RunTerminal {
		return cfg, fmt.Errorf("%s: default_run is %q, want %q or %q", path, cfg.DefaultRun, RunBackground, RunTerminal)
	}
	return checkClaudeConfigDir(path, expand(cfg))
}

// checkClaudeConfigDir refuses a relative claude config dir. docket runs some
// claude commands in the review's directory and others in its own, so a
// relative dir would name a different account for each.
func checkClaudeConfigDir(path string, cfg Config) (Config, error) {
	if cfg.ClaudeConfigDir != "" && !filepath.IsAbs(cfg.ClaudeConfigDir) {
		return cfg, fmt.Errorf("%s: the claude config dir %q from claude_config_dir or CLAUDE_CONFIG_DIR is relative; give an absolute path or one that starts with ~", path, cfg.ClaudeConfigDir)
	}
	return cfg, nil
}

// expand fills in the defaults a config.toml left out and resolves the ~ in
// every path, so nothing downstream has to.
func expand(cfg Config) Config {
	cfg.ReviewCodeDir = ExpandHome(cmp.Or(cfg.ReviewCodeDir, DefaultReviewCodeDir))
	cfg.CodexSessionsDir = ExpandHome(cmp.Or(cfg.CodexSessionsDir, DefaultCodexSessionsDir))
	cfg.ClaudeConfigDir = ExpandHome(cmp.Or(cfg.ClaudeConfigDir, os.Getenv("CLAUDE_CONFIG_DIR")))
	cfg.ClaudeDefaultDir = ExpandHome(DefaultClaudeConfigDir)
	cfg.ReviewCodeWorktreeDir = ExpandHome(os.Getenv("REVIEW_CODE_WORKTREE_DIR"))
	cfg.ReviewCodeReviewDir = ExpandHome(os.Getenv("REVIEW_CODE_REVIEW_DIR"))
	cfg.DefaultEngine = cmp.Or(cfg.DefaultEngine, EngineClaude)
	cfg.DefaultRun = cmp.Or(cfg.DefaultRun, RunBackground)
	return cfg
}

// SaveKey sets one key in config.toml and keeps every other key as the file has
// it. A default the file leaves out stays out of it. An edit to another key made
// while docket runs survives. The rewrite drops the file's comments.
func SaveKey(path, key string, value any) error {
	values := map[string]any{}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := toml.Unmarshal(data, &values); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("read %s: %w", path, err)
	}
	values[key] = value
	return write(path, values)
}

// write replaces config.toml through a temporary file and a rename. A reader
// never sees half a file.
func write(path string, v map[string]any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := toml.NewEncoder(f).Encode(v); err != nil {
		f.Close()
		return fmt.Errorf("encode config: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close temp config: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

// RunsInBackground reports whether the new review screen starts a review in the
// background. It reads an empty DefaultRun as RunBackground, which is the value
// expand fills in.
func (c Config) RunsInBackground() bool {
	return c.DefaultRun != RunTerminal
}

// ReposConfPath is review-code's repos.conf.
func (c Config) ReposConfPath() string {
	return filepath.Join(c.ReviewCodeDir, "repos.conf")
}

// The directories review-code keeps under its installed skill.
func (c Config) ReviewsDir() string {
	return cmp.Or(c.ReviewCodeReviewDir, filepath.Join(c.ReviewCodeDir, ".reviews"))
}
func (c Config) WorktreesDir() string {
	return cmp.Or(c.ReviewCodeWorktreeDir, filepath.Join(c.ReviewCodeDir, ".worktrees"))
}
func (c Config) SessionsDir() string { return filepath.Join(c.ReviewCodeDir, ".sessions") }

// AgentDirs are the directories an agent has to be able to write to, beyond the
// one it runs in. review-code keeps its notes, worktrees, and session state
// outside the working root, and codex sandboxes writes to that root.
func (c Config) AgentDirs() []string {
	return []string{c.SessionsDir(), c.WorktreesDir(), c.ReviewsDir()}
}

// NotesPath is where review-code writes the review for a pull request.
func (c Config) NotesPath(org, repo string, number int) string {
	return filepath.Join(c.ReviewsDir(), org, repo, fmt.Sprintf("pr-%d.md", number))
}

// WorktreeDir is where review-code provisions its tier-1 worktree. docket
// reports it and never deletes it. review-code lowercases the org and the repo
// when it builds this path, so docket does too: a mixed-case ref otherwise names
// a directory that is not there on a case-sensitive filesystem.
func (c Config) WorktreeDir(org, repo string, number int) string {
	return filepath.Join(c.WorktreesDir(), strings.ToLower(org), strings.ToLower(repo), fmt.Sprintf("pr-%d", number))
}

// ExpandHome turns a leading ~ into the home directory.
func ExpandHome(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(path, "~"), "/"))
}

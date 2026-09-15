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

// EngineClaude is the engine docket uses when config.toml names none.
const EngineClaude = "claude"

// Config is config.toml.
type Config struct {
	ReviewCodeDir string `toml:"review_code_dir"`
	DefaultEngine string `toml:"default_engine"`
	GitHubUser    string `toml:"github_user"`
	DefaultRepo   string `toml:"default_repo"`
}

// Paths are the files and directories under DOCKET_HOME.
type Paths struct {
	Home    string
	Index   string
	Lock    string
	Clones  string
	Scratch string
	Config  string
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
		Home:    home,
		Index:   filepath.Join(home, "index.jsonl"),
		Lock:    filepath.Join(home, "index.lock"),
		Clones:  filepath.Join(home, "clones"),
		Scratch: filepath.Join(home, "scratch"),
		Config:  filepath.Join(home, "config.toml"),
	}, nil
}

// EnsureDirs creates the directories docket writes to.
func (p Paths) EnsureDirs() error {
	for _, dir := range []string{p.Home, p.Clones, p.Scratch} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s: %w", dir, err)
		}
	}
	return nil
}

// CloneDir is where a tier-2 clone of one pull request lives.
func (p Paths) CloneDir(org, repo string, number int) string {
	return filepath.Join(p.Clones, org, repo, fmt.Sprintf("pr-%d", number))
}

// Load reads config.toml and fills in defaults. A missing file is not an error.
func Load(path string) (Config, error) {
	cfg := Config{
		ReviewCodeDir: DefaultReviewCodeDir,
		DefaultEngine: EngineClaude,
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			cfg.ReviewCodeDir = ExpandHome(cfg.ReviewCodeDir)
			return cfg, nil
		}
		return cfg, fmt.Errorf("read %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg.ReviewCodeDir = ExpandHome(cmp.Or(cfg.ReviewCodeDir, DefaultReviewCodeDir))
	cfg.DefaultEngine = cmp.Or(cfg.DefaultEngine, EngineClaude)
	return cfg, nil
}

// Save writes config.toml. docket calls it to cache the GitHub login.
func Save(path string, cfg Config) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".config.toml.*")
	if err != nil {
		return fmt.Errorf("create temp config: %w", err)
	}
	tmp := f.Name()
	defer os.Remove(tmp)

	if err := toml.NewEncoder(f).Encode(cfg); err != nil {
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

// ReposConfPath is review-code's repos.conf.
func (c Config) ReposConfPath() string {
	return filepath.Join(c.ReviewCodeDir, "repos.conf")
}

// NotesPath is where review-code writes the review for a pull request.
func (c Config) NotesPath(org, repo string, number int) string {
	return filepath.Join(c.ReviewCodeDir, ".reviews", org, repo, fmt.Sprintf("pr-%d.md", number))
}

// WorktreeDir is where review-code provisions its tier-1 worktree. docket
// reports it and never deletes it. review-code lowercases the org and the repo
// when it builds this path, so docket does too: a mixed-case ref otherwise names
// a directory that is not there on a case-sensitive filesystem.
func (c Config) WorktreeDir(org, repo string, number int) string {
	return filepath.Join(c.ReviewCodeDir, ".worktrees", strings.ToLower(org), strings.ToLower(repo), fmt.Sprintf("pr-%d", number))
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

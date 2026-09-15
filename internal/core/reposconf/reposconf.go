// Package reposconf reads review-code's repos.conf, which maps org/repo to a
// local clone. docket reimplements the lookup so it never sources review-code's
// bash.
package reposconf

import (
	"bufio"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/haacked/docket/internal/core/config"
)

// Entry is one org/repo to path mapping, with the key as it was written.
type Entry struct {
	Key  string
	Path string
}

// Parse reads the flat, whitespace-separated format. It skips comments and
// blank lines, and ignores lines with no path.
func Parse(r io.Reader) []Entry {
	var entries []Entry
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.IndexFunc(line, unicode.IsSpace)
		if i < 0 {
			continue
		}
		path := strings.TrimSpace(line[i:])
		if path == "" {
			continue
		}
		entries = append(entries, Entry{Key: line[:i], Path: path})
	}
	return entries
}

// ParseFile returns no entries when the file is missing or unreadable. That is
// how review-code treats it, so the review falls back to diff-only rather than
// failing.
func ParseFile(path string) []Entry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	return Parse(f)
}

// Resolve returns the local clone path for org/repo, or "" when there is none.
// The match is case-insensitive, a leading ~ expands to the home directory, and
// a relative path is rejected because it would resolve against whatever
// directory the caller happens to be in. isRepo decides whether the path holds
// a git repository.
func Resolve(entries []Entry, org, repo string, isRepo func(string) bool) string {
	if org == "" || repo == "" {
		return ""
	}
	key := org + "/" + repo
	for _, e := range entries {
		if !strings.EqualFold(e.Key, key) {
			continue
		}
		path := config.ExpandHome(e.Path)
		if !filepath.IsAbs(path) {
			return ""
		}
		if isRepo(path) {
			return path
		}
		return ""
	}
	return ""
}

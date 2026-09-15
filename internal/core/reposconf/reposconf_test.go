package reposconf_test

import (
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/reposconf"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  []reposconf.Entry
	}{
		{
			name:  "empty input yields no entries",
			input: "",
			want:  nil,
		},
		{
			name:  "single entry",
			input: "acme/tool /srv/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "last line without trailing newline",
			input: "acme/tool /srv/acme/tool",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "comment lines are skipped",
			input: "# repos.conf header\nacme/tool /srv/acme/tool\n#acme/other /srv/other\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "indented comment lines are skipped",
			input: "    # indented comment\n\t# tab indented comment\nacme/tool /srv/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "blank and whitespace only lines are skipped",
			input: "\nacme/tool /srv/acme/tool\n   \n\t\nacme/other /srv/other\n\n",
			want: []reposconf.Entry{
				{Key: "acme/tool", Path: "/srv/acme/tool"},
				{Key: "acme/other", Path: "/srv/other"},
			},
		},
		{
			name:  "split on the first run of whitespace collapses repeated spaces",
			input: "acme/tool     /srv/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "tab separates key from path",
			input: "acme/tool\t/srv/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "mixed tab and space separator",
			input: "acme/tool \t  /srv/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "leading whitespace before the key is trimmed",
			input: "   acme/tool /srv/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "trailing whitespace after the path is trimmed",
			input: "acme/tool /srv/acme/tool   \n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
		},
		{
			name:  "key keeps its verbatim case",
			input: "PostHog/PostHog /srv/posthog\n",
			want:  []reposconf.Entry{{Key: "PostHog/PostHog", Path: "/srv/posthog"}},
		},
		{
			name:  "tilde path is kept unexpanded",
			input: "acme/tool ~/dev/acme/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "~/dev/acme/tool"}},
		},
		{
			name:  "path keeps interior spaces",
			input: "acme/tool /srv/my repos/acme tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/my repos/acme tool"}},
		},
		{
			name:  "hash inside a path is not a comment",
			input: "acme/tool /srv/acme#1/tool\n",
			want:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme#1/tool"}},
		},
		{
			name:  "line with no whitespace has no path and is skipped",
			input: "acme/tool\nacme/other /srv/other\n",
			want:  []reposconf.Entry{{Key: "acme/other", Path: "/srv/other"}},
		},
		{
			name:  "duplicate keys are all kept in file order",
			input: "acme/tool /first\nacme/tool /second\n",
			want: []reposconf.Entry{
				{Key: "acme/tool", Path: "/first"},
				{Key: "acme/tool", Path: "/second"},
			},
		},
		{
			name:  "entries keep file order",
			input: "c/c /three\na/a /one\nb/b /two\n",
			want: []reposconf.Entry{
				{Key: "c/c", Path: "/three"},
				{Key: "a/a", Path: "/one"},
				{Key: "b/b", Path: "/two"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reposconf.Parse(strings.NewReader(tt.input))

			if len(got) != len(tt.want) {
				t.Fatalf("Parse(%q) returned %d entries (%+v), want %d (%+v)", tt.input, len(got), got, len(tt.want), tt.want)
			}
			if len(tt.want) > 0 && !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse(%q) = %+v, want %+v", tt.input, got, tt.want)
			}
		})
	}
}

func allRepos(string) bool { return true }

func noRepos(string) bool { return false }

func reposAt(paths ...string) func(string) bool {
	return func(candidate string) bool {
		for _, p := range paths {
			if p == candidate {
				return true
			}
		}
		return false
	}
}

func TestResolve(t *testing.T) {
	tests := []struct {
		name    string
		entries []reposconf.Entry
		org     string
		repo    string
		isRepo  func(string) bool
		want    string
	}{
		{
			name:    "exact match on a git repo returns the path",
			entries: []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "/srv/acme/tool",
		},
		{
			name:    "no entries returns empty",
			entries: nil,
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "",
		},
		{
			name:    "unmatched key returns empty",
			entries: []reposconf.Entry{{Key: "acme/other", Path: "/srv/other"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "",
		},
		{
			name:    "org alone does not match",
			entries: []reposconf.Entry{{Key: "acme", Path: "/srv/acme"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "",
		},
		{
			name:    "entry key case is ignored",
			entries: []reposconf.Entry{{Key: "PostHog/PostHog", Path: "/srv/posthog"}},
			org:     "posthog",
			repo:    "posthog",
			isRepo:  allRepos,
			want:    "/srv/posthog",
		},
		{
			name:    "query case is ignored",
			entries: []reposconf.Entry{{Key: "posthog/posthog", Path: "/srv/posthog"}},
			org:     "PostHog",
			repo:    "PostHog",
			isRepo:  allRepos,
			want:    "/srv/posthog",
		},
		{
			name:    "path that is not a git repo returns empty",
			entries: []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  noRepos,
			want:    "",
		},
		{
			name:    "relative path is rejected",
			entries: []reposconf.Entry{{Key: "acme/tool", Path: "dev/acme/tool"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "",
		},
		{
			name:    "dot relative path is rejected",
			entries: []reposconf.Entry{{Key: "acme/tool", Path: "./dev/acme/tool"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "",
		},
		{
			name:    "parent relative path is rejected",
			entries: []reposconf.Entry{{Key: "acme/tool", Path: "../acme/tool"}},
			org:     "acme",
			repo:    "tool",
			isRepo:  allRepos,
			want:    "",
		},
		{
			name: "first matching entry wins",
			entries: []reposconf.Entry{
				{Key: "acme/tool", Path: "/srv/first"},
				{Key: "acme/tool", Path: "/srv/second"},
			},
			org:    "acme",
			repo:   "tool",
			isRepo: allRepos,
			want:   "/srv/first",
		},
		{
			name: "first match that is not a git repo does not fall through to a later duplicate",
			entries: []reposconf.Entry{
				{Key: "acme/tool", Path: "/srv/first"},
				{Key: "acme/tool", Path: "/srv/second"},
			},
			org:    "acme",
			repo:   "tool",
			isRepo: reposAt("/srv/second"),
			want:   "",
		},
		{
			name: "match is found after non matching entries",
			entries: []reposconf.Entry{
				{Key: "other/one", Path: "/srv/one"},
				{Key: "other/two", Path: "/srv/two"},
				{Key: "acme/tool", Path: "/srv/acme/tool"},
			},
			org:    "acme",
			repo:   "tool",
			isRepo: allRepos,
			want:   "/srv/acme/tool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reposconf.Resolve(tt.entries, tt.org, tt.repo, tt.isRepo)

			if got != tt.want {
				t.Errorf("Resolve(%+v, %q, %q, …) = %q, want %q", tt.entries, tt.org, tt.repo, got, tt.want)
			}
		})
	}
}

func TestResolveExpandsTildePrefixToHomeDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, "dev/acme/tool")
	entries := []reposconf.Entry{{Key: "acme/tool", Path: "~/dev/acme/tool"}}

	got := reposconf.Resolve(entries, "acme", "tool", allRepos)

	if got != want {
		t.Errorf("Resolve with path %q = %q, want %q", entries[0].Path, got, want)
	}
}

func TestResolveChecksTheExpandedPathForBeingAGitRepo(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, "dev/acme/tool")
	entries := []reposconf.Entry{{Key: "acme/tool", Path: "~/dev/acme/tool"}}
	var checked []string
	record := func(candidate string) bool {
		checked = append(checked, candidate)
		return true
	}

	reposconf.Resolve(entries, "acme", "tool", record)

	if !slices.Contains(checked, want) {
		t.Errorf("Resolve checked %q for being a git repo, want the expanded path %q to be checked", checked, want)
	}
	if slices.Contains(checked, entries[0].Path) {
		t.Errorf("Resolve checked the unexpanded path %q for being a git repo", entries[0].Path)
	}
}

package pr_test

import (
	"testing"

	"github.com/haacked/docket/internal/core/pr"
)

func TestParseRefAcceptsValidInput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		defaultRepo string
		want        pr.Ref
	}{
		{
			name:  "https pull url",
			input: "https://github.com/acme/tool/pull/123",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 123},
		},
		{
			name:  "http pull url",
			input: "http://github.com/acme/tool/pull/1",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 1},
		},
		{
			name:  "pull url with trailing slash",
			input: "https://github.com/acme/tool/pull/123/",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 123},
		},
		{
			name:  "pull url with files segment",
			input: "https://github.com/acme/tool/pull/123/files",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 123},
		},
		{
			name:  "pull url with nested extra segments",
			input: "https://github.com/acme/tool/pull/123/commits/0123456789abcdef0123456789abcdef01234567",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 123},
		},
		{
			name:  "pull url with dotted and dashed repo name",
			input: "https://github.com/PostHog/posthog-js.ext/pull/4567",
			want:  pr.Ref{Org: "PostHog", Repo: "posthog-js.ext", Number: 4567},
		},
		{
			name:  "pull url preserves owner and repo case",
			input: "https://github.com/PostHog/PostHog/pull/9",
			want:  pr.Ref{Org: "PostHog", Repo: "PostHog", Number: 9},
		},
		{
			name:  "shorthand org repo hash number",
			input: "acme/tool#7",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 7},
		},
		{
			name:        "shorthand wins over default repo",
			input:       "acme/tool#7",
			defaultRepo: "other/thing",
			want:        pr.Ref{Org: "acme", Repo: "tool", Number: 7},
		},
		{
			name:        "url wins over default repo",
			input:       "https://github.com/acme/tool/pull/8",
			defaultRepo: "other/thing",
			want:        pr.Ref{Org: "acme", Repo: "tool", Number: 8},
		},
		{
			name:        "bare number uses default repo",
			input:       "42",
			defaultRepo: "acme/tool",
			want:        pr.Ref{Org: "acme", Repo: "tool", Number: 42},
		},
		{
			name:        "bare number with multi digit value",
			input:       "30125",
			defaultRepo: "PostHog/posthog",
			want:        pr.Ref{Org: "PostHog", Repo: "posthog", Number: 30125},
		},
		{
			name:  "surrounding whitespace is ignored",
			input: "  https://github.com/acme/tool/pull/5\n",
			want:  pr.Ref{Org: "acme", Repo: "tool", Number: 5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pr.ParseRef(tt.input, tt.defaultRepo)

			if err != nil {
				t.Fatalf("ParseRef(%q, %q) returned error %v, want no error", tt.input, tt.defaultRepo, err)
			}
			if got != tt.want {
				t.Errorf("ParseRef(%q, %q) = %+v, want %+v", tt.input, tt.defaultRepo, got, tt.want)
			}
		})
	}
}

func TestParseRefRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		defaultRepo string
	}{
		{name: "empty input", input: ""},
		{name: "whitespace only input", input: "   "},
		{name: "empty input with default repo", input: "", defaultRepo: "acme/tool"},
		{name: "prose", input: "review the auth change please"},
		{name: "url without pull segment", input: "https://github.com/acme/tool"},
		{name: "url with pull but no number", input: "https://github.com/acme/tool/pull"},
		{name: "url with pull and empty number", input: "https://github.com/acme/tool/pull/"},
		{name: "url with non numeric pull number", input: "https://github.com/acme/tool/pull/abc"},
		{name: "url with zero pull number", input: "https://github.com/acme/tool/pull/0"},
		{name: "url with negative pull number", input: "https://github.com/acme/tool/pull/-1"},
		{name: "shorthand without number", input: "acme/tool"},
		{name: "shorthand with empty number", input: "acme/tool#"},
		{name: "shorthand with non numeric number", input: "acme/tool#abc"},
		{name: "shorthand with zero number", input: "acme/tool#0"},
		{name: "shorthand with negative number", input: "acme/tool#-3"},
		{name: "bare number without default repo", input: "12"},
		{name: "bare zero with default repo", input: "0", defaultRepo: "acme/tool"},
		{name: "bare negative number with default repo", input: "-5", defaultRepo: "acme/tool"},
		// The org and the repo become path segments under the clones directory, so
		// these keep git init and the fetch from running outside it.
		{name: "parent directory as the repo", input: "acme/..#1"},
		{name: "parent directory as the org", input: "https://github.com/../tool/pull/1"},
		{name: "current directory as the repo", input: "acme/.#1"},
		{name: "org starting with a dash", input: "-x/tool#1"},
		{name: "repo starting with a dash", input: "acme/-tool#1"},
		{name: "path separator inside the repo", input: "acme/a/b#1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pr.ParseRef(tt.input, tt.defaultRepo)

			if err == nil {
				t.Errorf("ParseRef(%q, %q) returned no error, want an error", tt.input, tt.defaultRepo)
			}
		})
	}
}

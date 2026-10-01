package tier_test

import (
	"path/filepath"
	"testing"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/reposconf"
	"github.com/haacked/docket/internal/core/tier"
)

func allRepos(string) bool { return true }

func noRepos(string) bool { return false }

func TestDecide(t *testing.T) {
	ref := pr.Ref{Org: "acme", Repo: "tool", Number: 123}

	tests := []struct {
		name     string
		ref      pr.Ref
		entries  []reposconf.Entry
		isRepo   func(string) bool
		wantTier tier.Tier
		wantDir  string
	}{
		{
			name:     "repo listed in repos conf is tier 1 with the local clone path",
			ref:      ref,
			entries:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
			isRepo:   allRepos,
			wantTier: tier.Tier1,
			wantDir:  "/srv/acme/tool",
		},
		{
			name:     "repo listed with different case is tier 1",
			ref:      pr.Ref{Org: "PostHog", Repo: "PostHog", Number: 9},
			entries:  []reposconf.Entry{{Key: "posthog/posthog", Path: "/srv/posthog"}},
			isRepo:   allRepos,
			wantTier: tier.Tier1,
			wantDir:  "/srv/posthog",
		},
		{
			name:     "first listed entry wins",
			ref:      ref,
			entries:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/first"}, {Key: "acme/tool", Path: "/srv/second"}},
			isRepo:   allRepos,
			wantTier: tier.Tier1,
			wantDir:  "/srv/first",
		},
		{
			name:     "repo missing from repos conf is tier 2 with no directory",
			ref:      ref,
			entries:  []reposconf.Entry{{Key: "other/thing", Path: "/srv/other/thing"}},
			isRepo:   allRepos,
			wantTier: tier.Tier2,
			wantDir:  "",
		},
		{
			name:     "empty repos conf is tier 2",
			ref:      ref,
			entries:  nil,
			isRepo:   allRepos,
			wantTier: tier.Tier2,
			wantDir:  "",
		},
		{
			name:     "listed path that is not a git repo is tier 2",
			ref:      ref,
			entries:  []reposconf.Entry{{Key: "acme/tool", Path: "/srv/acme/tool"}},
			isRepo:   noRepos,
			wantTier: tier.Tier2,
			wantDir:  "",
		},
		{
			name:     "listed path that is relative is tier 2",
			ref:      ref,
			entries:  []reposconf.Entry{{Key: "acme/tool", Path: "dev/acme/tool"}},
			isRepo:   allRepos,
			wantTier: tier.Tier2,
			wantDir:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTier, gotDir := tier.Decide(tt.ref, tt.entries, tt.isRepo)

			if gotTier != tt.wantTier {
				t.Errorf("Decide(%+v, %+v, …) tier = %v, want %v", tt.ref, tt.entries, gotTier, tt.wantTier)
			}
			if gotDir != tt.wantDir {
				t.Errorf("Decide(%+v, %+v, …) dir = %q, want %q", tt.ref, tt.entries, gotDir, tt.wantDir)
			}
		})
	}
}

func TestDecideReturnsTheExpandedTier1Path(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, "dev/acme/tool")
	entries := []reposconf.Entry{{Key: "acme/tool", Path: "~/dev/acme/tool"}}

	gotTier, gotDir := tier.Decide(pr.Ref{Org: "acme", Repo: "tool", Number: 1}, entries, allRepos)

	if gotTier != tier.Tier1 {
		t.Errorf("Decide with path %q tier = %v, want %v", entries[0].Path, gotTier, tier.Tier1)
	}
	if gotDir != want {
		t.Errorf("Decide with path %q dir = %q, want %q", entries[0].Path, gotDir, want)
	}
}

func TestTierString(t *testing.T) {
	tests := []struct {
		name string
		tier tier.Tier
		want string
	}{
		{name: "tier 1", tier: tier.Tier1, want: "tier1"},
		{name: "tier 2", tier: tier.Tier2, want: "tier2"},
		{name: "out of range", tier: tier.Tier(0), want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.tier.String(); got != tt.want {
				t.Errorf("Tier(%d).String() = %q, want %q", int(tt.tier), got, tt.want)
			}
		})
	}
}

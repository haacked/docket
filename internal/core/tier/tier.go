// Package tier decides how docket supplies the repository for a review.
package tier

import (
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/reposconf"
)

// Tier is the repository handling docket uses for one review.
type Tier int

const (
	// Tier1 means review-code already knows a local clone, so it provisions and
	// tears down its own worktree and docket clones nothing.
	Tier1 Tier = 1
	// Tier2 means review-code has no clone for this repo, so docket clones the
	// PR head itself to keep the review off the diff-only path.
	Tier2 Tier = 2
)

func (t Tier) String() string {
	switch t {
	case Tier1:
		return "tier1"
	case Tier2:
		return "tier2"
	default:
		return "unknown"
	}
}

// Decide returns the tier and, for Tier1, the local clone review-code will use.
func Decide(ref pr.Ref, entries []reposconf.Entry, isRepo func(string) bool) (Tier, string) {
	if clone := reposconf.Resolve(entries, ref.Org, ref.Repo, isRepo); clone != "" {
		return Tier1, clone
	}
	return Tier2, ""
}

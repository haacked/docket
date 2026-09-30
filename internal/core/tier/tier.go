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
	// Tier3 means review-code knows a local clone, and docket adds a worktree of
	// that clone on the pull request's head branch. A fix review edits the files.
	// docket removes the worktree when the record is archived or abandoned,
	// unless it holds fixes that are not on GitHub.
	//
	// A worktree shares the clone's objects, so git fetches only the files the
	// pull request changed, where a tier-2 clone checks out the whole repository.
	// The worktree also inherits the clone's config, so the commit hooks that
	// core.hooksPath names run when the user commits the fixes.
	Tier3 Tier = 3
)

func (t Tier) String() string {
	switch t {
	case Tier1:
		return "tier1"
	case Tier2:
		return "tier2"
	case Tier3:
		return "tier3"
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

// ForFix is the tier a fix review uses, given the tier Decide chose and whether
// the local clone already has a branch named like the head branch. A fix review
// edits the head branch, which review-code's own tier-1 worktree does not check
// out. git checks a branch out in one worktree only, so a taken branch means a
// clone.
func ForFix(decided Tier, branchTaken bool) Tier {
	switch {
	case decided != Tier1:
		return decided
	case branchTaken:
		return Tier2
	default:
		return Tier3
	}
}

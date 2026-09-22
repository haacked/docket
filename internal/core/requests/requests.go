// Package requests arranges the pull requests that ask for the user's review into
// the sections the requests screen draws. It reads nothing itself, so the screen
// can hold its types without reaching GitHub.
package requests

import (
	"slices"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// PR is one open pull request that asks for a review.
type PR struct {
	Ref       pr.Ref
	Title     string
	Author    string
	IsDraft   bool
	UpdatedAt time.Time
}

// Team is what one team's search returned.
type Team struct {
	Slug string
	PRs  []PR
}

// Fetched is what one refresh read from GitHub: the requests that name the user
// and each configured team's. It carries no records, so the root can regroup the
// screen when the index changes without searching GitHub again.
type Fetched struct {
	Mine  []PR
	Teams []Team
}

// Row is a pull request with the state of docket's open record for it. State is
// empty when docket has no open record.
type Row struct {
	PR
	State review.State
}

// Section is one heading on the screen. Team is empty for the requests that name
// the user.
type Section struct {
	Team string
	Rows []Row
}

// Group builds the requests that name the user as the first section and then a
// section per team in the order given. A pull request that asks for both the user
// and a team appears only in the first section, and one that asks for two teams
// appears only under the first of them. Each section lists the most recently
// updated pull request first.
func Group(f Fetched, records []review.Record) []Section {
	var seen []pr.Ref
	section := func(team string, prs []PR) Section {
		s := Section{Team: team}
		for _, p := range prs {
			if slices.ContainsFunc(seen, p.Ref.Equal) {
				continue
			}
			seen = append(seen, p.Ref)
			s.Rows = append(s.Rows, Row{PR: p, State: openState(p.Ref, records)})
		}
		slices.SortStableFunc(s.Rows, func(a, b Row) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
		return s
	}

	sections := []Section{section("", f.Mine)}
	for _, t := range f.Teams {
		sections = append(sections, section(t.Slug, t.PRs))
	}
	return sections
}

// openState is the state of the open record for ref. The index holds one record
// per review. A pull request that was reviewed, archived, and asked for again has
// two records, and only the open one counts.
func openState(ref pr.Ref, records []review.Record) review.State {
	for _, rec := range records {
		if rec.State.Open() && rec.Ref.Equal(ref) {
			return rec.State
		}
	}
	return ""
}

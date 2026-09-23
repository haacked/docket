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

// Team is what one team's search returned. Err is set when the search failed.
type Team struct {
	Slug string
	PRs  []PR
	Err  error
}

// Fetched is what one refresh read from GitHub: the requests that name the user
// and each configured team's. It carries no records, so the root can regroup the
// screen when the index changes without searching GitHub again.
type Fetched struct {
	Mine  []PR
	Teams []Team
}

// Row is a pull request with docket's open record for it. State and RecordID are
// empty when docket has no open record.
type Row struct {
	PR
	State    review.State
	RecordID string
}

// Section is one heading on the screen. Team is empty for the requests that name
// the user. Err is the reason the team's search failed.
type Section struct {
	Team string
	Rows []Row
	Err  error
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
			row := Row{PR: p}
			if rec, ok := openRecord(p.Ref, records); ok {
				row.State, row.RecordID = rec.State, rec.ID
			}
			s.Rows = append(s.Rows, row)
		}
		slices.SortStableFunc(s.Rows, func(a, b Row) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
		return s
	}

	sections := []Section{section("", f.Mine)}
	for _, t := range f.Teams {
		s := section(t.Slug, t.PRs)
		s.Err = t.Err
		sections = append(sections, s)
	}
	return sections
}

// openRecord is the open record for ref. The index holds one record per review.
// A pull request that was reviewed, archived, and asked for again has two
// records, and only the open one counts.
func openRecord(ref pr.Ref, records []review.Record) (review.Record, bool) {
	for _, rec := range records {
		if rec.State.Open() && rec.Ref.Equal(ref) {
			return rec, true
		}
	}
	return review.Record{}, false
}

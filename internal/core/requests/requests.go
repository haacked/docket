// Package requests arranges the pull requests that ask for the user's review into
// the sections the requests screen draws. It reads nothing itself, so the screen
// can hold its types without reaching GitHub.
package requests

import (
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// PR is one open pull request that asks for a review.
type PR struct {
	Ref       pr.Ref
	Title     string
	Author    string
	Assignees []string
	IsDraft   bool
	UpdatedAt time.Time
}

// Team is what one team's search returned. Err is set when the search failed,
// and PRs is then empty. Err is also set when docket could not tell which pull
// requests the user already reviewed, and PRs then keeps them.
type Team struct {
	Slug string
	PRs  []PR
	Err  error
}

// Fetched is what one refresh read from GitHub: the requests that name the user
// and each configured team's. Login is the user the searches named. It carries no
// records, so the root can regroup the screen when the index changes without
// searching GitHub again.
type Fetched struct {
	Login string
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
// the user. Err is the team's Err.
type Section struct {
	Team   string
	Groups []AssigneeGroup
	Err    error
}

// Rows lists the section's rows in the order the screen draws them.
func (s Section) Rows() []Row {
	var rows []Row
	for _, g := range s.Groups {
		rows = append(rows, g.Rows...)
	}
	return rows
}

// AssigneeGroup is the rows of one section that share an assignee. Mine marks
// the pull requests assigned to the user. Assignee is empty for those and for the
// pull requests assigned to nobody.
type AssigneeGroup struct {
	Mine     bool
	Assignee string
	Rows     []Row
}

// Group builds the requests that name the user as the first section and then a
// section per team in the order given. A pull request that asks for both the user
// and a team appears only in the first section, and one that asks for two teams
// appears only under the first of them. Each section groups its rows by assignee
// (see byAssignee).
func Group(f Fetched, records []review.Record) []Section {
	var seen []pr.Ref
	section := func(team string, prs []PR) Section {
		var rows []Row
		for _, p := range prs {
			if slices.ContainsFunc(seen, p.Ref.Equal) {
				continue
			}
			seen = append(seen, p.Ref)
			row := Row{PR: p}
			if rec, ok := review.OpenRecord(records, p.Ref); ok {
				row.State, row.RecordID = rec.State, rec.ID
			}
			rows = append(rows, row)
		}
		return Section{Team: team, Groups: byAssignee(rows, f.Login)}
	}

	sections := []Section{section("", f.Mine)}
	for _, t := range f.Teams {
		s := section(t.Slug, t.PRs)
		s.Err = t.Err
		sections = append(sections, s)
	}
	return sections
}

// byAssignee puts the pull requests assigned to me first, then the ones assigned
// to nobody, and then a group for each other assignee in alphabetical order. A
// pull request with several assignees appears only once: under me when I am one
// of them, and otherwise under the first assignee GitHub lists. Each group lists
// the most recently updated pull request first.
func byAssignee(rows []Row, me string) []AssigneeGroup {
	slices.SortStableFunc(rows, func(a, b Row) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	var mine, nobody []Row
	others := map[string][]Row{}
	for _, row := range rows {
		switch {
		case slices.ContainsFunc(row.Assignees, func(login string) bool { return strings.EqualFold(login, me) }):
			mine = append(mine, row)
		case len(row.Assignees) == 0:
			nobody = append(nobody, row)
		default:
			others[row.Assignees[0]] = append(others[row.Assignees[0]], row)
		}
	}

	var groups []AssigneeGroup
	if len(mine) > 0 {
		groups = append(groups, AssigneeGroup{Mine: true, Rows: mine})
	}
	if len(nobody) > 0 {
		groups = append(groups, AssigneeGroup{Rows: nobody})
	}
	caseless := func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }
	for _, login := range slices.SortedFunc(maps.Keys(others), caseless) {
		groups = append(groups, AssigneeGroup{Assignee: login, Rows: others[login]})
	}
	return groups
}

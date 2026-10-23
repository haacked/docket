// Package requests arranges the pull requests that ask for the user's review into
// the sections the requests screen draws. It reads nothing itself, so the screen
// can hold its types without reaching GitHub.
package requests

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// PR is one open pull request that asks for a review.
type PR struct {
	Ref    pr.Ref
	Title  string
	Author string
	// Bot marks a pull request that a bot account opened.
	Bot bool
	// PersonStarted marks a bot's pull request whose body says a person started
	// it (see StartedByAPerson).
	PersonStarted bool
	// StartedBy is the person who started a bot's pull request, which is the
	// first user the bot assigned to it. It stays empty unless PersonStarted is
	// set.
	StartedBy string
	Assignees []string
	IsDraft   bool
	UpdatedAt time.Time
}

// Owner is the person who answers for the pull request and so cannot review
// it: the author of a person's pull request, or the person who started a bot's.
// It is empty for a bot's pull request that no person started.
func (p PR) Owner() string {
	if p.Bot {
		return p.StartedBy
	}
	return p.Author
}

// personStarted is the line PostHog's self-driving bot writes into the body of
// a pull request that a person started. The bot assigns that person when it
// opens the pull request. A pull request the bot started by itself says
// "Task started by: auto-start" instead. Its assignee is the owner the bot
// chose, who reviews it.
const personStarted = "Task started by: a person"

// StartedByAPerson reports whether a bot's pull request body says a person
// started it.
func StartedByAPerson(body string) bool {
	return strings.Contains(body, personStarted)
}

// Team is what one team's search returned. Errs holds the search's failure, and
// PRs is then empty. Errs also holds a failure to tell which pull requests the
// user already reviewed, who started a bot's pull request, or who belongs to the
// team. PRs then keeps every pull request docket could not rule out.
type Team struct {
	Slug string
	// Members are the logins of the team's members.
	Members []string
	// MembersUnknown is true when the read of Members failed.
	MembersUnknown bool
	PRs            []PR
	Errs           []error
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

// Kind is the section a pull request belongs in.
type Kind int

const (
	// Yours holds the pull requests whose review is the user's: the request
	// names the user, the user is assigned, or docket has an open record.
	Yours Kind = iota
	// NeedsReviewer holds the team's pull requests that no member of the team
	// other than the owner is assigned to.
	NeedsReviewer
	// Covered holds the team's pull requests that a member of the team other
	// than the owner is assigned to.
	Covered
)

// Row is a pull request with docket's open record for it. State and RecordID are
// empty when docket has no open record.
type Row struct {
	PR
	State    review.State
	RecordID string
	// Team is the team whose request listed the pull request. It is empty when
	// the request names the user.
	Team       string
	AssignedMe bool
	// Teammate marks a pull request whose owner is a member of Team.
	Teammate bool
	// Reviewer is an assignee other than the owner. On a covered row it is the
	// member of Team who took the review. On a row that needs a reviewer it is
	// someone outside Team, or empty when nobody else is assigned.
	Reviewer string
	// MembersUnknown marks a row whose team's members docket could not read.
	// Teammate is then false and Reviewer may be a member of Team.
	MembersUnknown bool
}

// AskedMe reports whether the request names the user rather than a team.
func (r Row) AskedMe() bool { return r.Team == "" }

// Section is one heading on the screen.
type Section struct {
	Kind Kind
	Rows []Row
}

// List is what the requests screen draws: one section of each Kind in Kind
// order, how many teams were searched, and each team's Errs prefixed with the
// team's slug.
type List struct {
	Sections []Section
	Teams    int
	Errs     []error
}

// Group sorts each pull request into the section of its Kind. A pull request
// that asks for both the user and a team counts as asked of the user. One that
// asks for two teams counts under the first of them. Each section lists the
// most recently updated pull request first. NeedsReviewer lists the teammates'
// pull requests before the rest.
//
// The owner of a pull request is usually assigned to it, so only another
// assignee counts as its reviewer. A reviewer from outside the team
// leaves the team's review open.
func Group(f Fetched, records []review.Record) List {
	var seen []pr.Ref
	rows := make([][]Row, Covered+1)
	add := func(t Team) {
		for _, p := range t.PRs {
			if slices.ContainsFunc(seen, p.Ref.Equal) {
				continue
			}
			seen = append(seen, p.Ref)
			row := Row{PR: p, Team: t.Slug, AssignedMe: has(p.Assignees, f.Login), MembersUnknown: t.MembersUnknown}
			if rec, ok := review.OpenRecord(records, p.Ref); ok {
				row.State, row.RecordID = rec.State, rec.ID
			}
			kind := classify(&row, t.Members)
			rows[kind] = append(rows[kind], row)
		}
	}

	add(Team{PRs: f.Mine})
	list := List{Teams: len(f.Teams)}
	for _, t := range f.Teams {
		add(t)
		for _, err := range t.Errs {
			list.Errs = append(list.Errs, fmt.Errorf("%s: %w", t.Slug, err))
		}
	}

	for kind, section := range rows {
		teammatesFirst := Kind(kind) == NeedsReviewer
		slices.SortStableFunc(section, func(a, b Row) int {
			if teammatesFirst && a.Teammate != b.Teammate {
				if a.Teammate {
					return -1
				}
				return 1
			}
			return b.UpdatedAt.Compare(a.UpdatedAt)
		})
		list.Sections = append(list.Sections, Section{Kind: Kind(kind), Rows: section})
	}
	return list
}

// classify sets the row's Teammate and Reviewer from members, the logins of the
// team that asked, and returns the row's section.
func classify(row *Row, members []string) Kind {
	owner := row.Owner()
	row.Teammate = owner != "" && has(members, owner)
	if row.AskedMe() || row.AssignedMe || row.RecordID != "" {
		return Yours
	}
	// Any assignee may be the person who started it, so none counts as its
	// reviewer.
	if row.PersonStarted && row.StartedBy == "" {
		return NeedsReviewer
	}
	for _, login := range row.Assignees {
		if strings.EqualFold(login, owner) {
			continue
		}
		if has(members, login) {
			row.Reviewer = login
			return Covered
		}
		if row.Reviewer == "" {
			row.Reviewer = login
		}
	}
	return NeedsReviewer
}

// has reports whether logins holds login. GitHub compares logins without case,
// and the user types github_user in config.toml by hand.
func has(logins []string, login string) bool {
	return slices.ContainsFunc(logins, func(l string) bool { return strings.EqualFold(l, login) })
}

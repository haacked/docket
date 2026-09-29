package requests

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

var base = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

func request(org, repo string, number int, hoursAgo int) PR {
	ref := pr.Ref{Org: org, Repo: repo, Number: number}
	return PR{Ref: ref, Title: ref.String(), UpdatedAt: base.Add(-time.Duration(hoursAgo) * time.Hour)}
}

func assigned(p PR, logins ...string) PR {
	p.Assignees = logins
	return p
}

func refs(rows []Row) []pr.Ref {
	out := make([]pr.Ref, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Ref)
	}
	return out
}

// GitHub compares owners and repositories without case, so a team search can
// name the pull request a personal search already returned in other letters.
func TestGroupListsAPullRequestAskedOfMeAndATeamOnlyUnderMe(t *testing.T) {
	f := Fetched{
		Mine:  []PR{request("o", "r", 1, 0)},
		Teams: []Team{{Slug: "o/team", PRs: []PR{request("O", "R", 1, 0), request("o", "r", 2, 0)}}},
	}

	sections := Group(f, nil)

	if len(sections) != 2 {
		t.Fatalf("sections = %d, want me and one team", len(sections))
	}
	if got := refs(sections[0].Rows()); len(got) != 1 || got[0].Number != 1 {
		t.Errorf("my section = %v, want #1", got)
	}
	if got := refs(sections[1].Rows()); len(got) != 1 || got[0].Number != 2 {
		t.Errorf("team section = %v, want only #2", got)
	}
}

func TestGroupListsAPullRequestAskedOfTwoTeamsOnlyUnderTheFirst(t *testing.T) {
	f := Fetched{
		Teams: []Team{
			{Slug: "o/zeta", PRs: []PR{request("o", "r", 1, 0)}},
			{Slug: "o/alpha", PRs: []PR{request("o", "r", 1, 0), request("o", "r", 2, 0)}},
		},
	}

	sections := Group(f, nil)

	if len(sections) != 3 {
		t.Fatalf("sections = %d, want me and two teams", len(sections))
	}
	if got := refs(sections[1].Rows()); len(got) != 1 || got[0].Number != 1 {
		t.Errorf("first team = %v, want #1", got)
	}
	if got := refs(sections[2].Rows()); len(got) != 1 || got[0].Number != 2 {
		t.Errorf("second team = %v, want only #2", got)
	}
}

func TestGroupKeepsATeamsSearchError(t *testing.T) {
	failure := errors.New("422")
	f := Fetched{Teams: []Team{{Slug: "o/typo", Err: failure}}}

	sections := Group(f, nil)

	if len(sections) != 2 || !errors.Is(sections[1].Err, failure) {
		t.Errorf("sections = %+v, want the team's section to carry its error", sections)
	}
}

func TestGroupPutsMeFirstAndTeamsInTheOrderGiven(t *testing.T) {
	f := Fetched{
		Mine: []PR{request("o", "r", 1, 0)},
		Teams: []Team{
			{Slug: "o/zeta", PRs: []PR{request("o", "r", 2, 0)}},
			{Slug: "o/alpha", PRs: []PR{request("o", "r", 3, 0)}},
		},
	}

	sections := Group(f, nil)

	var teams []string
	for _, s := range sections {
		teams = append(teams, s.Team)
	}
	want := []string{"", "o/zeta", "o/alpha"}
	if len(teams) != len(want) || teams[0] != want[0] || teams[1] != want[1] || teams[2] != want[2] {
		t.Errorf("section teams = %q, want %q", teams, want)
	}
}

func TestGroupSortsEachGroupByMostRecentlyUpdated(t *testing.T) {
	f := Fetched{Mine: []PR{request("o", "r", 1, 5), request("o", "r", 2, 1), request("o", "r", 3, 3)}}

	rows := Group(f, nil)[0].Rows()

	got := refs(rows)
	if len(got) != 3 || got[0].Number != 2 || got[1].Number != 3 || got[2].Number != 1 {
		t.Errorf("order = %v, want #2, #3, #1", got)
	}
}

func TestGroupAttachesTheStateOfAnOpenRecord(t *testing.T) {
	f := Fetched{Mine: []PR{request("o", "r", 1, 0), request("o", "r", 2, 1)}}
	records := []review.Record{{ID: "a", Ref: pr.Ref{Org: "o", Repo: "r", Number: 1}, State: review.StateDrafted}}

	rows := Group(f, records)[0].Rows()

	if rows[0].State != review.StateDrafted || rows[0].RecordID != "a" {
		t.Errorf("#1 = %q on %q, want drafted on record a", rows[0].State, rows[0].RecordID)
	}
	if rows[1].State != "" || rows[1].RecordID != "" {
		t.Errorf("#2 = %q on %q, want no record", rows[1].State, rows[1].RecordID)
	}
}

// A pull request reviewed once, archived, and asked for again is a new request,
// so the old record must not make the row look taken.
func TestGroupIgnoresClosedRecords(t *testing.T) {
	for _, state := range []review.State{review.StateArchived, review.StateAbandoned} {
		t.Run(string(state), func(t *testing.T) {
			f := Fetched{Mine: []PR{request("o", "r", 1, 0)}}
			records := []review.Record{{ID: "a", Ref: pr.Ref{Org: "o", Repo: "r", Number: 1}, State: state}}

			rows := Group(f, records)[0].Rows()

			if rows[0].State != "" {
				t.Errorf("state = %q, want none from a %s record", rows[0].State, state)
			}
		})
	}
}

func TestGroupPrefersTheOpenRecordOverAnArchivedOne(t *testing.T) {
	ref := pr.Ref{Org: "o", Repo: "r", Number: 1}
	f := Fetched{Mine: []PR{request("o", "r", 1, 0)}}
	records := []review.Record{
		{ID: "old", Ref: ref, State: review.StateArchived},
		{ID: "new", Ref: ref, State: review.StateReviewing},
	}

	rows := Group(f, records)[0].Rows()

	if rows[0].State != review.StateReviewing || rows[0].RecordID != "new" {
		t.Errorf("row = %q on %q, want reviewing on the open record", rows[0].State, rows[0].RecordID)
	}
}

func TestGroupPutsMineFirstThenUnassignedThenOthersByLogin(t *testing.T) {
	f := Fetched{
		Login: "haacked",
		Teams: []Team{{Slug: "o/team", PRs: []PR{
			assigned(request("o", "r", 1, 0), "zed"),
			request("o", "r", 2, 0),
			assigned(request("o", "r", 3, 0), "Alice"),
			assigned(request("o", "r", 4, 0), "haacked"),
			assigned(request("o", "r", 5, 0), "bob"),
		}}},
	}

	groups := Group(f, nil)[1].Groups

	type group struct {
		mine     bool
		assignee string
		number   int
	}
	var got []group
	for _, g := range groups {
		for _, row := range g.Rows {
			got = append(got, group{g.Mine, g.Assignee, row.Ref.Number})
		}
	}
	want := []group{{true, "", 4}, {false, "", 2}, {false, "Alice", 3}, {false, "bob", 5}, {false, "zed", 1}}
	if !slices.Equal(got, want) {
		t.Errorf("groups = %+v, want %+v", got, want)
	}
}

// A row appears once, because a mark and the cursor both follow the pull request
// rather than its position.
func TestGroupListsAPullRequestWithSeveralAssigneesOnce(t *testing.T) {
	f := Fetched{
		Login: "haacked",
		Mine: []PR{
			assigned(request("o", "r", 1, 0), "alice", "haacked"),
			assigned(request("o", "r", 2, 0), "carol", "bob"),
		},
	}

	groups := Group(f, nil)[0].Groups

	if len(groups) != 2 {
		t.Fatalf("groups = %+v, want one for me and one for carol", groups)
	}
	if !groups[0].Mine || len(groups[0].Rows) != 1 || groups[0].Rows[0].Ref.Number != 1 {
		t.Errorf("first group = %+v, want #1 under me", groups[0])
	}
	if groups[1].Assignee != "carol" || len(groups[1].Rows) != 1 || groups[1].Rows[0].Ref.Number != 2 {
		t.Errorf("second group = %+v, want #2 under its first assignee", groups[1])
	}
}

// GitHub compares logins without case. The user types github_user in
// config.toml by hand, so its case can differ from GitHub's.
func TestGroupFindsMyAssignmentsWithoutCase(t *testing.T) {
	f := Fetched{Login: "Haacked", Mine: []PR{assigned(request("o", "r", 1, 0), "haacked")}}

	groups := Group(f, nil)[0].Groups

	if len(groups) != 1 || !groups[0].Mine {
		t.Errorf("groups = %+v, want #1 under me", groups)
	}
}

func TestGroupDrawsNoGroupForAnEmptySection(t *testing.T) {
	f := Fetched{Login: "haacked", Teams: []Team{{Slug: "o/team"}}}

	if groups := Group(f, nil)[1].Groups; len(groups) != 0 {
		t.Errorf("groups = %+v, want none", groups)
	}
}

package requests

import (
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
	if got := refs(sections[0].Rows); len(got) != 1 || got[0].Number != 1 {
		t.Errorf("my section = %v, want #1", got)
	}
	if got := refs(sections[1].Rows); len(got) != 1 || got[0].Number != 2 {
		t.Errorf("team section = %v, want only #2", got)
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

func TestGroupSortsEachSectionByMostRecentlyUpdated(t *testing.T) {
	f := Fetched{Mine: []PR{request("o", "r", 1, 5), request("o", "r", 2, 1), request("o", "r", 3, 3)}}

	rows := Group(f, nil)[0].Rows

	got := refs(rows)
	if len(got) != 3 || got[0].Number != 2 || got[1].Number != 3 || got[2].Number != 1 {
		t.Errorf("order = %v, want #2, #3, #1", got)
	}
}

func TestGroupAttachesTheStateOfAnOpenRecord(t *testing.T) {
	f := Fetched{Mine: []PR{request("o", "r", 1, 0), request("o", "r", 2, 1)}}
	records := []review.Record{{ID: "a", Ref: pr.Ref{Org: "o", Repo: "r", Number: 1}, State: review.StateDrafted}}

	rows := Group(f, records)[0].Rows

	if rows[0].State != review.StateDrafted {
		t.Errorf("#1 state = %q, want drafted", rows[0].State)
	}
	if rows[1].State != "" {
		t.Errorf("#2 state = %q, want none without a record", rows[1].State)
	}
}

// A pull request reviewed once, archived, and asked for again is a new request,
// so the old record must not make the row look taken.
func TestGroupIgnoresClosedRecords(t *testing.T) {
	for _, state := range []review.State{review.StateArchived, review.StateAbandoned} {
		t.Run(string(state), func(t *testing.T) {
			f := Fetched{Mine: []PR{request("o", "r", 1, 0)}}
			records := []review.Record{{ID: "a", Ref: pr.Ref{Org: "o", Repo: "r", Number: 1}, State: state}}

			rows := Group(f, records)[0].Rows

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

	rows := Group(f, records)[0].Rows

	if rows[0].State != review.StateReviewing {
		t.Errorf("state = %q, want reviewing from the open record", rows[0].State)
	}
}

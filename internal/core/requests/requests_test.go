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

const me = "haacked"

// alice and bob are on the team. carol, dave, and erin are not.
var members = []string{"alice", "bob"}

var kindNames = map[Kind]string{Yours: "Yours", NeedsReviewer: "NeedsReviewer", Covered: "Covered"}

func request(org, repo string, number int, hoursAgo int) PR {
	ref := pr.Ref{Org: org, Repo: repo, Number: number}
	return PR{Ref: ref, Title: ref.String(), UpdatedAt: base.Add(-time.Duration(hoursAgo) * time.Hour)}
}

func assigned(p PR, logins ...string) PR {
	p.Assignees = logins
	return p
}

func by(p PR, author string) PR {
	p.Author = author
	return p
}

// bot makes p a bot's pull request. An empty startedBy means the bot started it
// by itself.
func bot(p PR, startedBy string) PR {
	p.Author = "posthog-bot[bot]"
	p.Bot = true
	p.PersonStarted = startedBy != ""
	p.StartedBy = startedBy
	return p
}

// teamAsked is a refresh in which only the team of alice and bob asked for
// reviews.
func teamAsked(prs ...PR) Fetched {
	return Fetched{Login: me, Teams: []Team{{Slug: "o/team", Members: members, PRs: prs}}}
}

func record(id string, ref pr.Ref, state review.State) review.Record {
	return review.Record{ID: id, Ref: ref, State: state}
}

func numbers(rows []Row) []int {
	out := make([]int, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.Ref.Number)
	}
	return out
}

func rowsIn(t *testing.T, l List, k Kind) []Row {
	t.Helper()
	for _, s := range l.Sections {
		if s.Kind == k {
			return s.Rows
		}
	}
	t.Fatalf("no %s section in %+v", kindNames[k], l.Sections)
	return nil
}

// place returns the section and the row of pull request #n, and fails the test
// unless exactly one row in the list has that number.
func place(t *testing.T, l List, n int) (Kind, Row) {
	t.Helper()
	var kind Kind
	var row Row
	found := 0
	for _, s := range l.Sections {
		for _, r := range s.Rows {
			if r.Ref.Number == n {
				kind, row = s.Kind, r
				found++
			}
		}
	}
	if found != 1 {
		t.Fatalf("#%d is listed %d times, want once", n, found)
	}
	return kind, row
}

func TestGroupAlwaysReturnsOneSectionOfEachKindInKindOrder(t *testing.T) {
	cases := []struct {
		name string
		f    Fetched
	}{
		{"nothing fetched", Fetched{Login: me}},
		{"only my requests", Fetched{Login: me, Mine: []PR{request("o", "r", 1, 0)}}},
		{"only an empty team", Fetched{Login: me, Teams: []Team{{Slug: "o/team", Members: members}}}},
		{"only a covered pull request", teamAsked(assigned(by(request("o", "r", 1, 0), "carol"), "bob"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			list := Group(c.f, nil)

			var kinds []Kind
			for _, s := range list.Sections {
				kinds = append(kinds, s.Kind)
			}
			if want := []Kind{Yours, NeedsReviewer, Covered}; !slices.Equal(kinds, want) {
				t.Errorf("section kinds = %v, want %v", kinds, want)
			}
		})
	}
}

func TestGroupPrefixesEachTeamsErrorWithItsSlugInTeamOrder(t *testing.T) {
	f := Fetched{Login: me, Teams: []Team{
		{Slug: "o/zeta", Errs: []error{errors.New("422")}},
		{Slug: "o/fine"},
		{Slug: "o/alpha", Errs: []error{errors.New("timeout")}},
	}}

	errs := Group(f, nil).Errs

	var got []string
	for _, err := range errs {
		got = append(got, err.Error())
	}
	if want := []string{"o/zeta: 422", "o/alpha: timeout"}; !slices.Equal(got, want) {
		t.Errorf("errs = %q, want %q", got, want)
	}
}

func TestGroupKeepsEachTeamsOriginalErrorReachable(t *testing.T) {
	zeta, alpha := errors.New("422"), errors.New("timeout")
	f := Fetched{Login: me, Teams: []Team{{Slug: "o/zeta", Errs: []error{zeta}}, {Slug: "o/alpha", Errs: []error{alpha}}}}

	errs := Group(f, nil).Errs

	if len(errs) != 2 || !errors.Is(errs[0], zeta) || !errors.Is(errs[1], alpha) {
		t.Errorf("errs = %v, want them to wrap the zeta and alpha errors in turn", errs)
	}
}

// The screen draws each error on one line cut to its width, so each of a team's
// failures needs an entry of its own.
func TestGroupGivesEachOfATeamsFailuresItsOwnEntry(t *testing.T) {
	search, read := errors.New("search failed"), errors.New("403")
	f := Fetched{Login: me, Teams: []Team{{Slug: "o/team", Errs: []error{search, read}}}}

	errs := Group(f, nil).Errs

	var got []string
	for _, err := range errs {
		got = append(got, err.Error())
	}
	if want := []string{"o/team: search failed", "o/team: 403"}; !slices.Equal(got, want) {
		t.Fatalf("errs = %q, want %q", got, want)
	}
	if !errors.Is(errs[0], search) || !errors.Is(errs[1], read) {
		t.Errorf("errs = %v, want them to wrap the search and read errors in turn", errs)
	}
}

func TestGroupReportsNoErrorsWhenEveryTeamSearchSucceeded(t *testing.T) {
	f := Fetched{Login: me, Mine: []PR{request("o", "r", 1, 0)}, Teams: []Team{{Slug: "o/a"}, {Slug: "o/b"}}}

	if errs := Group(f, nil).Errs; len(errs) != 0 {
		t.Errorf("errs = %v, want none", errs)
	}
}

// A team's Err can mean only that a later check failed, and PRs then keeps every
// pull request the check could not rule out.
func TestGroupStillListsThePullRequestsOfATeamThatReportedAnError(t *testing.T) {
	f := teamAsked(request("o", "r", 1, 0))
	f.Teams[0].Errs = []error{errors.New("could not read reviews")}

	place(t, Group(f, nil), 1)
}

// GitHub compares owners and repositories without case, so a team search can
// name the pull request a personal search already returned in other letters.
func TestGroupListsAPullRequestAskedOfMeAndATeamOnlyUnderMe(t *testing.T) {
	f := teamAsked(assigned(by(request("O", "R", 1, 0), "carol"), "bob"), request("o", "r", 2, 0))
	f.Mine = []PR{by(request("o", "r", 1, 0), "carol")}

	kind, row := place(t, Group(f, nil), 1)

	if kind != Yours || !row.AskedMe() {
		t.Errorf("#1 is in %s with team %q, want Yours as asked of me", kindNames[kind], row.Team)
	}
}

func TestGroupListsAPullRequestAskedOfTwoTeamsOnlyUnderTheFirst(t *testing.T) {
	f := Fetched{Login: me, Teams: []Team{
		{Slug: "o/zeta", PRs: []PR{request("o", "r", 1, 0)}},
		{Slug: "o/alpha", PRs: []PR{request("O", "R", 1, 0), request("o", "r", 2, 0)}},
	}}

	list := Group(f, nil)

	if _, row := place(t, list, 1); row.Team != "o/zeta" {
		t.Errorf("#1 is under %q, want o/zeta", row.Team)
	}
	if _, row := place(t, list, 2); row.Team != "o/alpha" {
		t.Errorf("#2 is under %q, want o/alpha", row.Team)
	}
}

// bob covers the pull request for alpha but is an outsider to zeta, which
// listed it first.
func TestGroupJudgesAPullRequestAskedOfTwoTeamsByTheFirstTeamsMembers(t *testing.T) {
	p := assigned(by(request("o", "r", 1, 0), "dave"), "bob")
	f := Fetched{Login: me, Teams: []Team{
		{Slug: "o/zeta", Members: []string{"carol"}, PRs: []PR{p}},
		{Slug: "o/alpha", Members: []string{"bob"}, PRs: []PR{p}},
	}}

	kind, row := place(t, Group(f, nil), 1)

	if kind != NeedsReviewer || row.Reviewer != "bob" {
		t.Errorf("#1 is in %s with reviewer %q, want NeedsReviewer with bob from outside zeta", kindNames[kind], row.Reviewer)
	}
}

// GitHub compares logins without case. The user types github_user in
// config.toml by hand, so its case can differ from GitHub's.
func TestGroupMarksWhetherARowIsAssignedToMe(t *testing.T) {
	cases := []struct {
		name      string
		login     string
		assignees []string
		want      bool
	}{
		{"assigned to me", "haacked", []string{"haacked"}, true},
		{"assigned to me in other letters", "Haacked", []string{"haacked"}, true},
		{"assigned to me among others", "haacked", []string{"bob", "haacked"}, true},
		{"assigned to someone else", "haacked", []string{"bob"}, false},
		{"unassigned", "haacked", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := teamAsked(assigned(by(request("o", "r", 1, 0), "carol"), c.assignees...))
			f.Login = c.login

			_, row := place(t, Group(f, nil), 1)

			if row.AssignedMe != c.want {
				t.Errorf("AssignedMe = %v, want %v", row.AssignedMe, c.want)
			}
		})
	}
}

func TestGroupMarksARowWhoseOwnerIsAMemberAsATeammates(t *testing.T) {
	cases := []struct {
		name    string
		p       PR
		members []string
		want    bool
	}{
		{"a member's pull request", by(request("o", "r", 1, 0), "alice"), members, true},
		{"a member's pull request in other letters", by(request("o", "r", 1, 0), "Alice"), members, true},
		{"an outsider's pull request", by(request("o", "r", 1, 0), "carol"), members, false},
		{"a bot's pull request a member started", bot(request("o", "r", 1, 0), "alice"), members, true},
		{"a bot's pull request an outsider started", bot(request("o", "r", 1, 0), "carol"), members, false},
		{"a bot's pull request nobody started, with the bot among the members", bot(request("o", "r", 1, 0), ""), []string{"alice", "posthog-bot[bot]"}, false},
		{"a bot's pull request nobody started, with an empty login among the members", bot(request("o", "r", 1, 0), ""), []string{"alice", ""}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := teamAsked(c.p)
			f.Teams[0].Members = c.members

			_, row := place(t, Group(f, nil), 1)

			if row.Teammate != c.want {
				t.Errorf("Teammate = %v, want %v", row.Teammate, c.want)
			}
		})
	}
}

func TestGroupListsAPullRequestAskedOfMeUnderYoursWhateverItsAssignees(t *testing.T) {
	cases := []struct {
		name string
		p    PR
	}{
		{"unassigned", by(request("o", "r", 1, 0), "carol")},
		{"assigned to its author", assigned(by(request("o", "r", 1, 0), "carol"), "carol")},
		{"assigned to someone else", assigned(by(request("o", "r", 1, 0), "carol"), "dave")},
		{"a bot's, assigned to someone", assigned(bot(request("o", "r", 1, 0), ""), "dave")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Fetched{Login: me, Mine: []PR{c.p}}

			if kind, _ := place(t, Group(f, nil), 1); kind != Yours {
				t.Errorf("#1 is in %s, want Yours", kindNames[kind])
			}
		})
	}
}

// bob would cover the pull request for the team, but the assignment to me wins.
func TestGroupListsATeamPullRequestAssignedToMeUnderYours(t *testing.T) {
	f := teamAsked(assigned(by(request("o", "r", 1, 0), "alice"), "bob", me))

	if kind, _ := place(t, Group(f, nil), 1); kind != Yours {
		t.Errorf("#1 is in %s, want Yours", kindNames[kind])
	}
}

func TestGroupListsATeamPullRequestWithAnOpenRecordUnderYoursEvenWhenATeammateCoversIt(t *testing.T) {
	open := []review.State{
		review.StatePreparing, review.StateReviewing, review.StateDrafted, review.StateSubmitted,
		review.StateUnreviewed, review.StateReviewed, review.StateNotStarted,
	}
	for _, state := range open {
		t.Run(string(state), func(t *testing.T) {
			p := assigned(by(request("o", "r", 1, 0), "carol"), "bob")
			records := []review.Record{record("a", p.Ref, state)}

			if kind, _ := place(t, Group(teamAsked(p), records), 1); kind != Yours {
				t.Errorf("#1 is in %s, want Yours", kindNames[kind])
			}
		})
	}
}

func TestGroupLeavesAPullRequestAssignedOnlyToItsAuthorNeedingAReviewer(t *testing.T) {
	cases := []struct {
		name string
		p    PR
	}{
		{"a member's", assigned(by(request("o", "r", 1, 0), "alice"), "alice")},
		{"a member's, assigned in other letters", assigned(by(request("o", "r", 1, 0), "alice"), "Alice")},
		{"an outsider's", assigned(by(request("o", "r", 1, 0), "carol"), "carol")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, row := place(t, Group(teamAsked(c.p), nil), 1)

			if kind != NeedsReviewer || row.Reviewer != "" {
				t.Errorf("#1 is in %s with reviewer %q, want NeedsReviewer with none", kindNames[kind], row.Reviewer)
			}
		})
	}
}

func TestGroupCountsAPullRequestAssignedToItsAuthorAndAnotherMemberAsCoveredByThatMember(t *testing.T) {
	f := teamAsked(assigned(by(request("o", "r", 1, 0), "alice"), "alice", "bob"))

	kind, row := place(t, Group(f, nil), 1)

	if kind != Covered || row.Reviewer != "bob" {
		t.Errorf("#1 is in %s with reviewer %q, want Covered by bob", kindNames[kind], row.Reviewer)
	}
}

func TestGroupLeavesAPullRequestAssignedOutsideTheTeamNeedingAReviewer(t *testing.T) {
	cases := []struct {
		name string
		p    PR
	}{
		{"an outsider's, assigned to its author and another outsider", assigned(by(request("o", "r", 1, 0), "carol"), "carol", "dave")},
		{"a member's, assigned to an outsider", assigned(by(request("o", "r", 1, 0), "alice"), "dave")},
		{"a bot's nobody started, assigned to an outsider", assigned(bot(request("o", "r", 1, 0), ""), "dave")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, row := place(t, Group(teamAsked(c.p), nil), 1)

			if kind != NeedsReviewer || row.Reviewer != "dave" {
				t.Errorf("#1 is in %s with reviewer %q, want NeedsReviewer with dave", kindNames[kind], row.Reviewer)
			}
		})
	}
}

// Without the members, a member's assignment looks like an outsider's, so the
// row has to say that its team's members are unknown.
func TestGroupMarksTheRowsOfATeamWhoseMembersAreUnknown(t *testing.T) {
	f := Fetched{Login: me, Teams: []Team{
		{Slug: "o/unread", MembersUnknown: true, PRs: []PR{assigned(by(request("o", "r", 1, 0), "alice"), "bob")}},
		{Slug: "o/team", Members: members, PRs: []PR{assigned(by(request("o", "r", 2, 0), "carol"), "dave")}},
	}}

	list := Group(f, nil)

	if _, row := place(t, list, 1); !row.MembersUnknown || row.Teammate {
		t.Errorf("#1 = %+v, want its members unknown and no teammate claimed", row)
	}
	if _, row := place(t, list, 2); row.MembersUnknown {
		t.Errorf("#2 = %+v, want its members known", row)
	}
}

func TestGroupNamesTheFirstOutsideReviewerOfAPullRequestThatNeedsOne(t *testing.T) {
	f := teamAsked(assigned(by(request("o", "r", 1, 0), "carol"), "carol", "dave", "erin"))

	if _, row := place(t, Group(f, nil), 1); row.Reviewer != "dave" {
		t.Errorf("reviewer = %q, want dave", row.Reviewer)
	}
}

func TestGroupCountsAMemberAsTheReviewerEvenWhenAnOutsiderIsAssignedFirst(t *testing.T) {
	f := teamAsked(assigned(by(request("o", "r", 1, 0), "carol"), "dave", "bob"))

	kind, row := place(t, Group(f, nil), 1)

	if kind != Covered || row.Reviewer != "bob" {
		t.Errorf("#1 is in %s with reviewer %q, want Covered by bob", kindNames[kind], row.Reviewer)
	}
}

func TestGroupFindsAMemberAmongTheAssigneesWithoutCase(t *testing.T) {
	f := teamAsked(assigned(by(request("o", "r", 1, 0), "carol"), "BOB"))

	if kind, _ := place(t, Group(f, nil), 1); kind != Covered {
		t.Errorf("#1 is in %s, want Covered", kindNames[kind])
	}
}

func TestGroupLeavesAnUnassignedPullRequestNeedingAReviewer(t *testing.T) {
	cases := []struct {
		name string
		p    PR
	}{
		{"a member's", by(request("o", "r", 1, 0), "alice")},
		{"an outsider's", by(request("o", "r", 1, 0), "carol")},
		{"a bot's nobody started", bot(request("o", "r", 1, 0), "")},
		{"a bot's a member started", bot(request("o", "r", 1, 0), "alice")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			kind, row := place(t, Group(teamAsked(c.p), nil), 1)

			if kind != NeedsReviewer || row.Reviewer != "" {
				t.Errorf("#1 is in %s with reviewer %q, want NeedsReviewer with none", kindNames[kind], row.Reviewer)
			}
		})
	}
}

// When the bot starts a pull request by itself, it assigns the owner it chose,
// who reviews it.
func TestGroupCountsABotPullRequestNobodyStartedAsCoveredByItsMemberAssignee(t *testing.T) {
	f := teamAsked(assigned(bot(request("o", "r", 1, 0), ""), "bob"))

	kind, row := place(t, Group(f, nil), 1)

	if kind != Covered || row.Reviewer != "bob" {
		t.Errorf("#1 is in %s with reviewer %q, want Covered by bob", kindNames[kind], row.Reviewer)
	}
}

// When a person starts a bot's pull request, the bot assigns that person, who
// answers for it and so cannot review it.
func TestGroupLeavesABotPullRequestAssignedOnlyToItsStarterNeedingAReviewer(t *testing.T) {
	cases := []struct {
		name     string
		starter  string
		teammate bool
	}{
		{"started by a member", "alice", true},
		{"started by an outsider", "carol", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := teamAsked(assigned(bot(request("o", "r", 1, 0), c.starter), c.starter))

			kind, row := place(t, Group(f, nil), 1)

			if kind != NeedsReviewer || row.Reviewer != "" || row.Teammate != c.teammate {
				t.Errorf("#1 is in %s with reviewer %q and teammate %v, want NeedsReviewer with none and teammate %v",
					kindNames[kind], row.Reviewer, row.Teammate, c.teammate)
			}
		})
	}
}

func TestGroupCountsABotPullRequestAssignedToItsStarterAndAMemberAsCoveredByThatMember(t *testing.T) {
	f := teamAsked(assigned(bot(request("o", "r", 1, 0), "alice"), "alice", "bob"))

	kind, row := place(t, Group(f, nil), 1)

	if kind != Covered || row.Reviewer != "bob" {
		t.Errorf("#1 is in %s with reviewer %q, want Covered by bob", kindNames[kind], row.Reviewer)
	}
}

func TestGroupAttachesTheStateOfAnOpenRecord(t *testing.T) {
	f := Fetched{Login: me, Mine: []PR{request("o", "r", 1, 0), request("o", "r", 2, 1)}}
	records := []review.Record{record("a", pr.Ref{Org: "o", Repo: "r", Number: 1}, review.StateDrafted)}

	list := Group(f, records)

	if _, row := place(t, list, 1); row.State != review.StateDrafted || row.RecordID != "a" {
		t.Errorf("#1 = %q on %q, want drafted on record a", row.State, row.RecordID)
	}
	if _, row := place(t, list, 2); row.State != "" || row.RecordID != "" {
		t.Errorf("#2 = %q on %q, want no record", row.State, row.RecordID)
	}
}

func TestGroupMatchesARecordToItsPullRequestWithoutCase(t *testing.T) {
	f := Fetched{Login: me, Mine: []PR{request("o", "r", 1, 0)}}
	records := []review.Record{record("a", pr.Ref{Org: "O", Repo: "R", Number: 1}, review.StateDrafted)}

	if _, row := place(t, Group(f, records), 1); row.RecordID != "a" {
		t.Errorf("record = %q, want a", row.RecordID)
	}
}

// A pull request reviewed once, archived, and asked for again is a new request,
// so the old record must not make the row look taken.
func TestGroupIgnoresClosedRecords(t *testing.T) {
	for _, state := range []review.State{review.StateArchived, review.StateAbandoned} {
		t.Run(string(state), func(t *testing.T) {
			p := by(request("o", "r", 1, 0), "carol")
			records := []review.Record{record("a", p.Ref, state)}

			kind, row := place(t, Group(teamAsked(p), records), 1)

			if kind != NeedsReviewer || row.State != "" || row.RecordID != "" {
				t.Errorf("#1 is in %s as %q on %q, want NeedsReviewer with no record", kindNames[kind], row.State, row.RecordID)
			}
		})
	}
}

func TestGroupPrefersTheOpenRecordOverAnArchivedOne(t *testing.T) {
	ref := pr.Ref{Org: "o", Repo: "r", Number: 1}
	old, current := record("old", ref, review.StateArchived), record("new", ref, review.StateReviewing)
	cases := []struct {
		name    string
		records []review.Record
	}{
		{"archived first", []review.Record{old, current}},
		{"open first", []review.Record{current, old}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := Fetched{Login: me, Mine: []PR{request("o", "r", 1, 0)}}

			_, row := place(t, Group(f, c.records), 1)

			if row.State != review.StateReviewing || row.RecordID != "new" {
				t.Errorf("row = %q on %q, want reviewing on the open record", row.State, row.RecordID)
			}
		})
	}
}

func TestGroupListsEachSectionNewestFirst(t *testing.T) {
	outsider := func(n, hoursAgo int, assignees ...string) PR {
		return assigned(by(request("o", "r", n, hoursAgo), "carol"), assignees...)
	}
	cases := []struct {
		name string
		f    Fetched
		kind Kind
	}{
		{"my requests", Fetched{Login: me, Mine: []PR{request("o", "r", 1, 5), request("o", "r", 2, 1), request("o", "r", 3, 3)}}, Yours},
		{"my requests and a team's assigned to me", func() Fetched {
			f := teamAsked(outsider(2, 1, me), outsider(3, 3, me))
			f.Mine = []PR{request("o", "r", 1, 5)}
			return f
		}(), Yours},
		{"needs a reviewer", teamAsked(outsider(1, 5), outsider(2, 1), outsider(3, 3)), NeedsReviewer},
		{"covered", teamAsked(outsider(1, 5, "bob"), outsider(2, 1, "bob"), outsider(3, 3, "bob")), Covered},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rows := rowsIn(t, Group(c.f, nil), c.kind)

			if got, want := numbers(rows), []int{2, 3, 1}; !slices.Equal(got, want) {
				t.Errorf("%s order = %v, want %v", kindNames[c.kind], got, want)
			}
		})
	}
}

func TestGroupListsTeammatesPullRequestsFirstWhenTheyNeedAReviewer(t *testing.T) {
	f := teamAsked(
		by(request("o", "r", 1, 0), "carol"),
		by(request("o", "r", 2, 5), "alice"),
		by(request("o", "r", 3, 2), "carol"),
		by(request("o", "r", 4, 1), "bob"),
		assigned(bot(request("o", "r", 5, 3), "alice"), "alice"),
		bot(request("o", "r", 6, 4), ""),
	)

	rows := rowsIn(t, Group(f, nil), NeedsReviewer)

	if got, want := numbers(rows), []int{4, 5, 2, 1, 3, 6}; !slices.Equal(got, want) {
		t.Errorf("order = %v, want %v: the teammates' newest first, then the rest newest first", got, want)
	}
}

func TestGroupDoesNotPutTeammatesFirstOutsideNeedsReviewer(t *testing.T) {
	cases := []struct {
		name     string
		kind     Kind
		assignee string
	}{
		{"yours", Yours, me},
		{"covered", Covered, "bob"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := teamAsked(
				assigned(by(request("o", "r", 1, 3), "alice"), c.assignee),
				assigned(by(request("o", "r", 2, 1), "carol"), c.assignee),
			)

			rows := rowsIn(t, Group(f, nil), c.kind)

			if got, want := numbers(rows), []int{2, 1}; !slices.Equal(got, want) {
				t.Errorf("%s order = %v, want %v", kindNames[c.kind], got, want)
			}
		})
	}
}

func TestOwnerIsTheAuthorOfAPersonsPullRequestAndTheStarterOfABots(t *testing.T) {
	cases := []struct {
		name string
		p    PR
		want string
	}{
		{"a person's", by(request("o", "r", 1, 0), "carol"), "carol"},
		{"a person's with a starter set", PR{Author: "carol", StartedBy: "dave"}, "carol"},
		{"a bot's a person started", bot(request("o", "r", 1, 0), "alice"), "alice"},
		{"a bot's nobody started", bot(request("o", "r", 1, 0), ""), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.p.Owner(); got != c.want {
				t.Errorf("Owner() = %q, want %q", got, c.want)
			}
		})
	}
}

func TestStartedByAPersonReadsTheBotsTaskLine(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"started by a person", "- Task started by: a person, from the inbox", true},
		{"started by a person, among other lines", "## Summary\n\nFixes the flag.\n\n- Task started by: a person, from the inbox\n- Report: #12", true},
		{"started by itself", "- Task started by: auto-start, after the report was rated P3 and ready to fix", false},
		{"no task line", "## Summary\n\nFixes the flag.", false},
		{"empty", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StartedByAPerson(c.body); got != c.want {
				t.Errorf("StartedByAPerson = %v, want %v", got, c.want)
			}
		})
	}
}

// When docket could not read who started a bot's pull request, the member
// assigned to it may be that starter rather than a reviewer.
func TestGroupLeavesABotPullRequestWhoseStarterIsUnknownNeedingAReviewer(t *testing.T) {
	p := bot(request("o", "r", 1, 0), "")
	p.PersonStarted = true

	kind, row := place(t, Group(teamAsked(assigned(p, "alice")), nil), 1)

	if kind != NeedsReviewer || row.Reviewer != "" {
		t.Errorf("placed under %s with reviewer %q, want NeedsReviewer with none", kindNames[kind], row.Reviewer)
	}
}

func TestGroupCountsTheTeamsItSearched(t *testing.T) {
	f := Fetched{Teams: []Team{{Slug: "o/zeta"}, {Slug: "o/alpha"}}}

	if got := Group(f, nil).Teams; got != 2 {
		t.Errorf("teams = %d, want 2", got)
	}
}

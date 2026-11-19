package requests

import (
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
)

var reviewedAt = base.Add(-72 * time.Hour)

// latestOn is the user's latest real review, made on commit at reviewedAt.
func latestOn(commit string) review.GHReview {
	at := reviewedAt
	return review.GHReview{User: review.GHUser{Login: me}, State: review.StateCommented, Body: "Some thoughts.", CommitID: commit, SubmittedAt: &at}
}

func TestCountChangesCountsTheCommitsAfterTheReviewedOne(t *testing.T) {
	tests := []struct {
		name     string
		commits  []string
		reviewed string
		want     int
	}{
		{"the reviewed commit in the middle", []string{"a", "b", "c", "d"}, "b", 2},
		{"the reviewed commit first", []string{"a", "b", "c"}, "a", 2},
		{"the reviewed commit last", []string{"a", "b", "c"}, "c", 0},
		// A force push rewrote the reviewed commit away, so every commit is
		// one the review never covered.
		{"the reviewed commit is gone", []string{"x", "y", "z"}, "b", 3},
		{"no commits", nil, "b", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountChanges(Timeline{Commits: tt.commits}, latestOn(tt.reviewed))

			if got.Commits != tt.want {
				t.Errorf("commits = %d, want %d", got.Commits, tt.want)
			}
		})
	}
}

// created_at is GitHub's own clock, so a force push counts only when GitHub
// recorded it after the review.
func TestCountChangesCountsOnlyTheForcePushesAfterTheReview(t *testing.T) {
	tests := []struct {
		name   string
		pushes []time.Time
		want   int
	}{
		{"none", nil, 0},
		{"one before the review", []time.Time{reviewedAt.Add(-time.Hour)}, 0},
		{"one at the instant of the review", []time.Time{reviewedAt}, 0},
		{"one after the review", []time.Time{reviewedAt.Add(time.Second)}, 1},
		{"before, at, and two after", []time.Time{reviewedAt.Add(-time.Hour), reviewedAt, reviewedAt.Add(time.Minute), reviewedAt.Add(24 * time.Hour)}, 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CountChanges(Timeline{Commits: []string{"a"}, ForcePushes: tt.pushes}, latestOn("a"))

			if got.ForcePushes != tt.want {
				t.Errorf("force pushes = %d, want %d", got.ForcePushes, tt.want)
			}
		})
	}
}

func TestCountChangesReportsWhenTheReviewWasSubmitted(t *testing.T) {
	got := CountChanges(Timeline{Commits: []string{"a", "b"}}, latestOn("a"))

	if !got.ReviewedAt.Equal(reviewedAt) {
		t.Errorf("reviewed at = %v, want %v", got.ReviewedAt, reviewedAt)
	}
}

// candidate is a re-review candidate whose author's association with the
// repository's organization is inOrg.
func candidate(p PR, inOrg bool) Candidate {
	p.AuthorInOrg = inOrg
	return Candidate{PR: p, Changes: Changes{ReviewedAt: reviewedAt, Commits: 2, ForcePushes: 1}}
}

// withCandidates adds a check for re-review candidates that found cs to f.
func withCandidates(f Fetched, cs ...Candidate) Fetched {
	f.Rereview = &Rereview{Candidates: cs}
	return f
}

func TestGroupReturnsTheCandidateSectionsAfterEveryOtherInKindOrder(t *testing.T) {
	f := withCandidates(teamAsked(by(request("o", "r", 2, 0), "carol")),
		candidate(by(request("o", "r", 3, 0), "dave"), false),
		candidate(by(request("o", "r", 4, 0), "alice"), true))
	f.Mine = []PR{request("o", "r", 1, 0)}

	list := Group(f, nil)

	var kinds []Kind
	for _, s := range list.Sections {
		kinds = append(kinds, s.Kind)
	}
	if want := []Kind{Yours, NeedsReviewer, Covered, RereviewExternal, RereviewMember}; !slices.Equal(kinds, want) {
		t.Errorf("section kinds = %v, want %v", kinds, want)
	}
}

// author_association is relative to the pull request's repository. A bot reads
// CONTRIBUTOR or NONE, and the repository's owner installed it.
func TestGroupPutsACandidateInTheSectionOfItsAuthor(t *testing.T) {
	tests := []struct {
		name string
		c    Candidate
		want Kind
	}{
		{"an author in the org", candidate(by(request("o", "r", 1, 0), "alice"), true), RereviewMember},
		{"an outside contributor", candidate(by(request("o", "r", 1, 0), "jadibdev"), false), RereviewExternal},
		{"a bot outside the org", candidate(bot(request("o", "r", 1, 0), ""), false), RereviewMember},
		{"a bot a person started", candidate(bot(request("o", "r", 1, 0), "pauldambra"), false), RereviewMember},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			kind, _ := place(t, Group(withCandidates(Fetched{Login: me}, tt.c), nil), 1)

			if kind != tt.want {
				t.Errorf("section = %d, want %d", kind, tt.want)
			}
		})
	}
}

func TestGroupCarriesWhatChangedOntoTheCandidatesRow(t *testing.T) {
	c := candidate(by(request("o", "r", 1, 0), "alice"), true)

	_, row := place(t, Group(withCandidates(Fetched{Login: me}, c), nil), 1)

	if row.Changes != c.Changes {
		t.Errorf("changes = %+v, want %+v", row.Changes, c.Changes)
	}
}

// A candidate row names no team. Its section comes from the author alone.
func TestGroupNamesNoTeamOnACandidatesRow(t *testing.T) {
	c := candidate(assigned(by(request("o", "r", 1, 0), "alice"), "bob"), true)
	f := withCandidates(teamAsked(), c)

	kind, row := place(t, Group(f, nil), 1)

	if kind != RereviewMember || row.Team != "" || row.Reviewer != "" {
		t.Errorf("#1 is in section %d with team %q and reviewer %q, want the member candidates with neither", kind, row.Team, row.Reviewer)
	}
}

// A pull request appears once, because marks and the cursor key on it. Every
// earlier section wins over a candidate section, whatever the letters.
func TestGroupListsACandidateAnotherSearchListedOnlyUnderThatSearch(t *testing.T) {
	tests := []struct {
		name string
		f    func(c PR) Fetched
		want Kind
	}{
		{"asked of me", func(c PR) Fetched {
			return Fetched{Login: me, Mine: []PR{c}}
		}, Yours},
		{"asked of me in other letters", func(c PR) Fetched {
			upper := c
			upper.Ref.Org, upper.Ref.Repo = "O", "R"
			return Fetched{Login: me, Mine: []PR{upper}}
		}, Yours},
		{"asked of a team and needing a reviewer", func(c PR) Fetched {
			return teamAsked(c)
		}, NeedsReviewer},
		{"asked of a team and covered by a member", func(c PR) Fetched {
			return teamAsked(assigned(c, "bob"))
		}, Covered},
		{"asked of two teams", func(c PR) Fetched {
			return Fetched{Login: me, Teams: []Team{
				{Slug: "o/one", Members: members, PRs: []PR{c}},
				{Slug: "o/two", Members: members, PRs: []PR{c}},
			}}
		}, NeedsReviewer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := by(request("o", "r", 1, 0), "carol")
			f := withCandidates(tt.f(p), candidate(p, false))

			kind, _ := place(t, Group(f, nil), 1)

			if kind != tt.want {
				t.Errorf("section = %d, want %d", kind, tt.want)
			}
		})
	}
}

func TestGroupListsACandidateTheCheckReturnedTwiceOnce(t *testing.T) {
	c := candidate(by(request("o", "r", 1, 0), "alice"), true)

	place(t, Group(withCandidates(Fetched{Login: me}, c, c), nil), 1)
}

// p hides a candidate with an open record like any other candidate, so the
// record does not move it to Yours the way it moves a team's row.
func TestGroupKeepsACandidateWithAnOpenRecordInItsSectionAndAttachesTheState(t *testing.T) {
	p := by(request("o", "r", 1, 0), "jadibdev")
	records := []review.Record{record("rec-1", p.Ref, review.StateDrafted)}

	kind, row := place(t, Group(withCandidates(Fetched{Login: me}, candidate(p, false)), records), 1)

	if kind != RereviewExternal {
		t.Errorf("section = %d, want the outside contributors' candidates", kind)
	}
	if row.State != review.StateDrafted || row.RecordID != "rec-1" {
		t.Errorf("row = state %q, record %q, want drafted and rec-1", row.State, row.RecordID)
	}
}

func TestGroupListsEachCandidateSectionNewestFirst(t *testing.T) {
	f := withCandidates(Fetched{Login: me},
		candidate(request("o", "r", 1, 30), false),
		candidate(request("o", "r", 2, 1), false),
		candidate(request("o", "r", 3, 10), false),
		candidate(request("o", "r", 4, 50), true),
		candidate(request("o", "r", 5, 2), true),
	)

	list := Group(f, nil)

	if got := numbers(rowsIn(t, list, RereviewExternal)); !slices.Equal(got, []int{2, 3, 1}) {
		t.Errorf("outside contributors = %v, want newest first", got)
	}
	if got := numbers(rowsIn(t, list, RereviewMember)); !slices.Equal(got, []int{5, 4}) {
		t.Errorf("org members = %v, want newest first", got)
	}
}

func TestGroupPrefixesTheCandidateErrorsAfterTheTeams(t *testing.T) {
	team, check := errors.New("422"), errors.New("rate limited")
	f := Fetched{Login: me, Teams: []Team{{Slug: "o/team", Errs: []error{team}}}, Rereview: &Rereview{Errs: []error{check}}}

	list := Group(f, nil)
	errs := slices.Concat(list.Errs, list.RereviewErrs)

	var got []string
	for _, err := range errs {
		got = append(got, err.Error())
	}
	if want := []string{"o/team: 422", "re-review candidates: rate limited"}; !slices.Equal(got, want) {
		t.Fatalf("errs = %q, want %q", got, want)
	}
	if !errors.Is(errs[1], check) {
		t.Errorf("errs[1] = %v, want it to wrap the check's error", errs[1])
	}
}

// The check stops at its first failure and keeps what it found before it.
func TestGroupListsTheCandidatesFoundBeforeTheCheckFailed(t *testing.T) {
	f := withCandidates(Fetched{Login: me}, candidate(request("o", "r", 1, 0), true))
	f.Rereview.Errs = []error{errors.New("rate limited")}

	place(t, Group(f, nil), 1)
}

// List.Rereview tells the screen whether to search again when the user asks to
// see the candidates.
func TestGroupSaysWhetherTheRefreshLookedForCandidates(t *testing.T) {
	tests := []struct {
		name string
		f    Fetched
		want bool
	}{
		{"not looked for", Fetched{Login: me}, false},
		{"looked for and none found", withCandidates(Fetched{Login: me}), true},
		{"looked for and one found", withCandidates(Fetched{Login: me}, candidate(request("o", "r", 1, 0), true)), true},
		{"looked for and failed", Fetched{Login: me, Rereview: &Rereview{Errs: []error{errors.New("rate limited")}}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Group(tt.f, nil).Rereview; got != tt.want {
				t.Errorf("Rereview = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGroupLeavesTheCandidateSectionsEmptyWhenNoneWereLookedFor(t *testing.T) {
	list := Group(teamAsked(request("o", "r", 1, 0)), nil)

	if n := len(rowsIn(t, list, RereviewExternal)) + len(rowsIn(t, list, RereviewMember)); n != 0 {
		t.Errorf("candidate rows = %d, want none", n)
	}
	if len(list.Errs) != 0 {
		t.Errorf("errs = %v, want none", list.Errs)
	}
}

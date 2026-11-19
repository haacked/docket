package session

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/gh"
	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
)

const userQuery = "user-review-requested:haacked"

func candRef(n int) pr.Ref { return pr.Ref{Org: "o", Repo: "r", Number: n} }

// seedPR is a pull request the reviewed-by search returns.
func seedPR(ref pr.Ref, inOrg bool) requests.PR {
	return requests.PR{Ref: ref, Title: ref.String(), Author: "alice", AuthorInOrg: inOrg, UpdatedAt: start.Add(-time.Hour)}
}

// reviewOf is a review of mine with a summary, made on commit hoursAgo before
// start.
func reviewOf(state, commit string, hoursAgo int) review.GHReview {
	at := start.Add(-time.Duration(hoursAgo) * time.Hour)
	return review.GHReview{User: review.GHUser{Login: "haacked"}, State: state, Body: "Some thoughts.", CommitID: commit, SubmittedAt: &at}
}

// replyOf is how GitHub records a reply of mine in a review thread.
func replyOf(commit string, hoursAgo int) review.GHReview {
	r := reviewOf(review.StateCommented, commit, hoursAgo)
	r.Body = ""
	return r
}

func draftOf(commit string) review.GHReview {
	return review.GHReview{User: review.GHUser{Login: "haacked"}, State: review.StatePending, CommitID: commit}
}

// candidateService is a service with no teams whose fake answers PR and
// Timeline per pull request.
func candidateService(t *testing.T) (*Service, *fakeGH) {
	t.Helper()
	fake := &fakeGH{login: "haacked"}
	svc, _ := newService(t, fake, newFakeGit())
	fake.requested = map[string][]requests.PR{}
	fake.reviewsFor = map[pr.Ref][]review.GHReview{}
	fake.reviewErrs = map[pr.Ref]error{}
	fake.infoFor = map[pr.Ref]gh.PRInfo{}
	fake.infoErrs = map[pr.Ref]error{}
	fake.timelines = map[pr.Ref]requests.Timeline{}
	fake.timelineErrs = map[pr.Ref]error{}
	return svc, fake
}

// changedSince sets ref up as a candidate: my latest review comments on old,
// the head is new, and the timeline holds both.
func changedSince(fake *fakeGH, ref pr.Ref) {
	fake.reviewsFor[ref] = []review.GHReview{reviewOf(review.StateCommented, "old", 24)}
	fake.infoFor[ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: "new"}
	fake.timelines[ref] = requests.Timeline{Commits: []string{"old", "new"}}
}

func candidateRefs(f requests.Fetched) []string {
	if f.Rereview == nil {
		return nil
	}
	var refs []string
	for _, c := range f.Rereview.Candidates {
		refs = append(refs, c.Ref.String())
	}
	return refs
}

func searchesFor(searches []string, query string) int {
	n := 0
	for _, q := range searches {
		if q == query {
			n++
		}
	}
	return n
}

// The default view costs nothing new: no candidate reads and no reviewed-by
// search unless a team has rows.
func TestRequestsWithoutRereviewLooksForNoCandidates(t *testing.T) {
	svc, fake := candidateService(t)
	seed := seedPR(candRef(1), true)
	fake.requested[reviewedQuery] = []requests.PR{seed}
	changedSince(fake, seed.Ref)

	f, err := svc.Requests(context.Background(), false)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}

	if f.Rereview != nil {
		t.Errorf("rereview = %+v, want nil", f.Rereview)
	}
	if want := []string{userQuery}; !slices.Equal(fake.searches, want) {
		t.Errorf("searches = %v, want %v", fake.searches, want)
	}
	if fake.reads != 0 || fake.infoReads != 0 || fake.timelineReads != 0 {
		t.Errorf("read reviews %d, heads %d, and timelines %d times, want none", fake.reads, fake.infoReads, fake.timelineReads)
	}
}

func TestRequestsWithoutRereviewReadsNoTimelineWhenATeamHasRows(t *testing.T) {
	svc, fake := candidateService(t)
	svc.Cfg.Teams = []string{"PostHog/team-a"}
	teamRow := seedPR(candRef(1), true)
	seed := seedPR(candRef(2), true)
	fake.requested[teamQuery("PostHog/team-a")] = []requests.PR{teamRow}
	fake.requested[reviewedQuery] = []requests.PR{teamRow, seed}
	changedSince(fake, teamRow.Ref)
	changedSince(fake, seed.Ref)

	f, err := svc.Requests(context.Background(), false)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}

	if f.Rereview != nil {
		t.Errorf("rereview = %+v, want nil", f.Rereview)
	}
	if fake.timelineReads != 0 {
		t.Errorf("read timelines %d times, want none", fake.timelineReads)
	}
	if fake.reads != 1 {
		t.Errorf("read reviews %d times, want one, for the team's row", fake.reads)
	}
}

// GitHub allows 30 searches a minute, so the team check and the candidates
// share one reviewed-by search.
func TestRequestsWithRereviewSearchesForReviewedPullRequestsOnce(t *testing.T) {
	tests := []struct {
		name  string
		teams []string
		rows  map[string][]requests.PR
	}{
		{"no teams", nil, nil},
		{"teams with no rows", []string{"PostHog/team-a", "PostHog/team-b"}, nil},
		{"teams with rows", []string{"PostHog/team-a", "PostHog/team-b"}, map[string][]requests.PR{
			teamQuery("PostHog/team-a"): {seedPR(candRef(1), true)},
			teamQuery("PostHog/team-b"): {seedPR(candRef(2), true)},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fake := candidateService(t)
			svc.Cfg.Teams = tt.teams
			for q, prs := range tt.rows {
				fake.requested[q] = prs
			}

			f, err := svc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests: %v", err)
			}

			if n := searchesFor(fake.searches, reviewedQuery); n != 1 {
				t.Errorf("reviewed-by searches = %d, want 1 in %v", n, fake.searches)
			}
			if f.Rereview == nil {
				t.Error("rereview = nil, want the check's answer even when it found nothing")
			}
		})
	}
}

// requests.Group lists a pull request once, under the first search that
// returned it, so reading a seed another search listed costs calls and changes
// nothing on the screen. A draft never shows as a candidate.
func TestRequestsReadsNothingForASeedAnotherSearchListedOrADraft(t *testing.T) {
	svc, fake := candidateService(t)
	svc.Cfg.Teams = []string{"PostHog/team-a", "PostHog/team-b"}
	askedOfMe := seedPR(candRef(1), true)
	askedOfMeUpper := seedPR(candRef(2), true)
	upper := askedOfMeUpper
	upper.Ref.Org, upper.Ref.Repo = "O", "R"
	teamA := seedPR(candRef(3), true)
	teamB := seedPR(candRef(4), true)
	draft := seedPR(candRef(5), true)
	draft.IsDraft = true
	wanted := seedPR(candRef(6), false)
	fake.requested[userQuery] = []requests.PR{askedOfMe, upper}
	fake.requested[teamQuery("PostHog/team-a")] = []requests.PR{teamA}
	fake.requested[teamQuery("PostHog/team-b")] = []requests.PR{teamB}
	fake.requested[reviewedQuery] = []requests.PR{askedOfMe, askedOfMeUpper, teamA, teamB, draft, wanted}
	for _, p := range []requests.PR{askedOfMe, askedOfMeUpper, teamA, teamB, draft, wanted} {
		changedSince(fake, p.Ref)
	}
	// A read of the pull requests asked of me or of the draft would end the
	// checks with this error.
	failure := errors.New("read a pull request that needs no check")
	fake.reviewErrs[askedOfMe.Ref] = failure
	fake.reviewErrs[askedOfMeUpper.Ref] = failure
	fake.reviewErrs[draft.Ref] = failure

	f, err := svc.Requests(context.Background(), true)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}

	if errs := errors.Join(f.Rereview.Errs...); errs != nil {
		t.Errorf("rereview errs = %v, want none", errs)
	}
	if got, want := candidateRefs(f), []string{"o/r#6"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
	// The team check reads #3 and #4 once each. The candidates read only #6.
	if fake.reads != 3 {
		t.Errorf("read reviews %d times, want 3: once for each team row and once for #6", fake.reads)
	}
}

// The team check already found the user caught up on a team's pull request, so
// the team dropped it. It is not a candidate either.
func TestATeamsPullRequestTheUserIsCaughtUpOnIsNoCandidate(t *testing.T) {
	svc, fake := candidateService(t)
	svc.Cfg.Teams = []string{"PostHog/team-a"}
	caughtUp := seedPR(candRef(1), true)
	fake.requested[teamQuery("PostHog/team-a")] = []requests.PR{caughtUp}
	fake.requested[reviewedQuery] = []requests.PR{caughtUp}
	fake.reviewsFor[caughtUp.Ref] = []review.GHReview{reviewOf(review.StateCommented, "head", 24)}
	fake.infoFor[caughtUp.Ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: "head"}

	f, err := svc.Requests(context.Background(), true)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}

	if len(f.Teams[0].PRs) != 0 {
		t.Errorf("team rows = %+v, want #1 dropped as caught up", f.Teams[0].PRs)
	}
	if got := candidateRefs(f); len(got) != 0 {
		t.Errorf("candidates = %v, want none", got)
	}
	if fake.timelineReads != 0 {
		t.Errorf("read timelines %d times, want none", fake.timelineReads)
	}
	if fake.reads != 1 || fake.infoReads != 1 {
		t.Errorf("read reviews %d and heads %d times, want once each, by the team check", fake.reads, fake.infoReads)
	}
}

// The latest real review decides. A comment with a summary or a request for
// changes leaves the pull request open to a re-review. An approval or a
// dismissal does not, and neither does a pull request with no real review. A
// pull request the rule leaves out needs no read of its head.
func TestACandidatesLatestRealReviewMustNotApprove(t *testing.T) {
	tests := []struct {
		name    string
		reviews []review.GHReview
		want    bool
	}{
		{name: "no reviews"},
		{name: "only replies in review threads", reviews: []review.GHReview{replyOf("old", 48), replyOf("old", 24)}},
		{name: "only my pending review", reviews: []review.GHReview{draftOf("old")}},
		{name: "only someone else's review", reviews: []review.GHReview{{User: review.GHUser{Login: "octocat"}, State: review.StateChangesRequested, Body: "No.", CommitID: "old", SubmittedAt: &start}}},
		{name: "my real review with no commit", reviews: []review.GHReview{reviewOf(review.StateCommented, "", 24)}},
		{name: "an approval after a request for changes", reviews: []review.GHReview{reviewOf(review.StateChangesRequested, "older", 48), reviewOf(review.StateApproved, "old", 24)}},
		{name: "two comments then an approval", reviews: []review.GHReview{reviewOf(review.StateCommented, "older", 72), reviewOf(review.StateCommented, "older", 48), reviewOf(review.StateApproved, "old", 24)}},
		{name: "a dismissed review last", reviews: []review.GHReview{reviewOf(review.StateCommented, "older", 48), reviewOf(review.StateDismissed, "old", 24)}},
		{name: "a comment with a summary", reviews: []review.GHReview{reviewOf(review.StateCommented, "old", 24)}, want: true},
		{name: "a request for changes", reviews: []review.GHReview{reviewOf(review.StateChangesRequested, "old", 24)}, want: true},
		{name: "a comment with a summary after an approval", reviews: []review.GHReview{reviewOf(review.StateApproved, "older", 48), reviewOf(review.StateCommented, "old", 24)}, want: true},
		{name: "a reply in a thread after a request for changes", reviews: []review.GHReview{reviewOf(review.StateChangesRequested, "old", 48), replyOf("old", 24)}, want: true},
		{name: "my login in other letters", reviews: []review.GHReview{func() review.GHReview {
			r := reviewOf(review.StateCommented, "old", 24)
			r.User.Login = "HAACKED"
			return r
		}()}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fake := candidateService(t)
			seed := seedPR(candRef(1), true)
			fake.requested[reviewedQuery] = []requests.PR{seed}
			fake.reviewsFor[seed.Ref] = tt.reviews
			fake.infoFor[seed.Ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: "new"}
			fake.timelines[seed.Ref] = requests.Timeline{Commits: []string{"old", "new"}}

			f, err := svc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests: %v", err)
			}

			if got := len(candidateRefs(f)) == 1; got != tt.want {
				t.Errorf("candidate = %v, want %v", got, tt.want)
			}
			if !tt.want && fake.infoReads != 0 {
				t.Errorf("read the head %d times, want none for a pull request the review rule leaves out", fake.infoReads)
			}
		})
	}
}

// A pending draft of mine on the head is a draft nobody has seen, so it does
// not cover the commits since my last real review (RFC-internal #1218).
func TestAPendingDraftOnTheHeadLeavesACandidate(t *testing.T) {
	svc, fake := candidateService(t)
	seed := seedPR(candRef(1), true)
	fake.requested[reviewedQuery] = []requests.PR{seed}
	fake.reviewsFor[seed.Ref] = []review.GHReview{reviewOf(review.StateCommented, "old", 24), draftOf("new")}
	fake.infoFor[seed.Ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: "new"}
	fake.timelines[seed.Ref] = requests.Timeline{Commits: []string{"old", "new"}}

	f, err := svc.Requests(context.Background(), true)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}

	if got, want := candidateRefs(f), []string{"o/r#1"}; !slices.Equal(got, want) {
		t.Errorf("candidates = %v, want %v", got, want)
	}
}

// A head one of my real reviews was made on has nothing new, even when the
// author force-pushed back to it after a later review. It needs no timeline.
func TestACaughtUpHeadIsNoCandidateAndReadsNoTimeline(t *testing.T) {
	tests := []struct {
		name    string
		reviews []review.GHReview
		head    string
	}{
		{"the head is the commit of my latest review", []review.GHReview{reviewOf(review.StateChangesRequested, "old", 48), reviewOf(review.StateCommented, "new", 24)}, "new"},
		{"the head was force-pushed back to a commit I reviewed earlier", []review.GHReview{reviewOf(review.StateChangesRequested, "old", 48), reviewOf(review.StateCommented, "new", 24)}, "old"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fake := candidateService(t)
			seed := seedPR(candRef(1), true)
			fake.requested[reviewedQuery] = []requests.PR{seed}
			fake.reviewsFor[seed.Ref] = tt.reviews
			fake.infoFor[seed.Ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: tt.head}

			f, err := svc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests: %v", err)
			}

			if got := candidateRefs(f); len(got) != 0 {
				t.Errorf("candidates = %v, want none", got)
			}
			if fake.timelineReads != 0 {
				t.Errorf("read timelines %d times, want none", fake.timelineReads)
			}
		})
	}
}

// These are the shapes the user's own data held on 2026-11-19: three pull
// requests by members, one with a pending draft of the user's on the head and
// a rewritten history, and one by an outside collaborator from 2022. Around
// them are pull requests the rule leaves out: the latest real review approved,
// and only replies in threads.
func TestRequestsFindsTheCandidatesInTheUsersData(t *testing.T) {
	svc, fake := candidateService(t)
	posthog := pr.Ref{Org: "PostHog", Repo: "posthog", Number: 136159}
	rfc1224 := pr.Ref{Org: "PostHog", Repo: "requests-for-comments-internal", Number: 1224}
	rfc1218 := pr.Ref{Org: "PostHog", Repo: "requests-for-comments-internal", Number: 1218}
	interview := pr.Ref{Org: "aseriousbiz-interviews", Repo: "working-hours-csharp-nsteinhafel", Number: 1}
	approved := pr.Ref{Org: "PostHog", Repo: "sdk-specs", Number: 231}
	approvedAfterComments := pr.Ref{Org: "PostHog", Repo: "posthog", Number: 137945}
	repliesOnly := pr.Ref{Org: "PostHog", Repo: "requests-for-comments-internal", Number: 1240}
	fake.requested[reviewedQuery] = []requests.PR{
		seedPR(posthog, true), seedPR(approved, true), seedPR(rfc1224, true),
		seedPR(approvedAfterComments, true), seedPR(rfc1218, true), seedPR(repliesOnly, true),
		seedPR(interview, false),
	}

	at := func(hoursAgo int) time.Time { return start.Add(-time.Duration(hoursAgo) * time.Hour) }
	fake.reviewsFor[posthog] = []review.GHReview{reviewOf(review.StateCommented, "8ce6d606", 30)}
	fake.infoFor[posthog] = gh.PRInfo{HeadRefName: "b", HeadRefOid: "b08edfd2"}
	fake.timelines[posthog] = requests.Timeline{Commits: []string{"1111aaaa", "8ce6d606", "2222bbbb", "3333cccc", "b08edfd2"}}

	fake.reviewsFor[rfc1224] = []review.GHReview{reviewOf(review.StateCommented, "914baa7c", 50)}
	fake.infoFor[rfc1224] = gh.PRInfo{HeadRefName: "b", HeadRefOid: "6dfa5764"}
	fake.timelines[rfc1224] = requests.Timeline{Commits: []string{"914baa7c", "6dfa5764"}, ForcePushes: []time.Time{at(10)}}

	fake.reviewsFor[rfc1218] = []review.GHReview{reviewOf(review.StateCommented, "c87ea62e", 20), draftOf("888d46dc")}
	fake.infoFor[rfc1218] = gh.PRInfo{HeadRefName: "b", HeadRefOid: "888d46dc"}
	fake.timelines[rfc1218] = requests.Timeline{Commits: []string{"4444dddd", "888d46dc"}, ForcePushes: []time.Time{at(40), at(5)}}

	fake.reviewsFor[interview] = []review.GHReview{reviewOf(review.StateCommented, "5555eeee", 4*365*24)}
	fake.infoFor[interview] = gh.PRInfo{HeadRefName: "b", HeadRefOid: "6666ffff"}
	fake.timelines[interview] = requests.Timeline{Commits: []string{"5555eeee", "6666ffff"}}

	fake.reviewsFor[approved] = []review.GHReview{reviewOf(review.StateChangesRequested, "7777aaaa", 48), reviewOf(review.StateApproved, "8888bbbb", 24)}
	fake.reviewsFor[approvedAfterComments] = []review.GHReview{reviewOf(review.StateCommented, "7777aaaa", 72), reviewOf(review.StateCommented, "7777aaaa", 48), reviewOf(review.StateApproved, "8888bbbb", 24)}
	fake.reviewsFor[repliesOnly] = []review.GHReview{replyOf("7777aaaa", 24)}

	f, err := svc.Requests(context.Background(), true)
	if err != nil {
		t.Fatalf("Requests: %v", err)
	}

	type found struct {
		ref     string
		inOrg   bool
		changes requests.Changes
	}
	var got []found
	for _, c := range f.Rereview.Candidates {
		got = append(got, found{c.Ref.String(), c.AuthorInOrg, c.Changes})
	}
	want := []found{
		{posthog.String(), true, requests.Changes{ReviewedAt: at(30), Commits: 3}},
		{rfc1224.String(), true, requests.Changes{ReviewedAt: at(50), Commits: 1, ForcePushes: 1}},
		{rfc1218.String(), true, requests.Changes{ReviewedAt: at(20), Commits: 2, ForcePushes: 1}},
		{interview.String(), false, requests.Changes{ReviewedAt: at(4 * 365 * 24), Commits: 1}},
	}
	if !slices.Equal(got, want) {
		t.Errorf("candidates = %+v\nwant %+v", got, want)
	}
	if errs := errors.Join(f.Rereview.Errs...); errs != nil {
		t.Errorf("errs = %v, want none", errs)
	}
}

// The likely cause of a failed read is a rate limit, so the first failure ends
// the checks. The candidates found before it stay. A pull request the checks
// did not finish never shows, because its row would claim the head moved.
func TestAFailedReadKeepsTheCandidatesFoundBeforeItAndDropsTheRest(t *testing.T) {
	failure := errors.New("HTTP 403: secondary rate limit")
	tests := []struct {
		name string
		fail func(*fakeGH, pr.Ref)
	}{
		{"the reviews", func(f *fakeGH, ref pr.Ref) { f.reviewErrs[ref] = failure }},
		{"the head", func(f *fakeGH, ref pr.Ref) { f.infoErrs[ref] = failure }},
		{"the timeline", func(f *fakeGH, ref pr.Ref) { f.timelineErrs[ref] = failure }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fake := candidateService(t)
			first, failing, last := seedPR(candRef(1), true), seedPR(candRef(2), true), seedPR(candRef(3), true)
			fake.requested[reviewedQuery] = []requests.PR{first, failing, last}
			for _, p := range []requests.PR{first, failing, last} {
				changedSince(fake, p.Ref)
			}
			tt.fail(fake, failing.Ref)

			f, err := svc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests: %v", err)
			}

			if got, want := candidateRefs(f), []string{"o/r#1"}; !slices.Equal(got, want) {
				t.Errorf("candidates = %v, want %v", got, want)
			}
			if errs := f.Rereview.Errs; len(errs) != 1 || !errors.Is(errs[0], failure) {
				t.Fatalf("errs = %v, want one that wraps %v", errs, failure)
			}
			if msg := f.Rereview.Errs[0].Error(); !strings.Contains(msg, "could not check 2 of") {
				t.Errorf("err = %q, want it to say the checks stopped", msg)
			}
			if fake.reads != 2 {
				t.Errorf("read reviews %d times, want 2: the check stops before #3", fake.reads)
			}
		})
	}
}

// A failed reviewed-by search leaves nothing to check. The team check reports
// it as before, and the candidates report it too.
func TestAFailedReviewedSearchLeavesNoCandidatesAndSaysWhy(t *testing.T) {
	failure := errors.New("rate limited")
	tests := []struct {
		name  string
		teams []string
	}{
		{"no teams", nil},
		{"a team with rows", []string{"PostHog/team-a"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, fake := candidateService(t)
			svc.Cfg.Teams = tt.teams
			fake.requested[teamQuery("PostHog/team-a")] = []requests.PR{seedPR(candRef(1), true)}
			fake.requestErrs = map[string]error{reviewedQuery: failure}

			f, err := svc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests: %v", err)
			}

			if f.Rereview == nil || len(f.Rereview.Candidates) != 0 || !errors.Is(errors.Join(f.Rereview.Errs...), failure) {
				t.Errorf("rereview = %+v, want no candidates and the search's failure", f.Rereview)
			}
			for _, team := range f.Teams {
				if !errors.Is(errors.Join(team.Errs...), failure) {
					t.Errorf("%s errs = %v, want the search's failure", team.Slug, team.Errs)
				}
			}
			if n := searchesFor(fake.searches, reviewedQuery); n != 1 {
				t.Errorf("reviewed-by searches = %d, want 1 in %v", n, fake.searches)
			}
		})
	}
}

// The team rows and the candidates must agree on whether the user has
// reviewed the head. Each case runs once through a team's search and once
// through the candidates. The latest real review is a comment or a request for
// changes in every case, so only the head decides.
func TestTheTeamCheckAndTheCandidatesAgreeOnWhetherTheHeadWasReviewed(t *testing.T) {
	tests := []struct {
		name     string
		reviews  []review.GHReview
		head     string
		reviewed bool
	}{
		{"the head is the commit of my latest review", []review.GHReview{reviewOf(review.StateChangesRequested, "old", 48), reviewOf(review.StateCommented, "head", 24)}, "head", true},
		{"the head is the commit of an older review", []review.GHReview{reviewOf(review.StateChangesRequested, "old", 48), reviewOf(review.StateCommented, "newer", 24)}, "old", true},
		{"the head is a commit nobody reviewed", []review.GHReview{reviewOf(review.StateCommented, "old", 24)}, "head", false},
		{"the head is my pending review's commit", []review.GHReview{reviewOf(review.StateCommented, "old", 24), draftOf("head")}, "head", false},
		{"the head is my reply's commit", []review.GHReview{reviewOf(review.StateChangesRequested, "old", 48), replyOf("head", 24)}, "head", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			seed := seedPR(candRef(1), true)
			arrange := func(fake *fakeGH) {
				fake.requested[reviewedQuery] = []requests.PR{seed}
				fake.reviewsFor[seed.Ref] = tt.reviews
				fake.infoFor[seed.Ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: tt.head}
				fake.timelines[seed.Ref] = requests.Timeline{Commits: []string{"old", "head"}}
			}

			teamSvc, teamFake := candidateService(t)
			teamSvc.Cfg.Teams = []string{"PostHog/team-a"}
			teamFake.requested[teamQuery("PostHog/team-a")] = []requests.PR{seed}
			arrange(teamFake)
			teamF, err := teamSvc.Requests(context.Background(), false)
			if err != nil {
				t.Fatalf("Requests through the team: %v", err)
			}
			candSvc, candFake := candidateService(t)
			arrange(candFake)
			candF, err := candSvc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests for candidates: %v", err)
			}

			keptByTeam := len(teamF.Teams[0].PRs) == 1
			candidate := len(candidateRefs(candF)) == 1
			if keptByTeam != candidate {
				t.Errorf("the team kept the row: %v, but it is a candidate: %v", keptByTeam, candidate)
			}
			if keptByTeam == tt.reviewed {
				t.Errorf("the team kept the row: %v, want %v", keptByTeam, !tt.reviewed)
			}
		})
	}
}

// The search lists only open pull requests, but one can merge or close before
// docket reads its head. The read says so, and the pull request is then no
// candidate.
func TestAPullRequestThatClosedAfterTheSearchIsNoCandidate(t *testing.T) {
	for _, state := range []review.PRState{review.PRMerged, review.PRClosed} {
		t.Run(string(state), func(t *testing.T) {
			svc, fake := candidateService(t)
			seed := seedPR(candRef(1), true)
			fake.requested[reviewedQuery] = []requests.PR{seed}
			changedSince(fake, seed.Ref)
			fake.infoFor[seed.Ref] = gh.PRInfo{HeadRefName: "branch", HeadRefOid: "new", State: state}

			f, err := svc.Requests(context.Background(), true)
			if err != nil {
				t.Fatalf("Requests: %v", err)
			}

			if got := candidateRefs(f); len(got) != 0 {
				t.Errorf("candidates = %v, want none", got)
			}
			if fake.timelineReads != 0 {
				t.Errorf("read timelines %d times, want none", fake.timelineReads)
			}
		})
	}
}

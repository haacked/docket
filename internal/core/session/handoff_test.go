package session

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

// handoffService is a drafted review of unlisted (haacked/docket#7) by a user on
// three teams, two of them in the pull request's organization. GitHub has asked
// one of those two to review it, and a team the user is not on.
func handoffService(t *testing.T) (*Service, *fakeGH, review.Record) {
	t.Helper()
	ghc := &fakeGH{login: "haacked", info: prInfo(), requestedTeams: []string{"core", "web"}}
	svc, _ := newService(t, ghc, newFakeGit())
	svc.Cfg.Teams = []string{"haacked/core", "haacked/ops", "posthog/core"}
	rec := drafted(t, svc, ghc, unlisted)
	return svc, ghc, rec
}

func TestSubmittingRequestChangesHandsTheReviewBackToTheTeam(t *testing.T) {
	svc, ghc, rec := handoffService(t)

	done, handoff, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if want := []string{"haacked/docket#7 haacked/core"}; !slices.Equal(ghc.teamRequests, want) {
		t.Errorf("team requests = %q, want %q: only the user's team in the pull request's org that was requested", ghc.teamRequests, want)
	}
	if want := []string{"haacked/docket#7 haacked"}; !slices.Equal(ghc.unassigns, want) {
		t.Errorf("unassigns = %q, want %q", ghc.unassigns, want)
	}
	if ghc.commentReads != 0 {
		t.Errorf("read the review's comments %d times, want none: a request for changes blocks whatever its comments say", ghc.commentReads)
	}
	if want := (Handoff{Teams: []string{"haacked/core"}, Unassigned: true}); !reflect.DeepEqual(handoff, want) {
		t.Errorf("handoff = %+v, want %+v", handoff, want)
	}
	if done.State != review.StateArchived {
		t.Errorf("state = %q, want archived", done.State)
	}
}

// The user types teams in config.toml or picks them from GitHub's list, so the
// case of a slug can differ from the one the pull request's events carry.
func TestHandoffMatchesTeamsWithoutCase(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	svc.Cfg.Teams = []string{"Haacked/Core"}

	_, handoff, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if want := []string{"haacked/docket#7 Haacked/Core"}; !slices.Equal(ghc.teamRequests, want) {
		t.Errorf("team requests = %q, want %q", ghc.teamRequests, want)
	}
	if !slices.Equal(handoff.Teams, []string{"Haacked/Core"}) {
		t.Errorf("handoff teams = %q, want the configured spelling", handoff.Teams)
	}
}

func TestSubmittingACommentWithABlockingFindingHandsTheReviewBack(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	ghc.comments = map[int64][]string{pendingID: {"`nit`: rename this", "`blocking`: this drops the lock"}}

	_, handoff, err := svc.Submit(context.Background(), rec, review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.teamRequests) != 1 || len(ghc.unassigns) != 1 {
		t.Errorf("team requests = %q, unassigns = %q, want one of each", ghc.teamRequests, ghc.unassigns)
	}
	if !handoff.Unassigned || !slices.Equal(handoff.Teams, []string{"haacked/core"}) {
		t.Errorf("handoff = %+v, want the user unassigned and haacked/core requested", handoff)
	}
}

func TestSubmittingANonBlockingReviewKeepsTheUserOnIt(t *testing.T) {
	tests := []struct {
		name         string
		event        string
		comments     []string
		commentReads int
	}{
		{"a comment with no blocking finding", review.EventComment, []string{"`suggestion`: rename this", "This is non-blocking."}, 1},
		{"a comment with no inline comments", review.EventComment, nil, 1},
		{"an approval", review.EventApprove, []string{"`blocking`: an approval never hands back"}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ghc, rec := handoffService(t)
			ghc.comments = map[int64][]string{pendingID: tt.comments}

			_, handoff, err := svc.Submit(context.Background(), rec, tt.event, "")
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}

			if len(ghc.teamRequests) != 0 || len(ghc.unassigns) != 0 {
				t.Errorf("team requests = %q, unassigns = %q, want neither", ghc.teamRequests, ghc.unassigns)
			}
			if ghc.commentReads != tt.commentReads {
				t.Errorf("read the review's comments %d times, want %d", ghc.commentReads, tt.commentReads)
			}
			if handoff.String() != "" {
				t.Errorf("handoff = %q, want nothing to report", handoff.String())
			}
		})
	}
}

// The author of a pull request is not its reviewer, so there is nothing to hand
// back. GitHub refuses a request for changes on your own pull request anyway.
func TestSubmittingABlockingReviewOfMyOwnPullRequestHandsNothingBack(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	rec.Author = "Haacked"
	ghc.comments = map[int64][]string{pendingID: {"`blocking`: this drops the lock"}}

	_, handoff, err := svc.Submit(context.Background(), rec, review.EventComment, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if len(ghc.teamRequests) != 0 || len(ghc.unassigns) != 0 || ghc.commentReads != 0 {
		t.Errorf("team requests = %q, unassigns = %q, comment reads = %d, want none", ghc.teamRequests, ghc.unassigns, ghc.commentReads)
	}
	if handoff.String() != "" {
		t.Errorf("handoff = %q, want nothing to report", handoff.String())
	}
}

// A blocking review still takes the user off the pull request when no team of
// the user's was asked to review it. The handoff says that no team was asked.
func TestHandoffWithNoRequestedTeamOfMineStillUnassigns(t *testing.T) {
	tests := []struct {
		name      string
		teams     []string
		requested []string
	}{
		{"none of my teams was requested", []string{"haacked/ops"}, []string{"core"}},
		{"my team is in another org", []string{"posthog/core"}, []string{"core"}},
		{"no team was requested", []string{"haacked/core"}, nil},
		{"I configured no teams", nil, []string{"core"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ghc, rec := handoffService(t)
			svc.Cfg.Teams = tt.teams
			ghc.requestedTeams = tt.requested

			_, handoff, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "")
			if err != nil {
				t.Fatalf("Submit: %v", err)
			}

			if len(ghc.teamRequests) != 0 {
				t.Errorf("team requests = %q, want none", ghc.teamRequests)
			}
			if len(ghc.unassigns) != 1 {
				t.Errorf("unassigns = %q, want one", ghc.unassigns)
			}
			if !handoff.NoTeam || !handoff.Unassigned || len(handoff.Errs) != 0 {
				t.Errorf("handoff = %+v, want NoTeam and Unassigned", handoff)
			}
		})
	}
}

// The review is on GitHub before any handoff step runs. The handoff reports a
// step that fails. The submit still counts.
func TestAHandoffThatFailsStillCountsTheSubmit(t *testing.T) {
	failure := errors.New("gh: HTTP 502")
	tests := []struct {
		name        string
		event       string
		breakIt     func(*fakeGH)
		unassigned  bool
		teams       []string
		unassigns   int
		requestsRun int
	}{
		{
			name:    "the comments cannot be read",
			event:   review.EventComment,
			breakIt: func(f *fakeGH) { f.commentsErr = failure },
		},
		{
			name:       "the requested teams cannot be read",
			event:      review.EventRequestChanges,
			breakIt:    func(f *fakeGH) { f.requestedTeamsErr = failure },
			unassigned: true,
			unassigns:  1,
		},
		{
			name:        "GitHub refuses the team request",
			event:       review.EventRequestChanges,
			breakIt:     func(f *fakeGH) { f.teamRequestErr = failure },
			unassigned:  true,
			unassigns:   1,
			requestsRun: 1,
		},
		{
			name:        "GitHub refuses the unassign",
			event:       review.EventRequestChanges,
			breakIt:     func(f *fakeGH) { f.unassignErr = failure },
			teams:       []string{"haacked/core"},
			unassigns:   1,
			requestsRun: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, ghc, rec := handoffService(t)
			tt.breakIt(ghc)

			done, handoff, err := svc.Submit(context.Background(), rec, tt.event, "")
			if err != nil {
				t.Fatalf("Submit: %v, want the submit to count", err)
			}

			if done.State != review.StateArchived {
				t.Errorf("state = %q, want archived", done.State)
			}
			if len(ghc.unassigns) != tt.unassigns || len(ghc.teamRequests) != tt.requestsRun {
				t.Errorf("unassigns = %q, team requests = %q, want %d and %d", ghc.unassigns, ghc.teamRequests, tt.unassigns, tt.requestsRun)
			}
			if handoff.Unassigned != tt.unassigned || !slices.Equal(handoff.Teams, tt.teams) {
				t.Errorf("handoff = %+v, want Unassigned %v and teams %q", handoff, tt.unassigned, tt.teams)
			}
			if len(handoff.Errs) != 1 || !errors.Is(handoff.Errs[0], failure) {
				t.Errorf("handoff errors = %v, want one that wraps %v", handoff.Errs, failure)
			}
			if !strings.Contains(handoff.String(), failure.Error()) {
				t.Errorf("handoff = %q, want it to report %q", handoff.String(), failure.Error())
			}
		})
	}
}

// A pull request can ask the user to review it without assigning the user, so
// there is no assignee to remove, and the handoff does not claim one.
func TestHandoffOfAReviewerWhoIsNotAssignedSaysNothingAboutAssignees(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	ghc.notAssigned = true

	_, handoff, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if handoff.Unassigned || len(handoff.Errs) != 0 {
		t.Errorf("handoff = %+v, want nothing unassigned and no error", handoff)
	}
	if want := "docket asked haacked/core to review it"; handoff.String() != want {
		t.Errorf("handoff = %q, want %q", handoff.String(), want)
	}
}

// A team request can only name a team of the repository's own organization, so
// the pull request's events have nothing to say about any other team.
func TestHandoffReadsNoEventsWhenNoTeamOfMineIsInTheOrg(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	svc.Cfg.Teams = []string{"posthog/core"}

	_, handoff, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "")
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	if ghc.eventReads != 0 {
		t.Errorf("read the events %d times, want none", ghc.eventReads)
	}
	if !handoff.NoTeam {
		t.Errorf("handoff = %+v, want NoTeam", handoff)
	}
}

// A read of GitHub that fails after the submit is the only thing a caller
// reports, so the handoff that already happened travels in it.
func TestASubmitWhoseReadBackFailsReportsTheHandoffInTheError(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	ghc.reviewErr = errors.New("gh: HTTP 502")

	_, _, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, "")

	if err == nil || !errors.Is(err, ghc.reviewErr) {
		t.Fatalf("error = %v, want it to wrap %v", err, ghc.reviewErr)
	}
	if want := "docket asked haacked/core to review it and removed you from its assignees"; !strings.HasSuffix(err.Error(), ". "+want) {
		t.Errorf("error = %q, want it to end with %q", err, want)
	}
}

func TestASubmitGitHubRefusesHandsNothingBack(t *testing.T) {
	svc, ghc, rec := handoffService(t)
	ghc.submitErr = errors.New("gh: HTTP 422")

	if _, _, err := svc.Submit(context.Background(), rec, review.EventRequestChanges, ""); err == nil {
		t.Fatal("Submit succeeded although GitHub refused it")
	}

	if len(ghc.teamRequests) != 0 || len(ghc.unassigns) != 0 {
		t.Errorf("team requests = %q, unassigns = %q, want neither", ghc.teamRequests, ghc.unassigns)
	}
}

func TestHandoffStringSaysWhatHappened(t *testing.T) {
	failure := errors.New("gh: HTTP 502")
	tests := []struct {
		name    string
		handoff Handoff
		want    string
	}{
		{"nothing", Handoff{}, ""},
		{
			"both steps",
			Handoff{Teams: []string{"posthog/core", "posthog/web"}, Unassigned: true},
			"docket asked posthog/core and posthog/web to review it and removed you from its assignees",
		},
		{
			"no team",
			Handoff{NoTeam: true, Unassigned: true},
			"docket removed you from its assignees. None of your teams was asked to review it, so docket asked no team",
		},
		{
			"a failure",
			Handoff{Unassigned: true, Errs: []error{failure}},
			"docket removed you from its assignees. The handoff to your team failed: gh: HTTP 502",
		},
		{
			"only a failure",
			Handoff{Errs: []error{failure}},
			"The handoff to your team failed: gh: HTTP 502",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.handoff.String(); got != tt.want {
				t.Errorf("String() = %q\nwant       %q", got, tt.want)
			}
		})
	}
}

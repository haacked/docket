package review_test

import (
	"errors"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

// gh pr view names a GitHub App's pull requests app/<name>, and the search API
// names the same author <name>[bot]. The requests screen reads the second and
// Prepare reads the first, so fix_authors has to match both whichever one the
// user wrote down.
func TestListedAuthorMatchesAnAppUnderEitherSpelling(t *testing.T) {
	tests := []struct {
		name  string
		list  []string
		login string
		want  bool
	}{
		{name: "gh pr view spelling in the list and the login", list: []string{"app/posthog"}, login: "app/posthog", want: true},
		{name: "search spelling of the login", list: []string{"app/posthog"}, login: "posthog[bot]", want: true},
		{name: "search spelling in the list", list: []string{"posthog[bot]"}, login: "app/posthog", want: true},
		{name: "search spelling in both", list: []string{"posthog[bot]"}, login: "posthog[bot]", want: true},
		{name: "login in another case", list: []string{"app/posthog"}, login: "App/PostHog", want: true},
		{name: "search login in another case", list: []string{"app/posthog"}, login: "PostHog[bot]", want: true},
		{name: "list entry in another case", list: []string{"APP/PostHog"}, login: "app/posthog", want: true},
		{name: "a person's login matches itself", list: []string{"haacked"}, login: "HaackeD", want: true},
		{name: "later entry matches", list: []string{"app/dependabot", "app/posthog"}, login: "posthog[bot]", want: true},
		// A person's login can hold neither / nor [. A person who happens to share
		// the app's name is somebody else.
		{name: "a person named like the app", list: []string{"app/posthog"}, login: "posthog", want: false},
		{name: "the app when a person is listed", list: []string{"posthog"}, login: "app/posthog", want: false},
		{name: "another app", list: []string{"app/posthog"}, login: "app/dependabot", want: false},
		{name: "empty list", list: nil, login: "app/posthog", want: false},
		{name: "empty login", list: []string{"app/posthog"}, login: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := review.ListedAuthor(tt.list, tt.login); got != tt.want {
				t.Errorf("ListedAuthor(%q, %q) = %v, want %v", tt.list, tt.login, got, tt.want)
			}
		})
	}
}

// The new review screen and start_review send these strings, so they are part
// of the message format.
func TestFixChoiceUsesTheSpelledOutValues(t *testing.T) {
	tests := []struct {
		choice review.FixChoice
		want   string
	}{
		{choice: review.FixAuto, want: ""},
		{choice: review.FixOn, want: "fix"},
		{choice: review.FixOff, want: "draft"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			if string(tt.choice) != tt.want {
				t.Errorf("choice = %q, want %q", tt.choice, tt.want)
			}
		})
	}
}

// An explicit choice wins over fix_authors. Auto leaves it to the list.
func TestAFixChoiceDecidesWhetherTheReviewFixes(t *testing.T) {
	tests := []struct {
		name   string
		choice review.FixChoice
		listed bool
		want   bool
	}{
		{name: "auto with a listed author", choice: review.FixAuto, listed: true, want: true},
		{name: "auto with an author not listed", choice: review.FixAuto, listed: false, want: false},
		{name: "fix with an author not listed", choice: review.FixOn, listed: false, want: true},
		{name: "fix with a listed author", choice: review.FixOn, listed: true, want: true},
		{name: "draft with a listed author", choice: review.FixOff, listed: true, want: false},
		{name: "draft with an author not listed", choice: review.FixOff, listed: false, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.choice.Fixes(tt.listed); got != tt.want {
				t.Errorf("%q.Fixes(%v) = %v, want %v", tt.choice, tt.listed, got, tt.want)
			}
		})
	}
}

func TestTheFixStatesReadAsWhatTheUserDoesNext(t *testing.T) {
	tests := []struct {
		state review.State
		want  string
	}{
		{state: review.StateFixed, want: "fixes to push"},
		{state: review.StatePushed, want: "ready to approve"},
	}

	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			if got := tt.state.Label(); got != tt.want {
				t.Errorf("label = %q, want %q", got, tt.want)
			}
		})
	}
}

// The strings are stored in the index, so they are part of the file format.
func TestTheFixStatesUseTheSpelledOutValues(t *testing.T) {
	if review.StateFixed != "fixed" {
		t.Errorf("fixed state = %q", review.StateFixed)
	}
	if review.StatePushed != "pushed" {
		t.Errorf("pushed state = %q", review.StatePushed)
	}
}

// append refuses a state with no event, and compaction refuses an event Fold
// does not know. Either gap drops a fix row from the index.
func TestTheFixStatesHaveEventsThatRoundTrip(t *testing.T) {
	for _, state := range []review.State{review.StateFixed, review.StatePushed} {
		t.Run(string(state), func(t *testing.T) {
			eventType, ok := review.EventForState(state)
			if !ok {
				t.Fatalf("state %q has no event type, so appending the record fails", state)
			}
			if !review.KnownEvent(review.Event{ID: "rec-1", Type: eventType}) {
				t.Errorf("event %q is unknown to Fold, so compaction stops", eventType)
			}

			got := review.Fold([]review.Event{
				event("rec-1", foldBase, "prepared", map[string]any{"fix": true}),
				event("rec-1", foldBase.Add(time.Minute), eventType, map[string]any{"fix_head": "abc123"}),
			})

			if len(got) != 1 || got[0].State != state {
				t.Fatalf("Fold = %+v, want one record in %q", got, state)
			}
			if !got[0].Fix {
				t.Error("the fold dropped the fix flag the first event carried")
			}
			if got[0].FixHead != "abc123" {
				t.Errorf("fix head = %q, want abc123", got[0].FixHead)
			}
		})
	}
}

func TestTheFixStatesAreOpenAndSettled(t *testing.T) {
	for _, state := range []review.State{review.StateFixed, review.StatePushed} {
		t.Run(string(state), func(t *testing.T) {
			rec := review.Record{State: state, Mode: review.ModeBackground}

			if !rec.State.Open() {
				t.Error("the row is closed, so the dashboard hides fixes the user still has to act on")
			}
			if rec.InProgress() {
				t.Error("the row reads as in progress, so u and c refuse it")
			}
			if rec.InBackgroundSession() {
				t.Error("the row reads as a running session, so r and R refuse it")
			}
		})
	}
}

// A pushed row has no pending review. Submitting it posts a new one on the
// commit the fixes ended at, so it needs no review id. A fixed row has changes
// that are not on GitHub yet, and approving would approve code GitHub has not
// seen.
func TestOnlyAPushedFixRowIsSubmittable(t *testing.T) {
	tests := []struct {
		name string
		rec  review.Record
		want bool
	}{
		{name: "pushed in the background", rec: review.Record{Fix: true, State: review.StatePushed, Mode: review.ModeBackground}, want: true},
		{name: "pushed in the terminal", rec: review.Record{Fix: true, State: review.StatePushed, Mode: review.ModeInteractive}, want: true},
		{name: "fixed in the background", rec: review.Record{Fix: true, State: review.StateFixed, Mode: review.ModeBackground}, want: false},
		{name: "fixed in the terminal", rec: review.Record{Fix: true, State: review.StateFixed, Mode: review.ModeInteractive}, want: false},
		{name: "fixed with a stale review id", rec: review.Record{Fix: true, State: review.StateFixed, Mode: review.ModeBackground, ReviewID: 9}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.rec.Submittable(); got != tt.want {
				t.Errorf("Submittable() = %v, want %v", got, tt.want)
			}
		})
	}
}

// PostHog deletes a head branch when its pull request merges. A pushed row then
// has nothing left to approve and archives. A fixed row still holds work that
// is not on GitHub, and archiving would delete it with the checkout.
func TestAMergedPushedRowIsFinishedAndAFixedOneIsNot(t *testing.T) {
	tests := []struct {
		name  string
		state review.State
		pr    review.PRState
		want  bool
	}{
		{name: "pushed on a merged pull request", state: review.StatePushed, pr: review.PRMerged, want: true},
		{name: "pushed on a closed pull request", state: review.StatePushed, pr: review.PRClosed, want: true},
		{name: "pushed on an open pull request", state: review.StatePushed, pr: review.PROpen, want: false},
		{name: "pushed with an unknown state", state: review.StatePushed, pr: "", want: false},
		{name: "fixed on a merged pull request", state: review.StateFixed, pr: review.PRMerged, want: false},
		{name: "fixed on a closed pull request", state: review.StateFixed, pr: review.PRClosed, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := review.Record{Fix: true, State: tt.state, PRState: tt.pr}
			if got := rec.Finished(); got != tt.want {
				t.Errorf("Finished() = %v, want %v", got, tt.want)
			}
		})
	}
}

// A tier-3 worktree is a directory of the record's own, as a tier-2 clone is.
// Every tier-1 record shares the scratch directory, so none of them has one.
func TestHasCheckoutCoversTheClonesAndWorktreesDocketMade(t *testing.T) {
	tests := []struct {
		name string
		tier tier.Tier
		dir  string
		want bool
	}{
		{name: "tier 1 in the scratch directory", tier: tier.Tier1, dir: "/home/me/.docket/scratch", want: false},
		{name: "tier 2 clone", tier: tier.Tier2, dir: "/home/me/.docket/clones/o/r/pr-1", want: true},
		{name: "tier 3 worktree", tier: tier.Tier3, dir: "/home/me/.docket/worktrees/o/r/pr-1", want: true},
		{name: "tier 2 before the clone landed", tier: tier.Tier2, dir: "", want: false},
		{name: "tier 3 before the worktree landed", tier: tier.Tier3, dir: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := review.Record{Tier: tt.tier, Dir: tt.dir}
			if got := rec.HasCheckout(); got != tt.want {
				t.Errorf("HasCheckout() = %v, want %v", got, tt.want)
			}
		})
	}
}

// localWork compares the checkout against <remote>/<branch>, the ref the fetch
// updates.
func TestUpstreamIsTheRemoteTrackingRefOfTheHeadBranch(t *testing.T) {
	rec := review.Record{Remote: "upstream", Branch: "posthog/fix-thing"}

	if got := rec.Upstream(); got != "upstream/posthog/fix-thing" {
		t.Errorf("Upstream() = %q, want upstream/posthog/fix-thing", got)
	}
}

// Local work is anything deleting the checkout would lose: an edit not yet
// committed, an untracked file, or a commit not yet pushed.
func TestACheckoutWithLocalWorkIsFixedAndACleanOneIsPushed(t *testing.T) {
	tests := []struct {
		name      string
		checkout  review.Checkout
		wantLocal bool
		wantState review.State
	}{
		{name: "uncommitted changes", checkout: review.Checkout{Head: "abc", Dirty: true}, wantLocal: true, wantState: review.StateFixed},
		{name: "commits not pushed", checkout: review.Checkout{Head: "abc", Ahead: 2}, wantLocal: true, wantState: review.StateFixed},
		{name: "both", checkout: review.Checkout{Head: "abc", Dirty: true, Ahead: 1}, wantLocal: true, wantState: review.StateFixed},
		{name: "clean and pushed", checkout: review.Checkout{Head: "abc"}, wantLocal: false, wantState: review.StatePushed},
		// A failed fetch falls back to the ref of the last fetch that worked. The
		// failure does not count as work on its own.
		{name: "clean after a failed fetch", checkout: review.Checkout{Head: "abc", FetchErr: errors.New("couldn't find remote ref")}, wantLocal: false, wantState: review.StatePushed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.checkout.Local(); got != tt.wantLocal {
				t.Errorf("Local() = %v, want %v", got, tt.wantLocal)
			}
			if got := tt.checkout.State("base", true); got != tt.wantState {
				t.Errorf("State() = %q, want %q", got, tt.wantState)
			}
		})
	}
}

// Every event carries the whole record, so the keys the fix fields are stored
// under are part of the file format.
func TestTheFixFieldsAreStoredUnderTheirOwnKeys(t *testing.T) {
	got := review.Fold([]review.Event{
		event("rec-1", foldBase, "prepared", map[string]any{
			"fix":         true,
			"branch":      "posthog/fix-thing",
			"remote":      "origin",
			"worktree_of": "/home/me/dev/posthog/posthog",
			"fix_base":    "base",
			"fix_head":    "head",
		}),
	})

	if len(got) != 1 {
		t.Fatalf("Fold = %+v, want one record", got)
	}
	want := review.Record{
		ID:         "rec-1",
		State:      review.StatePreparing,
		Fix:        true,
		Branch:     "posthog/fix-thing",
		Remote:     "origin",
		WorktreeOf: "/home/me/dev/posthog/posthog",
		FixBase:    "base",
		FixHead:    "head",
	}
	rec := got[0]
	if rec.Fix != want.Fix || rec.Branch != want.Branch || rec.Remote != want.Remote ||
		rec.WorktreeOf != want.WorktreeOf || rec.FixBase != want.FixBase || rec.FixHead != want.FixHead {
		t.Errorf("Fold = %+v, want the fix fields of %+v", rec, want)
	}
}

// review-code writes its notes after the fix pass. A clean checkout still on the
// commit it started from, with no notes from this session, is a session that
// ended before the review finished. Moving the head or writing the notes both
// show the session got that far.
func TestACleanCheckoutOnItsBaseWithoutNotesIsUnreviewed(t *testing.T) {
	tests := []struct {
		name  string
		head  string
		notes bool
		want  review.State
	}{
		{name: "on base, no notes", head: "base", notes: false, want: review.StateUnreviewed},
		{name: "on base, notes written", head: "base", notes: true, want: review.StatePushed},
		{name: "head moved, no notes", head: "fixed", notes: false, want: review.StatePushed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := (review.Checkout{Head: tt.head}).State("base", tt.notes); got != tt.want {
				t.Errorf("State = %q, want %q", got, tt.want)
			}
		})
	}
	if got := (review.Checkout{Head: "base", Dirty: true}).State("base", false); got != review.StateFixed {
		t.Errorf("a dirty checkout on its base = %q, want fixed", got)
	}
}

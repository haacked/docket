package dashboard

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func fixRow(id string, number int, state review.State, base, head string) review.Record {
	return review.Record{
		ID:        id,
		Ref:       pr.Ref{Org: "PostHog", Repo: "posthog", Number: number},
		Title:     "Fix the retry loop",
		Author:    "app/posthog",
		Engine:    "claude",
		State:     state,
		StartedAt: time.Now(),
		Fix:       true,
		FixBase:   base,
		FixHead:   head,
	}
}

// wide is the dashboard with room for every row's whole metadata.
func wide(records ...review.Record) Model {
	m := New(Styles{})
	m.Width = 240
	m.Records = records
	return m
}

func TestFixRowsAreListedAndSelectable(t *testing.T) {
	m := wide(
		fixRow("fixed", 1, review.StateFixed, "a", "b"),
		fixRow("pushed", 2, review.StatePushed, "a", "b"),
	)

	rows := m.rows()

	if len(rows) != 2 {
		t.Fatalf("rows = %+v, want both fix rows", rows)
	}
}

// Rows that need the user come ahead of drafts, after the reviews that did not
// start.
func TestTheFixGroupsComeAfterDidNotStartAndBeforeDrafted(t *testing.T) {
	m := wide(
		review.Record{ID: "d", Ref: pr.Ref{Org: "o", Repo: "r", Number: 4}, State: review.StateDrafted, StartedAt: time.Now()},
		fixRow("pushed", 2, review.StatePushed, "a", "b"),
		review.Record{ID: "n", Ref: pr.Ref{Org: "o", Repo: "r", Number: 3}, State: review.StateNotStarted, Mode: review.ModeBackground},
		fixRow("fixed", 1, review.StateFixed, "a", "b"),
	)

	view := m.View()

	order := []string{"Did not start (1)", "Fixes to push (1)", "Ready to approve (1)", "Drafted (1)"}
	last := -1
	for _, title := range order {
		at := strings.Index(view, title)
		if at < 0 {
			t.Fatalf("the view has no %q group:\n%s", title, view)
		}
		if at < last {
			t.Errorf("%q is out of order, want %v:\n%s", title, order, view)
		}
		last = at
	}
}

// The cursor walks the rows in the order they are drawn.
func TestTheCursorReachesFixRowsInTheOrderTheyAreDrawn(t *testing.T) {
	m := wide(
		review.Record{ID: "d", Ref: pr.Ref{Org: "o", Repo: "r", Number: 4}, State: review.StateDrafted, StartedAt: time.Now()},
		fixRow("pushed", 2, review.StatePushed, "a", "b"),
		fixRow("fixed", 1, review.StateFixed, "a", "b"),
	)

	var ids []string
	for range 3 {
		rec, _ := m.Selected()
		ids = append(ids, rec.ID)
		m, _ = m.Update(key("j"))
	}

	if got := strings.Join(ids, ","); got != "fixed,pushed,d" {
		t.Errorf("cursor visited %s, want fixed,pushed,d", got)
	}
}

func TestAFixRowSaysItIsAFixReview(t *testing.T) {
	view := wide(fixRow("fixed", 1, review.StateFixed, "a", "b")).View()

	if !strings.Contains(view, "· fix") {
		t.Errorf("the row does not say it is a fix review:\n%s", view)
	}
}

func TestADraftRowDoesNotSayFix(t *testing.T) {
	rec := review.Record{ID: "d", Ref: pr.Ref{Org: "o", Repo: "r", Number: 4}, Title: "A change", State: review.StateDrafted, StartedAt: time.Now(), Engine: "claude"}

	if view := wide(rec).View(); strings.Contains(view, "· fix") {
		t.Errorf("a draft review's row says fix:\n%s", view)
	}
}

// review-code found nothing it could fix cleanly. Approving then approves the
// pull request as the bot left it, and the row says so.
func TestAPushedRowWhoseHeadIsStillTheBaseSaysNoChanges(t *testing.T) {
	tests := []struct {
		name string
		rec  review.Record
		want bool
	}{
		{name: "pushed with the head at the base", rec: fixRow("p", 1, review.StatePushed, "base", "base"), want: true},
		{name: "pushed with new commits", rec: fixRow("p", 1, review.StatePushed, "base", "fixed"), want: false},
		{name: "fixed with the head at the base", rec: fixRow("f", 1, review.StateFixed, "base", "base"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := wide(tt.rec).View()

			if got := strings.Contains(view, "no changes"); got != tt.want {
				t.Errorf("the row says no changes = %v, want %v:\n%s", got, tt.want, view)
			}
		})
	}
}

// enter opens a fixed row's session, where the user asks the agent to commit
// and push. s opens the submit screen for a pushed row.
func TestKeysOnFixRowsEmitTheirIntents(t *testing.T) {
	tests := []struct {
		name  string
		state review.State
		key   tea.KeyPressMsg
		want  tea.Msg
	}{
		{name: "enter on a fixed row", state: review.StateFixed, key: named(tea.KeyEnter), want: msg.Resume{ID: "f"}},
		{name: "s on a pushed row", state: review.StatePushed, key: key("s"), want: msg.OpenSubmit{ID: "f"}},
		{name: "x on a fixed row", state: review.StateFixed, key: key("x"), want: msg.Abandon{ID: "f"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, cmd := wide(fixRow("f", 1, tt.state, "a", "b")).Update(tt.key)
			if cmd == nil {
				t.Fatalf("%s produced no command", tt.name)
			}
			if got := cmd(); got != tt.want {
				t.Errorf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

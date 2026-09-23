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

// adoptedRecord is a row taken over from an existing review, with no review
// session of its own.
func adoptedRecord() review.Record {
	return review.Record{
		ID:        "d",
		Ref:       pr.Ref{Org: "o", Repo: "r", Number: 4},
		State:     review.StateReviewed,
		Intent:    review.IntentAsk,
		StartedAt: time.Now(),
	}
}

func only(rec review.Record) Model {
	m := New(Styles{})
	m.Records = []review.Record{rec}
	return m
}

func TestAskAndRereviewKeysEmitIntents(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want tea.Msg
	}{
		{"ask", key("c"), msg.Ask{ID: "a"}},
		{"re-review", key("u"), msg.OpenRereview{ID: "a"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cmd := newModel().Update(tc.key)
			if cmd == nil {
				t.Fatalf("%s produced no command", tc.name)
			}
			if got := cmd(); got != tc.want {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestAskAndRereviewNeedARecord(t *testing.T) {
	m := New(Styles{})

	for _, press := range []tea.KeyPressMsg{key("c"), key("u")} {
		if _, cmd := m.Update(press); cmd != nil {
			t.Errorf("%s with no records produced %#v", press, cmd())
		}
	}
}

// A plain resume of a row with no review session falls through to a fresh
// review-code start with no flag, which stops at the prompt the user answered.
func TestEnterOnAReviewedRowWithNoSessionOffersTheReReview(t *testing.T) {
	_, cmd := only(adoptedRecord()).Update(named(tea.KeyEnter))

	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if got, want := cmd(), (msg.OpenRereview{ID: "d"}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// A row a review ran on keeps enter as a resume, whatever its session id says.
func TestEnterOnAReviewRowStillResumes(t *testing.T) {
	rec := adoptedRecord()
	rec.State = review.StateUnreviewed
	rec.Intent = review.IntentReview

	_, cmd := only(rec).Update(named(tea.KeyEnter))

	if cmd == nil {
		t.Fatal("enter produced no command")
	}
	if got, want := cmd(), (msg.Resume{ID: "d"}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

// reviewed is an open state. A row the dashboard does not list is one the user
// can neither ask about nor submit.
func TestAReviewedRowIsVisibleAndSelectable(t *testing.T) {
	m := only(adoptedRecord())

	rec, ok := m.Selected()
	if !ok || rec.ID != "d" {
		t.Fatalf("selected %+v (%v), want the reviewed row", rec, ok)
	}
	if view := m.View(); !strings.Contains(view, "o/r#4") {
		t.Errorf("view does not list the reviewed row:\n%s", view)
	}
}

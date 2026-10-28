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

func TestRereviewKeyEmitsItsIntent(t *testing.T) {
	_, cmd := newModel().Update(key("u"))
	if cmd == nil {
		t.Fatal("u produced no command")
	}
	if got, want := cmd(), (msg.OpenRereview{ID: "a"}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestRereviewNeedsARecord(t *testing.T) {
	if _, cmd := New(Styles{}).Update(key("u")); cmd != nil {
		t.Errorf("u with no records produced %#v", cmd())
	}
}

// The root decides whether enter opens the review session or a session about
// the notes, so the dashboard sends the same intent for every row.
func TestEnterResumesTheRow(t *testing.T) {
	_, cmd := only(adoptedRecord()).Update(named(tea.KeyEnter))
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

package dashboard

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func key(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func named(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func records() []review.Record {
	return []review.Record{
		{ID: "a", Ref: pr.Ref{Org: "o", Repo: "r", Number: 1}, State: review.StateReviewing, StartedAt: time.Now()},
		{ID: "b", Ref: pr.Ref{Org: "o", Repo: "r", Number: 2}, State: review.StateDrafted, StartedAt: time.Now()},
		{ID: "c", Ref: pr.Ref{Org: "o", Repo: "r", Number: 3}, State: review.StateArchived, StartedAt: time.Now()},
	}
}

func newModel() Model {
	m := New(Styles{})
	m.Records = records()
	return m
}

func TestCursorSkipsArchivedUntilShown(t *testing.T) {
	m := newModel()

	if got := len(m.rows()); got != 2 {
		t.Fatalf("visible rows = %d, want 2", got)
	}

	m, _ = m.Update(key("a"))
	if got := len(m.rows()); got != 3 {
		t.Fatalf("visible rows after a = %d, want 3", got)
	}
}

func TestCursorStopsAtEnds(t *testing.T) {
	m := newModel()

	m, _ = m.Update(key("k"))
	if m.Cursor != 0 {
		t.Errorf("cursor after k at top = %d, want 0", m.Cursor)
	}

	for range 5 {
		m, _ = m.Update(key("j"))
	}
	if m.Cursor != 1 {
		t.Errorf("cursor after five j = %d, want 1", m.Cursor)
	}
}

func TestHidingArchivedPullsTheCursorBack(t *testing.T) {
	m := newModel()
	m, _ = m.Update(key("a"))
	m.Cursor = 2

	m, _ = m.Update(key("a"))
	if m.Cursor != 1 {
		t.Errorf("cursor = %d, want 1", m.Cursor)
	}
}

func TestKeysEmitIntents(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want tea.Msg
	}{
		{"new", key("n"), msg.Goto{Screen: msg.NewReview}},
		{"resume", named(tea.KeyEnter), msg.Resume{ID: "a"}},
		{"abandon", key("x"), msg.Abandon{ID: "a"}},
		{"refresh one", key("r"), msg.RefreshRecords{ID: "a"}},
		{"refresh all", key("R"), msg.RefreshRecords{}},
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

func TestNoRecordsMeansNoIntent(t *testing.T) {
	m := New(Styles{})
	if _, cmd := m.Update(named(tea.KeyEnter)); cmd != nil {
		t.Errorf("enter with no records produced %#v", cmd())
	}
}

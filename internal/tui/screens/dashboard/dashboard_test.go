package dashboard

import (
	"testing"
	"time"
	"unicode/utf8"

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

func TestAgeReadsTheInjectedClock(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	m := New(Styles{})
	m.Now = func() time.Time { return now }

	tests := []struct {
		name    string
		started time.Time
		want    string
	}{
		{name: "never started", started: time.Time{}, want: "not started"},
		{name: "under a minute", started: now.Add(-30 * time.Second), want: "just now"},
		{name: "minutes", started: now.Add(-5 * time.Minute), want: "5m ago"},
		{name: "an hour", started: now.Add(-time.Hour), want: "1h ago"},
		{name: "hours", started: now.Add(-3 * time.Hour), want: "3h ago"},
		{name: "a day", started: now.Add(-24 * time.Hour), want: "1d ago"},
		{name: "days", started: now.Add(-49 * time.Hour), want: "2d ago"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := m.age(review.Record{StartedAt: tt.started}); got != tt.want {
				t.Errorf("age = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestASubmittedRecordIsVisibleAndSelectable(t *testing.T) {
	m := New(Styles{})
	m.Records = []review.Record{
		{ID: "s", Ref: pr.Ref{Org: "o", Repo: "r", Number: 4}, State: review.StateSubmitted, StartedAt: time.Now()},
	}

	if len(m.rows()) != 1 {
		t.Fatalf("rows = %d, want the submitted record listed: its archive did not finish and only the user can retry it", len(m.rows()))
	}
	if _, ok := m.Selected(); !ok {
		t.Error("a submitted record cannot be selected, so no key reaches it")
	}
	if m.View() == "" {
		t.Error("the dashboard renders nothing while holding a submitted record")
	}
}

func TestTruncateCutsOnRuneBoundaries(t *testing.T) {
	got := truncate("Fix the café résumé parser ✨ and the widgets", 22)
	if !utf8.ValidString(got) {
		t.Errorf("truncate returned invalid UTF-8: %q", got)
	}
	if r := []rune(got); len(r) > 22 {
		t.Errorf("truncate returned %d runes, want at most 22", len(r))
	}
}

func TestTheCursorFollowsTheRecordWhenDetectionReordersRows(t *testing.T) {
	three := func() []review.Record {
		return []review.Record{
			{ID: "a", Ref: pr.Ref{Org: "o", Repo: "r", Number: 1}, State: review.StateReviewing},
			{ID: "b", Ref: pr.Ref{Org: "o", Repo: "r", Number: 2}, State: review.StateReviewing},
			{ID: "d", Ref: pr.Ref{Org: "o", Repo: "r", Number: 4}, State: review.StateReviewing},
		}
	}
	m := New(Styles{})
	m.Records = three()
	m.Cursor = 1
	selected, ok := m.Selected()
	if !ok {
		t.Fatal("nothing selected to start from")
	}
	if selected.ID != "b" {
		t.Fatalf("selected %q, want b", selected.ID)
	}

	// Detection archives the row above the cursor, so every row below it shifts up
	// and a clamped cursor lands on the record that took this one's place.
	moved := three()
	moved[0].State = review.StateArchived
	m = m.SetRecords(moved)

	got, ok := m.Selected()
	if !ok {
		t.Fatal("nothing is selected after the list was replaced")
	}
	if got.ID != selected.ID {
		t.Errorf("cursor is on record %q, want it still on %q: x abandons the selected record with no confirmation", got.ID, selected.ID)
	}
}

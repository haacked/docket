package requests

import (
	"slices"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/pr"
	core "github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

var (
	space = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
)

func row(number int, state review.State) core.Row {
	ref := pr.Ref{Org: "o", Repo: "r", Number: number}
	return core.Row{PR: core.PR{Ref: ref, Title: ref.String()}, State: state}
}

// newModel draws "me" with #1 and #2, then a team with #3. #2 has an open record.
func newModel() Model {
	return New(Styles{}, "claude").SetSections([]core.Section{
		{Rows: []core.Row{row(1, ""), row(2, review.StateReviewing)}},
		{Team: "o/team", Rows: []core.Row{row(3, "")}},
	})
}

func sent(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	return cmd()
}

func TestSpaceMarksTheRowUnderTheCursor(t *testing.T) {
	m, _ := newModel().Update(space)

	if !m.Marked[row(1, "").Ref.URL()] || len(m.Marked) != 1 {
		t.Errorf("marked = %v, want only #1", m.Marked)
	}
}

func TestSpaceAgainUnmarks(t *testing.T) {
	m, _ := newModel().Update(space)
	m, _ = m.Update(space)

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want none", m.Marked)
	}
}

// A batch skips a pull request docket already has an open record for, so the
// row must not look as if it would start.
func TestSpaceIsRefusedOnARowWithARecord(t *testing.T) {
	m, _ := newModel().Update(key("j"))

	m, _ = m.Update(space)

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want the row with a record refused", m.Marked)
	}
}

func TestEnterWithMarksStartsABatchOfOnlyTheMarked(t *testing.T) {
	m, _ := newModel().Update(key("G"))
	m, _ = m.Update(space)
	m, _ = m.Update(key("g"))

	_, cmd := m.Update(enter)

	got, ok := sent(t, cmd).(msg.StartBatch)
	if !ok {
		t.Fatalf("enter sent %#v, want StartBatch", cmd())
	}
	if want := []string{row(3, "").Ref.URL()}; !slices.Equal(got.URLs, want) {
		t.Errorf("URLs = %v, want %v", got.URLs, want)
	}
}

// Nothing marked keeps the single-review path, with its choice of engine and of
// running on the terminal.
func TestEnterWithNothingMarkedPrefillsTheNewReviewScreen(t *testing.T) {
	_, cmd := newModel().Update(enter)

	if got, want := sent(t, cmd), (msg.PrefillReview{URL: row(1, "").Ref.URL()}); got != want {
		t.Errorf("enter sent %#v, want %#v", got, want)
	}
}

// The cursor moves over pull requests only. Stepping off the last row of one
// section lands on the first row of the next, never on the team's heading.
func TestCursorSkipsSectionHeaders(t *testing.T) {
	m, _ := newModel().Update(key("j"))
	m, _ = m.Update(key("j"))

	selected, ok := m.Selected()
	if !ok || selected.Ref.Number != 3 {
		t.Errorf("selected = %v (ok=%v), want the team's #3", selected.Ref, ok)
	}
}

func TestKeysEmitIntents(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want tea.Msg
	}{
		{"refresh", key("r"), msg.RefreshRequests{}},
		{"back", tea.KeyPressMsg{Code: tea.KeyEscape}, msg.Goto{Screen: msg.Dashboard}},
		{"help", key("?"), msg.OpenHelp{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cmd := newModel().Update(tc.key)
			if got := sent(t, cmd); got != tc.want {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestEnterOnAnEmptyListSendsNothing(t *testing.T) {
	m := New(Styles{}, "claude").SetSections([]core.Section{{}})

	if _, cmd := m.Update(enter); cmd != nil {
		t.Errorf("enter on an empty list sent %#v", cmd())
	}
}

func TestSpaceMarksNothingWhenNoEngineHasABackgroundMode(t *testing.T) {
	m := newModel()
	m.Engine = ""

	m, _ = m.Update(space)

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want none: no engine could run the batch", m.Marked)
	}
}

func TestEnterSendsTheBatchWithTheScreensEngine(t *testing.T) {
	m, _ := newModel().Update(space)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})

	got, ok := sent(t, cmd).(msg.StartBatch)
	if !ok || got.Engine != "claude" {
		t.Errorf("sent %#v, want a batch under claude", got)
	}
}

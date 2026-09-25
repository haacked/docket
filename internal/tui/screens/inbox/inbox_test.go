package inbox

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

var (
	space = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
)

func row(number int, state review.State) requests.Row {
	ref := pr.Ref{Org: "o", Repo: "r", Number: number}
	r := requests.Row{PR: requests.PR{Ref: ref, Title: ref.String()}, State: state}
	if state != "" {
		r.RecordID = fmt.Sprintf("rec-%d", number)
	}
	return r
}

// newModel draws "me" with #1 and #2, then a team with #3. #2 has an open record.
func newModel() Model {
	return New(Styles{}, "claude").SetSections([]requests.Section{
		{Rows: []requests.Row{row(1, ""), row(2, review.StateReviewing)}},
		{Team: "o/team", Rows: []requests.Row{row(3, "")}},
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

func TestABatchListsTheMarkedRowsTopToBottom(t *testing.T) {
	m, _ := newModel().Update(key("G"))
	m, _ = m.Update(space)
	m, _ = m.Update(key("g"))
	m, _ = m.Update(space)

	_, cmd := m.Update(enter)

	got, ok := sent(t, cmd).(msg.StartBatch)
	if !ok {
		t.Fatalf("enter sent %#v, want StartBatch", cmd())
	}
	if want := []string{row(1, "").Ref.URL(), row(3, "").Ref.URL()}; !slices.Equal(got.URLs, want) {
		t.Errorf("URLs = %v, want %v", got.URLs, want)
	}
}

// A reload can reorder the rows while the user is on the screen, and enter must
// still act on the pull request the user selected.
func TestTheCursorFollowsItsPullRequestWhenTheRowsAreReplaced(t *testing.T) {
	m, _ := newModel().Update(key("G"))

	m = m.SetSections([]requests.Section{
		{Rows: []requests.Row{row(3, ""), row(1, "")}},
	})

	if selected, ok := m.Selected(); !ok || selected.Ref.Number != 3 {
		t.Errorf("selected = %v (ok=%v), want #3", selected.Ref, ok)
	}
}

func TestReplacingTheRowsDropsMarksThatCanNoLongerStart(t *testing.T) {
	m, _ := newModel().Update(space)
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)

	m = m.SetSections([]requests.Section{
		{Rows: []requests.Row{row(1, review.StateReviewing), row(2, review.StateReviewing)}},
	})

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want #1 dropped for its record and #3 for leaving the list", m.Marked)
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

// Prepare refuses a pull request docket already has open, so enter on that row
// opens the review in progress, the way enter does on the dashboard.
func TestEnterOnARowWithARecordResumesIt(t *testing.T) {
	m, _ := newModel().Update(key("j"))

	_, cmd := m.Update(enter)

	if got, want := sent(t, cmd), (msg.Resume{ID: "rec-2"}); got != want {
		t.Errorf("enter sent %#v, want %#v", got, want)
	}
}

func TestEnterOnAnEmptyListSendsNothing(t *testing.T) {
	m := New(Styles{}, "claude").SetSections([]requests.Section{{}})

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

func TestTheViewMarksRowsAndCountsTheMarks(t *testing.T) {
	m, _ := newModel().Update(space)

	view := m.View()

	for _, want := range []string{"1 marked", "[x] o/r#1", "    o/r#2", "[ ] o/r#3"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view does not show %q:\n%s", want, view)
		}
	}
}

// A team GitHub cannot resolve keeps its heading, so the user can see which
// entry in the config to fix.
func TestTheViewShowsATeamsSearchError(t *testing.T) {
	m := New(Styles{}, "claude").SetSections([]requests.Section{
		{Rows: []requests.Row{row(1, "")}},
		{Team: "o/typo", Err: errors.New("search team-review-requested:o/typo: HTTP 422")},
	})

	view := m.View()

	if !strings.Contains(view, "o/typo") || !strings.Contains(view, "HTTP 422") {
		t.Errorf("the view hides the failed team:\n%s", view)
	}
	if !strings.Contains(view, "o/r#1") {
		t.Errorf("the view dropped the search that succeeded:\n%s", view)
	}
}

func TestTheViewKeepsTheCursorLineVisibleInAShortPane(t *testing.T) {
	m, _ := newModel().Update(key("G"))
	m.Height = 3

	view := m.View()

	if !strings.Contains(view, "> [ ] o/r#3") {
		t.Errorf("the selected row scrolled out of view:\n%s", view)
	}
	if strings.Contains(view, "o/r#1") {
		t.Errorf("a pane two rows tall still drew the first section:\n%s", view)
	}
}

func TestWindow(t *testing.T) {
	lines := []string{"0", "1", "2", "3", "4", "5"}
	tests := []struct {
		name           string
		cursor, height int
		want           []string
	}{
		{"everything fits", 2, 10, lines},
		{"cursor at the top", 0, 3, []string{"0", "1", "2"}},
		{"cursor in the middle", 3, 3, []string{"2", "3", "4"}},
		{"cursor at the bottom", 5, 3, []string{"3", "4", "5"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := window(lines, tc.cursor, tc.height); !slices.Equal(got, tc.want) {
				t.Errorf("window = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestARowWithNoReviewPostedSaysSo(t *testing.T) {
	m := New(Styles{}, "claude").SetSections([]requests.Section{
		{Rows: []requests.Row{row(1, review.StateUnreviewed)}},
	})

	view := m.View()
	if !strings.Contains(view, "no review posted") || strings.Contains(view, "unreviewed") {
		t.Errorf("the row does not say the session posted nothing:\n%s", view)
	}
}

var notesWritten = time.Date(2026, 9, 18, 15, 30, 0, 0, time.UTC)

// asking is the screen after the root found that #1, one of two marked pull
// requests, already has a review of yours.
func asking() Model {
	m, _ := newModel().Update(space)
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)
	m, _ = m.Update(enter)
	return m.SetExisting([]Existing{{Ref: "o/r#1", Found: review.Found{NotesAt: notesWritten, Submitted: true}}}, 1)
}

// The root reads GitHub for every marked pull request before it answers. A
// second enter in that time would check and start the same batch twice.
func TestASecondEnterWhileTheBatchIsCheckedSendsNothing(t *testing.T) {
	m, _ := newModel().Update(space)
	m, _ = m.Update(enter)

	if _, cmd := m.Update(enter); cmd != nil {
		t.Errorf("the second enter sent %#v", cmd())
	}
}

func TestTheChoiceSaysWhatReviewEachPullRequestHas(t *testing.T) {
	view := asking().View()

	for _, want := range []string{"o/r#1", "already has a review of yours", "notes from 2026-09-18", "submitted review on GitHub", "append", "overwrite", "skip"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view does not show %q:\n%s", want, view)
		}
	}
}

func TestChoiceKeysAnswerTheBatch(t *testing.T) {
	tests := []struct {
		key  string
		want string
	}{
		{"a", string(review.IntentAppend)},
		{"o", string(review.IntentOverwrite)},
		{"s", ""},
	}
	for _, tc := range tests {
		t.Run(tc.key, func(t *testing.T) {
			_, cmd := asking().Update(key(tc.key))

			if got, want := sent(t, cmd), (msg.AnswerBatch{Intent: tc.want}); got != want {
				t.Errorf("%s sent %#v, want %#v", tc.key, got, want)
			}
		})
	}
}

// Skipping the pull requests that have a review starts the others. With no
// others, a skip would start nothing, which is what esc already does.
func TestSkipIsNotOfferedWhenNothingElseWouldStart(t *testing.T) {
	m := asking().SetExisting([]Existing{{Ref: "o/r#1", Found: review.Found{NotesAt: notesWritten}}}, 0)

	if _, cmd := m.Update(key("s")); cmd != nil {
		t.Errorf("s sent %#v with nothing else to start", cmd())
	}
	if strings.Contains(m.View(), "skip") {
		t.Errorf("the view offers a skip that starts nothing:\n%s", m.View())
	}
}

// The root starts the batch when the answer arrives. A second key before then
// would answer the same batch again.
func TestASecondAnswerSendsNothing(t *testing.T) {
	m, _ := asking().Update(key("a"))

	if _, cmd := m.Update(key("o")); cmd != nil {
		t.Errorf("the second answer sent %#v", cmd())
	}
}

// esc goes back to the list with the marks as they were, so the user can change
// them and press enter again.
func TestEscFromTheChoiceKeepsTheMarks(t *testing.T) {
	m, cmd := asking().Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if cmd != nil {
		t.Errorf("esc sent %#v", cmd())
	}
	if m.Asking() {
		t.Errorf("the choice is still on screen: %+v", m.Existing)
	}
	if len(m.Marked) != 2 {
		t.Errorf("marked = %v, want both marks kept", m.Marked)
	}
	if _, cmd := m.Update(enter); cmd == nil {
		t.Error("enter after esc did not start the batch again")
	}
}

func TestListKeysDoNothingDuringTheChoice(t *testing.T) {
	before := asking()
	for _, k := range []tea.KeyPressMsg{space, enter, key("r"), key("j")} {
		m, cmd := before.Update(k)
		if cmd != nil {
			t.Errorf("%q sent %#v during the choice", k.String(), cmd())
		}
		if len(m.Marked) != 2 || m.Cursor != before.Cursor {
			t.Errorf("%q changed the list during the choice", k.String())
		}
	}
}

func TestTheFirstSearchCarriesTheSpinnersFrame(t *testing.T) {
	m := New(Styles{}, "claude")
	m.Loading = true
	m.Frame = "⠙"

	if view := m.View(); !strings.Contains(view, "⠙ Searching GitHub for review requests…") {
		t.Errorf("the view does not put the frame before the search:\n%s", view)
	}
}

// A refresh keeps the rows that are already on screen. The header is therefore
// the only place that says a search is running.
func TestARefreshCarriesTheSpinnersFrameInTheHeader(t *testing.T) {
	m := newModel()
	m.Loading = true
	m.Frame = "⠙"

	header, _, _ := strings.Cut(m.View(), "\n")

	if !strings.Contains(header, "⠙ refreshing…") {
		t.Errorf("header = %q, want the frame before the refresh", header)
	}
}

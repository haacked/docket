package inbox

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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

// teamRow is a row the team o/team asked for.
func teamRow(number int) requests.Row {
	r := row(number, "")
	r.Team = "o/team"
	return r
}

// list builds what requests.Group returns after a search of the team o/team.
func list(yours, needs, covered []requests.Row) requests.List {
	return requests.List{
		Sections: []requests.Section{
			{Kind: requests.Yours, Rows: yours},
			{Kind: requests.NeedsReviewer, Rows: needs},
			{Kind: requests.Covered, Rows: covered},
		},
		Teams: 1,
	}
}

func rows(r ...requests.Row) []requests.Row { return r }

// newModel draws #1 asked of me and #2, which has an open record, under Yours.
// It then draws the team's #3, which needs a reviewer.
func newModel() Model {
	return New(Styles{}, "claude").SetList(list(rows(row(1, ""), row(2, review.StateReviewing)), rows(teamRow(3)), nil))
}

// startMarked presses enter and answers no if the screen asks whether to
// assign you.
func startMarked(m Model) (Model, tea.Cmd) {
	m, cmd := m.Update(enter)
	if m.Unassigned > 0 {
		return m.Update(key("n"))
	}
	return m, cmd
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

	_, cmd := startMarked(m)

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

	_, cmd := startMarked(m)

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

	m = m.SetList(list(rows(teamRow(3), row(1, "")), nil, nil))

	if selected, ok := m.Selected(); !ok || selected.Ref.Number != 3 {
		t.Errorf("selected = %v (ok=%v), want #3", selected.Ref, ok)
	}
}

// grouped draws #1 under Yours, then the teammate's #2 and the outsider's #3,
// which both need a reviewer, then #4, which a teammate covers.
func grouped() Model {
	mate := teamRow(2)
	mate.Teammate = true
	return New(Styles{}, "claude").SetList(list(rows(row(1, "")), rows(mate, teamRow(3)), rows(teamRow(4))))
}

func TestTheViewHeadsEachSectionAndSplitsTheOnesThatNeedAReviewerByTeam(t *testing.T) {
	view := grouped().View()

	want := []string{"Yours (1)", "o/r#1", "Needs a reviewer (2)", "Your team (1)", "o/r#2", "Everyone else (1)", "o/r#3", "Covered by a teammate (1) · hidden"}
	at := 0
	for _, w := range want {
		i := strings.Index(view[at:], w)
		if i < 0 {
			t.Fatalf("the view does not show %q after the text before it:\n%s", w, view)
		}
		at += i + len(w)
	}
}

// A group of the pull requests that need a reviewer heads itself only when it
// has rows.
func TestTheViewDrawsNoHeadingForAnEmptyTeamGroup(t *testing.T) {
	view := New(Styles{}, "claude").SetList(list(nil, rows(teamRow(3)), nil)).View()

	if strings.Contains(view, "Your team") || !strings.Contains(view, "Everyone else (1)") {
		t.Errorf("want only the group with rows:\n%s", view)
	}
}

// With no team configured, only a request that names you can list, so the two
// sections about a team's requests have nothing to say.
func TestTheViewDrawsOnlyYoursWhenNoTeamWasSearched(t *testing.T) {
	l := list(rows(row(1, "")), nil, nil)
	l.Teams = 0

	view := New(Styles{}, "claude").SetList(l).View()

	if strings.Contains(view, "Needs a reviewer") || strings.Contains(view, "Covered") {
		t.Errorf("the view draws a team's sections with no team searched:\n%s", view)
	}
}

func TestTheViewLeavesOutAnEmptyCoveredSection(t *testing.T) {
	view := newModel().View()

	if strings.Contains(view, "Covered") {
		t.Errorf("the view draws an empty covered section:\n%s", view)
	}
	if !strings.Contains(view, "Needs a reviewer (1)") {
		t.Errorf("the view does not draw the pull requests that need a reviewer:\n%s", view)
	}
}

func TestTheViewSaysNoneUnderAnEmptySection(t *testing.T) {
	view := New(Styles{}, "claude").SetList(list(rows(row(1, "")), nil, nil)).View()

	if !strings.Contains(view, "Needs a reviewer (0)\n  none") {
		t.Errorf("the view does not say no pull request needs a reviewer:\n%s", view)
	}
}

// The covered pull requests are hidden until c, because a teammate has taken
// them.
func TestCoveredPullRequestsAreHiddenUntilC(t *testing.T) {
	m, _ := grouped().Update(key("G"))

	if strings.Contains(m.View(), "o/r#4") {
		t.Errorf("the view shows the covered #4 before c:\n%s", m.View())
	}
	if selected, _ := m.Selected(); selected.Ref.Number != 3 {
		t.Errorf("selected = %v, want the last row shown, #3", selected.Ref)
	}

	m, _ = m.Update(key("c"))
	m, _ = m.Update(key("G"))

	view := m.View()
	if !strings.Contains(view, "Covered by a teammate (1)\n") || !strings.Contains(view, "o/r#4") {
		t.Errorf("the view does not show the covered #4 after c:\n%s", view)
	}
	if selected, _ := m.Selected(); selected.Ref.Number != 4 {
		t.Errorf("selected = %v, want #4", selected.Ref)
	}
}

// Hiding the covered rows drops their marks, because a batch would otherwise
// start a row the user cannot see.
func TestHidingTheCoveredRowsDropsTheirMarks(t *testing.T) {
	m, _ := grouped().Update(key("c"))
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)

	m, _ = m.Update(key("c"))

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want the hidden row's mark dropped", m.Marked)
	}
}

func TestShownCoveredRowsStayShownWhenTheRowsAreReplaced(t *testing.T) {
	m, _ := grouped().Update(key("c"))

	m = m.SetList(grouped().all)

	if !strings.Contains(m.View(), "o/r#4") {
		t.Errorf("the covered row is hidden again after a reload:\n%s", m.View())
	}
}

// A reload can move a row to another section. Its mark goes with it.
func TestAMarkFollowsItsPullRequestWhenTheRowsAreReplaced(t *testing.T) {
	m, _ := newModel().Update(key("G"))
	m, _ = m.Update(space)

	m = m.SetList(list(rows(row(1, ""), teamRow(3)), nil, nil))

	if !m.Marked[row(3, "").Ref.URL()] {
		t.Errorf("marked = %v, want #3 kept", m.Marked)
	}
	if selected, ok := m.Selected(); !ok || selected.Ref.Number != 3 {
		t.Errorf("selected = %v (ok=%v), want #3", selected.Ref, ok)
	}
}

// withDraft draws the draft #1 and the ready #2 under Yours, and the draft #3
// under the pull requests that need a reviewer.
func withDraft() Model {
	draft := func(r requests.Row) requests.Row {
		r.IsDraft = true
		return r
	}
	return New(Styles{Dim: lipgloss.NewStyle().Faint(true)}, "claude").SetList(list(rows(draft(row(1, "")), row(2, "")), rows(draft(teamRow(3))), nil))
}

// The list leaves out a draft until the user presses d, because a draft is not
// ready for review.
func TestDraftsAreHiddenUntilD(t *testing.T) {
	m := withDraft()

	view := ansi.Strip(m.View())
	for _, hidden := range []string{"o/r#1", "o/r#3"} {
		if strings.Contains(view, hidden) {
			t.Errorf("the view shows %q while drafts are hidden:\n%s", hidden, view)
		}
	}
	for _, heading := range []string{"Yours (1) · 1 draft hidden", "Needs a reviewer (0) · 1 draft hidden"} {
		if !strings.Contains(view, heading) {
			t.Errorf("the view does not head a section %q:\n%s", heading, view)
		}
	}
	if strings.Contains(view, "none") {
		t.Errorf("the view says a section with a hidden draft has none:\n%s", view)
	}
	if selected, ok := m.Selected(); !ok || selected.Ref.Number != 2 {
		t.Errorf("selected = %v (ok=%v), want #2", selected.Ref, ok)
	}

	m, _ = m.Update(key("d"))

	view = ansi.Strip(m.View())
	for _, shown := range []string{"o/r#1", "o/r#3"} {
		if !strings.Contains(view, shown) {
			t.Errorf("the view does not show %q after d:\n%s", shown, view)
		}
	}
	if strings.Contains(view, "hidden") {
		t.Errorf("the view still says drafts are hidden:\n%s", view)
	}
}

// The screen draws a shown draft faint, so it reads as not ready.
func TestAShownDraftIsDrawnFaint(t *testing.T) {
	m, _ := withDraft().Update(key("d"))
	m, _ = m.Update(key("G"))

	for _, line := range strings.Split(m.View(), "\n") {
		faint := strings.HasPrefix(line, "\x1b[2m")
		switch {
		case strings.Contains(line, "o/r#1") && !faint:
			t.Errorf("the draft #1 is not faint: %q", line)
		case strings.Contains(line, "o/r#2") && faint:
			t.Errorf("the ready #2 is faint: %q", line)
		}
	}
}

// The cursor's style does not undo the faint that marks a draft.
func TestTheSelectedDraftIsDrawnFaint(t *testing.T) {
	m := withDraft()
	m.Styles.Selected = lipgloss.NewStyle().Bold(true)
	m, _ = m.Update(key("d"))
	m, _ = m.Update(key("g"))

	for _, line := range strings.Split(m.View(), "\n") {
		if strings.Contains(line, "> ") && strings.Contains(line, "o/r#1") && !strings.Contains(line, "\x1b[1;2m") && !strings.Contains(line, "\x1b[2;1m") {
			t.Errorf("the selected draft is not faint: %q", line)
		}
	}
}

// The user can still mark a shown draft. Hiding it again drops its mark, because
// a batch would otherwise start a row the user cannot see.
func TestADraftCanBeMarkedWhileShownAndHidingItDropsTheMark(t *testing.T) {
	m, _ := withDraft().Update(key("d"))
	m, _ = m.Update(key("g"))
	m, _ = m.Update(space)
	if !m.Marked[row(1, "").Ref.URL()] {
		t.Fatalf("marked = %v, want the draft #1", m.Marked)
	}

	m, _ = m.Update(key("d"))

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want the hidden draft's mark dropped", m.Marked)
	}
}

// The root replaces the rows after every reload. The choice to show drafts
// outlasts the reload.
func TestShownDraftsStayShownWhenTheRowsAreReplaced(t *testing.T) {
	m, _ := withDraft().Update(key("d"))

	m = m.SetList(withDraft().all)

	if !strings.Contains(m.View(), "o/r#1") {
		t.Errorf("the draft is hidden again after a reload:\n%s", m.View())
	}
}

func TestReplacingTheRowsDropsMarksThatCanNoLongerStart(t *testing.T) {
	m, _ := newModel().Update(space)
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)

	m = m.SetList(list(rows(row(1, review.StateReviewing), row(2, review.StateReviewing)), nil, nil))

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
// section lands on the first row of the next, never on its heading.
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
	m := New(Styles{}, "claude").SetList(list(nil, nil, nil))

	if _, cmd := m.Update(enter); cmd != nil {
		t.Errorf("enter on an empty list sent %#v", cmd())
	}
}

// o lets the user read a pull request before they decide whether to review it.
func TestOOpensTheSelectedPullRequest(t *testing.T) {
	_, cmd := newModel().Update(key("o"))

	if got, want := sent(t, cmd), (msg.OpenPullRequest{URL: row(1, "").Ref.URL()}); got != want {
		t.Errorf("o sent %#v, want %#v", got, want)
	}
}

// A row with an open record opens its review, the way o does on the dashboard.
func TestOOnARowWithARecordOpensItsReview(t *testing.T) {
	m, _ := newModel().Update(key("j"))

	_, cmd := m.Update(key("o"))

	if got, want := sent(t, cmd), (msg.OpenOnGitHub{ID: "rec-2"}); got != want {
		t.Errorf("o sent %#v, want %#v", got, want)
	}
}

func TestOOnAnEmptyListSendsNothing(t *testing.T) {
	m := New(Styles{}, "claude").SetList(list(nil, nil, nil))

	if _, cmd := m.Update(key("o")); cmd != nil {
		t.Errorf("o on an empty list sent %#v", cmd())
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

	_, cmd := startMarked(m)

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

// A team whose rows docket could not check shows the reason above the rows, so
// the user knows the list may hold pull requests they already reviewed.
func TestTheViewShowsATeamsErrorAboveTheRows(t *testing.T) {
	l := list(nil, rows(teamRow(1)), nil)
	l.Errs = []error{errors.New("o/team: could not tell which of these you already reviewed")}
	m := New(Styles{}, "claude").SetList(l)

	view := m.View()

	reason, pr := strings.Index(view, "already reviewed"), strings.Index(view, "o/r#1")
	if reason < 0 || pr < reason {
		t.Errorf("want the reason and then the row:\n%s", view)
	}
}

// A team GitHub cannot resolve names itself, so the user can see which entry in
// the config to fix.
func TestTheViewShowsATeamsSearchError(t *testing.T) {
	l := list(rows(row(1, "")), nil, nil)
	l.Errs = []error{errors.New("o/typo: search team-review-requested:o/typo: HTTP 422")}
	m := New(Styles{}, "claude").SetList(l)

	view := m.View()

	if !strings.Contains(view, "o/typo") || !strings.Contains(view, "HTTP 422") {
		t.Errorf("the view hides the failed team:\n%s", view)
	}
	if !strings.Contains(view, "o/r#1") {
		t.Errorf("the view dropped the search that succeeded:\n%s", view)
	}
}

func TestEachRowSaysWhatPutItInItsSection(t *testing.T) {
	asked := row(1, "")
	assigned := teamRow(2)
	assigned.AssignedMe = true
	recorded := teamRow(3)
	recorded.State, recorded.RecordID = review.StateDrafted, "rec-3"
	outsider := teamRow(4)
	outsider.Reviewer = "ioannisj"
	nobody := teamRow(5)
	covered := teamRow(6)
	covered.Reviewer = "gustavohstrassburger"
	m, _ := New(Styles{}, "claude").SetList(list(rows(asked, assigned, recorded), rows(outsider, nobody), rows(covered))).Update(key("c"))

	lines := strings.Split(m.View(), "\n")
	line := func(n int) string {
		for _, l := range lines {
			if strings.Contains(l, fmt.Sprintf("o/r#%d ", n)) {
				return l
			}
		}
		t.Fatalf("no line for #%d:\n%s", n, strings.Join(lines, "\n"))
		return ""
	}
	for n, want := range map[int]string{
		1: "asked you",
		2: "assigned to you",
		3: "drafted",
		4: "ioannisj assigned, not on the team",
		6: "assigned to gustavohstrassburger",
	} {
		if got := line(n); !strings.HasSuffix(got, want) {
			t.Errorf("#%d = %q, want it to end with %q", n, got, want)
		}
	}
	if got := line(5); strings.Contains(got, "assigned") {
		t.Errorf("#5 = %q, want no assignee named", got)
	}
}

// PostHog's bot opens the pull request, so the person who started it is who
// owns it.
func TestABotsRowNamesThePersonWhoStartedIt(t *testing.T) {
	r := teamRow(1)
	r.Author, r.Bot, r.StartedBy = "posthog[bot]", true, "pauldambra"

	view := New(Styles{}, "claude").SetList(list(nil, rows(r), nil)).View()

	if !strings.Contains(view, "posthog[bot] for pauldambra") {
		t.Errorf("the row does not name who started it:\n%s", view)
	}
}

// With one team every row is that team's. With two, each row says which team
// asked.
func TestARowNamesItsTeamOnlyWhenSeveralWereSearched(t *testing.T) {
	line := func(l requests.List) string {
		for _, line := range strings.Split(New(Styles{}, "claude").SetList(l).View(), "\n") {
			if strings.Contains(line, "o/r#1 ") {
				return line
			}
		}
		t.Fatal("no line for #1")
		return ""
	}
	l := list(nil, rows(teamRow(1)), nil)
	if got := line(l); strings.HasSuffix(got, "· team") {
		t.Errorf("a single team's row names its team: %q", got)
	}

	l.Teams = 2

	if got := line(l); !strings.HasSuffix(got, "· team") {
		t.Errorf("the row does not name its team: %q", got)
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

// A tall status can leave the list a single line. The list still follows the
// cursor there rather than drawing every row past the bottom of the terminal.
func TestAOneLinePaneStillShowsOnlyTheCursorsRow(t *testing.T) {
	m, _ := newModel().Update(key("G"))
	m.Height = 1

	view := m.View()

	if !strings.Contains(view, "> [ ] o/r#3") {
		t.Errorf("the selected row scrolled out of view:\n%s", view)
	}
	if strings.Contains(view, "o/r#1") {
		t.Errorf("a pane one line tall drew other rows:\n%s", view)
	}
	if strings.Contains(view, "\n") {
		t.Errorf("a pane one line tall drew more than one line:\n%s", view)
	}
}

func TestARowWithNoReviewPostedSaysSo(t *testing.T) {
	m := New(Styles{}, "claude").SetList(list(rows(row(1, review.StateUnreviewed)), nil, nil))

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
	m, _ = startMarked(m)
	return m.SetExisting([]Existing{{Ref: "o/r#1", Found: review.Found{NotesAt: notesWritten, Submitted: true}}}, 1)
}

// The root reads GitHub for every marked pull request before it answers. A
// second enter in that time would check and start the same batch twice.
func TestASecondEnterWhileTheBatchIsCheckedSendsNothing(t *testing.T) {
	m, _ := newModel().Update(space)
	m, _ = startMarked(m)

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
	if _, cmd := startMarked(m); cmd == nil {
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
	m.Spinner.Frame = "⠙"

	if view := m.View(); !strings.Contains(view, "⠙ Searching GitHub for review requests…") {
		t.Errorf("the view does not put the frame before the search:\n%s", view)
	}
}

// A refresh keeps the rows that are already on screen. The header is therefore
// the only place that says a search is running.
func TestARefreshCarriesTheSpinnersFrameInTheHeader(t *testing.T) {
	m := newModel()
	m.Loading = true
	m.Spinner.Frame = "⠙"

	header, _, _ := strings.Cut(m.View(), "\n")

	if !strings.Contains(header, "⠙ refreshing…") {
		t.Errorf("header = %q, want the frame before the refresh", header)
	}
}

// A one-line pane with no search results yet has no row to show.
func TestAOneLinePaneWithNoSectionsDrawsWithoutPanicking(t *testing.T) {
	m := New(Styles{}, "claude")
	m.Height = 1

	_ = m.View()
}

func TestTOpensTheTeamsScreen(t *testing.T) {
	_, cmd := newModel().Update(key("t"))

	if got, want := sent(t, cmd), (msg.OpenTeams{}); got != want {
		t.Errorf("t sent %#v, want %#v", got, want)
	}
}

func TestTSendsNothingDuringTheChoice(t *testing.T) {
	if _, cmd := asking().Update(key("t")); cmd != nil {
		t.Errorf("t sent %#v during the choice", cmd())
	}
}

// confirming is the screen asking whether to assign you to #1 and #3, two of
// the three marked pull requests. You are assigned to the third, #4.
func confirming(t *testing.T) Model {
	t.Helper()
	mine := teamRow(4)
	mine.AssignedMe = true
	m := New(Styles{}, "claude").SetList(list(rows(row(1, ""), mine), rows(teamRow(3)), nil))
	for range 3 {
		m, _ = m.Update(space)
		m, _ = m.Update(key("j"))
	}

	m, cmd := m.Update(enter)

	if cmd != nil {
		t.Fatalf("enter sent %#v before the answer", cmd())
	}
	if !m.Asking() {
		t.Fatal("enter did not ask whether to assign you")
	}
	return m
}

func TestEnterAsksWhetherToAssignYouToTheMarkedPullRequestsYouAreNotAssignedTo(t *testing.T) {
	view := confirming(t).View()

	for _, want := range []string{"Assign yourself on GitHub to 2 of the 3 marked pull requests?", "already assigned to the other 1."} {
		if !strings.Contains(view, want) {
			t.Errorf("the question does not show %q:\n%s", want, view)
		}
	}
}

// GitHub keeps an assignee it is asked to add again, so the batch asks to
// assign you to all three.
func TestYesStartsTheBatchAndAssignsYou(t *testing.T) {
	_, cmd := confirming(t).Update(key("y"))

	got, ok := sent(t, cmd).(msg.StartBatch)
	if !ok || len(got.URLs) != 3 || !got.Assign {
		t.Errorf("y sent %#v, want all three marked and you assigned", cmd())
	}
}

func TestNoStartsTheBatchWithoutAssigningYou(t *testing.T) {
	_, cmd := confirming(t).Update(key("n"))

	got, ok := sent(t, cmd).(msg.StartBatch)
	if !ok || len(got.URLs) != 3 || got.Assign {
		t.Errorf("n sent %#v, want all three marked and nobody assigned", cmd())
	}
}

func TestEscFromTheAssignQuestionKeepsTheMarks(t *testing.T) {
	m, cmd := confirming(t).Update(tea.KeyPressMsg{Code: tea.KeyEscape})

	if cmd != nil {
		t.Errorf("esc sent %#v", cmd())
	}
	if m.Asking() || len(m.Marked) != 3 {
		t.Errorf("asking = %v with marks %v, want the list back with all three marks", m.Asking(), m.Marked)
	}
}

func TestOtherKeysDoNothingDuringTheAssignQuestion(t *testing.T) {
	before := confirming(t)
	for _, k := range []tea.KeyPressMsg{space, enter, key("a"), key("j"), key("c")} {
		m, cmd := before.Update(k)
		if cmd != nil {
			t.Errorf("%q sent %#v during the question", k.String(), cmd())
		}
		if !m.Asking() || len(m.Marked) != 3 || m.Cursor != before.Cursor {
			t.Errorf("%q changed the screen during the question", k.String())
		}
	}
}

// The root answers the batch once it has checked it. A second key before then
// would start the same batch twice.
func TestASecondAnswerToTheAssignQuestionSendsNothing(t *testing.T) {
	m, _ := confirming(t).Update(key("y"))

	if _, cmd := m.Update(key("n")); cmd != nil {
		t.Errorf("the second answer sent %#v", cmd())
	}
	if _, cmd := m.Update(enter); cmd != nil {
		t.Errorf("enter after the answer sent %#v", cmd())
	}
}

func TestEnterAsksNothingWhenYouAreAssignedToEveryMarkedPullRequest(t *testing.T) {
	mine := teamRow(4)
	mine.AssignedMe = true
	m, _ := New(Styles{}, "claude").SetList(list(rows(mine), nil, nil)).Update(space)

	m, cmd := m.Update(enter)

	if m.Asking() {
		t.Error("enter asked whether to assign you to a pull request you are assigned to")
	}
	if got, ok := sent(t, cmd).(msg.StartBatch); !ok || got.Assign {
		t.Errorf("enter sent %#v, want a batch that assigns nobody", cmd())
	}
}

// The index watcher can reload the list while the question is up, and a reload
// drops the mark of a row that now has a record.
func TestAReloadDuringTheAssignQuestionCountsTheMarksThatAreLeft(t *testing.T) {
	m := confirming(t)
	mine := teamRow(4)
	mine.AssignedMe = true

	m = m.SetList(list(rows(row(1, review.StateReviewing), mine), rows(teamRow(3)), nil))

	if m.Unassigned != 1 || !strings.Contains(m.View(), "Assign yourself on GitHub to 1 of the 2 marked pull requests?") {
		t.Errorf("unassigned = %d, want the question to count #3 alone:\n%s", m.Unassigned, m.View())
	}
}

func TestAReloadThatLeavesNothingToAssignClosesTheQuestion(t *testing.T) {
	m := confirming(t)

	m = m.SetList(list(rows(row(1, review.StateReviewing)), rows(row(3, review.StateReviewing)), nil))

	if m.Asking() {
		t.Errorf("the question is still up with no mark left: unassigned = %d", m.Unassigned)
	}
	if _, cmd := m.Update(key("y")); cmd != nil {
		t.Errorf("y sent %#v with nothing marked", cmd())
	}
}

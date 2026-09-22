package notes

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func record() review.Record {
	return review.Record{
		ID:        "rec-1",
		Ref:       pr.Ref{Org: "haacked", Repo: "docket", Number: 7},
		URL:       "https://github.com/haacked/docket/pull/7",
		Title:     "Add a thing",
		State:     review.StateDrafted,
		NotesPath: "/opt/review-code/.reviews/haacked/docket/pr-7.md",
	}
}

const markdown = "# Review of pull 7\n\nThe compaction drops its lock partway through.\n"

// opened is the screen as the root hands it a record: sized first, because the
// pane renders nothing before it knows how much room it has.
func opened(markdown string, missing bool) Model {
	return New(Styles{}).SetSize(100, 20).SetNotes(record(), markdown, missing)
}

func key(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func TestTheNotesAreShown(t *testing.T) {
	view := opened(markdown, false).View()

	if !strings.Contains(view, "compaction") {
		t.Errorf("the view does not show what review-code wrote:\n%s", view)
	}
}

func TestTheViewNamesTheRecordAndWhereTheNotesLive(t *testing.T) {
	view := opened(markdown, false).View()

	for _, want := range []string{"haacked/docket#7", record().NotesPath} {
		if !strings.Contains(view, want) {
			t.Errorf("the view is missing %q:\n%s", want, view)
		}
	}
}

// review-code writes the notes during the session, so a record that has not
// reached one yet has none. That is not a failure, and the path is what tells
// the user where they will appear.
func TestNotesThatDoNotExistYetAreNotAnError(t *testing.T) {
	view := opened("", true).View()

	if !strings.Contains(strings.ToLower(view), "no notes") {
		t.Errorf("the view does not say there are no notes:\n%s", view)
	}
	if !strings.Contains(view, record().NotesPath) {
		t.Errorf("the view does not say where the notes would be:\n%s", view)
	}
}

func TestEAsksForTheEditor(t *testing.T) {
	_, cmd := opened(markdown, false).Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	if cmd == nil {
		t.Fatal("e produced no command")
	}

	if got, want := cmd(), (msg.EditNotes{ID: "rec-1"}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestQuestionMarkAsksForHelp(t *testing.T) {
	_, cmd := opened(markdown, false).Update(tea.KeyPressMsg{Code: '?', Text: "?"})
	if cmd == nil {
		t.Fatal("? produced no command")
	}

	if got, want := cmd(), (msg.OpenHelp{}); got != want {
		t.Errorf("got %#v, want %#v", got, want)
	}
}

func TestLeavingGoesBackToTheDashboard(t *testing.T) {
	for _, press := range []tea.KeyPressMsg{key(tea.KeyEscape), {Code: 'q', Text: "q"}} {
		_, cmd := opened(markdown, false).Update(press)
		if cmd == nil {
			t.Fatalf("%s produced no command", press)
		}
		if got, want := cmd(), (msg.Goto{Screen: msg.Dashboard}); got != want {
			t.Errorf("%s gave %#v, want %#v", press, got, want)
		}
	}
}

func TestALongReviewScrolls(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "Finding %d in the review.\n\n", i)
	}
	m := New(Styles{}).SetSize(80, 10).SetNotes(record(), b.String(), false)

	before := m.View()
	m, _ = m.Update(key(tea.KeyDown))
	after := m.View()

	if before == after {
		t.Error("the pane does not scroll, so everything past the first screenful is unreachable")
	}
}

// glamour wraps to a fixed width, so a resize that only moved the pane would
// leave the old line breaks behind.
func TestAResizeReRendersTheNotes(t *testing.T) {
	// renderMarkdown falls back to the raw markdown when glamour cannot build a
	// renderer, and a GLAMOUR_STYLE naming a style file this machine does not
	// have does exactly that. Both widths would then return the same string and
	// the test would fail on that developer's box rather than on the code.
	t.Setenv("GLAMOUR_STYLE", "notty")

	long := "The compaction rewrites the log from what Fold produced, and it holds the lock for the whole rewrite rather than dropping it between the read and the replace.\n"
	m := New(Styles{}).SetSize(100, 20).SetNotes(record(), long, false)
	wide := m.View()

	narrow := m.SetSize(40, 20).View()

	if wide == narrow {
		t.Error("the notes were not re-rendered at the new width")
	}
	for name, view := range map[string]string{"wide": wide, "narrow": narrow} {
		if !strings.Contains(view, "compaction") {
			t.Errorf("the %s view lost the notes:\n%s", name, view)
		}
	}
}

// Opening a second record has to leave the pane at the top of its notes.
func TestOpeningAnotherRecordStartsAtTheTop(t *testing.T) {
	var b strings.Builder
	for i := range 200 {
		fmt.Fprintf(&b, "Finding %d in the review.\n\n", i)
	}
	m := New(Styles{}).SetSize(80, 10).SetNotes(record(), b.String(), false)
	top := m.View()

	m, _ = m.Update(key(tea.KeyDown))
	m = m.SetNotes(record(), b.String(), false)

	if m.View() != top {
		t.Error("the pane kept the last record's scroll position")
	}
}

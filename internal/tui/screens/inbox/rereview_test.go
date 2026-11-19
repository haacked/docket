package inbox

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

var drawnAt = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

// candidateRow is a re-review candidate by alice that was updated 5 hours ago
// and that the user reviewed 3 days ago.
func candidateRow(number int, changes requests.Changes) requests.Row {
	r := row(number, "")
	r.Author = "alice"
	r.UpdatedAt = drawnAt.Add(-5 * time.Hour)
	changes.ReviewedAt = drawnAt.Add(-72 * time.Hour)
	r.Changes = changes
	return r
}

var twoCommits = requests.Changes{Commits: 2}

// withCandidates is what requests.Group returns after a search that looked for
// re-review candidates.
func withCandidates(teams int, yours, external, member []requests.Row) requests.List {
	return requests.List{
		Sections: []requests.Section{
			{Kind: requests.Yours, Rows: yours},
			{Kind: requests.NeedsReviewer},
			{Kind: requests.Covered},
			{Kind: requests.RereviewExternal, Rows: external},
			{Kind: requests.RereviewMember, Rows: member},
		},
		Teams:    teams,
		Rereview: true,
	}
}

// drawn is the screen wide enough that no row's metadata is cut, with a fixed
// clock.
func drawn(l requests.List) Model {
	m := New(Styles{}, "claude")
	m.Now = func() time.Time { return drawnAt }
	m.Width = 200
	return m.SetList(l)
}

// candidates draws #1 under Yours, #7 by an outside contributor, and #8 by a
// member of the org, with no team configured.
func candidates() Model {
	return drawn(withCandidates(0, rows(row(1, "")), rows(candidateRow(7, twoCommits)), rows(candidateRow(8, twoCommits))))
}

func shown(t *testing.T, m Model) Model {
	t.Helper()
	m, _ = m.Update(key("p"))
	if !m.ShowRereview {
		t.Fatal("p did not show the candidates")
	}
	return m
}

func lineOf(t *testing.T, view string, n int) string {
	t.Helper()
	for _, l := range strings.Split(view, "\n") {
		if strings.Contains(l, fmt.Sprintf("o/r#%d ", n)) {
			return l
		}
	}
	t.Fatalf("no line for #%d:\n%s", n, view)
	return ""
}

// Finding the candidates costs reads the default list does not make, so the
// first p searches GitHub again.
func TestPSearchesAgainWhenTheListHasNoCandidates(t *testing.T) {
	m, cmd := newModel().Update(key("p"))

	if !m.ShowRereview {
		t.Error("p did not turn the candidates on")
	}
	if got, want := sent(t, cmd), (msg.RefreshRequests{}); got != want {
		t.Errorf("p sent %#v, want %#v", got, want)
	}
}

func TestPOnlyRedrawsWhenTheListHasCandidates(t *testing.T) {
	m, cmd := candidates().Update(key("p"))

	if !m.ShowRereview {
		t.Error("p did not turn the candidates on")
	}
	if cmd != nil {
		t.Errorf("p sent %#v, want nothing", cmd())
	}
}

// The root searches once more when the running search answers without the
// candidates, so p sends nothing of its own.
func TestPDuringASearchTurnsTheCandidatesOnAndSendsNothing(t *testing.T) {
	m := newModel()
	m.Loading = true

	m, cmd := m.Update(key("p"))

	if !m.ShowRereview {
		t.Error("p did not turn the candidates on")
	}
	if cmd != nil {
		t.Errorf("p sent %#v, want nothing", cmd())
	}
}

func TestPAgainHidesTheCandidatesAndSendsNothing(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    Model
	}{
		{"with candidates", candidates()},
		{"without candidates", newModel()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.m
			m.ShowRereview = true

			m, cmd := m.Update(key("p"))

			if m.ShowRereview {
				t.Error("p did not turn the candidates off")
			}
			if cmd != nil {
				t.Errorf("p sent %#v, want nothing", cmd())
			}
		})
	}
}

func TestTheCandidateSectionsAreHiddenUntilP(t *testing.T) {
	view := candidates().View()

	for _, hidden := range []string{"Changed since your review", "o/r#7", "o/r#8"} {
		if strings.Contains(view, hidden) {
			t.Errorf("the view shows %q before p:\n%s", hidden, view)
		}
	}
}

// The candidates do not depend on a team, so they draw with none configured.
func TestTheCandidateSectionsDrawInOrderAfterYoursWithNoTeam(t *testing.T) {
	view := shown(t, candidates()).View()

	want := []string{"Yours (1)", "o/r#1", "Changed since your review: outside contributors (1)", "o/r#7", "Changed since your review: org members (1)", "o/r#8"}
	at := 0
	for _, w := range want {
		i := strings.Index(view[at:], w)
		if i < 0 {
			t.Fatalf("the view does not show %q after the text before it:\n%s", w, view)
		}
		at += i + len(w)
	}
	if strings.Contains(view, "Needs a reviewer") || strings.Contains(view, "Covered") {
		t.Errorf("the view draws a team's sections with no team searched:\n%s", view)
	}
}

func TestTheCandidateSectionsDrawAfterTheTeamsSections(t *testing.T) {
	l := withCandidates(1, rows(row(1, "")), rows(candidateRow(7, twoCommits)), nil)
	l.Sections[1].Rows = rows(teamRow(3))

	view := shown(t, drawn(l)).View()

	needs, external := strings.Index(view, "Needs a reviewer (1)"), strings.Index(view, "Changed since your review: outside contributors (1)")
	if needs < 0 || external < needs {
		t.Errorf("want the candidates after the team's sections:\n%s", view)
	}
}

func TestAnEmptyCandidateSectionSaysNone(t *testing.T) {
	m := shown(t, drawn(withCandidates(0, nil, nil, rows(candidateRow(8, twoCommits)))))

	if view := m.View(); !strings.Contains(view, "Changed since your review: outside contributors (0)\n  none") {
		t.Errorf("the view does not say no outside contributor's pull request changed:\n%s", view)
	}
}

// Until the search that looks for them answers, the screen has nothing to say
// about the candidates, not even that there are none.
func TestTheCandidateSectionsDrawNothingBeforeTheCandidatesArrive(t *testing.T) {
	l := withCandidates(0, rows(row(1, "")), nil, nil)
	l.Rereview = false
	m := drawn(l)
	m.Loading = true

	m, _ = m.Update(key("p"))

	if view := m.View(); strings.Contains(view, "Changed since your review") {
		t.Errorf("the view draws the candidate sections before they were looked for:\n%s", view)
	}
}

func TestTheCursorReachesTheCandidatesOnlyWhileTheyShow(t *testing.T) {
	m, _ := candidates().Update(key("G"))
	if selected, _ := m.Selected(); selected.Ref.Number != 1 {
		t.Errorf("selected = %v, want the last row shown, #1", selected.Ref)
	}

	m = shown(t, m)
	m, _ = m.Update(key("G"))

	if selected, _ := m.Selected(); selected.Ref.Number != 8 {
		t.Errorf("selected = %v, want the last candidate, #8", selected.Ref)
	}
}

// Hiding the candidates drops their marks, because a batch would otherwise
// start a row the user cannot see.
func TestAMarkOnACandidateLastsUntilPHidesIt(t *testing.T) {
	m := shown(t, candidates())
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)
	url := row(8, "").Ref.URL()

	m = m.SetList(m.all)
	if !m.Marked[url] {
		t.Fatalf("marked = %v, want #8 kept across a reload", m.Marked)
	}

	m, _ = m.Update(key("p"))

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want the hidden candidate's mark dropped", m.Marked)
	}
}

func TestEnterWithAMarkedCandidateStartsABatchOfIt(t *testing.T) {
	m := shown(t, candidates())
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)

	_, cmd := startMarked(m)

	got, ok := sent(t, cmd).(msg.StartBatch)
	if !ok {
		t.Fatalf("enter sent %#v, want StartBatch", cmd())
	}
	if want := []string{row(8, "").Ref.URL()}; !slices.Equal(got.URLs, want) {
		t.Errorf("URLs = %v, want %v", got.URLs, want)
	}
}

// The assign question counts a candidate like any other row you are not
// assigned to.
func TestEnterWithAMarkedCandidateAsksWhetherToAssignYou(t *testing.T) {
	m := shown(t, candidates())
	m, _ = m.Update(key("G"))
	m, _ = m.Update(space)

	m, _ = m.Update(enter)

	if m.Unassigned != 1 {
		t.Errorf("unassigned = %d, want the candidate counted", m.Unassigned)
	}
}

func TestEnterOnACandidatePrefillsTheNewReviewScreen(t *testing.T) {
	m := shown(t, candidates())
	m, _ = m.Update(key("G"))

	_, cmd := m.Update(enter)

	if got, want := sent(t, cmd), (msg.PrefillReview{URL: row(8, "").Ref.URL()}); got != want {
		t.Errorf("enter sent %#v, want %#v", got, want)
	}
}

func TestOOnACandidateOpensThePullRequest(t *testing.T) {
	m := shown(t, candidates())
	m, _ = m.Update(key("G"))

	_, cmd := m.Update(key("o"))

	if got, want := sent(t, cmd), (msg.OpenPullRequest{URL: row(8, "").Ref.URL()}); got != want {
		t.Errorf("o sent %#v, want %#v", got, want)
	}
}

// recordedCandidate draws #9, a candidate by an outside contributor that docket
// has a drafted record for.
func recordedCandidate(t *testing.T) Model {
	t.Helper()
	r := candidateRow(9, twoCommits)
	r.State, r.RecordID = review.StateDrafted, "rec-9"
	return shown(t, drawn(withCandidates(0, nil, rows(r), nil)))
}

func TestACandidateWithARecordCannotBeMarked(t *testing.T) {
	m, _ := recordedCandidate(t).Update(space)

	if len(m.Marked) != 0 {
		t.Errorf("marked = %v, want the row with a record refused", m.Marked)
	}
}

func TestEnterOnACandidateWithARecordResumesIt(t *testing.T) {
	_, cmd := recordedCandidate(t).Update(enter)

	if got, want := sent(t, cmd), (msg.Resume{ID: "rec-9"}); got != want {
		t.Errorf("enter sent %#v, want %#v", got, want)
	}
}

func TestACandidateWithARecordEndsWithItsState(t *testing.T) {
	line := lineOf(t, recordedCandidate(t).View(), 9)

	if want := "· alice · 2 commits · reviewed 3d ago · drafted"; !strings.HasSuffix(line, want) {
		t.Errorf("#9 = %q, want it to end with %q", line, want)
	}
}

// A candidate row says what landed since the review and when the review was,
// in place of when the pull request was last updated. A zero count is left
// out.
func TestACandidateRowSaysWhatChangedAndWhenYouReviewed(t *testing.T) {
	tests := []struct {
		name    string
		changes requests.Changes
		want    string
	}{
		{"commits only", requests.Changes{Commits: 2}, "2 commits"},
		{"one commit", requests.Changes{Commits: 1}, "1 commit"},
		{"force pushes only", requests.Changes{ForcePushes: 3}, "3 force pushes"},
		{"one force push", requests.Changes{ForcePushes: 1}, "1 force push"},
		{"both", requests.Changes{Commits: 2, ForcePushes: 1}, "2 commits · 1 force push"},
		{"one of each", requests.Changes{Commits: 1, ForcePushes: 1}, "1 commit · 1 force push"},
		{"neither", requests.Changes{}, "head moved"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := shown(t, drawn(withCandidates(0, nil, rows(candidateRow(7, tt.changes)), nil)))

			line := lineOf(t, m.View(), 7)

			if want := "· alice · " + tt.want + " · reviewed 3d ago"; !strings.HasSuffix(line, want) {
				t.Errorf("#7 = %q, want it to end with %q", line, want)
			}
			if strings.Contains(line, "5h ago") {
				t.Errorf("#7 = %q, want no update age", line)
			}
		})
	}
}

// A candidate row has no team, which is how a row asked of the user looks. It
// must still not say the user was asked or name a team.
func TestACandidateRowClaimsNoRequest(t *testing.T) {
	m := shown(t, drawn(withCandidates(2, nil, rows(candidateRow(7, twoCommits)), nil)))

	line := lineOf(t, m.View(), 7)

	if !strings.HasSuffix(line, "· reviewed 3d ago") || strings.Contains(line, "asked you") {
		t.Errorf("#7 = %q, want it to end with the review age and claim no request", line)
	}
}

// c hides only the covered rows, so it leaves the candidates as p set them.
func TestCLeavesTheCandidatesAlone(t *testing.T) {
	m := shown(t, candidates())

	for _, press := range []string{"c", "c"} {
		m, _ = m.Update(key(press))
		if view := m.View(); !strings.Contains(view, "o/r#7") || !strings.Contains(view, "o/r#8") {
			t.Fatalf("the candidates are hidden after c (covered shown: %v):\n%s", m.ShowCovered, view)
		}
	}
}

// With the toggle off, the screen draws a list that carries candidates exactly
// as it draws the same list without them, so the default view is unchanged.
func TestTheViewWithTheCandidatesOffIsTheViewWithoutThem(t *testing.T) {
	for _, teams := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("%d teams", teams), func(t *testing.T) {
			without := list(rows(row(1, ""), row(2, review.StateReviewing)), rows(teamRow(3)), rows(teamRow(4)))
			without.Teams = teams
			carrying := without
			carrying.Sections = append(slices.Clone(without.Sections),
				requests.Section{Kind: requests.RereviewExternal, Rows: rows(candidateRow(7, twoCommits))},
				requests.Section{Kind: requests.RereviewMember, Rows: rows(candidateRow(8, twoCommits))},
			)
			carrying.Rereview = true

			for _, keys := range [][]string{nil, {"G"}, {"c", "G"}} {
				a, b := drawn(without), drawn(carrying)
				for _, k := range keys {
					a, _ = a.Update(key(k))
					b, _ = b.Update(key(k))
				}

				if a.View() != b.View() {
					t.Errorf("after %v the view changed:\nwithout candidates:\n%s\ncarrying candidates:\n%s", keys, a.View(), b.View())
				}
			}
		})
	}
}

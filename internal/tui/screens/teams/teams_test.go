package teams

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
)

func key(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: rune(s[0]), Text: s} }

var (
	space = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	enter = tea.KeyPressMsg{Code: tea.KeyEnter}
	esc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

func sent(t *testing.T, cmd tea.Cmd) tea.Msg {
	t.Helper()
	if cmd == nil {
		t.Fatal("no command")
	}
	return cmd()
}

func slugs(m Model) []string {
	var out []string
	for _, row := range m.Rows {
		out = append(out, row.Slug)
	}
	return out
}

// loaded is the screen after GitHub listed two teams, of which the config names
// one. The unchecked team sorts first, and the cursor is on it. SetMemberships
// alone leaves the cursor on the configured team.
func loaded() Model {
	m := New(Styles{}).
		Load([]string{"PostHog/team-feature-flags"}).
		SetMemberships([]string{"aseriousbiz/founders", "PostHog/team-feature-flags"}, nil)
	m, _ = m.Update(key("g"))
	return m
}

// line is the line of the view that names slug.
func line(t *testing.T, view, slug string) string {
	t.Helper()
	for _, l := range strings.Split(ansi.Strip(view), "\n") {
		if strings.Contains(l, slug) {
			return l
		}
	}
	t.Fatalf("the view does not show %s:\n%s", slug, view)
	return ""
}

func manyTeams(n int) []string {
	var out []string
	for i := range n {
		out = append(out, fmt.Sprintf("o/team-%02d", i))
	}
	return out
}

func TestLoadChecksEveryConfiguredTeam(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/a", "o/b"})

	if len(m.Rows) != 2 {
		t.Fatalf("rows = %+v, want one for each configured team", m.Rows)
	}
	for _, row := range m.Rows {
		if !row.Checked || row.Member {
			t.Errorf("row = %+v, want it checked and not yet known to be one of the user's teams", row)
		}
	}
}

func TestLoadSortsTheConfiguredTeamsWithoutRegardToCase(t *testing.T) {
	m := New(Styles{}).Load([]string{"Z/a", "b/c"})

	if want := []string{"b/c", "Z/a"}; !slices.Equal(slugs(m), want) {
		t.Errorf("rows = %v, want %v", slugs(m), want)
	}
}

// GitHub resolves a team slug without regard to case, so two spellings in the
// config are one team.
func TestLoadCollapsesTeamsThatDifferOnlyByCase(t *testing.T) {
	m := New(Styles{}).Load([]string{"PostHog/team-a", "posthog/TEAM-A"})

	if len(m.Rows) != 1 || !strings.EqualFold(m.Rows[0].Slug, "PostHog/team-a") {
		t.Errorf("rows = %+v, want the one team", m.Rows)
	}
}

func TestLoadStartsTheScreenOver(t *testing.T) {
	m := loaded()
	m.Cursor = 1
	m.Err = errors.New("HTTP 403")
	m.Busy = true

	m = m.Load([]string{"o/a"})

	if !slices.Equal(slugs(m), []string{"o/a"}) {
		t.Errorf("rows = %v, want only the configured team", slugs(m))
	}
	if m.Cursor != 0 || !m.Loading || m.Err != nil || m.Busy {
		t.Errorf("cursor = %d, loading = %v, err = %v, busy = %v, want 0, true, nil, false", m.Cursor, m.Loading, m.Err, m.Busy)
	}
}

func TestSetMembershipsStopsLoading(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/a"})
	m.Loading = true

	if m.SetMemberships([]string{"o/b"}, nil).Loading {
		t.Error("the screen still reads as loading after GitHub answered")
	}
}

func TestSetMembershipsAddsTheOtherTeamsUnchecked(t *testing.T) {
	m := loaded()

	i := slices.IndexFunc(m.Rows, func(r Row) bool { return r.Slug == "aseriousbiz/founders" })
	if i < 0 {
		t.Fatalf("rows = %v, want GitHub's other team added", slugs(m))
	}
	if row := m.Rows[i]; row.Checked || !row.Member {
		t.Errorf("row = %+v, want it unchecked and one of the user's teams", row)
	}
}

func TestSetMembershipsMarksAConfiguredTeamAsOneOfTheUsers(t *testing.T) {
	m := loaded()

	i := slices.IndexFunc(m.Rows, func(r Row) bool { return r.Slug == "PostHog/team-feature-flags" })
	if i < 0 {
		t.Fatalf("rows = %v, want the configured team", slugs(m))
	}
	if row := m.Rows[i]; !row.Checked || !row.Member {
		t.Errorf("row = %+v, want it checked and one of the user's teams", row)
	}
}

// A slug typed into config.toml by hand may not match GitHub's capitalization.
func TestSetMembershipsTakesGitHubsSpellingOfAConfiguredTeam(t *testing.T) {
	m := New(Styles{}).Load([]string{"posthog/team-feature-flags"})

	m = m.SetMemberships([]string{"PostHog/team-feature-flags"}, nil)

	want := []Row{{Slug: "PostHog/team-feature-flags", Checked: true, Member: true}}
	if !slices.Equal(m.Rows, want) {
		t.Errorf("rows = %+v, want %+v", m.Rows, want)
	}
}

func TestSetMembershipsKeepsTheRowsSortedWithoutRegardToCase(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/m"})

	m = m.SetMemberships([]string{"Z/z", "a/a"}, nil)

	if want := []string{"a/a", "o/m", "Z/z"}; !slices.Equal(slugs(m), want) {
		t.Errorf("rows = %v, want %v", slugs(m), want)
	}
}

// config.toml may still name a team the user has left or one GitHub cannot
// resolve. It stays checked so that the user decides whether to drop it.
func TestSetMembershipsKeepsAConfiguredTeamGitHubDoesNotList(t *testing.T) {
	m := New(Styles{}).Load([]string{"PostHog/gone"})

	m = m.SetMemberships([]string{"PostHog/team-a"}, nil)

	i := slices.IndexFunc(m.Rows, func(r Row) bool { return r.Slug == "PostHog/gone" })
	if i < 0 {
		t.Fatalf("rows = %v, want the configured team kept", slugs(m))
	}
	if row := m.Rows[i]; !row.Checked || row.Member {
		t.Errorf("row = %+v, want it checked and not one of the user's teams", row)
	}
}

func TestSetMembershipsDoesNotAddATeamTwice(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/a"})

	m = m.SetMemberships([]string{"o/b"}, nil).SetMemberships([]string{"O/B"}, nil)

	if len(m.Rows) != 2 {
		t.Errorf("rows = %+v, want o/a and o/b once each", m.Rows)
	}
}

// GitHub can answer while the user is already moving through the configured
// teams. GitHub's teams join the list in sorted order. Space must still act on
// the team the user selected.
func TestSetMembershipsKeepsTheCursorOnItsTeam(t *testing.T) {
	m := New(Styles{}).Load([]string{"z/team"})

	m = m.SetMemberships([]string{"a/team", "Z/team"}, nil)

	if m.Cursor < 0 || m.Cursor >= len(m.Rows) || m.Rows[m.Cursor].Slug != "Z/team" {
		t.Errorf("cursor = %d over %v, want it on Z/team", m.Cursor, slugs(m))
	}
}

func TestSetMembershipsKeepsTheCursorInRange(t *testing.T) {
	m := New(Styles{}).Load(nil)
	m.Cursor = 5

	m = m.SetMemberships([]string{"o/a", "o/b"}, nil)

	if m.Cursor < 0 || m.Cursor >= len(m.Rows) {
		t.Errorf("cursor = %d over %d rows, want it on a row", m.Cursor, len(m.Rows))
	}
}

func TestAFailedReadKeepsTheConfiguredTeamsChecked(t *testing.T) {
	failure := errors.New("HTTP 403")

	m := New(Styles{}).Load([]string{"o/a"}).SetMemberships(nil, failure)

	if !errors.Is(m.Err, failure) || m.Loading {
		t.Errorf("err = %v, loading = %v, want the failure and no longer loading", m.Err, m.Loading)
	}
	if want := []Row{{Slug: "o/a", Checked: true}}; !slices.Equal(m.Rows, want) {
		t.Errorf("rows = %+v, want %+v", m.Rows, want)
	}
}

func TestCheckedListsTheCheckedTeamsInRowOrder(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/c", "o/a"}).SetMemberships([]string{"o/b"}, nil)

	if want := []string{"o/a", "o/c"}; !slices.Equal(m.Checked(), want) {
		t.Errorf("checked = %v, want %v", m.Checked(), want)
	}
}

func TestSpaceChecksTheTeamUnderTheCursor(t *testing.T) {
	m, _ := loaded().Update(space)

	if want := []string{"aseriousbiz/founders", "PostHog/team-feature-flags"}; !slices.Equal(m.Checked(), want) {
		t.Errorf("checked = %v, want %v", m.Checked(), want)
	}
}

func TestSpaceUnchecksAConfiguredTeam(t *testing.T) {
	m, _ := loaded().Update(key("j"))

	m, _ = m.Update(space)

	i := slices.IndexFunc(m.Rows, func(r Row) bool { return r.Slug == "PostHog/team-feature-flags" })
	if i < 0 || m.Rows[i].Checked {
		t.Errorf("rows = %+v, want the configured team unchecked", m.Rows)
	}
}

func TestSpaceAgainUnchecks(t *testing.T) {
	m, _ := loaded().Update(space)
	m, _ = m.Update(space)

	if want := []string{"PostHog/team-feature-flags"}; !slices.Equal(m.Checked(), want) {
		t.Errorf("checked = %v, want %v", m.Checked(), want)
	}
}

// The root is writing the checked teams to config.toml. A change now would show
// a list that is not the one saved.
func TestSpaceDoesNothingWhileSaving(t *testing.T) {
	m := loaded()
	m.Busy = true

	m, _ = m.Update(space)

	if want := []string{"PostHog/team-feature-flags"}; !slices.Equal(m.Checked(), want) {
		t.Errorf("checked = %v, want %v", m.Checked(), want)
	}
}

func TestSpaceOnAnEmptyListDoesNothing(t *testing.T) {
	m := New(Styles{}).Load(nil).SetMemberships(nil, nil)

	m, cmd := m.Update(space)

	if len(m.Rows) != 0 || cmd != nil {
		t.Errorf("rows = %+v, cmd = %v, want nothing", m.Rows, cmd != nil)
	}
}

func TestCursorKeysStayInsideTheList(t *testing.T) {
	tests := []struct {
		name string
		keys []tea.KeyPressMsg
		want int
	}{
		{"up at the top", []tea.KeyPressMsg{key("k")}, 0},
		{"down", []tea.KeyPressMsg{key("j")}, 1},
		{"down past the bottom", []tea.KeyPressMsg{key("j"), key("j"), key("j")}, 2},
		{"arrow down then up", []tea.KeyPressMsg{{Code: tea.KeyDown}, {Code: tea.KeyDown}, {Code: tea.KeyUp}}, 1},
		{"to the bottom", []tea.KeyPressMsg{key("G")}, 2},
		{"end", []tea.KeyPressMsg{{Code: tea.KeyEnd}}, 2},
		{"back to the top", []tea.KeyPressMsg{key("G"), key("g")}, 0},
		{"home", []tea.KeyPressMsg{key("G"), {Code: tea.KeyHome}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := New(Styles{}).Load([]string{"o/a", "o/b", "o/c"})
			for _, k := range tc.keys {
				m, _ = m.Update(k)
			}

			if m.Cursor != tc.want {
				t.Errorf("cursor = %d, want %d", m.Cursor, tc.want)
			}
		})
	}
}

func TestEnterSavesTheCheckedTeams(t *testing.T) {
	m, _ := loaded().Update(space)

	m, cmd := m.Update(enter)

	got, ok := sent(t, cmd).(msg.SaveTeams)
	if !ok {
		t.Fatalf("enter sent %#v, want SaveTeams", cmd())
	}
	if want := []string{"aseriousbiz/founders", "PostHog/team-feature-flags"}; !slices.Equal(got.Teams, want) {
		t.Errorf("teams = %v, want %v", got.Teams, want)
	}
	if !m.Busy {
		t.Error("the screen is not busy while the root saves")
	}
}

func TestEnterWithNothingCheckedSavesNoTeams(t *testing.T) {
	m, _ := loaded().Update(key("j"))
	m, _ = m.Update(space)

	_, cmd := m.Update(enter)

	got, ok := sent(t, cmd).(msg.SaveTeams)
	if !ok {
		t.Fatalf("enter sent %#v, want SaveTeams", cmd())
	}
	if len(got.Teams) != 0 {
		t.Errorf("teams = %v, want none", got.Teams)
	}
}

func TestASecondEnterWhileSavingSendsNothing(t *testing.T) {
	m, cmd := loaded().Update(enter)
	if _, ok := sent(t, cmd).(msg.SaveTeams); !ok {
		t.Fatalf("the first enter sent %#v, want SaveTeams", cmd())
	}

	if _, cmd := m.Update(enter); cmd != nil {
		t.Errorf("the second enter sent %#v", cmd())
	}
}

// A user who only wants to drop a configured team need not wait on GitHub.
func TestEnterSavesWhileGitHubIsStillAnswering(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/a", "o/b"})
	m, _ = m.Update(space)

	_, cmd := m.Update(enter)

	got, ok := sent(t, cmd).(msg.SaveTeams)
	if !ok {
		t.Fatalf("enter sent %#v, want SaveTeams", cmd())
	}
	if want := []string{"o/b"}; !slices.Equal(got.Teams, want) {
		t.Errorf("teams = %v, want %v", got.Teams, want)
	}
}

func TestKeysEmitIntents(t *testing.T) {
	tests := []struct {
		name string
		key  tea.KeyPressMsg
		want tea.Msg
	}{
		{"back", esc, msg.Goto{Screen: msg.Requests}},
		{"help", key("?"), msg.OpenHelp{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, cmd := loaded().Update(tc.key)
			if got := sent(t, cmd); got != tc.want {
				t.Errorf("got %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestMessagesThatAreNotKeysDoNothing(t *testing.T) {
	before := loaded()

	m, cmd := before.Update(tea.WindowSizeMsg{Width: 80, Height: 24})

	if cmd != nil || m.Cursor != before.Cursor || !slices.Equal(m.Rows, before.Rows) {
		t.Errorf("a window size changed the screen: rows = %+v, cursor = %d", m.Rows, m.Cursor)
	}
}

func TestTheViewSaysItIsReadingGitHub(t *testing.T) {
	m := New(Styles{}).Load(nil)

	if view := ansi.Strip(m.View()); !strings.Contains(view, "Reading your teams from GitHub") {
		t.Errorf("the view does not say it is reading GitHub:\n%s", view)
	}
}

func TestTheReadCarriesTheSpinnersFrame(t *testing.T) {
	m := New(Styles{}).Load(nil)
	m.Spinner.Frame = "⠙"

	if view := ansi.Strip(m.View()); !strings.Contains(view, "⠙ Reading your teams from GitHub") {
		t.Errorf("the view does not put the frame before the read:\n%s", view)
	}
}

func TestTheViewMarksCheckedAndUncheckedTeams(t *testing.T) {
	view := loaded().View()

	if l := line(t, view, "PostHog/team-feature-flags"); !strings.Contains(l, "[x]") {
		t.Errorf("the checked team reads %q, want [x]", l)
	}
	if l := line(t, view, "aseriousbiz/founders"); !strings.Contains(l, "[ ]") {
		t.Errorf("the unchecked team reads %q, want [ ]", l)
	}
}

func TestTheViewPointsAtTheCursorsRow(t *testing.T) {
	m, _ := loaded().Update(key("j"))

	view := m.View()

	if l := line(t, view, "PostHog/team-feature-flags"); !strings.HasPrefix(l, ">") {
		t.Errorf("the cursor's row reads %q, want it to start with >", l)
	}
	if l := line(t, view, "aseriousbiz/founders"); strings.HasPrefix(l, ">") {
		t.Errorf("a row off the cursor reads %q", l)
	}
}

// The configured teams show while GitHub is still answering, so the user can
// uncheck one without waiting.
func TestTheViewShowsTheConfiguredTeamsWhileReading(t *testing.T) {
	view := New(Styles{}).Load([]string{"o/a"}).View()

	if l := line(t, view, "o/a"); !strings.Contains(l, "[x]") {
		t.Errorf("the configured team reads %q, want it checked", l)
	}
}

// Before GitHub answers, or when it could not, docket does not know which
// teams are the user's. It tags none of them as not one of the user's teams.
func TestTheViewSaysATeamIsNotOneOfTheUsersOnlyOnceGitHubAnswered(t *testing.T) {
	tests := []struct {
		name string
		m    Model
		want bool
	}{
		{"reading", New(Styles{}).Load([]string{"PostHog/gone"}), false},
		{"read failed", New(Styles{}).Load([]string{"PostHog/gone"}).SetMemberships(nil, errors.New("HTTP 403")), false},
		{"answered", New(Styles{}).Load([]string{"PostHog/gone"}).SetMemberships([]string{"PostHog/team-a"}, nil), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			l := line(t, tc.m.View(), "PostHog/gone")

			if got := strings.Contains(l, "not one of your teams"); got != tc.want {
				t.Errorf("the row reads %q, want it to say it is not one of your teams = %v", l, tc.want)
			}
		})
	}
}

func TestTheViewDoesNotCallAMemberTeamAStranger(t *testing.T) {
	m := New(Styles{}).Load([]string{"PostHog/gone"}).SetMemberships([]string{"PostHog/team-a"}, nil)

	if l := line(t, m.View(), "PostHog/team-a"); strings.Contains(l, "not one of your teams") {
		t.Errorf("the row reads %q, want nothing about membership", l)
	}
}

// Listing a user's teams needs gh's read:org scope, which a token may lack.
func TestTheViewShowsWhyGitHubListedNoTeams(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/a"}).SetMemberships(nil, errors.New("gh api user/teams: HTTP 403: Resource not accessible by integration"))
	m.Width = 200

	view := strings.Join(strings.Fields(ansi.Strip(m.View())), " ")

	for _, want := range []string{"HTTP 403: Resource not accessible by integration", "read:org", "o/a"} {
		if !strings.Contains(view, want) {
			t.Errorf("the view does not show %q:\n%s", want, view)
		}
	}
}

func TestTheViewSaysWhenGitHubListsNoTeams(t *testing.T) {
	m := New(Styles{}).Load(nil).SetMemberships(nil, nil)

	if view := ansi.Strip(m.View()); !strings.Contains(view, "GitHub lists no teams for you") {
		t.Errorf("the view does not say there are no teams:\n%s", view)
	}
}

func TestTheViewKeepsTheCursorsRowVisibleInAShortPane(t *testing.T) {
	m := New(Styles{}).Load(manyTeams(10)).SetMemberships(nil, nil)
	m.Height = 3

	m, _ = m.Update(key("G"))
	view := ansi.Strip(m.View())

	if l := line(t, view, "o/team-09"); !strings.HasPrefix(l, ">") {
		t.Errorf("the cursor's row reads %q, want it to start with >", l)
	}
	if strings.Contains(view, "o/team-00") {
		t.Errorf("a pane three rows tall still drew the first team:\n%s", view)
	}
	if n := len(strings.Split(view, "\n")); n > 3 {
		t.Errorf("the view is %d lines, want at most 3:\n%s", n, view)
	}
}

func TestAPaneWithNoHeightDrawsEveryTeam(t *testing.T) {
	m := New(Styles{}).Load(manyTeams(10)).SetMemberships(nil, nil)

	view := m.View()

	for _, slug := range manyTeams(10) {
		if !strings.Contains(view, slug) {
			t.Errorf("the view does not show %s:\n%s", slug, view)
		}
	}
}

// With configured teams on show, the header is the one sign that GitHub is
// still answering.
func TestTheHeaderKeepsTheReadInViewOnANarrowTerminal(t *testing.T) {
	m := New(Styles{}).Load([]string{"o/a"})
	m.Width = 76

	header := strings.Split(ansi.Strip(m.View()), "\n")[0]

	if !strings.Contains(header, "reading your teams from GitHub") || format.Columns(header) > 76 {
		t.Errorf("header = %q, want the read in view within 76 columns", header)
	}
}

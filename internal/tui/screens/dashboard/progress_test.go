package dashboard

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/core/tier"
)

var progressNow = time.Date(2026, 9, 24, 17, 30, 0, 0, time.UTC)

func runningRecord() review.Record {
	return review.Record{
		ID:        "rec-1",
		Ref:       pr.Ref{Org: "PostHog", Repo: "posthog", Number: 105890},
		Title:     "feat(flags): evaluate supported v2 flags and cache them beside v1",
		Engine:    "claude",
		Tier:      tier.Tier1,
		Mode:      review.ModeBackground,
		State:     review.StateReviewing,
		BGID:      "91ff90f0",
		StartedAt: progressNow.Add(-4 * time.Minute),
	}
}

func progressModel(width int, records ...review.Record) Model {
	m := New(Styles{}).SetRecords(records)
	m.Width = width
	m.Now = func() time.Time { return progressNow }
	return m
}

func lineOf(t *testing.T, view, substr string) string {
	t.Helper()
	for line := range strings.Lines(view) {
		if strings.Contains(line, substr) {
			return line
		}
	}
	t.Fatalf("no line contains %q:\n%s", substr, view)
	return ""
}

// The terminal cuts a row wider than itself at the right edge. The metadata at
// the end of the row is what it drops.
func TestNoLineIsWiderThanTheTerminal(t *testing.T) {
	long := runningRecord()
	long.Title = strings.Repeat("a very long pull request title ", 6)
	failed := long
	failed.ID, failed.State, failed.BGID = "rec-2", review.StateNotStarted, ""
	failed.Err = "claude does not trust " + strings.Repeat("/nested", 20)

	for _, width := range []int{60, 100, 160} {
		m := progressModel(width, long, failed)
		m.Busy["rec-1"] = "stopping"
		m.Background = map[string]review.Progress{"rec-1": {Detail: strings.Repeat("dispatching review agents ", 10), Agents: 7}}

		for line := range strings.Lines(m.View()) {
			line = strings.TrimRight(line, "\n")
			if got := utf8.RuneCountInString(line); got > width {
				t.Errorf("width %d: line is %d columns:\n%s", width, got, line)
			}
		}
	}
}

// The title shrinks first, because the metadata is what tells the rows apart.
func TestALongTitleGivesWayToTheMetadata(t *testing.T) {
	m := progressModel(100, runningRecord())

	line := lineOf(t, m.View(), "PostHog/posthog#105890")
	if !strings.HasSuffix(strings.TrimSpace(line), "· background") {
		t.Errorf("the metadata was cut off:\n%s", line)
	}
	if !strings.Contains(line, "…") {
		t.Errorf("the title was not shortened:\n%s", line)
	}
}

func TestARunningRowSaysWhatItsSessionIsDoing(t *testing.T) {
	m := progressModel(160, runningRecord())
	m.Background = map[string]review.Progress{"rec-1": {
		Detail:    "7 review agents dispatched; loading synthesis",
		Agents:    7,
		UpdatedAt: progressNow.Add(-30 * time.Second),
	}}

	line := lineOf(t, m.View(), "7 review agents dispatched")
	if !strings.Contains(line, "7 agents") {
		t.Errorf("the line does not say how many agents are running:\n%s", line)
	}
	if strings.Contains(line, "no update") {
		t.Errorf("a session that updated 30 seconds ago reads as quiet:\n%s", line)
	}
}

// A session waiting on the user makes no progress until the user opens it, and
// nothing else on the dashboard would say so.
func TestABlockedSessionSaysItIsWaitingForYou(t *testing.T) {
	m := progressModel(160, runningRecord())
	m.Background = map[string]review.Progress{"rec-1": {Detail: "asking to run gh", Needs: "permission to run gh"}}

	line := lineOf(t, m.View(), "waiting for you")
	if !strings.Contains(line, "permission to run gh") || !strings.Contains(line, "enter") {
		t.Errorf("the line does not say what the session needs or how to answer:\n%s", line)
	}
}

// Nothing tells docket that a session has hung. The time since the agent last
// updated its account is the sign the user can judge by.
func TestAQuietSessionSaysHowLongSinceItsLastUpdate(t *testing.T) {
	m := progressModel(160, runningRecord())
	m.Background = map[string]review.Progress{"rec-1": {Detail: "running reviewers", UpdatedAt: progressNow.Add(-12 * time.Minute)}}

	if line := lineOf(t, m.View(), "running reviewers"); !strings.Contains(line, "no update for 12m") {
		t.Errorf("the line does not say the session has been quiet:\n%s", line)
	}
}

// A blocked session gets the error style, so it stands out from sessions
// working on their own.
func TestABlockedSessionIsStyledAsAWarning(t *testing.T) {
	m := progressModel(160, runningRecord())
	m.Styles.Err = lipgloss.NewStyle().SetString("!")
	m.Background = map[string]review.Progress{"rec-1": {Needs: "your input"}}

	if line := lineOf(t, m.View(), "waiting for you"); !strings.HasPrefix(line, "!") {
		t.Errorf("the blocked line is not styled as a warning:\n%q", line)
	}
}

func TestALaunchThatDidNotStartHasItsOwnGroup(t *testing.T) {
	rec := runningRecord()
	rec.State, rec.BGID = review.StateNotStarted, ""
	rec.Err = "claude does not trust /Users/haacked/.docket/scratch yet"
	m := progressModel(160, rec)

	view := m.View()
	if !strings.Contains(view, "Did not start (1)") {
		t.Errorf("the refused launch is not in a group of its own:\n%s", view)
	}
	if !strings.Contains(view, "does not trust") {
		t.Errorf("the row does not say why it did not start:\n%s", view)
	}
}

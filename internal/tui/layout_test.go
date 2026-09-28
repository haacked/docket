package tui

import (
	"errors"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/requests"
	"github.com/haacked/docket/internal/core/review"
	"github.com/haacked/docket/internal/tui/msg"
)

func sizedTo(a App, width, height int) App {
	next, _ := a.Update(tea.WindowSizeMsg{Width: width, Height: height})
	return next.(App)
}

func lines(a App) []string {
	return strings.Split(ansi.Strip(a.View().Content), "\n")
}

// Nothing sits against the terminal's edge on any screen: a blank line above the
// title and a blank band down each side. A screen that does not fit its own
// lines to the width still keeps out of the right margin.
func TestEveryScreenKeepsAMarginAroundEverything(t *testing.T) {
	const width = 80
	rec := draftedRecord()
	rec.Title = strings.Repeat("a long pull request title ", 6)
	rec.NotesPath = "/Users/someone/.agents/skills/review-code/.reviews/PostHog/posthog/pr-106413.md"

	for name, open := range map[string]func(App) App{
		"dashboard": func(a App) App { return a },
		"new review": func(a App) App {
			next, _ := a.Update(msg.Goto{Screen: msg.NewReview})
			return next.(App)
		},
		"submit": func(a App) App {
			a.sub = a.sub.For(rec, []string{review.EventComment})
			a.screen = msg.Submit
			return a
		},
		"notes": func(a App) App {
			next, _ := a.Update(msg.OpenNotes{ID: rec.ID})
			return next.(App)
		},
		"help": func(a App) App {
			next, _ := a.Update(msg.OpenHelp{})
			return next.(App)
		},
		"requests": func(a App) App {
			a.screen = msg.Requests
			return a
		},
		"teams": func(a App) App {
			a.teams = a.teams.Load([]string{"PostHog/" + strings.Repeat("a-long-team-name-", 6)}).
				SetMemberships(nil, errors.New(strings.Repeat("gh api user/teams: HTTP 403 ", 4)))
			a.screen = msg.Teams
			return a
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := lines(sizedTo(open(liveApp(rec)), width, 30))

			if strings.TrimSpace(got[0]) != "" {
				t.Errorf("the first line is %q, want it blank", got[0])
			}
			for _, line := range got {
				if strings.TrimSpace(line) == "" {
					continue
				}
				if !strings.HasPrefix(line, strings.Repeat(" ", marginX)) {
					t.Errorf("line %q starts inside the left margin", line)
				}
				if w := ansi.StringWidth(strings.TrimRight(line, " ")); w > width-marginX {
					t.Errorf("line %q runs into the right margin at %d columns", line, w)
				}
			}
		})
	}
}

// The dashboard, the status, and an error fit themselves to the width inside
// the margins. The margin test cannot see this, because inMargins cuts a line
// that is too wide before the test measures it, and a cut line ends in "…".
func TestTheDashboardAndStatusFitInsideTheMargins(t *testing.T) {
	const width = 80
	rec := draftedRecord()
	rec.Title = strings.Repeat("a long pull request title ", 6)

	for name, set := range map[string]func(*App){
		"status": func(a *App) { a.status = "Refreshed 3 records." },
		"error":  func(a *App) { a.err = errors.New("gh: not signed in") },
	} {
		t.Run(name, func(t *testing.T) {
			a := liveApp(rec)
			set(&a)
			for _, line := range lines(sizedTo(a, width, 30)) {
				if strings.HasSuffix(strings.TrimRight(line, " "), "…") {
					t.Errorf("line %q was cut at the margin instead of fit to the width inside it", line)
				}
			}
		})
	}
}

// A terminal too narrow for the footer on one line wraps it between hints, so
// every key stays on screen and none is split from its label.
func TestANarrowTerminalWrapsTheFooterBetweenHints(t *testing.T) {
	const width = 50
	a := sizedTo(app(), width, 24)
	view := ansi.Strip(a.View().Content)

	for _, e := range helpFor(msg.Dashboard, false, false) {
		if hint := e.Key + " " + e.Short; !strings.Contains(view, hint) {
			t.Errorf("the footer is missing %q or splits it:\n%s", hint, view)
		}
	}
	for _, line := range lines(a) {
		if w := ansi.StringWidth(line); w > width {
			t.Errorf("line %q is %d columns, wider than the %d-column terminal", line, w, width)
		}
	}
}

// fit sizes the panes from what the page draws around them. A footer
// or a status that wraps to more lines leaves the pane less room, rather than
// pushing the footer off the bottom of the terminal.
func TestAWrappedFooterAndStatusStillFitTheTerminal(t *testing.T) {
	const width, height = 40, 20
	rec := draftedRecord()
	var mine []requests.PR
	for n := range 30 {
		mine = append(mine, requests.PR{Ref: pr.Ref{Org: "o", Repo: "r", Number: n + 1}, Title: "a request"})
	}

	for name, open := range map[string]tea.Msg{
		"help":     msg.OpenHelp{},
		"notes":    msg.OpenNotes{ID: rec.ID},
		"requests": msg.OpenRequests{},
	} {
		t.Run(name, func(t *testing.T) {
			// The requests list is longer than the pane, so it fills whatever
			// height fit gives it.
			loaded, _ := liveApp(rec).Update(requestsLoadedMsg{fetched: requests.Fetched{Mine: mine}})
			a := loaded.(App)
			a.status = strings.Repeat("a status long enough to wrap ", 4)
			next, _ := sizedTo(a, width, height).Update(open)
			got := lines(next.(App))

			if len(got) > height {
				t.Errorf("the page is %d lines on a %d-line terminal:\n%s", len(got), height, strings.Join(got, "\n"))
			}
			if last := got[len(got)-1-marginY]; !strings.Contains(last, "ctrl+c quit") {
				t.Errorf("the footer is not at the bottom of the page, which ends:\n%s", strings.Join(got[len(got)-4:], "\n"))
			}
		})
	}
}

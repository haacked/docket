package tui

import (
	"slices"

	"github.com/haacked/docket/internal/tui/format"
	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/help"
)

// helpFor is the footer's keys for a screen, built from the same per-screen
// tables help.View uses for the full key reference, so the two cannot drift
// the way they once did. choosing means the screen is asking what to do with a
// review that already exists. That question lists its own keys. The screen's
// other keys do nothing until the user answers it.
func helpFor(screen msg.Screen, showArchived, choosing bool) []help.Entry {
	if choosing {
		return []help.Entry{help.Quit}
	}
	switch screen {
	case msg.NewReview:
		return append(help.Footer(help.NewReview), help.Quit)
	case msg.Submit:
		return append(help.Footer(help.Submit), help.Quit)
	case msg.Notes:
		return append(help.Footer(help.Notes), help.Quit)
	case msg.Requests:
		return append(help.Footer(help.Requests), help.Quit)
	case msg.Teams:
		return append(help.Footer(help.Teams), help.Quit)
	case msg.Help:
		return []help.Entry{{Key: "↑/↓", Short: "scroll"}, {Key: "esc/?", Short: "back"}, help.Quit}
	default:
		archived := help.Entry{Key: "a", Short: "show archived"}
		if showArchived {
			archived.Short = "hide archived"
		}
		// ? and q are the table's last two footer entries. The archived
		// toggle's label depends on state, so it is built here rather than
		// in the table. It is slotted in before them, where "a" sits in the
		// table.
		entries := help.Footer(help.Dashboard)
		return slices.Insert(entries, len(entries)-2, archived)
	}
}

// footer is the screen's keys, wrapped to the page between hints. Keys take the
// style the full help screen gives them, so they stand out from their labels.
func (a App) footer() string {
	var hints []string
	for _, e := range helpFor(a.screen, a.dash.ShowArchived, a.choosing()) {
		hints = append(hints, a.styles.Label.Render(e.Key)+" "+a.styles.Footer.Render(e.Short))
	}
	return format.Wrap(hints, a.styles.Footer.Render(" · "), format.Width(a.inner()))
}

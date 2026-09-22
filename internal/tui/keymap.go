package tui

import (
	"slices"
	"strings"

	"github.com/haacked/docket/internal/tui/msg"
	"github.com/haacked/docket/internal/tui/screens/help"
)

// helpFor is the footer line for a screen, built from the same per-screen
// tables help.View uses for the full key reference, so the two cannot drift
// the way they once did.
func helpFor(screen msg.Screen, showArchived bool) string {
	switch screen {
	case msg.NewReview:
		return join(append(help.Footer(help.NewReview), "ctrl+c quit")...)
	case msg.Submit:
		return join(append(help.Footer(help.Submit), "ctrl+c quit")...)
	case msg.Notes:
		return join(append(help.Footer(help.Notes), "ctrl+c quit")...)
	case msg.Help:
		return join("↑/↓ scroll", "esc/? back", "ctrl+c quit")
	default:
		archived := "a show archived"
		if showArchived {
			archived = "a hide archived"
		}
		// ? and q are the table's last two footer entries. The archived
		// toggle's label depends on state, so it is built here rather than
		// in the table. It is slotted in before them, where "a" sits in the
		// table.
		parts := help.Footer(help.Dashboard)
		parts = slices.Insert(parts, len(parts)-2, archived)
		return join(parts...)
	}
}

func join(parts ...string) string { return strings.Join(parts, "  ·  ") }

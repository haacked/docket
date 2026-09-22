package tui

import (
	"strings"

	"github.com/haacked/docket/internal/tui/msg"
)

// helpFor is the footer line for a screen. NewReview and Submit leave ? out of
// their own hints: each holds a free-text field (a pasted PR URL, a review
// body) that a user may legitimately want to type a "?" into.
func helpFor(screen msg.Screen, showArchived bool) string {
	switch screen {
	case msg.NewReview:
		return join("enter start", "tab engine", "ctrl+b background", "esc back", "ctrl+c quit")
	case msg.Submit:
		return join("ctrl+s submit", "tab event", "esc back", "ctrl+c quit")
	case msg.Notes:
		return join("e edit", "↑/↓ scroll", "esc back", "? help", "ctrl+c quit")
	case msg.Help:
		return join("esc/? back", "ctrl+c quit")
	default:
		archived := "a show archived"
		if showArchived {
			archived = "a hide archived"
		}
		return join("n new", "enter resume", "s submit", "v notes", "x abandon", "r refresh", "R refresh all", archived, "? help", "q quit")
	}
}

func join(parts ...string) string { return strings.Join(parts, "  ·  ") }

package tui

import (
	"strings"

	"github.com/haacked/docket/internal/tui/msg"
)

// helpFor is the footer line for a screen.
func helpFor(screen msg.Screen, showArchived bool) string {
	switch screen {
	case msg.NewReview:
		return join("enter start", "tab engine", "esc back", "ctrl+c quit")
	default:
		archived := "a show archived"
		if showArchived {
			archived = "a hide archived"
		}
		return join("n new", "enter resume", "x abandon", "r refresh", "R refresh all", archived, "q quit")
	}
}

func join(parts ...string) string { return strings.Join(parts, "  ·  ") }

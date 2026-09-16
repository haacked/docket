// Package choice is the one-line list of options a screen cycles with tab. The
// new review screen picks an engine with it and the submit screen picks a review
// event.
package choice

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
)

// Next returns the option after current, wrapping at the end. slices.Index
// returns -1 for an option that is not listed, which lands on the first one.
func Next(options []string, current string) string {
	if len(options) == 0 {
		return current
	}
	return options[(slices.Index(options, current)+1)%len(options)]
}

// Line renders the options with the current one in brackets. A zero selected
// style leaves the brackets to mark it.
func Line(options []string, current string, selected, dim lipgloss.Style) string {
	parts := make([]string, 0, len(options))
	for _, option := range options {
		if option == current {
			parts = append(parts, selected.Render("["+option+"]"))
			continue
		}
		parts = append(parts, dim.Render(" "+option+" "))
	}
	return strings.Join(parts, " ")
}

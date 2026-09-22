// Package format holds the text helpers more than one screen draws with.
package format

import (
	"fmt"
	"time"
)

// Truncate counts runes, not bytes. width is a column budget, and a pull request
// title carrying an accent or an emoji otherwise gets cut inside a character.
func Truncate(s string, width int) string {
	if width <= 1 {
		return s
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

// Ago says how long ago something happened, to the coarsest unit that fits.
func Ago(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

// Width is the terminal width, or 80 before the first WindowSizeMsg arrives.
func Width(width int) int {
	if width <= 0 {
		return 80
	}
	return width
}

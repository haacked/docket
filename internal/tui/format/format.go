// Package format holds the text helpers more than one screen draws with.
package format

import (
	"fmt"
	"time"
	"unicode/utf8"
)

// Truncate counts runes, not bytes. width is a column budget, and a pull request
// title carrying an accent or an emoji otherwise gets cut inside a character.
// A budget with no room for even the ellipsis leaves nothing.
func Truncate(s string, width int) string {
	if width < 2 {
		return ""
	}
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}

// Columns counts runes, the unit Truncate cuts by.
func Columns(s string) int { return utf8.RuneCountInString(s) }

// minTitle is the fewest columns Row shrinks a title to before it cuts the
// metadata instead.
const minTitle = 20

// Row fits a list row of head, title, and meta into width columns. It shortens
// the title first, because the metadata is what tells the rows apart. When the
// title is at its minimum, it cuts the metadata too, and a terminal too narrow
// for even that cuts the row itself. It returns the two parts separately so that
// each can take its own style.
func Row(head, title, meta string, width int) (string, string) {
	left := head + Truncate(title, max(minTitle, width-Columns(head)-1-Columns(meta)))
	if Columns(left) > width {
		return Truncate(left, width), ""
	}
	return left, Truncate(meta, width-Columns(left)-1)
}

// Ago says how long ago something happened, to the coarsest unit that fits.
func Ago(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	return Duration(d) + " ago"
}

// Duration is d to the coarsest unit that fits.
func Duration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "<1m"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// Plural is noun as it reads after the count n.
func Plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// Width is the terminal width, or 80 before the first WindowSizeMsg arrives.
func Width(width int) int {
	if width <= 0 {
		return 80
	}
	return width
}

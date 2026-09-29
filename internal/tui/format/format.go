// Package format holds the text helpers more than one screen draws with.
package format

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/haacked/docket/internal/core/review"
)

// Truncate cuts s to width terminal columns, ending in an ellipsis when it cuts.
// An emoji or a CJK character takes two columns, so a rune count would let a
// title overflow. A budget with no room for even the ellipsis leaves nothing.
func Truncate(s string, width int) string {
	if width < 2 {
		return ""
	}
	return ansi.Truncate(s, width, "…")
}

// Columns is how many terminal columns s takes, the unit Truncate cuts by.
func Columns(s string) int { return ansi.StringWidth(s) }

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

// Wrap lays items out left to right over as many lines as width needs, with sep
// between neighbors on a line. It breaks only between items, so a key never lands
// on a different line from its label. An item wider than width goes on a line of
// its own, cut to fit.
func Wrap(items []string, sep string, width int) string {
	var lines []string
	line := ""
	for _, item := range items {
		if Columns(item) > width {
			item = Truncate(item, width)
		}
		switch {
		case line == "":
			line = item
		case Columns(line)+Columns(sep)+Columns(item) <= width:
			line += sep + item
		default:
			lines = append(lines, line)
			line = item
		}
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
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

// Pane draws header above lines in height rows and keeps the cursor's line in
// view. A zero height draws every line. A pane one line tall has no room for
// the header. It draws only the cursor's line, which is the line the user acts
// on.
func Pane(header string, lines []string, cursor, height int) string {
	if height == 1 && len(lines) > 0 {
		return lines[cursor]
	}
	if height > 1 {
		lines = window(lines, cursor, height-1)
	}
	return header + "\n" + strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// window keeps at most height lines, placed so the cursor's line is visible.
func window(lines []string, cursor, height int) []string {
	if len(lines) <= height {
		return lines
	}
	start := min(max(0, cursor-height/2), len(lines)-height)
	return lines[start : start+height]
}

// OneLine is err's text on a single line. gh's errors carry its stderr, which
// can run over several lines.
func OneLine(err error) string {
	return strings.Join(strings.Fields(err.Error()), " ")
}

// Plural is noun as it reads after the count n.
func Plural(n int, noun string) string {
	if n == 1 {
		return noun
	}
	return noun + "s"
}

// Existing says in one line what review of a pull request is already there. It
// is empty when there is none.
func Existing(found review.Found) string {
	return strings.Join(found.Phrases(), " · ")
}

// Width is the terminal width, or 80 before the first WindowSizeMsg arrives.
func Width(width int) int {
	if width <= 0 {
		return 80
	}
	return width
}

// Spinner draws work in flight in the busy style. The root sets it on every
// screen before each draw. Frame is empty in a view drawn before the root's
// spinner has ticked.
type Spinner struct {
	Frame string
	Style lipgloss.Style
}

// Render is the spinner's frame, then what is running.
func (s Spinner) Render(text string) string {
	if s.Frame == "" {
		return s.Style.Render(text + "…")
	}
	return s.Style.Render(s.Frame + " " + text + "…")
}

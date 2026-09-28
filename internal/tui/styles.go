package tui

import (
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/haacked/docket/internal/tui/format"
)

// styles are docket's colors and spacing. Colors are ANSI indexes so they follow
// whatever palette the user's terminal already uses. Secondary text asks for
// faint instead of ANSI 8. A theme may set that slot as close to its background
// as it likes, and many set it too close to read. Faint dims the foreground the
// terminal is already using, and a terminal that ignores it renders full
// contrast rather than nothing. Work in flight is never faint, because a user who
// cannot see it assumes nothing is running.
type styles struct {
	Title    lipgloss.Style
	Group    lipgloss.Style
	Row      lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
	Err      lipgloss.Style
	Footer   lipgloss.Style
	Label    lipgloss.Style
	Busy     lipgloss.Style
}

// wrap folds long text to width. Bubble Tea cuts anything wider than the
// terminal, and a launch command easily runs past it.
func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	return lipgloss.NewStyle().Width(width).Render(text)
}

// inMargins puts the page inside its margins. It cuts any line wider than
// width, the room inside the margins, so a screen that does not fit its own
// lines to the width still keeps out of the right margin. A width of zero is
// unknown and cuts nothing.
func inMargins(page string, width int) string {
	lines := strings.Split(page, "\n")
	indent := strings.Repeat(" ", marginX)
	for i, line := range lines {
		if width > 0 && format.Columns(line) > width {
			line = format.Truncate(line, width)
		}
		lines[i] = indent + line
	}
	blank := strings.Repeat("\n", marginY)
	return blank + strings.Join(lines, "\n") + blank
}

// marginX and marginY are the blank columns on each side of the page and the
// blank lines above and below it, so no text sits against the terminal's edge.
const (
	marginX = 2
	marginY = 1
)

func newStyles() styles {
	return styles{
		Title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		Group:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4")),
		Row:      lipgloss.NewStyle(),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
		Dim:      lipgloss.NewStyle().Faint(true),
		Err:      lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Footer:   lipgloss.NewStyle().Faint(true),
		Label:    lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
		Busy:     lipgloss.NewStyle().Foreground(lipgloss.Color("6")),
	}
}

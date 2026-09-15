package tui

import "charm.land/lipgloss/v2"

// styles are docket's colors and spacing. Colors are ANSI indexes so they follow
// whatever palette the user's terminal already uses.
type styles struct {
	Title    lipgloss.Style
	Group    lipgloss.Style
	Row      lipgloss.Style
	Selected lipgloss.Style
	Dim      lipgloss.Style
	Err      lipgloss.Style
	Footer   lipgloss.Style
	Label    lipgloss.Style
}

// wrap folds long text to the window width. Bubble Tea cuts anything wider than
// the terminal, and a launch command easily runs past it.
func wrap(text string, width int) string {
	if width <= 0 {
		return text
	}
	return lipgloss.NewStyle().Width(width).Render(text)
}

func newStyles() styles {
	return styles{
		Title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		Group:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("4")),
		Row:      lipgloss.NewStyle(),
		Selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("3")),
		Dim:      lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Err:      lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		Footer:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		Label:    lipgloss.NewStyle().Foreground(lipgloss.Color("5")),
	}
}

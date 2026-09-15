// Package msg holds the messages the screens send to the root model, and the
// helper that turns one into a command. It imports nothing but Bubble Tea, so a
// screen can name a message without reaching the service the root owns.
package msg

import tea "charm.land/bubbletea/v2"

// Screen names one of docket's screens.
type Screen int

const (
	Dashboard Screen = iota
	NewReview
)

// Goto switches screens.
type Goto struct{ Screen Screen }

// StartReview asks for a review of whatever the user typed.
type StartReview struct {
	Input  string
	Engine string
}

// Resume reopens the session behind a record.
type Resume struct{ ID string }

// Abandon drops a record and cleans up after it.
type Abandon struct{ ID string }

// RefreshRecords re-reads GitHub. An empty ID means every record whose session is
// over.
type RefreshRecords struct{ ID string }

// Send wraps a message as the command that delivers it.
func Send(message tea.Msg) tea.Cmd {
	return func() tea.Msg { return message }
}

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
	Submit
	Notes
	Help
	Requests
)

// Goto switches screens.
type Goto struct{ Screen Screen }

// StartReview asks for a review of whatever the user typed. Background asks for
// one that runs without the terminal.
type StartReview struct {
	Input      string
	Engine     string
	Background bool
}

// Resume reopens the session behind a record.
type Resume struct{ ID string }

// Abandon drops a record and cleans up after it.
type Abandon struct{ ID string }

// RefreshRecords re-reads GitHub. An empty ID means every record whose session is
// over.
type RefreshRecords struct{ ID string }

// OpenSubmit asks for the submit screen for a record.
type OpenSubmit struct{ ID string }

// SubmitReview submits the record's pending review.
type SubmitReview struct {
	ID    string
	Event string
	Body  string
}

// OpenNotes asks for the notes review-code wrote for a record.
type OpenNotes struct{ ID string }

// EditNotes opens those notes in the user's editor.
type EditNotes struct{ ID string }

// OpenRequests asks for the list of pull requests waiting on the user's review,
// searched afresh.
type OpenRequests struct{}

// RefreshRequests searches GitHub again for that list.
type RefreshRequests struct{}

// StartBatch starts a background review of each pull request under Engine.
type StartBatch struct {
	URLs   []string
	Engine string
}

// PrefillReview opens the new review screen with the pull request filled in.
type PrefillReview struct{ URL string }

// OpenHelp asks for the full key reference over whatever screen sent it.
type OpenHelp struct{}

// Send wraps a message as the command that delivers it.
func Send(message tea.Msg) tea.Cmd {
	return func() tea.Msg { return message }
}

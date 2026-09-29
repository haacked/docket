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
	Teams
)

// Goto switches screens.
type Goto struct{ Screen Screen }

// StartReview asks for a review of whatever the user typed. Background asks for
// one that runs without the terminal.
//
// Intent is the user's answer when the pull request already has a review, as
// one of review.Intent's values. It is empty on the first request, which is
// the one that asks what is already there. RecordID names the dashboard record
// to review again instead of the typed input.
type StartReview struct {
	Input      string
	Engine     string
	Background bool
	Intent     string
	RecordID   string
}

// Ask opens a question-and-answer session about a record's notes.
type Ask struct{ ID string }

// OpenRereview asks for the choice between appending to a record's review and
// overwriting it.
type OpenRereview struct{ ID string }

// Resume reopens the session behind a record.
type Resume struct{ ID string }

// Abandon drops a record and cleans up after it.
type Abandon struct{ ID string }

// RefreshRecords re-reads GitHub. An empty ID means every record whose session is
// over.
type RefreshRecords struct{ ID string }

// OpenSubmit asks for the submit screen for a record.
type OpenSubmit struct{ ID string }

// SubmitReview submits the record's pending review. ReviewID is the pending
// review the screen showed. The poll can move the row to a newer draft while the
// screen is open. The root then refuses the submit.
type SubmitReview struct {
	ID       string
	ReviewID int64
	Event    string
	Body     string
}

// OpenNotes asks for the notes review-code wrote for a record.
type OpenNotes struct{ ID string }

// EditNotes opens those notes in the user's editor.
type EditNotes struct{ ID string }

// OpenOnGitHub opens a record's review on GitHub in the browser.
type OpenOnGitHub struct{ ID string }

// OpenRequests asks for the list of pull requests waiting on the user's review,
// searched afresh.
type OpenRequests struct{}

// RefreshRequests searches GitHub again for that list.
type RefreshRequests struct{}

// StartBatch starts a background review of each pull request under Engine. The
// root first reads what review each one already has. When any has one, the root
// asks the requests screen for an AnswerBatch before it starts anything.
type StartBatch struct {
	URLs   []string
	Engine string
}

// AnswerBatch says what to do with the pull requests in a batch that already have
// a review. Intent is one of review.Intent's values, append or overwrite, and
// applies to all of them. An empty Intent leaves them out and starts the rest.
type AnswerBatch struct{ Intent string }

// PrefillReview opens the new review screen with the pull request filled in.
type PrefillReview struct{ URL string }

// OpenTeams asks for the list of the user's teams, read afresh from GitHub, with
// the ones the requests screen searches for checked.
type OpenTeams struct{}

// SaveTeams makes Teams the teams whose review requests the requests screen
// lists. Each is an "org/team" slug.
type SaveTeams struct{ Teams []string }

// OpenHelp asks for the full key reference over whatever screen sent it.
type OpenHelp struct{}

// Send wraps a message as the command that delivers it.
func Send(message tea.Msg) tea.Cmd {
	return func() tea.Msg { return message }
}

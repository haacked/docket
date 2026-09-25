package review

import "time"

// Found is what already exists for a pull request before docket reviews it.
type Found struct {
	// NotesAt is when the notes file was last written, and zero when there is
	// none.
	NotesAt time.Time
	// PendingID is my pending review on GitHub, or 0.
	PendingID int64
	// Submitted reports that I have already submitted a review.
	Submitted bool
}

// Any reports whether there is a review the user has to choose what to do with.
func (f Found) Any() bool {
	return !f.NotesAt.IsZero() || f.PendingID != 0 || f.Submitted
}

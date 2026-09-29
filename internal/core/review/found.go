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

// Phrases names each review that is there, in the words docket shows the user.
func (f Found) Phrases() []string {
	var out []string
	if !f.NotesAt.IsZero() {
		out = append(out, "notes from "+f.NotesAt.Format(time.DateOnly))
	}
	if f.PendingID != 0 {
		out = append(out, "pending draft on GitHub")
	}
	if f.Submitted {
		out = append(out, "submitted review on GitHub")
	}
	return out
}

// Any reports whether there is a review the user has to choose what to do with.
func (f Found) Any() bool {
	return !f.NotesAt.IsZero() || f.PendingID != 0 || f.Submitted
}

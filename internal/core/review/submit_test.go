package review

import (
	"slices"
	"testing"
)

// The strings go on the wire to GitHub, which answers 422 to anything else.
func TestTheEventsAreTheOnesGitHubAccepts(t *testing.T) {
	for _, tt := range []struct{ got, want string }{
		{EventComment, "COMMENT"},
		{EventApprove, "APPROVE"},
		{EventRequestChanges, "REQUEST_CHANGES"},
	} {
		if tt.got != tt.want {
			t.Errorf("event = %q, want %q", tt.got, tt.want)
		}
	}
}

func TestSubmitEventsForLeavesOutApprovingYourOwnPullRequest(t *testing.T) {
	tests := []struct {
		name   string
		author string
		me     string
		want   []string
	}{
		{
			name:   "someone else's pull request",
			author: "someone",
			me:     "haacked",
			want:   []string{EventComment, EventApprove, EventRequestChanges},
		},
		{
			name:   "my own pull request",
			author: "haacked",
			me:     "haacked",
			want:   []string{EventComment, EventRequestChanges},
		},
		{
			// GitHub resolves a login without case, so this is still me.
			name:   "my own under a different case",
			author: "HaackeD",
			me:     "haacked",
			want:   []string{EventComment, EventRequestChanges},
		},
		{
			// docket has not asked GitHub who it is yet. Offering the approval
			// costs a 422 the user can retry from; hiding it strands the case
			// where the pull request is somebody else's.
			name:   "login not known yet",
			author: "haacked",
			me:     "",
			want:   []string{EventComment, EventApprove, EventRequestChanges},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SubmitEventsFor(tt.author, tt.me)

			if !slices.Equal(got, tt.want) {
				t.Errorf("events = %v, want %v", got, tt.want)
			}
		})
	}
}

// The screen offers the first event as the choice already made, so the order is
// what makes comment the default.
func TestCommentIsTheFirstEventOffered(t *testing.T) {
	for _, me := range []string{"haacked", "someone"} {
		events := SubmitEventsFor("haacked", me)

		if len(events) == 0 || events[0] != EventComment {
			t.Errorf("events for me=%q are %v, want comment first", me, events)
		}
	}
}

// review-code publishes a blocking finding as "`blocking`: ...". A user who
// edits the comment can write the prefix in any form review-code's own parser
// accepts, so each of those counts too.
func TestBlockingReadsTheSeverityPrefix(t *testing.T) {
	tests := []struct {
		body string
		want bool
	}{
		{"`blocking`: This drops the lock.", true},
		{"`blocking:` This drops the lock.", true},
		{"**blocking:** This drops the lock.", true},
		{"**blocking**: This drops the lock.", true},
		{"blocking: This drops the lock.", true},
		{"`Blocking`: This drops the lock.", true},
		{"  \n`blocking`: leading space", true},
		{"`suggestion`: Rename this.", false},
		{"`nit`: blocking: is not the prefix here", false},
		{"This is non-blocking.", false},
		{"`blocking` without a colon", false},
		{"blockingly: no", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := Blocking(tt.body); got != tt.want {
			t.Errorf("Blocking(%q) = %v, want %v", tt.body, got, tt.want)
		}
	}
}

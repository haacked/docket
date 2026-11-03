package review

import (
	"regexp"
	"strings"
)

// The events GitHub accepts when a pending review is submitted.
const (
	EventComment        = "COMMENT"
	EventApprove        = "APPROVE"
	EventRequestChanges = "REQUEST_CHANGES"
)

// SubmitEvents are the events in the order the submit screen offers them.
// Comment leads because it is the one that always applies.
var SubmitEvents = []string{EventComment, EventApprove, EventRequestChanges}

// SubmitEventsFor lists the events the user may submit on this pull request.
// GitHub answers 422 to approving your own, so that choice is left out rather
// than offered and refused after the fact.
func SubmitEventsFor(author, me string) []string {
	if me == "" || !strings.EqualFold(author, me) {
		return SubmitEvents
	}
	events := make([]string, 0, len(SubmitEvents))
	for _, event := range SubmitEvents {
		if event != EventApprove {
			events = append(events, event)
		}
	}
	return events
}

// blockingPrefix matches the severity prefix of a blocking finding in each form
// review-code's finding-comment-contract.py accepts. review-code publishes the
// first form, and the user may edit a comment into one of the others.
var blockingPrefix = regexp.MustCompile("(?i)^(?:`blocking(?::`|`:)|\\*\\*blocking(?::\\*\\*|\\*\\*:)|blocking:)")

// Blocking reports whether a review comment's body is a blocking finding.
func Blocking(body string) bool {
	return blockingPrefix.MatchString(strings.TrimSpace(body))
}

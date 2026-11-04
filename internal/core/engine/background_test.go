package engine

import (
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

// At launch the listing reads idle before the session starts, while the status
// file already says active.
func TestWaitingNeedsAnEndedTurnThatTheStatusFileDoesNotCallActive(t *testing.T) {
	tests := []struct {
		name   string
		status BGStatus
		want   bool
	}{
		{name: "ended its turn", status: BGStatus{Idle: true}, want: true},
		{name: "working", status: BGStatus{}, want: false},
		{name: "starting", status: BGStatus{Idle: true, Progress: review.Progress{Active: true}}, want: false},
	}
	for _, tc := range tests {
		if got := tc.status.Waiting(); got != tc.want {
			t.Errorf("%s: Waiting() = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// claude can name a need while the session's reviewer agents still run.
func TestNeedIsNamedOnlyWhileTheSessionWaits(t *testing.T) {
	needs := review.Progress{Needs: "permission to run gh"}
	tests := []struct {
		name   string
		status BGStatus
		want   string
	}{
		{name: "waiting", status: BGStatus{Idle: true, Progress: needs}, want: "permission to run gh"},
		{name: "working", status: BGStatus{Progress: needs}, want: ""},
		{name: "starting", status: BGStatus{Idle: true, Progress: review.Progress{Active: true, Needs: needs.Needs}}, want: ""},
		{name: "at a prompt", status: BGStatus{Prompted: true, Progress: needs}, want: "permission to run gh"},
	}
	for _, tc := range tests {
		if got := tc.status.Need(); got != tc.want {
			t.Errorf("%s: Need() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// A prompt stops a session partway through its turn. The session makes no
// progress until the user answers, but its turn has not ended.
func TestASessionAtAPromptNeedsTheUserWithoutEndingItsTurn(t *testing.T) {
	prompted := BGStatus{Prompted: true, Progress: review.Progress{Active: true}}
	if !prompted.NeedsUser() {
		t.Error("a session at a prompt does not need the user")
	}
	if prompted.Waiting() {
		t.Error("a session at a prompt read as having ended its turn")
	}
	if !(BGStatus{Idle: true}).NeedsUser() {
		t.Error("a session that ended its turn does not need the user")
	}
	if (BGStatus{}).NeedsUser() {
		t.Error("a working session needs the user")
	}
}

func TestBackgroundNamePrefersTheOneAskedForAndFallsBackToOneWithABackgroundMode(t *testing.T) {
	if got := BackgroundName("claude"); got != "claude" {
		t.Errorf("BackgroundName(claude) = %q", got)
	}
	if got := BackgroundName("codex"); got != "claude" {
		t.Errorf("BackgroundName(codex) = %q, want claude: codex has no background mode", got)
	}
}

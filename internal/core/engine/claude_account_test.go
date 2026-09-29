package engine

import (
	"slices"
	"testing"

	"github.com/haacked/docket/internal/core/exec"
)

// automation is the config directory of a second Claude Code account.
const automation = "/Users/me/.claude-automation"

// accountEnv is the environment a spec gives claude when docket itself runs
// with another account's CLAUDE_CONFIG_DIR exported.
func accountEnv(spec exec.CommandSpec) []string {
	return slices.Sorted(slices.Values(spec.Env([]string{"CLAUDE_CONFIG_DIR=/elsewhere", "HOME=/h"})))
}

// claudeSpecs builds every command the claude engine runs for a record.
func claudeSpecs(t *testing.T, paths Paths) map[string]exec.CommandSpec {
	t.Helper()
	rec := record()
	rec.BGID = "6d681a76"

	resume, ok := Claude{}.Resume(rec, paths)
	if !ok {
		t.Fatal("Resume refused a record with a session id")
	}
	attach, ok := Claude{}.OpenSpec(rec, BGStatus{Live: true}, paths)
	if !ok {
		t.Fatal("OpenSpec refused a live background session")
	}
	reopen, ok := Claude{}.OpenSpec(rec, BGStatus{}, paths)
	if !ok {
		t.Fatal("OpenSpec refused a stopped background session")
	}
	return map[string]exec.CommandSpec{
		"start":            Claude{}.Start(rec, paths),
		"resume":           resume,
		"ask":              Claude{}.Ask(rec, paths),
		"background start": Claude{}.StartBackground(rec, paths),
		"status":           Claude{}.StatusSpec(paths),
		"attach":           attach,
		"open by resume":   reopen,
		"stop":             Claude{}.StopSpec(rec, paths),
		"trust":            Claude{}.TrustSpec("/tmp/clone", paths),
	}
}

// CLAUDE_CONFIG_DIR picks the account whose sessions claude runs, lists, and
// stops. claude names its keychain credential differently whenever the variable
// is set, even when it is set to ~/.claude. The default account therefore
// removes the variable rather than setting it to that directory.
func TestEveryClaudeCommandRunsUnderTheAccountItIsGiven(t *testing.T) {
	for _, account := range []struct {
		name   string
		config string
		want   []string
	}{
		{name: "a named account", config: automation, want: []string{"CLAUDE_CONFIG_DIR=" + automation, "HOME=/h"}},
		{name: "the default account", config: "", want: []string{"HOME=/h"}},
	} {
		t.Run(account.name, func(t *testing.T) {
			for command, spec := range claudeSpecs(t, Paths{ClaudeConfig: account.config}) {
				t.Run(command, func(t *testing.T) {
					if got := accountEnv(spec); !slices.Equal(got, account.want) {
						t.Errorf("%s runs with %v, want %v", spec, got, account.want)
					}
				})
			}
		})
	}
}

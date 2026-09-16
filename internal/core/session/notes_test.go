package session

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/haacked/docket/internal/core/review"
)

func writeNotes(t *testing.T, path, body string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestNotesReadWhatReviewCodeWrote(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	writeNotes(t, rec.NotesPath, "# Review\n\nThe lock is dropped before the rewrite.\n")

	markdown, missing, err := svc.Notes(rec)
	if err != nil {
		t.Fatalf("Notes: %v", err)
	}

	if missing {
		t.Errorf("Notes reports %s missing, but review-code wrote it", rec.NotesPath)
	}
	if !strings.Contains(markdown, "The lock is dropped before the rewrite.") {
		t.Errorf("markdown = %q, want the file's contents", markdown)
	}
}

// The editor the notes screen hands the terminal to writes the file, so the
// read after it has to reach disk rather than answer from what was read before.
func TestNotesAreReReadEveryTime(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)
	writeNotes(t, rec.NotesPath, "before the edit\n")

	if _, _, err := svc.Notes(rec); err != nil {
		t.Fatalf("Notes: %v", err)
	}
	writeNotes(t, rec.NotesPath, "after the edit\n")

	markdown, _, err := svc.Notes(rec)
	if err != nil {
		t.Fatalf("Notes: %v", err)
	}
	if !strings.Contains(markdown, "after the edit") {
		t.Errorf("markdown = %q, want what the editor left behind", markdown)
	}
}

// review-code writes the notes during the session, so a record that has not
// reached one yet simply has none. The path is still worth reporting.
func TestNotesReportAMissingFileWithoutFailing(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	markdown, missing, err := svc.Notes(rec)
	if err != nil {
		t.Fatalf("Notes: %v", err)
	}

	if !missing {
		t.Error("Notes reports a file that is not there as present")
	}
	if markdown != "" {
		t.Errorf("markdown = %q, want nothing", markdown)
	}
}

func TestEditorSpecOpensThePathInTheConfiguredEditor(t *testing.T) {
	const path = "/opt/review-code/.reviews/haacked/docket/pr-7.md"

	tests := []struct {
		name   string
		editor string
		want   string
	}{
		{
			name:   "a plain editor",
			editor: "vim",
			want:   `vim "$1"`,
		},
		{
			// An editor that returns before the file is saved would send docket
			// back to notes it has already re-read, so people set this.
			name:   "an editor carrying arguments",
			editor: "code --wait",
			want:   `code --wait "$1"`,
		},
		{
			name:   "several arguments",
			editor: "emacsclient -nw -c",
			want:   `emacsclient -nw -c "$1"`,
		},
		{
			// The quotes are the shell's to read, so the space stays inside the
			// executable's path.
			name:   "an executable whose path contains a space",
			editor: `"/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl" -w`,
			want:   `"/Applications/Sublime Text.app/Contents/SharedSupport/bin/subl" -w "$1"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := EditorSpec(tt.editor, path)

			if spec.Path != "sh" {
				t.Errorf("path = %q, want the shell that reads $EDITOR", spec.Path)
			}
			want := []string{"-c", tt.want, "sh", path}
			if !slices.Equal(spec.Args, want) {
				t.Errorf("args = %q, want %q", spec.Args, want)
			}
		})
	}
}

// The notes path reaches the shell as $1, not inside the script. A path
// carrying a quote or a semicolon is therefore an argument, never a command.
func TestEditorSpecKeepsThePathOutOfTheScript(t *testing.T) {
	const path = `/tmp/notes/"; touch /tmp/pwned; ".md`

	spec := EditorSpec("vim", path)

	if got := spec.Args[1]; got != `vim "$1"` {
		t.Errorf("script = %q, want the path left out of it", got)
	}
	if got := spec.Args[len(spec.Args)-1]; got != path {
		t.Errorf("last argument = %q, want the path %q", got, path)
	}
}

func TestEditorSpecFallsBackWhenTheEnvironmentNamesNoEditor(t *testing.T) {
	const path = "/opt/review-code/.reviews/haacked/docket/pr-7.md"

	for _, editor := range []string{"", "   "} {
		spec := EditorSpec(editor, path)

		if spec.Args[1] != DefaultEditor+` "$1"` {
			t.Errorf("EDITOR=%q gives the script %q, want %q", editor, spec.Args[1], DefaultEditor+` "$1"`)
		}
		if spec.Args[len(spec.Args)-1] != path {
			t.Errorf("EDITOR=%q gives the arguments %v, want the notes last", editor, spec.Args)
		}
	}
}

func TestEditNotesSpecOpensTheRecordsNotes(t *testing.T) {
	t.Setenv("EDITOR", "code --wait")
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())
	rec := launched(t, svc, unlisted)

	spec, err := svc.EditNotesSpec(rec)
	if err != nil {
		t.Fatalf("EditNotesSpec: %v", err)
	}

	if spec.Args[1] != `code --wait "$1"` {
		t.Errorf("script = %q, want the editor from the environment", spec.Args[1])
	}
	if spec.Args[len(spec.Args)-1] != rec.NotesPath {
		t.Errorf("args = %v, want %s last", spec.Args, rec.NotesPath)
	}
}

func TestEditNotesSpecRefusesARecordWithNoNotes(t *testing.T) {
	ghc := &fakeGH{login: "haacked", info: prInfo()}
	svc, _ := newService(t, ghc, newFakeGit())

	if _, err := svc.EditNotesSpec(review.Record{ID: "rec-1"}); err == nil {
		t.Error("EditNotesSpec built a command for a record that names no notes")
	}
}

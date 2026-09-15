package session

import (
	"os"
	"path/filepath"
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
		name     string
		editor   string
		wantPath string
		wantArgs []string
	}{
		{
			name:     "a plain editor",
			editor:   "vim",
			wantPath: "vim",
			wantArgs: []string{path},
		},
		{
			// An editor that returns before the file is saved would send docket
			// back to notes it has already re-read, so people set this.
			name:     "an editor carrying arguments",
			editor:   "code --wait",
			wantPath: "code",
			wantArgs: []string{"--wait", path},
		},
		{
			name:     "several arguments",
			editor:   "emacsclient -nw -c",
			wantPath: "emacsclient",
			wantArgs: []string{"-nw", "-c", path},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := EditorSpec(tt.editor, path)

			if spec.Path != tt.wantPath {
				t.Errorf("path = %q, want %q", spec.Path, tt.wantPath)
			}
			if strings.Join(spec.Args, " ") != strings.Join(tt.wantArgs, " ") {
				t.Errorf("args = %v, want %v", spec.Args, tt.wantArgs)
			}
		})
	}
}

func TestEditorSpecFallsBackWhenTheEnvironmentNamesNoEditor(t *testing.T) {
	const path = "/opt/review-code/.reviews/haacked/docket/pr-7.md"

	for _, editor := range []string{"", "   "} {
		spec := EditorSpec(editor, path)

		if spec.Path == "" || strings.ContainsAny(spec.Path, " \t") {
			t.Errorf("EDITOR=%q gives the command %q, want one executable docket can run", editor, spec.Path)
		}
		if len(spec.Args) == 0 || spec.Args[len(spec.Args)-1] != path {
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

	if spec.Path != "code" {
		t.Errorf("path = %q, want the editor from the environment", spec.Path)
	}
	if len(spec.Args) == 0 || spec.Args[len(spec.Args)-1] != rec.NotesPath {
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

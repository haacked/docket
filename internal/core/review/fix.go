package review

import "strings"

// FixChoice is what the user asked for about fixing when starting a review.
type FixChoice string

const (
	// FixAuto fixes a pull request whose author is in fix_authors and drafts a
	// review of any other.
	FixAuto FixChoice = ""
	// FixOn runs review-code with --fix whoever the author is.
	FixOn FixChoice = "fix"
	// FixOff drafts a review whoever the author is.
	FixOff FixChoice = "draft"
)

// Fixes reports whether the review runs --fix, given whether the pull
// request's author is in fix_authors.
func (c FixChoice) Fixes(listed bool) bool {
	switch c {
	case FixOn:
		return true
	case FixOff:
		return false
	default:
		return listed
	}
}

// ListedAuthor reports whether login is in list, ignoring case. gh pr view
// names a GitHub App's pull requests `app/<name>` and the search API names them
// `<name>[bot]`, so either spelling in the list matches either spelling of the
// login. A person's login can contain neither `/` nor `[`, so a person named
// like the app does not match.
func ListedAuthor(list []string, login string) bool {
	want := canonicalLogin(login)
	for _, entry := range list {
		if canonicalLogin(entry) == want {
			return true
		}
	}
	return false
}

func canonicalLogin(login string) string {
	login = strings.ToLower(strings.TrimSpace(login))
	if name, ok := strings.CutSuffix(login, "[bot]"); ok {
		return "app/" + name
	}
	return login
}

// Checkout is what docket read from a fix review's working tree.
type Checkout struct {
	// Head is the commit the checkout is on.
	Head string
	// Dirty reports uncommitted changes, untracked files included.
	Dirty bool
	// Ahead counts the commits the checkout has that its remote-tracking ref
	// does not.
	Ahead int
	// FetchErr is why the fetch before the read failed. Ahead then counts
	// against the remote-tracking ref from the last fetch that worked.
	FetchErr error
}

// Local reports whether the checkout holds work GitHub does not have, which
// deleting the checkout would lose.
func (c Checkout) Local() bool {
	return c.Dirty || c.Ahead > 0
}

// State is where a fix review stands, judged from its checkout. base is the
// commit the checkout started from. A clean checkout still on base with no
// notes written since docket recorded base did not finish, because review-code
// writes its notes after the fix pass.
func (c Checkout) State(base string, notesWritten bool) State {
	switch {
	case c.Local():
		return StateFixed
	case c.Head == base && !notesWritten:
		return StateUnreviewed
	default:
		return StatePushed
	}
}

package session

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/haacked/docket/internal/core/exec"
	"github.com/haacked/docket/internal/core/review"
)

// Browse opens the record's review on GitHub in the user's browser.
func (s *Service) Browse(rec review.Record) error {
	if err := s.Runner.Start(BrowseSpec(rec)); err != nil {
		return fmt.Errorf("open %s in the browser: %w", rec.WebURL(), err)
	}
	return nil
}

// BrowseSpec is exported so that a dry run can print the command Browse
// would run.
func BrowseSpec(rec review.Record) exec.CommandSpec {
	return BrowserSpec(os.Getenv("BROWSER"), runtime.GOOS, rec.WebURL())
}

// BrowserSpec builds the command that opens url. browser is $BROWSER. With no
// browser set, macOS opens the URL with open and every other platform uses
// xdg-open.
func BrowserSpec(browser, goos, url string) exec.CommandSpec {
	switch {
	case strings.TrimSpace(browser) != "":
		return shellSpec(browser, url)
	case goos == "darwin":
		return exec.CommandSpec{Path: "open", Args: []string{url}}
	default:
		return exec.CommandSpec{Path: "xdg-open", Args: []string{url}}
	}
}

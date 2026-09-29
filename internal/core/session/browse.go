package session

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/haacked/docket/internal/core/exec"
)

// Browse opens url in the user's browser.
func (s *Service) Browse(url string) error {
	if err := s.Runner.Start(BrowseSpec(url)); err != nil {
		return fmt.Errorf("open %s in the browser: %w", url, err)
	}
	return nil
}

// BrowseSpec is exported so that a dry run can print the command Browse
// would run.
func BrowseSpec(url string) exec.CommandSpec {
	return BrowserSpec(os.Getenv("BROWSER"), runtime.GOOS, url)
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

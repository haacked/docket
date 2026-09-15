// Package pr parses the ways a pull request can be named on the command line.
package pr

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Ref identifies one pull request.
type Ref struct {
	Org    string `json:"org"`
	Repo   string `json:"repo"`
	Number int    `json:"number"`
}

func (r Ref) String() string { return fmt.Sprintf("%s/%s#%d", r.Org, r.Repo, r.Number) }

func (r Ref) Slug() string { return r.Org + "/" + r.Repo }

func (r Ref) URL() string {
	return fmt.Sprintf("https://github.com/%s/%s/pull/%d", r.Org, r.Repo, r.Number)
}

func (r Ref) Valid() bool {
	return validSegment(r.Org) && validSegment(r.Repo) && r.Number > 0
}

var (
	urlRE   = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([^/]+)/([^/]+)/pull/(\d+)`)
	hashRE  = regexp.MustCompile(`^([^/\s]+)/([^/#\s]+)#(\d+)$`)
	pathRE  = regexp.MustCompile(`^([^/\s]+)/([^/\s]+)/pull/(\d+)$`)
	plainRE = regexp.MustCompile(`^#?(\d+)$`)
	segRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

// ParseRef reads a PR URL, an "org/repo#N" reference, or a bare number. A bare
// number needs defaultRepo in "org/repo" form.
func ParseRef(input, defaultRepo string) (Ref, error) {
	s := strings.TrimSpace(input)
	if s == "" {
		return Ref{}, fmt.Errorf("no pull request given")
	}

	for _, re := range []*regexp.Regexp{urlRE, hashRE, pathRE} {
		if m := re.FindStringSubmatch(s); m != nil {
			return build(m[1], m[2], m[3])
		}
	}

	if m := plainRE.FindStringSubmatch(s); m != nil {
		org, repo, ok := splitSlug(defaultRepo)
		if !ok {
			return Ref{}, fmt.Errorf("%q is a bare number and no default repo is set", s)
		}
		return build(org, repo, m[1])
	}

	return Ref{}, fmt.Errorf("cannot read %q as a pull request", s)
}

func build(org, repo, number string) (Ref, error) {
	n, err := strconv.Atoi(number)
	if err != nil {
		return Ref{}, fmt.Errorf("bad pull request number %q: %w", number, err)
	}
	ref := Ref{Org: org, Repo: strings.TrimSuffix(repo, ".git"), Number: n}
	if !ref.Valid() {
		return Ref{}, fmt.Errorf("%s is not a usable pull request reference", ref)
	}
	return ref, nil
}

func splitSlug(slug string) (org, repo string, ok bool) {
	org, repo, ok = strings.Cut(strings.TrimSpace(slug), "/")
	if !ok || org == "" || repo == "" {
		return "", "", false
	}
	return org, repo, true
}

// validSegment also keeps org and repo safe to use as directory names, since
// docket builds clone paths out of them.
func validSegment(s string) bool {
	if s == "." || s == ".." || strings.Contains(s, "..") {
		return false
	}
	return segRE.MatchString(s)
}

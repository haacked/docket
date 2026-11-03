package session

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/haacked/docket/internal/core/pr"
	"github.com/haacked/docket/internal/core/review"
)

// Handoff is what Submit did to hand a blocking review back to the user's
// teams. A step that failed is in Errs. A failed step never fails the submit,
// because the review is already on GitHub when the handoff starts.
type Handoff struct {
	// Teams are the teams Submit asked to review the pull request, as org/team.
	Teams []string
	// NoTeam reports that the review blocks and that none of the configured teams
	// was ever asked to review the pull request, so Submit asked no team.
	NoTeam bool
	// Unassigned reports that Submit removed the user from the assignees. It is
	// false when the user was not an assignee.
	Unassigned bool
	Errs       []error
}

// String is the handoff as one line for the user, or "" when the review did not
// block and nothing failed.
func (h Handoff) String() string {
	var did []string
	if len(h.Teams) > 0 {
		did = append(did, "asked "+joinAnd(h.Teams)+" to review it")
	}
	if h.Unassigned {
		did = append(did, "removed you from its assignees")
	}
	var sentences []string
	if len(did) > 0 {
		sentences = append(sentences, "docket "+strings.Join(did, " and "))
	}
	if h.NoTeam {
		sentences = append(sentences, "None of your teams was asked to review it, so docket asked no team")
	}
	if len(h.Errs) > 0 {
		reasons := make([]string, len(h.Errs))
		for i, err := range h.Errs {
			reasons[i] = err.Error()
		}
		sentences = append(sentences, "The handoff to your team failed: "+strings.Join(reasons, "; "))
	}
	return strings.Join(sentences, ". ")
}

func joinAnd(items []string) string {
	switch len(items) {
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", and " + items[len(items)-1]
	}
}

// MayHandBack reports whether submitting the record's review as event hands it
// back to the user's teams when the review blocks. An approval never blocks. The
// author of a pull request is not its reviewer, so a review of the user's own
// pull request hands nothing back.
func MayHandBack(rec review.Record, event, me string) bool {
	return event != review.EventApprove && !ownPR(rec.Author, me)
}

// handBack lets a teammate finish a review that blocks the pull request when the
// author fixes it and the user is away. It asks each configured team that was
// ever asked to review the pull request to review it again. When the user is an
// assignee, it also removes the user from the assignees, where Assign put the
// user to tell the team that the user took the review. GitHub already drops the
// user's own review request on a submit.
func (s *Service) handBack(ctx context.Context, rec review.Record, event, me string) Handoff {
	var h Handoff
	if !MayHandBack(rec, event, me) {
		return h
	}
	blocks, err := s.blocks(ctx, rec, event)
	if err != nil {
		h.Errs = append(h.Errs, err)
		return h
	}
	if !blocks {
		return h
	}

	teams, err := s.requestedTeamsOfMine(ctx, rec.Ref)
	switch {
	case err != nil:
		h.Errs = append(h.Errs, err)
	case len(teams) == 0:
		h.NoTeam = true
	default:
		if err := s.GH.RequestTeamReview(ctx, rec.Ref, teams); err != nil {
			h.Errs = append(h.Errs, err)
		} else {
			h.Teams = teams
		}
	}
	if h.Unassigned, err = s.GH.Unassign(ctx, rec.Ref, me); err != nil {
		h.Errs = append(h.Errs, err)
	}
	return h
}

// blocks reports whether submitting the record's review as event blocks the
// pull request. A request for changes always blocks. A comment blocks when one
// of its inline comments is a blocking finding, which review-code marks with a
// severity prefix (review.Blocking).
func (s *Service) blocks(ctx context.Context, rec review.Record, event string) (bool, error) {
	switch event {
	case review.EventRequestChanges:
		return true, nil
	case review.EventComment:
		bodies, err := s.GH.ReviewComments(ctx, rec.Ref, rec.ReviewID)
		if err != nil {
			return false, fmt.Errorf("could not tell whether the review blocks %s: %w", rec.Ref, err)
		}
		return slices.ContainsFunc(bodies, review.Blocking), nil
	default:
		return false, nil
	}
}

// requestedTeamsOfMine returns the configured teams, as org/team, that were ever
// asked to review the pull request. A team request can only name a team of the
// repository's own organization, so it reads GitHub only when a configured team
// belongs to it.
func (s *Service) requestedTeamsOfMine(ctx context.Context, ref pr.Ref) ([]string, error) {
	var candidates []string
	for _, team := range s.Config().Teams {
		if org, _, ok := strings.Cut(team, "/"); ok && strings.EqualFold(org, ref.Org) {
			candidates = append(candidates, team)
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}
	requested, err := s.GH.RequestedTeams(ctx, ref)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(candidates, func(team string) bool {
		_, slug, _ := strings.Cut(team, "/")
		return !slices.ContainsFunc(requested, func(r string) bool { return strings.EqualFold(r, slug) })
	}), nil
}

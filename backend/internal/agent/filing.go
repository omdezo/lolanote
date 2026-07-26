package agent

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// Cross-board filing.
//
// Every other capability here is bounded by one board's subtree, and that
// boundary is what makes "the agent cannot reach anything outside the board you
// started it on" true rather than merely intended. Filing is the one job that
// genuinely needs to cross it: a tray of captures belongs on the boards they are
// about, not on the board they landed in.
//
// The widening is an ATTENUATION, kept safe by four properties:
//
//  1. The destination must be a board the HUMAN can already edit, checked with
//     their own principal at apply time. The grant can never exceed the person.
//  2. The destination must be one the agent FOUND — via search or the board
//     tree — not an id lifted from card text. An id it was never shown is
//     refused and counted, exactly like an out-of-scope element.
//  3. The list is derived from a plan a person approved, and minted per commit.
//  4. It expires with the run.
//
// The plan states an intention; the service decides whether it is permitted.
// Those are deliberately different places.

// maxDestinationsPerRun bounds how far one run can spread. Filing a tray across
// three or four boards is ordinary; across twenty is a plan nobody can review.
const maxDestinationsPerRun = 6

// noteDestination records a board this plan intends to file into, after
// checking the agent actually discovered it.
func (s *staging) noteDestination(ctx context.Context, boardID string) error {
	if boardID == "" || boardID == s.scope.Board.ID {
		return nil // inside the root; no widening needed
	}
	if !s.discovered[boardID] {
		// The same discipline as element ids: a board id the run was never
		// shown can only have come from content, so it is refused and counted
		// rather than quietly honoured.
		s.outOfScope++
		return fmt.Errorf("%s is not a board you have looked at — search for it first", boardID)
	}
	for _, d := range s.plan.Destinations {
		if d == boardID {
			return nil
		}
	}
	if len(s.plan.Destinations) >= maxDestinationsPerRun {
		return fmt.Errorf("that is enough different boards for one run (%d)", maxDestinationsPerRun)
	}
	s.plan.Destinations = append(s.plan.Destinations, boardID)
	return nil
}

// markDiscovered records board ids the run has legitimately seen, so filing can
// tell a board it found from one that was named at it.
func (s *staging) markDiscovered(ids ...string) {
	if s.discovered == nil {
		s.discovered = map[string]bool{}
	}
	for _, id := range ids {
		if id != "" {
			s.discovered[id] = true
		}
	}
}

// authorizeDestinations validates every board an approved plan files into
// against the HUMAN's own edit rights, and returns the ones that passed.
//
// Checked with the person's principal, never the delegation: the whole point is
// that this cannot grant reach the human does not already have. A destination
// they cannot edit is dropped rather than failing the apply — the rest of the
// plan is still worth committing, and the run reports what it left alone.
func (s *Service) authorizeDestinations(ctx context.Context, p *domain.Principal, plan *Plan) []string {
	if plan == nil || len(plan.Destinations) == 0 {
		return nil
	}
	out := make([]string, 0, len(plan.Destinations))
	for _, boardID := range plan.Destinations {
		if _, err := s.access.RequireEdit(ctx, boardID, p); err != nil {
			s.log.Warn("agent: destination refused — the user cannot edit it",
				zap.String("board", boardID))
			continue
		}
		el, err := s.elements.Get(ctx, boardID)
		if err != nil || el.Type != domain.TypeBoard || el.IsDeleted() {
			continue
		}
		out = append(out, boardID)
	}
	return out
}

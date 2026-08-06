package agent

import (
	"context"
	"time"

	"qomranote/backend/internal/domain"
)

// BoardState is everything the composer needs the moment a board opens: how
// much of the board the agent can actually see, whether it looks like it wants
// tidying, and whether a run is already under way here.
//
// One call rather than three because all of it comes from one scope compile,
// and because the three answers are only useful together. Asking separately was
// how the client ended up able to render a confident, wrong picture: it knew
// the board's element count and nothing about the agent's reach, so the scope
// chip promised 300 elements when the digest caps at 120.
type BoardState struct {
	// Visible is how many elements the agent would actually be shown.
	Visible int `json:"visible"`
	// Elided is how many the budget leaves out. The composer must show this:
	// an agent that silently sees two thirds of a board looks like an agent
	// that ignores instructions.
	Elided int `json:"elided"`
	// Drift is the free observation that this board wants attention, or nil.
	Drift *Drift `json:"drift,omitempty"`
	// Active describes a run already under way on this board, or nil.
	Active *ActiveRun `json:"active,omitempty"`
}

// ActiveRun is what anyone on the board may know about a run in flight.
//
// Deliberately two shapes in one type. If the run is YOURS, ID is set and the
// client re-adopts the whole run and re-enters the review. If it belongs to a
// colleague you get the fact, the actor and the size — enough that the board
// changing under you has a visible cause — and nothing else. The request text
// is the person's own words and is not shared; the plan is theirs to decide on.
type ActiveRun struct {
	// ID is present only when the run is the caller's own.
	ID    string   `json:"id,omitempty"`
	State RunState `json:"state"`
	Mine  bool     `json:"mine"`
	// Actor is the display name of whoever started it. Empty when it is yours —
	// the client already knows who you are.
	Actor     string    `json:"actor,omitempty"`
	StartedAt time.Time `json:"startedAt"`
}

// BoardStateFor compiles the board once and answers all three questions.
func (s *Service) BoardStateFor(ctx context.Context, p *domain.Principal, boardID string) (*BoardState, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	// Edit rights, not view: everything here is about a capability only editors
	// have, and the actor's name is not for a share-link visitor.
	if _, err := s.access.RequireEdit(ctx, boardID, p); err != nil {
		return nil, err
	}

	out := &BoardState{}

	if active, err := s.runs.ActiveByBoard(ctx, boardID); err == nil && active != nil {
		out.Active = s.describeActive(ctx, p, active)
	}

	scope, err := CompileScope(ctx, s.elements, TaskSpec{
		Owner: p.Sub, RootBoardID: boardID, Scope: ScopeBoard,
	})
	if err != nil {
		return nil, err
	}
	// Items, not Elements: the scope now admits things it cannot afford to PRINT
	// so their ids still resolve, and counting those as "visible" would put the
	// chip back to promising a reach the digest does not carry.
	out.Visible = len(scope.Items)
	for _, n := range scope.Elided {
		out.Elided += n
	}
	// A board already being worked on should not also be nagged about.
	if out.Active == nil {
		out.Drift = DetectDrift(scope)
	}
	return out, nil
}

// describeActive redacts a colleague's run down to the fact of it.
func (s *Service) describeActive(ctx context.Context, p *domain.Principal, run *Run) *ActiveRun {
	view := &ActiveRun{State: run.State, StartedAt: run.CreatedAt, Mine: run.Tenant == p.Sub}
	if view.Mine {
		view.ID = run.ID
		return view
	}
	view.Actor = "Someone"
	if s.users != nil {
		if u, err := s.users.GetBySub(ctx, run.Tenant); err == nil && u != nil && u.DisplayName != "" {
			view.Actor = sanitizeName(u.DisplayName)
		}
	}
	return view
}

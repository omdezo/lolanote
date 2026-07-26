package http

import (
	"net/http"
	"strconv"

	"github.com/labstack/echo/v4"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// The agent's HTTP surface. Handlers stay thin — bind, call the service, shape
// the response — and every authorization decision lives in the service layer
// where it can be tested without a request.
//
// Note what is NOT here: no endpoint accepts ops. The client sends intent
// (create a run) and typed adjustments (rearrange a proposal); the server alone
// decides what that becomes on the board.

// CreateAgentRun admits an organize task and starts it. It returns immediately
// with the durable run; progress arrives over the board's realtime channel and
// can be replayed from the journal.
func (h *Handlers) CreateAgentRun(c echo.Context) error {
	if h.Agent == nil || !h.Agent.Enabled() {
		return domain.ErrUnavailable
	}
	var req agent.CreateRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.Create(c.Request().Context(), principal(c), req)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, run)
}

// GetAgentRun returns authoritative run state, including the proposal awaiting
// a decision.
func (h *Handlers) GetAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	run, err := h.Agent.Get(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// ListAgentRuns returns the caller's recent runs on a board.
func (h *Handlers) ListAgentRuns(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	boardID := c.QueryParam("boardId")
	if boardID == "" {
		return domain.ErrValidation
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	runs, err := h.Agent.ListByBoard(c.Request().Context(), principal(c), boardID, limit)
	if err != nil {
		return err
	}
	if runs == nil {
		runs = []*agent.Run{}
	}
	return c.JSON(http.StatusOK, runs)
}

// AgentRunEvents is the resumable catch-up path: events after ?since=<sequence>.
// Live updates ride the board's WebSocket; this exists so a client that
// reconnects recovers what it missed instead of losing it.
func (h *Handlers) AgentRunEvents(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	since, _ := strconv.ParseInt(c.QueryParam("since"), 10, 64)
	events, err := h.Agent.Events(c.Request().Context(), principal(c), c.Param("id"), since)
	if err != nil {
		return err
	}
	if events == nil {
		events = []*agent.Event{}
	}
	return c.JSON(http.StatusOK, events)
}

// applyBody carries the human's typed edits to a proposal. The closed set of
// adjustment kinds is validated server-side; anything else is ignored.
type applyBody struct {
	Adjustments []agent.Adjustment `json:"adjustments"`
}

// ApplyAgentRun commits a proposed run.
func (h *Handlers) ApplyAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	var body applyBody
	if err := c.Bind(&body); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.Apply(c.Request().Context(), principal(c), c.Param("id"), body.Adjustments)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// DiscardAgentRun throws a proposal away. Nothing was written, so nothing is undone.
func (h *Handlers) DiscardAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	run, err := h.Agent.Discard(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// RefineAgentRun sends a proposed plan back for another pass with the person's
// steer. The run keeps its identity and its cost meter, so what the user sees
// is the price of the conversation, not of its last turn.
func (h *Handlers) RefineAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	var body struct {
		Note string `json:"note"`
	}
	if err := c.Bind(&body); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.Refine(c.Request().Context(), principal(c), c.Param("id"), body.Note)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// CancelAgentRun stops a run. Idempotent: cancelling a finished run is a no-op
// that reports the run's actual state.
func (h *Handlers) CancelAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	run, err := h.Agent.Cancel(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// RevertAgentRun undoes everything a run committed, as one new transaction that
// collaborators see like any other change.
func (h *Handlers) RevertAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	run, err := h.Agent.Revert(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// AgentCapabilities is the honest catalogue: what this deployment's agent can
// and cannot do. The client renders its affordances from this rather than
// hard-coding assumptions that could drift from the server's actual policy.
func (h *Handlers) AgentCapabilities(c echo.Context) error {
	enabled := h.Agent != nil && h.Agent.Enabled()
	budget := agent.DefaultBudget()
	return c.JSON(http.StatusOK, map[string]any{
		"enabled": enabled,
		"can": []string{
			"create boards, columns, notes, to-do lists and links",
			"move, rename and rewrite what is already here",
			"organize a messy board into columns",
			"read nested boards and search your own content",
		},
		// Stated explicitly so the boundary is visible in the API, not only to
		// whoever wrote the policy code.
		"cannot": []string{
			"change sharing or permissions",
			"leave the board it was started on",
			"empty the trash or delete permanently",
			"modify account settings",
			"delete anything without your review",
		},
		"limits": map[string]any{
			"maxActions": budget.MaxActions,
			"maxSteps":   budget.MaxSteps,
		},
	})
}

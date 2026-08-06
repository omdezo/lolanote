package http

import (
	"net/http"
	"strconv"
	"strings"
	"time"

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
	// ?wait=30s turns fire-and-forget into a call a machine can make.
	//
	// A browser holds a WebSocket and wants the 202 immediately. Everything else
	// — a cron job, a chat command, an MCP host — had to poll an events endpoint
	// with a cursor to find out whether the thing it asked for happened. The run
	// object is already the complete answer, so this changes nothing about the
	// execution model and only lets the caller wait for it.
	if raw := c.QueryParam("wait"); raw != "" {
		if d, perr := time.ParseDuration(raw); perr == nil && d > 0 {
			settled, werr := h.Agent.AwaitSettled(c.Request().Context(), principal(c), run.ID, d)
			if werr == nil && settled != nil {
				return c.JSON(http.StatusOK, settled)
			}
		}
	}
	return c.JSON(http.StatusAccepted, run)
}

// ClearAgentHistory erases the caller's own runs and journal.
//
// Everything typed into the composer was retained verbatim and indefinitely,
// including the plans the person explicitly discarded, with no way to see or
// clear it. The retention window and this button are the two halves of the
// answer; agent.HistoryRetention is the single number both are stated from.
func (h *Handlers) ClearAgentHistory(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	if err := h.Agent.ClearHistory(c.Request().Context(), principal(c)); err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]any{
		"cleared":       true,
		"retentionDays": int(agent.HistoryRetention.Hours() / 24),
	})
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

// ListAgentRuns returns recent runs: on one board, or across the account.
//
// The account-wide half is new. This endpoint used to return ErrValidation when
// boardId was absent — so "what has the assistant done for me lately" and
// "which of my boards has a proposal waiting" had no read path at all, while
// the spend cap worked around the same gap by calling the board query with an
// EMPTY board id and summing 500 documents in Go.
func (h *Handlers) ListAgentRuns(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	if boardID := c.QueryParam("boardId"); boardID != "" {
		runs, err := h.Agent.ListByBoard(c.Request().Context(), principal(c), boardID, limit)
		if err != nil {
			return err
		}
		if runs == nil {
			runs = []*agent.Run{}
		}
		return c.JSON(http.StatusOK, runs)
	}
	runs, err := h.Agent.AccountRuns(c.Request().Context(), principal(c), runFilterFrom(c), limit)
	if err != nil {
		return err
	}
	if runs == nil {
		runs = []*agent.Run{}
	}
	return c.JSON(http.StatusOK, runs)
}

// runFilterFrom binds ?since= and ?state= — the two narrowings every caller of
// the account-wide query wants and neither of which is worth a body.
func runFilterFrom(c echo.Context) agent.RunFilter {
	f := agent.RunFilter{}
	if raw := c.QueryParam("since"); raw != "" {
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			f.Since = t.UTC()
		}
	}
	for _, s := range strings.Split(c.QueryParam("state"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			f.States = append(f.States, agent.RunState(s))
		}
	}
	return f
}

// AgentUsage answers "what am I spending" as one grouped query.
func (h *Handlers) AgentUsage(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	roll, err := h.Agent.AccountUsage(c.Request().Context(), principal(c), runFilterFrom(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, roll)
}

// BoardAgentRuns is the OWNER's view of what the assistant has done here,
// whoever ran it — the public half of each run and none of anybody's words.
//
// Per-run revert is this product's central trust promise, and it was
// unavailable to the person with the most at stake: an owner who watched forty
// elements appear from an editor's run had to ask that editor to undo it.
func (h *Handlers) BoardAgentRuns(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	views, err := h.Agent.BoardRuns(c.Request().Context(), principal(c), c.Param("id"), limit)
	if err != nil {
		return err
	}
	if views == nil {
		views = []agent.RunPublicView{}
	}
	return c.JSON(http.StatusOK, views)
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
	// VariantIndex is which offered SHAPE the person picked in the review's
	// segmented control. Absent means the first, which is what a plan with no
	// alternatives carries and what every client sent before this existed.
	VariantIndex *int `json:"variantIndex,omitempty"`
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
	run, err := h.Agent.Apply(c.Request().Context(), principal(c), c.Param("id"), body.Adjustments, body.VariantIndex)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// DiscardAgentRun throws a proposal away. Nothing was written, so nothing is undone.
//
// It accepts the same adjustment body Apply does, and an empty body still works
// — the old client posted nothing. The adjustments are not applied to anything;
// they are recorded, because "I dropped four rows, reparented two, and then gave
// up" names which capabilities failed with typed targets, and it was being
// thrown away in the same set() call that made the request.
func (h *Handlers) DiscardAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	var body applyBody
	if err := c.Bind(&body); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.Discard(c.Request().Context(), principal(c), c.Param("id"), body.Adjustments)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// AnswerAgentRule records whether the person wanted the rule a run proposed.
//
// Separate from the board write that saves the rule text, deliberately. The
// text is board content and belongs on the ordinary transaction path where it
// is undoable and syncs; the ANSWER is a fact about the run, and it has to
// survive the person changing their mind about the write.
func (h *Handlers) AnswerAgentRule(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	var body struct {
		Accepted bool `json:"accepted"`
	}
	if err := c.Bind(&body); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.AnswerRule(c.Request().Context(), principal(c), c.Param("id"), body.Accepted)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// AuditAgentBoard answers "what has the AI changed on this board".
func (h *Handlers) AuditAgentBoard(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	entries, err := h.Agent.Audit(c.Request().Context(), principal(c), c.Param("id"), limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, entries)
}

// BoardAgentState answers, in one read on board open: how much of this board
// the agent can see, whether it wants tidying, and whether a run is already
// under way. Costs no tokens.
//
// It replaces a drift-only endpoint. The three facts come from one scope
// compile and are only useful together — asking for drift alone is how the
// composer ended up confidently claiming a reach the agent does not have.
func (h *Handlers) BoardAgentState(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	state, err := h.Agent.BoardStateFor(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, state)
}

// RefineAgentRun sends a proposed plan back for another pass with the person's
// steer. The run keeps its identity and its cost meter, so what the user sees
// is the price of the conversation, not of its last turn.
func (h *Handlers) RefineAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	// The typed edits ride along with the note. They are the same wire shape the
	// apply endpoint already accepts, because they are the same decisions — a
	// refine that arrived without them silently threw away every row the person
	// had just dropped by hand.
	var body refineBody
	if err := c.Bind(&body); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.Refine(c.Request().Context(), principal(c), c.Param("id"), body.Note, body.Adjustments)
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

// revertRequest optionally narrows a revert to specific elements. An absent or
// empty list means the whole run, which is what every existing client sends.
type revertRequest struct {
	ElementIDs []string `json:"elementIds"`
}

// RevertAgentRun undoes a run — all of it, or just the elements named — as one
// new transaction that collaborators see like any other change.
func (h *Handlers) RevertAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	// A revert with no body is the whole run; a malformed body is not silently
	// promoted into one, because "undo everything" is the larger action.
	var req revertRequest
	if c.Request().ContentLength > 0 {
		if err := c.Bind(&req); err != nil {
			return domain.ErrValidation
		}
	}
	run, err := h.Agent.RevertOne(c.Request().Context(), principal(c), c.Param("id"), req.ElementIDs)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// noteRequest carries a free-text steer or refinement.
type noteRequest struct {
	Note string `json:"note"`
}

// refineBody is a note plus the hand edits already made to the proposal. Both
// halves are one instruction: "drop these three, and also make it four columns".
type refineBody struct {
	Note        string             `json:"note"`
	Adjustments []agent.Adjustment `json:"adjustments"`
}

// SteerAgentRun hands a correction to a run that is still working. The note is
// queued and picked up between model turns.
func (h *Handlers) SteerAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	var req noteRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	run, err := h.Agent.Steer(c.Request().Context(), principal(c), c.Param("id"), req.Note)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// RetryAgentRun starts a fresh run from a finished one's request, so recovering
// from a transient failure is a click rather than retyping the whole ask.
func (h *Handlers) RetryAgentRun(c echo.Context) error {
	if h.Agent == nil {
		return domain.ErrUnavailable
	}
	run, err := h.Agent.Retry(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, run)
}

// ContinueAgentRun picks up where a run that ran out of room left off. The new
// run's request is composed on the server from the prior run's own unmet list —
// the client sends nothing but the id, because a client-supplied "keep going" is
// the context-free prompt this exists to replace.
func (h *Handlers) ContinueAgentRun(c echo.Context) error {
	if h.Agent == nil || !h.Agent.Enabled() {
		return domain.ErrUnavailable
	}
	run, err := h.Agent.Continue(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusAccepted, run)
}

// AgentCapabilities is the honest catalogue: what this deployment's agent can
// and cannot do. The client renders its affordances from this rather than
// hard-coding assumptions that could drift from the server's actual policy.
func (h *Handlers) AgentCapabilities(c echo.Context) error {
	enabled := h.Agent != nil && h.Agent.Enabled()
	budget := agent.DefaultBudget()
	var spent, cap float64
	// Configured and actually able to answer are different questions, and the
	// composer used to only ask the first. A rotated key left it accepting
	// intents it could not serve, one failed run at a time, with the honest
	// message ("the agent is unavailable right now") available nowhere.
	providerHealthy := false
	// Who actually receives the board's contents. Read from the configured
	// provider, never from a constant: the composer's disclosure names a real
	// third party, and a name compiled into the client becomes a false claim
	// about somebody's data on the first deployment that switches keys.
	processing := map[string]any{}
	if enabled {
		spent, cap = h.Agent.SpentToday(c.Request().Context(), principal(c).Sub)
		providerHealthy = h.Agent.ProviderHealthy(c.Request().Context())
		processing["provider"] = h.Agent.ProviderName()
		processing["model"] = h.Agent.ModelName()
	}
	return c.JSON(http.StatusOK, map[string]any{
		"enabled":         enabled,
		"providerHealthy": providerHealthy,
		"processing":      processing,
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
			// The ceiling on what one PASS can spend, and how many passes a run
			// may have. A revision resets the budget, so the honest worst case
			// for a run is the product — the composer showed the single-pass
			// figure and understated the ceiling by up to six times.
			"maxCostUsd": budget.MaxCostUSD,
			"maxPasses":  agent.MaxPassesPerRun(),
		},
		// What is left of today's allowance. Shown so the ceiling is something a
		// person can plan around rather than discover by being refused.
		"budget": map[string]any{"spentTodayUsd": spent, "dailyCapUsd": cap},
	})
}

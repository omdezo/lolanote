package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// The refusals the agent authors have to survive the transport layer.
//
// They did not. The conflict and forbidden arms of the error switch answered
// with the constants "conflict" and "forbidden", so the most careful sentence in
// the backend — "Sara started one 3 minutes ago — it will finish shortly" —
// reached the browser as one word, and the client could do no better than guess
// which conflict it was looking at. Six of the agent's eight refusals took that
// door; the one people found legible (the daily budget) took the ErrValidation
// door purely by accident of which sentinel it wraps.
//
// This asserts on the RENDERED body, not on the handler's return value: the
// whole defect lived between the two.
func replyWith(err error) *httptest.ResponseRecorder {
	e := echo.New()
	e.HideBanner, e.HidePort = true, true
	e.HTTPErrorHandler = errorHandler(zap.NewNop())
	e.GET("/probe", func(echo.Context) error { return err })
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/probe", nil))
	return rec
}

func bodyMessage(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var env struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body %s is not the error envelope: %v", rec.Body, err)
	}
	return env.Error.Message
}

func TestErrorBoundary_AuthoredRefusalsSurviveTheRoundTrip(t *testing.T) {
	// Every sentinel the agent package exports, with the status its base
	// sentinel maps to. A new one added without a row here is the thing this
	// table exists to catch — it will simply not be covered, so the list is
	// deliberately exhaustive rather than illustrative.
	cases := []struct {
		name string
		err  error
		want int
	}{
		{"not running", agent.ErrNotRunning, http.StatusConflict},
		{"run active", agent.ErrRunActive, http.StatusConflict},
		{"stale plan", agent.ErrStalePlan, http.StatusConflict},
		{"not proposed", agent.ErrNotProposed, http.StatusConflict},
		{"destructive needs review", agent.ErrDestructiveNeedsReview, http.StatusForbidden},
		{"disabled", agent.ErrDisabled, http.StatusNotImplemented},
		{"budget", agent.ErrBudget, http.StatusBadRequest},
		{"no intent", agent.ErrNoIntent, http.StatusBadRequest},
		{"nothing left", agent.ErrNothingLeft, http.StatusBadRequest},
		{"nothing to do", agent.ErrNothingToDo, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := replyWith(tc.err)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			if got := bodyMessage(t, rec); got != tc.err.Error() {
				t.Errorf("body message = %q, want the authored text %q", got, tc.err.Error())
			}
		})
	}
}

// The most careful refusal in the package is composed with fmt.Errorf around its
// sentinel, so the marker interface is reached by unwrapping. What must arrive is
// the COMPOSED text — the name and the age — not the sentinel's bare prefix.
func TestErrorBoundary_ComposedRefusalKeepsItsDetail(t *testing.T) {
	err := fmt.Errorf("%w: %s started one %s — it will finish shortly",
		agent.ErrRunActive, "Sara", "3 minutes ago")
	rec := replyWith(err)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409", rec.Code)
	}
	got := bodyMessage(t, rec)
	if got != err.Error() {
		t.Fatalf("body message = %q, want %q", got, err.Error())
	}
	if got == "conflict" {
		t.Fatal("the composed refusal collapsed back to the generic word")
	}
}

// The other half of the contract: only AUTHORED errors are passed through. A
// driver error dressed as a conflict — a duplicate key, a lock timeout — must
// still answer with the generic word, or the switch becomes a way to leak
// storage internals to anyone who can provoke one.
func TestErrorBoundary_UntypedErrorsStillGetTheGenericWord(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
		code int
	}{
		{"bare conflict", domain.ErrConflict, "conflict", http.StatusConflict},
		{"bare forbidden", domain.ErrForbidden, "forbidden", http.StatusForbidden},
		{
			"driver detail wrapped as a conflict",
			fmt.Errorf("%w: E11000 duplicate key on index agent_runs_board_active", domain.ErrConflict),
			"conflict", http.StatusConflict,
		},
		{
			"driver detail wrapped as forbidden",
			fmt.Errorf("%w: acl walk aborted at depth 64 for element 65f0", domain.ErrForbidden),
			"forbidden", http.StatusForbidden,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := replyWith(tc.err)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d", rec.Code, tc.code)
			}
			if got := bodyMessage(t, rec); got != tc.want {
				t.Errorf("body message = %q, want the generic %q", got, tc.want)
			}
		})
	}
}

// A sanity check on the marker itself: errors.As must see through fmt.Errorf's
// wrapper, which is the only reason the composed case above can work.
func TestErrorBoundary_MarkerIsReachedByUnwrapping(t *testing.T) {
	var a authoredError
	if !errors.As(fmt.Errorf("%w: detail", agent.ErrStalePlan), &a) {
		t.Fatal("a composed agent refusal is not recognised as authored")
	}
	if errors.As(domain.ErrConflict, &a) {
		t.Fatal("a bare domain sentinel claims to carry authored text")
	}
}

package http

import (
	"context"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/websocket"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"qomranote/backend/internal/auth"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/realtime"
	"qomranote/backend/internal/service"
	"qomranote/backend/internal/storage"
)

// Handlers bundles every dependency the routes need. Handlers stay thin:
// bind → call service → shape response.
type Handlers struct {
	Users         *service.UserService
	Account       *service.AccountService
	Boards        *service.BoardService
	Elements      *service.ElementService
	Txns          *service.TransactionService
	Share         *service.ShareService
	Uploads       *service.UploadService
	Links         *service.LinkService
	Comments      *service.CommentService
	Labels        *service.LabelService
	Notifications domain.NotificationRepository
	Access        *service.AccessResolver
	Hub           *realtime.Hub
	Verifier      *auth.Verifier
	Tickets       *auth.TicketStore
	Local         *storage.LocalPresigner // nil when the R2 driver is active
	ReadyCheck    func(ctx context.Context) error
	Log           *zap.Logger
}

// ---- system ----

func (h *Handlers) Health(c echo.Context) error {
	return c.JSON(http.StatusOK, map[string]string{"status": "ok"})
}

// Ready reports whether the dependencies (Mongo, Keycloak) actually answer —
// compose healthchecks and orchestrators gate traffic on this.
func (h *Handlers) Ready(c echo.Context) error {
	if h.ReadyCheck != nil {
		if err := h.ReadyCheck(c.Request().Context()); err != nil {
			return c.JSON(http.StatusServiceUnavailable, map[string]string{"status": "degraded", "error": err.Error()})
		}
	}
	return c.JSON(http.StatusOK, map[string]string{"status": "ready"})
}

// ---- bootstrap & identity ----

func (h *Handlers) Me(c echo.Context) error {
	u, err := h.Users.Bootstrap(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, u)
}

func (h *Handlers) LookupUser(c echo.Context) error {
	email := c.QueryParam("email")
	if email == "" {
		return domain.ErrValidation
	}
	sub, name, err := h.Users.LookupByEmail(c.Request().Context(), email)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]string{"sub": sub, "name": name, "email": email})
}

type resolveUsersRequest struct {
	Subs []string `json:"subs"`
}

// ResolveUsers batch-maps subs to display names (comment authors, assignee
// pickers, mention suggestions).
func (h *Handlers) ResolveUsers(c echo.Context) error {
	var req resolveUsersRequest
	if err := c.Bind(&req); err != nil || len(req.Subs) == 0 {
		return domain.ErrValidation
	}
	return c.JSON(http.StatusOK, h.Users.ResolveNames(c.Request().Context(), req.Subs))
}

type moveAcrossRequest struct {
	IDs           []string `json:"ids"`
	TargetBoardID string   `json:"targetBoardId"`
}

// MoveElements reparents elements into another board's Unsorted tray — the
// drag-onto-breadcrumb / board-tile gesture.
func (h *Handlers) MoveElements(c echo.Context) error {
	var req moveAcrossRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	if err := h.Txns.MoveAcrossBoards(c.Request().Context(), principal(c), req.IDs, req.TargetBoardID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- boards ----

func (h *Handlers) MyBoards(c echo.Context) error {
	boards, err := h.Boards.Boards(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(boards))
}

func (h *Handlers) GetBoard(c echo.Context) error {
	view, err := h.Boards.Get(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, view)
}

func (h *Handlers) BoardChildren(c echo.Context) error {
	els, err := h.Boards.Children(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(els))
}

func (h *Handlers) BoardChildStats(c echo.Context) error {
	stats, err := h.Boards.ChildBoardStats(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, stats)
}

func (h *Handlers) BoardUnsorted(c echo.Context) error {
	els, err := h.Boards.Unsorted(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(els))
}

func (h *Handlers) BoardTransactions(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	txns, err := h.Txns.History(c.Request().Context(), principal(c), c.Param("id"), limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(txns))
}

func (h *Handlers) Templates(c echo.Context) error {
	boards, err := h.Boards.Templates(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(boards))
}

func (h *Handlers) ExportBoard(c echo.Context) error {
	format := c.QueryParam("format")
	if format == "" {
		format = "markdown"
	}
	body, contentType, err := h.Boards.Export(c.Request().Context(), principal(c), c.Param("id"), format)
	if err != nil {
		return err
	}
	return c.Blob(http.StatusOK, contentType, []byte(body))
}

// ---- search ----

func (h *Handlers) Search(c echo.Context) error {
	limit, _ := strconv.Atoi(c.QueryParam("limit"))
	els, err := h.Boards.Search(c.Request().Context(), principal(c), c.QueryParam("q"), limit)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(els))
}

// ---- transactions ----

type txnRequest struct {
	BoardID  string      `json:"boardId"`
	ClientID string      `json:"clientId"`
	Ops      []domain.Op `json:"ops"`
}

func (h *Handlers) ApplyTransaction(c echo.Context) error {
	var req txnRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	txn, err := h.Txns.Apply(c.Request().Context(), principal(c), req.BoardID, req.ClientID, req.Ops)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, txn)
}

// ---- websocket ----

var upgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// Browser origin enforcement happens at the CORS/proxy layer; tokens
	// gate the actual access below.
	CheckOrigin: func(*http.Request) bool { return true },
}

// IssueRealtimeTicket exchanges the caller's verified token for a short-lived
// single-use ticket used at the WebSocket handshake (keeps the bearer out of
// the WS query string and access logs).
func (h *Handlers) IssueRealtimeTicket(c echo.Context) error {
	ticket := h.Tickets.Issue(principal(c), time.Now())
	return c.JSON(http.StatusOK, map[string]string{"ticket": ticket})
}

func (h *Handlers) WebSocket(c echo.Context) error {
	ctx := c.Request().Context()

	// Ticket-only handshake: bearer tokens never appear in WS URLs (they
	// would land in proxy/access logs). Tooling obtains a ticket the same
	// way the app does — POST /realtime/ticket with its bearer.
	ticket := c.QueryParam("ticket")
	if ticket == "" {
		return echo.NewHTTPError(http.StatusUnauthorized, "missing ticket")
	}
	p, ok := h.Tickets.Redeem(ticket, time.Now())
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "invalid or expired ticket")
	}
	if st := c.QueryParam("shareToken"); st != "" {
		p.ShareToken = st
	}
	boardID := c.QueryParam("board")
	clientID := c.QueryParam("clientId")
	if boardID == "" || clientID == "" {
		return domain.ErrValidation
	}
	if _, _, err := h.Access.RequireView(ctx, boardID, p); err != nil {
		return err
	}
	// Privacy: users who turned presence off join invisibly — they receive
	// every broadcast but never appear in presence, cursors, or editing.
	invisible := false
	if p.Sub != "" && h.Account != nil {
		if settings, err := h.Account.Settings(ctx, p); err == nil {
			invisible = !settings.Privacy.ShowPresence
		}
	}
	conn, err := upgrader.Upgrade(c.Response(), c.Request(), nil)
	if err != nil {
		return err
	}
	realtime.NewClient(h.Hub, conn, clientID, boardID, p, invisible)
	return nil
}

// ---- local blob driver (dev fallback for R2, same presign contract) ----

func (h *Handlers) BlobPut(c echo.Context) error {
	if h.Local == nil {
		return echo.NewHTTPError(http.StatusNotFound, "local storage driver not active")
	}
	key := c.Param("*")
	if err := h.Local.Save(key, c.Request().Body); err != nil {
		return err
	}
	return c.NoContent(http.StatusOK)
}

func (h *Handlers) BlobGet(c echo.Context) error {
	if h.Local == nil {
		return echo.NewHTTPError(http.StatusNotFound, "local storage driver not active")
	}
	path, err := h.Local.Path(c.Param("*"))
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return domain.ErrNotFound
	}
	return c.File(path)
}

// orEmpty keeps JSON arrays arrays (never null) for empty slices.
func orEmpty[T any](in []T) []T {
	if in == nil {
		return []T{}
	}
	return in
}

package http

import (
	"github.com/labstack/echo/v4"
	echomw "github.com/labstack/echo/v4/middleware"
)

// registerRoutes declares the full /api/v1 surface (see PLAN.md §3.3).
func registerRoutes(e *echo.Echo, h *Handlers) {
	// System probes — no auth. readyz actually pings Mongo + Keycloak.
	e.GET("/healthz", h.Health)
	e.GET("/readyz", h.Ready)
	// Prometheus scrape target. NETWORK-GATED IN DEPLOY, NOT ROUTE-GATED: it
	// carries no auth of its own by design, so the deployment must keep it off
	// the public reverse proxy and reachable only from the scrape network
	// (§II.0.4). Do not "fix" this by bolting a token onto the route.
	e.GET("/metrics", MetricsHandler())

	// Local-driver blob store: GET is public (unguessable ObjectId keys feed
	// <img src>, which cannot send headers); PUT requires a valid token.
	e.GET("/api/v1/blob/*", h.BlobGet)
	e.PUT("/api/v1/blob/*", h.BlobPut, authMiddleware(h.Verifier, true))

	// Optional-auth group: an anonymous caller with a valid share token can
	// read a board (§6.1 mechanism 4). The ACL resolver is the real gate —
	// no token AND no matching share token still yields 403.
	optional := e.Group("/api/v1", authMiddleware(h.Verifier, false))
	optional.GET("/shared/:token", h.ResolveSharedLink)
	optional.GET("/boards/:id", h.GetBoard)
	optional.GET("/boards/:id/children", h.BoardChildren)
	optional.GET("/boards/:id/unsorted", h.BoardUnsorted)
	optional.GET("/boards/:id/childstats", h.BoardChildStats)
	optional.GET("/boards/:id/export", h.ExportBoard)
	// The stable address of an uploaded file. Optional-auth because a share-link
	// holder is legitimately allowed to see a board's pictures; UploadService
	// resolves who may read what and signs a short-lived URL per request.
	optional.GET("/attachments/:id/blob", h.AttachmentBlob)

	api := e.Group("/api/v1", authMiddleware(h.Verifier, true))

	// Bootstrap & identity
	api.GET("/me", h.Me)
	api.GET("/users/lookup", h.LookupUser)
	api.POST("/users/resolve", h.ResolveUsers)

	// Account & settings (the Settings dialog surface)
	api.PATCH("/me", h.UpdateMe)
	api.GET("/me/settings", h.GetSettings)
	api.PATCH("/me/settings", h.UpdateSettings)
	api.POST("/me/password", h.ChangePassword)
	api.GET("/me/export", h.ExportMyData)
	api.DELETE("/me", h.DeleteMe)

	// Boards (authed-only reads)
	api.GET("/boards", h.MyBoards)
	api.GET("/boards/:id/transactions", h.BoardTransactions)
	api.GET("/templates", h.Templates)
	api.POST("/templates/:id/use", h.UseTemplate)

	// Elements
	api.POST("/elements", h.CreateElement)
	api.GET("/elements/:id", h.GetElement)
	api.PATCH("/elements/:id", h.PatchElement)
	api.POST("/elements/:id/duplicate", h.DuplicateElement)
	api.POST("/elements/:id/clone", h.ConvertToClone)
	api.GET("/elements/:id/clones", h.CloneInstances)
	api.POST("/elements/:id/labels", h.AttachLabel)
	api.DELETE("/elements/:id/labels/:labelId", h.DetachLabel)

	// Transactions — THE write path.
	//
	// Tighter than the global 64 MB, which exists for local-driver uploads and
	// has nothing to do with edits. A journal row is one Mongo document and
	// Mongo's hard ceiling is 16 MB, so a 64 MB batch was a request the router
	// accepted and the database could not store: every op applied, then the
	// journal insert failed, leaving changes standing with no inverse and no
	// broadcast. The service refuses at 8 MB of ops; this refuses earlier and
	// cheaper, before a body that large is even parsed.
	api.POST("/transactions", h.ApplyTransaction, echomw.BodyLimit("12M"))
	// Cross-board move (drag onto a breadcrumb or board tile → Unsorted)
	api.POST("/elements/move", h.MoveElements)

	// Trash
	api.GET("/trash", h.ListTrash)
	api.POST("/trash/:id/restore", h.RestoreTrash)
	api.DELETE("/trash/:id", h.DeleteTrashItem)
	api.DELETE("/trash", h.EmptyTrash)

	// Uploads (presign → PUT to storage → complete)
	api.POST("/attachments/presign", h.PresignUpload)
	api.POST("/attachments/:id/complete", h.CompleteUpload)

	// Link metadata
	api.POST("/links/resolve", h.ResolveLink)

	// Sharing
	api.GET("/boards/:id/share", h.ShareState)
	api.POST("/boards/:id/share/editors", h.InviteEditor)
	api.DELETE("/boards/:id/share/editors/:sub", h.RemoveEditor)
	api.POST("/boards/:id/share/owner/:sub", h.TransferBoardOwnership)
	api.POST("/boards/:id/share/link", h.CreateShareLink)
	api.DELETE("/boards/:id/share/link/:kind", h.RevokeShareLink)
	// Who may automate here is the same decision as who may edit here, so it
	// lives on the same dialog and the same owner gate.
	api.PUT("/boards/:id/share/agent-policy", h.SetBoardAgentPolicy)

	// Search
	api.GET("/search", h.Search)

	// Comments
	api.GET("/threads/:id/comments", h.ListComments)
	api.POST("/threads/:id/comments", h.AddComment)
	api.PATCH("/comments/:id", h.EditComment)
	api.POST("/comments/:id/reactions", h.ReactToComment)

	// Labels
	api.GET("/labels", h.ListLabels)
	api.POST("/labels", h.CreateLabel)
	api.PATCH("/labels/:id", h.UpdateLabel)
	api.DELETE("/labels/:id", h.DeleteLabel)

	// Notifications
	api.GET("/notifications", h.ListNotifications)
	api.POST("/notifications/read", h.MarkNotificationsRead)

	// AI agent. Runs are admitted, watched, and decided on here; the run's own
	// writes go through POST /transactions like everyone else's.
	ai := api.Group("/agent")
	ai.GET("/capabilities", h.AgentCapabilities)
	// A run costs money; a GET does not. Its own, much tighter bucket.
	ai.POST("/runs", h.CreateAgentRun, agentRunLimiter())
	ai.GET("/runs", h.ListAgentRuns)
	ai.GET("/runs/:id", h.GetAgentRun)
	ai.GET("/runs/:id/events", h.AgentRunEvents) // ?since=<sequence> — resumable
	ai.POST("/runs/:id/apply", h.ApplyAgentRun)
	ai.GET("/boards/:id/audit", h.AuditAgentBoard)
	// The board owner's view of runs anyone made here, public halves only.
	ai.GET("/boards/:id/runs", h.BoardAgentRuns)
	// What the assistant has cost this account, grouped in the database.
	ai.GET("/usage", h.AgentUsage)
	// One read on board open: the agent's reach here, any drift, any live run.
	ai.GET("/boards/:id/state", h.BoardAgentState)
	ai.POST("/runs/:id/refine", h.RefineAgentRun)
	ai.POST("/runs/:id/discard", h.DiscardAgentRun)
	ai.POST("/runs/:id/cancel", h.CancelAgentRun)
	ai.POST("/runs/:id/revert", h.RevertAgentRun)
	ai.POST("/runs/:id/retry", h.RetryAgentRun)
	// Keep going from what a run left undone. Its intent is composed server-side
	// from that run's own unmet list.
	ai.POST("/runs/:id/continue", h.ContinueAgentRun)
	ai.POST("/runs/:id/steer", h.SteerAgentRun)
	// Whether the person wanted the rule the run proposed. The card has been
	// asking since it shipped and recording the answer nowhere.
	ai.POST("/runs/:id/rule", h.AnswerAgentRule)
	// The privacy tab's other half: agent history is kept for a bounded window,
	// and this is how a person ends it early.
	ai.DELETE("/history", h.ClearAgentHistory)

	// Realtime — the client first exchanges its bearer for a single-use
	// ticket, then connects with ?ticket=… (keeps tokens out of WS URLs/logs).
	api.POST("/realtime/ticket", h.IssueRealtimeTicket)
	e.GET("/ws", h.WebSocket)
}

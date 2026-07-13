package http

import (
	"github.com/labstack/echo/v4"
)

// registerRoutes declares the full /api/v1 surface (see PLAN.md §3.3).
func registerRoutes(e *echo.Echo, h *Handlers) {
	// System probes — no auth.
	e.GET("/healthz", h.Health)
	e.GET("/readyz", h.Health)

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

	api := e.Group("/api/v1", authMiddleware(h.Verifier, true))

	// Bootstrap & identity
	api.GET("/me", h.Me)
	api.GET("/users/lookup", h.LookupUser)

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

	// Transactions — THE write path
	api.POST("/transactions", h.ApplyTransaction)

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
	api.POST("/boards/:id/share/link", h.CreateShareLink)
	api.DELETE("/boards/:id/share/link/:kind", h.RevokeShareLink)

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

	// Realtime — the client first exchanges its bearer for a single-use
	// ticket, then connects with ?ticket=… (keeps tokens out of WS URLs/logs).
	api.POST("/realtime/ticket", h.IssueRealtimeTicket)
	e.GET("/ws", h.WebSocket)
}

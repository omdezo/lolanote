package http

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/service"
)

// ---- uploads (presigned direct-to-storage, §9.10) ----

type presignRequest struct {
	Filename    string `json:"filename"`
	ContentType string `json:"contentType"`
	FileSize    int64  `json:"fileSize"`
}

func (h *Handlers) PresignUpload(c echo.Context) error {
	var req presignRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	res, err := h.Uploads.Presign(c.Request().Context(), principal(c), req.Filename, req.ContentType, req.FileSize)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, res)
}

func (h *Handlers) CompleteUpload(c echo.Context) error {
	att, err := h.Uploads.Complete(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, att)
}

// ---- links (§4.4) ----

type resolveLinkRequest struct {
	URL string `json:"url"`
}

func (h *Handlers) ResolveLink(c echo.Context) error {
	var req resolveLinkRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	meta, err := h.Links.Resolve(c.Request().Context(), req.URL)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, meta)
}

// ---- sharing (§6.1) ----

func (h *Handlers) ShareState(c echo.Context) error {
	st, err := h.Share.State(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, st)
}

type inviteRequest struct {
	Email string `json:"email"`
}

func (h *Handlers) InviteEditor(c echo.Context) error {
	var req inviteRequest
	if err := c.Bind(&req); err != nil || req.Email == "" {
		return domain.ErrValidation
	}
	st, err := h.Share.InviteEditor(c.Request().Context(), principal(c), c.Param("id"), req.Email)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, st)
}

func (h *Handlers) RemoveEditor(c echo.Context) error {
	st, err := h.Share.RemoveEditor(c.Request().Context(), principal(c), c.Param("id"), c.Param("sub"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, st)
}

func (h *Handlers) CreateShareLink(c echo.Context) error {
	var opts service.LinkOptions
	if err := c.Bind(&opts); err != nil {
		return domain.ErrValidation
	}
	st, err := h.Share.CreateLink(c.Request().Context(), principal(c), c.Param("id"), opts)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, st)
}

func (h *Handlers) RevokeShareLink(c echo.Context) error {
	st, err := h.Share.RevokeLink(c.Request().Context(), principal(c), c.Param("id"), c.Param("kind"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, st)
}

// ResolveSharedLink is the public entry point for /shared/:token URLs.
// Password-protected view links pass the password via header.
func (h *Handlers) ResolveSharedLink(c echo.Context) error {
	board, kind, err := h.Share.ResolveToken(c.Request().Context(), c.Param("token"), c.Request().Header.Get("X-Share-Password"))
	if err != nil {
		return err
	}
	welcome := ""
	if board.ACL != nil && board.ACL.ViewLink != nil {
		welcome = board.ACL.ViewLink.WelcomeMessage
	}
	return c.JSON(http.StatusOK, map[string]any{
		"boardId": board.ID, "title": board.Title(), "kind": kind, "welcomeMessage": welcome,
	})
}

// ---- comments (§4.17) ----

func (h *Handlers) ListComments(c echo.Context) error {
	list, err := h.Comments.List(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(list))
}

type commentRequest struct {
	Body     string   `json:"body"`
	Mentions []string `json:"mentions"`
}

func (h *Handlers) AddComment(c echo.Context) error {
	var req commentRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	comment, err := h.Comments.Add(c.Request().Context(), principal(c), c.Param("id"), req.Body, req.Mentions)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, comment)
}

func (h *Handlers) EditComment(c echo.Context) error {
	var req commentRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	comment, err := h.Comments.Edit(c.Request().Context(), principal(c), c.Param("id"), req.Body)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, comment)
}

type reactionRequest struct {
	Emoji string `json:"emoji"`
}

func (h *Handlers) ReactToComment(c echo.Context) error {
	var req reactionRequest
	if err := c.Bind(&req); err != nil || req.Emoji == "" {
		return domain.ErrValidation
	}
	comment, err := h.Comments.React(c.Request().Context(), principal(c), c.Param("id"), req.Emoji)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, comment)
}

// ---- labels (§4.18) ----

func (h *Handlers) ListLabels(c echo.Context) error {
	list, err := h.Labels.List(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(list))
}

type labelRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

func (h *Handlers) CreateLabel(c echo.Context) error {
	var req labelRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	label, err := h.Labels.Create(c.Request().Context(), principal(c), req.Name, req.Color)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, label)
}

func (h *Handlers) UpdateLabel(c echo.Context) error {
	var req labelRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	label, err := h.Labels.Update(c.Request().Context(), principal(c), c.Param("id"), req.Name, req.Color)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, label)
}

func (h *Handlers) DeleteLabel(c echo.Context) error {
	if err := h.Labels.Delete(c.Request().Context(), principal(c), c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

type attachLabelRequest struct {
	LabelID string `json:"labelId"`
}

func (h *Handlers) AttachLabel(c echo.Context) error {
	var req attachLabelRequest
	if err := c.Bind(&req); err != nil || req.LabelID == "" {
		return domain.ErrValidation
	}
	if err := h.Labels.Attach(c.Request().Context(), principal(c), c.Param("id"), req.LabelID); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handlers) DetachLabel(c echo.Context) error {
	if err := h.Labels.Detach(c.Request().Context(), principal(c), c.Param("id"), c.Param("labelId")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

// ---- notifications (§6.2) ----

func (h *Handlers) ListNotifications(c echo.Context) error {
	unread := c.QueryParam("unread") == "true"
	list, err := h.Notifications.ListByUser(c.Request().Context(), principal(c).Sub, unread, 50)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(list))
}

type markReadRequest struct {
	IDs []string `json:"ids"`
}

func (h *Handlers) MarkNotificationsRead(c echo.Context) error {
	var req markReadRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	if err := h.Notifications.MarkRead(c.Request().Context(), principal(c).Sub, req.IDs); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

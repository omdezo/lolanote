package http

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"qomranote/backend/internal/domain"
)

// Element REST endpoints. Creation and patching flow through the transaction
// service so every mutation is recorded and broadcast — REST is just a
// convenience wrapper around a one-op transaction.

type createElementRequest struct {
	BoardID  string         `json:"boardId"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type"`
	Location map[string]any `json:"location"`
	Content  map[string]any `json:"content"`
	ClientID string         `json:"clientId,omitempty"`
}

func (h *Handlers) CreateElement(c echo.Context) error {
	var req createElementRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	op := domain.Op{
		ElementID: req.ID,
		Action:    domain.ActionCreate,
		Changes: domain.Content{
			"type":     req.Type,
			"location": req.Location,
			"content":  req.Content,
		},
		UndoChanges: domain.Content{},
	}
	txn, err := h.Txns.Apply(c.Request().Context(), principal(c), req.BoardID, req.ClientID, []domain.Op{op})
	if err != nil {
		return err
	}
	el, err := h.Elements.Get(c.Request().Context(), principal(c), txn.Ops[0].ElementID)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, el)
}

func (h *Handlers) GetElement(c echo.Context) error {
	el, err := h.Elements.Get(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, el)
}

type patchElementRequest struct {
	BoardID  string         `json:"boardId"`
	Changes  map[string]any `json:"changes"`
	ClientID string         `json:"clientId,omitempty"`
}

func (h *Handlers) PatchElement(c echo.Context) error {
	var req patchElementRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	op := domain.Op{
		ElementID: c.Param("id"),
		Action:    domain.ActionUpdate,
		Changes:   domain.Content(req.Changes),
	}
	if _, err := h.Txns.Apply(c.Request().Context(), principal(c), req.BoardID, req.ClientID, []domain.Op{op}); err != nil {
		return err
	}
	el, err := h.Elements.Get(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, el)
}

func (h *Handlers) DuplicateElement(c echo.Context) error {
	created, err := h.Elements.Duplicate(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, orEmpty(created))
}

type cloneRequest struct {
	TargetParentID string       `json:"targetParentId"`
	Position       domain.Point `json:"position"`
}

func (h *Handlers) ConvertToClone(c echo.Context) error {
	var req cloneRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	clone, err := h.Elements.ConvertToClone(c.Request().Context(), principal(c), c.Param("id"), req.TargetParentID, req.Position)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, clone)
}

type useTemplateRequest struct {
	BoardID  string       `json:"boardId"`
	Position domain.Point `json:"position"`
}

func (h *Handlers) UseTemplate(c echo.Context) error {
	var req useTemplateRequest
	if err := c.Bind(&req); err != nil {
		return domain.ErrValidation
	}
	root, err := h.Elements.UseTemplate(c.Request().Context(), principal(c), c.Param("id"), req.BoardID, req.Position)
	if err != nil {
		return err
	}
	return c.JSON(http.StatusCreated, root)
}

func (h *Handlers) CloneInstances(c echo.Context) error {
	list, err := h.Elements.CloneInstances(c.Request().Context(), principal(c), c.Param("id"))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(list))
}

// ---- trash ----

func (h *Handlers) ListTrash(c echo.Context) error {
	items, err := h.Elements.Trash(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, orEmpty(items))
}

func (h *Handlers) RestoreTrash(c echo.Context) error {
	if err := h.Elements.RestoreFromTrash(c.Request().Context(), principal(c), c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handlers) DeleteTrashItem(c echo.Context) error {
	if err := h.Elements.DeletePermanently(c.Request().Context(), principal(c), c.Param("id")); err != nil {
		return err
	}
	return c.NoContent(http.StatusNoContent)
}

func (h *Handlers) EmptyTrash(c echo.Context) error {
	count, err := h.Elements.EmptyTrash(c.Request().Context(), principal(c))
	if err != nil {
		return err
	}
	return c.JSON(http.StatusOK, map[string]int{"deleted": count})
}

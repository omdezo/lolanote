package service

import (
	"context"
	"time"

	"qomranote/backend/internal/domain"
)

// ---- Comments (§4.17) --------------------------------------------------------
// Threads are COMMENT_THREAD elements; messages live here. Rules mirror
// Milanote: commenting needs an account, only authors edit their own
// comments, and comments cannot be removed from a thread once posted.

type CommentService struct {
	comments      domain.CommentRepository
	elements      domain.ElementRepository
	notifications domain.NotificationRepository
	access        *AccessResolver
	newID         IDGenerator
}

func NewCommentService(comments domain.CommentRepository, elements domain.ElementRepository, notifications domain.NotificationRepository, access *AccessResolver, newID IDGenerator) *CommentService {
	return &CommentService{comments: comments, elements: elements, notifications: notifications, access: access, newID: newID}
}

// List returns a thread's messages after a view check.
func (s *CommentService) List(ctx context.Context, p *domain.Principal, threadID string) ([]*domain.Comment, error) {
	if _, _, err := s.access.RequireView(ctx, threadID, p); err != nil {
		return nil, err
	}
	return s.comments.ListByThread(ctx, threadID)
}

// Add posts a message. Feedback-level access suffices — read-only boards can
// allow commenting without edit rights (§6.1 mechanism 3).
func (s *CommentService) Add(ctx context.Context, p *domain.Principal, threadID, body string) (*domain.Comment, error) {
	if body == "" {
		return nil, domain.ErrValidation
	}
	role, board, err := s.access.Resolve(ctx, threadID, p)
	if err != nil {
		return nil, err
	}
	if role < RoleFeedback {
		return nil, domain.ErrForbidden
	}
	c := &domain.Comment{
		ID: s.newID(), ThreadID: threadID, AuthorID: p.Sub,
		Body: body, CreatedAt: time.Now().UTC(),
	}
	if err := s.comments.Insert(ctx, c); err != nil {
		return nil, err
	}
	// Notify the board owner about new feedback (skip self-comments).
	if board.ACL != nil && board.ACL.OwnerID != p.Sub {
		_ = s.notifications.Insert(ctx, &domain.Notification{
			ID: s.newID(), UserID: board.ACL.OwnerID, Kind: domain.NotifyComment,
			ActorID: p.Sub, BoardID: board.ID, ElementID: threadID,
			Message: p.Name + " commented on \"" + board.Title() + "\"",
		})
	}
	return c, nil
}

// Edit updates a message body — authors only.
func (s *CommentService) Edit(ctx context.Context, p *domain.Principal, commentID, body string) (*domain.Comment, error) {
	c, err := s.comments.Get(ctx, commentID)
	if err != nil {
		return nil, err
	}
	if c.AuthorID != p.Sub {
		return nil, domain.ErrForbidden
	}
	now := time.Now().UTC()
	c.Body = body
	c.EditedAt = &now
	if err := s.comments.Update(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// React toggles an emoji reaction for the caller.
func (s *CommentService) React(ctx context.Context, p *domain.Principal, commentID, emoji string) (*domain.Comment, error) {
	c, err := s.comments.Get(ctx, commentID)
	if err != nil {
		return nil, err
	}
	role, _, err := s.access.Resolve(ctx, c.ThreadID, p)
	if err != nil {
		return nil, err
	}
	if role < RoleFeedback {
		return nil, domain.ErrForbidden
	}
	if c.Reactions == nil {
		c.Reactions = map[string][]string{}
	}
	subs := c.Reactions[emoji]
	for i, sub := range subs {
		if sub == p.Sub { // already reacted → toggle off
			c.Reactions[emoji] = append(subs[:i], subs[i+1:]...)
			if len(c.Reactions[emoji]) == 0 {
				delete(c.Reactions, emoji)
			}
			return c, s.comments.Update(ctx, c)
		}
	}
	c.Reactions[emoji] = append(subs, p.Sub)
	return c, s.comments.Update(ctx, c)
}

// ---- Labels (§4.18) -----------------------------------------------------------
// A private tagging layer; auto-assigned colors; usage counts drive the
// filter UI.

type LabelService struct {
	labels   domain.LabelRepository
	elements domain.ElementRepository
	access   *AccessResolver
	newID    IDGenerator
}

func NewLabelService(labels domain.LabelRepository, elements domain.ElementRepository, access *AccessResolver, newID IDGenerator) *LabelService {
	return &LabelService{labels: labels, elements: elements, access: access, newID: newID}
}

// labelPalette rotates through pleasant defaults when creating labels.
var labelPalette = []string{"#e17055", "#6c5ce7", "#00b894", "#0984e3", "#fdcb6e", "#d63031", "#00cec9", "#e84393"}

func (s *LabelService) List(ctx context.Context, p *domain.Principal) ([]*domain.Label, error) {
	return s.labels.ListByOwner(ctx, p.Sub)
}

func (s *LabelService) Create(ctx context.Context, p *domain.Principal, name, color string) (*domain.Label, error) {
	if name == "" {
		return nil, domain.ErrValidation
	}
	existing, err := s.labels.ListByOwner(ctx, p.Sub)
	if err != nil {
		return nil, err
	}
	for _, l := range existing {
		if l.Name == name {
			return l, nil // reuse rather than duplicate
		}
	}
	if color == "" {
		color = labelPalette[len(existing)%len(labelPalette)]
	}
	l := &domain.Label{
		ID: s.newID(), OwnerID: p.Sub, Name: name, Color: color,
		CreatedAt: time.Now().UTC(),
	}
	return l, s.labels.Insert(ctx, l)
}

func (s *LabelService) Update(ctx context.Context, p *domain.Principal, id, name, color string) (*domain.Label, error) {
	l, err := s.labels.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.OwnerID != p.Sub {
		return nil, domain.ErrForbidden
	}
	if name != "" {
		l.Name = name
	}
	if color != "" {
		l.Color = color
	}
	return l, s.labels.Update(ctx, l)
}

func (s *LabelService) Delete(ctx context.Context, p *domain.Principal, id string) error {
	l, err := s.labels.Get(ctx, id)
	if err != nil {
		return err
	}
	if l.OwnerID != p.Sub {
		return domain.ErrForbidden
	}
	return s.labels.Delete(ctx, id)
}

// Attach tags an element; Detach removes the tag. Both adjust usage counts.
func (s *LabelService) Attach(ctx context.Context, p *domain.Principal, elementID, labelID string) error {
	if _, err := s.access.RequireEdit(ctx, elementID, p); err != nil {
		return err
	}
	el, err := s.elements.Get(ctx, elementID)
	if err != nil {
		return err
	}
	for _, id := range el.LabelIDs {
		if id == labelID {
			return nil
		}
	}
	labelIDs := append(append([]string{}, el.LabelIDs...), labelID)
	if _, err := s.elements.MergePatch(ctx, elementID, domain.Content{"labelIds": labelIDs}); err != nil {
		return err
	}
	return s.labels.IncrementUsage(ctx, labelID, 1)
}

func (s *LabelService) Detach(ctx context.Context, p *domain.Principal, elementID, labelID string) error {
	if _, err := s.access.RequireEdit(ctx, elementID, p); err != nil {
		return err
	}
	el, err := s.elements.Get(ctx, elementID)
	if err != nil {
		return err
	}
	kept := []string{}
	found := false
	for _, id := range el.LabelIDs {
		if id == labelID {
			found = true
			continue
		}
		kept = append(kept, id)
	}
	if !found {
		return nil
	}
	if _, err := s.elements.MergePatch(ctx, elementID, domain.Content{"labelIds": kept}); err != nil {
		return err
	}
	return s.labels.IncrementUsage(ctx, labelID, -1)
}

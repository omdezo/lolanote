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
	comments domain.CommentRepository
	elements domain.ElementRepository
	notifier *Notifier
	access   *AccessResolver
	newID    IDGenerator
	events   domain.EventBroadcaster // optional: live comment.new pushes
}

func NewCommentService(comments domain.CommentRepository, elements domain.ElementRepository, notifier *Notifier, access *AccessResolver, newID IDGenerator) *CommentService {
	return &CommentService{comments: comments, elements: elements, notifier: notifier, access: access, newID: newID}
}

// AttachEvents enables realtime comment broadcasts to the board room.
func (s *CommentService) AttachEvents(events domain.EventBroadcaster) { s.events = events }

// List returns a thread's messages after a view check.
func (s *CommentService) List(ctx context.Context, p *domain.Principal, threadID string) ([]*domain.Comment, error) {
	if _, _, err := s.access.RequireView(ctx, threadID, p); err != nil {
		return nil, err
	}
	return s.comments.ListByThread(ctx, threadID)
}

// Add posts a message. Feedback-level access suffices — read-only boards can
// allow commenting without edit rights (§6.1 mechanism 3). Mentions carry the
// mentioned users' subs (the client resolves @names to subs before posting).
func (s *CommentService) Add(ctx context.Context, p *domain.Principal, threadID, body string, mentions []string) (*domain.Comment, error) {
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

	// Live push to everyone viewing the board.
	if s.events != nil {
		s.events.BroadcastEvent(board.ID, "comment.new", c)
	}

	// @mentions notify the mentioned users (gated by their preferences).
	notified := map[string]bool{p.Sub: true}
	for _, sub := range mentions {
		if sub == "" || notified[sub] {
			continue
		}
		notified[sub] = true
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: sub, Kind: domain.NotifyMention,
			ActorID: p.Sub, BoardID: board.ID, ElementID: threadID,
			Message: p.Name + " mentioned you on \"" + board.Title() + "\"",
		})
	}
	// The board owner hears about new feedback (unless already mentioned).
	if board.ACL != nil && !notified[board.ACL.OwnerID] {
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: board.ACL.OwnerID, Kind: domain.NotifyComment,
			ActorID: p.Sub, BoardID: board.ID, ElementID: threadID,
			Message: p.Name + " commented on \"" + board.Title() + "\"",
		})
	}
	return c, nil
}

// AnnounceAgentComment gives an agent-written comment everything a human's
// comment gets: the live push, the board owner's bell, and any @mentions.
//
// The agent's comment used to be a bare repository Insert. So the REPORTING
// register's entire output surface produced a message that never pushed
// comment.new — nobody's open board showed it without a reload — never notified
// the board owner though every human comment does, and could carry no mentions
// at all. A run that answered "what is blocked?" on a shared board left an
// answer nobody was told about.
//
// It is announce-only rather than a re-routed Add because the thread element
// does not exist yet when the body is written: agent comments are pre-writes,
// staged against an element the same transaction is about to create, so an
// Add() that resolves the thread's ACL would fail on every new thread. The
// authorization is not skipped, it has already happened — the ops that create
// the thread go through the delegated write path, and this runs only after that
// transaction has landed.
func (s *CommentService) AnnounceAgentComment(ctx context.Context, p *domain.Principal, c *domain.Comment, mentions []string) {
	if c == nil || p == nil {
		return
	}
	_, board, err := s.access.Resolve(ctx, c.ThreadID, p)
	if err != nil || board == nil {
		return
	}
	if s.events != nil {
		s.events.BroadcastEvent(board.ID, "comment.new", c)
	}
	// "X's assistant", never "X": the sentence is about who wrote the words,
	// and the whole point of the item is that the human did not.
	who := p.Name + "'s assistant"
	notified := map[string]bool{}
	for _, sub := range mentions {
		if sub == "" || notified[sub] {
			continue
		}
		notified[sub] = true
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: sub, Kind: domain.NotifyMention,
			ActorID: p.Sub, BoardID: board.ID, ElementID: c.ThreadID,
			Origin: domain.OriginAgent, AgentRunID: c.AgentRunID,
			Message: who + " mentioned you on \"" + board.Title() + "\"",
		})
	}
	if board.ACL != nil && !notified[board.ACL.OwnerID] && board.ACL.OwnerID != "" {
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: board.ACL.OwnerID, Kind: domain.NotifyComment,
			ActorID: p.Sub, BoardID: board.ID, ElementID: c.ThreadID,
			Origin: domain.OriginAgent, AgentRunID: c.AgentRunID,
			Message: who + " commented on \"" + board.Title() + "\"",
		})
	}
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
	// The label must be the caller's own. Labels are private to their owner, so
	// stamping somebody else's id onto an element both leaks that a label exists
	// and inflates their usage count from outside their account. The agent's
	// path has always checked this; the path a person uses did not.
	label, err := s.labels.Get(ctx, labelID)
	if err != nil || label.OwnerID != p.Sub {
		return domain.ErrForbidden
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

// Detach deliberately does NOT repeat Attach's ownership check, and the
// asymmetry is the rule rather than an oversight in it.
//
// The invariant the write path settled on (see authorizeLabelPatch in
// transaction_service.go, which enforces the same thing on the transaction
// route) is about ATTACHING: you may only put your own label on something. Once
// a label is genuinely on an element, taking it off is ordinary editing, and
// anyone who may edit the element may do it — a collaborator tidying a shared
// card should not be blocked by a tag whose owner has since left the board.
//
// The usage count therefore moves even for somebody else's label, because it
// really did come off; leaving it high would strand a phantom in that person's
// filter list with no element behind it and no way to reach it.
//
// If this ever grows an OwnerID check to "match Attach", a shared board acquires
// tags nobody present can remove.
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

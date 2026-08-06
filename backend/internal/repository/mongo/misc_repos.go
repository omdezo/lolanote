package mongo

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"qomranote/backend/internal/domain"
)

// ---- transactions ----------------------------------------------------------

type TransactionRepo struct{ col *mongo.Collection }

func NewTransactionRepo(s *Store) *TransactionRepo {
	return &TransactionRepo{col: s.DB.Collection(colTransactions)}
}

var _ domain.TransactionRepository = (*TransactionRepo)(nil)

// Insert writes the journal row, mapping a duplicate id to ErrConflict.
//
// It returned the raw driver error, so a retried commit — a network blip, a
// double-click that beats the state transition, a scheduled run with no human
// to not-double-click — surfaced as an opaque failure that no caller could tell
// apart from a real one. ElementRepo.Insert has mapped this since it was
// written; the journal simply never did, and the journal is the one row that
// says a transaction happened.
func (r *TransactionRepo) Insert(ctx context.Context, t *domain.Transaction) error {
	_, err := r.col.InsertOne(ctx, t)
	if mongo.IsDuplicateKeyError(err) {
		return domain.ErrConflict
	}
	return err
}

// RedactElements strips the content of ops naming permanently-deleted elements.
//
// The row stays and the verb stays — "Ali deleted a card" is the audit trail and
// the reason the journal exists. What goes is the verbatim copy of what the card
// said, which is what made Empty Trash untrue: GET /boards/:id/transactions
// serves this collection to any current editor, including one invited after the
// deletion, and a delete op carries the deleted element's whole prior content.
//
// arrayFilters so only the matching ops in a mixed transaction are touched: a
// drag of forty cards where one was later purged must keep the other
// thirty-nine's inverses, or undo stops working for changes nobody deleted.
func (r *TransactionRepo) RedactElements(ctx context.Context, elementIDs []string) (int64, error) {
	if len(elementIDs) == 0 {
		return 0, nil
	}
	res, err := r.col.UpdateMany(ctx,
		bson.M{"ops.elementId": bson.M{"$in": elementIDs}},
		bson.A{bson.M{"$set": bson.M{"ops": bson.M{"$map": bson.M{
			"input": "$ops",
			"as":    "op",
			"in": bson.M{"$cond": bson.A{
				bson.M{"$in": bson.A{"$$op.elementId", elementIDs}},
				bson.M{"elementId": "$$op.elementId", "action": "$$op.action", "redacted": true},
				"$$op",
			}},
		}}}}},
	)
	if err != nil {
		return 0, err
	}
	return res.ModifiedCount, nil
}

// Complete settles a claimed row: the ops it applied, the notifications it rang,
// and the state that turns a claim into a commit. See service.TransactionReserver
// for why the id is claimed before the work rather than after it.
func (r *TransactionRepo) Complete(ctx context.Context, txn *domain.Transaction) error {
	res, err := r.col.UpdateOne(ctx, bson.M{"_id": txn.ID}, bson.M{"$set": bson.M{
		"ops":             txn.Ops,
		"notificationIds": txn.NotificationIDs,
		"state":           domain.TxnCommitted,
	}})
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

// Abandon releases a claim whose work did not stand, so the next attempt is a
// real retry rather than a lookup of a commit that never happened.
func (r *TransactionRepo) Abandon(ctx context.Context, id string) error {
	_, err := r.col.DeleteOne(ctx, bson.M{"_id": id, "state": domain.TxnApplying})
	return err
}

func (r *TransactionRepo) Get(ctx context.Context, id string) (*domain.Transaction, error) {
	var t domain.Transaction
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&t)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (r *TransactionRepo) ListByBoard(ctx context.Context, boardID string, limit int) ([]*domain.Transaction, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cur, err := r.col.Find(ctx, bson.M{"boardId": boardID},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Transaction
	return out, cur.All(ctx, &out)
}

// DeleteByUser removes every transaction a user authored.
//
// The journal was the one collection account deletion never touched, and it is
// the one that holds the most: Op.Changes and Op.UndoChanges are full content
// snapshots — text previews, table cells, document bodies — so "delete my
// account" left a complete, queryable copy of everything the person had ever
// written, keyed by their own userId, in a product that ships a privacy tab and
// a data-export endpoint.
func (r *TransactionRepo) DeleteByUser(ctx context.Context, sub string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"userId": sub})
	return err
}

// DeleteOlderThan trims the journal past the retention window.
//
// This is the highest-write collection in the system and it had no retention at
// all, while the elements it describes are purged at 90 days — so most rows
// eventually described elements that no longer existed, degrading exactly the
// revert and audit features that make agent runs trustworthy.
func (r *TransactionRepo) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := r.col.DeleteMany(ctx, bson.M{"createdAt": bson.M{"$lt": cutoff}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

// ---- users -----------------------------------------------------------------

type UserRepo struct{ col *mongo.Collection }

func NewUserRepo(s *Store) *UserRepo { return &UserRepo{col: s.DB.Collection(colUsers)} }

var _ domain.UserRepository = (*UserRepo)(nil)

func (r *UserRepo) GetBySub(ctx context.Context, sub string) (*domain.User, error) {
	return r.one(ctx, bson.M{"keycloakSub": sub})
}

func (r *UserRepo) GetByEmail(ctx context.Context, email string) (*domain.User, error) {
	return r.one(ctx, bson.M{"email": email})
}

func (r *UserRepo) one(ctx context.Context, q bson.M) (*domain.User, error) {
	var u domain.User
	err := r.col.FindOne(ctx, q).Decode(&u)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	return &u, err
}

func (r *UserRepo) Insert(ctx context.Context, u *domain.User) error {
	_, err := r.col.InsertOne(ctx, u)
	if mongo.IsDuplicateKeyError(err) {
		return domain.ErrConflict
	}
	return err
}

func (r *UserRepo) Update(ctx context.Context, u *domain.User) error {
	res, err := r.col.ReplaceOne(ctx, bson.M{"_id": u.ID}, u)
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *UserRepo) UpdateSettings(ctx context.Context, sub string, s *domain.UserSettings) error {
	res, err := r.col.UpdateOne(ctx, bson.M{"keycloakSub": sub},
		bson.M{"$set": bson.M{"settings": s}})
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *UserRepo) Delete(ctx context.Context, sub string) error {
	_, err := r.col.DeleteOne(ctx, bson.M{"keycloakSub": sub})
	return err
}

// ---- comments ---------------------------------------------------------------

type CommentRepo struct{ col *mongo.Collection }

func NewCommentRepo(s *Store) *CommentRepo { return &CommentRepo{col: s.DB.Collection(colComments)} }

var _ domain.CommentRepository = (*CommentRepo)(nil)

func (r *CommentRepo) Insert(ctx context.Context, c *domain.Comment) error {
	_, err := r.col.InsertOne(ctx, c)
	return err
}

func (r *CommentRepo) Get(ctx context.Context, id string) (*domain.Comment, error) {
	var c domain.Comment
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	return &c, err
}

func (r *CommentRepo) ListByThread(ctx context.Context, threadID string) ([]*domain.Comment, error) {
	cur, err := r.col.Find(ctx, bson.M{"threadId": threadID},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Comment
	return out, cur.All(ctx, &out)
}

func (r *CommentRepo) Update(ctx context.Context, c *domain.Comment) error {
	res, err := r.col.ReplaceOne(ctx, bson.M{"_id": c.ID}, c)
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

// Delete removes one message. Reachable only from the failure-cleanup path, not
// from any UI verb — §4.17's "messages cannot be removed from a thread once
// posted" is a rule about what a PERSON may do, and it was being enforced by the
// storage layer having no delete at all.
func (r *CommentRepo) Delete(ctx context.Context, id string) error {
	_, err := r.col.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// DeleteByThread removes a thread's messages when the thread element itself is
// permanently deleted. Without it, purging a COMMENT_THREAD — by trash sweep, by
// board hard-delete, or by account deletion — left its messages behind as
// permanently orphaned, permanently un-attributable text whose reaction maps
// still held live users' subject ids.
func (r *CommentRepo) DeleteByThread(ctx context.Context, threadID string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"threadId": threadID})
	return err
}

// DeleteByAuthor removes everything one person wrote. Account deletion only.
func (r *CommentRepo) DeleteByAuthor(ctx context.Context, sub string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"authorId": sub})
	return err
}

// ---- labels ------------------------------------------------------------------

type LabelRepo struct{ col *mongo.Collection }

func NewLabelRepo(s *Store) *LabelRepo { return &LabelRepo{col: s.DB.Collection(colLabels)} }

var _ domain.LabelRepository = (*LabelRepo)(nil)

func (r *LabelRepo) Insert(ctx context.Context, l *domain.Label) error {
	_, err := r.col.InsertOne(ctx, l)
	return err
}

func (r *LabelRepo) Get(ctx context.Context, id string) (*domain.Label, error) {
	var l domain.Label
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&l)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	return &l, err
}

func (r *LabelRepo) ListByOwner(ctx context.Context, ownerSub string) ([]*domain.Label, error) {
	cur, err := r.col.Find(ctx, bson.M{"ownerId": ownerSub},
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Label
	return out, cur.All(ctx, &out)
}

func (r *LabelRepo) Update(ctx context.Context, l *domain.Label) error {
	res, err := r.col.ReplaceOne(ctx, bson.M{"_id": l.ID}, l)
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *LabelRepo) Delete(ctx context.Context, id string) error {
	_, err := r.col.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (r *LabelRepo) DeleteByOwner(ctx context.Context, ownerSub string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"ownerId": ownerSub})
	return err
}

func (r *LabelRepo) IncrementUsage(ctx context.Context, id string, delta int64) error {
	_, err := r.col.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$inc": bson.M{"usageCount": delta}})
	return err
}

// ---- attachments ---------------------------------------------------------------

type AttachmentRepo struct{ col *mongo.Collection }

func NewAttachmentRepo(s *Store) *AttachmentRepo {
	return &AttachmentRepo{col: s.DB.Collection(colAttachments)}
}

var _ domain.AttachmentRepository = (*AttachmentRepo)(nil)

func (r *AttachmentRepo) Insert(ctx context.Context, a *domain.Attachment) error {
	_, err := r.col.InsertOne(ctx, a)
	return err
}

func (r *AttachmentRepo) Get(ctx context.Context, id string) (*domain.Attachment, error) {
	var a domain.Attachment
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&a)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	return &a, err
}

func (r *AttachmentRepo) Update(ctx context.Context, a *domain.Attachment) error {
	res, err := r.col.ReplaceOne(ctx, bson.M{"_id": a.ID}, a)
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *AttachmentRepo) StalePresigned(ctx context.Context, olderThan time.Time) ([]*domain.Attachment, error) {
	cur, err := r.col.Find(ctx, bson.M{
		"status":    domain.AttachmentPresigned,
		"createdAt": bson.M{"$lt": olderThan},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Attachment
	return out, cur.All(ctx, &out)
}

func (r *AttachmentRepo) Delete(ctx context.Context, id string) error {
	_, err := r.col.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (r *AttachmentRepo) DeleteByOwner(ctx context.Context, ownerSub string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"ownerId": ownerSub})
	return err
}

// ListByOwner enumerates a person's uploads so the bytes can be removed before
// the rows that name them are. The index existed and the method did not, which
// is the whole reason "this permanently deletes all uploaded files" was false:
// DeleteByOwner is a row delete and nothing else, so every image and PDF stayed
// in the bucket, still fetchable through the unauthenticated blob route, with
// nothing left in the database able to enumerate what had survived.
func (r *AttachmentRepo) ListByOwner(ctx context.Context, ownerSub string) ([]*domain.Attachment, error) {
	cur, err := r.col.Find(ctx, bson.M{"ownerId": ownerSub})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Attachment
	return out, cur.All(ctx, &out)
}

// ---- notifications ---------------------------------------------------------------

type NotificationRepo struct{ col *mongo.Collection }

func NewNotificationRepo(s *Store) *NotificationRepo {
	return &NotificationRepo{col: s.DB.Collection(colNotifications)}
}

var _ domain.NotificationRepository = (*NotificationRepo)(nil)

func (r *NotificationRepo) Insert(ctx context.Context, n *domain.Notification) error {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	_, err := r.col.InsertOne(ctx, n)
	return err
}

func (r *NotificationRepo) ListByUser(ctx context.Context, sub string, unreadOnly bool, limit int) ([]*domain.Notification, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := bson.M{"userId": sub}
	if unreadOnly {
		q["read"] = false
	}
	cur, err := r.col.Find(ctx, q,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Notification
	return out, cur.All(ctx, &out)
}

func (r *NotificationRepo) MarkRead(ctx context.Context, sub string, ids []string) error {
	q := bson.M{"userId": sub}
	if len(ids) > 0 {
		q["_id"] = bson.M{"$in": ids}
	}
	_, err := r.col.UpdateMany(ctx, q, bson.M{"$set": bson.M{"read": true}})
	return err
}

func (r *NotificationRepo) DeleteByUser(ctx context.Context, sub string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"userId": sub})
	return err
}

// DeleteByIDs withdraws specific notifications — the bells a reverted
// transaction rang. See service.Notifier.Retract.
func (r *NotificationRepo) DeleteByIDs(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := r.col.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
	return err
}

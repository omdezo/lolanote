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

func (r *TransactionRepo) Insert(ctx context.Context, t *domain.Transaction) error {
	_, err := r.col.InsertOne(ctx, t)
	return err
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

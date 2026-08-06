package mongo

import (
	"context"
	"regexp"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"qomranote/backend/internal/domain"
)

// ---- audit ------------------------------------------------------------------

// AuditRepo is the append-only audit log (§9). It implements exactly the two
// verbs the port declares; the collection is never updated and never deleted
// from here, and retention is the TTL index in indexes.go rather than a sweep
// this type could be asked to run.
type AuditRepo struct{ col *mongo.Collection }

// NewAuditRepo constructs the repository.
func NewAuditRepo(s *Store) *AuditRepo {
	return &AuditRepo{col: s.DB.Collection(colAuditEvents)}
}

var _ domain.AuditRepository = (*AuditRepo)(nil)

// Insert writes one row. A duplicate id maps to ErrConflict like every other
// insert here, so a retried emit is distinguishable from a real failure.
func (r *AuditRepo) Insert(ctx context.Context, ev *domain.AuditEvent) error {
	if ev.ID == "" {
		ev.ID = NewID()
	}
	if _, err := r.col.InsertOne(ctx, ev); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

// List pages newest-first on _id.
//
// The cursor is the id and not the timestamp because ids are hex ObjectIds —
// time-ordered and unique — so a page boundary that falls inside a burst of
// events written in the same millisecond neither repeats a row nor skips one.
// A timestamp cursor can do both, and the bursts are exactly the moments an
// audit reader cares about: a bulk role change, an impersonated session, an
// agent run applying forty ops.
func (r *AuditRepo) List(ctx context.Context, f domain.AuditFilter) ([]*domain.AuditEvent, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := bson.M{}
	if f.OrgID != "" {
		q["orgId"] = f.OrgID
	}
	if f.ActionPrefix != "" {
		// Anchored so the (orgId, action, _id) index serves the scan; quoted so a
		// dot in the taxonomy stays a dot.
		q["action"] = primitive.Regex{Pattern: "^" + regexp.QuoteMeta(f.ActionPrefix)}
	}
	if !f.Since.IsZero() || !f.Until.IsZero() {
		at := bson.M{}
		if !f.Since.IsZero() {
			at["$gte"] = f.Since
		}
		if !f.Until.IsZero() {
			at["$lt"] = f.Until
		}
		q["at"] = at
	}
	if f.Cursor != "" {
		q["_id"] = bson.M{"$lt": f.Cursor}
	}
	// One more than asked for: the extra row is how the caller learns there IS a
	// next page. Returning a cursor whenever the page came back full hands out a
	// link to an empty page at every exact multiple of the limit.
	cur, err := r.col.Find(ctx, q,
		options.Find().SetSort(bson.D{{Key: "_id", Value: -1}}).SetLimit(int64(limit)+1))
	if err != nil {
		return nil, "", err
	}
	defer cur.Close(ctx)
	var rows []*domain.AuditEvent
	if err := cur.All(ctx, &rows); err != nil {
		return nil, "", err
	}
	if len(rows) > limit {
		rows = rows[:limit]
		return rows, rows[limit-1].ID, nil
	}
	return rows, "", nil
}

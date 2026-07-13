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

// ElementRepo implements domain.ElementRepository.
type ElementRepo struct{ col *mongo.Collection }

// NewElementRepo constructs the repository.
func NewElementRepo(s *Store) *ElementRepo { return &ElementRepo{col: s.DB.Collection(colElements)} }

var _ domain.ElementRepository = (*ElementRepo)(nil)

func (r *ElementRepo) Insert(ctx context.Context, el *domain.Element) error {
	_, err := r.col.InsertOne(ctx, el)
	if mongo.IsDuplicateKeyError(err) {
		return domain.ErrConflict
	}
	return err
}

func (r *ElementRepo) Get(ctx context.Context, id string) (*domain.Element, error) {
	var el domain.Element
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&el)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	return &el, err
}

func (r *ElementRepo) GetMany(ctx context.Context, ids []string) ([]*domain.Element, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return r.find(ctx, bson.M{"_id": bson.M{"$in": ids}}, nil)
}

func (r *ElementRepo) Children(ctx context.Context, f domain.ElementFilter) ([]*domain.Element, error) {
	q := bson.M{"location.parentId": f.ParentID}
	if !f.IncludeDeleted {
		q["deletedAt"] = nil
	}
	if f.Section != "" {
		q["location.section"] = f.Section
	}
	if len(f.Types) > 0 {
		q["type"] = bson.M{"$in": f.Types}
	}
	return r.find(ctx, q, options.Find().SetSort(bson.D{{Key: "location.index", Value: 1}}))
}

// Descendants walks the containment tree breadth-first, batching each level
// into one $in query.
func (r *ElementRepo) Descendants(ctx context.Context, rootID string, includeDeleted bool) ([]*domain.Element, error) {
	var out []*domain.Element
	frontier := []string{rootID}
	for len(frontier) > 0 {
		q := bson.M{"location.parentId": bson.M{"$in": frontier}}
		if !includeDeleted {
			q["deletedAt"] = nil
		}
		level, err := r.find(ctx, q, nil)
		if err != nil {
			return nil, err
		}
		frontier = frontier[:0]
		for _, el := range level {
			out = append(out, el)
			if el.Type.IsContainer() {
				frontier = append(frontier, el.ID)
			}
		}
	}
	return out, nil
}

// patchableRoots is the whitelist of top-level fields a merge patch may
// touch. Identity, ownership, ACL, and trash state have dedicated methods.
var patchableRoots = map[string]bool{"content": true, "location": true, "labelIds": true}

func (r *ElementRepo) MergePatch(ctx context.Context, id string, patch domain.Content) (*domain.Element, error) {
	var raw bson.M
	if err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&raw); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
		return nil, err
	}
	for key, val := range patch {
		if !patchableRoots[key] {
			continue
		}
		raw[key] = mergeValue(raw[key], val)
	}
	raw["updatedAt"] = time.Now().UTC()
	if _, err := r.col.ReplaceOne(ctx, bson.M{"_id": id}, raw); err != nil {
		return nil, err
	}
	bytes, err := bson.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var el domain.Element
	if err := bson.Unmarshal(bytes, &el); err != nil {
		return nil, err
	}
	return &el, nil
}

// mergeValue applies RFC-7386 merge-patch semantics: nested objects merge
// recursively, null deletes, everything else replaces.
func mergeValue(existing, patch any) any {
	patchMap, patchIsMap := asMap(patch)
	if !patchIsMap {
		return patch
	}
	existingMap, existingIsMap := asMap(existing)
	if !existingIsMap {
		existingMap = bson.M{}
	}
	for k, v := range patchMap {
		if v == nil {
			delete(existingMap, k)
			continue
		}
		existingMap[k] = mergeValue(existingMap[k], v)
	}
	return existingMap
}

func asMap(v any) (bson.M, bool) {
	switch m := v.(type) {
	case bson.M:
		return m, true
	case map[string]any:
		return bson.M(m), true
	case domain.Content:
		return bson.M(m), true
	case bson.D:
		out := bson.M{}
		for _, e := range m {
			out[e.Key] = e.Value
		}
		return out, true
	default:
		return nil, false
	}
}

func (r *ElementRepo) SetACL(ctx context.Context, id string, acl *domain.ACL) error {
	res, err := r.col.UpdateOne(ctx, bson.M{"_id": id},
		bson.M{"$set": bson.M{"acl": acl, "updatedAt": time.Now().UTC()}})
	if err == nil && res.MatchedCount == 0 {
		return domain.ErrNotFound
	}
	return err
}

func (r *ElementRepo) SoftDelete(ctx context.Context, ids []string, by, batchID string, at time.Time) error {
	_, err := r.col.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"deletedAt": at, "deletedBy": by, "trashBatchId": batchID, "updatedAt": at}})
	return err
}

func (r *ElementRepo) Restore(ctx context.Context, ids []string) error {
	_, err := r.col.UpdateMany(ctx, bson.M{"_id": bson.M{"$in": ids}},
		bson.M{"$set": bson.M{"deletedAt": nil, "deletedBy": "", "trashBatchId": "", "updatedAt": time.Now().UTC()}})
	return err
}

func (r *ElementRepo) RestoreBatch(ctx context.Context, batchID string) error {
	if batchID == "" {
		return domain.ErrValidation
	}
	_, err := r.col.UpdateMany(ctx, bson.M{"trashBatchId": batchID},
		bson.M{"$set": bson.M{"deletedAt": nil, "deletedBy": "", "trashBatchId": "", "updatedAt": time.Now().UTC()}})
	return err
}

func (r *ElementRepo) HardDelete(ctx context.Context, ids []string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
	return err
}

func (r *ElementRepo) Trashed(ctx context.Context, ownerSub string) ([]*domain.Element, error) {
	q := bson.M{
		"deletedAt": bson.M{"$ne": nil},
		"$or": bson.A{
			bson.M{"deletedBy": ownerSub},
			bson.M{"createdBy": ownerSub},
		},
	}
	return r.find(ctx, q, options.Find().SetSort(bson.D{{Key: "deletedAt", Value: -1}}).SetLimit(500))
}

// Search matches case-insensitive substrings across the text-bearing content
// fields of elements the caller created or owns.
func (r *ElementRepo) Search(ctx context.Context, ownerSub, query string, limit int) ([]*domain.Element, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rx := bson.M{"$regex": escapeRegex(query), "$options": "i"}
	q := bson.M{
		"deletedAt": nil,
		"$and": bson.A{
			bson.M{"$or": bson.A{
				bson.M{"createdBy": ownerSub},
				bson.M{"acl.ownerId": ownerSub},
				bson.M{"acl.editors": ownerSub},
			}},
			bson.M{"$or": bson.A{
				bson.M{"content.textPreview": rx},
				bson.M{"content.title": rx},
				bson.M{"content.filename": rx},
				bson.M{"content.url": rx},
			}},
		},
	}
	return r.find(ctx, q, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(int64(limit)))
}

func (r *ElementRepo) CloneInstances(ctx context.Context, sourceID string) ([]*domain.Element, error) {
	return r.find(ctx, bson.M{
		"type":                  domain.TypeClone,
		"content.cloneSourceId": sourceID,
		"deletedAt":             nil,
	}, nil)
}

func (r *ElementRepo) BoardsOwnedBy(ctx context.Context, sub string, templatesOnly bool) ([]*domain.Element, error) {
	q := bson.M{"type": domain.TypeBoard, "deletedAt": nil, "$or": bson.A{
		bson.M{"acl.ownerId": sub},
		bson.M{"acl.editors": sub},
	}}
	if templatesOnly {
		q["content.isTemplate"] = true
	}
	return r.find(ctx, q, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}))
}

func (r *ElementRepo) BoardsByShareToken(ctx context.Context, token string) ([]*domain.Element, error) {
	if token == "" {
		return nil, domain.ErrNotFound
	}
	return r.find(ctx, bson.M{"type": domain.TypeBoard, "deletedAt": nil, "$or": bson.A{
		bson.M{"acl.publicEditLink": token},
		bson.M{"acl.viewLink.token": token},
	}}, nil)
}

// DueTaskReminders finds live TASK elements whose reminderAt has passed and
// that were not yet notified. reminderAt is an RFC3339 UTC string, so a plain
// lexicographic comparison is chronologically correct.
func (r *ElementRepo) DueTaskReminders(ctx context.Context, now time.Time, limit int) ([]*domain.Element, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := bson.M{
		"type":                 domain.TypeTask,
		"deletedAt":            nil,
		"content.done":         bson.M{"$ne": true},
		"content.reminderSent": bson.M{"$ne": true},
		"content.reminderAt": bson.M{
			"$gt":  "",
			"$lte": now.UTC().Format(time.RFC3339),
		},
	}
	return r.find(ctx, q, options.Find().SetLimit(int64(limit)))
}

// OwnedBoards lists boards whose ACL owner is sub (account purge needs the
// trashed ones too, hence includeDeleted).
func (r *ElementRepo) OwnedBoards(ctx context.Context, sub string, includeDeleted bool) ([]*domain.Element, error) {
	q := bson.M{"type": domain.TypeBoard, "acl.ownerId": sub}
	if !includeDeleted {
		q["deletedAt"] = nil
	}
	return r.find(ctx, q, nil)
}

// RemoveEditorEverywhere pulls a departing user out of every board ACL.
func (r *ElementRepo) RemoveEditorEverywhere(ctx context.Context, sub string) error {
	_, err := r.col.UpdateMany(ctx,
		bson.M{"type": domain.TypeBoard, "acl.editors": sub},
		bson.M{"$pull": bson.M{"acl.editors": sub}})
	return err
}

// CountsByParent groups live children by parent and type in one aggregation.
func (r *ElementRepo) CountsByParent(ctx context.Context, parentIDs []string) (map[string]map[domain.ElementType]int64, error) {
	out := map[string]map[domain.ElementType]int64{}
	if len(parentIDs) == 0 {
		return out, nil
	}
	cur, err := r.col.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"location.parentId": bson.M{"$in": parentIDs}, "deletedAt": nil}}},
		{{Key: "$group", Value: bson.M{
			"_id": bson.M{"p": "$location.parentId", "t": "$type"},
			"n":   bson.M{"$sum": 1},
		}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID struct {
			P string `bson:"p"`
			T string `bson:"t"`
		} `bson:"_id"`
		N int64 `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	for _, row := range rows {
		if out[row.ID.P] == nil {
			out[row.ID.P] = map[domain.ElementType]int64{}
		}
		out[row.ID.P][domain.ElementType(row.ID.T)] = row.N
	}
	return out, nil
}

// PurgeExpired permanently removes trash older than the retention window
// (Milanote keeps deleted items for 3 months, §3.4).
func (r *ElementRepo) PurgeExpired(ctx context.Context, olderThan time.Time) (int64, error) {
	res, err := r.col.DeleteMany(ctx, bson.M{"deletedAt": bson.M{"$ne": nil, "$lt": olderThan}})
	if err != nil {
		return 0, err
	}
	return res.DeletedCount, nil
}

func (r *ElementRepo) find(ctx context.Context, q bson.M, opts *options.FindOptions) ([]*domain.Element, error) {
	var cur *mongo.Cursor
	var err error
	if opts != nil {
		cur, err = r.col.Find(ctx, q, opts)
	} else {
		cur, err = r.col.Find(ctx, q)
	}
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Element
	if err := cur.All(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// escapeRegex neutralizes regex metacharacters in user queries.
func escapeRegex(s string) string {
	special := `\.+*?()|[]{}^$`
	out := make([]rune, 0, len(s)*2)
	for _, r := range s {
		for _, sp := range special {
			if r == sp {
				out = append(out, '\\')
				break
			}
		}
		out = append(out, r)
	}
	return string(out)
}

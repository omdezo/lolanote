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
	els, _, err := r.DescendantsLimited(ctx, rootID, includeDeleted, 0)
	return els, err
}

// DescendantsLimited is Descendants with a ceiling, reporting whether it stopped
// short. A limit of 0 means no ceiling, which is what the unbounded callers get.
//
// The bound exists because a subtree read had none: Duplicate, Export, the
// delete cascade and the account purge each pull a whole subtree into one
// request, and a workspace is a tree a person grows without ever being told
// there is a limit. Returning the flag rather than a short list is what lets the
// caller refuse — a truncated duplicate is a board missing cards nobody can
// name, and a truncated export is an archive that looks complete.
func (r *ElementRepo) DescendantsLimited(ctx context.Context, rootID string, includeDeleted bool, limit int) ([]*domain.Element, bool, error) {
	var out []*domain.Element
	frontier := []string{rootID}
	for len(frontier) > 0 {
		q := bson.M{"location.parentId": bson.M{"$in": frontier}}
		if !includeDeleted {
			q["deletedAt"] = nil
		}
		level, err := r.find(ctx, q, nil)
		if err != nil {
			return nil, false, err
		}
		frontier = frontier[:0]
		for _, el := range level {
			if limit > 0 && len(out) >= limit {
				return out, true, nil
			}
			out = append(out, el)
			if el.Type.IsContainer() {
				frontier = append(frontier, el.ID)
			}
		}
	}
	return out, false, nil
}

// patchableRoots is the whitelist of top-level fields a merge patch may
// touch. Identity, ownership, ACL, and trash state have dedicated methods.
var patchableRoots = map[string]bool{"content": true, "location": true, "labelIds": true}

// MergePatch applies an RFC-7386 merge patch as ONE conditional update.
//
// It used to be FindOne → merge in Go → ReplaceOne with no predicate, and that
// made this — the only element write path in the product — document-granular.
// hub.go states the design as "concurrency is element-granular … two users on
// different cards merge trivially; the same card resolves server-authoritatively
// (last writer wins)", and the merge function below honoured that while the
// write that followed it threw the result away: if A moves a card while B tags
// it, whichever ReplaceOne lands second was built from a snapshot taken before
// the other read, so it silently restores every field the other write changed.
// On a shared board that reads as "my label came off by itself".
//
// The second cost was write amplification: a two-float nudge read and rewrote
// the whole document — an arbitrarily large content.doc or content.strokes —
// and the oplog carried the full replacement too, so replication traffic scaled
// with document size instead of change size.
//
// Translating the patch into $set/$unset on dotted paths fixes both: one round
// trip instead of two, only the changed leaves on the wire, and genuinely
// field-granular concurrency — which is what the hub's header always claimed.
func (r *ElementRepo) MergePatch(ctx context.Context, id string, patch domain.Content) (*domain.Element, error) {
	update := mergePatchUpdate(patch, time.Now().UTC())
	var raw bson.M
	err := r.col.FindOneAndUpdate(ctx, bson.M{"_id": id}, update,
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&raw)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, domain.ErrNotFound
		}
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

// maxFlattenDepth bounds how far a patch is broken into dotted paths.
//
// Deep enough for location.position.x — the deepest leaf any caller actually
// patches — and no deeper, because content.doc is a ProseMirror tree: flattening
// a document to its leaves would produce thousands of paths and an update
// document larger than the value it is writing.
//
// Past the bound a subtree is written whole, so at that depth a partial object
// REPLACES rather than merges. Stated rather than hidden: every caller that
// sends a nested object that deep (an editor saving content.doc, a drag sending
// location.position) sends all of it, and the concurrency this change exists for
// is between writers touching different content keys — one level down, where the
// flattening is exact.
const maxFlattenDepth = 3

// maxDepthFor is that bound PER ROOT, because the two roots are not the same
// kind of field.
//
// A uniform depth of 3 produced `content.doc.type` — two levels into content,
// one deeper than the comment above claims — and an empty note stores
// `doc: null`. Mongo cannot create a field inside a null, so every edit of a
// note that had never held rich text answered:
//
//	(PathNotViable) Cannot create field 'content' in element {doc: null}
//
// a 500 on /api/v1/transactions for the most ordinary action in the product.
// The schemaless half of an element is exactly where a value may legitimately
// be null, so it is exactly where a dotted path cannot be assumed to have
// somewhere to land.
//
// location is structural: it and location.position are always objects on every
// stored element, so a dotted path there always has a parent. That is what buys
// the property this whole change exists for — two people dragging different
// cards, or one dragging while another types, writing disjoint keys.
func maxDepthFor(root string) int {
	if root == "location" {
		return maxFlattenDepth // location.position.x
	}
	return 2 // content.doc, content.textPreview — never inside them
}

// mergePatchUpdate turns a merge patch into the $set/$unset document Mongo
// applies. Null means delete, per RFC 7386, so it becomes $unset; everything
// else is a $set at the path it was found on.
func mergePatchUpdate(patch domain.Content, now time.Time) bson.M {
	set := bson.M{"updatedAt": now}
	unset := bson.M{}
	for key, val := range patch {
		// Identity, ownership, ACL and trash state have dedicated methods, and
		// op.Changes comes straight from the client: a patch that reached them
		// would be a privilege escalation through the ordinary write path.
		if !patchableRoots[key] {
			continue
		}
		flattenPatch(key, val, 1, maxDepthFor(key), set, unset)
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	return update
}

// flattenPatch walks the patch tree accumulating dotted paths. It is the old
// mergeValue recursion with the destination changed: instead of building a
// merged copy of the stored document it records where each leaf goes.
func flattenPatch(path string, val any, depth, maxDepth int, set, unset bson.M) {
	if val == nil {
		unset[path] = ""
		return
	}
	obj, isObj := asMap(val)
	if isObj && len(obj) == 0 {
		// Merging an empty object changes nothing (RFC 7386). Setting it would
		// erase everything under the path — and callers do send an empty
		// content alongside a real location patch.
		return
	}
	if !isObj || depth >= maxDepth {
		set[path] = val
		return
	}
	for k, v := range obj {
		flattenPatch(path+"."+k, v, depth+1, maxDepth, set, unset)
	}
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

// maxTrashBatches is how many DELETIONS the trash can show.
//
// JN18: the cap used to be 500 ELEMENTS, flat. Trashing a container cascades
// its whole live subtree under one trashBatchId, so ending a production —
// which, with no archive, is the only way to say "we finished" — turned into
// 400 trash rows and pushed every other recoverable item out of the list. The
// card somebody deleted by accident last Tuesday was still in the database,
// still restorable by id, and no longer visible in the only UI that can restore
// it. Nothing said so.
//
// Counting deletions instead means one project deletion costs one row of the
// budget, which is what a person means by "the last 500 things I deleted".
const maxTrashBatches = 500

// maxTrashElements bounds the payload, since a batch has no size limit.
//
// Deliberately generous and deliberately present: the cap that matters is the
// batch one above, and this exists only so a single pathological deletion
// cannot turn one trash read into an unbounded response. It is an order of
// magnitude above the old flat cap, so it can only ever return MORE than the
// list did before.
const maxTrashElements = 5000

func (r *ElementRepo) Trashed(ctx context.Context, ownerSub string) ([]*domain.Element, error) {
	mine := bson.M{
		"deletedAt": bson.M{"$ne": nil},
		"$or": bson.A{
			bson.M{"deletedBy": ownerSub},
			bson.M{"createdBy": ownerSub},
		},
	}

	// One aggregation to find the most recent deletions, keyed by batch. It
	// groups on the batch id where there is one and on the element's own id
	// where there is not, so a lone delete is a batch of one and the two kinds
	// of row compete for the same budget on equal terms.
	// $ifNull first, because the field is absent on elements trashed before the
	// batch machinery existed and a missing field in an aggregation expression
	// is not the same value as an empty string. Getting this wrong would file
	// every one of those under a single null key — one row standing for years
	// of unrelated deletions.
	batchKey := bson.M{"$let": bson.M{
		"vars": bson.M{"b": bson.M{"$ifNull": bson.A{"$trashBatchId", ""}}},
		"in":   bson.M{"$cond": bson.A{bson.M{"$eq": bson.A{"$$b", ""}}, "$_id", "$$b"}},
	}}
	cur, err := r.col.Aggregate(ctx, bson.A{
		bson.M{"$match": mine},
		bson.M{"$group": bson.M{
			"_id": batchKey,
			// The batch's own moment. Members are stamped within microseconds of
			// each other, but $max is the one that cannot be wrong.
			"deletedAt": bson.M{"$max": "$deletedAt"},
		}},
		bson.M{"$sort": bson.M{"deletedAt": -1}},
		bson.M{"$limit": maxTrashBatches},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var groups []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &groups); err != nil {
		return nil, err
	}
	if len(groups) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(groups))
	for _, g := range groups {
		keys = append(keys, g.ID)
	}

	// Members are fetched separately rather than pushed into the group above:
	// $push of whole documents would put a 400-element deletion's entire subtree
	// into one aggregation stage, and the 16MB document ceiling is not a limit
	// anybody should discover by having their trash stop loading.
	q := bson.M{"$and": bson.A{mine, bson.M{"$or": bson.A{
		bson.M{"trashBatchId": bson.M{"$in": keys}},
		bson.M{"_id": bson.M{"$in": keys}},
	}}}}
	return r.find(ctx, q, options.Find().
		SetSort(bson.D{{Key: "deletedAt", Value: -1}}).
		SetLimit(maxTrashElements))
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
				// searchText is the WHOLE body, derived from content.doc at
				// commit. textPreview stays in the list for elements written
				// before it existed, but it is a 500-character preview — search
				// keyed on it alone meant a six-thousand-word treatment was
				// findable only by its opening paragraph.
				bson.M{"content.searchText": rx},
				bson.M{"content.textPreview": rx},
				bson.M{"content.title": rx},
				bson.M{"content.filename": rx},
				bson.M{"content.url": rx},
				// A checklist item's words live under a different key entirely,
				// so nothing on any to-do list has ever been findable.
				bson.M{"content.text": rx},
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

// ExpiringSoon lists the trashed elements a purge at this cutoff will remove.
//
// Read BEFORE the delete, because content.attachmentId is the only link from an
// element to its bytes and the delete destroys it. Nothing has ever done this,
// which is why every "Delete forever" and every Empty Trash removed the card and
// left the photograph — still in the bucket, still fetchable through a blob
// route that takes no credential.
func (r *ElementRepo) ExpiringSoon(ctx context.Context, olderThan time.Time) ([]*domain.Element, error) {
	cur, err := r.col.Find(ctx, bson.M{"deletedAt": bson.M{"$ne": nil, "$lt": olderThan}})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Element
	return out, cur.All(ctx, &out)
}

// AttachmentRefs returns the attachment ids the given elements point at.
func (r *ElementRepo) AttachmentRefs(ctx context.Context, ids []string) ([]string, error) {
	cur, err := r.col.Find(ctx, bson.M{
		"_id":                  bson.M{"$in": ids},
		"content.attachmentId": bson.M{"$exists": true, "$ne": ""},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []*domain.Element
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []string
	for _, el := range rows {
		if att, _ := el.Content["attachmentId"].(string); att != "" && !seen[att] {
			seen[att] = true
			out = append(out, att)
		}
	}
	return out, nil
}

// ElementsByAttachment is the reverse index from an uploaded file to the cards
// that show it. Blob reads had no authorization point of any kind; this is what
// gives them one, because "may this person see this picture" is really "does any
// element holding it sit on a board they can open".
func (r *ElementRepo) ElementsByAttachment(ctx context.Context, attachmentID string) ([]*domain.Element, error) {
	cur, err := r.col.Find(ctx, bson.M{"content.attachmentId": attachmentID})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*domain.Element
	return out, cur.All(ctx, &out)
}

// AttachmentReferrers counts how many elements still point at each attachment.
//
// A duplicated card shares its original's attachment id, so collecting on
// "the element that named it is gone" alone would take the picture out from
// under the copy. The count is what makes the sweep safe, and its errors are
// what make it FAIL CLOSED: an attachment whose referrer query failed is kept.
func (r *ElementRepo) AttachmentReferrers(ctx context.Context, attachmentIDs []string) (map[string]int64, error) {
	cur, err := r.col.Aggregate(ctx, []bson.M{
		{"$match": bson.M{"content.attachmentId": bson.M{"$in": attachmentIDs}}},
		{"$group": bson.M{"_id": "$content.attachmentId", "n": bson.M{"$sum": 1}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var rows []struct {
		ID string `bson:"_id"`
		N  int64  `bson:"n"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	out := make(map[string]int64, len(rows))
	for _, row := range rows {
		out[row.ID] = row.N
	}
	return out, nil
}

// SweepOrphanConnectors trashes LINE elements that can no longer be drawn.
//
// The write path now takes connectors down with the thing they joined, but
// boards edited before that shipped still carry the leftovers: lines pointing
// at a deleted or hard-purged id. They draw nothing — an unresolvable endpoint
// renders as no line at all — so they are invisible clutter that still comes
// back in queries and on restores.
//
// A dead endpoint is only half of it. A line is drawn on ONE board's canvas and
// resolves its endpoints from that board's elements, so a line whose endpoints
// have ended up under two different boards is exactly as invisible as one
// pointing at nothing — and moves, not deletes, are what produce those. Four
// were left on one real board by a single organizing run, joining columns that
// had been filed into different sub-boards.
//
// Soft delete, not hard: these were real elements a person drew, and the
// three-month trash window is the same grace everything else gets.
func (r *ElementRepo) SweepOrphanConnectors(ctx context.Context, at time.Time) (int64, error) {
	lines, err := r.find(ctx, bson.M{"type": string(domain.TypeLine), "deletedAt": nil}, nil)
	if err != nil {
		return 0, err
	}
	// Collect every endpoint first, then resolve them a LEVEL at a time. Walking
	// one line's ancestry at a time would be a query per connector per level on
	// every deploy.
	wanted := map[string]bool{}
	for _, l := range lines {
		for _, key := range []string{"fromId", "toId"} {
			if id, _ := l.Content[key].(string); id != "" {
				wanted[id] = true
			}
		}
	}
	if len(wanted) == 0 {
		return 0, nil
	}
	ids := make([]string, 0, len(wanted))
	for id := range wanted {
		ids = append(ids, id)
	}
	graph, err := r.loadWithAncestors(ctx, ids)
	if err != nil {
		return 0, err
	}

	orphans := strandedConnectors(lines, graph)
	if len(orphans) == 0 {
		return 0, nil
	}
	// Each gets its own batch id so restoring one does not drag the rest back.
	for _, id := range orphans {
		if err := r.SoftDelete(ctx, []string{id}, "system", id, at); err != nil {
			return 0, err
		}
	}
	return int64(len(orphans)), nil
}

// RepointAttachmentURLs rewrites stored file references to the ACL-checked
// route, and reports how many it moved.
//
// Every IMAGE and FILE written before that route existed carries a presigned
// URL in content.url: a bearer credential for the bytes, readable by anyone who
// can read the element or an export of it, surviving every permission change —
// and dead on day seven, at which point the picture simply disappears from a
// board nobody touched. Fixing the write path helps only new uploads; this is
// what makes the boards people already have keep working.
//
// Keyed on content.attachmentId, which is the only durable handle: elements
// whose url points somewhere that is not an attachment are left alone.
func (r *ElementRepo) RepointAttachmentURLs(ctx context.Context) (int64, error) {
	els, err := r.find(ctx, bson.M{
		"type":                 bson.M{"$in": []string{string(domain.TypeImage), string(domain.TypeFile)}},
		"deletedAt":            nil,
		"content.attachmentId": bson.M{"$exists": true, "$ne": ""},
	}, nil)
	if err != nil {
		return 0, err
	}
	var moved int64
	for _, el := range els {
		id, _ := el.Content["attachmentId"].(string)
		if id == "" {
			continue
		}
		want := "/api/v1/attachments/" + id + "/blob"
		if cur, _ := el.Content["url"].(string); cur == want {
			continue
		}
		if _, err := r.col.UpdateOne(ctx,
			bson.M{"_id": el.ID},
			bson.M{"$set": bson.M{"content.url": want, "updatedAt": time.Now().UTC()}},
		); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// RepointAgentColorKey moves colour written under the key nothing renders.
//
// The compiler wrote content.color while the card reads
// content.backgroundColor, so every colour an agent run ever set was invisible
// on the board and invisible to the next run's reading of it. The write path is
// fixed and the digest reads both keys, so this is only about making past runs'
// work appear; it touches nothing that already has the right key.
func (r *ElementRepo) RepointAgentColorKey(ctx context.Context) (int64, error) {
	els, err := r.find(ctx, bson.M{
		"deletedAt":               nil,
		"content.color":           bson.M{"$exists": true, "$ne": ""},
		"content.backgroundColor": bson.M{"$exists": false},
		"content.authoredBy":      bson.M{"$exists": true},
	}, nil)
	if err != nil {
		return 0, err
	}
	var moved int64
	for _, el := range els {
		hex, _ := el.Content["color"].(string)
		if hex == "" {
			continue
		}
		if _, err := r.col.UpdateOne(ctx,
			bson.M{"_id": el.ID},
			bson.M{
				"$set":   bson.M{"content.backgroundColor": hex, "updatedAt": time.Now().UTC()},
				"$unset": bson.M{"content.color": ""},
			},
		); err != nil {
			return moved, err
		}
		moved++
	}
	return moved, nil
}

// maxCanvasDepth bounds the walk from an endpoint up to the board it is drawn
// on. Deeper than any real nesting, and it is what stops bad parent data from
// turning a maintenance sweep into an infinite loop.
const maxCanvasDepth = 8

// loadWithAncestors fetches the named elements and every live element above
// them, one query per level of containment.
func (r *ElementRepo) loadWithAncestors(ctx context.Context, ids []string) (map[string]*domain.Element, error) {
	out := map[string]*domain.Element{}
	frontier := ids
	for depth := 0; depth < maxCanvasDepth && len(frontier) > 0; depth++ {
		found, err := r.find(ctx, bson.M{"_id": bson.M{"$in": frontier}, "deletedAt": nil}, nil)
		if err != nil {
			return nil, err
		}
		frontier = nil
		for _, el := range found {
			if _, seen := out[el.ID]; seen {
				continue
			}
			out[el.ID] = el
			// The walk does not stop at the first board it meets. An endpoint
			// that IS a board is drawn on the canvas ABOVE it, so stopping there
			// left every arrow to a sub-board tile looking like it pointed at
			// nothing.
			if el.Location.ParentID == "" {
				continue
			}
			if _, seen := out[el.Location.ParentID]; !seen {
				frontier = append(frontier, el.Location.ParentID)
			}
		}
	}
	return out, nil
}

// strandedConnectors names the lines that cannot render, given every live
// element their endpoints hang from.
//
// Pure, and separate from the query above, because the rule is the interesting
// part and a rule that can only be exercised against a live database is a rule
// nobody checks.
func strandedConnectors(lines []*domain.Element, graph map[string]*domain.Element) []string {
	var orphans []string
	for _, l := range lines {
		from, _ := l.Content["fromId"].(string)
		to, _ := l.Content["toId"].(string)
		// A line missing an endpoint id entirely is just as dead as one
		// pointing at a gone element: it has nothing to attach to.
		if from == "" || to == "" {
			orphans = append(orphans, l.ID)
			continue
		}
		fromCanvas := canvasIn(graph, from)
		toCanvas := canvasIn(graph, to)
		if fromCanvas == "" || toCanvas == "" {
			orphans = append(orphans, l.ID) // deleted, purged, or orphaned endpoint
			continue
		}
		// The line renders where BOTH endpoints resolve, which is the board it
		// is parented to and no other. Either endpoint elsewhere and it is an
		// arrow nobody will ever see.
		if fromCanvas != toCanvas || fromCanvas != l.Location.ParentID {
			orphans = append(orphans, l.ID)
		}
	}
	return orphans
}

// canvasIn is the board an endpoint is drawn on: its nearest board ancestor,
// or — when the endpoint IS a board — the board its tile sits on, which is one
// level further up.
func canvasIn(graph map[string]*domain.Element, id string) string {
	el, ok := graph[id]
	if !ok {
		return ""
	}
	if el.Type == domain.TypeBoard {
		id = el.Location.ParentID
	}
	for depth := 0; depth < maxCanvasDepth; depth++ {
		el, ok := graph[id]
		if !ok {
			return ""
		}
		if el.Type == domain.TypeBoard {
			return el.ID
		}
		id = el.Location.ParentID
	}
	return ""
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

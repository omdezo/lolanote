package mongo

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson"

	"qomranote/backend/internal/domain"
)

// What reaches the database is the assertion, because that is where the bug
// lived. The merge itself was always right; the write that followed it was a
// ReplaceOne of the whole document with no predicate, so a patch built from a
// stale snapshot silently restored every field somebody else had just changed.
// Nothing about the merge function could have shown that — only the update
// document can.
func setOf(t *testing.T, update bson.M) bson.M {
	t.Helper()
	set, ok := update["$set"].(bson.M)
	if !ok {
		t.Fatalf("update carries no $set: %#v", update)
	}
	return set
}

func TestMergePatch_ANudgeWritesTwoFloatsAndNotTheDocument(t *testing.T) {
	now := time.Now().UTC()
	update := mergePatchUpdate(domain.Content{
		"content": map[string]any{}, // the caller's untouched half
		"location": map[string]any{
			"position": map[string]any{"x": 500.0, "y": 640.0},
		},
	}, now)

	set := setOf(t, update)
	if set["location.position.x"] != 500.0 || set["location.position.y"] != 640.0 {
		t.Errorf("$set = %#v, want the two moved coordinates on dotted paths", set)
	}
	// The whole point: no key names a container. A write naming "content" or
	// "location" is a write that carries every byte under it, which is how a
	// concurrent label change got reverted by a drag.
	for key := range set {
		switch key {
		case "content", "location", "location.position":
			t.Errorf("$set writes the whole %q subtree — that is the document-granular write this replaced", key)
		}
	}
	if set["updatedAt"] != now {
		t.Errorf("updatedAt = %v, want the write's own timestamp", set["updatedAt"])
	}
}

func TestMergePatch_NullDeletesTheKeyRatherThanWritingNull(t *testing.T) {
	update := mergePatchUpdate(domain.Content{
		"content": map[string]any{"color": nil, "text": "kept"},
	}, time.Now().UTC())

	unset, ok := update["$unset"].(bson.M)
	if !ok {
		t.Fatalf("no $unset: %#v", update)
	}
	if _, cleared := unset["content.color"]; !cleared {
		t.Errorf("$unset = %#v, want content.color — RFC 7386 null is a delete", unset)
	}
	if setOf(t, update)["content.text"] != "kept" {
		t.Error("the sibling key in the same patch was lost")
	}
	if _, both := setOf(t, update)["content.color"]; both {
		t.Error("content.color is in $set and $unset at once; Mongo refuses that update")
	}
}

// A ProseMirror document is a tree. Flattening it to leaves would put thousands
// of dotted paths on the wire to write one paragraph — worse than the whole-
// document write this is replacing — so the walk stops and sets the subtree.
func TestMergePatch_DoesNotExplodeADocumentIntoLeafPaths(t *testing.T) {
	doc := map[string]any{
		"type": "doc",
		"content": []any{map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": "a treatment"}},
		}},
	}
	set := setOf(t, mergePatchUpdate(domain.Content{
		"content": map[string]any{"doc": doc},
	}, time.Now().UTC()))

	if len(set) > 4 {
		t.Errorf("$set has %d paths (%#v); a document patch must stay small", len(set), set)
	}
	// content.doc is written WHOLE, and this assertion used to demand the
	// opposite — that the document be split into content.doc.type and
	// content.doc.content.
	//
	// That is what made every edit of an empty note a 500. A card created from
	// the toolbar stores `doc: null`, and Mongo cannot create a field inside a
	// null: "(PathNotViable) Cannot create field 'content' in element
	// {doc: null}". The schemaless half of an element is exactly where a value
	// may legitimately be null, so it is exactly where a dotted path cannot be
	// assumed to have a parent to land in.
	//
	// Nothing is lost: an editor saving a document sends all of it, so writing
	// it whole is what was happening anyway. The per-key concurrency this
	// change exists for is BETWEEN content keys — doc against textPreview
	// against caption — which is one level down, and still exact.
	if _, ok := set["content.doc"]; !ok {
		t.Errorf("$set = %#v, want content.doc written whole", set)
	}
	for path := range set {
		if strings.HasPrefix(path, "content.doc.") {
			t.Errorf("$set reaches inside a content value (%s); an empty note "+
				"stores doc:null and Mongo cannot create a field inside it", path)
		}
	}
}

// Identity, ownership, ACL and trash state have dedicated methods. op.Changes
// comes straight off the wire, so a patch that reached them would be a
// privilege escalation through the ordinary write path.
func TestMergePatch_UnpatchableRootsNeverReachTheUpdate(t *testing.T) {
	set := setOf(t, mergePatchUpdate(domain.Content{
		"_id":       "somebody-elses-id",
		"createdBy": "attacker",
		"acl":       map[string]any{"ownerId": "attacker"},
		"deletedAt": time.Now().UTC(),
		"content":   map[string]any{"text": "fine"},
	}, time.Now().UTC()))

	for _, forbidden := range []string{"_id", "createdBy", "acl", "deletedAt"} {
		for key := range set {
			if key == forbidden || key == forbidden+".ownerId" {
				t.Errorf("$set reaches %q", key)
			}
		}
	}
	if set["content.text"] != "fine" {
		t.Error("the legitimate half of the patch was dropped with the rest")
	}
}

// The failure this whole change exists for, stated as two writes: A patches the
// labels, B moves the card. Both are real writes to disjoint paths and both
// must survive, whatever order they land in.
func TestMergePatch_TwoWritersOnDisjointFieldsDoNotOverlap(t *testing.T) {
	tagging := setOf(t, mergePatchUpdate(domain.Content{
		"labelIds": []string{"l-urgent"},
	}, time.Now().UTC()))
	dragging := setOf(t, mergePatchUpdate(domain.Content{
		"location": map[string]any{"position": map[string]any{"x": 40.0, "y": 90.0}},
	}, time.Now().UTC()))

	for key := range tagging {
		if key == "updatedAt" {
			continue
		}
		if _, collides := dragging[key]; collides {
			t.Errorf("both writes name %q, so one of them still loses the other", key)
		}
	}
	if _, ok := tagging["labelIds"]; !ok {
		t.Errorf("the tagging write = %#v, want labelIds", tagging)
	}
}

// The 500 that reached a real board, in the shape it actually arrived.
//
// A card made from the toolbar stores `doc: null` — that is what an empty note
// IS. Typing into it sends a merge patch carrying the new document, and the
// patch was being flattened to content.doc.type / content.doc.content. Mongo
// answered:
//
//	(PathNotViable) Plan executor error during findAndModify :: caused by ::
//	Cannot create field 'content' in element {doc: null}
//
// So /api/v1/transactions returned 500 for the most ordinary action in the
// product — writing in a note — and the console filled with them.
func TestMergePatch_CanWriteIntoAContentValueThatIsNull(t *testing.T) {
	set := setOf(t, mergePatchUpdate(domain.Content{
		"content": map[string]any{
			"doc":         map[string]any{"type": "doc", "content": []any{}},
			"textPreview": "first words",
		},
	}, time.Now().UTC()))

	// Every path must be settable against a stored {doc: null}: that means the
	// deepest content path is content.<key>, never content.<key>.<something>.
	for path := range set {
		if path == "updatedAt" {
			continue
		}
		if strings.Count(path, ".") > 1 {
			t.Errorf("%s reaches past a content key; against a stored doc:null "+
				"Mongo refuses the whole findAndModify", path)
		}
	}
	if _, ok := set["content.doc"]; !ok {
		t.Error("content.doc was not written")
	}
	if _, ok := set["content.textPreview"]; !ok {
		t.Error("content.textPreview was not written")
	}
}

// And the property the flattening exists for is untouched: location is
// structural — it and location.position are objects on every stored element —
// so a drag still writes disjoint paths and two people moving different cards
// do not clobber each other.
func TestMergePatch_StillWritesDisjointPathsForADrag(t *testing.T) {
	set := setOf(t, mergePatchUpdate(domain.Content{
		"location": map[string]any{"position": map[string]any{"x": 120.0, "y": 40.0}},
	}, time.Now().UTC()))

	for _, want := range []string{"location.position.x", "location.position.y"} {
		if _, ok := set[want]; !ok {
			t.Errorf("$set = %#v, want %s", set, want)
		}
	}
	for _, forbidden := range []string{"location", "location.position"} {
		if _, ok := set[forbidden]; ok {
			t.Errorf("$set names %s, which overwrites a sibling writer's field", forbidden)
		}
	}
}

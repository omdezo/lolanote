package memory_test

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"go.mongodb.org/mongo-driver/bson"

	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
)

// The audit log is append-only, and the memory repo is the double every service
// test reads it through. If a returned row shares its Meta map with the stored
// one, a caller can rewrite history in place and a test asserting append-only
// passes against a store that is not. The mongo adapter serializes on both hops
// and cannot be reached this way, so the double must not be either.
func TestAuditRepo_ListDoesNotAliasStoredMeta(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewAuditRepo()

	if err := repo.Insert(ctx, &domain.AuditEvent{
		ID:     "a1",
		OrgID:  "org1",
		Actor:  domain.AuditActor{Sub: "u1"},
		Action: "org.member_role_changed",
		Target: domain.AuditTarget{Type: "user", ID: "u2"},
		Meta: map[string]any{
			"kind":  "role",
			"roles": []any{"viewer", "editor"},
			"diff":  map[string]any{"before": "viewer"},
		},
		At: time.Now(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	first, _, err := repo.List(ctx, domain.AuditFilter{OrgID: "org1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("want 1 row, got %d", len(first))
	}
	first[0].Meta["kind"] = "tampered"
	first[0].Meta["added"] = true
	first[0].Meta["roles"].([]any)[0] = "tampered"
	first[0].Meta["diff"].(map[string]any)["before"] = "tampered"

	second, _, err := repo.List(ctx, domain.AuditFilter{OrgID: "org1"})
	if err != nil {
		t.Fatalf("list again: %v", err)
	}
	if len(second) != 1 {
		t.Fatalf("want 1 row, got %d", len(second))
	}
	if got := second[0].Meta["kind"]; got != "role" {
		t.Errorf("stored meta kind = %v, want role", got)
	}
	if _, ok := second[0].Meta["added"]; ok {
		t.Error("a List caller added a key to a stored audit row")
	}
	if got := second[0].Meta["roles"].([]any)[0]; got != "viewer" {
		t.Errorf("stored meta roles[0] = %v, want viewer", got)
	}
	if got := second[0].Meta["diff"].(map[string]any)["before"]; got != "viewer" {
		t.Errorf("stored meta diff.before = %v, want viewer", got)
	}
}

// The other direction: the caller keeps its own reference to the map it passed
// to Insert, so the store must not be reading from that map afterwards.
func TestAuditRepo_InsertDoesNotAliasCallerMeta(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewAuditRepo()

	meta := map[string]any{"kind": "role"}
	if err := repo.Insert(ctx, &domain.AuditEvent{
		ID:     "a1",
		OrgID:  "org1",
		Actor:  domain.AuditActor{Sub: "u1"},
		Action: "org.member_role_changed",
		Meta:   meta,
		At:     time.Now(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	meta["kind"] = "tampered"

	rows, _, err := repo.List(ctx, domain.AuditFilter{OrgID: "org1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if got := rows[0].Meta["kind"]; got != "role" {
		t.Errorf("stored meta kind = %v, want role", got)
	}
}

// metaPayload is a pointer target inside Meta whose own fields are reference
// types, so reaching it is not enough — the copy has to descend through it.
type metaPayload struct {
	Name string
	Tags []string
}

// sealedPayload keeps its mutable state unexported and hands it out through a
// method. A copy that walks only the fields it is allowed to read gives the
// caller a slice that still points into the stored row.
type sealedPayload struct{ tags []string }

func (s *sealedPayload) Tags() []string { return s.tags }

// newShapedMeta builds a Meta holding the reference shapes that a type switch
// over map[string]any / []any / []string / map[string]string does not name.
// Meta is map[string]any, so any of these is a legal thing for a caller to
// store, and each one is an alias if it is copied by interface value.
func newShapedMeta() map[string]any {
	return map[string]any{
		"rows":   []map[string]any{{"n": 1}},
		"ints":   []int{1, 2},
		"bytes":  []byte("ok"),
		"index":  map[string][]string{"a": {"x"}},
		"ptr":    &metaPayload{Name: "orig", Tags: []string{"t1"}},
		"sealed": &sealedPayload{tags: []string{"s1"}},
		"arr":    [2]map[string]any{{"k": "a"}, {"k": "b"}},
		"nested": []any{[]map[string]any{{"deep": "orig"}}},
	}
}

// tamperShapedMeta writes through every shape newShapedMeta put in. Whether the
// map came back from List or is the caller's own copy from before Insert, none
// of these writes may reach storage.
func tamperShapedMeta(m map[string]any) {
	m["rows"].([]map[string]any)[0]["n"] = 99
	m["ints"].([]int)[0] = 99
	m["bytes"].([]byte)[0] = 'X'
	m["index"].(map[string][]string)["a"][0] = "tampered"
	p := m["ptr"].(*metaPayload)
	p.Name = "tampered"
	p.Tags[0] = "tampered"
	m["sealed"].(*sealedPayload).Tags()[0] = "tampered"
	// Indexing the array yields a copy of the array, but the maps inside that
	// copy are the same map headers the stored array holds.
	m["arr"].([2]map[string]any)[0]["k"] = "tampered"
	m["nested"].([]any)[0].([]map[string]any)[0]["deep"] = "tampered"
}

func assertShapedMetaPristine(t *testing.T, m map[string]any) {
	t.Helper()
	if got := m["rows"].([]map[string]any)[0]["n"]; got != 1 {
		t.Errorf("stored meta rows[0][n] = %v, want 1", got)
	}
	if got := m["ints"].([]int)[0]; got != 1 {
		t.Errorf("stored meta ints[0] = %v, want 1", got)
	}
	if got := m["bytes"].([]byte)[0]; got != 'o' {
		t.Errorf("stored meta bytes[0] = %q, want 'o'", got)
	}
	if got := m["index"].(map[string][]string)["a"][0]; got != "x" {
		t.Errorf("stored meta index[a][0] = %v, want x", got)
	}
	p := m["ptr"].(*metaPayload)
	if p.Name != "orig" {
		t.Errorf("stored meta ptr.Name = %v, want orig", p.Name)
	}
	if p.Tags[0] != "t1" {
		t.Errorf("stored meta ptr.Tags[0] = %v, want t1", p.Tags[0])
	}
	if got := m["sealed"].(*sealedPayload).Tags()[0]; got != "s1" {
		t.Errorf("stored meta sealed.Tags()[0] = %v, want s1", got)
	}
	if got := m["arr"].([2]map[string]any)[0]["k"]; got != "a" {
		t.Errorf("stored meta arr[0][k] = %v, want a", got)
	}
	if got := m["nested"].([]any)[0].([]map[string]any)[0]["deep"]; got != "orig" {
		t.Errorf("stored meta nested[0][0][deep] = %v, want orig", got)
	}
}

func insertMeta(repo *memory.AuditRepo, id string, meta map[string]any) error {
	return repo.Insert(context.Background(), &domain.AuditEvent{
		ID:     id,
		OrgID:  "org1",
		Actor:  domain.AuditActor{Sub: "u1"},
		Action: "org.member_role_changed",
		Meta:   meta,
		At:     time.Now(),
	})
}

func insertShaped(t *testing.T, repo *memory.AuditRepo, meta map[string]any) {
	t.Helper()
	if err := insertMeta(repo, "a1", meta); err != nil {
		t.Fatalf("insert: %v", err)
	}
}

func assertNoRows(t *testing.T, repo *memory.AuditRepo) {
	t.Helper()
	rows, _, err := repo.List(context.Background(), domain.AuditFilter{OrgID: "org1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("a rejected insert left %d rows behind", len(rows))
	}
}

func listOne(t *testing.T, repo *memory.AuditRepo) *domain.AuditEvent {
	t.Helper()
	rows, _, err := repo.List(context.Background(), domain.AuditFilter{OrgID: "org1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	return rows[0]
}

// The List hop, for every reference shape Meta can hold rather than the four a
// type switch names.
func TestAuditRepo_ListDoesNotAliasAnyMetaShape(t *testing.T) {
	repo := memory.NewAuditRepo()
	insertShaped(t, repo, newShapedMeta())

	tamperShapedMeta(listOne(t, repo).Meta)

	assertShapedMetaPristine(t, listOne(t, repo).Meta)
}

// The Insert hop: the caller still holds the map it handed over, and writing
// through it must not reach the stored row either.
func TestAuditRepo_InsertDoesNotAliasAnyMetaShape(t *testing.T) {
	repo := memory.NewAuditRepo()
	meta := newShapedMeta()
	insertShaped(t, repo, meta)

	tamperShapedMeta(meta)

	assertShapedMetaPristine(t, listOne(t, repo).Meta)
}

// Meta is `any`, so nothing stops a caller handing over a value that points
// back at itself. The store cannot hold one: bson.Marshal has no cycle guard,
// so it recurses until the stack goes, and a stack overflow is not an error the
// adapter returns or that recover can catch. The double refuses the row instead
// — the walk has to notice the cycle either way, and accepting what the adapter
// would die writing is the more permissive double, not the safer one.
func TestAuditRepo_SelfReferentialMetaRejected(t *testing.T) {
	loop := make([]any, 1)
	loop[0] = loop
	self := map[string]any{"tag": "orig"}
	self["self"] = self
	deep := map[string]any{}
	deep["under"] = []any{map[string]any{"down": deep}}

	for name, meta := range map[string]map[string]any{
		"slice through itself":  {"loop": loop},
		"map through itself":    {"cycle": self},
		"cycle two levels down": {"nested": deep},
	} {
		t.Run(name, func(t *testing.T) {
			repo := memory.NewAuditRepo()
			if err := insertMeta(repo, "a1", meta); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("insert err = %v, want %v", err, domain.ErrValidation)
			}
			assertNoRows(t, repo)
		})
	}
}

// One referent reached by two paths is not a cycle: the adapter encodes it once
// per path and stores both, so the double stores it too. Refusing it would make
// the double stricter than the store for a shape the store accepts.
func TestAuditRepo_SharedReferentIsNotACycle(t *testing.T) {
	repo := memory.NewAuditRepo()
	shared := map[string]any{"n": 1}
	insertShaped(t, repo, map[string]any{"a": shared, "b": shared})

	got := listOne(t, repo).Meta
	got["a"].(map[string]any)["n"] = 99
	if v := got["b"].(map[string]any)["n"]; v != 1 {
		t.Errorf("both copies came back as one map: b.n = %v, want 1", v)
	}
	if v := listOne(t, repo).Meta["a"].(map[string]any)["n"]; v != 1 {
		t.Errorf("stored meta a.n = %v, want 1", v)
	}
}

// The driver has no encoder for these, so Insert fails against mongo. A double
// that takes them is more permissive than the store it stands in for, and a
// test that stores one passes against an insert that errors in production. Each
// case asserts the driver's own verdict alongside the repo's, so the pair
// documents that the two agree rather than merely asserting the repo's half.
func TestAuditRepo_RejectsWhatTheDriverCannotEncode(t *testing.T) {
	n := 7
	cases := map[string]any{
		"chan":                   make(chan int),
		"func":                   func() {},
		"unsafe.Pointer":         unsafe.Pointer(&n),
		"complex":                complex(1, 2),
		"nested chan":            []any{map[string]any{"c": make(chan int)}},
		"chan in a struct field": struct{ C chan int }{make(chan int)},
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			meta := map[string]any{"v": v}
			if _, err := bson.Marshal(meta); err == nil {
				t.Fatalf("bson.Marshal accepted %s: the premise of this test is that the store does not", name)
			}
			repo := memory.NewAuditRepo()
			if err := insertMeta(repo, "a1", meta); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("insert err = %v, want %v", err, domain.ErrValidation)
			}
			assertNoRows(t, repo)
		})
	}
}

// These keep their whole contents behind an unexported unsafe.Pointer or func,
// which the driver skips: bson.Marshal writes them as an empty document and
// returns no error, so the store silently drops what they hold. A copy cannot
// descend through such a field either — it can only hand back the same
// referent, which is the aliasing the append-only tests exist to forbid — and
// walking one of reflect's own descriptors as if it were data corrupts the
// runtime's view of it, which ends the process rather than the request.
//
// So the double is deliberately stricter than the encoder here. Stricter is
// available to a double; more mutable and more fragile are not.
func TestAuditRepo_RejectsValuesWhoseStateTheStoreWouldDrop(t *testing.T) {
	ptr := &atomic.Pointer[int]{}
	n := 7
	ptr.Store(&n)

	cases := map[string]any{
		"sync.Map":      &sync.Map{},
		"atomic.Ptr":    ptr,
		"reflect.Value": reflect.ValueOf(n),
		"reflect.Type":  reflect.TypeOf(n),
	}
	for name, v := range cases {
		t.Run(name, func(t *testing.T) {
			meta := map[string]any{"v": v}
			if _, err := bson.Marshal(meta); err != nil {
				t.Fatalf("bson.Marshal rejected %s (%v): this test exists because it does not", name, err)
			}
			repo := memory.NewAuditRepo()
			if err := insertMeta(repo, "a1", meta); !errors.Is(err, domain.ErrValidation) {
				t.Fatalf("insert err = %v, want %v", err, domain.ErrValidation)
			}
			assertNoRows(t, repo)
		})
	}
}

// A rejected Meta is rejected before anything is written, and a later insert of
// the same id is not a conflict against a row that was never stored.
func TestAuditRepo_RejectedInsertStoresNothing(t *testing.T) {
	repo := memory.NewAuditRepo()
	if err := insertMeta(repo, "a1", map[string]any{"c": make(chan int)}); !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("insert err = %v, want %v", err, domain.ErrValidation)
	}
	if err := insertMeta(repo, "a1", map[string]any{"kind": "role"}); err != nil {
		t.Fatalf("insert after a rejected one: %v", err)
	}
	if got := listOne(t, repo).Meta["kind"]; got != "role" {
		t.Errorf("stored meta kind = %v, want role", got)
	}
}

// A nil Meta stays nil: `bson:"meta,omitempty"` makes nil and empty different
// stored shapes, and the double has to agree with the adapter on which one it
// wrote.
func TestAuditRepo_NilMetaStaysNil(t *testing.T) {
	ctx := context.Background()
	repo := memory.NewAuditRepo()

	if err := repo.Insert(ctx, &domain.AuditEvent{
		ID:     "a1",
		OrgID:  "org1",
		Actor:  domain.AuditActor{Sub: "u1"},
		Action: "org.member_removed",
		At:     time.Now(),
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rows, _, err := repo.List(ctx, domain.AuditFilter{OrgID: "org1"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	if rows[0].Meta != nil {
		t.Errorf("Meta = %v, want nil", rows[0].Meta)
	}
}

// deepChain builds a chain of nested *any n levels deep. A walk that answers
// for its own stack refuses it; one that does not takes the process down with
// it, which is the failure the depth budget exists to convert into an error.
func deepChain(n int) any {
	var v any = "bottom"
	for i := 0; i < n; i++ {
		next := v
		v = &next
	}
	return v
}

// opaqueMeta encodes itself, so the driver never reads what it wraps.
type opaqueMeta struct{ Deep any }

func (opaqueMeta) MarshalBSON() ([]byte, error) {
	return bson.Marshal(bson.M{"opaque": true})
}

func TestAuditRepo_DeepNestingIsRefusedNotFatal(t *testing.T) {
	repo := memory.NewAuditRepo()
	err := repo.Insert(context.Background(), &domain.AuditEvent{
		ID: "deep", Action: "t", Meta: map[string]any{"chain": deepChain(200_000)},
	})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("a chain past the depth budget must be refused, got %v", err)
	}
}

// A subgraph behind MarshalBSON is one the store never reads, so refusing it
// would make the double reject a row Mongo writes.
func TestAuditRepo_OpaqueValueIsNotWalked(t *testing.T) {
	for _, tc := range []struct {
		name string
		deep any
	}{
		{"deep chain", deepChain(200_000)},
		{"a channel", make(chan int)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := map[string]any{"o": opaqueMeta{Deep: tc.deep}}
			if _, err := bson.Marshal(meta); err != nil {
				t.Fatalf("the driver must accept this, else the test proves nothing: %v", err)
			}
			repo := memory.NewAuditRepo()
			if err := repo.Insert(context.Background(), &domain.AuditEvent{ID: "o", Action: "t", Meta: meta}); err != nil {
				t.Fatalf("the double refused a row the driver encodes: %v", err)
			}
		})
	}
}

func TestAuditRepo_TimeKeepsItsZone(t *testing.T) {
	zone := time.FixedZone("+04", 4*60*60)
	orig := time.Date(2026, 8, 5, 1, 22, 31, 0, zone)

	repo := memory.NewAuditRepo()
	if err := repo.Insert(context.Background(), &domain.AuditEvent{
		ID: "t", Action: "t", Meta: map[string]any{"when": orig},
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, _, err := repo.List(context.Background(), domain.AuditFilter{})
	if err != nil || len(got) != 1 {
		t.Fatalf("list: %v (%d rows)", err, len(got))
	}
	stored, ok := got[0].Meta["when"].(time.Time)
	if !ok {
		t.Fatalf("want a time.Time, got %T", got[0].Meta["when"])
	}
	if stored != orig {
		t.Errorf("a time must survive storage unchanged:\n got %v (%v)\nwant %v (%v)",
			stored, stored.Location(), orig, orig.Location())
	}
}

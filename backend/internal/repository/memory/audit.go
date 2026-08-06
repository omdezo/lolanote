package memory

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"go.mongodb.org/mongo-driver/bson"

	"qomranote/backend/internal/domain"
)

// AuditRepo is the in-memory AuditRepository used by tests. Like the port it
// implements it has an insert and a read and nothing else — a double that could
// erase a row would let a test pass against behaviour the real store forbids.
type AuditRepo struct {
	mu   sync.RWMutex
	rows map[string]*domain.AuditEvent
}

func NewAuditRepo() *AuditRepo {
	return &AuditRepo{rows: map[string]*domain.AuditEvent{}}
}

func (r *AuditRepo) Insert(_ context.Context, ev *domain.AuditEvent) error {
	if err := validateMeta(ev.Meta); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.rows[ev.ID]; ok {
		return domain.ErrConflict
	}
	r.rows[ev.ID] = cloneAuditEvent(ev)
	return nil
}

// List mirrors the mongo adapter: newest first by id, one page at a time, with
// a cursor that is empty when there is nothing after this page.
func (r *AuditRepo) List(_ context.Context, f domain.AuditFilter) ([]*domain.AuditEvent, string, error) {
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []*domain.AuditEvent
	for _, ev := range r.rows {
		if f.OrgID != "" && ev.OrgID != f.OrgID {
			continue
		}
		if f.ActionPrefix != "" && !strings.HasPrefix(ev.Action, f.ActionPrefix) {
			continue
		}
		if !f.Since.IsZero() && ev.At.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && !ev.At.Before(f.Until) {
			continue
		}
		if f.Cursor != "" && ev.ID >= f.Cursor {
			continue
		}
		out = append(out, cloneAuditEvent(ev))
	}
	// Ids are time-ordered hex, so id-descending is newest-first here for the
	// same reason it is in the database.
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
		return out, out[limit-1].ID, nil
	}
	return out, "", nil
}

// validateMeta refuses a Meta the real store could not hold, with the sentinel
// the transport layer already maps. Meta is map[string]any, so the set of
// values a caller can hand over is open, and a double that accepts more than
// the mongo adapter lets a test pass against an insert that fails in
// production.
//
// Two gates, because neither subsumes the other:
//
//   - checkStorable walks everything the copy below reaches, unexported fields
//     included, and refuses chan, func and unsafe.Pointer. A copy cannot
//     descend through one of those, only hand back the same referent, so a
//     stored row would stay writable through the value the caller kept. The
//     types that hide their state behind such a field — sync.Map,
//     atomic.Pointer, reflect.Value, reflect.Type — encode as an empty document
//     instead of an error, so the driver alone does not stop them; the store
//     drops their contents either way, and a double that keeps Go types has to
//     drop them loudly. The same walk refuses a value that contains itself: the
//     driver has no cycle guard, so a cycle takes the process down inside
//     bson.Marshal rather than coming back as an error, and nothing may reach
//     the second gate before the first has ruled the graph finite.
//   - bson.Marshal, the encoder the adapter writes through, for what a kind
//     walk cannot know: a complex number, a type whose MarshalBSON fails, any
//     other value the driver has no encoder for.
//
// The encoded bytes are discarded. Meta is stored as the caller's own types
// because callers read it back with type assertions, and a round trip through
// BSON returns an int as int32, a []any as primitive.A and a *T as a map.
func validateMeta(m map[string]any) error {
	if m == nil {
		return nil
	}
	for k, v := range m {
		if v == nil {
			continue
		}
		if err := checkStorable(reflect.ValueOf(v), map[metaRef]bool{}, maxMetaDepth); err != nil {
			return fmt.Errorf("%w: audit meta %q holds %s", domain.ErrValidation, k, err)
		}
	}
	if _, err := bson.Marshal(m); err != nil {
		return fmt.Errorf("%w: audit meta cannot be encoded: %v", domain.ErrValidation, err)
	}
	return nil
}

// bsonMarshaler is the interface that makes a value opaque to the encoder, and
// so opaque to the walk that models it.
var bsonMarshaler = reflect.TypeOf((*bson.Marshaler)(nil)).Elem()

// timeType is copied whole rather than walked. A time.Time exposes no mutator,
// so there is nothing to alias through it, while descending into its *Location
// hands back a value that no longer compares equal to the one stored and prints
// in a different zone.
var timeType = reflect.TypeOf(time.Time{})

// metaRef identifies a referent on the path from the top of Meta down to the
// value being checked. Length is part of the identity because two slices over
// one backing array are two distinct referents.
type metaRef struct {
	ptr unsafe.Pointer
	typ reflect.Type
	n   int
}

// maxMetaDepth bounds the walk. Recursion has to answer for its own stack: a
// chain deep enough to exhaust it takes the process down with a fatal error no
// recover can catch, which is a worse failure than the one this gate exists to
// prevent, and the adapter merely returns an error for anything it cannot
// encode. The bound is far above any shape a call site composes — Meta is built
// from literals — so reaching it means a value the store has no business
// holding.
const maxMetaDepth = 64

// checkStorable reports what in v the store cannot hold, and nil when it can
// hold all of it. path holds the referents v hangs beneath, so a referent met
// twice on one path is a value that contains itself; a referent met twice on
// two different paths is a shape the driver encodes twice and is left alone.
// depth is the remaining stack budget.
func checkStorable(v reflect.Value, path map[metaRef]bool, depth int) error {
	if depth <= 0 {
		return fmt.Errorf("more than %d levels of nesting", maxMetaDepth)
	}
	depth--

	// A type that encodes itself is a subgraph the driver never reads, so what
	// hangs beneath it cannot reach the store and must not be judged as though
	// it could. Refusing here would make the double stricter than the adapter in
	// the one direction strictness is not free: a row Mongo stores.
	if v.IsValid() && v.Type().Implements(bsonMarshaler) {
		return nil
	}

	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		return fmt.Errorf("a %s", v.Kind())

	case reflect.Interface:
		if v.IsNil() {
			return nil
		}
		return checkStorable(v.Elem(), path, depth)

	case reflect.Pointer, reflect.Map, reflect.Slice:
		// The three kinds that hold a referent, and so the three a cycle has to
		// pass through. Each goes on the path before its contents are checked
		// and comes off after, so what the guard catches is an ancestor rather
		// than a second route to the same value.
		if v.IsNil() {
			return nil
		}
		ref := metaRef{ptr: v.UnsafePointer(), typ: v.Type()}
		if v.Kind() != reflect.Pointer {
			ref.n = v.Len()
		}
		if path[ref] {
			return errors.New("a value that contains itself")
		}
		path[ref] = true
		defer delete(path, ref)
		switch v.Kind() {
		case reflect.Pointer:
			return checkStorable(v.Elem(), path, depth)
		case reflect.Slice:
			return checkElems(v, path, depth)
		}
		for it := v.MapRange(); it.Next(); {
			// Keys are checked too: a comparable key type can still be a struct
			// or an array holding something unstorable.
			if err := checkStorable(it.Key(), path, depth); err != nil {
				return err
			}
			if err := checkStorable(it.Value(), path, depth); err != nil {
				return err
			}
		}
		return nil

	case reflect.Array:
		// No referent of its own: an array is its elements, so a cycle through
		// one has to pass a pointer, map or slice that is already guarded.
		return checkElems(v, path, depth)

	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			// Unexported fields are walked because the copy below copies them:
			// what a method can hand out is state the double has to reach, and
			// what the double reaches it has to be able to copy.
			if err := checkStorable(v.Field(i), path, depth); err != nil {
				return err
			}
		}
		return nil

	default:
		// The basic kinds have no contents and nothing to alias through.
		return nil
	}
}

func checkElems(v reflect.Value, path map[metaRef]bool, depth int) error {
	for i := 0; i < v.Len(); i++ {
		if err := checkStorable(v.Index(i), path, depth); err != nil {
			return err
		}
	}
	return nil
}

// cloneAuditEvent returns an event that shares no mutable state with ev. Both
// hops copy: what a caller hands to Insert must not stay reachable from the
// stored row, and what List hands back must not reach it either.
//
// A struct copy alone is not enough — AuditEvent.Meta is a map, so the copy and
// the original address the same one, and a reader could rewrite the contents of
// an append-only row. The mongo adapter serializes in both directions and has
// no such opening; a double that is more mutable than the store it stands in
// for lets a test pass against behaviour the real store forbids, and the
// append-only guarantee is exactly what those tests are pinning.
func cloneAuditEvent(ev *domain.AuditEvent) *domain.AuditEvent {
	cp := *ev
	cp.Meta = cloneMetaMap(ev.Meta)
	return &cp
}

// cloneMetaMap preserves nil, because a nil Meta and an empty one are different
// stored shapes under `bson:",omitempty"`.
func cloneMetaMap(m map[string]any) map[string]any {
	if m == nil {
		return nil
	}
	cp := make(map[string]any, len(m))
	for k, v := range m {
		cp[k] = cloneMetaValue(v)
	}
	return cp
}

// cloneMetaValue copies v so that nothing mutable inside it remains reachable
// from the argument.
//
// Meta is map[string]any, so the set of types it can hold is open and a type
// switch is the wrong shape for the job: it copies the cases someone thought of
// and hands back every other reference type — a []map[string]any, a []byte, a
// *T, an array of maps, any of those nested one level down — by interface
// value, which is the alias. Reflection walks whatever is actually there.
func cloneMetaValue(v any) any {
	if v == nil {
		return nil
	}
	return deepCopyValue(reflect.ValueOf(v)).Interface()
}

// deepCopyValue returns a value of src's exact type that shares no mutable
// state with src. Every value passed in must be readable — descents into
// unexported fields re-derive through their address so the read-only flag never
// travels down, which is what keeps the recursion total.
//
// CONSTRAINT: src comes from a Meta that validateMeta accepted. That is what
// bounds the recursion, since an accepted Meta holds no value that contains
// itself, and what makes every kind reachable from here either copyable below
// or immutable. Two paths to one referent become two copies, as they do through
// the adapter, which encodes such a value once per path.
func deepCopyValue(src reflect.Value) reflect.Value {
	if src.IsValid() && src.Type() == timeType {
		return src
	}
	// Symmetric with the walk that validated this value: what the encoder never
	// reads is not part of the row, so there is nothing beneath it for a caller
	// to reach through the store. Descending anyway would meet the kinds the
	// walk was allowed to skip.
	if src.IsValid() && src.Type().Implements(bsonMarshaler) {
		return src
	}
	switch src.Kind() {
	case reflect.Pointer:
		if src.IsNil() {
			return src
		}
		dst := reflect.New(src.Type().Elem())
		dst.Elem().Set(deepCopyValue(src.Elem()))
		return dst

	case reflect.Interface:
		if src.IsNil() {
			return src
		}
		// Re-wrapped in the interface type rather than returned bare, so the
		// element of a []any stays a []any element.
		dst := reflect.New(src.Type()).Elem()
		dst.Set(deepCopyValue(src.Elem()))
		return dst

	case reflect.Map:
		if src.IsNil() {
			return src
		}
		dst := reflect.MakeMapWithSize(src.Type(), src.Len())
		for it := src.MapRange(); it.Next(); {
			// Keys are copied too: a comparable key type can still be a struct
			// or an array holding a pointer.
			dst.SetMapIndex(deepCopyValue(it.Key()), deepCopyValue(it.Value()))
		}
		return dst

	case reflect.Slice:
		if src.IsNil() {
			return src
		}
		dst := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := 0; i < src.Len(); i++ {
			dst.Index(i).Set(deepCopyValue(src.Index(i)))
		}
		return dst

	case reflect.Array:
		// An array is copied by assignment, but its elements are not: an
		// [N]map[string]any assigned wholesale carries N shared maps.
		dst := reflect.New(src.Type()).Elem()
		for i := 0; i < src.Len(); i++ {
			dst.Index(i).Set(deepCopyValue(src.Index(i)))
		}
		return dst

	case reflect.Struct:
		// UnsafeAddr below needs an address, and src has one only when it came
		// from something addressable.
		if !src.CanAddr() {
			addressable := reflect.New(src.Type()).Elem()
			addressable.Set(src)
			src = addressable
		}
		dst := reflect.New(src.Type()).Elem()
		for i := 0; i < src.NumField(); i++ {
			sf, df := src.Field(i), dst.Field(i)
			if !sf.CanInterface() {
				// An unexported field is still mutable state a method can hand
				// out, so it is copied like any other; going through the
				// address is the only way reflect will read and write it.
				sf = reflect.NewAt(sf.Type(), unsafe.Pointer(sf.UnsafeAddr())).Elem()
				df = reflect.NewAt(df.Type(), unsafe.Pointer(df.UnsafeAddr())).Elem()
			}
			df.Set(deepCopyValue(sf))
		}
		return dst

	case reflect.Chan, reflect.Func, reflect.UnsafePointer:
		// CONSTRAINT: unreachable. checkStorable refuses these before a row is
		// stored and both hops copy only values that passed it, so arriving
		// here means the two walks disagree about what Meta can hold. There is
		// no copy to make — the only thing this arm could return is the caller's
		// own referent — so it is a defect in the double, not a value a caller
		// can produce, and it stops here rather than aliasing quietly.
		panic("memory: audit meta holds a " + src.Kind().String() + ", which Insert rejects")

	default:
		// The basic kinds: an interface holding one already holds a copy.
		return src
	}
}

var _ domain.AuditRepository = (*AuditRepo)(nil)

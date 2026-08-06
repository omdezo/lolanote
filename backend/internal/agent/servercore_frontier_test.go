package agent

// Regressions for the security and stuck-state work: what left the board
// without leaving the board's owner, and the states a person could reach and
// not get out of.
//
// An IN-PACKAGE test, unlike the shared harness next door. Two of these have to
// reach a state no scripted run can produce — a run FAILED with a committed
// transaction, a run caught mid-APPLYING — and seeding one through an exported
// surface would mean inventing an exported surface for it.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/service"
)

// ---- minimal stores ---------------------------------------------------------
//
// Hand-rolled rather than borrowed from agentmem: that package imports this one,
// so an in-package test cannot reach it without closing a cycle.

type scRuns struct {
	mu   sync.Mutex
	rows map[string]*Run
}

func newSCRuns() *scRuns { return &scRuns{rows: map[string]*Run{}} }

func (r *scRuns) Insert(_ context.Context, run *Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.rows[run.ID]; dup {
		return domain.ErrConflict
	}
	for _, existing := range r.rows {
		if existing.Task.RootBoardID == run.Task.RootBoardID && existing.State.Active() {
			return domain.ErrConflict
		}
	}
	run.Rev = 1
	cp := *run
	r.rows[run.ID] = &cp
	return nil
}

func (r *scRuns) Get(_ context.Context, id string) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	run, ok := r.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *run
	cp.TransactionIDs = append([]string(nil), run.TransactionIDs...)
	cp.RevertedElementIDs = append([]string(nil), run.RevertedElementIDs...)
	return &cp, nil
}

func (r *scRuns) Update(_ context.Context, run *Run, expectedRev int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.rows[run.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if stored.Rev != expectedRev {
		return domain.ErrConflict
	}
	run.Rev = stored.Rev + 1
	cp := *run
	r.rows[run.ID] = &cp
	return nil
}

func (r *scRuns) ActiveByBoard(_ context.Context, boardID string) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, run := range r.rows {
		if run.Task.RootBoardID == boardID && run.State.Active() {
			cp := *run
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

func (r *scRuns) ListByBoard(_ context.Context, tenant, boardID string, limit int) ([]*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Run
	for _, run := range r.rows {
		if run.Tenant != tenant {
			continue
		}
		if boardID != "" && run.Task.RootBoardID != boardID {
			continue
		}
		cp := *run
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

func (r *scRuns) Unfinished(context.Context) ([]*Run, error)   { return nil, nil }
func (r *scRuns) DeleteByTenant(context.Context, string) error { return nil }

type scEvents struct {
	mu  sync.Mutex
	seq int64
}

func (e *scEvents) Append(_ context.Context, ev *Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.seq++
	ev.Sequence = e.seq
	return nil
}
func (e *scEvents) List(context.Context, string, int64, int) ([]*Event, error) { return nil, nil }
func (e *scEvents) Aggregate(context.Context, string, time.Time, []EventType) (map[EventType]int64, error) {
	return nil, nil
}
func (e *scEvents) DeleteByTenant(context.Context, string) error { return nil }

// scBus records both channels, so a test can ask what a WATCHER saw as opposed
// to what the run's owner saw.
type scBus struct {
	mu    sync.Mutex
	room  []map[string]any
	owner []map[string]any
}

func (b *scBus) BroadcastEvent(_, _ string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := data.(map[string]any); ok {
		b.room = append(b.room, m)
	}
}

func (b *scBus) NotifyUser(_, _ string, data any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if m, ok := data.(map[string]any); ok {
		b.owner = append(b.owner, m)
	}
}

func (b *scBus) json(t *testing.T, frames []map[string]any) string {
	t.Helper()
	b.mu.Lock()
	defer b.mu.Unlock()
	raw, err := json.Marshal(frames)
	if err != nil {
		t.Fatalf("marshal frames: %v", err)
	}
	return string(raw)
}

// ---- harness ----------------------------------------------------------------

const scBoard = "9999999999999999999ab001"

type scHarness struct {
	svc       *Service
	elements  *memory.ElementRepo
	txns      *memory.TransactionRepo
	labels    *memory.LabelRepo
	comments  *memory.CommentRepo
	runs      *scRuns
	bus       *scBus
	principal *domain.Principal
}

func newSCHarness(t *testing.T, steps ...cognition.ScriptedStep) *scHarness {
	t.Helper()
	elements := memory.NewElementRepo()
	txnRepo := memory.NewTransactionRepo()
	labels := memory.NewLabelRepo()
	comments := memory.NewCommentRepo()
	access := service.NewAccessResolver(elements)
	bus := &scBus{}
	runs := newSCRuns()

	n := 0
	newID := func() string { n++; return fmt.Sprintf("%024x", 0xbee70000+n) }
	txnSvc := service.NewTransactionService(elements, txnRepo, access, nil,
		service.IDGenerator(newID), zap.NewNop())

	svc := NewService(Config{
		Elements: elements, Txns: txnSvc, TxnRepo: txnRepo, Access: access,
		Labels: labels, Comments: comments,
		Runs: runs, Events: &scEvents{},
		Provider: cognition.NewScripted(steps...), Bus: bus,
		NewID: newID, Log: zap.NewNop(),
	})
	return &scHarness{
		svc: svc, elements: elements, txns: txnRepo, labels: labels,
		comments: comments, runs: runs, bus: bus,
		principal: &domain.Principal{Sub: "alice", Name: "Alice"},
	}
}

func (h *scHarness) seed(t *testing.T, board string, texts ...string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: board, Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Production"},
		ACL:       &domain.ACL{OwnerID: "alice", Editors: []string{}},
		CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed board: %v", err)
	}
	for i, text := range texts {
		if err := h.elements.Insert(ctx, &domain.Element{
			ID: fmt.Sprintf("%sc%03x", board[:20], i+1), Type: domain.TypeCard,
			Location:  domain.Location{ParentID: board, Section: domain.SectionUnsorted, Index: float64(i)},
			Content:   domain.Content{"textPreview": text},
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed card: %v", err)
		}
	}
}

func (h *scHarness) await(t *testing.T, runID string, want ...RunState) *Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.svc.Get(context.Background(), h.principal, runID)
		if err == nil {
			for _, w := range want {
				if run.State == w {
					return run
				}
			}
			if run.State.Terminal() {
				t.Fatalf("run reached %s (%s), wanted %v", run.State, run.Reason, want)
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("run %s never reached %v", runID, want)
	return nil
}

func scColumn(title string) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
		{Name: "create_column", Input: map[string]any{"parentId": scBoard, "title": title}},
	}}
}

func scFinish(summary string) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
		{Name: "finish", Input: map[string]any{"summary": summary}},
	}}
}

// ---------------------------------------------------------------------------
// MP3 — the run journal was broadcast unredacted to every board viewer
// ---------------------------------------------------------------------------

// The privacy property was enforced only by the frontend choosing not to render
// fields it already had. Room membership needs RoleView, which a password-less
// view link satisfies with no account at all — so anyone with the board's link
// and a WebSocket could read every collaborator's prompts, revisions and mid-run
// corrections verbatim, live. Two code paths implemented opposite policies for
// the same data, and describeActim one file away implemented the right one.
//
// This asserts on the BROADCAST FRAMES, which is where the leak was.
func TestJournal_NoRoomFrameCarriesTheRequestText(t *testing.T) {
	const secret = "cut the DoP from episode three before the producer sees the budget"
	h := newSCHarness(t, scColumn("Editing"), scFinish("Made a column."), scFinish("Reviewed."))
	h.seed(t, scBoard, "a note")

	run, err := h.svc.Create(context.Background(), h.principal, CreateRequest{
		BoardID: scBoard, Intent: secret,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.await(t, run.ID, StateProposed)

	if len(h.bus.room) == 0 {
		t.Fatal("the room saw nothing at all; a watcher still needs to know a run is happening")
	}
	if got := h.bus.json(t, h.bus.room); strings.Contains(got, secret) {
		t.Fatalf("a room frame carries the person's own words verbatim:\n%s", got)
	}
	if got := h.bus.json(t, h.bus.owner); !strings.Contains(got, secret) {
		t.Fatal("redaction reached the owner's own channel; they can no longer see their own run")
	}

	// The allowlist, stated positively: a watcher learns the fact of the run and
	// where it has got to, and nothing else. A new field added to the payload is
	// private until somebody argues otherwise — that is what makes this an
	// allowlist rather than a list of things somebody remembered to remove.
	allowed := map[string]bool{"runId": true, "sequence": true, "state": true, "type": true, "at": true}
	for _, frame := range h.bus.room {
		for k := range frame {
			if !allowed[k] {
				t.Errorf("room frame carries %q, which is outside the room-safe set", k)
			}
		}
	}
}

// A revision note is the most private thing in the system — a correction typed
// while watching a run go wrong — and it rode the same board-wide channel.
func TestJournal_NoRoomFrameCarriesARevisionNote(t *testing.T) {
	const note = "no, keep the Ealing shoot out of this"
	h := newSCHarness(t,
		scColumn("Editing"), scFinish("Made a column."), scFinish("Reviewed."),
		scColumn("Sound"), scFinish("Revised."), scFinish("Reviewed."))
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, CreateRequest{BoardID: scBoard, Intent: "organise"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.await(t, run.ID, StateProposed)
	if _, err := h.svc.Refine(ctx, h.principal, proposed.ID, note, nil); err != nil {
		t.Fatalf("refine: %v", err)
	}
	if got := h.bus.json(t, h.bus.room); strings.Contains(got, note) {
		t.Fatalf("a revision note reached the board room:\n%s", got)
	}
}

// ---------------------------------------------------------------------------
// MP2 — an edit link minted agent authority
// ---------------------------------------------------------------------------

// A capability that mints capabilities is never granted by a bearer token.
// Anyone who forwarded the edit link handed a stranger a delegation plus a live
// model budget on the owner's board, and every downstream disclosure in this
// package was reachable from a pasted URL.
func TestAdmission_AShareLinkCannotStartARun(t *testing.T) {
	h := newSCHarness(t, scFinish("Nothing to do."), scFinish("Reviewed."))
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	board, err := h.elements.Get(ctx, scBoard)
	if err != nil {
		t.Fatalf("get board: %v", err)
	}
	board.ACL.PublicEditLink = "forwarded-edit-link"
	if err := h.elements.SetACL(ctx, scBoard, board.ACL); err != nil {
		t.Fatalf("set acl: %v", err)
	}

	stranger := &domain.Principal{Sub: "stranger", ShareToken: "forwarded-edit-link"}
	_, err = h.svc.Create(ctx, stranger, CreateRequest{
		BoardID: scBoard, Intent: "reorganise everything",
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("a pasted edit link started an agent run (err = %v)", err)
	}
	// The refusal has to say what to do next, or an editable board that refuses
	// the assistant reads as a bug.
	if !strings.Contains(err.Error(), "editor") {
		t.Errorf("the refusal names no way forward: %q", err.Error())
	}

	// The link still does what it was designed for.
	access := service.NewAccessResolver(h.elements)
	if _, err := access.RequireEdit(ctx, scBoard, stranger); err != nil {
		t.Fatalf("closing the delegation hole also closed the edit link: %v", err)
	}

	// And somebody named in the ACL is not caught by it.
	board, _ = h.elements.Get(ctx, scBoard)
	board.ACL.Editors = []string{"bob"}
	if err := h.elements.SetACL(ctx, scBoard, board.ACL); err != nil {
		t.Fatalf("set acl: %v", err)
	}
	if _, err := h.svc.Create(ctx, &domain.Principal{Sub: "bob"}, CreateRequest{
		BoardID: scBoard, Intent: "organise",
	}); errors.Is(err, domain.ErrForbidden) {
		t.Fatal("an editor named in the ACL was refused a run")
	}
}

// ---------------------------------------------------------------------------
// MP4 — ancestry read ancestor and sibling boards with no access check
// ---------------------------------------------------------------------------

// Sharing cascades DOWNWARD only — Resolve takes the max role over an element's
// own ancestor chain — and this walk ignored that entirely, reading straight off
// the repository. Share one sub-board with a contractor and their first run told
// them the parent workspace's name and every sibling project's.
func TestAncestry_ANestedOnlyPrincipalLearnsNothingAbove(t *testing.T) {
	const parent = "8888888888888888888ab001"
	const mine = "8888888888888888888ab002"
	const sibling = "8888888888888888888ab003"

	h := newSCHarness(t)
	ctx := context.Background()
	now := time.Now().UTC()
	mk := func(id, parentID, title string, editors []string) {
		if err := h.elements.Insert(ctx, &domain.Element{
			ID: id, Type: domain.TypeBoard,
			Location:  domain.Location{ParentID: parentID, Section: domain.SectionCanvas},
			Content:   domain.Content{"title": title},
			ACL:       &domain.ACL{OwnerID: "alice", Editors: editors},
			CreatedBy: "alice", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("seed %s: %v", id, err)
		}
	}
	mk(parent, "", "Acme Q3 Layoffs", nil)
	mk(mine, parent, "Contractor scratch", []string{"carol"})
	mk(sibling, parent, "Series B", nil)

	scopeFor := func(sub string) *BoardScope {
		t.Helper()
		scope, err := CompileScope(ctx, h.elements, TaskSpec{
			Owner: sub, RootBoardID: mine, Scope: ScopeBoard,
		})
		if err != nil {
			t.Fatalf("compile scope for %s: %v", sub, err)
		}
		h.svc.attachAncestry(ctx, &domain.Principal{Sub: sub}, scope)
		return scope
	}

	carol := scopeFor("carol")
	rendered := strings.Join(append(append([]string{}, carol.Ancestry...), carol.Siblings...), " | ")
	for _, secret := range []string{"Acme Q3 Layoffs", "Series B"} {
		if strings.Contains(rendered, secret) {
			t.Errorf("a nested-board-only principal was told about %q — %q", secret, rendered)
		}
	}

	// The owner still gets the context the feature exists for; the fix must not
	// be "stop reading ancestry".
	alice := scopeFor("alice")
	if len(alice.Ancestry) < 2 {
		t.Errorf("the owner lost their own workspace path: %v", alice.Ancestry)
	}
	if len(alice.Siblings) == 0 {
		t.Error("the owner lost the sibling list; 'put this with the others' has no referent")
	}
}

// ---------------------------------------------------------------------------
// FR3 — Stop pressed during a write
// ---------------------------------------------------------------------------

// StateApplying → StateCancelled was a legal edge and the UI offered Stop right
// through APPLYING and VERIFYING, so pressing it in the second the state flipped
// told the person — in two languages — that nothing had changed while forty
// elements landed on their board, and the run then had no transaction id
// recorded against it at all.
func TestCancel_RefusesOnceTheWriteHasBegun(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	run := &Run{
		ID: "cccccccccccccccccccccc01", Tenant: "alice",
		Task:      TaskSpec{Intent: "organise", Owner: "alice", RootBoardID: scBoard},
		State:     StateApplying,
		Active:    true,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.runs.Insert(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	_, err := h.svc.Cancel(ctx, h.principal, run.ID)
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("Stop was accepted mid-write (err = %v); the outcome card would say nothing changed", err)
	}
	// The refusal has to name the exit, or the person reaches for the reload
	// button — which is the one thing that destroys the last escape hatch.
	if !strings.Contains(err.Error(), "undo") {
		t.Errorf("the refusal offers no next step: %q", err.Error())
	}
	after, gerr := h.svc.Get(ctx, h.principal, run.ID)
	if gerr != nil {
		t.Fatalf("get: %v", gerr)
	}
	if after.State != StateApplying {
		t.Errorf("the refused Stop still moved the run to %s", after.State)
	}

	// Before the write begins, Stop still stops.
	early := &Run{
		ID: "cccccccccccccccccccccc09", Tenant: "alice",
		Task:      TaskSpec{Intent: "organise", Owner: "alice", RootBoardID: "7777777777777777777ab009"},
		State:     StateRunning,
		Active:    true,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.runs.Insert(ctx, early); err != nil {
		t.Fatalf("seed running run: %v", err)
	}
	stopped, err := h.svc.Cancel(ctx, h.principal, early.ID)
	if err != nil {
		t.Fatalf("Stop was refused on a run that had written nothing: %v", err)
	}
	if stopped.State != StateCancelled {
		t.Errorf("state after Stop = %s, want CANCELLED", stopped.State)
	}
}

// ---------------------------------------------------------------------------
// FR4 / FR18 — the exit out of a failed run, and a revert that half-completes
// ---------------------------------------------------------------------------

// The state machine modelled failure as "nothing happened", and three real paths
// produce failure with things having happened. The person was shown a card
// asserting, in their own language, that the board was untouched; there was no
// Undo, the API answered "conflict", and the audit view skipped the run.
//
// This reaches the bad state and proves the exit exists.
func TestRevert_AFailedRunThatWroteIsStillRevertible(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	const created = "9999999999999999999ac0aa"
	const txnID = "9999999999999999999add01"
	if _, err := h.commitOneCard(ctx, txnID, created); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	run := &Run{
		ID: "cccccccccccccccccccccc02", Tenant: "alice",
		Task:  TaskSpec{Intent: "organise", Owner: "alice", RootBoardID: scBoard},
		State: StateFailed, Reason: "the changes were rejected",
		TransactionIDs: []string{txnID},
		Plan: &Plan{Actions: []Action{
			{Seq: 1, Kind: ActCreateNote, ElementID: created, ParentID: scBoard},
		}},
		Delegation: scGrant("cccccccccccccccccccccc02"),
		CreatedAt:  time.Now().UTC(),
	}
	if err := h.runs.Insert(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	if _, err := h.svc.Revert(ctx, h.principal, run.ID); err != nil {
		t.Fatalf("a run that wrote and then failed could not be undone: %v", err)
	}
	el, err := h.elements.Get(ctx, created)
	if err == nil && !el.IsDeleted() {
		t.Fatal("the revert reported success and the element is still standing")
	}
	// The record has to say so too, or the review list re-offers rows that are
	// already gone.
	after, err := h.svc.Get(ctx, h.principal, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(after.RevertedElementIDs) != 1 || after.RevertedElementIDs[0] != created {
		t.Errorf("reverted ids = %v, want the element that was undone", after.RevertedElementIDs)
	}

	// And the honest refusal survives: a run that never wrote has nothing to
	// undo, and that is the ONLY case that should refuse.
	empty := &Run{
		ID: "cccccccccccccccccccccc03", Tenant: "alice",
		Task:      TaskSpec{Intent: "organise", Owner: "alice", RootBoardID: "7777777777777777777ab003"},
		State:     StateFailed,
		CreatedAt: time.Now().UTC(),
	}
	if err := h.runs.Insert(ctx, empty); err != nil {
		t.Fatalf("seed empty run: %v", err)
	}
	if _, err := h.svc.Revert(ctx, h.principal, empty.ID); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("reverting a run that wrote nothing answered %v", err)
	}
}

// A revert that fails partway used to accumulate its progress in memory and
// discard it on the error, so the person pressed Undo, saw a toast, and the list
// still showed every row as standing — then pressed the individual buttons on
// rows that had already been inverted.
func TestRevert_ProgressSurvivesAPartialFailure(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	const first = "9999999999999999999ac0bb"
	const firstTxn = "9999999999999999999add02"
	if _, err := h.commitOneCard(ctx, firstTxn, first); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}
	run := &Run{
		ID: "cccccccccccccccccccccc04", Tenant: "alice",
		Task:  TaskSpec{Intent: "organise", Owner: "alice", RootBoardID: scBoard},
		State: StateCompleted,
		// The second id names a transaction that is NOT in the journal, so the
		// revert undoes the first and then cannot read the second: exactly the
		// half-completed shape, without needing a fault-injecting repository.
		TransactionIDs: []string{"9999999999999999999add99", firstTxn},
		Plan: &Plan{Actions: []Action{
			{Seq: 1, Kind: ActCreateNote, ElementID: first, ParentID: scBoard},
		}},
		Delegation: scGrant("cccccccccccccccccccccc04"),
		CreatedAt:  time.Now().UTC(),
	}
	if err := h.runs.Insert(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	if _, err := h.svc.Revert(ctx, h.principal, run.ID); err == nil {
		t.Fatal("a revert that could not read one of its transactions reported success")
	}
	after, err := h.svc.Get(ctx, h.principal, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(after.RevertedElementIDs) != 1 || after.RevertedElementIDs[0] != first {
		t.Fatalf("reverted ids = %v; the half that DID come back was forgotten, so the UI re-offers it",
			after.RevertedElementIDs)
	}
	if after.RevertedAt == nil || after.RevertedAt[first].IsZero() {
		t.Error("no timestamp was stamped on the element that was undone")
	}
}

// commitOneCard puts one create through the REAL write path, so the journal row
// carries a real inverse rather than a hand-made one.
func (h *scHarness) commitOneCard(ctx context.Context, txnID, elementID string) (*domain.Transaction, error) {
	return h.svc.txns.ApplyWithMeta(ctx,
		&domain.Principal{Sub: "alice", Name: "Alice"}, scBoard, "", []domain.Op{{
			ElementID: elementID, Action: domain.ActionCreate,
			Changes: domain.Content{
				"type":     string(domain.TypeCard),
				"location": map[string]any{"parentId": scBoard, "section": string(domain.SectionCanvas)},
				"content":  map[string]any{"textPreview": "the agent made this"},
			},
		}}, service.TxnMeta{TxnID: txnID, Origin: domain.OriginAgent, AgentRunID: "seeded"})
}

// ---------------------------------------------------------------------------
// FR12 — the grant outlived by the offer
// ---------------------------------------------------------------------------

// The delegation expired at 30 minutes and the proposal lived for 2 hours, so
// for ninety minutes the product showed an Apply button guaranteed to fail:
// preview before lunch, apply after, and the answer was "the changes were
// rejected" with no mention of expiry and no way to re-authorise.
func TestApply_AProposalOlderThanTheOldGrantStillApplies(t *testing.T) {
	h := newSCHarness(t, scColumn("Editing"), scFinish("Made a column."), scFinish("Reviewed."))
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, CreateRequest{BoardID: scBoard, Intent: "organise"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.await(t, run.ID, StateProposed)

	// The two clocks are stamped in one place now, so they cannot diverge.
	if proposed.ProposalExpiresAt == nil {
		t.Fatal("a proposal with no deadline holds the board's run slot forever")
	}
	if proposed.Delegation.ExpiresAt.Before(*proposed.ProposalExpiresAt) {
		t.Fatalf("the grant (%s) expires before the offer it authorises (%s)",
			proposed.Delegation.ExpiresAt, *proposed.ProposalExpiresAt)
	}

	// Forty-five minutes later — past the old 30-minute grant, inside the offer.
	stored, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	stored.Delegation.ExpiresAt = time.Now().UTC().Add(-15 * time.Minute)
	if err := h.runs.Update(ctx, stored, stored.Rev); err != nil {
		t.Fatalf("age the grant: %v", err)
	}

	applied, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("applying a proposal past the old grant's expiry failed: %v", err)
	}
	if applied.State == StateFailed {
		t.Fatalf("the apply landed in FAILED: %s", applied.Reason)
	}
}

// The other direction, and the one that must still refuse: the person's own
// permission is gone. That is DENIED with a reason, not a generic failure.
func TestApply_AnExpiredGrantWithNoPermissionLeftIsDenied(t *testing.T) {
	h := newSCHarness(t, scColumn("Editing"), scFinish("Made a column."), scFinish("Reviewed."))
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, CreateRequest{BoardID: scBoard, Intent: "organise"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.await(t, run.ID, StateProposed)

	stored, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("get stored: %v", err)
	}
	stored.Delegation.ExpiresAt = time.Now().UTC().Add(-15 * time.Minute)
	if err := h.runs.Update(ctx, stored, stored.Rev); err != nil {
		t.Fatalf("age the grant: %v", err)
	}
	// Alice is no longer the owner of the board she proposed against.
	if err := h.elements.SetACL(ctx, scBoard,
		&domain.ACL{OwnerID: "somebody-else", Editors: []string{}}); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	if _, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil); err == nil {
		t.Fatal("a plan was applied by somebody who can no longer edit the board")
	}
	after, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if after.State != StateDenied {
		t.Errorf("state = %s, want DENIED — a refused authority is not a broken one", after.State)
	}
	if !strings.Contains(after.Reason, "permission") {
		t.Errorf("reason = %q, want it to name the permission", after.Reason)
	}
}

// ---------------------------------------------------------------------------
// FR13 — a failed apply left the labels it had already inserted
// ---------------------------------------------------------------------------

// The reasoning that put the label insert before the ops is right — the ops
// carry the label ids — and the conclusion drawn from it was wrong: "only here"
// protects against discard and revert and NOT against failure, which is the case
// where cleanup matters most. The residue is a label that appears in the filter
// bar and on nothing, one set per failed apply.
func TestApply_AFailedApplyTakesBackTheLabelsItCoined(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()

	run := &Run{
		ID: "cccccccccccccccccccccc05", Tenant: "alice",
		Task:  TaskSpec{Intent: "tag everything", Owner: "alice", RootBoardID: scBoard},
		State: StateApplying, Active: true,
		Plan: &Plan{
			Actions: []Action{{
				// A create whose parent is a board the run has no grant over:
				// compiles, and is refused by the write path.
				Seq: 1, Kind: ActCreateNote, ElementID: "9999999999999999999ac0cc",
				ParentID: "7777777777777777777ab077", Text: "orphan",
			}},
			NewLabels: []*domain.Label{{
				ID: "9999999999999999999ae001", OwnerID: "alice", Name: "Needs grade",
			}},
		},
		Delegation: scGrant("cccccccccccccccccccccc05"),
		CreatedAt:  time.Now().UTC(),
	}
	if err := h.runs.Insert(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}

	if _, err := h.svc.commit(ctx, h.principal, run, nil); err == nil {
		t.Fatal("a plan filing into a board outside the grant was accepted")
	}
	if _, err := h.labels.Get(ctx, "9999999999999999999ae001"); err == nil {
		t.Fatal("a label coined by a failed apply is still in the person's taxonomy, on nothing")
	}
}

// scGrant is the delegation admission mints for every real run. Seeded runs
// carry one because the write path checks it: a hand-made run with a zero grant
// is refused for the wrong reason, which would make these tests pass on the bug
// they exist to catch.
func scGrant(runID string) Delegation {
	return Delegation{
		RunID: runID, OnBehalfOf: "alice", RootBoardID: scBoard,
		Capabilities: []domain.Capability{
			domain.CapElementCreate, domain.CapElementUpdate,
			domain.CapElementMove, domain.CapElementDelete,
		},
		Consequence: domain.ConsequenceDestructive,
		MaxOps:      240,
		ExpiresAt:   time.Now().UTC().Add(time.Hour),
	}
}

// ---------------------------------------------------------------------------
// FR9 — terminal states that nothing produced
// ---------------------------------------------------------------------------

// Three of the nine terminal states were unreachable dead code, with full
// bilingual copy and dedicated UI branches: DENIED and SECURITY_QUARANTINED
// appeared only in the state list and the transition table, and nothing ever
// transitioned to either. So a provider outage, a deadline, a precondition
// rejection, an expired grant and a rejected write all arrived at the person as
// the identical red card — and they learned that the agent breaks at random,
// which is precisely the lesson the FAILED/DENIED split was introduced to
// prevent. The split was built in the renderer and never in the producer.
//
// ---------------------------------------------------------------------------
// DA20 — one stale element threw away the whole plan
// ---------------------------------------------------------------------------

// Exact-action binding is right and the response to it was all-or-nothing: a
// colleague touching ONE card while you read the review threw away every other
// change in the plan, and the only exit was to discard the run and pay for
// another. On a two-person board that made a thirty-action plan nearly
// un-appliable, and it is why a scheduled run and a person working at the same
// time starve each other.
//
// What must never happen instead is a silent partial, so the skipped row becomes
// a named unmet.
func scRetext(elementID, text string) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
		{Name: "set_note_text", Input: map[string]any{"elementId": elementID, "text": text}},
	}}
}

func TestApply_AStaleElementLosesItsOwnRowAndNotThePlan(t *testing.T) {
	const cardA = "9999999999999999999ac001"
	const cardB = "9999999999999999999ac002"
	h := newSCHarness(t,
		scRetext(cardA, "tightened"),
		scRetext(cardB, "tightened too"),
		scFinish("Rewrote both notes."),
		scFinish("Reviewed."),
	)
	h.seed(t, scBoard, "first draft", "second draft")
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.principal, CreateRequest{BoardID: scBoard, Intent: "tighten the notes"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.await(t, run.ID, StateProposed)
	if len(proposed.Plan.Actions) != 2 {
		t.Fatalf("plan has %d actions, want the two rewrites", len(proposed.Plan.Actions))
	}

	// Sara edits one of the two cards while Alice is reading the review.
	if _, err := h.elements.MergePatch(ctx, cardA,
		domain.Content{"content": map[string]any{"textPreview": "sara got there first"}}); err != nil {
		t.Fatalf("collaborator edit: %v", err)
	}

	applied, err := h.svc.Apply(ctx, h.principal, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("one stale card refused the whole plan: %v", err)
	}
	if applied.State == StateProposed {
		t.Fatal("the plan bounced back to review over a card it could have simply left alone")
	}

	// B was rewritten; A was left exactly as Sara wrote it.
	b, err := h.elements.Get(ctx, cardB)
	if err != nil {
		t.Fatalf("read B: %v", err)
	}
	if b.Content["textPreview"] != "tightened too" {
		t.Errorf("the untouched card was not rewritten: %v", b.Content["textPreview"])
	}
	a, err := h.elements.Get(ctx, cardA)
	if err != nil {
		t.Fatalf("read A: %v", err)
	}
	if a.Content["textPreview"] != "sara got there first" {
		t.Errorf("the plan wrote over a card that had changed under it: %v", a.Content["textPreview"])
	}

	// And it SAYS so. A partial that reports itself as complete is worse than
	// the abort it replaced.
	stored, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if len(stored.Plan.Unmet) == 0 {
		t.Fatal("the run did less than it showed and recorded no unmet request")
	}
	if len(stored.StaleElementIDs) == 0 {
		t.Error("nothing names WHICH element was left alone")
	}
}

// ---------------------------------------------------------------------------
// FR15c — retry is unbounded and uninformed
// ---------------------------------------------------------------------------

// Retry is the primary action on every FAILED / BUDGET_EXHAUSTED / CANCELLED /
// DENIED card, and it wrote a row that looked like a first attempt: no link back
// to what it was redoing, so the retry rate — the cleanest per-capability
// failure signal there is — read as zero, and nothing anywhere noticed that the
// same request had now failed three times. When the provider is down, pressing
// it is a loop: press, wait up to eight minutes, receive the identical sentence,
// press again, each pass charged at full price against the daily cap.
func seedTerminalRun(t *testing.T, h *scHarness, id, retryOf, reason string) *Run {
	t.Helper()
	run := &Run{
		ID: id, Tenant: "alice",
		Task:         TaskSpec{Intent: "organise the board", Owner: "alice", RootBoardID: scBoard},
		State:        StateFailed,
		Reason:       reason,
		RetryOfRunID: retryOf,
		CreatedAt:    time.Now().UTC(),
	}
	if err := h.runs.Insert(context.Background(), run); err != nil {
		t.Fatalf("seed run %s: %v", id, err)
	}
	return run
}

func TestRetry_LinksTheNewRunToTheOneItIsRedoing(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()
	seedTerminalRun(t, h, "aaaaaaaaaaaaaaaaaaaaaa01", "", "the AI service is unavailable right now")

	again, err := h.svc.Retry(ctx, h.principal, "aaaaaaaaaaaaaaaaaaaaaa01")
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if again.RetryOfRunID != "aaaaaaaaaaaaaaaaaaaaaa01" {
		t.Errorf("retryOfRunId = %q; a retry that records no lineage is a retry nothing can count",
			again.RetryOfRunID)
	}
}

func TestRetry_RefusesTheThirdIdenticalAttempt(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()
	const same = "the AI service is unavailable right now"
	seedTerminalRun(t, h, "aaaaaaaaaaaaaaaaaaaaaa01", "", same)
	seedTerminalRun(t, h, "aaaaaaaaaaaaaaaaaaaaaa02", "aaaaaaaaaaaaaaaaaaaaaa01", same)

	_, err := h.svc.Retry(ctx, h.principal, "aaaaaaaaaaaaaaaaaaaaaa02")
	if err == nil {
		t.Fatal("a third identical attempt was accepted; that is another eight minutes and another charge for the same sentence")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("err = %v, want a conflict", err)
	}
	// Saying no is only half of it. The point of having noticed is to say what
	// was noticed.
	if !strings.Contains(err.Error(), same) {
		t.Errorf("the refusal does not name what keeps happening: %q", err.Error())
	}
}

// Two DIFFERENT failures are two real attempts. The bound is on sameness, not
// on count, or a person who fixed the first problem cannot try again.
func TestRetry_ADifferentFailureStillRuns(t *testing.T) {
	h := newSCHarness(t)
	h.seed(t, scBoard, "a note")
	ctx := context.Background()
	seedTerminalRun(t, h, "aaaaaaaaaaaaaaaaaaaaaa01", "", "the AI service is unavailable right now")
	seedTerminalRun(t, h, "aaaaaaaaaaaaaaaaaaaaaa02", "aaaaaaaaaaaaaaaaaaaaaa01", "the run ran out of time")

	if _, err := h.svc.Retry(ctx, h.principal, "aaaaaaaaaaaaaaaaaaaaaa02"); err != nil {
		t.Fatalf("a retry after a DIFFERENT failure was refused: %v", err)
	}
}

// A source probe rather than a runtime one, because the property is "somebody
// can reach this", and the honest way to check that is to look at whether
// anybody writes it. It fails the day a state is added with no way in.
func TestStateMachine_EveryTerminalStateHasAProducer(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	var src strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, rerr := os.ReadFile(name)
		if rerr != nil {
			t.Fatalf("read %s: %v", name, rerr)
		}
		src.Write(body)
	}
	text := src.String()

	// Every terminal state, and the identifier a producer would name it by.
	terminal := map[RunState]string{
		StateCompleted:  "StateCompleted",
		StatePartial:    "StatePartial",
		StateDiscarded:  "StateDiscarded",
		StateCancelled:  "StateCancelled",
		StateFailed:     "StateFailed",
		StateDenied:     "StateDenied",
		StateExhausted:  "StateExhausted",
		StateQuarantine: "StateQuarantine",
		StateReverted:   "StateReverted",
	}
	for state, ident := range terminal {
		produced := strings.Contains(text, "finishWithReason(ctx, run, "+ident) ||
			strings.Contains(text, "transition(ctx, run, "+ident) ||
			strings.Contains(text, "return "+ident) ||
			strings.Contains(text, "return terminalFor(err), reasonFor(err)")
		if !produced {
			t.Errorf("%s is a terminal state nothing in this package ever assigns — "+
				"it ships with copy and a UI branch and cannot be reached", state)
		}
	}

	// And the reverse: a state the transition table can reach from somewhere.
	for state := range terminal {
		var reachable bool
		for _, targets := range allowedTransitions {
			for _, to := range targets {
				if to == state {
					reachable = true
				}
			}
		}
		if !reachable {
			t.Errorf("%s is not the target of any edge in allowedTransitions; "+
				"every producer of it has to force the machine", state)
		}
	}
}

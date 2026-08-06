package agent

// Regressions for the multiplayer and platform wave: everything that was
// correct for one person on one board and wrong the moment somebody pressed
// Share, plus the read paths a machine caller needs.
//
// IN-PACKAGE for the same reason the frontier file next door is: several of
// these have to reach a state no scripted run produces — a run filed under one
// person on another person's board, a transaction older than the element it
// created — and inventing an exported surface to seed them would be inventing
// an exported surface for a test.

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/realtime"
	"qomranote/backend/internal/repository/memory"
	"qomranote/backend/internal/service"
)

// ---- a run store that implements the optional halves --------------------------
//
// The frontier file's scRuns implements RunStore and nothing else, which is
// exactly the adapter the fallbacks exist for. These tests are about the
// capabilities themselves, so this double implements all three.

type mpRuns struct {
	mu   sync.Mutex
	rows map[string]*Run
}

func newMPRuns() *mpRuns { return &mpRuns{rows: map[string]*Run{}} }

var (
	_ RunStore           = (*mpRuns)(nil)
	_ RunOverlapStore    = (*mpRuns)(nil)
	_ RunBoardOwnerStore = (*mpRuns)(nil)
	_ RunAnalyticsStore  = (*mpRuns)(nil)
)

func (r *mpRuns) Insert(_ context.Context, run *Run) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.rows[run.ID]; dup {
		return domain.ErrConflict
	}
	run.Rev = 1
	cp := *run
	r.rows[run.ID] = &cp
	return nil
}

func (r *mpRuns) Get(_ context.Context, id string) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[id]
	if !ok {
		return nil, domain.ErrNotFound
	}
	cp := *row
	return &cp, nil
}

func (r *mpRuns) Update(_ context.Context, run *Run, expectedRev int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	row, ok := r.rows[run.ID]
	if !ok {
		return domain.ErrNotFound
	}
	if row.Rev != expectedRev {
		return domain.ErrConflict
	}
	run.Rev = expectedRev + 1
	cp := *run
	r.rows[run.ID] = &cp
	return nil
}

func (r *mpRuns) ActiveByBoard(_ context.Context, boardID string) (*Run, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if row.Task.RootBoardID == boardID && row.State.Active() {
			cp := *row
			return &cp, nil
		}
	}
	return nil, domain.ErrNotFound
}

// ActiveOverlapping is the storage half of the invariant: a run conflicts if
// its root is in my chain, or mine is in its.
func (r *mpRuns) ActiveOverlapping(_ context.Context, boardID string, ancestorIDs []string) (*Run, error) {
	chain := map[string]bool{boardID: true}
	for _, id := range ancestorIDs {
		chain[id] = true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, row := range r.rows {
		if !row.State.Active() {
			continue
		}
		if chain[row.Task.RootBoardID] {
			cp := *row
			return &cp, nil
		}
		for _, a := range row.Task.AncestorIDs {
			if a == boardID {
				cp := *row
				return &cp, nil
			}
		}
	}
	return nil, domain.ErrNotFound
}

func (r *mpRuns) ListByBoard(_ context.Context, tenant, boardID string, limit int) ([]*Run, error) {
	return r.filter(func(row *Run) bool {
		return row.Tenant == tenant && (boardID == "" || row.Task.RootBoardID == boardID)
	}, limit), nil
}

func (r *mpRuns) ListByBoardOwner(_ context.Context, ownerSub, boardID string, limit int) ([]*Run, error) {
	return r.filter(func(row *Run) bool {
		return row.BoardOwnerSub == ownerSub && (boardID == "" || row.Task.RootBoardID == boardID)
	}, limit), nil
}

func (r *mpRuns) ListByTenant(_ context.Context, tenant string, f RunFilter, limit int) ([]*Run, error) {
	return r.filter(func(row *Run) bool {
		if row.Tenant != tenant {
			return false
		}
		if !f.Since.IsZero() && row.CreatedAt.Before(f.Since) {
			return false
		}
		if f.BoardID != "" && row.Task.RootBoardID != f.BoardID {
			return false
		}
		return true
	}, limit), nil
}

func (r *mpRuns) AggregateUsage(ctx context.Context, tenant string, f RunFilter) (UsageRollup, error) {
	rows, _ := r.ListByTenant(ctx, tenant, f, 1000)
	out := UsageRollup{ByState: map[RunState]int64{}}
	for _, row := range rows {
		out.Runs++
		out.CostUSD += row.Usage.CostUSD
		out.ByState[row.State]++
	}
	return out, nil
}

func (r *mpRuns) filter(keep func(*Run) bool, limit int) []*Run {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []*Run
	for _, row := range r.rows {
		if keep(row) {
			cp := *row
			out = append(out, &cp)
		}
	}
	sortRunsNewestFirst(out)
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

func (r *mpRuns) Unfinished(context.Context) ([]*Run, error) { return nil, nil }

func (r *mpRuns) DeleteByTenant(_ context.Context, tenant string) error {
	return r.deleteWhere(func(row *Run) bool { return row.Tenant == tenant })
}

func (r *mpRuns) DeleteByBoardOwner(_ context.Context, owner string) error {
	return r.deleteWhere(func(row *Run) bool { return row.BoardOwnerSub == owner })
}

func (r *mpRuns) deleteWhere(match func(*Run) bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, row := range r.rows {
		if match(row) {
			delete(r.rows, id)
		}
	}
	return nil
}

func (r *mpRuns) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// ---- notifier double ---------------------------------------------------------

type mpNotes struct {
	mu   sync.Mutex
	rows []*domain.Notification
}

func (n *mpNotes) Insert(_ context.Context, note *domain.Notification) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	cp := *note
	n.rows = append(n.rows, &cp)
	return nil
}

func (n *mpNotes) ListByUser(context.Context, string, bool, int) ([]*domain.Notification, error) {
	return nil, nil
}
func (n *mpNotes) MarkRead(context.Context, string, []string) error { return nil }
func (n *mpNotes) DeleteByUser(context.Context, string) error       { return nil }

func (n *mpNotes) forUser(sub string) []*domain.Notification {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []*domain.Notification
	for _, row := range n.rows {
		if row.UserID == sub {
			out = append(out, row)
		}
	}
	return out
}

// ---- harness -----------------------------------------------------------------

const (
	mpParent = "9999999999999999999cc001"
	mpChild  = "9999999999999999999cc002"
)

type mpHarness struct {
	svc      *Service
	elements *memory.ElementRepo
	txns     *memory.TransactionRepo
	labels   *memory.LabelRepo
	runs     *mpRuns
	notes    *mpNotes
	owner    *domain.Principal
	editor   *domain.Principal
	stranger *domain.Principal
	newID    func() string
}

func newMPHarness(t *testing.T, steps ...cognition.ScriptedStep) *mpHarness {
	t.Helper()
	elements := memory.NewElementRepo()
	txnRepo := memory.NewTransactionRepo()
	labels := memory.NewLabelRepo()
	access := service.NewAccessResolver(elements)
	runs := newMPRuns()
	notes := &mpNotes{}

	n := 0
	newID := func() string { n++; return fmt.Sprintf("%024x", 0xd0e70000+n) }
	txnSvc := service.NewTransactionService(elements, txnRepo, access, nil,
		service.IDGenerator(newID), zap.NewNop())
	notifier := service.NewNotifier(notes, nil)
	txnSvc.AttachNotifier(notifier)

	svc := NewService(Config{
		Elements: elements, Txns: txnSvc, TxnRepo: txnRepo, Access: access,
		Labels: labels,
		Runs:   runs, Events: &scEvents{},
		Provider: cognition.NewScripted(steps...), Bus: &scBus{},
		NewID: newID, Log: zap.NewNop(), Notifier: notifier,
	})
	return &mpHarness{
		svc: svc, elements: elements, txns: txnRepo, labels: labels,
		runs: runs, notes: notes, newID: newID,
		owner:    &domain.Principal{Sub: "owner", Name: "Omar"},
		editor:   &domain.Principal{Sub: "editor", Name: "Sara"},
		stranger: &domain.Principal{Sub: "nobody", Name: "Nobody"},
	}
}

// seedShared builds the shape every finding in this corner needs and no probe
// in the corpus ever had: a board owned by one person, edited by another, with
// a nested board inside it.
func (h *mpHarness) seedShared(t *testing.T, policy *domain.AgentPolicy) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: mpParent, Type: domain.TypeBoard,
		Content:   domain.Content{"title": "Pre-Production"},
		ACL:       &domain.ACL{OwnerID: "owner", Editors: []string{"editor"}, AgentPolicy: policy},
		CreatedBy: "owner", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed parent: %v", err)
	}
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: mpChild, Type: domain.TypeBoard,
		Location:  domain.Location{ParentID: mpParent, Section: domain.SectionCanvas},
		Content:   domain.Content{"title": "Shot list"},
		CreatedBy: "owner", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: mpParent[:20] + "e001", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: mpParent, Section: domain.SectionUnsorted},
		Content:   domain.Content{"textPreview": "location scout"},
		CreatedBy: "owner", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed card: %v", err)
	}
}

func (h *mpHarness) awaitState(t *testing.T, p *domain.Principal, runID string, want ...RunState) *Run {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		run, err := h.svc.load(context.Background(), p, runID)
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

func mpColumn(board, title string) cognition.ScriptedStep {
	return cognition.ScriptedStep{Tools: []cognition.ScriptedCall{
		{Name: "create_column", Input: map[string]any{"parentId": board, "title": title}},
	}}
}

// ---------------------------------------------------------------------------
// MP1 — consent is a property of the (board, principal) pair
// ---------------------------------------------------------------------------

// RequireEdit was the ONLY consent gate on a run, so an editor could set
// autonomy=auto and have thirty model-chosen actions land unpreviewed on
// somebody else's board — while the owner had no way to express "preview only".
//
// A silent downgrade rather than a refusal is the design decision: a hard
// refusal makes the assistant look broken on every shared board.
func TestConsent_AnEditorAskingForAnUnattendedRunIsDowngradedNotRefused(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("Made a column."), scFinish("Reviewed."))
	h.seedShared(t, nil) // no policy at all — the DEFAULT must already bind

	run, err := h.svc.Create(context.Background(), h.editor, CreateRequest{
		BoardID: mpParent, Intent: "tidy this up", Autonomy: AutonomyAuto,
	})
	if err != nil {
		t.Fatalf("an editor's request must not be refused outright: %v", err)
	}
	if run.Task.Autonomy != AutonomyPreview {
		t.Fatalf("an editor got an unattended run on somebody else's board: autonomy=%s", run.Task.Autonomy)
	}
	if run.Task.AutonomyNote == "" {
		t.Fatal("the downgrade was silent — a plan that appears instead of applying, with no reason, reads as a bug")
	}
	if !run.Delegation.RequiresApproval {
		t.Fatal("the write path has no independent copy of the decision, so a later path could restore auto")
	}
	// And the run itself must stop at PROPOSED rather than writing.
	h.awaitState(t, h.editor, run.ID, StateProposed)
}

// The owner keeps what they always had, or the fix is a regression dressed as a
// security property.
func TestConsent_TheOwnerMayStillRunUnattended(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("Made a column."), scFinish("Reviewed."))
	h.seedShared(t, nil)

	run, err := h.svc.Create(context.Background(), h.owner, CreateRequest{
		BoardID: mpParent, Intent: "tidy this up", Autonomy: AutonomyAuto,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.Task.Autonomy != AutonomyAuto {
		t.Fatalf("the board's owner was downgraded on their own board: %s", run.Task.Autonomy)
	}
	if run.Delegation.RequiresApproval {
		t.Fatal("the owner's own unattended run was marked as needing somebody's approval")
	}
}

// "No AI on this board" is a legitimate requirement for a client-facing or
// contractual board and the product could not express it at all.
func TestConsent_TheOwnerCanTurnTheAssistantOffEntirely(t *testing.T) {
	h := newMPHarness(t, scFinish("nothing"))
	h.seedShared(t, &domain.AgentPolicy{Allow: domain.AgentAllowNone})

	_, err := h.svc.Create(context.Background(), h.owner, CreateRequest{
		BoardID: mpParent, Intent: "tidy this up",
	})
	if err == nil {
		t.Fatal("a board with the assistant switched off admitted a run")
	}
	if !strings.Contains(err.Error(), "turned the assistant off") {
		t.Fatalf("the refusal names the mechanism rather than the decision: %v", err)
	}
}

// allow=owner is the middle setting: the owner works, the editor does not.
func TestConsent_OwnerOnlyKeepsEditorsOut(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("done"), scFinish("reviewed"))
	h.seedShared(t, &domain.AgentPolicy{Allow: domain.AgentAllowOwner})

	if _, err := h.svc.Create(context.Background(), h.editor, CreateRequest{
		BoardID: mpParent, Intent: "tidy",
	}); err == nil {
		t.Fatal("an editor started a run on a board reserved to its owner")
	}
	if _, err := h.svc.Create(context.Background(), h.owner, CreateRequest{
		BoardID: mpParent, Intent: "tidy",
	}); err != nil {
		t.Fatalf("the owner was locked out of their own board: %v", err)
	}
}

// ---------------------------------------------------------------------------
// MP5 — the single-run guard was keyed on the exact root board
// ---------------------------------------------------------------------------

// Scope is the whole SUBTREE and the guard was one equality, so a second person
// starting a run rooted at a nested board passed it trivially and both wrote the
// same elements concurrently. Deep subtrees are the normal shape of a board now,
// so nesting is exactly where the content lives.
func TestConcurrency_ARunOnTheParentBlocksOneOnTheChild(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("done"), scFinish("reviewed"))
	h.seedShared(t, nil)
	ctx := context.Background()

	first, err := h.svc.Create(ctx, h.owner, CreateRequest{BoardID: mpParent, Intent: "organise"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	h.awaitState(t, h.owner, first.ID, StateProposed)

	_, err = h.svc.Create(ctx, h.editor, CreateRequest{BoardID: mpChild, Intent: "organise the shot list"})
	if err == nil {
		t.Fatal("a run rooted one level down walked straight past the single-run guard")
	}
	// The refusal has to name the board, or "someone else started one" on a
	// board the person is not looking at is indistinguishable from a bug.
	if !strings.Contains(err.Error(), "Pre-Production") {
		t.Fatalf("the refusal does not name the overlapping board: %v", err)
	}
}

// And the other direction, which the ancestor-walk fallback cannot see: a run
// rooted at the CHILD must block one rooted at the parent.
func TestConcurrency_ARunOnTheChildBlocksOneOnTheParent(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpChild, "Day one"), scFinish("done"), scFinish("reviewed"))
	h.seedShared(t, nil)
	ctx := context.Background()

	first, err := h.svc.Create(ctx, h.owner, CreateRequest{BoardID: mpChild, Intent: "organise"})
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	if len(first.Task.AncestorIDs) == 0 || first.Task.AncestorIDs[0] != mpParent {
		t.Fatalf("the run did not record its containment chain: %v", first.Task.AncestorIDs)
	}
	h.awaitState(t, h.owner, first.ID, StateProposed)

	if _, err := h.svc.Create(ctx, h.editor, CreateRequest{BoardID: mpParent, Intent: "organise"}); err == nil {
		t.Fatal("a run whose subtree contains a live run was admitted")
	}
}

// ---------------------------------------------------------------------------
// MP6 / DL21 — the run has a public half, and the board owner owns it
// ---------------------------------------------------------------------------

// seedForeignRun files a COMPLETED run under one person on another's board,
// with a real journal row behind it. That pair — tenant ≠ board owner — is the
// shape the whole corner is about and no scripted run can produce it alone.
func (h *mpHarness) seedForeignRun(t *testing.T, tenant, boardOwner, elementID string, at time.Time) *Run {
	t.Helper()
	ctx := context.Background()

	// The element and the journal row are seeded directly, both stamped at the
	// same past moment. Going through the write path would stamp the element
	// NOW and leave the transaction backdated, which is precisely the
	// "edited since" condition these tests have to be able to switch on and off.
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: elementID, Type: domain.TypeColumn,
		Location:  domain.Location{ParentID: mpParent, Section: domain.SectionCanvas},
		Content:   domain.Content{"title": "Editing"},
		CreatedBy: tenant, CreatedAt: at, UpdatedAt: at,
	}); err != nil {
		t.Fatalf("seed column: %v", err)
	}
	txnID := "txn" + elementID
	if err := h.txns.Insert(ctx, &domain.Transaction{
		ID: txnID, BoardID: mpParent, UserID: tenant,
		Ops: []domain.Op{{
			ElementID: elementID, Action: domain.ActionCreate,
			Changes: domain.Content{
				"type":     string(domain.TypeColumn),
				"location": map[string]any{"parentId": mpParent, "section": string(domain.SectionCanvas)},
				"content":  map[string]any{"title": "Editing"},
			},
		}},
		Origin: domain.OriginAgent, AgentRunID: "run" + elementID, CreatedAt: at,
	}); err != nil {
		t.Fatalf("seed transaction: %v", err)
	}

	run := &Run{
		ID: "run" + elementID, Tenant: tenant, BoardOwnerSub: boardOwner,
		Task: TaskSpec{
			Intent: "a secret request nobody else may read", Owner: tenant,
			RootBoardID: mpParent, Scope: ScopeBoard, Autonomy: AutonomyPreview,
			Budget: DefaultBudget(),
		},
		State: StateCompleted, Active: false,
		// The grant a real admission would have minted. Without a capability set
		// and an op ceiling the revert's compensating write is refused by the
		// delegation guard, which would make this fixture test the guard rather
		// than the thing under test.
		Delegation: Delegation{
			RunID: "run" + elementID, OnBehalfOf: tenant, RootBoardID: mpParent,
			Capabilities: []domain.Capability{
				domain.CapElementCreate, domain.CapElementUpdate,
				domain.CapElementMove, domain.CapElementDelete,
			},
			Consequence: domain.ConsequenceDestructive,
			MaxOps:      120,
			ContentKeys: ContentKeyAllowance(),
			ExpiresAt:   time.Now().UTC().Add(time.Hour),
		},
		Plan: &Plan{Summary: "made a column", Actions: []Action{
			{Kind: ActCreateColumn, ElementID: elementID, ParentID: mpParent, Title: "Editing"},
		}},
		TransactionIDs: []string{txnID},
		CreatedAt:      at, UpdatedAt: at,
	}
	if err := h.runs.Insert(ctx, run); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	return run
}

// Per-run revert is the product's central trust promise and it was unavailable
// to the person with the most at stake: the tenant gate meant an owner watching
// forty elements appear from an editor's run had to ask that editor to undo it.
func TestOwnerAudit_TheOwnerCanRevertACollaboratorsRun(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	col := mpParent[:20] + "f001"
	run := h.seedForeignRun(t, "editor", "owner", col, time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()

	out, err := h.svc.Revert(ctx, h.owner, run.ID)
	if err != nil {
		t.Fatalf("the board's owner could not undo a run on their own board: %v", err)
	}
	// The public half only. The owner may undo the EFFECTS without reading the
	// requester's own words.
	if out.Task.Intent != "" {
		t.Fatalf("reverting somebody else's run handed over their request text: %q", out.Task.Intent)
	}
	if out.Plan != nil {
		t.Fatal("reverting somebody else's run handed over their plan")
	}
	el, err := h.elements.Get(ctx, col)
	if err != nil {
		t.Fatalf("read reverted element: %v", err)
	}
	if !el.IsDeleted() {
		t.Fatal("the revert reported success and the column is still standing")
	}
	// And the author is told, because discovering it on the canvas is the
	// multiplayer failure this whole corner is about.
	if len(h.notes.forUser("editor")) == 0 {
		t.Fatal("somebody else undid this person's run and nothing told them")
	}
}

// A stranger must not be able to probe a run id for existence, let alone undo it.
func TestOwnerAudit_AStrangerStillCannotTouchTheRun(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	run := h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f002", time.Now().UTC().Add(-time.Hour))

	if _, err := h.svc.Revert(context.Background(), h.stranger, run.ID); err == nil {
		t.Fatal("somebody with no access to the board reverted a run on it")
	}
}

// The audit view is "what has the AI changed HERE" and it answered "what have
// YOU changed here", which is the one question it does not exist to answer.
func TestOwnerAudit_TheBoardViewShowsEveryonesRunsAndNobodysWords(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f003", time.Now().UTC().Add(-time.Hour))

	entries, err := h.svc.Audit(context.Background(), h.owner, mpParent, 25)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("the owner's audit view is empty on a board a collaborator's run rewrote")
	}
	for _, e := range entries {
		if e.Mine {
			continue
		}
		if e.Intent != "" {
			t.Fatalf("the audit view leaked a collaborator's request text: %q", e.Intent)
		}
		if e.CostUSD != 0 {
			t.Fatal("the audit view leaked a collaborator's spend")
		}
		if e.Ops == 0 {
			t.Fatal("a collaborator's run is listed with no account of what it did")
		}
	}
}

// The public view is the same split, exposed as its own object.
func TestOwnerAudit_ThePublicViewCarriesEffectsAndNotWords(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f004", time.Now().UTC().Add(-time.Hour))

	views, err := h.svc.BoardRuns(context.Background(), h.owner, mpParent, 25)
	if err != nil {
		t.Fatalf("board runs: %v", err)
	}
	if len(views) != 1 {
		t.Fatalf("wanted one public view, got %d", len(views))
	}
	v := views[0]
	if v.Mine {
		t.Fatal("a collaborator's run is reported as the reader's own")
	}
	if !v.Revertible {
		t.Fatal("a run with a standing transaction is reported as not revertible")
	}
	if v.Ops != 1 {
		t.Fatalf("the public view lost the op count: %d", v.Ops)
	}
}

// Erasure has to run on BOTH keys. Your board, their run: the row is filed
// under their sub, so a tenant-keyed purge cannot see it, and a verbatim
// partial copy of your board outlived your account.
func TestPurge_DeletingYourAccountReachesACollaboratorsRunOnYourBoard(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f005", time.Now().UTC().Add(-time.Hour))
	if h.runs.count() != 1 {
		t.Fatalf("setup: wanted one run, got %d", h.runs.count())
	}

	if err := h.svc.PurgeTenant(context.Background(), "owner"); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if h.runs.count() != 0 {
		t.Fatal("the board owner deleted their account and a collaborator's copy of their content survived")
	}
}

// ---------------------------------------------------------------------------
// MP15 — revert re-checks what commit checked
// ---------------------------------------------------------------------------

// The agent creates a column, a colleague spends an hour filling it, the
// original requester presses Undo, and the column plus everything in it goes to
// trash. The inverse ops were computed at commit time against a board that no
// longer exists, and were replayed with no staleness comparison at all — while
// the commit path runs a fingerprint check.
func TestRevert_LeavesAloneWhatSomebodyHasEditedSince(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	col := mpParent[:20] + "f006"
	run := h.seedForeignRun(t, "editor", "owner", col, time.Now().UTC().Add(-time.Hour))

	ctx := context.Background()
	// The colleague's hour of work: an ordinary edit, stamped now, long after
	// the run's transaction.
	if _, err := h.elements.MergePatch(ctx, col, domain.Content{
		"content": map[string]any{"title": "Editing — locked for the grade"},
	}); err != nil {
		t.Fatalf("colleague edit: %v", err)
	}

	if _, err := h.svc.Revert(ctx, h.editor, run.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	el, err := h.elements.Get(ctx, col)
	if err != nil {
		t.Fatalf("read element: %v", err)
	}
	if el.IsDeleted() {
		t.Fatal("undo destroyed an element somebody had edited after the run wrote it")
	}
	// And the run has to SAY so, or a silent partial undo is a lie about what
	// the button did.
	fresh, err := h.runs.Get(ctx, run.ID)
	if err != nil {
		t.Fatalf("reload run: %v", err)
	}
	if len(fresh.StaleElementIDs) == 0 {
		t.Fatal("the revert skipped an element and recorded nothing about it")
	}
}

// A container that gained children the run did not create is emptied-but-kept
// rather than trashed with a colleague's work inside it.
func TestRevert_KeepsAContainerThatGainedSomebodyElsesChildren(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	col := mpParent[:20] + "f007"
	run := h.seedForeignRun(t, "editor", "owner", col, time.Now().UTC().Add(-time.Hour))

	ctx := context.Background()
	now := time.Now().UTC()
	// A card the colleague filed into the agent's column. Backdated so the
	// column's own UpdatedAt is NOT what saves it — the child is.
	if err := h.elements.Insert(ctx, &domain.Element{
		ID: mpParent[:20] + "f107", Type: domain.TypeCard,
		Location:  domain.Location{ParentID: col, Section: domain.SectionCanvas},
		Content:   domain.Content{"textPreview": "an hour of somebody's work"},
		CreatedBy: "owner", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed child card: %v", err)
	}

	if _, err := h.svc.Revert(ctx, h.editor, run.ID); err != nil {
		t.Fatalf("revert: %v", err)
	}
	el, err := h.elements.Get(ctx, col)
	if err != nil {
		t.Fatalf("read column: %v", err)
	}
	if el.IsDeleted() {
		t.Fatal("undo trashed a container holding work the run did not put there")
	}
}

// ---------------------------------------------------------------------------
// MP17 — the abandoned proposal held the board for everyone
// ---------------------------------------------------------------------------

// PROPOSED is non-terminal, so it holds the board's only run slot, and Discard
// was tenant-gated: Sara previewed a plan, closed her laptop, and Omar could not
// use the assistant on his own board until after lunch.
func TestProposalLock_TheOwnerCanReclaimAnAbandonedPlan(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("done"), scFinish("reviewed"))
	h.seedShared(t, nil)
	ctx := context.Background()

	run, err := h.svc.Create(ctx, h.editor, CreateRequest{BoardID: mpParent, Intent: "reorganise everything"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, h.editor, run.ID, StateProposed)

	out, err := h.svc.Discard(ctx, h.owner, run.ID, nil)
	if err != nil {
		t.Fatalf("the owner could not clear an abandoned plan on their own board: %v", err)
	}
	// The reclaimer cancels; they never see what they cancelled.
	if out.Task.Intent != "" || out.Plan != nil {
		t.Fatal("reclaiming a plan showed the reclaimer its contents")
	}
	// The slot is free.
	if _, err := h.svc.Create(ctx, h.owner, CreateRequest{BoardID: mpParent, Intent: "now let me work"}); err != nil {
		t.Fatalf("the board is still locked after a reclaim: %v", err)
	}
	if len(h.notes.forUser("editor")) == 0 {
		t.Fatal("the plan's author was never told it had been cleared")
	}
}

// A shared board's plan expires in a meeting's length, not a lunch's, because
// the cost of an unanswered plan there is borne by people who never saw it.
func TestProposalLock_TheWindowIsShorterOnASharedBoard(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("done"), scFinish("reviewed"))
	h.seedShared(t, nil)

	run, err := h.svc.Create(context.Background(), h.owner, CreateRequest{BoardID: mpParent, Intent: "organise"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	proposed := h.awaitState(t, h.owner, run.ID, StateProposed)
	if proposed.ProposalExpiresAt == nil {
		t.Fatal("no deadline at all")
	}
	life := proposed.ProposalExpiresAt.Sub(proposed.UpdatedAt)
	if life > sharedProposalLifetime+time.Minute {
		t.Fatalf("a shared board's plan holds the assistant for %s", life)
	}
}

// ---------------------------------------------------------------------------
// MP7 — memory follows the board, confidentiality follows the person
// ---------------------------------------------------------------------------

// The PREVIOUS RUN block was EMPTY the moment the previous run was somebody
// else's, so the duplicate-structure failure it exists to prevent was still
// live — and MORE likely in a team, because two people organising the same
// board an hour apart is the ordinary case.
func TestMemory_ACollaboratorsRunIsRememberedWithoutItsWords(t *testing.T) {
	const secret = "cut the DoP from episode three before the producer sees the budget"
	h := newMPHarness(t)
	h.seedShared(t, nil)
	prior := h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f008", time.Now().UTC().Add(-time.Hour))
	prior.Task.Intent = secret
	prior.Plan.Summary = "reorganised into 5 stages, left Editing and Sound empty"
	if err := h.runs.Update(context.Background(), prior, prior.Rev); err != nil {
		t.Fatalf("update prior run: %v", err)
	}

	mine := &Run{
		ID: "mineRun", Tenant: "owner", BoardOwnerSub: "owner",
		Task: TaskSpec{Intent: "finish it", Owner: "owner", RootBoardID: mpParent},
	}
	scope := &BoardScope{}
	h.svc.attachHistory(context.Background(), scope, mine)

	if len(scope.History) == 0 {
		t.Fatal("a colleague's run on this board is invisible to the next run — the amnesia this item is about")
	}
	found := false
	for _, entry := range scope.History {
		if strings.Contains(entry.Intent, secret) {
			t.Fatalf("board-scoped memory leaked a collaborator's request text: %q", entry.Intent)
		}
		if strings.Contains(entry.Summary, "5 stages") {
			found = true
		}
	}
	if !found {
		t.Fatal("the outcome and summary — the safe half, and the only useful half — did not travel")
	}
}

// ---------------------------------------------------------------------------
// MP13 — the runner's private labels stamped on other people's elements
// ---------------------------------------------------------------------------

// Labels are private by construction, and the comment/label service documents
// the hazard in as many words. B's run tagged A's cards with B's vocabulary, so
// A saw labelIds on their own elements resolving to nothing in their label list.
func TestLabels_ANonOwnersVocabularyIsNotOfferedOnSomebodyElsesBoard(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	ctx := context.Background()
	if err := h.labels.Insert(ctx, &domain.Label{
		ID: "lbl00000000000000000001", OwnerID: "editor", Name: "personal-followup", Color: "#f00",
	}); err != nil {
		t.Fatalf("seed label: %v", err)
	}

	board, err := h.elements.Get(ctx, mpParent)
	if err != nil {
		t.Fatalf("read board: %v", err)
	}

	foreign := &BoardScope{Board: board}
	h.svc.attachLabels(ctx, foreign, "editor")
	if len(foreign.Labels) != 0 {
		t.Fatalf("an editor's private vocabulary was offered for stamping onto the owner's board: %v", foreign.Labels)
	}

	// The owner's own board still works, or this is a capability regression
	// rather than a privacy fix.
	if err := h.labels.Insert(ctx, &domain.Label{
		ID: "lbl00000000000000000002", OwnerID: "owner", Name: "blocked", Color: "#00f",
	}); err != nil {
		t.Fatalf("seed owner label: %v", err)
	}
	own := &BoardScope{Board: board}
	h.svc.attachLabels(ctx, own, "owner")
	if len(own.Labels) != 1 {
		t.Fatalf("the board owner lost their own vocabulary: %v", own.Labels)
	}
}

// ---------------------------------------------------------------------------
// PL9 / PL6 — the account-wide read path, and a cap that can bind
// ---------------------------------------------------------------------------

// ListAgentRuns refused without a boardId and the store had no tenant-wide
// list, so "what has the assistant done for me lately" and "what am I spending"
// had no signature to be asked through.
func TestAccountRuns_AnsweredWithoutABoardID(t *testing.T) {
	h := newMPHarness(t)
	h.seedShared(t, nil)
	h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f009", time.Now().UTC().Add(-time.Hour))
	ctx := context.Background()

	runs, err := h.svc.AccountRuns(ctx, h.editor, RunFilter{}, 25)
	if err != nil {
		t.Fatalf("account runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("the account-wide query returned %d runs", len(runs))
	}
	roll, err := h.svc.AccountUsage(ctx, h.editor, RunFilter{})
	if err != nil {
		t.Fatalf("account usage: %v", err)
	}
	if roll.Runs != 1 || roll.ByState[StateCompleted] != 1 {
		t.Fatalf("the usage rollup did not group by state: %+v", roll)
	}
	// And it is the CALLER's account, never anybody else's.
	other, err := h.svc.AccountRuns(ctx, h.owner, RunFilter{}, 25)
	if err != nil {
		t.Fatalf("account runs for the owner: %v", err)
	}
	if len(other) != 0 {
		t.Fatal("the account-wide query returned another person's runs")
	}
}

// An unpriced model reports $0 forever, so every configured cap evaluates to
// "not reached" no matter what is spent — while the boot log calls the
// configuration fine. A cap that cannot bind is worse than no cap.
func TestBudget_ACapOnAnUnpricedModelRefusesToStart(t *testing.T) {
	h := newMPHarness(t, scFinish("done"))
	h.seedShared(t, nil)
	h.svc.dailyCapUSD = 5

	_, err := h.svc.Create(context.Background(), h.owner, CreateRequest{BoardID: mpParent, Intent: "organise"})
	if err == nil {
		t.Fatal("a spend ceiling was configured on a model with no price and the run started anyway")
	}
	if !strings.Contains(err.Error(), "no price") {
		t.Fatalf("the refusal does not name the problem: %v", err)
	}
}

// The board's own ceiling binds independently of the deployment's, and the
// refusal names the number rather than saying "budget reached".
func TestBudget_ABoardCeilingBindsAndNamesItself(t *testing.T) {
	h := newMPHarness(t, scFinish("done"))
	h.seedShared(t, &domain.AgentPolicy{DailyCapUSD: 1})
	cognition.RegisterPrice(h.svc.provider.Model(), cognition.Price{InputPer1M: 1, OutputPer1M: 1})
	ctx := context.Background()

	spent := h.seedForeignRun(t, "editor", "owner", mpParent[:20]+"f010", time.Now().UTC())
	spent.Usage.CostUSD = 2
	if err := h.runs.Update(ctx, spent, spent.Rev); err != nil {
		t.Fatalf("update spend: %v", err)
	}

	_, err := h.svc.Create(ctx, h.editor, CreateRequest{BoardID: mpParent, Intent: "organise"})
	if err == nil {
		t.Fatal("a board whose daily assistant budget is spent admitted another run")
	}
	if !strings.Contains(err.Error(), "board's daily assistant budget") {
		t.Fatalf("the refusal does not name the board's ceiling: %v", err)
	}
}

// ---------------------------------------------------------------------------
// PL16 — provenance spanning HTTP → run → transaction
// ---------------------------------------------------------------------------

// echo has minted a request id since the first commit and thrown it away one
// middleware later, so "why did my board change last Tuesday" meant joining
// three stores by hand. Every write records its source.
func TestProvenance_TheRequestIdReachesTheTransaction(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("done"), scFinish("reviewed"))
	h.seedShared(t, nil)
	ctx := context.Background()

	caller := &domain.Principal{Sub: "owner", Name: "Omar", RequestID: "req-abc-123", Source: domain.SourceAPI}
	run, err := h.svc.Create(ctx, caller, CreateRequest{BoardID: mpParent, Intent: "organise"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if run.RequestID != "req-abc-123" || run.Source != domain.SourceAPI {
		t.Fatalf("the run lost its provenance: %q / %q", run.RequestID, run.Source)
	}
	h.awaitState(t, caller, run.ID, StateProposed)

	applied, err := h.svc.Apply(ctx, caller, run.ID, nil, nil)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if len(applied.TransactionIDs) == 0 {
		t.Fatal("apply recorded no transaction")
	}
	txn, err := h.txns.Get(ctx, applied.TransactionIDs[0])
	if err != nil {
		t.Fatalf("read transaction: %v", err)
	}
	if txn.Source != domain.SourceAPI {
		t.Fatalf("the transaction cannot say where it came from: %q", txn.Source)
	}
	if txn.RequestID == "" {
		t.Fatal("the transaction carries no request id, so provenance is still a three-store join")
	}
}

// ---------------------------------------------------------------------------
// MP16 — the agent had no presence
// ---------------------------------------------------------------------------

// mpPresence is a bus that records the synthetic participants a run publishes.
type mpPresence struct {
	scBus
	mu   sync.Mutex
	live map[string]realtime.PresenceUser
	seen []realtime.PresenceUser
}

func newMPPresence() *mpPresence {
	return &mpPresence{live: map[string]realtime.PresenceUser{}}
}

func (b *mpPresence) RegisterVirtual(boardID string, p realtime.PresenceUser) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.live[boardID+"|"+p.Sub] = p
	b.seen = append(b.seen, p)
}

func (b *mpPresence) UnregisterVirtual(boardID, sub string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.live, boardID+"|"+sub)
}

func (b *mpPresence) liveCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.live)
}

func (b *mpPresence) claims() []realtime.PresenceUser {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]realtime.PresenceUser(nil), b.seen...)
}

// Every human editor in this product claims what it touches; the agent was the
// only writer that did not. And a stuck synthetic participant is worse than
// none — it claims a card nobody is working on and cannot be closed by
// reloading — so the teardown is the half that has to be tested.
func TestPresence_TheRunClaimsTheCanvasAndLetsGoOfIt(t *testing.T) {
	h := newMPHarness(t, mpColumn(mpParent, "Editing"), scFinish("done"), scFinish("reviewed"))
	presence := newMPPresence()
	h.svc.bus = presence
	h.seedShared(t, nil)

	run, err := h.svc.Create(context.Background(), h.owner, CreateRequest{BoardID: mpParent, Intent: "organise"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	h.awaitState(t, h.owner, run.ID, StateProposed)

	claims := presence.claims()
	if len(claims) == 0 {
		t.Fatal("the run never appeared on the canvas: a colleague typing in the note it rewrites gets no badge at all")
	}
	named := false
	for _, c := range claims {
		if !strings.HasPrefix(c.Sub, "agent:") {
			t.Fatalf("the run published a participant that is not marked synthetic: %q", c.Sub)
		}
		if strings.Contains(c.Name, "organise") {
			t.Fatal("the presence entry carries the request text — it must be room-safe by construction")
		}
		if strings.Contains(c.Name, "Omar") {
			named = true
		}
	}
	if !named {
		t.Fatal("the badge does not say whose assistant it is")
	}
	if presence.liveCount() != 0 {
		t.Fatal("the run reached PROPOSED and its participant is still sitting on the board")
	}
}

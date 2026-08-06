package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/agent/cognition"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/realtime"
	"qomranote/backend/internal/service"
)

// The run coordinator: admission, the state machine, and the four decisions a
// human can make about a run (apply, discard, cancel, revert).
//
// Execution model for this milestone: a run executes in an in-process goroutine
// driven by a durable state machine, and a boot reconciler resolves whatever a
// crash left mid-flight. That is crash-recoverable without a queue, and it is
// the honest fit for work measured in seconds (G12). Activity leases and a
// separate worker role become worth their cost when a workload's runs start
// exceeding a couple of minutes; Organize does not.

// IDGenerator mints 24-hex element/run ids.
type IDGenerator func() string

// Service is the agent harness.
type Service struct {
	elements    domain.ElementRepository
	users       domain.UserRepository
	txns        *service.TransactionService
	txnRepo     domain.TransactionRepository
	access      *service.AccessResolver
	labels      domain.LabelRepository
	comments    domain.CommentRepository
	images      ImageFetcher
	attachments domain.AttachmentRepository
	links       LinkResolver
	runs        RunStore
	events      EventStore
	provider    cognition.Provider
	health      *cognition.Health
	bus         domain.EventBroadcaster
	newID       IDGenerator
	log         *zap.Logger

	// dailyCapUSD bounds one tenant's spend per UTC day. Denial-of-wallet is a
	// named threat, not an afterthought.
	dailyCapUSD float64

	// cancels holds the abort handle of every run executing in THIS process.
	//
	// Stop used to be a placebo. Cancel wrote CANCELLED and returned; the run
	// goroutine's context came from context.Background() and nothing in the loop
	// ever read run state, so the model kept working for up to eight more
	// minutes on the person's money — and because Cancel freed the board slot,
	// they could start a second run writing the same board alongside the first.
	cancelMu sync.Mutex
	cancels  map[string]context.CancelFunc

	// notifier is the one gate every in-app notification passes through.
	//
	// The agent had a bus and no notifier, so a finished run reached only the
	// sockets that happened to be open on that board — and NotifyBoardChange
	// was a stored preference with zero producers anywhere in the backend.
	notifier *service.Notifier

	// commentAnnouncer publishes the comments a run writes. Optional; nil means
	// the deployment keeps the silent behaviour rather than failing to start.
	commentAnnouncer CommentAnnouncer

	// audit records the two moments a run changes the board irreversibly enough
	// to be somebody else's problem: the commit and the undo. The run journal
	// already holds far more detail, but it is per-run and expires with the run —
	// the audit log is the tenant-wide, append-only one, and "what did the
	// assistant change here, and who let it" is asked from that side.
	//
	// Optional, like the other two above: nil emits nothing.
	audit *service.AuditService

	// lastNotified coalesces same-board run outcomes per recipient.
	//
	// In-process and per-replica by design: the failure it prevents is a burst
	// of runs on one board ringing five bells in ten minutes, and that burst is
	// one person on one connection, so a shared store would buy correctness
	// nobody can perceive at the cost of a round trip per terminal state.
	notifyMu     sync.Mutex
	lastNotified map[string]time.Time
	// presenceNames is the display name each live run appears under, so a
	// mid-run badge move does not have to re-resolve it (a user read per journal
	// event) or silently downgrade "Sara's assistant" to "the assistant".
	presenceNames map[string]string
}

// runNotifyWindow coalesces repeated outcomes on the same board for the same
// person. The difference between a feature and an unsubscribe is entirely in
// these suppression rules.
const runNotifyWindow = 30 * time.Minute

// RoomPresence publishes a participant that has no socket.
//
// Every human editor in this product claims what it touches, and the agent was
// the only writer that did not: a colleague could be typing in the very note a
// run was rewriting, with no badge and no warning, and the run's transaction
// landed as a merge-patch over their in-flight edit.
//
// Optional and asserted on the bus for the same reason BoardPresence is: a
// deployment without it loses the badge, not the run.
type RoomPresence interface {
	RegisterVirtual(boardID string, p realtime.PresenceUser)
	UnregisterVirtual(boardID, sub string)
}

// claimPresence puts the run on the canvas, optionally sitting on the element
// it is working on right now. Idempotent, so it is safe to call per step.
func (s *Service) claimPresence(run *Run, actorName, editing string) {
	rp, ok := s.bus.(RoomPresence)
	if !ok || run == nil {
		return
	}
	s.notifyMu.Lock()
	name := s.presenceNames[run.ID]
	if actorName != "" {
		name = sanitizeName(actorName) + "'s assistant"
		s.presenceNames[run.ID] = name
	}
	s.notifyMu.Unlock()
	if name == "" {
		name = "the assistant"
	}
	rp.RegisterVirtual(run.Task.RootBoardID, realtime.PresenceUser{
		ClientID: agentPresenceSub(run.ID),
		Sub:      agentPresenceSub(run.ID),
		Name:     name,
		Editing:  editing,
	})
}

// releasePresence tears the participant down. Called from every exit path
// including the crash reconciler: a stuck synthetic member claims a card nobody
// is working on and cannot be closed by reloading, which is worse than none.
func (s *Service) releasePresence(run *Run) {
	if run == nil {
		return
	}
	s.notifyMu.Lock()
	delete(s.presenceNames, run.ID)
	s.notifyMu.Unlock()
	if rp, ok := s.bus.(RoomPresence); ok {
		rp.UnregisterVirtual(run.Task.RootBoardID, agentPresenceSub(run.ID))
	}
}

// agentPresenceSub is the synthetic identity a run appears under. Prefixed so
// no code path can mistake it for a real subject id.
func agentPresenceSub(runID string) string { return "agent:" + runID }

// The production bus must satisfy both optional halves, or these capabilities
// ship and do nothing: the assertion is discovered at runtime, so nothing else
// would fail if the hub stopped implementing them. Checked by the compiler here
// instead.
var (
	_ RoomPresence  = (*realtime.Hub)(nil)
	_ BoardPresence = (*realtime.Hub)(nil)
)

// BoardPresence reports who is currently watching a board.
//
// Optional and asserted on the bus, like the other capabilities the realtime
// layer grew after its port was written. A deployment without it simply sends
// the notification — which is the old behaviour of every other producer, and
// strictly better than the silence this replaces.
type BoardPresence interface {
	// Watching reports whether this person has a live connection to the board.
	Watching(boardID, sub string) bool
}

// Config carries the wiring for NewService.
type Config struct {
	Elements    domain.ElementRepository
	Users       domain.UserRepository
	Txns        *service.TransactionService
	TxnRepo     domain.TransactionRepository
	Access      *service.AccessResolver
	Labels      domain.LabelRepository
	Comments    domain.CommentRepository
	Images      ImageFetcher
	Attachments domain.AttachmentRepository
	Links       LinkResolver
	Runs        RunStore
	Events      EventStore
	Provider    cognition.Provider
	Bus         domain.EventBroadcaster
	NewID       IDGenerator
	Log         *zap.Logger
	DailyCapUSD float64
	// Notifier is how a run outcome reaches somebody who is not looking at the
	// board. Optional: a deployment without it keeps the realtime-only
	// behaviour rather than failing to start.
	Notifier *service.Notifier
	// Comments announces an agent-written comment the way a human's is
	// announced: broadcast, board-owner bell, mentions.
	CommentAnnouncer CommentAnnouncer
	// Audit receives agent.run_applied and agent.run_reverted. Optional: a
	// deployment or a test without it keeps the previous behaviour.
	Audit *service.AuditService
}

// NewService constructs the harness. A nil Provider yields a service that
// admits nothing — the deployment simply has no agent, which the API reports as
// unavailable rather than failing mysteriously at the first request.
func NewService(cfg Config) *Service {
	// SEC1 — the write path's label guard can only refuse a label it can look up,
	// and whether it could was a line in whichever constructor happened to
	// remember it. serve.go called AttachLabels; the eval service and every test
	// harness did not, though they hand the same repository to this Config one
	// field away. So the ownership check on `labelIds` was live in production and
	// absent everywhere the agent's own label tools are actually exercised —
	// meaning the tests that prove the guard works would have been the ones
	// running without it.
	//
	// Wired here because this is the one place that already holds both halves. A
	// security check whose enforcement depends on a caller remembering to enable
	// it is not a check.
	if cfg.Txns != nil && cfg.Labels != nil {
		cfg.Txns.AttachLabels(cfg.Labels)
	}
	return &Service{
		elements:         cfg.Elements,
		labels:           cfg.Labels,
		comments:         cfg.Comments,
		images:           cfg.Images,
		attachments:      cfg.Attachments,
		links:            cfg.Links,
		users:            cfg.Users,
		txns:             cfg.Txns,
		txnRepo:          cfg.TxnRepo,
		access:           cfg.Access,
		runs:             cfg.Runs,
		events:           cfg.Events,
		provider:         cfg.Provider,
		health:           cognition.NewHealth(cfg.Provider),
		bus:              cfg.Bus,
		newID:            cfg.NewID,
		log:              cfg.Log.Named("agent"),
		dailyCapUSD:      cfg.DailyCapUSD,
		notifier:         cfg.Notifier,
		commentAnnouncer: cfg.CommentAnnouncer,
		audit:            cfg.Audit,
		cancels:          map[string]context.CancelFunc{},
		lastNotified:     map[string]time.Time{},
		presenceNames:    map[string]string{},
	}
}

// holdCancel publishes a run's abort handle for the lifetime of its goroutine.
func (s *Service) holdCancel(runID string, cancel context.CancelFunc) func() {
	s.cancelMu.Lock()
	s.cancels[runID] = cancel
	s.cancelMu.Unlock()
	return func() {
		s.cancelMu.Lock()
		delete(s.cancels, runID)
		s.cancelMu.Unlock()
	}
}

// abort stops a run executing in this process. Reports whether a handle existed
// — a run being worked on by another replica has none here, which is what the
// loop's own terminal-state check between turns is for.
func (s *Service) abort(runID string) bool {
	s.cancelMu.Lock()
	cancel, ok := s.cancels[runID]
	s.cancelMu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

// persist applies one field-level change to the AUTHORITATIVE run and writes it,
// refetching once on a rev conflict.
//
// Durability of a fact must not depend on winning a rev race. Two facts were
// being persisted as side effects of a state transition — the committed
// transaction id and the accumulated spend — and transition writes with
// optimistic concurrency and rolls back on conflict. Any concurrent write to the
// run row (a steer landing, a refine, a cancel, a reconcile sweep) therefore
// orphaned the transaction from the run forever, or lost the money the run had
// already spent. Neither is a state transition; neither should have been written
// like one.
func (s *Service) persist(ctx context.Context, run *Run, what string, mutate func(*Run)) error {
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		var fresh *Run
		fresh, err = s.runs.Get(ctx, run.ID)
		if err != nil {
			break
		}
		mutate(fresh)
		if err = s.runs.Update(ctx, fresh, fresh.Rev); err == nil {
			run.Rev = fresh.Rev
			return nil
		}
		if !errors.Is(err, domain.ErrConflict) {
			break
		}
	}
	s.log.Error("could not persist run fact", zap.String("run", run.ID),
		zap.String("fact", what), zap.Error(err))
	return err
}

// Enabled reports whether a model provider is configured.
func (s *Service) Enabled() bool { return s != nil && s.provider != nil }

// ProviderName and ModelName are who actually processes a board's contents.
//
// The composer promises to say where the text goes, and a client constant
// cannot keep that promise: this is configurable per deployment, so a hard-coded
// vendor name becomes a false statement about somebody's data the first time an
// operator switches keys. Empty when the agent is off, which the client renders
// as "a third-party model provider" rather than inventing one.
func (s *Service) ProviderName() string {
	if !s.Enabled() {
		return ""
	}
	return s.provider.Name()
}

// ModelName is the specific model behind ProviderName.
func (s *Service) ModelName() string {
	if !s.Enabled() {
		return ""
	}
	return s.provider.Model()
}

// ProviderHealthy reports whether the model plane can currently answer.
//
// Enabled() only ever meant "a key was present at boot", so a rotated or
// rate-limited key left the composer cheerfully accepting intents it could not
// serve. The probe behind this is cached, so it is safe to call from a handler
// that runs on every page load.
func (s *Service) ProviderHealthy(ctx context.Context) bool {
	if !s.Enabled() {
		return false
	}
	return s.health.Err(ctx) == nil
}

// ---------------------------------------------------------------------------
// Admission
// ---------------------------------------------------------------------------

// CreateRequest is the client's ask, before normalization.
type CreateRequest struct {
	BoardID string `json:"boardId"`
	// Intent is what the person actually wants, in their own words. There is
	// no fixed workload: the same harness organizes, drafts, or declines,
	// depending on this one field.
	Intent      string   `json:"intent"`
	Scope       Scope    `json:"scope"`
	SelectionID []string `json:"selectionIds,omitempty"`
	Autonomy    Autonomy `json:"autonomy"`
	// AttachmentIDs are files attached to this request. Uploaded through the
	// ordinary presign flow first, so by the time they arrive here they are
	// already the user's own, already stored, and already typed.
	AttachmentIDs []string `json:"attachmentIds,omitempty"`
	// Viewport is where the person is looking, so new content lands there
	// rather than wherever the packer would otherwise put it.
	Viewport *Viewport `json:"viewport,omitempty"`
	// ActiveLabelIDs is the filter the person has on screen right now. It travels
	// for the same reason the viewport does: "tidy this up" means the twelve
	// starred items they are looking at, not the two hundred behind the filter,
	// and a run that cannot see the filter answers a different question.
	ActiveLabelIDs []string `json:"activeLabelIds,omitempty"`
	// ContinuesRunID links this run to the one it picks up from. Not bindable
	// from JSON: a continuation's intent is composed on the server from the
	// prior run's own unmet list, and a client that could set the link without
	// going through Continue could claim a lineage the intent does not have.
	ContinuesRunID string `json:"-"`
	// Refinements are the corrections a PRIOR run was already given, carried
	// forward by Retry. Server-composed for the same reason as the link above:
	// they are a record of a conversation that happened, not a field a client
	// gets to assert.
	Refinements []string `json:"-"`
	// RetryOfRunID links this run to the one it was asked to do over. Composed
	// on the server for the same reason the two links above are: a lineage a
	// client could assert is a lineage that means nothing.
	RetryOfRunID string `json:"-"`
	// Source is who composed the intent. Bindable from JSON only because the
	// client is the one place that knows the difference between somebody typing
	// and somebody clicking the ambient pill; Continue and Retry set it on the
	// server, where they overwrite whatever arrived.
	Source TaskSource `json:"source,omitempty"`
}

// sourceOr defaults an unstated authorship rather than storing an empty one. A
// blank Source would sort into whichever bucket a dashboard listed first and
// bias exactly the comparison the field exists to make.
func sourceOr(got, fallback TaskSource) TaskSource {
	switch got {
	case SourceTyped, SourceHint, SourceContinue, SourceRetry:
		return got
	}
	return fallback
}

// ErrLinkCannotDelegate refuses a run requested on the strength of a share
// link. It names the fix — ask the owner to add you — because "forbidden" on a
// board you can visibly edit reads as a bug rather than a boundary.
var ErrLinkCannotDelegate = wrap(domain.ErrForbidden,
	"the assistant runs for people the board names directly — ask the owner to add you as an editor")

// Create admits a task and starts the run. It returns as soon as the run is
// durable, and the work proceeds in the background — the client watches the
// journal rather than holding an HTTP request open for the model call.
func (s *Service) Create(ctx context.Context, p *domain.Principal, req CreateRequest) (*Run, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	// The human's own permission is checked first and independently. A
	// delegation attenuates; it never grants.
	role, _, prov, err := s.access.ResolveDetailed(ctx, req.BoardID, p)
	if err != nil {
		return nil, err
	}
	if !role.CanEdit() {
		return nil, domain.ErrForbidden
	}
	// A capability that mints capabilities is never granted by a bearer token.
	//
	// The edit link was designed for "let a contractor drag a card". Admission
	// asked only for RoleEdit, so anyone who forwarded that URL handed a stranger
	// a delegation-minting capability on the owner's board plus a live model
	// budget — and every downstream disclosure in this package (ancestry, the
	// people list, the run journal) was reachable from a pasted link.
	if prov.FromLink() {
		return nil, ErrLinkCannotDelegate
	}

	// The board has more than one stakeholder, and delegation was aware only of
	// the caller. One walk answers three questions the old admission could not
	// ask: which boards contain this one (the overlap guard), who owns it (the
	// erasure key and the audit key), and what the owner permits here.
	ancestors, governing, err := s.access.BoardChain(ctx, req.BoardID)
	if err != nil {
		return nil, err
	}
	boardOwner := ""
	var policy *domain.AgentPolicy
	if governing != nil {
		boardOwner = governing.OwnerID
		policy = governing.AgentPolicy
	}
	isOwner := boardOwner != "" && boardOwner == p.Sub
	if !policy.MayRun(isOwner) {
		return nil, ErrAgentClosedHere
	}
	// Silent downgrade, never a bare refusal. Autonomy is a property of the
	// (board, principal) pair, and the person who pressed the button is not
	// necessarily the person with the most at stake.
	autonomyNote := ""
	requiresApproval := !policy.MayAutoApply(isOwner)
	if req.Autonomy == AutonomyAuto && requiresApproval {
		req.Autonomy = AutonomyPreview
		autonomyNote = "this board's owner keeps unattended runs for themselves, " +
			"so this one will show you a plan first"
	}

	intent := truncate(sanitizeBody(req.Intent), 1000)
	if intent == "" {
		return nil, ErrNoIntent
	}
	if !req.Scope.Valid() {
		req.Scope = ScopeBoard
	}
	if !req.Autonomy.Valid() {
		req.Autonomy = AutonomyPreview
	}

	// One run per board (G8). Checked here for a clear error; also enforced by
	// a unique index so a concurrent create cannot slip between check and write.
	// The guard is correct — two agents writing one board is a real hazard —
	// but a bare rejection makes the feature look broken when a colleague holds
	// the slot. Same invariant, legible outcome.
	if active, err := s.activeOverlapping(ctx, req.BoardID, ancestors); err == nil && active != nil {
		who := "someone else"
		if active.Tenant == p.Sub {
			who = "you"
		}
		where := ""
		if active.Task.RootBoardID != req.BoardID {
			// Name the board, because "someone else started one" on a board the
			// person is not looking at is indistinguishable from a bug.
			where = " on " + s.boardTitle(ctx, active.Task.RootBoardID) + ", which overlaps this board,"
		}
		return nil, fmt.Errorf("%w: %s started one%s %s — it will finish shortly",
			ErrRunActive, who, where, humanAge(active.CreatedAt))
	} else if err != nil && !errors.Is(err, domain.ErrNotFound) {
		return nil, err
	}

	// Spend is bounded on both axes the board can express: the runner's own
	// day, and the ceiling the owner set for this board.
	chargeTo := p.Sub
	if policy.ChargedToOwner() && boardOwner != "" {
		chargeTo = boardOwner
	}
	if err := s.checkBudget(ctx, chargeTo, req.BoardID, policy); err != nil {
		return nil, err
	}

	budget := DefaultBudget()
	budget.Normalize()
	// A cap checked only at admission is not a bound: one expensive run crossed
	// it and kept going to the end of its own budget, so the ceiling described
	// what had been spent rather than limiting what would be. Clamping the RUN's
	// own cost ceiling to whatever the day and the board have left makes the
	// loop's existing per-turn check the mid-run check — the run stops on
	// stopCost with the honest exhaustion message and a Continue button, instead
	// of overshooting and then being refused next time.
	if head := s.remainingBudget(ctx, chargeTo, req.BoardID, policy); head >= 0 &&
		(budget.MaxCostUSD <= 0 || head < budget.MaxCostUSD) {
		budget.MaxCostUSD = head
	}

	runID := s.newID()
	now := time.Now().UTC()
	run := &Run{
		ID:     runID,
		Tenant: p.Sub,
		// Whose content this run read, as distinct from who started it. Without
		// it a purge keyed on the runner leaves a verbatim partial copy of a
		// deleted account's board inside a collaborator's history forever.
		BoardOwnerSub: boardOwner,
		RequestID:     p.RequestID,
		Source:        transportOf(p),
		ChargedTo:     chargeTo,
		BoardCapUSD:   boardCapOf(policy),
		Task: TaskSpec{
			Intent:      intent,
			Owner:       p.Sub,
			RootBoardID: req.BoardID,
			AncestorIDs: ancestors,
			Scope:       req.Scope,
			SelectionID: req.SelectionID,
			// Filtered to what this person actually owns and finished
			// uploading: an attachment id is a client-supplied string, and the
			// agent must never reach anything its principal could not.
			AttachmentIDs:  s.ownedAttachments(ctx, p, req.AttachmentIDs),
			Autonomy:       req.Autonomy,
			AutonomyNote:   autonomyNote,
			Budget:         budget,
			Viewport:       req.Viewport,
			ActiveLabelIDs: req.ActiveLabelIDs,
			Refinements:    req.Refinements,
			// Who composed this request. Four producers converge here
			// indistinguishably, and a hint accepted with one click has nothing
			// in common with a paragraph somebody wrote at 2 a.m. — so every
			// rate in every dashboard was computed over a mixed population.
			// Defaulted rather than trusted blank: an unlabelled run would land
			// in whichever bucket sorted first and quietly bias it.
			Source: sourceOr(req.Source, SourceTyped),
			// A content-free fingerprint, so "they asked the same thing three
			// times" is a $group rather than a scan over intent text nobody is
			// allowed to retain. It is also the missing trigger for "offer a
			// rule after the third identical run", which shipped specified with
			// no way to detect the third.
			IntentKey: IntentKey(intent),
			// The outline pre-phase is DARK until its checklist ships.
			//
			// It is built, gated and correct, but the half that makes it worth
			// anything is on the client: the person edits the sketch and posts
			// back a mask, and the run resumes with task.Outline set. Until then
			// outlinePhase returns an empty steer on every first run — so it
			// changes nothing about the plan and spends one fast-tier call per
			// authoring run emitting an event no surface renders.
			//
			// A capability that ships doing nothing is bad; one that also bills
			// for it every run is worse. Delete this line the day AgentDecide
			// renders EvOutlineReady as an editable checklist and Apply posts the
			// mask back — nothing else has to change, which is why it is one
			// field and not a removal.
			SkipOutline: true,
		},
		State:  StateCreated,
		Active: true,
		// The grant is minted server-side and is strictly weaker than the
		// human's own permissions: no delete, no ACL, no reach beyond this
		// board's subtree, and an expiry.
		Delegation: Delegation{
			RunID:       runID,
			OnBehalfOf:  p.Sub,
			RootBoardID: req.BoardID,
			Capabilities: []domain.Capability{
				domain.CapElementCreate,
				domain.CapElementUpdate,
				domain.CapElementMove,
				// Deletes are granted here, but a plan can only contain one
				// after a human has seen it: the tool is withheld from
				// unattended runs, and Preconditions rejects them there.
				domain.CapElementDelete,
			},
			Consequence: domain.ConsequenceDestructive,
			// One transaction may carry a to-do list and all of its tasks, so
			// the op ceiling sits above the action ceiling.
			MaxOps: budget.MaxActions * 4,
			// The compiler's own answer to "what does each action kind write",
			// carried on the grant so the write path can enforce an allowlist
			// without importing the planner that derives it. Privileged keys —
			// isHome, locked, cloneSourceId, agentInstructions — need no
			// enumeration once this is in force: they are excluded by not being
			// produced by any kind.
			ContentKeys: ContentKeyAllowance(),
			// The write path's own copy of the board's consent decision, so a
			// path that reconstructs a run cannot quietly restore auto-apply.
			RequiresApproval: requiresApproval,
			ExpiresAt:        now.Add(30 * time.Minute),
		},
		ContinuesRunID: req.ContinuesRunID,
		RetryOfRunID:   req.RetryOfRunID,
		// Which code produced this run. Stamped at admission rather than at
		// completion so a run that crashes is still attributable to the build
		// that crashed it.
		Build:     CurrentBuild(),
		CreatedAt: now,
		UpdatedAt: now,
		StateAt:   map[RunState]time.Time{StateCreated: now},
	}
	if err := s.runs.Insert(ctx, run); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			return nil, ErrRunActive
		}
		return nil, err
	}
	s.emit(ctx, run, EvRunCreated, "run admitted", map[string]any{
		"intent": intent, "scope": run.Task.Scope, "autonomy": run.Task.Autonomy,
	})

	// The principal is copied rather than captured: the request's context and
	// its cancellation belong to the HTTP round trip, not to the run.
	principal := &domain.Principal{
		Sub: p.Sub, Email: p.Email, Name: p.Name,
		// Provenance rides with the copy. Dropping it here is how a run's own
		// writes ended up unattributable to the request that asked for them.
		RequestID: p.RequestID, Source: transportOf(p),
	}
	go s.execute(context.Background(), principal, run.ID)
	return run, nil
}

// maxPromptAttachments bounds what one request can carry. A PDF costs roughly
// 258 tokens a page, so this is a spend limit as much as a usability one.
const maxPromptAttachments = 4

// ownedAttachments keeps only files this person actually owns and has finished
// uploading. Checked here rather than trusted from the request: an attachment id
// is a client-supplied string, and the whole point of the delegation model is
// that the agent never reaches anything its principal could not.
func (s *Service) ownedAttachments(ctx context.Context, p *domain.Principal, ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	// LOUD, because silence is what hid this. The repository was never wired in
	// production, so this returned nil on its first line and every file a person
	// attached was discarded without a word — and the agent then said, honestly
	// from its own point of view, that no attachment id had been provided. A
	// deployment that cannot resolve attachments is a deployment where the
	// person is being ignored, and that belongs in the log rather than in a
	// confusing answer.
	if s.attachments == nil {
		s.log.Error("attachments were sent with a request but no attachment repository is wired; "+
			"every attached file will be dropped",
			zap.Int("dropped", len(ids)))
		return nil
	}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if len(out) >= maxPromptAttachments {
			break
		}
		att, err := s.attachments.Get(ctx, id)
		if err != nil || att.OwnerID != p.Sub || att.Status != domain.AttachmentUploaded {
			continue
		}
		out = append(out, id)
	}
	return out
}

// checkDailyCap enforces the tenant's spend ceiling before any tokens are spent.
// SpentToday reports the tenant's model spend since midnight UTC, and the cap
// it counts against. A zero cap means uncapped.
//
// The cap was previously discoverable only by hitting it, mid-thought, with a
// message that named a ceiling nothing had ever shown.
func (s *Service) SpentToday(ctx context.Context, tenant string) (spent, cap float64) {
	return s.spentToday(ctx, tenant), s.dailyCapUSD
}

func (s *Service) spentToday(ctx context.Context, tenant string) float64 {
	return s.spentSince(ctx, tenant, "", midnightUTC())
}

func midnightUTC() time.Time { return time.Now().UTC().Truncate(24 * time.Hour) }

// spentSince answers the spend question in the database when the adapter can.
//
// The version this replaces summed `ListByBoard(tenant, "", 500)` in Go: a
// 500-document scan with a silent ceiling, so a day busy enough to exceed it
// under-reported — and the person spending the most was the least likely to be
// stopped. The Go path stays as the fallback for in-memory adapters, with the
// same honest limit it always had.
func (s *Service) spentSince(ctx context.Context, tenant, boardID string, since time.Time) float64 {
	f := RunFilter{Since: since, BoardID: boardID}
	if agg, ok := s.runs.(RunAnalyticsStore); ok {
		roll, err := agg.AggregateUsage(ctx, tenant, f)
		if err == nil {
			return roll.CostUSD
		}
	}
	runs, err := s.runs.ListByBoard(ctx, tenant, boardID, 500)
	if err != nil {
		return 0
	}
	var spent float64
	for _, r := range runs {
		if r.CreatedAt.After(since) {
			spent += r.Usage.CostUSD
		}
	}
	return spent
}

// ErrUnpricedCap refuses to start when a ceiling is configured and the model
// has no price.
//
// An unpriced model reports $0 forever, so every cap in the deployment
// evaluates to "not reached" no matter what is spent, while the boot log calls
// the configuration fine. A cap that cannot bind is worse than no cap: it is a
// control somebody is relying on. Naming the model is the fix instruction.
var ErrUnpricedCap = wrap(domain.ErrUnavailable,
	"a spend limit is configured but this model has no price, so the limit could not be enforced — "+
		"set a price override for it or clear the limit")

// checkBudget enforces every ceiling that binds this run, before a token is
// spent. Called again between model turns, which is what turns a cap into a
// bound rather than a post-hoc observation.
//
// Four things were wrong with the version this replaces and they compound: one
// deployment-wide number with no per-board ceiling; admission-time only, so one
// expensive run overshot arbitrarily; a 500-document Go scan; and no refusal at
// all on an unpriced model, which makes the whole mechanism decorative.
func (s *Service) checkBudget(ctx context.Context, tenant, boardID string, policy *domain.AgentPolicy) error {
	boardCap := 0.0
	if policy != nil {
		boardCap = policy.DailyCapUSD
	}
	if s.dailyCapUSD <= 0 && boardCap <= 0 {
		return nil
	}
	// A cap on an unpriced model is a control that silently does nothing.
	if !s.pricedModel() {
		return ErrUnpricedCap
	}
	since := midnightUTC()
	if s.dailyCapUSD > 0 {
		if spent := s.spentSince(ctx, tenant, "", since); spent >= s.dailyCapUSD {
			return fmt.Errorf("%w: daily AI budget reached (%.2f of %.2f USD today)",
				ErrBudget, spent, s.dailyCapUSD)
		}
	}
	if boardCap > 0 && boardID != "" {
		if spent := s.spentSince(ctx, tenant, boardID, since); spent >= boardCap {
			return fmt.Errorf("%w: this board's daily assistant budget is used up (%.2f of %.2f USD today)",
				ErrBudget, spent, boardCap)
		}
	}
	return nil
}

// remainingBudget is how much of every binding ceiling is left, in USD.
//
// Returns -1 when nothing binds, which the caller reads as "do not clamp" —
// distinct from 0, which is a real exhausted ledger.
func (s *Service) remainingBudget(ctx context.Context, tenant, boardID string, policy *domain.AgentPolicy) float64 {
	boardCap := boardCapOf(policy)
	if s.dailyCapUSD <= 0 && boardCap <= 0 {
		return -1
	}
	since := midnightUTC()
	left := -1.0
	if s.dailyCapUSD > 0 {
		left = s.dailyCapUSD - s.spentSince(ctx, tenant, "", since)
	}
	if boardCap > 0 && boardID != "" {
		byBoard := boardCap - s.spentSince(ctx, tenant, boardID, since)
		if left < 0 || byBoard < left {
			left = byBoard
		}
	}
	if left < 0 {
		return 0
	}
	return left
}

// pricedModel reports whether the configured model has a price the ledger can
// use. Deployments with no provider are treated as priced so a disabled agent
// does not report a budget problem it does not have.
func (s *Service) pricedModel() bool {
	if s.provider == nil {
		return true
	}
	return cognition.Priced(s.provider.Model())
}

// checkBudgetMidRun re-asks the question the run has been spending against.
//
// Admission-time only meant a run that crossed the line kept going to the end
// of its budget, so the ceiling described yesterday's spend rather than
// bounding today's. Reusing the forced-finish path, so crossing it is an honest
// exhaustion the person is told about rather than a kill.
func (s *Service) checkBudgetMidRun(ctx context.Context, run *Run) error {
	if s.dailyCapUSD <= 0 && run.BoardCapUSD <= 0 {
		return nil
	}
	tenant := run.Tenant
	if run.ChargedTo != "" {
		tenant = run.ChargedTo
	}
	policy := &domain.AgentPolicy{DailyCapUSD: run.BoardCapUSD}
	return s.checkBudget(ctx, tenant, run.Task.RootBoardID, policy)
}

// activeOverlapping asks whether any live run's subtree overlaps this one's.
//
// The guard and the blast radius used to be measured in different units: the
// check was one exact root board id, and scope is the whole subtree. An adapter
// that cannot answer the wider question falls back to walking the chain with
// the narrower one — which still catches a run rooted ABOVE this board, the
// commoner half — rather than pretending the guard is stronger than it is.
func (s *Service) activeOverlapping(ctx context.Context, boardID string, ancestors []string) (*Run, error) {
	if wide, ok := s.runs.(RunOverlapStore); ok {
		return wide.ActiveOverlapping(ctx, boardID, ancestors)
	}
	for _, id := range append([]string{boardID}, ancestors...) {
		run, err := s.runs.ActiveByBoard(ctx, id)
		if err == nil && run != nil {
			return run, nil
		}
		if err != nil && !errors.Is(err, domain.ErrNotFound) {
			return nil, err
		}
	}
	return nil, domain.ErrNotFound
}

// transportOf names how this request reached the server, defaulting to web.
// An unstamped source would sort into whichever bucket a query listed first and
// bias exactly the provenance question the field exists to answer.
func transportOf(p *domain.Principal) string {
	if p != nil && p.Source != "" {
		return p.Source
	}
	return domain.SourceWeb
}

func boardCapOf(policy *domain.AgentPolicy) float64 {
	if policy == nil {
		return 0
	}
	return policy.DailyCapUSD
}

// ErrAgentClosedHere is the owner's "no AI on this board", stated as such.
//
// A board can be client-facing, contractual or under NDA, and the product could
// not express that at all — the only consent gate on a run was "can the caller
// edit here". The message names the decision rather than the mechanism, because
// a 403 on a board you can visibly edit reads as a bug.
var ErrAgentClosedHere = wrap(domain.ErrForbidden,
	"the owner of this board has turned the assistant off here")

// boardTitle names a board for a refusal message. A refusal that names no place
// is a refusal a person cannot act on.
func (s *Service) boardTitle(ctx context.Context, boardID string) string {
	if el, err := s.elements.Get(ctx, boardID); err == nil && el != nil {
		if t := el.Title(); t != "" {
			return sanitizeName(t)
		}
	}
	return "another board"
}

// ---------------------------------------------------------------------------
// Execution
// ---------------------------------------------------------------------------

// execute drives one run to PROPOSED (preview) or COMPLETED (auto).
func (s *Service) execute(ctx context.Context, p *domain.Principal, runID string) {
	defer func() {
		// A panic in the model or compiler path must terminate the run
		// cleanly rather than leave it non-terminal forever.
		if r := recover(); r != nil {
			s.log.Error("run panicked", zap.String("run", runID), zap.Any("panic", r))
			s.fail(ctx, runID, "the run stopped unexpectedly")
		}
	}()

	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, run.Task.Budget.Deadline)
	defer cancel()
	// Stop has to reach the provider call, not just the run row.
	defer s.holdCancel(runID, cancel)()

	if err := s.transition(ctx, run, StatePlanning, ""); err != nil {
		return
	}

	scope, err := CompileScope(ctx, s.elements, run.Task)
	if err == nil {
		s.attachLabels(ctx, scope, run.Task.Owner)
		s.attachPeople(ctx, p, scope)
		s.attachHistory(ctx, scope, run)
		s.attachAccountRules(ctx, scope, run.Tenant)
		s.attachAncestry(ctx, p, scope)
	}
	if err != nil {
		s.fail(ctx, runID, "could not read the board")
		return
	}
	s.emit(ctx, run, EvStepFinished, fmt.Sprintf("read board - %d items in scope", len(scope.Items)),
		map[string]any{"items": len(scope.Items)})

	if err := s.transition(ctx, run, StateRunning, ""); err != nil {
		return
	}
	// The run claims the canvas for as long as it works. Released by transition
	// on any terminal state and by the boot reconciler, so a crash cannot leave
	// a participant nobody can close.
	s.claimPresence(run, p.Name, "")
	defer s.releasePresence(run)

	emit := func(t EventType, msg string, data map[string]any) { s.emit(ctx, run, t, msg, data) }
	planner := NewPlanner(s.provider, s.elements, s.labels, s.txnRepo, s.images, s.links).
		OnSteer(func(runID string) []string { return s.drainSteers(ctx, runID) }).
		OnFiles(s.attachments).
		OnComments(s.comments)
	plan, usage, err := planner.Run(ctx, scope, run.Task, run.ID, emit, run.Plan)
	// Whether the RUN's clock ran out, read before the loop's context is
	// replaced. A hung provider and a deadline are the same event, and reporting
	// a provider outage as "Ran out of room" sends somebody to raise a limit that
	// was never reached.
	deadlineFired := errors.Is(ctx.Err(), context.DeadlineExceeded)

	// From here on the work is bookkeeping and the write, and none of it may be
	// killed by the clock that bounded the model loop — or by the Stop that just
	// aborted the provider call. A deadline firing between the last turn and the
	// terminal write leaves a run non-terminal, holding the board's only slot,
	// for the reconciler to later describe as a server restart. `emit` closes
	// over this variable, so the journal follows.
	settle, settleCancel := context.WithTimeout(context.WithoutCancel(ctx), settleWindow)
	defer settleCancel()
	ctx = settle

	run.Usage.Add(usage)
	// The spend is written by its OWN rev-refetching write, before anything else
	// can go wrong. It used to ride out on the terminal transition, which fails
	// its rev check the moment anyone else has touched the row — so a cancelled
	// run's tokens were charged by the provider and invisible to spentToday, and
	// the daily cap could not see the one class of run most likely to be
	// abandoned mid-flight.
	if usage.CostUSD > 0 || usage.InputTokens > 0 || usage.OutputTokens > 0 {
		spent := usage
		_ = s.persist(ctx, run, "spend", func(fresh *Run) {
			fresh.Usage.Add(spent)
			run.Usage = fresh.Usage
		})
	}
	// Stop, a reconciler sweep, or another replica may have finished this run
	// while the loop was still inside a provider call. Its verdict stands:
	// writing a second terminal state over it would replace an honest "you
	// stopped this" with a misleading failure.
	if stopped, gerr := s.runs.Get(ctx, runID); gerr == nil && stopped.State.Terminal() {
		return
	}
	if err != nil {
		// A hard stop no longer voids the work. The planner hands back whatever
		// it staged before the provider died or the clock ran out, and keeping it
		// on the run is what makes that prefix reviewable — and what gives
		// Continue an unmet list to pick up from. Without this, eight minutes and
		// $0.35 produced a red card and nothing else.
		if plan != nil && len(plan.Actions) > 0 {
			run.Plan = plan
			s.emit(ctx, run, EvPlanReady,
				fmt.Sprintf("stopped with %d change(s) staged", len(plan.Actions)),
				map[string]any{"actions": len(plan.Actions), "partial": true})
		}
		state, reason := terminalOutcome(err, deadlineFired)
		s.finishWithReason(ctx, run, state, reason)
		return
	}

	// A question has nothing to validate and nothing to apply — it just needs a
	// person. Send it straight to PROPOSED, where the bar renders it.
	if plan.Question != nil {
		run.Plan = plan
		s.emit(ctx, run, EvPlanReady, "needs an answer", map[string]any{"question": plan.Question.Text})
		_ = s.transition(ctx, run, StateProposed, "")
		return
	}

	// An answer with no changes: keep the plan so the summary reaches the user,
	// and end the run rather than offering an Apply button for nothing.
	if len(plan.Actions) == 0 {
		run.Plan = plan
		s.emit(ctx, run, EvPlanReady, "answered without changing anything", nil)
		s.finishWithReason(ctx, run, StatePartial, plan.Summary)
		return
	}

	// Preconditions run before anything is committed - the cheapest place to
	// catch a bad plan, and the only place with nothing to undo.
	verdict := Preconditions(plan, scope, run.Task)
	run.Verdict = &verdict
	s.emit(ctx, run, EvVerdict, "pre-commit checks", map[string]any{"passed": verdict.Passed})
	if !verdict.Passed {
		s.finishWithReason(ctx, run, StateFailed, "the proposed changes did not pass validation")
		return
	}

	run.Plan = plan
	// The plan the person is about to be SHOWN, kept apart from the plan that
	// eventually gets applied. Commit overwrites run.Plan with the effective one,
	// so without this snapshot the "what did they change about it" diff compares
	// the corrected plan against itself and every review looks like unanimous
	// agreement. Idempotent, so the refine and apply-retry edges back into
	// PROPOSED can pass through here without adopting a corrected plan as the
	// original proposal.
	run.FreezeProposal()
	s.emit(ctx, run, EvPlanReady, fmt.Sprintf("proposed %d change(s)", len(plan.Actions)),
		map[string]any{"actions": len(plan.Actions), "destructive": plan.Destructive()})

	// A quarantined plan never auto-applies, whatever the user chose. They
	// consented to skipping review of the agent's OWN judgement, not to skipping
	// review of a run that board content was steering.
	//
	// The downgrade is a SECURITY event in its own right, named separately so
	// alerting can key on the prefix — which is the whole reason the journal
	// vocabulary has a prefix. Until now the only trace a quarantine left was an
	// amber sentence on an ordinary review card, indistinguishable from a
	// validation refusal and uncountable afterwards.
	//
	// The run still goes to PROPOSED rather than to a terminal QUARANTINE: the
	// person consented to skipping review, and taking the plan away from them
	// entirely would answer an injection attempt by destroying their work. What
	// they get is the review they did not ask for, which is the point.
	if run.Task.Autonomy == AutonomyAuto && plan.Quarantined {
		s.emit(ctx, run, EvSecQuarantined,
			"something written on this board tried to steer the assistant, so this needs a human look",
			map[string]any{"autonomy": string(run.Task.Autonomy), "downgraded": true})
	}
	if run.Task.Autonomy == AutonomyAuto && !plan.Quarantined {
		if err := s.transition(ctx, run, StateApplying, ""); err != nil {
			return
		}
		if _, err := s.commit(ctx, p, run, nil); err != nil {
			s.log.Warn("auto-apply failed", zap.String("run", runID), zap.Error(err))
		}
		return
	}
	_ = s.transition(ctx, run, StateProposed, "")
}

// settleWindow bounds everything after the model loop: the terminal write, and
// under auto-apply the commit. Generous, because a commit that is refused
// halfway through by an expired context is the worst outcome available here.
const settleWindow = 2 * time.Minute

// snapPref reads the user's snap-to-grid preference so agent-placed columns sit
// on the same grid as hand-placed ones.
func (s *Service) snapPref(ctx context.Context, sub string) bool {
	if s.users == nil {
		return false
	}
	u, err := s.users.GetBySub(ctx, sub)
	if err != nil || u == nil {
		return false
	}
	return u.EffectiveSettings().Preferences.SnapToGrid
}

// ---------------------------------------------------------------------------
// Human decisions
// ---------------------------------------------------------------------------

// Apply commits a proposed run, folding in the human's adjustments.
//
// The client sends TYPED adjustments, never ops. The server recompiles from its
// own stored proposal, so an adjustment can rearrange what was proposed and
// nothing else (G2).
// variantIndex selects which offered SHAPE to commit. Nil — and any run that
// offered no alternatives — means Variants[0], which is what Plan.Actions
// already holds, so an unchanged client keeps its exact behaviour.
func (s *Service) Apply(ctx context.Context, p *domain.Principal, runID string, adjustments []Adjustment, variantIndex *int) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State != StateProposed || run.Plan == nil {
		return nil, ErrNotProposed
	}
	if err := chooseVariant(run, variantIndex); err != nil {
		return nil, err
	}
	if _, err := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); err != nil {
		// A plan whose author can no longer edit the board is not a plan any
		// more, and leaving it PROPOSED would hold that board's only run slot for
		// two hours on behalf of somebody who has been removed from it. DENIED
		// with the reason, which is a state the product already renders and that
		// nothing had ever been able to produce.
		s.finishWithReason(ctx, run, StateDenied,
			"you no longer have permission to edit this board, so this plan cannot be applied")
		return nil, err
	}
	if err := s.transition(ctx, run, StateApplying, ""); err != nil {
		return nil, err
	}
	return s.commit(ctx, p, run, adjustments)
}

// chooseVariant swaps in the shape the person picked from the review's
// segmented control.
//
// The run built K complete shapes, laid every one of them out and measured them
// — and apply committed Variants[0] whichever one the person had chosen. Never
// wrong, only always less than what was offered, which is the failure mode that
// is hardest to notice: the picker looked live and the choice went nowhere.
//
// Each variant's actions were already laid out, ordered and destination-labelled
// at PROPOSED time, so selecting one is a swap and not a recompile.
//
// The discarded shapes are deliberately KEPT on the plan rather than dropped.
// Two reasons: the membership fingerprint was taken over the UNION of all
// shapes, so the record of what it covered has to survive alongside it; and a
// shape the person looked at and turned down is a first-class correction — the
// clearest statement of preference the product ever gets — which is worth far
// more stored than deleted.
//
// Nothing is written here. The APPLYING transition immediately after is a whole-
// run write, so the selection persists with it; a second write would spend the
// run's rev and make that transition fail on a conflict with itself.
func chooseVariant(run *Run, variantIndex *int) error {
	if variantIndex == nil || len(run.Plan.Variants) == 0 {
		return nil
	}
	i := *variantIndex
	if i < 0 || i >= len(run.Plan.Variants) {
		return ErrNoSuchVariant
	}
	run.Plan.Actions = append([]Action(nil), run.Plan.Variants[i].Actions...)
	run.Plan.ChosenVariant = i
	return nil
}

// commit is the write path: recompile, re-check, one transaction, verify.
func (s *Service) commit(ctx context.Context, p *domain.Principal, run *Run, adjustments []Adjustment) (*Run, error) {
	scope, err := CompileScope(ctx, s.elements, run.Task)
	if err != nil {
		s.fail(ctx, run.ID, "could not re-read the board")
		return nil, err
	}
	// Membership is checked BEFORE Hydrate widens the scope, against the same
	// shape the plan was compiled from. A card added to this board while the
	// human was reviewing touches nothing the plan names, so per-element
	// timestamps all still match — and the plan would commit against a board it
	// never saw, orphaning the new card outside a grouping built without it.
	if !CheckMembership(run.Plan, scope) {
		_ = s.transition(ctx, run, StateProposed, "")
		s.emit(ctx, run, EvError, "the board gained or lost items while you were reviewing", nil)
		return nil, ErrStalePlan
	}

	// The plan may reference elements the agent found by reading a nested
	// board, so the scope is widened to everything the plan touches before it
	// is validated against.
	//
	// This branch and the fingerprint one below used to `return nil, err` from
	// APPLYING with no terminal write at all, so a read failure wedged the run
	// non-terminal — holding the board's only slot until the reconciler came
	// past and described it as a server restart. No path may leave APPLYING
	// without reaching a terminal state.
	if err := scope.Hydrate(ctx, s.elements, run.Plan); err != nil {
		s.finishWithReason(ctx, run, StateFailed, "could not re-read what the plan refers to")
		return nil, err
	}

	// The person's own edits are applied BEFORE the staleness check, not after.
	//
	// Checking run.Plan meant the check covered rows the person had already
	// dropped, so once one element moved under the plan every subsequent Apply
	// failed identically no matter what they did — there was no way to say "fine,
	// leave that one out and do the rest". Exact-action binding is a statement
	// about what will be WRITTEN, so it is measured against that.
	// drops is the honest half of the correction record. Dropping one container
	// row silently takes its children with it, so a review recorded from the
	// adjustment list alone reports one rejection where ten changes were actually
	// refused — the supervision signal understated by 10x, in the direction that
	// makes the agent look better than it was.
	effective, drops := ApplyAdjustmentsDetailed(run.Plan, adjustments, scope)
	effective.Fingerprint = fingerprintFor(run.Plan, effective)
	if len(effective.Actions) == 0 {
		s.finishWithReason(ctx, run, StateDiscarded, "every change was removed")
		return run, nil
	}

	// Exact-action binding: the plan was computed against specific element
	// versions, and only those. If a collaborator changed one while the human
	// was reviewing, the approval no longer describes reality.
	stale, err := CheckFingerprint(ctx, s.elements, effective)
	if err != nil {
		s.finishWithReason(ctx, run, StateFailed, "could not check whether the board had changed")
		return nil, err
	}
	if len(stale) > 0 {
		// Recorded on the run, not only broadcast. A client that has to mine the
		// event journal to learn what moved loses that answer on reload, and the
		// person is back to a toast that names nothing.
		_ = s.persist(ctx, run, "stale elements", func(fresh *Run) {
			fresh.StaleElementIDs = stale
			run.StaleElementIDs = stale
		})

		// DROP THE STALE ROWS AND KEEP THE REST.
		//
		// Aborting the whole plan was the old answer, and on a two-person board
		// it made a thirty-action plan nearly un-appliable: a colleague touching
		// one card while you read the review threw away twenty-nine changes that
		// were still exactly right, and the only exit was to discard the run and
		// pay for another. It also makes a scheduled run and a person working at
		// the same time starve each other.
		//
		// Rebasing reuses the ApplyAdjustments path — the same machinery the
		// person's own drops go through, cascades included — so the compiler sees
		// a plan of the shape it always sees. What must never happen is a silent
		// partial: the skipped work becomes a named unmet, so the outcome says
		// "I did not touch the Casting card — it changed while you were
		// reviewing" instead of quietly doing less than it showed.
		rebased := ApplyAdjustments(effective, dropsForStale(effective, stale), scope)
		if len(rebased.Actions) == 0 || len(rebased.Actions) == len(effective.Actions) {
			// Everything went stale, or nothing could be attributed to a stale
			// element — there is no honest subset, so the plan goes back for
			// review exactly as it used to.
			_ = s.transition(ctx, run, StateProposed, "")
			s.emit(ctx, run, EvError, "the board changed while you were reviewing",
				map[string]any{"staleElements": stale})
			return nil, ErrStalePlan
		}
		skipped := len(effective.Actions) - len(rebased.Actions)
		effective = rebased
		effective.Fingerprint = fingerprintFor(run.Plan, effective)
		addUnmet(effective, Unmet{
			Request: fmt.Sprintf("%d change(s) that were already edited by somebody else", skipped),
			Why:     "those items changed while you were reviewing, so the approved change no longer described them",
		})
		s.emit(ctx, run, EvError, "some items changed while you were reviewing and were left alone",
			map[string]any{"staleElements": stale, "skipped": skipped})
	}

	verdict := Preconditions(effective, scope, run.Task)
	if !verdict.Passed {
		run.Verdict = &verdict
		s.finishWithReason(ctx, run, StateFailed, "the adjusted changes did not pass validation")
		return nil, domain.ErrValidation
	}

	ops, err := CompileOps(effective, scope)
	if err != nil {
		s.finishWithReason(ctx, run, StateFailed, "could not build the changes")
		return nil, err
	}

	// Labels and comment bodies the run coined exist only in the plan until this
	// moment. They are inserted BEFORE the ops, because the ops reference their
	// ids — and only here, so a discarded or reverted preview never leaves a
	// stray tag in the user's taxonomy. Insert is idempotent on id, so a retried
	// apply is safe.
	//
	// "Only here" protected against discard and revert and NOT against failure,
	// which is the case where it matters most: an expired grant, a delegation
	// refusal, a rejected op or a journal conflict all returned with the labels
	// and comments already written and no cleanup anywhere. The residue is
	// invisible and cumulative — a label that appears in the filter bar and on
	// nothing, one set per failed apply — and it defeats the guidance the agent
	// is given to reuse rather than coin tags. So the pre-writes are tracked and
	// rolled back on every path that does not reach a durable journal row.
	pre := &preWrites{}
	defer func() {
		if pre.keep {
			return
		}
		s.rollBackPreWrites(ctx, run, pre)
	}()

	if s.labels != nil {
		for _, l := range effective.NewLabels {
			if l == nil || l.OwnerID != p.Sub {
				continue // a label for someone else is not ours to make
			}
			if _, gerr := s.labels.Get(ctx, l.ID); gerr == nil {
				continue
			}
			if l.CreatedAt.IsZero() {
				l.CreatedAt = time.Now().UTC()
			}
			if err := s.labels.Insert(ctx, l); err != nil {
				s.finishWithReason(ctx, run, StateFailed, "could not create the new labels")
				return nil, err
			}
			pre.labels = append(pre.labels, l.ID)
		}
	}

	var wroteComments []*domain.Comment
	if s.comments != nil {
		for _, c := range effective.NewComments {
			if c == nil || c.AuthorID != p.Sub {
				continue
			}
			if c.ID == "" {
				c.ID = s.newID()
			}
			if c.CreatedAt.IsZero() {
				c.CreatedAt = time.Now().UTC()
			}
			// AuthorID stays the human — it is the ACL key and the edit key —
			// so the honesty about WHO wrote the words lives in these two
			// fields. Stamped here rather than at staging time because this is
			// the last point before the row is durable, and a comment that
			// reached storage unstamped would be indistinguishable from
			// something the person typed for the rest of its life.
			c.Origin = domain.OriginAgent
			c.AgentRunID = run.ID
			if err := s.comments.Insert(ctx, c); err != nil {
				s.log.Warn("agent: could not write comment body", zap.Error(err))
				continue
			}
			pre.comments = append(pre.comments, c.ID)
			wroteComments = append(wroteComments, c)
		}
	}

	aclBefore := aclHash(scope.Board.ACL)

	// The agent's principal for this write: the human's identity, attenuated by
	// the run's grant. Every op is re-validated against it inside the same write
	// path a human's drag uses.
	// Widen containment to the boards this approved plan files into — and only
	// after checking each against the HUMAN's own edit rights, with their own
	// principal. The grant is a copy: the stored delegation is never mutated, so
	// a destination authorised for this commit does not persist into the next.
	grant := run.Delegation
	if dests := s.authorizeDestinations(ctx, p, effective); len(dests) > 0 {
		grant.DestinationBoardIDs = dests
	}
	// A human pressed Apply exactly when the run is in preview: the unattended
	// branch commits straight out of the loop. Recording it here rather than
	// passing a flag down keeps the two facts — what the board permits, and what
	// actually happened — checkable against each other in one place.
	approved := run.Task.Autonomy != AutonomyAuto
	if grant.RequiresApproval && !approved {
		// Belt and braces on top of the admission downgrade. If this ever fires
		// it means something restored auto-apply on a board whose owner
		// reserved it, and the right answer is to refuse the write rather than
		// to trust that admission got it right.
		s.finishWithReason(ctx, run, StateDenied,
			"this board's owner reserves unattended runs, so this plan needs a person to apply it")
		return nil, domain.ErrForbidden
	}
	if approved {
		grant.ApprovedBy = p.Sub
	}
	// An expired grant does not fail the apply — it is re-minted against the
	// human's LIVE edit right.
	//
	// The grant lasted 30 minutes and the proposal 2 hours, so for ninety minutes
	// the product showed an Apply button guaranteed to fail: preview before
	// lunch, apply after, and the answer was a red card reading "the changes were
	// rejected" with no mention of expiry and no way to re-authorise. The grant's
	// purpose is to bound the MODEL, and the model is no longer in the loop at
	// this point; the person is present and their permission is checked here and
	// now. Only if that check fails does the run stop, and then as DENIED with
	// the honest reason rather than as a generic failure.
	if grant.Expired(time.Now().UTC()) {
		if _, aerr := s.access.RequireEdit(ctx, run.Task.RootBoardID, p); aerr != nil {
			s.finishWithReason(ctx, run, StateDenied,
				"you no longer have permission to edit this board, so this plan cannot be applied")
			return nil, aerr
		}
		grant.ExpiresAt = time.Now().UTC().Add(commitGrantWindow)
	}
	agentPrincipal := &domain.Principal{
		Sub: p.Sub, Email: p.Email, Name: p.Name,
		RequestID: run.RequestID, Source: run.Source,
		Delegation: &grant,
	}
	txn, err := s.txns.ApplyWithMeta(ctx, agentPrincipal, run.Task.RootBoardID, "", ops, service.TxnMeta{
		TxnID:      run.ID,
		Origin:     domain.OriginAgent,
		AgentRunID: run.ID,
		Source:     run.Source,
		// So the bells this write rings can say whose decision they carry.
		ApprovedByHuman: approved,
	})
	if err != nil {
		// A batch that failed partway can still have left work standing, and the
		// write path now journals exactly that rather than swallowing it. The run
		// has to own the row: it is the revert handle, and without it the person
		// is looking at changes nothing in the product admits to.
		if txn != nil {
			pre.keep = true
			run.TransactionIDs = append(run.TransactionIDs, txn.ID)
			standing := txn.ID
			_ = s.persist(ctx, run, "partial transaction id", func(fresh *Run) {
				for _, id := range fresh.TransactionIDs {
					if id == standing {
						return
					}
				}
				fresh.TransactionIDs = append(fresh.TransactionIDs, standing)
			})
			s.emit(ctx, run, EvError,
				fmt.Sprintf("%d change(s) landed before this failed and could not be rolled back", len(txn.Ops)),
				map[string]any{"transactionId": txn.ID, "ops": len(txn.Ops), "partial": true})
			s.finishWithReason(ctx, run, StateFailed,
				"some of the changes landed before this failed — they are still standing and can be undone")
			return run, err
		}
		// A refused authority is not the same event as a broken one, and the
		// product ships copy and a UI branch for the difference that nothing had
		// ever been able to reach.
		if errors.Is(err, domain.ErrForbidden) {
			s.finishWithReason(ctx, run, StateDenied,
				"this change was refused: it reaches beyond what the assistant was allowed to touch")
			return nil, err
		}
		s.finishWithReason(ctx, run, StateFailed, "the changes were rejected")
		return nil, err
	}

	// Past this point the board has been written and the labels and comments are
	// part of what landed, so they are no longer residue to roll back.
	pre.keep = true

	run.Plan = effective
	// Safe to overwrite Plan only because ProposedPlan was frozen at PROPOSED:
	// the diff below reads the frozen copy, not this one. Recorded here rather
	// than at the review click because this is the first point where "they
	// approved it AND it landed" is true — a correction recorded before the write
	// would label capabilities from a run that then failed.
	run.RecordCorrections(adjustments, drops, OutcomeApplied, time.Now().UTC())
	run.TransactionIDs = append(run.TransactionIDs, txn.ID)
	// The revert handle is written by its OWN rev-refetching write, immediately,
	// and never as a side effect of a state transition. It used to be persisted
	// only by the move to VERIFYING, which writes with optimistic concurrency and
	// rolls back on conflict — so a steer, a refine or a reconcile sweep landing
	// at that instant orphaned the committed transaction from its run forever:
	// invisible to Revert, to RevertOne and to the audit view, with the work
	// standing on the board.
	txnID := txn.ID
	_ = s.persist(ctx, run, "transaction id", func(fresh *Run) {
		for _, id := range fresh.TransactionIDs {
			if id == txnID {
				return
			}
		}
		fresh.TransactionIDs = append(fresh.TransactionIDs, txnID)
	})
	s.emit(ctx, run, EvOpsCommitted, fmt.Sprintf("applied %d change(s)", len(effective.Actions)),
		map[string]any{"ops": len(ops), "transactionId": txn.ID})
	// The run id goes in the ACTOR, not only the meta: the row's subject is a
	// human who is answerable for the change, acting through a run — and
	// `approved` is what separates "they read this plan and pressed Apply" from
	// "the board's autonomy setting committed it while nobody was watching",
	// which is the distinction the whole consent design exists to make.
	s.audit.Emit(ctx, p, &domain.AuditEvent{
		Actor:  domain.AuditActor{AgentRunID: run.ID},
		Action: "agent.run_applied",
		Target: domain.AuditTarget{Type: "board", ID: run.Task.RootBoardID},
		Meta: map[string]any{
			"runId": run.ID, "transactionId": txn.ID,
			"ops": len(ops), "actions": len(effective.Actions),
			"approvedByHuman": approved,
		},
	})
	// Now that the thread elements exist, the comments the run wrote get
	// everything a human's comment gets: the live push, the owner's bell,
	// mentions. Announced AFTER the transaction because the thread an agent
	// comment hangs on is usually created BY that transaction — an announce
	// before it would resolve nothing.
	s.announceComments(ctx, p, wroteComments)

	if err := s.transition(ctx, run, StateVerifying, ""); err != nil {
		// The work is on the board and its handle is durable; what failed is a
		// state write. Terminating is the only honest exit — leaving the run in
		// APPLYING would hold the board's slot over a completed change.
		s.finishWithReason(ctx, run, StateCompleted, "")
		return run, nil
	}

	// Completion is decided from re-read state, not from having reached the end
	// of the function.
	post := Postconditions(ctx, s.elements, effective, scope, aclBefore)
	run.Verdict = &post
	s.emit(ctx, run, EvVerdict, "post-commit checks", map[string]any{"passed": post.Passed})

	if !post.Passed {
		// Reality disagrees with intent: put the board back and fail, rather than
		// leaving the user with a half-applied change they did not ask for.
		//
		// The sentence branches on whether the restore actually worked. It used
		// not to: the person was told "the board was restored" in their own
		// language whether or not it had been, and a run in FAILED offered no
		// Undo, so a failed auto-revert left changes standing that nothing in the
		// product admitted to or could reach.
		s.log.Error("postconditions failed; auto-reverting", zap.String("run", run.ID))
		_, gone, rerr := s.revertTransactions(ctx, agentPrincipal, run)
		if rerr != nil {
			s.log.Error("auto-revert failed", zap.String("run", run.ID), zap.Error(rerr))
			s.finishWithReason(ctx, run, StateFailed,
				"the result did not verify and the board could not be put back — the changes are still standing")
			return run, nil
		}
		if len(gone) > 0 {
			// The auto-revert is seconds old, so a target being unreachable means
			// something else destroyed it inside this run's own lifetime. Rare,
			// but saying "I put the board back" when part of it could not be is
			// the exact dishonesty the branch above exists to stop.
			s.finishWithReason(ctx, run, StateFailed,
				"the result did not verify; I put most of the board back, but "+permanentlyGoneReason(len(gone)))
			return run, nil
		}
		s.finishWithReason(ctx, run, StateFailed, "the result did not verify; I put the board back")
		return run, nil
	}

	s.finishWithReason(ctx, run, StateCompleted, "")
	return run, nil
}

// commitGrantWindow is how long a re-minted grant lives: long enough for one
// write, short enough that it is not a standing authority.
const commitGrantWindow = 5 * time.Minute

// preWrites are the rows an apply inserts BEFORE the ops that reference them.
type preWrites struct {
	labels   []string
	comments []string
	// keep is set once the transaction is durable: from that moment the rows are
	// part of what landed rather than residue.
	keep bool
}

// commentRemover is the half of the comment store that can forget. Optional,
// because the port itself has no delete of any kind — which is why nothing has
// ever cleaned up after a failed apply.
type commentRemover interface {
	Delete(ctx context.Context, id string) error
}

// rollBackPreWrites removes the labels and comment bodies an apply inserted on
// its way to failing, and journals what it took back.
func (s *Service) rollBackPreWrites(ctx context.Context, run *Run, pre *preWrites) {
	if len(pre.labels) == 0 && len(pre.comments) == 0 {
		return
	}
	removed := 0
	if s.labels != nil {
		for _, id := range pre.labels {
			// Guarded on usage: between the insert and here, a concurrent human
			// edit may have attached the tag to something of their own, and
			// deleting it then would take a label off a card nobody asked about.
			if l, err := s.labels.Get(ctx, id); err == nil && l != nil && l.UsageCount > 0 {
				continue
			}
			if err := s.labels.Delete(ctx, id); err != nil {
				s.log.Warn("could not roll back a label a failed apply coined",
					zap.String("label", id), zap.Error(err))
				continue
			}
			removed++
		}
	}
	if remover, ok := s.comments.(commentRemover); ok {
		for _, id := range pre.comments {
			if err := remover.Delete(ctx, id); err != nil {
				s.log.Warn("could not roll back a comment a failed apply wrote",
					zap.String("comment", id), zap.Error(err))
				continue
			}
			removed++
		}
	}
	if removed > 0 {
		s.emit(ctx, run, EvError, fmt.Sprintf("rolled back %d row(s) this apply had written before it failed", removed),
			map[string]any{"labels": pre.labels, "comments": pre.comments})
	}
}

// Discard abandons a plan without writing anything.
//
// It takes the person's adjustments even though nothing will be written, and
// that is the whole point of the payload: "I dropped four rows, reparented two,
// and then gave up" names exactly which capabilities failed, with typed targets,
// and it is the highest-weight label in the correction set. Sent and then thrown
// away, the same abandonment reads as an undifferentiated "no".
func (s *Service) Discard(ctx context.Context, p *domain.Principal, runID string, adjustments []Adjustment) (*Run, error) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return nil, err
	}
	if run.Tenant != p.Sub {
		// The owner may RECLAIM the board without ever seeing the plan.
		//
		// PROPOSED is non-terminal, so it holds the board's only run slot, and
		// Discard was tenant-gated — so an abandoned preview locked the
		// assistant for everyone else on that board for the whole lifetime, and
		// their only signal was a polite message offering no action. This path
		// cancels; it never loads the plan body, and it is not allowed to
		// return one.
		return s.reclaimProposal(ctx, p, run)
	}
	if run.State != StateProposed {
		return nil, ErrNotProposed
	}
	// Recorded BEFORE forgetRejectedProse, which is about to destroy the very
	// titles the labels are derived from. The correction is the shape of the
	// decision — which rows, of what kind, against which elements — and that
	// shape is exactly what the privacy strip is designed to keep.
	if len(adjustments) > 0 {
		_, drops := ApplyAdjustmentsDetailed(run.Plan, adjustments, nil)
		run.RecordCorrections(adjustments, drops, OutcomeAbandoned, time.Now().UTC())
		corrections := run.Corrections
		_ = s.persist(ctx, run, "abandonment corrections", func(fresh *Run) {
			fresh.Corrections = corrections
		})
	}
	// The material a reflection reasons over, taken while it still exists. The
	// terminal write has to come first — Reflect must never reopen a run — and by
	// then the prose is gone, so the snapshot is the only way to have both.
	rejected := snapshotForReflection(run)
	// Pressing Discard is the person saying "not this", and the system was filing
	// it permanently: DISCARDED runs are excluded from the audit VIEW and were
	// never excluded from storage, so every rejected plan — its drafted titles,
	// its note bodies, its comment text — was retained verbatim under the
	// person's tenantSub with nothing in the product admitting it existed.
	//
	// What survives is the shape of the decision: you asked X, and you rejected
	// the answer. That is the whole audit value; the rejected sentences are not
	// part of it. Cancel deliberately does NOT do this — a run stopped partway
	// still has a partial plan somebody may want to continue from.
	forgetRejectedProse(run)
	// Rejecting a plan that board content was steering is not the same event as
	// rejecting one the agent simply got wrong, and the difference is the only
	// thing that makes injection attempts countable.
	if run.Plan != nil && run.Plan.Quarantined {
		s.finishWithReason(ctx, run, StateQuarantine,
			"discarded a plan that board content had tried to steer")
		return run, nil
	}
	s.finishWithReason(ctx, run, StateDiscarded, "")
	s.learnFromRejection(ctx, run, rejected, nil, true)
	return run, nil
}

// AnswerRule records the person's verdict on a rule the run proposed.
//
// Recorded BEFORE the board write, and separately from it. The card is a
// two-button experiment the product has been running on every corrected run and
// never scored: Save appended a sentence to the board's rules through an
// ordinary transaction with no run link, "Not now" set a local flag and the
// component vanished. So the direct measure of whether the memory work is worth
// building — do people want the rules it proposes — was unanswerable, and an
// accepted rule entered the rules string indistinguishable from one the owner
// typed. The moment the provenance was knowable was the moment it was thrown
// away.
//
// It deliberately does NOT write the board. The rule text is board content and
// belongs on the ordinary transaction path where it is undoable and syncs like
// any other edit; this endpoint's whole job is the fact that the question was
// answered, which survives the person then undoing the write.
func (s *Service) AnswerRule(ctx context.Context, p *domain.Principal, runID string, accepted bool) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.Plan == nil || run.Plan.ProposedRule == "" {
		// Nothing was ever offered, so there is no answer to record. Refused
		// rather than stored, because a verdict on a rule that does not exist
		// would land in the denominator of the only rate this measures.
		return nil, ErrNothingToDo
	}
	outcome := RuleDeclined
	if accepted {
		outcome = RuleAccepted
	}
	if err := s.persist(ctx, run, "rule outcome", func(fresh *Run) {
		fresh.RuleOutcome = outcome
		fresh.UpdatedAt = time.Now().UTC()
		run.RuleOutcome = outcome
	}); err != nil {
		return nil, err
	}
	return run, nil
}

// snapshotForReflection copies the run down to the plan it was judged on, so a
// reflection can still read what the person turned down after the run's own copy
// has been stripped. Nil when there is nothing to reason over.
func snapshotForReflection(run *Run) *Run {
	if run == nil {
		return nil
	}
	plan := run.ProposedPlan
	if plan == nil {
		plan = run.Plan
	}
	if plan == nil {
		return nil
	}
	frozen := *plan
	frozen.Actions = append([]Action(nil), plan.Actions...)
	shadow := *run
	shadow.Plan, shadow.ProposedPlan = &frozen, &frozen
	return &shadow
}

// forgetRejectedProse strips the words out of a plan the person turned down,
// keeping its shape: how many actions, of what kinds, against which elements.
// Both copies. ProposedPlan is a frozen snapshot of the plan as it was SHOWN,
// kept so the review diff has something to compare against — which also makes it
// a second verbatim copy of exactly the prose this function exists to destroy.
// Stripping only run.Plan would have left "not this" filed permanently anyway,
// one field to the left.
func forgetRejectedProse(run *Run) {
	if run == nil {
		return
	}
	for _, plan := range []*Plan{run.Plan, run.ProposedPlan} {
		if plan == nil {
			continue
		}
		for i := range plan.Actions {
			a := &plan.Actions[i]
			a.Title, a.Text, a.URL = "", "", ""
			a.Tasks, a.Rows = nil, nil
		}
		plan.NewComments = nil
	}
}

// Refine sends a proposed plan back for another pass with the person's steer.
//
// This is what turns the agent from a vending machine into a collaborator.
// Nobody gets a structural request right the first time, because seeing the
// wrong answer is how you discover what you wanted. Before this, "make it four
// columns instead" meant discard, retype, and pay again — and the second
// attempt had no idea what was wrong with the first.
//
// The run keeps its identity, its budget and its board slot. Cost accumulates
// against the SAME run, so the meter the user sees is the true cost of the
// conversation rather than of its last turn.
//
// The adjustments are the edits the person already made BY HAND to the proposal
// — rows dropped, titles retyped, destinations changed — and they travel with
// the note because they are the other half of the same sentence. Dropping three
// rows and then typing "also make it four columns" used to discard the three
// drops the moment the refine landed, so the next pass proposed them again and
// the person had to delete them twice.
func (s *Service) Refine(ctx context.Context, p *domain.Principal, runID, note string, adjustments []Adjustment) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State != StateProposed {
		return nil, ErrNotProposed
	}
	note = truncate(sanitizeBody(note), 1000)
	if note == "" {
		return nil, ErrNoIntent
	}
	if len(run.Task.Refinements) >= maxRefinements {
		return nil, fmt.Errorf("%w: this run has been revised enough — apply it or start fresh", ErrBudget)
	}
	// A refinement costs real tokens, so it is subject to the same ceilings as a
	// new run — the deployment's, and this board's. Otherwise the cap is
	// trivially escaped by revising.
	if err := s.checkBudgetMidRun(ctx, run); err != nil {
		return nil, err
	}

	run.Task.Refinements = append(run.Task.Refinements, note)
	run.Task.Adjustments = adjustments
	// Each pass gets fresh steps; without this a long conversation would starve
	// on the budget its first turn already spent.
	run.Task.Budget = DefaultBudget()
	run.Task.Budget.Normalize()
	// Optimistic concurrency: two people revising the same proposal at once
	// must not silently interleave their steers.
	if err := s.runs.Update(ctx, run, run.Rev); err != nil {
		return nil, err
	}
	s.emit(ctx, run, EvRefined, "revision requested", map[string]any{"note": note})

	principal := &domain.Principal{Sub: p.Sub, Email: p.Email, Name: p.Name}
	go s.execute(context.Background(), principal, run.ID)
	return run, nil
}

// maxRefinements bounds one conversation. Past a handful of passes the honest
// advice is to start again with a clearer request, not to keep nudging.
const maxRefinements = 5

// Steer hands a correction to a run that is still working.
//
// It does not interrupt: the note is queued and the loop picks it up between
// steps, which keeps the model's turn structure intact and means a steer can
// never arrive in the middle of a tool call. The cost is up to one step of
// latency; the alternative was watching a run go wrong with only a kill switch.
func (s *Service) Steer(ctx context.Context, p *domain.Principal, runID, note string) (*Run, error) {
	run, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if run.State.Terminal() || run.State == StateProposed {
		// A finished run takes a retry; a proposed one takes a refinement.
		// Both already exist and both are better than this.
		return nil, ErrNotRunning
	}
	note = truncate(sanitizeBody(note), 400)
	if note == "" {
		return nil, ErrNoIntent
	}
	if len(run.Steers) >= maxSteersPerRun {
		return nil, fmt.Errorf("%w: this run has been steered enough — let it finish and revise the plan", ErrBudget)
	}
	run.Steers = append(run.Steers, note)
	if err := s.runs.Update(ctx, run, run.Rev); err != nil {
		return nil, err
	}
	s.emit(ctx, run, EvSteered, "steered mid-run", map[string]any{"note": note})
	return run, nil
}

// drainSteers takes whatever corrections have been queued and clears them, so
// each is delivered exactly once.
//
// Re-read from storage rather than from the in-memory run: the steer arrives on
// a different request entirely, and the copy this goroutine is holding was
// loaded before the person had typed anything.
func (s *Service) drainSteers(ctx context.Context, runID string) []string {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return nil
	}
	// Belt and braces for the multi-replica case. The abort handle lives in the
	// process that started the run, so a Stop served by a different replica has
	// nothing local to cancel; this is the one read per turn that lets the
	// working replica notice its run has been stopped underneath it. The turn
	// boundary is where a steer is already delivered, so it costs nothing new.
	if run.State.Terminal() {
		s.abort(runID)
		return nil
	}
	if len(run.Steers) == 0 {
		return nil
	}
	notes := run.Steers
	run.Steers = nil
	if err := s.runs.Update(ctx, run, run.Rev); err != nil {
		// Leaving them queued is the safe failure: a steer delivered twice is
		// confusing, one delivered late is merely slow.
		return nil
	}
	// Emitted only AFTER the queue is durably cleared, so the client never sees
	// "Heard" for a note that is about to be redelivered.
	//
	// The person had no evidence a steer had gone anywhere: they typed "no, not
	// the archive", watched the identical spinner, typed it again, and the third
	// repetition was refused by maxSteersPerRun with a message that reads as a
	// telling-off. The chip that turns Queued into Heard has shipped and been
	// waiting on this line — until it existed, every note showed as queued
	// forever.
	for _, note := range notes {
		s.emit(ctx, run, EvSteerDelivered, "your note reached the assistant",
			map[string]any{"note": note})
	}
	return notes
}

// maxSteersPerRun bounds the queue. Past a few, a person is having a
// conversation the review step is better shaped for.
const maxSteersPerRun = 3

// Retry starts a fresh run from an earlier one's request.
//
// Every terminal failure — a provider timeout, a validation refusal, a budget
// ceiling — used to end at a card whose only action reopened an EMPTY composer.
// The intent, the scope, the attachments and the refinements were all still on
// the server and none of them were reused, so the person retyped a paragraph to
// recover from a transient error.
//
// A new run rather than a resurrection: the old one is terminal, it spent what
// it spent, and its journal stays intact as the record of what happened.
func (s *Service) Retry(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	prior, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if !prior.State.Terminal() {
		// Still going, or still awaiting a decision. Retrying that would mean
		// two runs on one board, which the single-run invariant forbids and
		// which would be the wrong thing anyway.
		return nil, domain.ErrConflict
	}
	// The steers the person already gave are part of the request, and they are
	// carried as REFINEMENTS rather than glued onto the intent.
	//
	// Concatenating them produced one long string that Create then truncates at
	// a thousand characters — so a run revised five times replayed with the later
	// corrections, the ones the person cared about most, cut off the end. The
	// planner already has a replay path for refinements; this just stops
	// destroying them on the way to it.
	//
	// Viewport travels too. Without it a retry places its work somewhere other
	// than where the original would have, so even a successful recovery lands in
	// the wrong part of the canvas.
	if err := s.refuseARetryLoop(ctx, p, prior); err != nil {
		return nil, err
	}
	return s.Create(ctx, p, CreateRequest{
		BoardID:        prior.Task.RootBoardID,
		Intent:         prior.Task.Intent,
		Refinements:    prior.Task.Refinements,
		Scope:          prior.Task.Scope,
		SelectionID:    prior.Task.SelectionID,
		Autonomy:       prior.Task.Autonomy,
		AttachmentIDs:  prior.Task.AttachmentIDs,
		Viewport:       prior.Task.Viewport,
		ActiveLabelIDs: prior.Task.ActiveLabelIDs,
		// The link the retry rate is computed from — and the chain this method
		// now walks to notice it is going in circles.
		RetryOfRunID: prior.ID,
		// Set here, not carried from the prior run: a retry of a typed request
		// is a retry, and inheriting `typed` would put both halves of the retry
		// rate in the same bucket as its own denominator.
		Source: SourceRetry,
	})
}

// dropsForStale turns the fingerprint's stale-id list into the drop adjustments
// that remove exactly the rows naming those elements.
//
// Expressed as adjustments rather than as a bespoke filter so the cascade rules
// apply: dropping a column the person can no longer safely touch has to take the
// cards being filed into it as well, and ApplyAdjustments is the one place that
// knows how.
func dropsForStale(p *Plan, stale []string) []Adjustment {
	doomed := make(map[string]bool, len(stale))
	for _, id := range stale {
		doomed[id] = true
	}
	var out []Adjustment
	for _, a := range p.Actions {
		if a.Kind.Creates() || a.ElementID == "" || !doomed[a.ElementID] {
			continue
		}
		out = append(out, Adjustment{Kind: AdjustDrop, Seq: a.Seq})
	}
	return out
}

// refuseARetryLoop stops the third identical attempt.
//
// Retry is the primary action on every FAILED / BUDGET_EXHAUSTED / CANCELLED /
// DENIED card and it was unbounded and uninformed: when the provider is down, or
// a precondition deterministically rejects this board's shape, pressing it is a
// loop — press, wait up to eight minutes, receive the identical sentence, press
// again, each pass charged at full price against the daily cap, with nothing
// anywhere saying that the same thing has now failed three times.
//
// The bound is on SAMENESS, not on count: two different failures are two real
// attempts and both should run. Only when a run and the run it was itself a
// retry of ended the same way does this refuse, and the refusal says what it
// knows rather than just saying no — that is the whole value of having noticed.
func (s *Service) refuseARetryLoop(ctx context.Context, p *domain.Principal, prior *Run) error {
	if prior.RetryOfRunID == "" {
		return nil // a first retry always runs
	}
	grandparent, err := s.load(ctx, p, prior.RetryOfRunID)
	if err != nil {
		return nil // the chain is unreadable; refusing on that would be worse
	}
	if grandparent.State != prior.State || grandparent.Reason != prior.Reason {
		return nil
	}
	detail := prior.Reason
	if detail == "" {
		detail = string(prior.State)
	}
	return wrap(domain.ErrConflict, "this failed the same way twice — "+detail+
		". Trying again now would cost another pass and end the same way; check whether the assistant is available, or change what you are asking for.")
}

// Continue starts a new run from what the last one did not finish.
//
// A job bigger than one run's budget had no way to span two. The film build was
// cut at thirty actions with its last column created and empty, and the only
// way to say "keep going" was to type a fresh prompt that knew nothing about
// what had just happened — which is precisely how the follow-up built a second
// copy of the structure beside the first.
//
// The intent is composed HERE, from the prior run's own unmet list, rather than
// taken from the client. The unmet list is the agent's own account of what it
// left behind; a client-supplied "continue" would be one more context-free
// request, which is the thing being fixed.
func (s *Service) Continue(ctx context.Context, p *domain.Principal, runID string) (*Run, error) {
	prior, err := s.load(ctx, p, runID)
	if err != nil {
		return nil, err
	}
	if !prior.State.Terminal() {
		// Still working, or still awaiting a decision. Continuing that would
		// mean two runs on one board, and there is nothing settled to continue
		// FROM until the first one stops.
		return nil, domain.ErrConflict
	}
	if prior.Plan == nil || len(prior.Plan.Unmet) == 0 {
		return nil, ErrNothingLeft
	}
	return s.Create(ctx, p, CreateRequest{
		BoardID:        prior.Task.RootBoardID,
		Intent:         continuationIntent(prior),
		Scope:          prior.Task.Scope,
		SelectionID:    prior.Task.SelectionID,
		Autonomy:       prior.Task.Autonomy,
		AttachmentIDs:  prior.Task.AttachmentIDs,
		Viewport:       prior.Task.Viewport,
		ActiveLabelIDs: prior.Task.ActiveLabelIDs,
		ContinuesRunID: prior.ID,
		// The intent was composed by the server from the prior run's unmet
		// list, so nobody typed it and it must not be counted as though they had.
		Source: SourceContinue,
	})
}

// continuationIntent turns a finished run's leftovers into the next run's
// request, in the words the run itself used.
//
// The original request comes first because the unmet lines are fragments of it
// — "filling Editing, Sound" means nothing without "make a film production
// plan" above it — and the last line is the instruction that the whole item
// exists for: finish what is standing rather than build beside it.
func continuationIntent(prior *Run) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Continue the earlier request: %q. It was not finished. "+
		"Carry on with exactly what it left undone:\n", prior.Task.Intent)
	for _, u := range prior.Plan.Unmet {
		fmt.Fprintf(&b, "- %s\n", u.Request)
	}
	b.WriteString("Work with what is already on the board — fill and finish the containers " +
		"that are standing there; do not create a second set beside them.")
	// Create truncates to its own ceiling; the important half is at the top
	// either way, and a long unmet list is bounded at five entries upstream.
	return b.String()
}

// MaxPassesPerRun is how many times the model may plan for one run: the first
// pass plus every revision allowed. Each pass is granted a fresh Budget, so
// this is the multiplier between a pass's cost ceiling and a run's.
func MaxPassesPerRun() int { return maxRefinements + 1 }

// proposalLifetime is how long an unanswered plan holds the board's run slot.
//
// Deliberately longer than the delegation's own 30 minutes but not by much: a
// person who steps away from a review should find their plan when they come
// back from lunch, and a board that somebody previewed and abandoned last
// Tuesday must not still be locked. Applying past the delegation's expiry fails
// anyway, so the sweep is also what turns a silent failure into a legible one.
const proposalLifetime = 2 * time.Hour

// sharedProposalLifetime is the same window on a board with more than one
// member.
//
// The expiry sweep fixed the permanent deadlock and left the two-hour one, and
// made it cross-user: Sara previews a plan, closes her laptop, and Omar cannot
// use the assistant on his own board until after lunch. On a shared board the
// cost of an unanswered plan is borne by people who never saw it, so the window
// shrinks to roughly the length of a meeting.
const sharedProposalLifetime = 20 * time.Minute

// proposalLifetimeFor picks the window by how many people the wait costs.
func (s *Service) proposalLifetimeFor(ctx context.Context, run *Run) time.Duration {
	if run == nil {
		return proposalLifetime
	}
	_, acl, err := s.access.BoardChain(ctx, run.Task.RootBoardID)
	if err != nil || acl == nil {
		return proposalLifetime
	}
	if len(acl.Editors) > 0 || acl.PublicEditLink != "" {
		return sharedProposalLifetime
	}
	return proposalLifetime
}

// AuditEntry is one change the agent made on a board, in the terms a person
// would ask about it.
type AuditEntry struct {
	RunID    string    `json:"runId"`
	Intent   string    `json:"intent"`
	At       time.Time `json:"at"`
	Ops      int       `json:"ops"`
	Reverted bool      `json:"reverted"`
	CostUSD  float64   `json:"costUsd"`
	State    RunState  `json:"state"`
	// Actor is who the run acted for, by display name — never by subject id.
	// The audit view is board-scoped now, so "the assistant changed 24 things
	// here" has to be able to say whose assistant.
	Actor string `json:"actor,omitempty"`
	// Mine says the reader is that person, so a client knows whether the intent
	// and cost above are populated or deliberately blank.
	Mine bool `json:"mine"`
}

// Audit answers "what has the AI changed here", which every transaction has
// recorded since the agent shipped and nothing has ever surfaced. Trust in an
// agent is mostly the ability to check up on it afterwards.
func (s *Service) Audit(ctx context.Context, p *domain.Principal, boardID string, limit int) ([]AuditEntry, error) {
	role, _, err := s.access.RequireView(ctx, boardID, p)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	// The board's history, not the caller's. "What has the AI changed here" used
	// to show an owner only their OWN runs on their OWN board, which is the one
	// question the view exists to answer and the one answer it could not give.
	runs, err := s.boardRuns(ctx, p, boardID, role, limit)
	if err != nil {
		return nil, err
	}
	out := make([]AuditEntry, 0, len(runs))
	for _, r := range runs {
		// Only runs that actually wrote something. A discarded preview changed
		// nothing, and listing it as a change is exactly the noise that makes
		// an audit log stop being read.
		if r.State != StateCompleted && r.State != StateReverted && r.State != StatePartial {
			continue
		}
		ops := 0
		if r.Plan != nil {
			ops = len(r.Plan.Actions)
		}
		if ops == 0 {
			continue
		}
		at := r.UpdatedAt
		if r.CompletedAt != nil {
			at = *r.CompletedAt
		}
		// Somebody else's run contributes the FACT and never the words. The
		// intent is the requester's own prose and the cost is their money;
		// neither becomes the board's business by being spent on it.
		entry := AuditEntry{
			RunID: r.ID, At: at, Ops: ops, Actor: s.displayName(ctx, r.Tenant),
			Mine:     r.Tenant == p.Sub,
			Reverted: r.State == StateReverted, State: r.State,
		}
		if entry.Mine {
			entry.Intent = r.Task.Intent
			entry.CostUSD = r.Usage.CostUSD
		}
		out = append(out, entry)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// State machine & journal
// ---------------------------------------------------------------------------

// transition moves a run to a new state, refusing edges the machine does not
// define. Only this function changes Run.State.
func (s *Service) transition(ctx context.Context, run *Run, to RunState, reason string) error {
	if !CanTransition(run.State, to) {
		return fmt.Errorf("agent: illegal transition %s → %s", run.State, to)
	}
	from := run.State
	run.State = to
	run.Active = !to.Terminal()
	run.Reason = reason
	run.UpdatedAt = time.Now().UTC()
	// Per-state stamps, written here because this is the only place State
	// changes. Every terminal state used to write the same CompletedAt, and
	// COMPLETED→REVERTED is a legal edge, so a reverted run's apply time was
	// overwritten by its revert time — both ends of the interval stored in one
	// field, the second write erasing the first. Nothing downstream could ask
	// how long a change stood before it was taken back.
	if run.StateAt == nil {
		run.StateAt = map[RunState]time.Time{}
	}
	_, stamped := run.StateAt[to]
	if !stamped {
		run.StateAt[to] = run.UpdatedAt
	}
	if to.Terminal() && run.CompletedAt == nil {
		now := run.UpdatedAt
		run.CompletedAt = &now
	}
	// A proposal holds the board's only run slot until a person answers it.
	// Stamping the deadline here — the single place State changes — is what
	// keeps it impossible for a state to carry the wrong one.
	if to == StateProposed {
		deadline := run.UpdatedAt.Add(s.proposalLifetimeFor(ctx, run))
		run.ProposalExpiresAt = &deadline
		// Authority and offer expire together, by construction. They used to be
		// stamped in two places with two constants — 30 minutes for the grant, 2
		// hours for the offer — so for ninety minutes the product showed an Apply
		// button that could not possibly work. Stamping both here, the single
		// place State changes, is what makes them unable to diverge; Refine
		// inherits it with no extra code because it comes back through PROPOSED.
		run.Delegation.ExpiresAt = deadline
	} else {
		run.ProposalExpiresAt = nil
	}
	rev := run.Rev
	if err := s.runs.Update(ctx, run, rev); err != nil {
		run.State = from
		run.Active = !from.Terminal()
		if !stamped {
			delete(run.StateAt, to)
		}
		s.log.Warn("state write rejected", zap.String("run", run.ID), zap.Error(err))
		return err
	}
	s.emit(ctx, run, EvRunState, string(to), map[string]any{"from": string(from), "to": string(to), "reason": reason})
	if to.Terminal() || to == StateProposed {
		// A proposal is not working on anything either: the plan is waiting for
		// a person, and a badge claiming a card during that wait is a lie about
		// what is happening to it.
		s.releasePresence(run)
	}
	// The one place State changes is the one place an outcome can be announced
	// without a producer per exit path. A run that finished after the tab closed
	// used to land nowhere a person looks.
	s.notifyRunOutcome(ctx, run, to)
	return nil
}

// finishWithReason drives a run to a terminal state, tolerating an illegal edge
// by forcing it: a run must always reach a terminal state, and refusing the
// transition would leave it stuck holding the board's run slot forever.
func (s *Service) finishWithReason(ctx context.Context, run *Run, to RunState, reason string) {
	if run.State.Terminal() {
		return
	}
	if err := s.transition(ctx, run, to, reason); err != nil {
		run.State = to
		run.Active = false
		run.Reason = reason
		run.UpdatedAt = time.Now().UTC()
		now := run.UpdatedAt
		if run.CompletedAt == nil {
			run.CompletedAt = &now
		}
		// A forced edge is still an edge: the metrics must not have a hole
		// exactly where the state machine had to be overruled.
		if run.StateAt == nil {
			run.StateAt = map[RunState]time.Time{}
		}
		if _, seen := run.StateAt[to]; !seen {
			run.StateAt[to] = now
		}
		if uerr := s.runs.Update(ctx, run, run.Rev); uerr != nil {
			s.log.Error("could not terminate run", zap.String("run", run.ID), zap.Error(uerr))
			return
		}
		s.emit(ctx, run, EvRunState, string(to), map[string]any{"to": string(to), "reason": reason, "forced": true})
		s.releasePresence(run)
		// A forced edge is still an outcome. Notifying only on the clean path
		// would go silent on exactly the runs worth telling somebody about.
		s.notifyRunOutcome(ctx, run, to)
	}
}

// CommentAnnouncer is the half of the comment service that publishes a message
// somebody has already been authorized to write.
//
// An interface rather than the concrete service so a deployment without one
// keeps the old behaviour — a silent comment — instead of failing to start, and
// so the agent package does not have to know how notifications are composed.
type CommentAnnouncer interface {
	AnnounceAgentComment(ctx context.Context, p *domain.Principal, c *domain.Comment, mentions []string)
}

// announceComments publishes each comment this run wrote.
func (s *Service) announceComments(ctx context.Context, p *domain.Principal, written []*domain.Comment) {
	if s.commentAnnouncer == nil || len(written) == 0 {
		return
	}
	for _, c := range written {
		// The mentions the comment tool resolved. Passing nil here was the last
		// inert half of MP9: the tool took the argument, the announcer took the
		// slice, and the run in between dropped it — so "flag this to Sara"
		// wrote a comment Sara was never told about.
		s.commentAnnouncer.AnnounceAgentComment(ctx, p, c, c.Mentions)
	}
}

// reclaimProposal lets a board's OWNER clear somebody else's abandoned plan.
//
// The whole point is what it does not do: it never reads run.Plan, never
// returns one, and never records adjustments — a reclaim is not a rejection of
// the plan's content, because the reclaimer has not seen the content. It is the
// board taking its slot back. The author is told, because a plan vanishing with
// no explanation is worse than the wait it replaced.
func (s *Service) reclaimProposal(ctx context.Context, p *domain.Principal, run *Run) (*Run, error) {
	if run.State != StateProposed {
		// Not a proposal, or already answered. A stranger must not be able to
		// tell those apart from a run id.
		return nil, domain.ErrNotFound
	}
	_, acl, err := s.access.BoardChain(ctx, run.Task.RootBoardID)
	if err != nil || acl == nil || acl.OwnerID != p.Sub {
		return nil, domain.ErrNotFound
	}
	s.finishWithReason(ctx, run, StateDiscarded,
		"the owner of this board cleared this plan so the assistant could be used again")
	if s.notifier != nil && run.Tenant != "" {
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: run.Tenant, Kind: domain.NotifyAgentRun, ActorID: p.Sub,
			BoardID: run.Task.RootBoardID, AgentRunID: run.ID, Origin: domain.OriginAgent,
			Message:   sanitizeName(p.Name) + " cleared your unanswered plan so the board's assistant was free again",
			CreatedAt: time.Now().UTC(),
		})
	}
	// The public half only. The reclaimer never sees what they cancelled.
	return s.redactFor(run, p.Sub), nil
}

// notifyRunOutcome is the producer NotifyBoardChange never had.
//
// One notification per RUN, never per op, on the states that either wrote
// something or need a decision. Everything below it is etiquette, and the
// etiquette is the feature: a bell for every run on every board is an
// unsubscribe, not a signal.
//
//   - Never the person who is already holding the answer. The requester of a
//     PREVIEW run is sitting in front of the plan; they need no bell.
//   - Never somebody with a live connection to that board — they are watching
//     the change happen.
//   - Coalesced per (recipient, board) within half an hour, so a person
//     working through six quick runs gets one bell, not six.
//   - Collaborators on a shared board are told, because an assistant run is
//     the largest single change anyone makes there all week and it used to
//     produce exactly one signal, visible only to whoever was already looking.
func (s *Service) notifyRunOutcome(ctx context.Context, run *Run, to RunState) {
	if s.notifier == nil || run == nil {
		return
	}
	wrote := len(run.TransactionIDs) > 0
	unattended := run.Task.Autonomy == AutonomyAuto
	msg := ""
	switch to {
	case StateCompleted, StatePartial:
		if !wrote {
			return // an answer that changed nothing is not news
		}
		msg = "made %d change(s) to \"%s\""
	case StateProposed:
		// Only under auto: the person who asked for a preview is looking at it.
		// A quarantined unattended run is the opposite — nobody expects to be
		// asked, and the plan sits there until somebody is told.
		if !unattended {
			return
		}
		msg = "needs a decision on \"%s\""
	case StateFailed:
		if !wrote {
			return
		}
		msg = "stopped partway on \"%s\" and left changes standing"
	default:
		return
	}

	board := s.boardTitle(ctx, run.Task.RootBoardID)
	ops := 0
	if run.Plan != nil {
		ops = len(run.Plan.Actions)
	}
	elementID := ""
	if run.Plan != nil && len(run.Plan.Actions) > 0 {
		// Deep-link to the work rather than to the board, so the person lands on
		// what changed instead of hunting for it.
		elementID = run.Plan.Actions[0].ElementID
	}

	for _, recipient := range s.runAudience(ctx, run) {
		// The person who approved it already knows; so does anyone watching.
		if recipient == run.Tenant && !unattended && to != StateFailed {
			continue
		}
		if watcher, ok := s.bus.(BoardPresence); ok && watcher.Watching(run.Task.RootBoardID, recipient) {
			continue
		}
		if !s.claimNotifySlot(recipient, run.Task.RootBoardID) {
			continue
		}
		text := ""
		if recipient == run.Tenant {
			text = "Your assistant " + fmt.Sprintf(msg, argsFor(msg, ops, board)...)
		} else {
			text = s.displayName(ctx, run.Tenant) + "'s assistant " +
				fmt.Sprintf(msg, argsFor(msg, ops, board)...)
		}
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: recipient, Kind: domain.NotifyAgentRun,
			ActorID: run.Tenant, BoardID: run.Task.RootBoardID, ElementID: elementID,
			Origin: domain.OriginAgent, AgentRunID: run.ID,
			Message: text, CreatedAt: time.Now().UTC(),
		})
	}
}

// argsFor supplies exactly the verbs each template takes. Two shapes, so a
// count is only formatted into the one message that has a place for it.
func argsFor(tmpl string, ops int, board string) []any {
	if strings.Contains(tmpl, "%d") {
		return []any{ops, board}
	}
	return []any{board}
}

// runAudience is who has a stake in this run: the person who ran it, plus the
// board's owner and editors. Deduplicated, and never a link holder — a bearer
// token is not a stakeholder.
func (s *Service) runAudience(ctx context.Context, run *Run) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(sub string) {
		if sub == "" || seen[sub] {
			return
		}
		seen[sub] = true
		out = append(out, sub)
	}
	add(run.Tenant)
	if _, acl, err := s.access.BoardChain(ctx, run.Task.RootBoardID); err == nil && acl != nil {
		add(acl.OwnerID)
		for _, e := range acl.Editors {
			add(e)
		}
	}
	return out
}

// claimNotifySlot returns true at most once per recipient-board per window.
func (s *Service) claimNotifySlot(sub, boardID string) bool {
	s.notifyMu.Lock()
	defer s.notifyMu.Unlock()
	key := sub + "\x00" + boardID
	now := time.Now()
	if last, ok := s.lastNotified[key]; ok && now.Sub(last) < runNotifyWindow {
		return false
	}
	s.lastNotified[key] = now
	return true
}

func (s *Service) fail(ctx context.Context, runID, reason string) {
	run, err := s.runs.Get(ctx, runID)
	if err != nil {
		return
	}
	s.finishWithReason(ctx, run, StateFailed, reason)
}

// roomSafeEventKeys is the ALLOWLIST of journal fields a board room may see.
//
// The list is short on purpose: a run's message and data carry the person's own
// words — the intent at admission, the note on a refinement, the steer typed
// mid-run, the sentences the plan drafted — and none of that belongs to anybody
// but the person who wrote it. What a watcher needs is the fact of the run and
// where it has got to, which is exactly what describeActive already decided when
// it "redacts a colleague's run down to the fact of it".
//
// Allowlist rather than denylist because the next field added to the payload
// must be private until somebody argues otherwise, not public until somebody
// remembers. That is the same lesson the ACL's json tags taught one layer down.
var roomSafeEventKeys = []string{"runId", "sequence", "state", "type", "at"}

// roomSafeEvent projects a full journal frame down to the allowlisted keys.
func roomSafeEvent(full map[string]any) map[string]any {
	out := make(map[string]any, len(roomSafeEventKeys))
	for _, k := range roomSafeEventKeys {
		if v, ok := full[k]; ok {
			out[k] = v
		}
	}
	return out
}

// emit appends to the journal and pushes the event out on two channels: the
// whole frame to the person the run belongs to, and the allowlisted projection
// to everyone else on the board.
//
// It used to be one board-wide broadcast of everything. Room membership needs
// only RoleView, which a password-less view link satisfies with no account at
// all — so a read-only link handed to a client or a contractor streamed the
// owner's prompts, revisions and mid-run corrections verbatim, live. The product
// stated the opposite policy one file away (describeActive: "the request text is
// the person's own words and is not shared") and enforced it only by the
// frontend choosing not to render fields it already held.
//
// The owner is in the room too, so they receive both frames; the client dedupes
// on sequence and the full one is sent first, so what they see is unchanged.
func (s *Service) emit(ctx context.Context, run *Run, t EventType, msg string, data map[string]any) {
	ev := &Event{
		ID:    s.newID(),
		RunID: run.ID,
		Type:  t,
		// Both are free here and were being thrown away. Without them the
		// journal can only be read one run at a time, so every aggregate over
		// it — refusal rate, revert rate, cost per board — was a full
		// collection scan the port could not even express.
		Tenant:      run.Tenant,
		RootBoardID: run.Task.RootBoardID,
		Message:     msg,
		Data:        data,
		At:          time.Now().UTC(),
	}
	if err := s.events.Append(ctx, ev); err != nil {
		// Escalated, not warned. An event the journal could not record is a hole
		// in the only record of what the agent did on someone's board, and it
		// appears under load — precisely when the behaviour is most worth
		// having. A Warn made those holes indistinguishable from a quiet run.
		s.log.Error("journal append failed — the run's audit trail has a hole",
			zap.String("run", run.ID), zap.String("type", string(t)), zap.Error(err))
	}
	if s.bus == nil {
		return
	}
	full := map[string]any{
		"runId":    run.ID,
		"sequence": ev.Sequence,
		"state":    run.State,
		"type":     t,
		"message":  msg,
		"data":     data,
		"at":       ev.At,
	}
	s.bus.NotifyUser(run.Tenant, "agent.event", full)
	s.bus.BroadcastEvent(run.Task.RootBoardID, "agent.event", roomSafeEvent(full))
	// Move the badge onto whatever this step names.
	//
	// Hooked here rather than in the planner because this is the one function
	// every step event already passes through, and because the element id is
	// already in the payload — the alternative is a callback per tool handler,
	// which is how a claim ends up missing from the three handlers nobody
	// remembers to update.
	if run.State.Active() && run.State != StateProposed {
		if id, ok := data["elementId"].(string); ok && id != "" {
			s.claimPresence(run, "", id)
		}
	}
}

// terminalOutcome names the ending, using the run clock as well as the error.
//
// A single hung provider call can eat a whole run: the HTTP client's timeout is
// three minutes and the resilient wrapper retries three times per provider, so
// nine minutes of one outage lands against an eight-minute run deadline. Which
// of the two fires first then decided whether the person was told "Ran out of
// room" — a budget they never approached, in warning tone, with an invitation to
// raise a limit — or "the AI service is unavailable". Same event, two answers,
// both arrived at by accident.
//
// So the deadline is only a budget when nothing else explains it. If the run
// died holding a provider error, the provider is the story.
func terminalOutcome(err error, deadlineFired bool) (RunState, string) {
	switch {
	case errors.Is(err, ErrNothingToDo), errors.Is(err, cognition.ErrRefused):
		return terminalFor(err), reasonFor(err)
	case errors.Is(err, cognition.ErrUnavailable):
		if deadlineFired {
			return StateFailed, "the AI service stopped responding"
		}
		return StateFailed, reasonFor(err)
	case deadlineFired, errors.Is(err, context.DeadlineExceeded):
		return StateExhausted, "the run ran out of time"
	}
	return terminalFor(err), reasonFor(err)
}

// terminalFor maps a workload error onto the terminal state that describes it.
// A model that found nothing is not a failure of the system.
func terminalFor(err error) RunState {
	switch {
	case errors.Is(err, ErrNothingToDo), errors.Is(err, cognition.ErrRefused):
		return StatePartial
	case errors.Is(err, context.DeadlineExceeded):
		return StateExhausted
	default:
		return StateFailed
	}
}

func reasonFor(err error) string {
	switch {
	case errors.Is(err, ErrNothingToDo):
		return "nothing needed changing"
	case errors.Is(err, cognition.ErrRefused):
		return "the model declined this request"
	case errors.Is(err, cognition.ErrUnavailable):
		return "the AI service is unavailable right now"
	case errors.Is(err, context.DeadlineExceeded):
		return "the run ran out of time"
	default:
		return "the run could not be completed"
	}
}

// AuthoredMessage marks this error's text as written for the person who will
// read it, so the HTTP boundary can put it on the wire instead of the sentinel's
// generic word.
//
// Every refusal in this package is a sentence somebody composed — "Sara started
// one 3 minutes ago", "the board changed while you were reviewing" — and all of
// them wrapping ErrConflict or ErrForbidden arrived at the browser as the single
// words "conflict" and "forbidden", because the transport switch tested the
// sentinel and answered with a constant. The marker is on the TYPE rather than
// on each sentinel so the next refusal written here is legible without anyone
// remembering to add it to a list, and a driver error that happens to wrap the
// same sentinel still gets the generic word.
func (e *agentError) AuthoredMessage() string { return e.msg }

// attachAncestry walks up from this board to the workspace root, and lists the
// boards sitting alongside it.
//
// Bounded and read-only. It changes what the agent KNOWS, never what it may
// touch: containment is still the root board's subtree.
//
// Every hop is now authorised against the run's principal, because sharing
// cascades DOWNWARD only: Resolve takes the max role over an element's own
// ancestor chain, so a contractor granted edit on one nested board holds
// RoleNone on its parent. This walk ignored that entirely and read straight off
// the repository, so the contractor's first run told them the parent
// workspace's name and every sibling project's — "Acme Q3 Layoffs", "Series B".
// A read-ACL bypass of exactly the class already closed for CLONE,
// reintroduced by a context-enrichment feature.
//
// The invariant is the one authorizeDestinations and the attachment filter
// already hold: the agent's context is a subset of what its principal could
// read by hand.
func (s *Service) attachAncestry(ctx context.Context, p *domain.Principal, scope *BoardScope) {
	if scope == nil || scope.Board == nil || p == nil {
		return
	}
	var path []string
	seen := map[string]bool{scope.Board.ID: true}
	cur := scope.Board
	parentID := ""
	for depth := 0; depth < maxAncestryDepth; depth++ {
		pid := cur.Location.ParentID
		if pid == "" || seen[pid] {
			break
		}
		seen[pid] = true
		parent, err := s.elements.Get(ctx, pid)
		if err != nil || parent.Type != domain.TypeBoard {
			break
		}
		if !s.canRead(ctx, p, parent.ID) {
			break // the workspace above the share boundary is not this run's to know
		}
		if title := parent.Title(); title != "" {
			path = append([]string{sanitizeName(title)}, path...)
		}
		if parentID == "" {
			parentID = parent.ID
		}
		cur = parent
	}
	if title := scope.Board.Title(); title != "" {
		path = append(path, sanitizeName(title))
	}
	if len(path) > 1 {
		scope.Ancestry = path
	}

	// Boards alongside this one, so "put this with the others" has a referent.
	// Only the ones this person could open themselves: a sibling's TITLE is the
	// disclosure, and the title is the whole of what gets rendered.
	if parentID == "" {
		return
	}
	kids, err := s.elements.Children(ctx, domain.ElementFilter{ParentID: parentID})
	if err != nil {
		return
	}
	for _, el := range kids {
		if el.Type != domain.TypeBoard || el.ID == scope.Board.ID || el.IsDeleted() {
			continue
		}
		if !s.canRead(ctx, p, el.ID) {
			continue
		}
		if title := el.Title(); title != "" {
			scope.Siblings = append(scope.Siblings, sanitizeName(title))
		}
		if len(scope.Siblings) == maxSiblingsShown {
			break
		}
	}
}

// canRead answers whether the run's principal could open this board by hand.
func (s *Service) canRead(ctx context.Context, p *domain.Principal, boardID string) bool {
	if s.access == nil {
		return false
	}
	role, _, err := s.access.Resolve(ctx, boardID, p)
	return err == nil && role.CanView()
}

// Bounds. Deep enough to describe a real project, shallow enough that the
// context stays about this board.
const (
	maxAncestryDepth = 5
	maxSiblingsShown = 8
)

// attachAccountRules adds the person's own standing notes, which apply to every
// board they own. The board's rules stay separate and take precedence.
func (s *Service) attachAccountRules(ctx context.Context, scope *BoardScope, tenant string) {
	if scope == nil || s.users == nil {
		return
	}
	u, err := s.users.GetBySub(ctx, tenant)
	if err != nil || u == nil {
		return
	}
	scope.AccountInstructions = truncate(sanitizeBody(u.Settings.Agent.Instructions), 400)
	// The person's clock travels with their rules, because both are facts about
	// them rather than about the board. Without it every time the model writes
	// and reads is UTC, and "remind me at 05:30 on the shoot morning" fired four
	// hours after the call time it existed to precede.
	scope.Timezone = u.Settings.Localization.TimeZone
}

// attachHistory gives the run the last few requests made on this board, and the
// most recent ones' own account of how they went.
//
// Intents and outcomes, plus summary and unmet — not plans. A plan replayed in
// full would be a much larger prompt and would invite the model to re-litigate
// decisions rather than build on them. The summary and the unmet list are the
// two sentences a plan would have been read FOR: what the run did, and what it
// left for whoever came next.
func (s *Service) attachHistory(ctx context.Context, scope *BoardScope, run *Run) {
	if scope == nil || run == nil {
		return
	}
	prior, err := s.boardHistory(ctx, run, 8)
	if err != nil {
		return // history is a nicety; failing the run over it would not be
	}
	for _, r := range prior {
		if r.ID == run.ID || r.Task.Intent == "" {
			continue
		}
		// Memory follows the BOARD; confidentiality follows the person.
		//
		// A colleague's run is a fact about this canvas — it happened, it did
		// this, it left that undone — and all three are safe to state on a board
		// both people can read. Their raw request is not: it is the most
		// sensitive text the system holds, because prompts routinely carry
		// reasoning nobody would write on a card. So the outcome, the summary
		// and the unmet list travel and the intent does not, with a placeholder
		// in its place rather than an empty line — the model has to be able to
		// tell "somebody asked something I cannot see" from "nobody asked".
		mine := r.Tenant == run.Tenant
		shownIntent := truncate(sanitizeBody(r.Task.Intent), 120)
		if !mine {
			shownIntent = s.displayName(ctx, r.Tenant) + " asked for something (their words are not shared)"
		}
		outcome := ""
		switch r.State {
		case StateCompleted:
			outcome = "applied"
		case StateReverted:
			// The most informative entry of all: it says what NOT to do again.
			outcome = "applied, then undone by the user"
		case StateDiscarded:
			outcome = "the user rejected the plan"
		case StatePartial:
			// An answer with nothing to apply. It was dropped as uninteresting,
			// and it is the opposite: "there is nothing on this board to work
			// from" is precisely the thing a follow-up needs to have been told,
			// and a one-word follow-up is usually asking about it.
			outcome = "answered without changing anything"
		default:
			// History follows EFFECTS, not verdicts.
			//
			// "It failed for reasons that teach nothing" is true of a validation
			// refusal and false of a half-commit. A run that wrote sixteen
			// elements and then failed — or one force-failed mid-write — is
			// exactly the run whose successor has to be told what is already
			// standing, and it was precisely the one this switch dropped. So the
			// person's own recovery button started context-free and built the
			// structure a second time beside the partial one: the duplicate-
			// structure failure, arriving through the failure door, onto a board
			// already in a worse state.
			if r.State.Active() || len(r.TransactionIDs) == 0 {
				continue
			}
			outcome = "stopped partway and left changes standing"
			if r.Plan != nil && len(r.Plan.Actions) > 0 {
				outcome = fmt.Sprintf("stopped partway and left %d change(s) standing", len(r.Plan.Actions))
			}
		}
		// The proposal is preferred over the effective plan because what the
		// person took back is a fact about what they were SHOWN. Commit rewrites
		// Plan with the corrected list, so reading Plan here would describe the
		// rejection using the very rows the rejection removed.
		shown := r.ProposedPlan
		if shown == nil {
			shown = r.Plan
		}
		if !mine {
			// Whose run it was, folded into the outcome rather than added as a
			// field: the digest renders this line unchanged, so attribution
			// arrives without a renderer that has to learn a new shape.
			outcome = s.displayName(ctx, r.Tenant) + "'s run — " + outcome
		}
		scope.History = append(scope.History, PriorRun{
			Intent:  shownIntent,
			Outcome: outcome,
			When:    humanAge(r.CreatedAt),
			Summary: priorSummary(r),
			Unmet:   priorUnmet(r),
			// "applied, then undone by the user" says a correction happened and
			// not one thing about what it was. These two fields are what the
			// digest needs to name the individual changes — and to withhold a
			// quarantined run's own words, which the digest already refuses to
			// repeat but only when the flag reaches it.
			Rejected:    RejectedShape(shown, r.RevertedElementIDs, r.State == StateReverted),
			Quarantined: r.Plan != nil && r.Plan.Quarantined,
		})
		if len(scope.History) == maxHistoryShown {
			break
		}
	}

	// Compiles this board's correction history into typed, validated predicates
	// and hangs them on the scope. Without this call the entire learned-rule
	// layer — the refusal in staging.add and the Preconditions backstop — is
	// unreachable code: the rules are never compiled, so nothing ever matches.
	AttachLearnedRules(scope, prior)
}

// boardHistory reads what happened on this board, whoever made it happen.
//
// Tenant-scoped memory is the multiplayer hole underneath the fix the previous
// wave called closed: the PREVIOUS RUN block was EMPTY the moment the previous
// run was somebody else's. So the exact failure that block exists to prevent —
// a second run building a duplicate structure beside the first — was still
// live, and MORE likely in a team, because two people organising the same board
// an hour apart is the ordinary case rather than the exception. The board also
// lost "applied, then undone by the user", the single most informative entry
// there is, across users.
//
// Falls back to the tenant-scoped read on an adapter without the board-owner
// key: narrower than intended, and honest about it.
func (s *Service) boardHistory(ctx context.Context, run *Run, limit int) ([]*Run, error) {
	mine, err := s.runs.ListByBoard(ctx, run.Tenant, run.Task.RootBoardID, limit)
	if err != nil {
		return nil, err
	}
	owner, ok := s.runs.(RunBoardOwnerStore)
	if !ok || run.BoardOwnerSub == "" {
		return mine, nil
	}
	theirs, err := owner.ListByBoardOwner(ctx, run.BoardOwnerSub, run.Task.RootBoardID, limit)
	if err != nil {
		return mine, nil
	}
	seen := map[string]bool{}
	out := make([]*Run, 0, len(mine)+len(theirs))
	for _, r := range append(append([]*Run{}, mine...), theirs...) {
		if seen[r.ID] {
			continue
		}
		seen[r.ID] = true
		out = append(out, r)
	}
	sortRunsNewestFirst(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// priorSummary is what an earlier run said about itself. Nil-checked because a
// run can reach a terminal state without ever producing a plan — a provider
// failure on the first turn is the ordinary case.
func priorSummary(r *Run) string {
	if r.Plan == nil {
		return ""
	}
	return truncate(sanitizeBody(r.Plan.Summary), maxPriorSummary)
}

// priorUnmet flattens an earlier run's unmet list into one line per entry,
// reason folded in. The reason is half the value — "filling Editing, Sound"
// alone reads as a choice, and "the run was stopped at step 14 of 14" is what
// makes it a to-do.
func priorUnmet(r *Run) []string {
	if r.Plan == nil {
		return nil
	}
	out := make([]string, 0, len(r.Plan.Unmet))
	for _, u := range r.Plan.Unmet {
		line := sanitizeBody(u.Request)
		if u.Why != "" {
			line += " — " + sanitizeBody(u.Why)
		}
		out = append(out, truncate(line, maxPriorUnmetLine))
		if len(out) == maxPriorUnmetShown {
			break
		}
	}
	return out
}

// maxHistoryShown bounds the recall. Three is enough to establish a convention
// and small enough that it cannot crowd out the board itself. The rest bound
// what one remembered run may spend of the ~600-token memory block.
const (
	maxHistoryShown    = 3
	maxPriorSummary    = 240
	maxPriorUnmetLine  = 140
	maxPriorUnmetShown = 4
)

// attachPeople lists who can be assigned work on this board: its owner and its
// editors. Resolved to names so the model reasons about "Sara" rather than a
// subject id, and scoped to the ACL so it cannot assign work to a stranger.
//
// DA7. It read `scope.Board.ACL` and nothing else, while AccessResolver.Resolve
// walks the whole containment chain and takes the MAX role across every
// ancestor — so sharing cascades downward and this list did not. `applyCreate`
// stamps every new BOARD with `ACL{OwnerID: creator, Editors: []}`, which makes
// the broken case the NORMAL one: a sub-board the agent itself created after one
// organizing run, on which `scope.People` then contained exactly the creator.
//
// Two consequences, and the second is why this is not merely a missing feature.
// `assign` rejected every real teammate with "X is not one of this board's
// people" — and `rejectID` counts an unrecognised id toward the INJECTION
// TALLY, so asking the agent to give a colleague a task looked like an attempted
// prompt injection and moved a legitimate run towards quarantine.
//
// So it walks upward exactly as Resolve does. The chain is at most four deep and
// its ids were already resolved at admission, so this costs the ancestor reads
// and nothing else.
func (s *Service) attachPeople(ctx context.Context, p *domain.Principal, scope *BoardScope) {
	if s.users == nil || scope == nil || scope.Board == nil {
		return
	}
	var subs []string
	if acl := scope.Board.ACL; acl != nil {
		subs = append(append(subs, acl.OwnerID), acl.Editors...)
	}
	subs = append(subs, s.inheritedEditors(ctx, p, scope)...)
	seen := map[string]bool{}
	for _, sub := range subs {
		if sub == "" || seen[sub] {
			continue
		}
		seen[sub] = true
		name := sub
		if u, err := s.users.GetBySub(ctx, sub); err == nil && u != nil && u.DisplayName != "" {
			name = u.DisplayName
		}
		// Numbered in ACL order, so the owner is always person1 and the handles
		// are stable across the turns of one run. Assigned here because this is
		// the only place People is built — an alias minted anywhere else could
		// collide with one already published in the digest.
		scope.People = append(scope.People, PersonRef{
			ID: sub, Name: sanitizeName(name),
			Alias: fmt.Sprintf("person%d", len(scope.People)+1),
		})
	}
}

// inheritedEditors is everyone the boards ABOVE this one admit — the same union
// AccessResolver takes when it decides whether somebody may act here.
//
// Nearest ancestor first, so the people closest to the work are numbered first
// and the owner of the board actually being organised stays person1.
//
// Bounded by maxAncestryDepth and stopped by an unreadable parent, for the
// reason attachAncestry stops there: the workspace above a share boundary is not
// this run's to know, and its collaborators are not this run's to name.
func (s *Service) inheritedEditors(ctx context.Context, p *domain.Principal, scope *BoardScope) []string {
	var out []string
	seen := map[string]bool{scope.Board.ID: true}
	cur := scope.Board
	for depth := 0; depth < maxAncestryDepth; depth++ {
		pid := cur.Location.ParentID
		if pid == "" || seen[pid] {
			break
		}
		seen[pid] = true
		parent, err := s.elements.Get(ctx, pid)
		if err != nil || parent == nil || parent.Type != domain.TypeBoard {
			break
		}
		if !s.canRead(ctx, p, parent.ID) {
			// The workspace above a share boundary is not this run's to know,
			// and its collaborators are not this run's to name — the same stop
			// attachAncestry makes, for the sharper reason: a name published
			// here becomes an assignable handle in the model'''s context.
			break
		}
		if acl := parent.ACL; acl != nil {
			out = append(append(out, acl.OwnerID), acl.Editors...)
		}
		cur = parent
	}
	return out
}

// attachLabels gives the scope the owner's label vocabulary, so the model can
// reuse a tag instead of coining a near-duplicate. Failure is not fatal: an
// agent that cannot read labels simply does not offer to apply them, which is a
// smaller loss than failing the run.
func (s *Service) attachLabels(ctx context.Context, scope *BoardScope, owner string) {
	if s.labels == nil || scope == nil {
		return
	}
	// A label is PRIVATE to whoever coined it, and the write path enforces that
	// on every human attach — the comment/label service says so in as many
	// words: "stamping somebody else's id onto an element both leaks that a
	// label exists and inflates their usage count".
	//
	// The agent path walked into the mirror image of that hazard. B's run tagged
	// A's cards with B's private vocabulary, so A saw labelIds on their own
	// elements that resolved to nothing in their label list — invisible tags
	// affecting nothing they can see and removable from no filter they have. A
	// human doing this by hand does one card; the agent does a board in one
	// transaction.
	//
	// So the vocabulary is offered only when the runner owns the board. Offering
	// the OWNER's vocabulary instead is the shape the fix wants, and it is not
	// reachable from here: labelsForCreate and the attach path both refuse any
	// label whose OwnerID is not the caller's, so every such plan would compile
	// to a guaranteed 403. That needs board-scoped labels, which is a schema
	// change and a separate item.
	if scope.Board != nil && scope.Board.ACL != nil && scope.Board.ACL.OwnerID != owner {
		// Recorded rather than returned silently. An absent vocabulary and a
		// withheld one are the same empty list to a model, and it acts on both
		// the same wrong way; the digest says which this is.
		scope.LabelsWithheld = true
		return
	}
	owned, err := s.labels.ListByOwner(ctx, owner)
	if err != nil {
		s.log.Warn("agent: could not read labels", zap.Error(err))
		return
	}
	if s.users != nil {
		if u, uerr := s.users.GetBySub(ctx, owner); uerr == nil && u != nil && u.DisplayName != "" {
			scope.LabelsOwner = sanitizeName(u.DisplayName)
		}
	}
	for _, l := range owned {
		// Usage is what turns the TAGS line from a bare list into reuse
		// guidance: the digest already sorts by it and renders "urgent (41)",
		// so the model can see which word this workspace actually uses instead
		// of coin-flipping between near-synonyms. Both halves shipped reading
		// this field and nothing ever set it, so every count rendered as absent
		// and the sort was a no-op over equal zeroes.
		scope.Labels = append(scope.Labels, LabelRef{ID: l.ID, Name: l.Name, Usage: l.UsageCount})
	}
}

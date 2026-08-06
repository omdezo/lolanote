package service

import (
	"context"
	"strconv"
	"time"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// BlobRemover deletes stored bytes for a key (the local driver implements
// it; nil when the storage backend manages lifecycle itself, e.g. an R2
// bucket lifecycle rule).
type BlobRemover interface {
	Remove(key string) error
}

// AttachmentGraph is the half of the element store that can answer what points
// at which uploaded file. Optional, discovered by assertion, because the
// question is only asked by the sweeper.
type AttachmentGraph interface {
	// ExpiringSoon lists the trashed elements a purge at this cutoff removes.
	ExpiringSoon(ctx context.Context, olderThan time.Time) ([]*domain.Element, error)
	// AttachmentRefs is the attachment ids those elements name.
	AttachmentRefs(ctx context.Context, elementIDs []string) ([]string, error)
	// AttachmentReferrers counts the elements still pointing at each attachment.
	AttachmentReferrers(ctx context.Context, attachmentIDs []string) (map[string]int64, error)
}

// TransactionPurger is the half of the journal that can forget. Optional for the
// same reason BlobRemover is: a deployment that has not wired it still runs, and
// the sweeper says what it is not doing rather than silently doing nothing.
type TransactionPurger interface {
	DeleteByUser(ctx context.Context, sub string) error
	DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error)
}

// JournalRetention is how long a transaction survives.
//
// Deliberately LONGER than TrashRetention: a restore from the trash must still
// be able to find its own inverse, so a journal that expired first would take
// the undo of every restorable delete with it. 180 days is the honest number.
const JournalRetention = 180 * 24 * time.Hour

// PurgeWarningLead is how much notice a person gets before their trash is
// destroyed — JN21's one-week clause.
//
// A week, not a day, because the thing at stake is usually a decision rather
// than a click: "do we still want the March cut" is a question somebody has to
// ask a colleague. It is deliberately the same number the trash panel turns
// amber at, so the row and the notification agree — two surfaces disagreeing
// about when something dies is worse than neither of them saying.
const PurgeWarningLead = 7 * 24 * time.Hour

// purgeWarnedKey stamps an element whose batch has already been warned about.
//
// The reminder sweeper solved the same problem the same way (`reminderSent`),
// and the alternative — a separate table of what we have told whom — is a
// second source of truth for a fact the element already carries.
const purgeWarnedKey = "purgeWarned"

// MaintenanceService is the scheduled housekeeping previously only reachable
// through the manual CLI (§3.4, GAPS 1.5): expired-trash purge (90-day
// retention) and garbage collection of abandoned presigned uploads.
type MaintenanceService struct {
	elements    domain.ElementRepository
	attachments domain.AttachmentRepository
	blobs       BlobRemover // optional
	comments    domain.CommentRepository
	journal     domain.TransactionRepository
	collector   *Collector
	notifier    *Notifier // optional: the week's notice before a purge (JN21)
	newID       IDGenerator
	log         *zap.Logger
}

// NewMaintenanceService constructs the sweeper.
//
// The collector is built here from what the constructor already has rather than
// waiting to be attached: blob collection was already this sweeper's job, and
// moving it behind an optional setter would have made a working capability
// depend on somebody remembering to wire it — which is precisely how the account
// purges shipped unreachable.
func NewMaintenanceService(elements domain.ElementRepository, attachments domain.AttachmentRepository, blobs BlobRemover, log *zap.Logger) *MaintenanceService {
	collector := NewCollector(elements, log)
	collector.AttachBlobs(attachments, blobs)
	return &MaintenanceService{
		elements: elements, attachments: attachments, blobs: blobs,
		collector: collector, log: log.Named("maintenance"),
	}
}

// AttachComments lets the sweeper take a purged thread's messages with it.
// Optional so existing wiring compiles unchanged.
func (s *MaintenanceService) AttachComments(c domain.CommentRepository) { s.comments = c }

// AttachJournal lets the sweeper enforce the journal's retention window and
// strip a purged element's content out of the rows that still describe it.
func (s *MaintenanceService) AttachJournal(t domain.TransactionRepository) {
	s.journal = t
	s.collector.AttachJournal(t)
}

// AttachNotifier lets the sweep warn before it destroys (JN21).
//
// Optional and set separately, like the other capabilities this sweeper grew
// after its constructor was written — a deployment that has not wired it still
// purges correctly, it simply purges silently, which is the behaviour that
// existed before this and is what the warning exists to end.
func (s *MaintenanceService) AttachNotifier(n *Notifier, newID IDGenerator) {
	s.notifier = n
	s.newID = newID
}

// SweepOnce purges expired trash and collects stale presigned attachments.
func (s *MaintenanceService) SweepOnce(ctx context.Context) {
	// Everything the purge is about to destroy the only record of, read first.
	// A trashed element names its blob in content.attachmentId and its messages
	// by being their thread; both links die with the row, so both have to be
	// taken before the delete rather than looked for afterwards.
	doomed := s.expiring(ctx)

	// Warn BEFORE purging, so a sweep can never both create the last week of
	// notice and consume it in the same pass.
	s.warnExpiring(ctx)

	if purged, err := s.elements.PurgeExpired(ctx, time.Now().Add(-domain.TrashRetention)); err != nil {
		s.log.Warn("trash purge failed", zap.Error(err))
	} else if purged > 0 {
		s.log.Info("expired trash purged", zap.Int64("count", purged))
	}

	// The same collector the explicit "Delete forever" gesture uses, so the two
	// paths that permanently destroy an element cannot drift: the sweeper used to
	// take the blobs and leave the journal's verbatim copy of the content behind.
	if s.collector != nil {
		s.collector.Collect(ctx, doomed.ids, doomed.attachments)
	}
	s.collectOrphanedComments(ctx, doomed.threads)
	s.trimJournal(ctx)

	// Presigned-but-never-completed uploads older than a day are abandoned.
	stale, err := s.attachments.StalePresigned(ctx, time.Now().Add(-24*time.Hour))
	if err != nil {
		s.log.Warn("stale attachment scan failed", zap.Error(err))
		return
	}
	removed := 0
	for _, a := range stale {
		if s.blobs != nil {
			if err := s.blobs.Remove(a.Key); err != nil {
				s.log.Warn("blob removal failed", zap.String("key", a.Key), zap.Error(err))
				continue
			}
		}
		if err := s.attachments.Delete(ctx, a.ID); err != nil {
			s.log.Warn("attachment row removal failed", zap.String("id", a.ID), zap.Error(err))
			continue
		}
		removed++
	}
	if removed > 0 {
		s.log.Info("abandoned uploads collected", zap.Int("count", removed))
	}
}

// doomedRefs is what a purge is about to make unreachable.
type doomedRefs struct {
	ids         []string
	attachments []string
	threads     []string
}

// expiring reads the links a purge destroys, before it destroys them.
func (s *MaintenanceService) expiring(ctx context.Context) doomedRefs {
	graph, ok := s.elements.(AttachmentGraph)
	if !ok {
		return doomedRefs{}
	}
	rows, err := graph.ExpiringSoon(ctx, time.Now().Add(-domain.TrashRetention))
	if err != nil {
		s.log.Warn("could not read what the purge is about to remove", zap.Error(err))
		return doomedRefs{}
	}
	out := doomedRefs{attachments: AttachmentRefsOf(rows)}
	for _, el := range rows {
		out.ids = append(out.ids, el.ID)
		if el.Type == domain.TypeCommentThread {
			out.threads = append(out.threads, el.ID)
		}
	}
	return out
}

// warnExpiring tells people what they are about to lose — JN21.
//
// The failure this closes: `PurgeExpired` returned a count to a log line, so
// the ninety-day promise was kept and broken entirely in silence. The one place
// the window was ever stated to a human was the trash panel's EMPTY state,
// meaning the sentence rendered only when there was nothing left to lose.
//
// Grouped by trash batch, which is the part that makes this shippable. Deleting
// a wrapped production cascades the whole subtree under one `trashBatchId`
// (JN18), so a per-element warning would put four hundred bells in somebody's
// inbox for one decision they made in March — and an inbox that floods is an
// inbox nobody opens, which would leave the warning worse than useless. One
// batch, one notice, naming the root and the member count, exactly as the trash
// panel now renders it.
//
// Idempotent through a stamp on one member rather than a stamp on all of them:
// a batch is skipped when ANY of its members is already marked, so a 400-element
// deletion costs one write, not four hundred, on the sweep that warns about it.
func (s *MaintenanceService) warnExpiring(ctx context.Context) {
	if s.notifier == nil || s.newID == nil {
		return
	}
	graph, ok := s.elements.(AttachmentGraph)
	if !ok {
		return
	}
	// Everything whose ninety days runs out inside the next week. `ExpiringSoon`
	// asks "deleted before X"; a purge happens at X = now-retention, so the
	// window that ends a week from now starts at now-retention+lead.
	rows, err := graph.ExpiringSoon(ctx, time.Now().UTC().Add(-domain.TrashRetention).Add(PurgeWarningLead))
	if err != nil {
		s.log.Warn("could not read what is about to expire", zap.Error(err))
		return
	}

	type batch struct {
		root    *domain.Element // the container if there is one, else the first row
		members int
		warned  bool
	}
	batches := map[string]*batch{}
	order := make([]string, 0, len(rows))
	for _, el := range rows {
		key := el.TrashBatchID
		if key == "" {
			key = el.ID // a lone delete is a batch of one
		}
		b := batches[key]
		if b == nil {
			b = &batch{root: el}
			batches[key] = b
			order = append(order, key)
		}
		b.members++
		if warned, _ := el.Content[purgeWarnedKey].(bool); warned {
			b.warned = true
		}
		// The batch's root is what the person recognises — "Ep 1 —
		// Pre-Production", not the fourteenth sticky note inside it.
		if el.Type.IsContainer() && !b.root.Type.IsContainer() {
			b.root = el
		}
	}

	for _, key := range order {
		b := batches[key]
		if b.warned {
			continue
		}
		recipient := b.root.DeletedBy
		if recipient == "" {
			recipient = b.root.CreatedBy
		}
		if recipient == "" {
			continue // nobody to tell; the purge will take it silently as before
		}
		s.notifier.Notify(ctx, &domain.Notification{
			ID: s.newID(), UserID: recipient, Kind: domain.NotifyTrashExpiring,
			ActorID: recipient, Message: purgeWarningMessage(b.root, b.members),
			CreatedAt: time.Now().UTC(),
		})
		// Stamp after the bell, not before: a failed notify should be retried
		// on the next sweep, and there are twenty-eight of them left in the week.
		if _, err := s.elements.MergePatch(ctx, b.root.ID, domain.Content{
			"content": map[string]any{purgeWarnedKey: true},
		}); err != nil {
			s.log.Warn("could not mark a purge warning as sent",
				zap.String("element", b.root.ID), zap.Error(err))
		}
	}
}

// purgeWarningMessage says what is going and when, in that order.
//
// The item first, because the recipient is scanning a bell for whether this is
// about something they care about; "7 days" first would make every one of these
// look identical.
func purgeWarningMessage(root *domain.Element, members int) string {
	title := root.Title()
	if title == "" {
		title = string(root.Type)
	}
	msg := "\"" + title + "\""
	if members > 1 {
		msg += " and " + strconv.Itoa(members-1) + " more"
	}
	return msg + " will be permanently deleted from your trash in 7 days. Restore it from Trash if you still need it."
}

// collectOrphanedComments takes a purged thread's messages with it.
//
// Purging a COMMENT_THREAD used to leave its messages behind for good: the
// repository port had no delete of any kind, so no purge path could even be
// wired. The product rule survives — nobody gains a way to unsay a comment; the
// STORAGE simply stops being unable to forget one.
func (s *MaintenanceService) collectOrphanedComments(ctx context.Context, threadIDs []string) {
	if len(threadIDs) == 0 || s.comments == nil {
		return
	}
	purger, ok := s.comments.(ThreadCommentPurger)
	if !ok {
		return
	}
	for _, id := range threadIDs {
		if err := purger.DeleteByThread(ctx, id); err != nil {
			s.log.Warn("thread messages outlived their thread",
				zap.String("thread", id), zap.Error(err))
		}
	}
}

// trimJournal enforces the transaction log's retention window.
func (s *MaintenanceService) trimJournal(ctx context.Context) {
	if s.journal == nil {
		return
	}
	purger, ok := s.journal.(TransactionPurger)
	if !ok {
		return
	}
	n, err := purger.DeleteOlderThan(ctx, time.Now().UTC().Add(-JournalRetention))
	if err != nil {
		s.log.Warn("journal retention sweep failed", zap.Error(err))
		return
	}
	if n > 0 {
		s.log.Info("expired journal rows removed", zap.Int64("count", n))
	}
}

// Run sweeps immediately, then on the interval until the context ends.
func (s *MaintenanceService) Run(ctx context.Context, every time.Duration) {
	s.SweepOnce(ctx)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.SweepOnce(ctx)
		}
	}
}

package service

import (
	"context"
	"fmt"

	"go.uber.org/zap"

	"qomranote/backend/internal/domain"
)

// TransactionRedactor is the half of the journal that can forget CONTENT while
// keeping the record that something happened.
//
// Optional and discovered by assertion, like every other capability the storage
// layer grew after its port was written.
type TransactionRedactor interface {
	// RedactElements strips changes and undoChanges from every op naming one of
	// these elements, leaving the op — who, when, what verb — in place.
	RedactElements(ctx context.Context, elementIDs []string) (int64, error)
}

// BoundedDescendants is the subtree read with a ceiling on it.
//
// Optional and discovered by assertion so the port stays as it is: every caller
// of Descendants — Duplicate, Export, the delete cascade, the account purge —
// would otherwise have to change together, and three of them are in packages
// that have nothing to do with this bound.
type BoundedDescendants interface {
	// DescendantsLimited returns at most limit elements and reports whether it
	// stopped short of the whole subtree.
	DescendantsLimited(ctx context.Context, rootID string, includeDeleted bool, limit int) ([]*domain.Element, bool, error)
}

// maxSubtree is the largest subtree a single request will walk.
//
// Descendants had no depth or size cap at all, on the READ side of the same
// hazard the op count has on the write side: Duplicate, Export, the delete
// cascade and the account purge each load a whole subtree into memory in one
// request, and a workspace is a tree a person grows without ever being told
// there is a limit. 5,000 is far above any board anyone has built and far below
// the point where one request is the reason the process runs out of memory.
const maxSubtree = 5000

// subtreeOf reads a subtree and REFUSES when it is larger than one request
// should carry, rather than silently returning part of it.
//
// The refusal is the point. A truncated duplicate is a board missing cards
// nobody can name; a truncated export is an archive that looks complete.
//
// Deliberately NOT used by the delete cascade or the account purge. Those have
// to finish: a bound that refuses would leave a person unable to trash their
// largest board or close their account, which is a worse failure than a slow
// query and — for the account case — a legal one.
func subtreeOf(ctx context.Context, repo domain.ElementRepository, rootID string, includeDeleted bool) ([]*domain.Element, error) {
	bounded, ok := repo.(BoundedDescendants)
	if !ok {
		return repo.Descendants(ctx, rootID, includeDeleted)
	}
	els, truncated, err := bounded.DescendantsLimited(ctx, rootID, includeDeleted, maxSubtree)
	if err != nil {
		return nil, err
	}
	if truncated {
		return nil, fmt.Errorf("%w: this holds more than %d items — too much for one operation",
			domain.ErrValidation, maxSubtree)
	}
	return els, nil
}

// Collector is the half of a permanent delete that is not the element row.
//
// Two things outlive a hard delete unless something takes them:
//
//   - The uploaded bytes. The only garbage collector in the product swept
//     presigned-but-never-completed uploads; a COMPLETED attachment whose IMAGE
//     element was purged matched no query anywhere. Every "Delete forever" left
//     the actual photograph in the bucket, still reachable, while the product
//     told a filmmaker the rejected casting photo was gone. That is the content
//     class most likely to be sensitive, and it was the one class the delete
//     gesture did not reach.
//
//   - The journal's copy of it. A delete op carries the deleted element's prior
//     content verbatim in undoChanges, forever, and GET /boards/:id/transactions
//     serves it to any current editor — including one invited after the
//     deletion. So "Empty trash" was contradicted by a live, editor-readable
//     archive of exactly what had been emptied.
//
// Both are driven from the code path that hard-deletes rather than from a
// sweeper, because a sweeper leaves a window in which Empty Trash has visibly
// not emptied anything.
type Collector struct {
	elements    domain.ElementRepository
	attachments domain.AttachmentRepository  // optional
	blobs       BlobRemover                  // optional
	journal     domain.TransactionRepository // optional
	log         *zap.Logger
}

// NewCollector constructs the collector. Every dependency past the element
// store is optional so a minimal wiring still deletes elements.
func NewCollector(elements domain.ElementRepository, log *zap.Logger) *Collector {
	return &Collector{elements: elements, log: log.Named("purge")}
}

// AttachBlobs lets the collector remove stored bytes, not merely the rows that
// name them.
func (c *Collector) AttachBlobs(attachments domain.AttachmentRepository, blobs BlobRemover) {
	c.attachments, c.blobs = attachments, blobs
}

// AttachJournal lets the collector strip a purged element's content from the
// transaction log.
func (c *Collector) AttachJournal(journal domain.TransactionRepository) { c.journal = journal }

// Collect runs after the elements are gone. It takes the ids and the
// attachment references READ BEFORE the delete, because content.attachmentId
// dies with the row and cannot be looked up afterwards.
func (c *Collector) Collect(ctx context.Context, ids []string, attachmentIDs []string) {
	c.collectBlobs(ctx, attachmentIDs)
	c.redactJournal(ctx, ids)
}

// AttachmentRefsOf reads the attachment ids a set of elements names, for a
// caller that is about to delete them.
func AttachmentRefsOf(els []*domain.Element) []string {
	seen := map[string]bool{}
	var out []string
	for _, el := range els {
		att, _ := el.Content["attachmentId"].(string)
		if att == "" || seen[att] {
			continue
		}
		seen[att] = true
		out = append(out, att)
	}
	return out
}

// collectBlobs removes the bytes behind attachments nothing points at any more.
//
// Reference-counted rather than "the element that named it is gone": a
// duplicated card shares its original's attachment id, and collecting on the
// weaker test would take the picture out from under the copy. The count FAILS
// CLOSED — an attachment whose referrer query errored is kept, because keeping
// bytes nobody wants costs storage and deleting bytes somebody wants costs the
// thing itself.
func (c *Collector) collectBlobs(ctx context.Context, attachmentIDs []string) {
	if len(attachmentIDs) == 0 || c.attachments == nil {
		return
	}
	graph, ok := c.elements.(AttachmentGraph)
	if !ok {
		c.log.Warn("no attachment graph — uploaded files will outlive the elements that named them")
		return
	}
	referrers, err := graph.AttachmentReferrers(ctx, attachmentIDs)
	if err != nil {
		c.log.Warn("attachment referrer count failed — keeping every blob", zap.Error(err))
		return
	}
	removed := 0
	for _, id := range attachmentIDs {
		if referrers[id] > 0 {
			continue // still on somebody's board
		}
		att, err := c.attachments.Get(ctx, id)
		if err != nil {
			continue
		}
		if c.blobs != nil {
			if err := c.blobs.Remove(att.Key); err != nil {
				c.log.Warn("blob survived a permanent delete",
					zap.String("key", att.Key), zap.Error(err))
				continue
			}
		}
		if err := c.attachments.Delete(ctx, att.ID); err != nil {
			c.log.Warn("attachment row survived a permanent delete",
				zap.String("id", att.ID), zap.Error(err))
			continue
		}
		removed++
	}
	if removed > 0 {
		c.log.Info("attachments collected behind a permanent delete", zap.Int("count", removed))
	}
}

// redactJournal strips the deleted content out of the change log.
//
// The ROW stays — "Ali deleted a card" is audit, and the audit is the reason the
// journal exists. What goes is the verbatim copy of what the card said, which is
// the part that made a permanent delete not permanent.
func (c *Collector) redactJournal(ctx context.Context, ids []string) {
	if len(ids) == 0 || c.journal == nil {
		return
	}
	redactor, ok := c.journal.(TransactionRedactor)
	if !ok {
		c.log.Warn("no journal redactor — deleted content stays readable through the board history endpoint",
			zap.Int("elements", len(ids)))
		return
	}
	n, err := redactor.RedactElements(ctx, ids)
	if err != nil {
		c.log.Error("deleted content could not be stripped from the journal and is still served by board history",
			zap.Int("elements", len(ids)), zap.Error(err))
		return
	}
	if n > 0 {
		c.log.Info("journal entries redacted behind a permanent delete", zap.Int64("count", n))
	}
}

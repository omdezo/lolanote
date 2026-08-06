package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"qomranote/backend/internal/agent"
)

// EnsureIndexes creates every index the query paths rely on. Idempotent;
// run via `qomranote migrate` and at API startup.
func (s *Store) EnsureIndexes(ctx context.Context) error {
	specs := map[string][]mongo.IndexModel{
		colElements: {
			// THE core query: all children of a parent (§9.4).
			{Keys: bson.D{{Key: "location.parentId", Value: 1}, {Key: "deletedAt", Value: 1}}},
			{Keys: bson.D{{Key: "type", Value: 1}, {Key: "content.cloneSourceId", Value: 1}}},
			{Keys: bson.D{{Key: "acl.ownerId", Value: 1}, {Key: "type", Value: 1}}},
			{Keys: bson.D{{Key: "deletedBy", Value: 1}, {Key: "deletedAt", Value: 1}}},
			{Keys: bson.D{{Key: "acl.publicEditLink", Value: 1}}, Options: options.Index().SetSparse(true)},
			{Keys: bson.D{{Key: "acl.viewLink.token", Value: 1}}, Options: options.Index().SetSparse(true)},
			{Keys: bson.D{
				{Key: "content.textPreview", Value: "text"},
				{Key: "content.title", Value: "text"},
				{Key: "content.filename", Value: "text"},
			}},
			// Reminder sweep: due, un-notified tasks.
			{Keys: bson.D{{Key: "type", Value: 1}, {Key: "content.reminderAt", Value: 1}}, Options: options.Index().SetSparse(true)},
		},
		colTransactions: {
			{Keys: bson.D{{Key: "boardId", Value: 1}, {Key: "createdAt", Value: -1}}},
			// "What did the AI change on this board?" — and the lookup revert
			// uses to replay a run's inverses.
			{Keys: bson.D{{Key: "agentRunId", Value: 1}}, Options: options.Index().SetSparse(true)},
			// The retention sweep's range delete, and the account purge's scan.
			// The highest-write collection in the system had neither, because it
			// had no deletes at all: Op.Changes and Op.UndoChanges are full
			// content snapshots, so "delete my account" left a complete queryable
			// copy of everything the person ever wrote, keyed by their userId.
			{Keys: bson.D{{Key: "createdAt", Value: 1}}},
			{Keys: bson.D{{Key: "userId", Value: 1}}},
		},
		colAgentRuns: {
			{Keys: bson.D{{Key: "tenantSub", Value: 1}, {Key: "createdAt", Value: -1}}},
			{Keys: bson.D{{Key: "task.rootBoardId", Value: 1}, {Key: "createdAt", Value: -1}}},
			// Re-ask clusters. "They asked the same thing three times" is the
			// operational definition of a capability that does not work, and
			// without this index it is a collection scan over every run a tenant
			// has ever made — so the question was never asked. The key is a hash
			// and never the text, which is what makes the aggregate content-free
			// by construction rather than by a promise about the query.
			{
				Keys: bson.D{
					{Key: "tenantSub", Value: 1},
					{Key: "task.intentKey", Value: 1},
					{Key: "createdAt", Value: -1},
				},
				Options: options.Index().SetSparse(true),
			},
			// One live run per board, enforced by the database rather than by a
			// check that a concurrent create could slip past.
			{
				Keys: bson.D{{Key: "task.rootBoardId", Value: 1}},
				Options: options.Index().SetUnique(true).
					SetPartialFilterExpression(bson.M{"active": true}),
			},
			{Keys: bson.D{{Key: "active", Value: 1}}},
			// The overlap half of the single-run guard. The unique index above
			// still holds one live run per exact root; this is what lets the
			// admission query ask the OTHER direction — "is a live run rooted
			// somewhere below me" — without a collection scan on every create.
			{
				Keys:    bson.D{{Key: "task.ancestorIds", Value: 1}, {Key: "active", Value: 1}},
				Options: options.Index().SetSparse(true),
			},
			// Erasure's second axis, and the owner's audit read.
			//
			// Run.tenantSub is who STARTED the run; boardOwnerSub is whose board
			// it read. A purge keyed only on the first leaves a verbatim partial
			// copy of a deleted account's board inside a collaborator's history,
			// which is the row this index exists to be able to find.
			{
				Keys:    bson.D{{Key: "boardOwnerSub", Value: 1}, {Key: "createdAt", Value: -1}},
				Options: options.Index().SetSparse(true),
			},
			// Retention. Both collections were a permanent, uncapped archive of
			// every prompt, refinement and steer a person ever typed — including
			// the plans they explicitly pressed Discard on, which are excluded
			// from the audit VIEW and were never excluded from storage. Nothing
			// in the product said this existed and nothing let anyone clear it.
			//
			// The partial filter is load-bearing: an in-flight run must never be
			// swept out from under itself, so only terminal runs age out.
			{
				Keys: bson.D{{Key: "completedAt", Value: 1}},
				Options: options.Index().
					SetExpireAfterSeconds(int32(agent.HistoryRetention.Seconds())).
					SetPartialFilterExpression(bson.M{"active": false}),
			},
		},
		colAgentEvents: {
			// The journal is ordered and gap-free: this uniqueness is what makes
			// a client's "events since N" cursor meaningful.
			{Keys: bson.D{{Key: "runId", Value: 1}, {Key: "sequence", Value: 1}}, Options: options.Index().SetUnique(true)},
			// For a long time this was the collection's ONLY index, so the only
			// question the journal could answer was "what happened in this one
			// run" — every rate, histogram or window over it was a full scan.
			// These two are what turn the journal from a per-run transcript into
			// something measurable.
			{Keys: bson.D{{Key: "at", Value: 1}, {Key: "type", Value: 1}}},
			{Keys: bson.D{{Key: "runId", Value: 1}, {Key: "type", Value: 1}}},
			{Keys: bson.D{{Key: "tenantSub", Value: 1}, {Key: "at", Value: -1}}, Options: options.Index().SetSparse(true)},
			// Same window as the runs it belongs to. An event outliving its run
			// would be a transcript with no way back to what it was about.
			{
				Keys:    bson.D{{Key: "at", Value: 1}},
				Options: options.Index().SetExpireAfterSeconds(int32(agent.HistoryRetention.Seconds())),
			},
		},
		colAuditEvents: {
			// All three end on _id descending because _id is what AuditRepo.List
			// both sorts and pages on, and an index can only serve a sort it is
			// keyed for. ObjectIds are monotonic, so _id order IS time order:
			// ending on `at` instead would order the rows identically and still
			// serve no page, turning every read into a blocking in-memory sort
			// of the whole matched set. Since/Until stay a filter on `at`; they
			// are never the sort, so `at` never needs to be an index key here.
			//
			// The org console's default view: this tenant, newest first.
			{Keys: bson.D{{Key: "orgId", Value: 1}, {Key: "_id", Value: -1}}},
			// The taxonomy filter. Action sits before the sort key so an anchored
			// prefix ("org.member_") narrows on the index rather than after the
			// fetch — the alternative is scanning a year of a busy org's rows to
			// answer "who changed roles", which is the question the panel opens on.
			{Keys: bson.D{{Key: "orgId", Value: 1}, {Key: "action", Value: 1}, {Key: "_id", Value: -1}}},
			// "Everything this person did", across orgs and personal boards. It is
			// the offboarding read and the incident read, and it is the one query
			// no org-scoped index can serve.
			{Keys: bson.D{{Key: "actor.sub", Value: 1}, {Key: "_id", Value: -1}}},
		},
		colUsers: {
			{Keys: bson.D{{Key: "keycloakSub", Value: 1}}, Options: options.Index().SetUnique(true)},
			{Keys: bson.D{{Key: "email", Value: 1}}},
		},
		colComments: {
			{Keys: bson.D{{Key: "threadId", Value: 1}, {Key: "createdAt", Value: 1}}},
		},
		colLabels: {
			{Keys: bson.D{{Key: "ownerId", Value: 1}, {Key: "name", Value: 1}}},
		},
		colAttachments: {
			{Keys: bson.D{{Key: "ownerId", Value: 1}, {Key: "createdAt", Value: -1}}},
		},
		colNotifications: {
			{Keys: bson.D{{Key: "userId", Value: 1}, {Key: "read", Value: 1}, {Key: "createdAt", Value: -1}}},
		},
	}
	for col, models := range specs {
		if _, err := s.DB.Collection(col).Indexes().CreateMany(ctx, models); err != nil {
			return err
		}
	}
	return nil
}

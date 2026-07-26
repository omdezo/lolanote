package mongo

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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
		},
		colAgentRuns: {
			{Keys: bson.D{{Key: "tenantSub", Value: 1}, {Key: "createdAt", Value: -1}}},
			{Keys: bson.D{{Key: "task.rootBoardId", Value: 1}, {Key: "createdAt", Value: -1}}},
			// One live run per board, enforced by the database rather than by a
			// check that a concurrent create could slip past.
			{
				Keys: bson.D{{Key: "task.rootBoardId", Value: 1}},
				Options: options.Index().SetUnique(true).
					SetPartialFilterExpression(bson.M{"active": true}),
			},
			{Keys: bson.D{{Key: "active", Value: 1}}},
		},
		colAgentEvents: {
			// The journal is ordered and gap-free: this uniqueness is what makes
			// a client's "events since N" cursor meaningful.
			{Keys: bson.D{{Key: "runId", Value: 1}, {Key: "sequence", Value: 1}}, Options: options.Index().SetUnique(true)},
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

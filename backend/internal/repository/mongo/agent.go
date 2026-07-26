package mongo

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/domain"
)

// Mongo adapters for the agent's run store and event journal.

// AgentRunRepo persists agent runs.
type AgentRunRepo struct{ col *mongo.Collection }

// NewAgentRunRepo constructs the repository.
func NewAgentRunRepo(s *Store) *AgentRunRepo {
	return &AgentRunRepo{col: s.DB.Collection(colAgentRuns)}
}

var _ agent.RunStore = (*AgentRunRepo)(nil)

// Insert stores a new run. The unique partial index on rootBoardId (active runs
// only) turns the single-run-per-board rule into a storage guarantee, so a race
// between two creates cannot produce two live runs on one canvas.
func (r *AgentRunRepo) Insert(ctx context.Context, run *agent.Run) error {
	run.Rev = 1
	if _, err := r.col.InsertOne(ctx, run); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

func (r *AgentRunRepo) Get(ctx context.Context, id string) (*agent.Run, error) {
	var run agent.Run
	err := r.col.FindOne(ctx, bson.M{"_id": id}).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// Update is a compare-and-swap on rev. A worker holding stale state loses the
// write instead of overwriting a newer transition.
func (r *AgentRunRepo) Update(ctx context.Context, run *agent.Run, expectedRev int64) error {
	next := expectedRev + 1
	run.Rev = next
	res, err := r.col.ReplaceOne(ctx, bson.M{"_id": run.ID, "rev": expectedRev}, run)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		run.Rev = expectedRev
		return domain.ErrConflict
	}
	return nil
}

func (r *AgentRunRepo) ActiveByBoard(ctx context.Context, boardID string) (*agent.Run, error) {
	var run agent.Run
	err := r.col.FindOne(ctx, bson.M{"task.rootBoardId": boardID, "active": true}).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// ListByBoard filters by tenant, and by board when boardID is non-empty — the
// empty case is the tenant-wide read the daily spend cap uses.
func (r *AgentRunRepo) ListByBoard(ctx context.Context, tenant, boardID string, limit int) ([]*agent.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	filter := bson.M{"tenantSub": tenant}
	if boardID != "" {
		filter["task.rootBoardId"] = boardID
	}
	cur, err := r.col.Find(ctx, filter,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*agent.Run
	return out, cur.All(ctx, &out)
}

func (r *AgentRunRepo) Unfinished(ctx context.Context) ([]*agent.Run, error) {
	cur, err := r.col.Find(ctx, bson.M{"active": true})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*agent.Run
	return out, cur.All(ctx, &out)
}

func (r *AgentRunRepo) DeleteByTenant(ctx context.Context, tenant string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"tenantSub": tenant})
	return err
}

// AgentEventRepo is the append-only run journal.
type AgentEventRepo struct{ col *mongo.Collection }

// NewAgentEventRepo constructs the repository.
func NewAgentEventRepo(s *Store) *AgentEventRepo {
	return &AgentEventRepo{col: s.DB.Collection(colAgentEvents)}
}

var _ agent.EventStore = (*AgentEventRepo)(nil)

// Append assigns the run's next sequence number and writes the event.
//
// The sequence is derived from a count rather than a shared counter, and the
// unique (runId, sequence) index is what makes that safe: a concurrent append
// that picks the same number is rejected and retried, so the journal stays
// ordered and gap-free rather than silently interleaving.
func (r *AgentEventRepo) Append(ctx context.Context, ev *agent.Event) error {
	for attempt := 0; attempt < 5; attempt++ {
		n, err := r.col.CountDocuments(ctx, bson.M{"runId": ev.RunID})
		if err != nil {
			return err
		}
		ev.Sequence = n + 1
		_, err = r.col.InsertOne(ctx, ev)
		if err == nil {
			return nil
		}
		if !mongo.IsDuplicateKeyError(err) {
			return err
		}
	}
	return domain.ErrConflict
}

func (r *AgentEventRepo) List(ctx context.Context, runID string, since int64, limit int) ([]*agent.Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	cur, err := r.col.Find(ctx,
		bson.M{"runId": runID, "sequence": bson.M{"$gt": since}},
		options.Find().SetSort(bson.D{{Key: "sequence", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*agent.Event
	return out, cur.All(ctx, &out)
}

// DeleteByTenant removes a user's journal entries by joining through their runs
// — account deletion must leave nothing behind (G13).
func (r *AgentEventRepo) DeleteByTenant(ctx context.Context, tenant string) error {
	runs := r.col.Database().Collection(colAgentRuns)
	cur, err := runs.Find(ctx, bson.M{"tenantSub": tenant}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	var ids []string
	for cur.Next(ctx) {
		var doc struct {
			ID string `bson:"_id"`
		}
		if err := cur.Decode(&doc); err == nil {
			ids = append(ids, doc.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	_, err = r.col.DeleteMany(ctx, bson.M{"runId": bson.M{"$in": ids}})
	return err
}

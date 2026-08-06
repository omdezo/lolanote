package mongo

import (
	"context"
	"errors"
	"time"

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

var (
	_ agent.RunOverlapStore    = (*AgentRunRepo)(nil)
	_ agent.RunBoardOwnerStore = (*AgentRunRepo)(nil)
	_ agent.RunAnalyticsStore  = (*AgentRunRepo)(nil)
)

// ActiveOverlapping finds a live run whose subtree overlaps this one's.
//
// Both directions, because containment is not symmetric in the index: a run
// rooted at one of MY ancestors reaches down into me (`rootBoardId ∈ chain`),
// and a run rooted at one of my DESCENDANTS is reached down into by me
// (`ancestorIds ∋ boardID`). The old single-equality query saw neither, so
// starting a second run one level down was a one-click bypass of the fairness
// guard — and the polite "someone else started one" message made the invariant
// look stronger than it was.
func (r *AgentRunRepo) ActiveOverlapping(ctx context.Context, boardID string, ancestorIDs []string) (*agent.Run, error) {
	chain := append([]string{boardID}, ancestorIDs...)
	var run agent.Run
	err := r.col.FindOne(ctx, bson.M{
		"active": true,
		"$or": []bson.M{
			{"task.rootBoardId": bson.M{"$in": chain}},
			{"task.ancestorIds": boardID},
		},
	}).Decode(&run)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, domain.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &run, nil
}

// ListByBoardOwner returns runs on boards this person owns, whoever ran them.
func (r *AgentRunRepo) ListByBoardOwner(ctx context.Context, ownerSub, boardID string, limit int) ([]*agent.Run, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	filter := bson.M{"boardOwnerSub": ownerSub}
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

// DeleteByBoardOwner is erasure's second axis: runs OTHER people made on this
// person's boards, which a tenant-keyed delete can never reach.
func (r *AgentRunRepo) DeleteByBoardOwner(ctx context.Context, ownerSub string) error {
	_, err := r.col.DeleteMany(ctx, bson.M{"boardOwnerSub": ownerSub})
	return err
}

func runFilterQuery(tenant string, f agent.RunFilter) bson.M {
	q := bson.M{}
	if tenant != "" {
		q["tenantSub"] = tenant
	}
	if !f.Since.IsZero() {
		q["createdAt"] = bson.M{"$gte": f.Since}
	}
	if len(f.States) > 0 {
		q["state"] = bson.M{"$in": f.States}
	}
	if f.BoardID != "" {
		q["task.rootBoardId"] = f.BoardID
	} else if len(f.BoardIDs) > 0 {
		q["task.rootBoardId"] = bson.M{"$in": f.BoardIDs}
	}
	return q
}

// ListByTenant is the account-wide run query four surfaces were blocked on.
func (r *AgentRunRepo) ListByTenant(ctx context.Context, tenant string, f agent.RunFilter, limit int) ([]*agent.Run, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	cur, err := r.col.Find(ctx, runFilterQuery(tenant, f),
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	var out []*agent.Run
	return out, cur.All(ctx, &out)
}

// AggregateUsage answers the spend question in the database.
//
// The caller it replaces summed `ListByBoard(tenant, "", 500)` in Go, which
// caps at 500 documents with no error when a day exceeds them — so the cap
// under-reported precisely the heavy days it exists to bound, and the person
// spending the most was the one least likely to be stopped.
func (r *AgentRunRepo) AggregateUsage(ctx context.Context, tenant string, f agent.RunFilter) (agent.UsageRollup, error) {
	out := agent.UsageRollup{ByState: map[agent.RunState]int64{}}
	cur, err := r.col.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: runFilterQuery(tenant, f)}},
		{{Key: "$group", Value: bson.M{
			"_id":  "$state",
			"n":    bson.M{"$sum": 1},
			"cost": bson.M{"$sum": "$usage.costUsd"},
			"in":   bson.M{"$sum": "$usage.inputTokens"},
			"out":  bson.M{"$sum": "$usage.outputTokens"},
		}}},
	})
	if err != nil {
		return out, err
	}
	defer cur.Close(ctx)
	for cur.Next(ctx) {
		var row struct {
			State agent.RunState `bson:"_id"`
			N     int64          `bson:"n"`
			Cost  float64        `bson:"cost"`
			In    int64          `bson:"in"`
			Out   int64          `bson:"out"`
		}
		if err := cur.Decode(&row); err != nil {
			return out, err
		}
		out.Runs += row.N
		out.CostUSD += row.Cost
		out.InTokens += row.In
		out.OutTokens += row.Out
		out.ByState[row.State] = row.N
	}
	return out, cur.Err()
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
type AgentEventRepo struct {
	col *mongo.Collection
	seq *mongo.Collection
}

// NewAgentEventRepo constructs the repository.
func NewAgentEventRepo(s *Store) *AgentEventRepo {
	return &AgentEventRepo{
		col: s.DB.Collection(colAgentEvents),
		seq: s.DB.Collection(colAgentEventSeq),
	}
}

var _ agent.EventStore = (*AgentEventRepo)(nil)

// Append allocates the run's next sequence atomically and writes the event.
//
// The sequence used to be derived from `CountDocuments({runId})` inside a
// five-attempt retry loop. That was quadratic — a run emitting E events did E
// counts over a growing range — and, worse, it was check-then-act on a document
// several goroutines append to at once (the run loop per step, the state
// machine per transition, the steer path). When all five attempts lost the race
// the event was DROPPED and only warned about, so the journal acquired holes
// under exactly the load that produces the most interesting behaviour, and the
// client's `sequence > since` cursor had no way to notice.
//
// `$inc` with an upsert hands out each number exactly once in one round trip,
// which removes both problems at the same time. The unique (runId, sequence)
// index stays, now as a genuine invariant rather than a collision detector.
func (r *AgentEventRepo) Append(ctx context.Context, ev *agent.Event) error {
	var counter struct {
		N int64 `bson:"n"`
	}
	err := r.seq.FindOneAndUpdate(ctx,
		bson.M{"_id": ev.RunID},
		bson.M{"$inc": bson.M{"n": 1}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&counter)
	if err != nil {
		return err
	}
	ev.Sequence = counter.N
	if _, err := r.col.InsertOne(ctx, ev); err != nil {
		if mongo.IsDuplicateKeyError(err) {
			return domain.ErrConflict
		}
		return err
	}
	return nil
}

// Aggregate groups journal rows by event type over a time window.
//
// The journal held every timing the loop needs and carried exactly one index —
// unique (runId, sequence) — so the only question it could answer was "what
// happened in this one run". Anything wider was a full collection scan, and the
// port had no method signature that could even express one, which is why every
// proposed analytics surface looked like a data-collection project rather than
// the projection it actually is. Tenant and board are denormalised onto the
// event at emit, so this filters rather than joins.
func (r *AgentEventRepo) Aggregate(ctx context.Context, tenant string, since time.Time, types []agent.EventType) (map[agent.EventType]int64, error) {
	match := bson.M{"at": bson.M{"$gte": since}}
	if tenant != "" {
		match["tenantSub"] = tenant
	}
	if len(types) > 0 {
		match["type"] = bson.M{"$in": types}
	}
	cur, err := r.col.Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: match}},
		{{Key: "$group", Value: bson.M{"_id": "$type", "n": bson.M{"$sum": 1}}}},
	})
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := map[agent.EventType]int64{}
	for cur.Next(ctx) {
		var row struct {
			Type agent.EventType `bson:"_id"`
			N    int64           `bson:"n"`
		}
		if err := cur.Decode(&row); err != nil {
			return nil, err
		}
		out[row.Type] = row.N
	}
	return out, cur.Err()
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

var _ agent.EventBoardOwnerPurger = (*AgentEventRepo)(nil)

// DeleteByTenant removes a user's journal entries by joining through their runs
// — account deletion must leave nothing behind (G13).
func (r *AgentEventRepo) DeleteByTenant(ctx context.Context, tenant string) error {
	return r.deleteByRunFilter(ctx, bson.M{"tenantSub": tenant})
}

// DeleteByBoardOwner is the same join on the other key: the journal of a run a
// COLLABORATOR made on this person's board carries their content in its staged
// -action summaries, and no tenant-keyed delete has ever been able to see it.
func (r *AgentEventRepo) DeleteByBoardOwner(ctx context.Context, ownerSub string) error {
	return r.deleteByRunFilter(ctx, bson.M{"boardOwnerSub": ownerSub})
}

func (r *AgentEventRepo) deleteByRunFilter(ctx context.Context, filter bson.M) error {
	runs := r.col.Database().Collection(colAgentRuns)
	cur, err := runs.Find(ctx, filter, options.Find().SetProjection(bson.M{"_id": 1}))
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
	if _, err := r.col.DeleteMany(ctx, bson.M{"runId": bson.M{"$in": ids}}); err != nil {
		return err
	}
	// The sequence counters are part of the user's data too: leaving them
	// behind would make "nothing left after account deletion" a claim with a
	// row-per-run counterexample.
	_, err = r.seq.DeleteMany(ctx, bson.M{"_id": bson.M{"$in": ids}})
	return err
}

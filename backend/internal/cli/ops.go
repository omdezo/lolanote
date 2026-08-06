package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.uber.org/zap"

	"qomranote/backend/internal/config"
	"qomranote/backend/internal/domain"
	repo "qomranote/backend/internal/repository/mongo"
)

// version is stamped at build time via -ldflags "-X ...".
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the QomraNote version",
	Run: func(*cobra.Command, []string) {
		fmt.Println("qomranote", version)
	},
}

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Create MongoDB indexes and purge expired trash",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, log, err := bootstrap()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()

		// Before the store is dialled, not after: a MONGO_URI carrying
		// ?replicaSet= cannot connect to a member that has never been
		// configured, which is the state this exists to leave.
		if err := ensureReplicaSet(ctx, cfg, log); err != nil {
			return err
		}

		store, err := repo.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close(context.Background()) }()

		if err := store.EnsureIndexes(ctx); err != nil {
			return err
		}
		log.Info("indexes ensured")

		// Trash retention: permanently delete items older than 3 months (§3.4).
		purged, err := repo.NewElementRepo(store).PurgeExpired(ctx, time.Now().Add(-domain.TrashRetention))
		if err != nil {
			return err
		}
		log.Info("expired trash purged", zap.Int64("count", purged))

		// Connectors left pointing at deleted elements. The write path takes
		// them down with their endpoint now, but boards edited before that
		// shipped still carry the leftovers — invisible lines that draw
		// nothing yet stay in the graph.
		swept, err := repo.NewElementRepo(store).SweepOrphanConnectors(ctx, time.Now().UTC())
		if err != nil {
			return err
		}
		log.Info("orphan connectors swept", zap.Int64("count", swept))

		// Files stored as a presigned URL rather than as a reference. Fixing the
		// upload path helps only new uploads; every board made before it still
		// carries a bearer credential that expires — so on day seven the picture
		// disappears from a board nobody touched, with the bytes still in the
		// bucket. Idempotent: an element already pointing at the route is
		// skipped.
		repointed, err := repo.NewElementRepo(store).RepointAttachmentURLs(ctx)
		if err != nil {
			return err
		}
		log.Info("attachment references repointed", zap.Int64("count", repointed))

		// Colour an agent run set under the key nothing renders. The compiler
		// wrote content.color; the card reads content.backgroundColor. Every
		// colour any past run chose has been invisible since the feature
		// shipped.
		recoloured, err := repo.NewElementRepo(store).RepointAgentColorKey(ctx)
		if err != nil {
			return err
		}
		log.Info("agent colour keys corrected", zap.Int64("count", recoloured))

		// Garbage-collect abandoned uploads: attachments presigned but never
		// completed within 24h (their bytes, if any, are orphaned).
		stale, err := repo.NewAttachmentRepo(store).StalePresigned(ctx, time.Now().Add(-24*time.Hour))
		if err != nil {
			return err
		}
		attRepo := repo.NewAttachmentRepo(store)
		for _, a := range stale {
			_ = attRepo.Delete(ctx, a.ID)
		}
		log.Info("abandoned uploads garbage-collected", zap.Int("count", len(stale)))
		return nil
	},
}

// notYetInitialized is the server's code for "a member of a replica set that
// has never been configured" — distinct from unreachable, unauthorized, or a
// mongod not started with --replSet at all, none of which this may initiate.
const notYetInitialized = 94

// defaultReplicaMemberHost is the compose service name, used when MONGO_URI
// carries no host this can read.
const defaultReplicaMemberHost = "mongo:27017"

// ensureReplicaSet configures a single-node replica set when the deployment
// declares one and the node has never been initiated (§II.0.5).
//
// Multi-document transactions and the oplog both require a replica set, so a
// standalone mongod is why the write path compensates by hand and why backups
// have no point-in-time recovery. Compose's mongo healthcheck self-initiates
// the dev set; this is the same step for a database started some other way.
// Idempotent — an already-configured member reports its status and nothing is
// written.
//
// It dials its own client rather than reusing repo.Connect, and dials it
// DIRECT: an uninitiated member has no primary, so ordinary server selection
// under a ?replicaSet= URI blocks until it times out instead of answering the
// one question being asked.
func ensureReplicaSet(ctx context.Context, cfg *config.Config, log *zap.Logger) error {
	if cfg.MongoReplicaSet == "" {
		return nil
	}
	client, err := mongo.Connect(ctx, options.Client().ApplyURI(cfg.MongoURI).SetDirect(true))
	if err != nil {
		return fmt.Errorf("replica set: connect: %w", err)
	}
	defer func() { _ = client.Disconnect(context.Background()) }()

	admin := client.Database("admin")
	err = admin.RunCommand(ctx, bson.D{{Key: "replSetGetStatus", Value: 1}}).Err()
	switch {
	case err == nil:
		log.Info("replica set already initialized", zap.String("set", cfg.MongoReplicaSet))
		return nil
	case !isNotYetInitialized(err):
		return fmt.Errorf("replica set: status: %w", err)
	}

	host := replicaMemberHost(cfg.MongoURI)
	initiate := bson.D{{Key: "replSetInitiate", Value: bson.D{
		{Key: "_id", Value: cfg.MongoReplicaSet},
		{Key: "members", Value: bson.A{bson.D{
			{Key: "_id", Value: 0},
			{Key: "host", Value: host},
		}}},
	}}}
	if err := admin.RunCommand(ctx, initiate).Err(); err != nil {
		return fmt.Errorf("replica set: initiate: %w", err)
	}
	log.Info("replica set initiated",
		zap.String("set", cfg.MongoReplicaSet), zap.String("host", host))
	return nil
}

func isNotYetInitialized(err error) bool {
	var cmdErr mongo.CommandError
	if errors.As(err, &cmdErr) {
		return cmdErr.Code == notYetInitialized || cmdErr.Name == "NotYetInitialized"
	}
	return false
}

// replicaMemberHost is the address the member records as its own identity.
//
// Read out of MONGO_URI, because the name this process used to reach mongod is
// a name the rest of that network resolves too; a member entry of
// "localhost:27017" written from inside the API container would pin the set to
// an address only mongod itself can use, and every other client would fail to
// find a primary. Falls back to the compose service name for the URI shapes
// this deliberately shallow parse cannot read — a seed list, or mongodb+srv,
// whose deployments are replica sets already and never reach the initiate path.
func replicaMemberHost(uri string) string {
	if strings.HasPrefix(uri, "mongodb+srv://") {
		return defaultReplicaMemberHost
	}
	rest := uri
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i] // database, options, fragment — never the host
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		rest = rest[i+1:] // userinfo, whose password may itself contain '@'
	}
	if rest == "" || strings.Contains(rest, ",") {
		return defaultReplicaMemberHost
	}
	if !strings.Contains(rest, ":") {
		rest += ":27017"
	}
	return rest
}

var seedCmd = &cobra.Command{
	Use:   "seed",
	Short: "Seed the built-in template board library",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, log, err := bootstrap()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), 60*time.Second)
		defer cancel()

		store, err := repo.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close(context.Background()) }()
		elements := repo.NewElementRepo(store)

		// System templates, owned by the reserved "system" principal so the
		// template picker can offer them to everyone (§5).
		templates := []struct {
			title string
			notes []string
		}{
			{"Moodboard", []string{"Drop images that capture the feeling", "Add color swatches from the imagery", "Collect typography references"}},
			{"Creative Brief", []string{"Objective — what are we making and why?", "Audience — who is it for?", "Deliverables & deadline"}},
			{"Storyboard", []string{"Scene 1 — opening shot", "Scene 2 — the turn", "Scene 3 — resolution"}},
			{"Project Plan", []string{"Goals", "Milestones", "Risks & dependencies"}},
		}

		now := time.Now().UTC()
		created := 0
		existing, err := elements.BoardsOwnedBy(ctx, "system", true)
		if err != nil {
			return err
		}
		have := map[string]bool{}
		for _, b := range existing {
			have[b.Title()] = true
		}
		for _, tpl := range templates {
			if have[tpl.title] {
				continue
			}
			board := &domain.Element{
				ID:       repo.NewID(),
				Type:     domain.TypeBoard,
				Location: domain.Location{Section: domain.SectionCanvas},
				Content: domain.Content{
					"title": tpl.title, "isTemplate": true,
				},
				ACL:       &domain.ACL{OwnerID: "system", Editors: []string{}},
				CreatedBy: "system",
				CreatedAt: now, UpdatedAt: now,
			}
			if err := elements.Insert(ctx, board); err != nil {
				return err
			}
			for i, note := range tpl.notes {
				el := &domain.Element{
					ID:   repo.NewID(),
					Type: domain.TypeCard,
					Location: domain.Location{
						ParentID: board.ID,
						Section:  domain.SectionCanvas,
						Position: domain.Point{X: 80, Y: 80 + float64(i)*140},
						Width:    300,
					},
					Content: domain.Content{
						"textPreview": note,
						"doc": map[string]any{
							"type": "doc",
							"content": []any{map[string]any{
								"type":    "paragraph",
								"content": []any{map[string]any{"type": "text", "text": note}},
							}},
						},
					},
					CreatedBy: "system",
					CreatedAt: now, UpdatedAt: now,
				}
				if err := elements.Insert(ctx, el); err != nil {
					return err
				}
			}
			created++
		}
		log.Info("seed complete", zap.Int("templatesCreated", created), zap.String("db", cfg.MongoDB))
		return nil
	},
}

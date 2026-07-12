package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

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
				ID:   repo.NewID(),
				Type: domain.TypeBoard,
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
								"type": "paragraph",
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

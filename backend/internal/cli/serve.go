package cli

import (
	"context"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"qomranote/backend/internal/auth"
	"qomranote/backend/internal/domain"
	"qomranote/backend/internal/realtime"
	repo "qomranote/backend/internal/repository/mongo"
	"qomranote/backend/internal/service"
	"qomranote/backend/internal/storage"
	httptransport "qomranote/backend/internal/transport/http"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Run the QomraNote API server",
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, log, err := bootstrap()
		if err != nil {
			return err
		}
		defer func() { _ = log.Sync() }()

		ctx, stop := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		// ---- data tier ----
		store, err := repo.Connect(ctx, cfg.MongoURI, cfg.MongoDB)
		if err != nil {
			return err
		}
		defer func() { _ = store.Close(context.Background()) }()
		if err := store.EnsureIndexes(ctx); err != nil {
			return err
		}
		log.Info("mongo connected", zap.String("db", cfg.MongoDB))

		elements := repo.NewElementRepo(store)
		transactions := repo.NewTransactionRepo(store)
		users := repo.NewUserRepo(store)
		comments := repo.NewCommentRepo(store)
		labels := repo.NewLabelRepo(store)
		attachments := repo.NewAttachmentRepo(store)
		notifications := repo.NewNotificationRepo(store)

		// ---- auth tier (Keycloak) ----
		verifier, err := auth.NewVerifier(ctx, cfg)
		if err != nil {
			return err
		}
		var identity domain.IdentityProvider
		var accounts domain.AccountManager
		if cfg.KeycloakAdminSecret != "" {
			kc := auth.NewKeycloakIdentityProvider(cfg)
			identity, accounts = kc, kc
		}
		tickets := auth.NewTicketStore()
		log.Info("keycloak verifier ready", zap.String("issuer", cfg.KeycloakIssuer))

		// ---- object storage ----
		var presigner domain.Presigner
		var localDriver *storage.LocalPresigner
		if cfg.StorageDriver == "r2" {
			presigner, err = storage.NewR2Presigner(ctx, cfg)
			if err != nil {
				return err
			}
			log.Info("storage driver: cloudflare r2", zap.String("bucket", cfg.R2Bucket))
		} else {
			localDriver, err = storage.NewLocalPresigner(cfg.LocalStorageDir, cfg.PublicAPIBase)
			if err != nil {
				return err
			}
			presigner = localDriver
			log.Info("storage driver: local", zap.String("dir", cfg.LocalStorageDir))
		}

		// ---- realtime + services ----
		hub := realtime.NewHub(log)
		access := service.NewAccessResolver(elements)
		newID := service.IDGenerator(repo.NewID)

		notifier := service.NewNotifier(notifications, users)
		userSvc := service.NewUserService(users, elements, identity, newID)
		accountSvc := service.NewAccountService(users, elements, labels, attachments, notifications, accounts, log)
		txnSvc := service.NewTransactionService(elements, transactions, access, hub, newID, log)
		txnSvc.AttachNotifier(notifier)
		elementSvc := service.NewElementService(elements, access, newID)
		boardSvc := service.NewBoardService(elements, users, access)
		shareSvc := service.NewShareService(elements, userSvc, notifier, access)
		uploadSvc := service.NewUploadService(attachments, presigner, newID)
		linkSvc := service.NewLinkService()
		commentSvc := service.NewCommentService(comments, elements, notifier, access, newID)
		labelSvc := service.NewLabelService(labels, elements, access, newID)

		// Background sweep: due task reminders become notifications (§4.11).
		reminderSvc := service.NewReminderService(elements, access, notifier, newID, log)
		go reminderSvc.Run(ctx, time.Minute)

		handlers := &httptransport.Handlers{
			Users: userSvc, Account: accountSvc, Boards: boardSvc, Elements: elementSvc,
			Txns: txnSvc, Share: shareSvc, Uploads: uploadSvc,
			Links: linkSvc, Comments: commentSvc, Labels: labelSvc,
			Notifications: notifications, Access: access,
			Hub: hub, Verifier: verifier, Tickets: tickets, Local: localDriver, Log: log,
		}

		server := httptransport.NewServer(cfg, log, handlers)
		log.Info("qomranote is up",
			zap.String("env", cfg.AppEnv),
			zap.String("addr", cfg.HTTPAddr))
		return server.Start(ctx)
	},
}

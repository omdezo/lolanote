package cli

import (
	"context"
	"fmt"
	"net/http"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"qomranote/backend/internal/agent"
	"qomranote/backend/internal/agent/cognition"
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
		// Whichever driver is in play, it is also the thing that can delete bytes.
		// This used to be local-only, so every deletion path on a production R2
		// deployment removed the row and left the object — the promise "we delete
		// your files" was kept exactly on the one backend nobody runs.
		var blobRemover service.BlobRemover
		if cfg.StorageDriver == "r2" {
			r2, rerr := storage.NewR2Presigner(ctx, cfg)
			if rerr != nil {
				return rerr
			}
			presigner, blobRemover = r2, r2
			log.Info("storage driver: cloudflare r2", zap.String("bucket", cfg.R2Bucket))
		} else {
			localDriver, err = storage.NewLocalPresigner(cfg.LocalStorageDir, cfg.PublicAPIBase)
			if err != nil {
				return err
			}
			presigner, blobRemover = localDriver, localDriver
			log.Info("storage driver: local", zap.String("dir", cfg.LocalStorageDir))
		}

		// ---- realtime + services ----
		hub := realtime.NewHub(log)
		access := service.NewAccessResolver(elements)
		newID := service.IDGenerator(repo.NewID)

		// Constructed before the services that write to it, because it is a
		// constructor argument to each of them rather than an Attach: an audit
		// trail that a deployment can be brought up without is one that will be.
		auditSvc := service.NewAuditService(repo.NewAuditRepo(store), newID, log)

		notifier := service.NewNotifier(notifications, users)
		notifier.AttachEvents(hub) // live notification badges
		userSvc := service.NewUserService(users, elements, identity, newID)
		accountSvc := service.NewAccountService(users, elements, labels, attachments, notifications, accounts, auditSvc, log)
		txnSvc := service.NewTransactionService(elements, transactions, access, hub, newID, log)
		txnSvc.AttachNotifier(notifier)
		txnSvc.AttachLabels(labels) // a create op may carry the caller's own labelIds
		elementSvc := service.NewElementService(elements, access, newID)
		boardSvc := service.NewBoardService(elements, users, access)
		// An export names its files instead of carrying signed URLs into them.
		boardSvc.AttachAttachments(attachments)
		shareSvc := service.NewShareService(elements, userSvc, notifier, access, auditSvc)
		uploadSvc := service.NewUploadService(attachments, presigner, newID)
		linkSvc := service.NewLinkService()
		commentSvc := service.NewCommentService(comments, elements, notifier, access, newID)
		commentSvc.AttachEvents(hub) // live comment threads
		labelSvc := service.NewLabelService(labels, elements, access, newID)

		// Background sweeps: due task reminders become notifications (§4.11);
		// expired trash purges and abandoned uploads collect every 6 hours.
		reminderSvc := service.NewReminderService(elements, access, notifier, newID, log)
		go reminderSvc.Run(ctx, time.Minute)
		maintenanceSvc := service.NewMaintenanceService(elements, attachments, blobRemover, log)

		// The optional halves of deletion and access control, all discovered by
		// assertion so a service can be built in a test without them. Left
		// unwired each one logs a warning and skips its work, which is how
		// "delete my account" shipped leaving the blobs, the comments and the
		// undo journal on disk: every purge was written, none was reachable.
		accountSvc.AttachBlobs(blobRemover)
		accountSvc.AttachComments(comments)
		accountSvc.AttachJournal(transactions)
		maintenanceSvc.AttachComments(comments)
		maintenanceSvc.AttachJournal(transactions)
		maintenanceSvc.AttachNotifier(notifier, newID)
		// The non-element half of a permanent delete, shared by the sweeper and
		// the explicit "Delete forever" gesture so the two cannot drift. Without
		// it, deleting a card left the photograph in the bucket and the card's
		// text readable through board history — the delete gesture stopping at
		// the element row is exactly the shape of the account-deletion bug above.
		collector := service.NewCollector(elements, log)
		collector.AttachBlobs(attachments, blobRemover)
		collector.AttachJournal(transactions)
		elementSvc.AttachCollector(collector)
		accountSvc.AttachCollector(collector)
		// Reading a file is a question about the board it is shown on, so the
		// upload service needs the element graph and the resolver to answer it.
		uploadSvc.AttachAccess(elements, access)
		// Revoking a share has to reach the sockets already holding it open;
		// without the hub, RemoveEditor returned success while the removed
		// session kept streaming the board.
		shareSvc.AttachEvictor(hub)

		// Started only now that every optional half is attached. `Run` sweeps
		// ONCE synchronously before its first tick, so launching it where it was
		// constructed raced its own wiring: the first pass after every restart
		// read `comments`, `journal` and `notifier` while the main goroutine was
		// still writing them — an unsynchronised read, and on the six-hour
		// cadence of a process that restarts daily, the pass that mattered.
		go maintenanceSvc.Run(ctx, 6*time.Hour)

		// ---- AI agent ----
		// Opt-in per deployment: without a provider key the harness is nil and
		// its routes report the feature as unavailable. Nothing else changes —
		// the agent adds a caller to the existing write path, not a new one.
		var agentSvc *agent.Service
		if provider, perr := cognition.New(cognition.Options{
			Provider:        cfg.AgentProvider,
			Model:           cfg.AgentModel,
			AnthropicAPIKey: cfg.AnthropicAPIKey,
			GeminiAPIKey:    cfg.GeminiAPIKey,
			// Two tiers, both optional. With them empty or equal to AGENT_MODEL
			// this resolves to the single client it always was and the
			// NewFallback below wraps it exactly as before; set, New returns a
			// Router over two Fallbacks and the same wrap still composes.
			FastModel:   cfg.AgentModelFast,
			StrongModel: cfg.AgentModelStrong,
			// One call may take a few turns' worth of the run's clock, never the
			// whole thing. Derived here rather than fixed in the provider so
			// raising MaxSteps can never make a single hung call eat the run.
			CallTimeout: agent.CallTimeoutFor(agent.DefaultBudget()),
		}); perr != nil {
			log.Info("AI agent disabled", zap.String("reason", perr.Error()))
		} else {
			// One 502 mid-run loses every staged action and the money already
			// spent producing them. Retrying a call is far cheaper than redoing
			// a run, so every provider is wrapped.
			provider = cognition.NewFallback(provider)
			agentSvc = agent.NewService(agent.Config{
				Elements: elements, Users: users,
				Txns: txnSvc, TxnRepo: transactions, Access: access, Labels: labels, Comments: comments,
				Images: agent.NewHTTPImageFetcher(attachments),
				// The REPOSITORY, not just the fetcher. Only Images was wired, so
				// look_at could read a file while ownedAttachments — which returns
				// nil the moment this is nil — silently discarded every id the
				// composer sent. Asked to place a picture that was sitting right
				// there, the agent answered "the request does not provide an
				// attachment ID", which was true of what it had been handed and
				// reads as a lie to the person who attached it. The whole
				// place_attachment capability had never once run in production.
				Attachments: attachments,
				Runs:        repo.NewAgentRunRepo(store),
				Events:      repo.NewAgentEventRepo(store),
				Provider:    provider, Bus: hub,
				NewID: agent.IDGenerator(repo.NewID), Log: log,
				DailyCapUSD: cfg.AgentDailyCapUSD,
				// Without these two the agent's outcome reaches only sockets
				// that happen to be open on that board, and its comments are
				// written silently — the producer side of two settings the
				// product has been offering with nothing behind them.
				Notifier:         notifier,
				CommentAnnouncer: commentSvc,
				// The commit and the undo, in the tenant-wide log rather than
				// only in the run's own journal, which expires with the run.
				Audit: auditSvc,
			})
			accountSvc.AttachPurger(agentSvc)
			// A crash can leave a run mid-flight, and an unanswered proposal
			// holds its board's run slot until something expires it. Boot is not
			// enough for the second: a server that stays up for a week would let
			// abandoned plans accumulate, one dead board each.
			agentSvc.Reconcile(ctx)
			go func() {
				tick := time.NewTicker(5 * time.Minute)
				defer tick.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-tick.C:
						agentSvc.Reconcile(ctx)
					}
				}
			}()
			if cfg.AgentPriceInPer1M > 0 || cfg.AgentPriceOutPer1M > 0 {
				cognition.RegisterPrice(provider.Model(), cognition.Price{
					InputPer1M: cfg.AgentPriceInPer1M, OutputPer1M: cfg.AgentPriceOutPer1M,
				})
			}
			// An unpriced model costs zero, so the daily cap never binds and the
			// figure shown to the user is a confident nothing. Say so loudly at
			// boot: silence here is indistinguishable from "spending is fine".
			if !cognition.Priced(provider.Model()) {
				log.Warn("agent model has no known price — cost reporting will read $0 and the daily cap CANNOT bind",
					zap.String("model", provider.Model()),
					zap.String("fix", "set AGENT_PRICE_INPUT_PER_1M and AGENT_PRICE_OUTPUT_PER_1M from your provider's pricing page"))
			}
			log.Info("AI agent enabled",
				zap.String("model", provider.Model()),
				zap.Bool("priced", cognition.Priced(provider.Model())),
				zap.Float64("dailyCapUSD", cfg.AgentDailyCapUSD))
		}

		// Readiness: Mongo ping + Keycloak discovery reachable.
		readyCheck := func(rctx context.Context) error {
			rctx, cancel := context.WithTimeout(rctx, 3*time.Second)
			defer cancel()
			if err := store.Ping(rctx); err != nil {
				return fmt.Errorf("mongo: %w", err)
			}
			req, _ := http.NewRequestWithContext(rctx, http.MethodGet,
				strings.TrimSuffix(cfg.KeycloakInternalBase, "/")+"/realms/"+cfg.KeycloakRealm+"/.well-known/openid-configuration", nil)
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("keycloak: %w", err)
			}
			_ = res.Body.Close()
			if res.StatusCode >= 400 {
				return fmt.Errorf("keycloak: status %d", res.StatusCode)
			}
			// The agent's own dependency. Enablement was decided once at boot
			// from the presence of a key and never re-checked, so a rotated,
			// expired or rate-limited provider made every run fail while this
			// endpoint went on saying "ready" — the orchestrator kept the
			// instance in rotation and the only symptom anyone saw was runs
			// failing one at a time. The probe behind this is cached for a
			// minute; readiness is polled every few seconds and an uncached
			// probe on a paid API is a bill, not a health check.
			if agentSvc != nil && !agentSvc.ProviderHealthy(rctx) {
				return fmt.Errorf("agent provider: not answering")
			}
			return nil
		}

		handlers := &httptransport.Handlers{
			Users: userSvc, Account: accountSvc, Boards: boardSvc, Elements: elementSvc,
			Txns: txnSvc, Share: shareSvc, Uploads: uploadSvc,
			Links: linkSvc, Comments: commentSvc, Labels: labelSvc,
			Notifications: notifications, Agent: agentSvc, Access: access, Audit: auditSvc,
			Hub: hub, Verifier: verifier, Tickets: tickets, Local: localDriver,
			ReadyCheck: readyCheck, Log: log,
		}

		server := httptransport.NewServer(cfg, log, handlers)
		log.Info("qomranote is up",
			zap.String("env", cfg.AppEnv),
			zap.String("addr", cfg.HTTPAddr))
		return server.Start(ctx)
	},
}

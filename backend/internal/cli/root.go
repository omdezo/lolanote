// Package cli defines the qomranote command tree (Cobra).
package cli

import (
	"github.com/spf13/cobra"

	"qomranote/backend/internal/config"
	"qomranote/backend/internal/logger"

	"go.uber.org/zap"
)

var rootCmd = &cobra.Command{
	Use:   "qomranote",
	Short: "QomraNote — a visual, board-based workspace for creative work",
	Long: `QomraNote API server and operational tooling.

Everything on a board is a typed element; every mutation is a transaction
that powers undo/redo and realtime broadcast. See PLAN.md for the full
architecture.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the CLI.
func Execute() error {
	rootCmd.AddCommand(serveCmd, migrateCmd, seedCmd, versionCmd, agentCheckCmd, agentEvalCmd,
		// The product had no backup of any kind — one docker volume, on one host,
		// removable by `docker compose down -v`. These two are the smallest honest
		// answer; `backup --help` states plainly what they still do not protect.
		backupCmd, restoreCmd)
	return rootCmd.Execute()
}

// bootstrap loads config and builds the logger — shared by every subcommand.
func bootstrap() (*config.Config, *zap.Logger, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	log, err := logger.New(cfg.AppEnv, cfg.LogLevel)
	if err != nil {
		return nil, nil, err
	}
	// Said once, at boot, on the way in. The settings that are merely PROBABLY
	// wrong in production have honest deployments where they are not — an http
	// issuer behind a TLS-terminating proxy, a local storage driver on a single
	// backed-up host — so config refuses the unambiguous ones and reports these.
	// What was not acceptable was silence: nothing anywhere told an operator that
	// their uploads are outside every backup, or that the assistant has no daily
	// spend ceiling.
	for _, warning := range cfg.ProductionWarnings() {
		log.Warn("production configuration", zap.String("check", warning))
	}
	return cfg, log, nil
}

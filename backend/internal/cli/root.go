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
	rootCmd.AddCommand(serveCmd, migrateCmd, seedCmd, versionCmd)
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
	return cfg, log, nil
}

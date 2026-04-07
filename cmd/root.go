package cmd

import (
	"log/slog"
	"os"
	"strings"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "go-navidrome-ratings-sync",
	Short: "Sync ratings with Navidrome",
	Long:  "A tool to synchronize music ratings between local MP3 files and a Navidrome server.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgFile)
		if err != nil {
			return err
		}

		var level slog.Level
		switch strings.ToLower(cfg.LogLevel) {
		case "debug":
			level = slog.LevelDebug
		case "warn":
			level = slog.LevelWarn
		case "error":
			level = slog.LevelError
		default:
			level = slog.LevelInfo
		}

		logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
		slog.SetDefault(logger)

		cmd.SetContext(config.WithContext(cmd.Context(), cfg))
		return nil
	},
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgFile, "config", "c", "", "config file path (default: config.yaml)")
}

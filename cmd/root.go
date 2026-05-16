package cmd

import (
	"os"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-sync/internal/config"
	"github.com/rigerc/go-navidrome-sync/internal/output"
	"github.com/spf13/cobra"
)

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "go-navidrome-sync",
	Short: "Sync ratings with Navidrome",
	Long:  "A tool to synchronize music ratings between local MP3 files and a Navidrome server.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgFile)
		if err != nil {
			return err
		}

		var level log.Level
		switch strings.ToLower(cfg.LogLevel) {
		case "debug":
			level = log.DebugLevel
		case "warn":
			level = log.WarnLevel
		case "error":
			level = log.ErrorLevel
		default:
			level = log.InfoLevel
		}

		manager := output.NewManager(os.Stderr, level)
		logger := log.NewWithOptions(manager.LogWriter(), log.Options{
			Level:           level,
			Formatter:       log.TextFormatter,
			ReportTimestamp: true,
			TimeFormat:      "15:04:05",
		})
		log.SetDefault(logger)

		ctx := config.WithContext(cmd.Context(), cfg)
		cmd.SetContext(output.WithContext(ctx, manager))
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

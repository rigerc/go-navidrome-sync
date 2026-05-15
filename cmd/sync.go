package cmd

import (
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/output"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/sync"
	"github.com/spf13/cobra"
)

var (
	dryRun           bool
	preferFlag       string
	userFlag         string
	passFlag         string
	baseURLFlag      string
	remotePathPrefix string
	searchInterval   string
	tlsSkipFlag      bool
	reportJSONFlag   string
)

var syncCmd = &cobra.Command{
	Use:   "sync [music-path]",
	Short: "Sync ratings between local MP3/FLAC files and Navidrome",
	Long:  "Scan music files for ratings and bidirectionally sync them with a Navidrome server.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		baseCfg := config.FromContext(cmd.Context())
		if baseCfg == nil {
			return fmt.Errorf("config not loaded")
		}

		cfg := *baseCfg
		applySyncOverrides(&cfg, args)
		if err := config.Validate(&cfg); err != nil {
			return err
		}
		searchIntervalDuration, err := config.ParseSearchInterval(cfg.Sync.SearchInterval)
		if err != nil {
			return fmt.Errorf("parse sync search interval: %w", err)
		}

		logger := log.Default()
		manager := output.FromContext(cmd.Context())
		progress := manager.NewSyncProgress()
		defer progress.Close()

		musicPath := cfg.Sync.MusicPath
		if musicPath == "" {
			return fmt.Errorf("music path is required (provide as argument or set sync.musicpath in config)")
		}

		logger.Debug("Starting sync",
			"music_path", musicPath,
			"navidrome", cfg.Navidrome.BaseURL,
			"prefer", cfg.Sync.Prefer,
			"remote_path_prefix", cfg.Sync.RemotePathPrefix,
			"search_interval", searchIntervalDuration,
			"dry_run", dryRun,
			"report_json", reportJSONFlag,
		)

		localFiles, scanWarnings, err := sync.ScanLocalFiles(musicPath, logger, progress)
		if err != nil {
			return err
		}

		if len(localFiles) == 0 {
			logger.Info("No music files found in music path", "path", musicPath)
			return nil
		}

		progress.StartConnecting()
		client, err := navidrome.Connect(cmd.Context(), &cfg, logger)
		if err != nil {
			return err
		}

		runOutput, err := sync.Run(cmd.Context(), musicPath, localFiles, client, cfg.Sync.RemotePathPrefix, cfg.Sync.Prefer, searchIntervalDuration, dryRun, logger, progress)
		if err != nil {
			return err
		}
		if len(scanWarnings) > 0 {
			runOutput.Report.Warnings = append(runOutput.Report.Warnings, scanWarnings...)
			runOutput.Report.Summary.Warnings += len(scanWarnings)
		}

		if reportJSONFlag != "" {
			progress.WritingReport(reportJSONFlag)
			if err := sync.WriteReportJSON(reportJSONFlag, runOutput.Report); err != nil {
				return err
			}
			if !progress.Enabled() {
				logger.Info("Wrote sync report", "path", reportJSONFlag)
			}
		}

		if err := sync.ApplyResults(cmd.Context(), musicPath, runOutput.Results, client, dryRun, logger, progress); err != nil {
			return err
		}

		if progress.Enabled() {
			unmatchedEntries := make([]output.SummaryUnmatched, 0, len(runOutput.Report.Unmatched))
			for _, item := range runOutput.Report.Unmatched {
				unmatchedEntries = append(unmatchedEntries, output.SummaryUnmatched{
					Path:   item.Path,
					Reason: item.Reason,
				})
			}
			progress.PrintSummary(output.Summary{
				Pushed:            runOutput.Report.Summary.Pushed,
				Pulled:            runOutput.Report.Summary.Pulled,
				Skipped:           runOutput.Report.Summary.Skipped,
				ConflictsResolved: runOutput.Report.Summary.ConflictsResolved,
				Matched:           runOutput.Report.Summary.Matched,
				Unmatched:         runOutput.Report.Summary.Unmatched,
				NoResults:         runOutput.Report.Summary.NoResults,
				Ambiguous:         runOutput.Report.Summary.Ambiguous,
				Warnings:          runOutput.Report.Summary.Warnings,
				Errors:            runOutput.Report.Summary.Errors,
				DryRun:            runOutput.Report.Summary.DryRun,
				UnmatchedEntries:  unmatchedEntries,
			})
		}

		return nil
	},
}

func applySyncOverrides(cfg *config.Config, args []string) {
	if len(args) > 0 {
		cfg.Sync.MusicPath = args[0]
	}
	if baseURLFlag != "" {
		cfg.Navidrome.BaseURL = baseURLFlag
	}
	if userFlag != "" {
		cfg.Navidrome.User = userFlag
	}
	if passFlag != "" {
		cfg.Navidrome.Password = passFlag
	}
	if tlsSkipFlag {
		cfg.Navidrome.TLSSkipVerify = true
	}
	if preferFlag != "" {
		cfg.Sync.Prefer = preferFlag
	}
	if remotePathPrefix != "" {
		cfg.Sync.RemotePathPrefix = remotePathPrefix
	}
	if searchInterval != "" {
		cfg.Sync.SearchInterval = searchInterval
	}
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be done without making changes")
	syncCmd.Flags().StringVar(&preferFlag, "prefer", "", "preferred source on conflict: \"local\" or \"navidrome\"")
	syncCmd.Flags().StringVar(&userFlag, "user", "", "Navidrome username (overrides config)")
	syncCmd.Flags().StringVar(&passFlag, "password", "", "Navidrome password (overrides config)")
	syncCmd.Flags().StringVar(&baseURLFlag, "baseurl", "", "Navidrome base URL (overrides config)")
	syncCmd.Flags().StringVar(&remotePathPrefix, "remote-path-prefix", "", "strip this prefix from Navidrome song paths before matching")
	syncCmd.Flags().StringVar(&searchInterval, "search-interval", "", "minimum delay between remote search requests, e.g. 100ms or 1s (overrides config)")
	syncCmd.Flags().StringVar(&reportJSONFlag, "report-json", "", "write a JSON report with matched, unmatched, and ambiguous results")
	syncCmd.Flags().BoolVar(&tlsSkipFlag, "tls-skip-verify", false, "skip TLS certificate verification (overrides config)")
}

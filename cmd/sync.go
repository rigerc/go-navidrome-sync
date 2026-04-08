package cmd

import (
	"fmt"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
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
	tlsSkipFlag      bool
)

var syncCmd = &cobra.Command{
	Use:   "sync [music-path]",
	Short: "Sync ratings between local MP3/FLAC files and Navidrome",
	Long:  "Scan music files for ratings and bidirectionally sync them with a Navidrome server.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.FromContext(cmd.Context())
		logger := log.Default()

		musicPath := cfg.Sync.MusicPath
		if len(args) > 0 {
			musicPath = args[0]
		}

		if musicPath == "" {
			return fmt.Errorf("music path is required (provide as argument or set sync.musicpath in config)")
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
		prefer := cfg.Sync.Prefer
		if preferFlag != "" {
			prefer = preferFlag
		}
		if remotePathPrefix != "" {
			cfg.Sync.RemotePathPrefix = remotePathPrefix
		}

		if prefer != "local" && prefer != "navidrome" {
			return cmd.Help()
		}

		logger.Debug("Starting sync",
			"music_path", musicPath,
			"navidrome", cfg.Navidrome.BaseURL,
			"prefer", prefer,
			"remote_path_prefix", cfg.Sync.RemotePathPrefix,
			"dry_run", dryRun,
		)

		localFiles, err := sync.ScanLocalFiles(musicPath, logger)
		if err != nil {
			return err
		}

		if len(localFiles) == 0 {
			logger.Info("No music files found in music path", "path", musicPath)
			return nil
		}

		client, err := navidrome.Connect(cfg, logger)
		if err != nil {
			return err
		}

		results, err := sync.Run(musicPath, localFiles, client, cfg.Sync.RemotePathPrefix, prefer, dryRun, logger)
		if err != nil {
			return err
		}

		return sync.ApplyResults(musicPath, results, client, dryRun, logger)
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be done without making changes")
	syncCmd.Flags().StringVar(&preferFlag, "prefer", "", "preferred source on conflict: \"local\" or \"navidrome\"")
	syncCmd.Flags().StringVar(&userFlag, "user", "", "Navidrome username (overrides config)")
	syncCmd.Flags().StringVar(&passFlag, "password", "", "Navidrome password (overrides config)")
	syncCmd.Flags().StringVar(&baseURLFlag, "baseurl", "", "Navidrome base URL (overrides config)")
	syncCmd.Flags().StringVar(&remotePathPrefix, "remote-path-prefix", "", "strip this prefix from Navidrome song paths before matching")
	syncCmd.Flags().BoolVar(&tlsSkipFlag, "tls-skip-verify", false, "skip TLS certificate verification (overrides config)")
}

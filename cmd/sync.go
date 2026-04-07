package cmd

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/rigerc/go-navidrome-ratings-sync/internal/config"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/sync"
	"github.com/spf13/cobra"
)

var (
	dryRun      bool
	preferFlag  string
	userFlag    string
	passFlag    string
	baseURLFlag string
	tlsSkipFlag bool
)

var syncCmd = &cobra.Command{
	Use:   "sync [music-path]",
	Short: "Sync ratings between local MP3/FLAC files and Navidrome",
	Long:  "Scan music files for ratings and bidirectionally sync them with a Navidrome server.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg := config.FromContext(cmd.Context())
		log := slog.Default()

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

		if prefer != "local" && prefer != "navidrome" {
			return cmd.Help()
		}

		log.Debug("Starting sync",
			"music_path", musicPath,
			"navidrome", cfg.Navidrome.BaseURL,
			"prefer", prefer,
			"dry_run", dryRun,
		)

		localFiles, err := sync.ScanLocalFiles(musicPath, log)
		if err != nil {
			return err
		}

		if len(localFiles) == 0 {
			log.Info("No music files found in music path", "path", musicPath)
			return nil
		}

		artistNames := make(map[string]bool)
		for _, lf := range localFiles {
			if lf.Artist != "" {
				artistNames[lf.Artist] = true
			} else {
				parts := strings.SplitN(lf.RelPath, "/", 2)
				if len(parts) >= 1 {
					artistNames[parts[0]] = true
				}
			}
		}

		client, err := navidrome.Connect(cfg, log)
		if err != nil {
			return err
		}

		remoteSongs, err := client.FetchSongsForArtists(artistNames)
		if err != nil {
			return err
		}

		if len(remoteSongs) == 0 {
			log.Warn("No songs found on Navidrome for local artists")
			return nil
		}

		log.Info("Fetched songs", "local", len(localFiles), "remote", len(remoteSongs))

		results, err := sync.Run(musicPath, localFiles, remoteSongs, prefer, dryRun, log)
		if err != nil {
			return err
		}

		return sync.ApplyResults(musicPath, results, client, dryRun, log)
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)

	syncCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be done without making changes")
	syncCmd.Flags().StringVar(&preferFlag, "prefer", "", "preferred source on conflict: \"local\" or \"navidrome\"")
	syncCmd.Flags().StringVar(&userFlag, "user", "", "Navidrome username (overrides config)")
	syncCmd.Flags().StringVar(&passFlag, "password", "", "Navidrome password (overrides config)")
	syncCmd.Flags().StringVar(&baseURLFlag, "baseurl", "", "Navidrome base URL (overrides config)")
	syncCmd.Flags().BoolVar(&tlsSkipFlag, "tls-skip-verify", false, "skip TLS certificate verification (overrides config)")
}

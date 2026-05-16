package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-sync/internal/config"
	"github.com/rigerc/go-navidrome-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-sync/internal/playlist"
	"github.com/spf13/cobra"
)

var (
	playlistDryRun        bool
	playlistPreferFlag    string
	playlistPublicFlag    bool
	playlistRemoveMissing bool
	playlistOnUnmatched   string
	playlistExportPaths   string
	playlistReportJSON    string
)

var playlistsCmd = &cobra.Command{
	Use:   "playlists",
	Short: "Sync Navidrome playlists with local M3U files",
}

var playlistsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Navidrome playlists",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		client, err := navidrome.Connect(cmd.Context(), config.FromContext(cmd.Context()), log.Default())
		if err != nil {
			return err
		}
		playlists, err := client.Playlists(cmd.Context())
		if err != nil {
			return err
		}
		for _, p := range playlists {
			log.Info("Playlist", "id", p.ID, "name", p.Name, "songs", p.SongCount, "readonly", p.Readonly)
		}
		return nil
	},
}

var playlistsExportCmd = &cobra.Command{
	Use:   "export [dir]",
	Short: "Export Navidrome playlists to local M3U8 files",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := playlistConfig(cmd, args)
		if err != nil {
			return err
		}
		client, err := navidrome.Connect(cmd.Context(), config.FromContext(cmd.Context()), log.Default())
		if err != nil {
			return err
		}
		remotes, err := client.Playlists(cmd.Context())
		if err != nil {
			return err
		}
		plan := &playlist.Plan{}
		for _, remote := range remotes {
			plan.Actions = append(plan.Actions, playlist.PlannedAction{
				Action:   playlist.ActionExportLocal,
				Name:     remote.Name,
				RemoteID: remote.ID,
			})
		}
		return applyPlaylistPlan(cmd, client, cfg, plan)
	},
}

var playlistsImportCmd = &cobra.Command{
	Use:   "import [dir]",
	Short: "Import local M3U playlists into Navidrome",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := playlistConfig(cmd, args)
		if err != nil {
			return err
		}
		cfg.Prefer = "local"
		client, err := navidrome.Connect(cmd.Context(), config.FromContext(cmd.Context()), log.Default())
		if err != nil {
			return err
		}
		locals, err := playlist.LoadDir(cfg.Path, cfg.MusicPath)
		if err != nil {
			return err
		}
		plan, err := playlist.BuildPlan(cmd.Context(), client, cfg, locals)
		if err != nil {
			return err
		}
		filtered := &playlist.Plan{}
		for _, action := range plan.Actions {
			if action.Action == playlist.ActionExportLocal || action.Action == playlist.ActionReplaceLocal || action.Action == playlist.ActionDeleteLocal {
				continue
			}
			filtered.Actions = append(filtered.Actions, action)
		}
		return applyPlaylistPlan(cmd, client, cfg, filtered)
	},
}

var playlistsSyncCmd = &cobra.Command{
	Use:   "sync [dir]",
	Short: "Bidirectionally sync local M3U playlists with Navidrome",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := playlistConfig(cmd, args)
		if err != nil {
			return err
		}
		client, err := navidrome.Connect(cmd.Context(), config.FromContext(cmd.Context()), log.Default())
		if err != nil {
			return err
		}
		locals, err := playlist.LoadDir(cfg.Path, cfg.MusicPath)
		if err != nil {
			return err
		}
		plan, err := playlist.BuildPlan(cmd.Context(), client, cfg, locals)
		if err != nil {
			return err
		}
		return applyPlaylistPlan(cmd, client, cfg, plan)
	},
}

func playlistConfig(cmd *cobra.Command, args []string) (playlist.Config, error) {
	base := config.FromContext(cmd.Context())
	if base == nil {
		return playlist.Config{}, fmt.Errorf("config not loaded")
	}
	cfg := *base
	if err := config.Validate(&cfg); err != nil {
		return playlist.Config{}, err
	}
	pc := playlist.Config{
		Path:             cfg.Playlists.Path,
		MusicPath:        cfg.Playlists.MusicPath,
		RemotePathPrefix: cfg.Playlists.RemotePathPrefix,
		Prefer:           cfg.Playlists.Prefer,
		Public:           cfg.Playlists.Public,
		RemoveMissing:    cfg.Playlists.RemoveMissing,
		OnUnmatched:      cfg.Playlists.OnUnmatched,
		ExportPaths:      cfg.Playlists.ExportPaths,
	}
	if len(args) > 0 {
		pc.Path = args[0]
	}
	if playlistPreferFlag != "" {
		pc.Prefer = playlistPreferFlag
	}
	if playlistPublicFlag {
		pc.Public = true
	}
	if playlistRemoveMissing {
		pc.RemoveMissing = true
	}
	if playlistOnUnmatched != "" {
		pc.OnUnmatched = playlistOnUnmatched
	}
	if playlistExportPaths != "" {
		pc.ExportPaths = playlistExportPaths
	}
	if pc.MusicPath == "" {
		return playlist.Config{}, fmt.Errorf("playlist music path is required (set playlists.musicpath or sync.musicpath)")
	}
	absMusic, err := filepath.Abs(pc.MusicPath)
	if err != nil {
		return playlist.Config{}, err
	}
	pc.MusicPath = absMusic
	if err := pc.Validate(); err != nil {
		return playlist.Config{}, err
	}
	return pc, nil
}

func applyPlaylistPlan(cmd *cobra.Command, client *navidrome.Client, cfg playlist.Config, plan *playlist.Plan) error {
	logger := log.Default()
	if playlistReportJSON != "" {
		plan.DryRun = playlistDryRun
		if err := playlist.WriteReport(playlistReportJSON, plan); err != nil {
			return err
		}
	}
	return playlist.Apply(cmd.Context(), client, cfg, plan, playlistDryRun, logger)
}

func init() {
	rootCmd.AddCommand(playlistsCmd)
	playlistsCmd.AddCommand(playlistsListCmd, playlistsExportCmd, playlistsImportCmd, playlistsSyncCmd)
	for _, cmd := range []*cobra.Command{playlistsExportCmd, playlistsImportCmd, playlistsSyncCmd} {
		cmd.Flags().BoolVar(&playlistDryRun, "dry-run", false, "show what would be done without making changes")
		cmd.Flags().StringVar(&playlistPreferFlag, "prefer", "", "preferred source on conflict: \"local\" or \"navidrome\"")
		cmd.Flags().BoolVar(&playlistPublicFlag, "public", false, "make newly-created remote playlists public")
		cmd.Flags().BoolVar(&playlistRemoveMissing, "remove-missing", false, "delete remote playlists missing locally during sync")
		cmd.Flags().StringVar(&playlistOnUnmatched, "on-unmatched", "", "behavior for unmatched local tracks: \"error\" or \"skip\"")
		cmd.Flags().StringVar(&playlistExportPaths, "export-paths", "", "local export path style: \"relative\", \"absolute\", or \"remote\"")
		cmd.Flags().StringVar(&playlistReportJSON, "report-json", "", "write a JSON playlist sync report")
	}
}

package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/output"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/tag"
)

func ApplyResults(
	ctx context.Context,
	musicPath string,
	results []Result,
	client *navidrome.Client,
	dryRun bool,
	log *log.Logger,
	progress output.SyncProgress,
) error {
	failures := 0
	var firstErr error
	totalActions := 0
	for _, result := range results {
		if result.Action != ActionSkip {
			totalActions++
		}
		if result.PlayStatsAction != ActionSkip {
			totalActions++
		}
		if result.StarAction != ActionSkip {
			totalActions++
		}
	}
	if progress.Enabled() {
		progress.StartApplying(totalActions, dryRun)
	}
	processedActions := 0
	pushed := 0
	pulled := 0

	recordErr := func(err error) {
		failures++
		if firstErr == nil {
			firstErr = err
		}
	}

	for _, r := range results {
		// --- rating ---
		switch r.Action {
		case ActionPush:
			if dryRun {
				if !progress.Enabled() {
					log.Info("[DRY-RUN] Would push rating to Navidrome",
						"path", r.Path, "rating", r.NewRating)
				}
				pushed++
			} else {
				if err := client.SetRating(ctx, r.RemoteID, r.NewRating); err != nil {
					log.Error("Failed to push rating",
						"path", r.Path, "rating", r.NewRating, "error", err)
					recordErr(err)
				} else {
					if !progress.Enabled() {
						log.Info("Pushed rating to Navidrome",
							"path", r.Path, "rating", r.NewRating)
					}
					pushed++
				}
			}
			processedActions++
			if progress.Enabled() {
				progress.UpdateApplying(processedActions, totalActions, pushed, pulled, failures, dryRun)
			}

		case ActionPull:
			fullPath := filepath.Join(musicPath, r.Path)
			if dryRun {
				if !progress.Enabled() {
					log.Info("[DRY-RUN] Would write rating to local file",
						"path", r.Path, "rating", r.NewRating)
				}
				pulled++
			} else {
				if err := tag.WriteRating(fullPath, r.NewRating); err != nil {
					log.Error("Failed to write rating",
						"path", r.Path, "rating", r.NewRating, "error", err)
					recordErr(err)
				} else {
					if !progress.Enabled() {
						log.Info("Wrote rating to local file",
							"path", r.Path, "rating", r.NewRating)
					}
					pulled++
				}
			}
			processedActions++
			if progress.Enabled() {
				progress.UpdateApplying(processedActions, totalActions, pushed, pulled, failures, dryRun)
			}
		}

		// --- stars ---
		switch r.StarAction {
		case ActionPush:
			if dryRun {
				if !progress.Enabled() {
					log.Info("[DRY-RUN] Would push starred state to Navidrome", "path", r.Path, "starred", r.NewStarred)
				}
				pushed++
			} else {
				var err error
				if r.NewStarred {
					err = client.Star(ctx, r.RemoteID)
				} else {
					err = client.Unstar(ctx, r.RemoteID)
				}
				if err != nil {
					log.Error("Failed to push starred state", "path", r.Path, "starred", r.NewStarred, "error", err)
					recordErr(err)
				} else {
					if !progress.Enabled() {
						log.Info("Pushed starred state to Navidrome", "path", r.Path, "starred", r.NewStarred)
					}
					pushed++
				}
			}
			processedActions++
			if progress.Enabled() {
				progress.UpdateApplying(processedActions, totalActions, pushed, pulled, failures, dryRun)
			}
		case ActionPull:
			fullPath := filepath.Join(musicPath, r.Path)
			if dryRun {
				if !progress.Enabled() {
					log.Info("[DRY-RUN] Would write starred state to local file", "path", r.Path, "starred", r.NewStarred)
				}
				pulled++
			} else {
				if err := tag.WriteStarred(fullPath, r.NewStarred); err != nil {
					log.Error("Failed to write starred state", "path", r.Path, "starred", r.NewStarred, "error", err)
					recordErr(err)
				} else {
					if !progress.Enabled() {
						log.Info("Wrote starred state to local file", "path", r.Path, "starred", r.NewStarred)
					}
					pulled++
				}
			}
			processedActions++
			if progress.Enabled() {
				progress.UpdateApplying(processedActions, totalActions, pushed, pulled, failures, dryRun)
			}
		}

		// --- play stats ---
		switch r.PlayStatsAction {
		case ActionPush:
			if r.NewPlayed == nil {
				break
			}
			delta := r.NewPlayCount - r.OldRemotePlayCount
			scrobbleCount := int(delta)
			if scrobbleCount <= 0 {
				scrobbleCount = 1
			}
			if dryRun {
				if !progress.Enabled() {
					log.Info("[DRY-RUN] Would push play stats to Navidrome",
						"path", r.Path, "play_count", r.NewPlayCount, "played", r.NewPlayed, "scrobbles", scrobbleCount)
				}
				pushed++
			} else {
				if err := client.Scrobble(ctx, r.RemoteID, scrobbleCount, *r.NewPlayed); err != nil {
					log.Error("Failed to push play stats",
						"path", r.Path, "play_count", r.NewPlayCount, "error", err)
					recordErr(err)
				} else {
					if !progress.Enabled() {
						log.Info("Pushed play stats to Navidrome",
							"path", r.Path, "play_count", r.NewPlayCount, "played", r.NewPlayed)
					}
					pushed++
				}
			}
			processedActions++
			if progress.Enabled() {
				progress.UpdateApplying(processedActions, totalActions, pushed, pulled, failures, dryRun)
			}

		case ActionPull:
			fullPath := filepath.Join(musicPath, r.Path)
			if dryRun {
				if !progress.Enabled() {
					log.Info("[DRY-RUN] Would write play stats to local file",
						"path", r.Path, "play_count", r.NewPlayCount, "played", r.NewPlayed)
				}
				pulled++
			} else {
				if err := tag.WritePlayStats(fullPath, r.NewPlayCount, r.NewPlayed); err != nil {
					log.Error("Failed to write play stats",
						"path", r.Path, "play_count", r.NewPlayCount, "error", err)
					recordErr(err)
				} else {
					if !progress.Enabled() {
						log.Info("Wrote play stats to local file",
							"path", r.Path, "play_count", r.NewPlayCount, "played", r.NewPlayed)
					}
					pulled++
				}
			}
			processedActions++
			if progress.Enabled() {
				progress.UpdateApplying(processedActions, totalActions, pushed, pulled, failures, dryRun)
			}
		}
	}

	if failures > 0 {
		return fmt.Errorf("%d sync action(s) failed: %w", failures, firstErr)
	}

	return nil
}

func WriteReportJSON(path string, report RunReport) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil && filepath.Dir(path) != "." {
		return fmt.Errorf("creating report directory for %s: %w", path, err)
	}

	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal sync report: %w", err)
	}
	data = append(data, '\n')

	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write sync report %s: %w", path, err)
	}
	return nil
}

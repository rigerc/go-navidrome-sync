package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-sync/internal/output"
	"github.com/rigerc/go-navidrome-sync/internal/tag"
)

func parseRemotePlayed(s string) *time.Time {
	if s == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil
	}
	return &t
}

type Action int

const (
	ActionSkip Action = iota
	ActionPush
	ActionPull
)

func (a Action) String() string {
	switch a {
	case ActionPush:
		return "PUSH"
	case ActionPull:
		return "PULL"
	default:
		return "SKIP"
	}
}

type Options struct {
	SyncRatings   bool
	SyncPlayStats bool
	SyncStars     bool
	PreferStars   string
}

type Result struct {
	Action             Action
	Path               string
	OldLocal           int
	OldRemote          int
	NewRating          int
	RemoteID           string
	PlayStatsAction    Action
	OldLocalPlayed     *time.Time
	OldRemotePlayed    *time.Time
	OldRemotePlayCount int64
	NewPlayCount       int64
	NewPlayed          *time.Time
	StarAction         Action
	OldLocalStarred    bool
	OldRemoteStarred   bool
	NewStarred         bool
}

type LocalFile struct {
	RelPath string
	*tag.LocalFile
}

type RunOutput struct {
	Results []Result
	Report  RunReport
}

type RunReport struct {
	Summary   ReportSummary     `json:"summary"`
	Matched   []MatchedEntry    `json:"matched"`
	Unmatched []UnresolvedEntry `json:"unmatched"`
	Ambiguous []UnresolvedEntry `json:"ambiguous"`
	Warnings  []IssueEntry      `json:"warnings,omitempty"`
	Errors    []IssueEntry      `json:"errors,omitempty"`
}

type ReportSummary struct {
	Pushed            int  `json:"pushed"`
	Pulled            int  `json:"pulled"`
	Skipped           int  `json:"skipped"`
	ConflictsResolved int  `json:"conflicts_resolved"`
	Matched           int  `json:"matched"`
	Unmatched         int  `json:"unmatched"`
	NoResults         int  `json:"no_results"`
	Ambiguous         int  `json:"ambiguous"`
	Warnings          int  `json:"warnings"`
	Errors            int  `json:"errors"`
	DryRun            bool `json:"dry_run"`
}

type MatchedEntry struct {
	Path         string `json:"path"`
	Query        string `json:"query"`
	Method       string `json:"method"`
	RemoteID     string `json:"remote_id"`
	RemotePath   string `json:"remote_path"`
	LocalRating  int    `json:"local_rating"`
	RemoteRating int    `json:"remote_rating"`
}

type UnresolvedEntry struct {
	Path           string           `json:"path"`
	Query          string           `json:"query,omitempty"`
	Reason         string           `json:"reason"`
	LocalPath      string           `json:"local_path"`
	LocalCanonical string           `json:"local_canonical,omitempty"`
	Candidates     []CandidateEntry `json:"candidates,omitempty"`
}

type CandidateEntry struct {
	RawPath        string `json:"raw_path"`
	NormalizedPath string `json:"normalized_path"`
	Score          int    `json:"score,omitempty"`
}

type IssueEntry struct {
	Path    string `json:"path"`
	Query   string `json:"query,omitempty"`
	Source  string `json:"source"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

type scanJob struct {
	path    string
	relPath string
}

const (
	maxScanWorkers   = 32
	progressInterval = 25
)

var (
	readLocalFile = tag.ReadLocalFile
	audioFileExts = map[string]struct{}{".mp3": {}, ".flac": {}}
)

func DefaultOptions() Options {
	return Options{SyncRatings: true, SyncPlayStats: true}
}

type songSearcher interface {
	SearchSongsByTitle(ctx context.Context, title string, limit int) ([]*navidrome.RemoteSong, error)
}

type fallbackSongSearcher interface {
	SearchSongsByTitleFallback(ctx context.Context, title string, limit int) ([]*navidrome.RemoteSong, error)
}

func ScanLocalFiles(musicPath string, log *log.Logger, progress output.SyncProgress) ([]*LocalFile, []IssueEntry, error) {
	progress.StartScanning()

	workerCount := scanWorkerCount()
	jobs := make(chan scanJob, workerCount*4)
	results := make(chan *LocalFile, workerCount*4)
	warningEntries := make(chan IssueEntry, workerCount*2)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for job := range jobs {
				lf, err := readLocalFile(job.path)
				if err != nil {
					log.Warn("Failed to read file metadata", "path", job.relPath, "error", err)
					warningEntries <- IssueEntry{
						Path:    job.relPath,
						Source:  "local",
						Stage:   "scan_metadata",
						Message: err.Error(),
					}
					lf = &tag.LocalFile{}
				}

				results <- &LocalFile{RelPath: job.relPath, LocalFile: lf}

				log.Debug("Scanned local file", "path", job.relPath, "rating", lf.Rating,
					"mbid", lf.MusicBrainzID, "isrc", lf.ISRC)
			}
		}()
	}

	errs := make(chan error, 1)
	go func() {
		err := filepath.WalkDir(musicPath, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				log.Warn("Error accessing path", "path", path, "error", err)
				warningEntries <- IssueEntry{
					Path:    path,
					Source:  "local",
					Stage:   "scan_walk",
					Message: err.Error(),
				}
				return nil
			}
			if d.IsDir() || !isSupportedAudioFile(path) {
				return nil
			}

			rel, err := filepath.Rel(musicPath, path)
			if err != nil {
				return fmt.Errorf("failed to get relative path for %s: %w", path, err)
			}

			jobs <- scanJob{
				path:    path,
				relPath: filepath.ToSlash(rel),
			}
			return nil
		})
		close(jobs)
		errs <- err
	}()

	go func() {
		workers.Wait()
		close(results)
		close(warningEntries)
	}()

	var files []*LocalFile
	var warnings []IssueEntry
	doneWarnings := make(chan struct{})
	go func() {
		defer close(doneWarnings)
		for warning := range warningEntries {
			warnings = append(warnings, warning)
		}
	}()
	for file := range results {
		files = append(files, file)
		if progress.Enabled() && (len(files)%progressInterval == 0) {
			progress.UpdateScan(len(files))
		}
	}

	if err := <-errs; err != nil {
		return nil, nil, fmt.Errorf("failed to walk music path: %w", err)
	}
	<-doneWarnings

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})

	if progress.Enabled() {
		progress.UpdateScan(len(files))
	} else {
		log.Info("Scanned local files", "count", len(files))
	}
	return files, warnings, nil
}

func isSupportedAudioFile(path string) bool {
	_, ok := audioFileExts[strings.ToLower(filepath.Ext(path))]
	return ok
}

func scanWorkerCount() int {
	workers := runtime.GOMAXPROCS(0) * 2
	if workers < 4 {
		workers = 4
	}
	if workers > maxScanWorkers {
		workers = maxScanWorkers
	}
	return workers
}

func Run(
	ctx context.Context,
	musicPath string,
	localFiles []*LocalFile,
	searcher songSearcher,
	remotePathPrefix string,
	prefer string,
	searchInterval time.Duration,
	dryRun bool,
	log *log.Logger,
	progress output.SyncProgress,
) (*RunOutput, error) {
	return RunWithOptions(ctx, musicPath, localFiles, searcher, remotePathPrefix, prefer, searchInterval, dryRun, log, progress, DefaultOptions())
}

func RunWithOptions(
	ctx context.Context,
	musicPath string,
	localFiles []*LocalFile,
	searcher songSearcher,
	remotePathPrefix string,
	prefer string,
	searchInterval time.Duration,
	dryRun bool,
	log *log.Logger,
	progress output.SyncProgress,
	options Options,
) (*RunOutput, error) {
	report, err := matchLocalToRemote(ctx, localFiles, searcher, remotePathPrefix, searchInterval, log, progress)
	if err != nil {
		return nil, err
	}

	results := make([]Result, 0, len(report.matches))
	runReport := RunReport{
		Matched:   make([]MatchedEntry, 0, len(report.matches)),
		Unmatched: make([]UnresolvedEntry, 0, len(report.unmatched)),
		Ambiguous: make([]UnresolvedEntry, 0, len(report.ambiguous)),
		Warnings:  make([]IssueEntry, 0, len(report.warnings)),
		Errors:    make([]IssueEntry, 0, len(report.errors)),
	}

	pushed := 0
	pulled := 0
	skipped := 0
	conflicts := 0
	noResults := 0

	for _, m := range report.matches {
		localRating := m.local.Rating
		remoteRating := m.remote.UserRating
		runReport.Matched = append(runReport.Matched, MatchedEntry{
			Path:         m.local.RelPath,
			Query:        m.query,
			Method:       m.method,
			RemoteID:     m.remote.ID,
			RemotePath:   m.remote.Path,
			LocalRating:  localRating,
			RemoteRating: remoteRating,
		})

		if dryRun && !progress.Enabled() {
			log.Info(formatMatchedDryRunMessage(m, localRating, remoteRating))
		}

		ratingAction, newRating, isConflict := resolveRatingAction(localRating, remoteRating, prefer, options.SyncRatings)
		if isConflict {
			conflicts++
		}
		switch ratingAction {
		case ActionPush:
			pushed++
		case ActionPull:
			pulled++
		default:
			skipped++
		}

		playStatsAction, newPlayCount, newPlayed := resolvePlayStatsAction(m.local, m.remote, options.SyncPlayStats)

		starPrefer := prefer
		if options.PreferStars != "" {
			starPrefer = options.PreferStars
		}
		starAction, newStarred := resolveStarAction(m.local.Starred, m.remote.Starred != "", starPrefer, options.SyncStars)
		switch starAction {
		case ActionPush:
			pushed++
		case ActionPull:
			pulled++
		}

		localPlayed := m.local.Played
		remotePlayed := parseRemotePlayed(m.remote.Played)

		results = append(results, Result{
			Action:             ratingAction,
			Path:               m.local.RelPath,
			OldLocal:           localRating,
			OldRemote:          remoteRating,
			NewRating:          newRating,
			RemoteID:           m.remote.ID,
			PlayStatsAction:    playStatsAction,
			OldLocalPlayed:     localPlayed,
			OldRemotePlayed:    remotePlayed,
			OldRemotePlayCount: m.remote.PlayCount,
			NewPlayCount:       newPlayCount,
			NewPlayed:          newPlayed,
			StarAction:         starAction,
			OldLocalStarred:    m.local.Starred,
			OldRemoteStarred:   m.remote.Starred != "",
			NewStarred:         newStarred,
		})
	}

	for _, item := range report.unmatched {
		runReport.Unmatched = append(runReport.Unmatched, unresolvedEntry(item))
		if isNoResultUnmatched(item) {
			noResults++
		}
	}
	for _, item := range report.ambiguous {
		runReport.Ambiguous = append(runReport.Ambiguous, unresolvedEntry(item))
	}
	runReport.Warnings = append(runReport.Warnings, report.warnings...)
	runReport.Errors = append(runReport.Errors, report.errors...)

	if dryRun && !progress.Enabled() {
		for _, unmatched := range report.unmatched {
			log.Info(formatUnmatchedDryRunMessage(unmatched))
		}
		for _, ambiguous := range report.ambiguous {
			log.Info(formatAmbiguousDryRunMessage(ambiguous))
		}
	}

	if !progress.Enabled() {
		log.Info("Sync summary",
			"pushed", pushed, "pulled", pulled,
			"skipped", skipped, "conflicts_resolved", conflicts,
			"unmatched", len(report.unmatched),
			"no_results", noResults,
			"ambiguous", len(report.ambiguous),
			"warnings", len(report.warnings),
			"errors", len(report.errors),
			"dry_run", dryRun,
		)
		if noResults > 0 {
			log.Info("No-result summary", "count", noResults)
			for _, item := range report.unmatched {
				if !isNoResultUnmatched(item) {
					continue
				}
				log.Info("No-result song",
					"path", item.path,
					"reason", item.reason,
					"query", item.query,
					"local_path", item.localPath,
				)
			}
		}
		if len(report.unmatched) > 0 {
			log.Info("Unmatched summary", "count", len(report.unmatched))
			for _, item := range report.unmatched {
				log.Info("Unmatched song",
					"path", item.path,
					"reason", item.reason,
					"query", item.query,
					"remote_paths", formatCandidatePaths(item.candidates),
				)
			}
		}
		if len(report.ambiguous) > 0 {
			log.Info("Ambiguous summary", "count", len(report.ambiguous))
			for _, item := range report.ambiguous {
				log.Info("Ambiguous song",
					"path", item.path,
					"reason", item.reason,
					"query", item.query,
					"remote_paths", formatCandidatePaths(item.candidates),
				)
			}
		}
		if len(report.warnings) > 0 {
			log.Info("Warning summary", "count", len(report.warnings))
		}
	}
	if len(report.warnings) > 0 {
		for _, item := range report.warnings {
			log.Warn("Sync warning",
				"path", item.Path,
				"query", item.Query,
				"source", item.Source,
				"stage", item.Stage,
				"message", item.Message,
			)
		}
	}
	if !progress.Enabled() && len(report.errors) > 0 {
		log.Info("Error summary", "count", len(report.errors))
	}
	if len(report.errors) > 0 {
		for _, item := range report.errors {
			log.Error("Sync error",
				"path", item.Path,
				"query", item.Query,
				"source", item.Source,
				"stage", item.Stage,
				"message", item.Message,
			)
		}
	}

	runReport.Summary = ReportSummary{
		Pushed:            pushed,
		Pulled:            pulled,
		Skipped:           skipped,
		ConflictsResolved: conflicts,
		Matched:           len(report.matches),
		Unmatched:         len(report.unmatched),
		NoResults:         noResults,
		Ambiguous:         len(report.ambiguous),
		Warnings:          len(report.warnings),
		Errors:            len(report.errors),
		DryRun:            dryRun,
	}

	return &RunOutput{
		Results: results,
		Report:  runReport,
	}, nil
}

// resolveRatingAction determines the sync action and new rating value.
// Returns the action, new rating, and whether a conflict was encountered.
func resolveRatingAction(localRating, remoteRating int, prefer string, enabled bool) (Action, int, bool) {
	if !enabled {
		return ActionSkip, 0, false
	}
	switch {
	case localRating == 0 && remoteRating == 0:
		return ActionSkip, 0, false
	case localRating == remoteRating:
		return ActionSkip, 0, false
	case localRating > 0 && remoteRating == 0:
		return ActionPush, localRating, false
	case remoteRating > 0 && localRating == 0:
		return ActionPull, remoteRating, false
	default:
		if prefer == "local" {
			return ActionPush, localRating, true
		}
		return ActionPull, remoteRating, true
	}
}

// resolvePlayStatsAction determines the sync action for play count and last-played timestamp.
// Takes the higher play count and most recent timestamp.
func resolvePlayStatsAction(local *LocalFile, remote *navidrome.RemoteSong, enabled bool) (Action, int64, *time.Time) {
	if !enabled {
		return ActionSkip, 0, nil
	}
	localPlayed := local.Played
	remotePlayed := parseRemotePlayed(remote.Played)
	localMore := local.PlayCount > remote.PlayCount ||
		(localPlayed != nil && (remotePlayed == nil || localPlayed.After(*remotePlayed)))
	remoteMore := remote.PlayCount > local.PlayCount ||
		(remotePlayed != nil && (localPlayed == nil || remotePlayed.After(*localPlayed)))
	switch {
	case localMore:
		return ActionPush, local.PlayCount, localPlayed
	case remoteMore:
		return ActionPull, remote.PlayCount, remotePlayed
	default:
		return ActionSkip, 0, nil
	}
}

// resolveStarAction determines the sync action for starred/favorite state.
func resolveStarAction(localStarred, remoteStarred bool, prefer string, enabled bool) (Action, bool) {
	if !enabled || localStarred == remoteStarred {
		return ActionSkip, false
	}
	if prefer == "local" {
		return ActionPush, localStarred
	}
	return ActionPull, remoteStarred
}

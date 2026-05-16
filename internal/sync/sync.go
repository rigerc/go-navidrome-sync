package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/output"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/tag"
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

var (
	trackPrefixRe    = regexp.MustCompile(`^(\d+(?:-\d+)?)(?:\s*-\s*|\s+)`)
	songCleanRe      = regexp.MustCompile(`[^a-z0-9]+`)
	readLocalFile    = tag.ReadLocalFile
	audioFileExts    = map[string]struct{}{".mp3": {}, ".flac": {}}
	maxScanWorkers   = 32
	maxSearchHits    = 5
	maxMatchWorkers  = 4
	minSuffixScore   = 3
	progressInterval = 25
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

type unmatchedFile struct {
	path       string
	query      string
	reason     string
	localPath  string
	candidates []candidatePath
}

type candidatePath struct {
	raw        string
	normalized string
	score      int
}

type matchReport struct {
	matches   []match
	unmatched []unmatchedFile
	ambiguous []unmatchedFile
	warnings  []IssueEntry
	errors    []IssueEntry
}

type searchRateLimiter struct {
	interval time.Duration
	mu       sync.Mutex
	next     time.Time
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

		// --- rating action ---
		var ratingAction Action
		var newRating int
		if options.SyncRatings {
			switch {
			case localRating == 0 && remoteRating == 0:
				ratingAction = ActionSkip
			case localRating == remoteRating:
				ratingAction = ActionSkip
			case localRating > 0 && remoteRating == 0:
				ratingAction = ActionPush
				newRating = localRating
			case remoteRating > 0 && localRating == 0:
				ratingAction = ActionPull
				newRating = remoteRating
			default:
				conflicts++
				if prefer == "local" {
					ratingAction = ActionPush
					newRating = localRating
				} else {
					ratingAction = ActionPull
					newRating = remoteRating
				}
			}
		} else {
			ratingAction = ActionSkip
		}
		switch ratingAction {
		case ActionPush:
			pushed++
		case ActionPull:
			pulled++
		default:
			skipped++
		}

		// --- play-stats action (take max play count and most recent last played) ---
		localPlayed := m.local.Played
		remotePlayed := parseRemotePlayed(m.remote.Played)
		var playStatsAction Action
		var newPlayCount int64
		var newPlayed *time.Time
		if options.SyncPlayStats {
			localMore := m.local.PlayCount > m.remote.PlayCount ||
				(localPlayed != nil && (remotePlayed == nil || localPlayed.After(*remotePlayed)))
			remoteMore := m.remote.PlayCount > m.local.PlayCount ||
				(remotePlayed != nil && (localPlayed == nil || remotePlayed.After(*localPlayed)))

			switch {
			case localMore:
				playStatsAction = ActionPush
				newPlayCount = m.local.PlayCount
				newPlayed = localPlayed
			case remoteMore:
				playStatsAction = ActionPull
				newPlayCount = m.remote.PlayCount
				newPlayed = remotePlayed
			default:
				playStatsAction = ActionSkip
			}
		} else {
			playStatsAction = ActionSkip
		}

		localStarred := m.local.Starred
		remoteStarred := m.remote.Starred != ""
		starPrefer := prefer
		if options.PreferStars != "" {
			starPrefer = options.PreferStars
		}
		starAction := ActionSkip
		newStarred := false
		if options.SyncStars && localStarred != remoteStarred {
			if starPrefer == "local" {
				starAction = ActionPush
				newStarred = localStarred
			} else {
				starAction = ActionPull
				newStarred = remoteStarred
			}
		}
		switch starAction {
		case ActionPush:
			pushed++
		case ActionPull:
			pulled++
		}

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
			OldLocalStarred:    localStarred,
			OldRemoteStarred:   remoteStarred,
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

type match struct {
	local  *LocalFile
	remote *navidrome.RemoteSong
	method string
	query  string
}

type matchResult struct {
	index     int
	match     *match
	unmatched *unmatchedFile
	ambiguous *unmatchedFile
	warnings  []IssueEntry
	errors    []IssueEntry
}

func matchLocalToRemote(ctx context.Context, localFiles []*LocalFile, searcher songSearcher, remotePathPrefix string, searchInterval time.Duration, log *log.Logger, progress output.SyncProgress) (*matchReport, error) {
	sorted := make([]*LocalFile, len(localFiles))
	copy(sorted, localFiles)

	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Rating > 0 && sorted[j].Rating == 0 {
			return true
		}
		if sorted[i].Rating == 0 && sorted[j].Rating > 0 {
			return false
		}
		return false
	})

	workerCount := scanWorkerCount()
	if workerCount > maxMatchWorkers {
		workerCount = maxMatchWorkers
	}

	if progress.Enabled() {
		progress.StartMatching(len(sorted), workerCount)
	} else {
		log.Info("Starting remote matching",
			"total", len(sorted),
			"workers", workerCount,
			"remote_path_prefix", remotePathPrefix,
			"search_interval", searchInterval,
		)
	}

	jobs := make(chan int, workerCount*2)
	results := make(chan matchResult, workerCount*2)
	var startedSearches atomic.Int64
	var inFlightSearches atomic.Int64
	limiter := newSearchRateLimiter(searchInterval)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				lf := sorted[index]
				queries := searchQueries(lf)
				query := strings.Join(queries, " | ")
				log.Debug("Matching local file",
					"index", index+1,
					"total", len(sorted),
					"path", lf.RelPath,
					"query", query,
				)
				if len(queries) == 0 {
					log.Debug("Skipping remote search for local file with empty query", "path", lf.RelPath)
					results <- matchResult{
						index: index,
						unmatched: &unmatchedFile{
							path:      lf.RelPath,
							reason:    "empty search query",
							localPath: normalizePath(lf.RelPath, ""),
						},
					}
					continue
				}

				localPath := normalizePath(lf.RelPath, "")
				var bestUnmatched *unmatchedFile
				for _, query := range queries {
					searchNumber := startedSearches.Add(1)
					inFlight := inFlightSearches.Add(1)
					startedAt := time.Now()
					log.Debug("Starting remote song search",
						"index", index+1,
						"total", len(sorted),
						"path", lf.RelPath,
						"query", query,
						"search_number", searchNumber,
						"in_flight", inFlight,
					)
					if err := limiter.Wait(ctx); err != nil {
						inFlight = inFlightSearches.Add(-1)
						results <- matchResult{
							index: index,
							unmatched: &unmatchedFile{
								path:      lf.RelPath,
								query:     query,
								reason:    fmt.Sprintf("search canceled: %v", err),
								localPath: localPath,
							},
							errors: []IssueEntry{{
								Path:    lf.RelPath,
								Query:   query,
								Source:  "subsonic",
								Stage:   "search",
								Message: fmt.Sprintf("search canceled: %v", err),
							}},
						}
						continue
					}
					candidates, err := searcher.SearchSongsByTitle(ctx, query, maxSearchHits)
					searchDuration := time.Since(startedAt)
					inFlight = inFlightSearches.Add(-1)
					if err != nil {
						log.Warn("Failed to search remote song",
							"path", lf.RelPath,
							"query", query,
							"error", err,
							"duration", searchDuration,
							"in_flight", inFlight,
						)
						results <- matchResult{
							index: index,
							unmatched: &unmatchedFile{
								path:      lf.RelPath,
								query:     query,
								reason:    fmt.Sprintf("search failed: %v", err),
								localPath: localPath,
							},
							errors: []IssueEntry{{
								Path:    lf.RelPath,
								Query:   query,
								Source:  "subsonic",
								Stage:   "search",
								Message: err.Error(),
							}},
						}
						continue
					}
					log.Debug("Completed remote song search",
						"index", index+1,
						"total", len(sorted),
						"path", lf.RelPath,
						"query", query,
						"candidate_count", len(candidates),
						"duration", searchDuration,
						"in_flight", inFlight,
					)

					if len(candidates) == 0 {
						continue
					}

					candidatePaths := make([]candidatePath, 0, len(candidates))
					for _, candidate := range candidates {
						candidatePaths = append(candidatePaths, candidatePath{
							raw:        candidate.Path,
							normalized: normalizePath(candidate.Path, remotePathPrefix),
						})
					}
					selection := selectCandidate(lf, candidates, localPath, remotePathPrefix)

					if selection.match != nil {
						results <- matchResult{
							index: index,
							match: &match{local: lf, remote: selection.match, method: selection.method, query: query},
						}
						log.Debug("Matched remote song",
							"local", lf.RelPath,
							"remote", selection.match.Path,
							"query", query,
							"method", selection.method,
						)
						goto nextFile
					}
					if selection.reason != "" {
						results <- matchResult{
							index: index,
							ambiguous: &unmatchedFile{
								path:       lf.RelPath,
								query:      query,
								reason:     selection.reason,
								localPath:  localPath,
								candidates: selection.candidates,
							},
						}
						goto nextFile
					}

					bestUnmatched = &unmatchedFile{
						path:       lf.RelPath,
						query:      query,
						reason:     "candidate paths did not match local path",
						localPath:  localPath,
						candidates: candidatePaths,
					}
				}

				if bestUnmatched != nil {
					log.Debug("Remote candidates did not match local path",
						"path", lf.RelPath,
						"query", bestUnmatched.query,
						"candidate_count", len(bestUnmatched.candidates),
						"local_path", localPath,
					)
					results <- matchResult{
						index:     index,
						unmatched: bestUnmatched,
					}
					continue
				}

				if fallbackSearcher, ok := searcher.(fallbackSongSearcher); ok {
					fallbackQuery := fallbackSearchQuery(lf)
					if fallbackQuery != "" {
						log.Debug("Attempting native Navidrome fallback search",
							"path", lf.RelPath,
							"source", "native",
							"title", fallbackQuery,
						)
						candidates, err := fallbackSearcher.SearchSongsByTitleFallback(ctx, fallbackQuery, maxSearchHits)
						if err != nil {
							log.Warn("Native Navidrome fallback search failed",
								"path", lf.RelPath,
								"source", "native",
								"title", fallbackQuery,
								"error", err,
							)
							results <- matchResult{
								index: index,
								warnings: []IssueEntry{{
									Path:    lf.RelPath,
									Query:   fallbackQuery,
									Source:  "native",
									Stage:   "search_fallback",
									Message: err.Error(),
								}},
							}
						} else if len(candidates) > 0 {
							log.Debug("Native Navidrome fallback search returned candidates",
								"path", lf.RelPath,
								"source", "native",
								"title", fallbackQuery,
								"candidate_count", len(candidates),
							)
							selection := selectCandidate(lf, candidates, localPath, remotePathPrefix)
							if selection.match != nil {
								results <- matchResult{
									index: index,
									match: &match{local: lf, remote: selection.match, method: selection.method, query: fallbackQuery},
								}
								log.Debug("Matched remote song using native Navidrome fallback",
									"local", lf.RelPath,
									"remote", selection.match.Path,
									"source", "native",
									"title", fallbackQuery,
									"method", selection.method,
								)
								goto nextFile
							}
							if selection.reason != "" {
								results <- matchResult{
									index: index,
									ambiguous: &unmatchedFile{
										path:       lf.RelPath,
										query:      fallbackQuery,
										reason:     selection.reason,
										localPath:  localPath,
										candidates: selection.candidates,
									},
								}
								goto nextFile
							}
							bestUnmatched = &unmatchedFile{
								path:       lf.RelPath,
								query:      fallbackQuery,
								reason:     "candidate paths did not match local path",
								localPath:  localPath,
								candidates: candidateEntries(candidates, remotePathPrefix, 0),
							}
						} else {
							log.Debug("Native Navidrome fallback search returned no candidates",
								"path", lf.RelPath,
								"source", "native",
								"title", fallbackQuery,
							)
						}
					}
				}

				if bestUnmatched != nil {
					log.Debug("Native fallback candidates did not match local path",
						"path", lf.RelPath,
						"source", "native",
						"query", bestUnmatched.query,
						"candidate_count", len(bestUnmatched.candidates),
						"local_path", localPath,
					)
					results <- matchResult{
						index:     index,
						unmatched: bestUnmatched,
					}
					continue
				}

				log.Debug("Remote search returned no candidates",
					"path", lf.RelPath,
					"queries", queries,
				)
				results <- matchResult{
					index: index,
					unmatched: &unmatchedFile{
						path:      lf.RelPath,
						query:     strings.Join(queries, " | "),
						reason:    "search returned no song candidates",
						localPath: localPath,
					},
				}
			nextFile:
			}
		}()
	}

	go func() {
		for i := range sorted {
			jobs <- i
		}
		close(jobs)
	}()

	go func() {
		workers.Wait()
		close(results)
	}()

	matchesByIndex := make(map[int]*match, len(sorted))
	unmatchedByIndex := make(map[int]*unmatchedFile, len(sorted))
	ambiguousByIndex := make(map[int]*unmatchedFile, len(sorted))
	orderedWarnings := make([]IssueEntry, 0)
	orderedErrors := make([]IssueEntry, 0)
	processed := 0
	for result := range results {
		processed++
		if result.match != nil {
			matchesByIndex[result.index] = result.match
		}
		if result.unmatched != nil {
			unmatchedByIndex[result.index] = result.unmatched
		}
		if result.ambiguous != nil {
			ambiguousByIndex[result.index] = result.ambiguous
		}
		if len(result.warnings) > 0 {
			orderedWarnings = append(orderedWarnings, result.warnings...)
		}
		if len(result.errors) > 0 {
			orderedErrors = append(orderedErrors, result.errors...)
		}
		if progress.Enabled() {
			progress.UpdateMatching(processed, len(sorted), len(matchesByIndex), len(unmatchedByIndex), len(ambiguousByIndex))
		} else if processed%progressInterval == 0 || processed == len(sorted) {
			log.Info("Remote matching progress",
				"processed", processed,
				"total", len(sorted),
				"matched", len(matchesByIndex),
				"unmatched", len(unmatchedByIndex),
				"ambiguous", len(ambiguousByIndex),
			)
		}
	}

	orderedMatches := make([]match, 0, len(matchesByIndex))
	orderedUnmatched := make([]unmatchedFile, 0, len(unmatchedByIndex))
	orderedAmbiguous := make([]unmatchedFile, 0, len(ambiguousByIndex))
	for i := range sorted {
		if result, ok := matchesByIndex[i]; ok {
			orderedMatches = append(orderedMatches, *result)
			continue
		}
		if result, ok := ambiguousByIndex[i]; ok {
			orderedAmbiguous = append(orderedAmbiguous, *result)
			continue
		}
		if result, ok := unmatchedByIndex[i]; ok {
			orderedUnmatched = append(orderedUnmatched, *result)
		}
	}

	return &matchReport{
		matches:   orderedMatches,
		unmatched: orderedUnmatched,
		ambiguous: orderedAmbiguous,
		warnings:  orderedWarnings,
		errors:    orderedErrors,
	}, nil
}

type selection struct {
	match      *navidrome.RemoteSong
	method     string
	reason     string
	candidates []candidatePath
}

func selectCandidate(
	localFile *LocalFile,
	candidates []*navidrome.RemoteSong,
	localPath string,
	remotePathPrefix string,
) selection {
	if localFile.MusicBrainzID != "" {
		matches := make([]*navidrome.RemoteSong, 0, 1)
		for _, candidate := range candidates {
			if strings.EqualFold(candidate.MusicBrainzID, localFile.MusicBrainzID) {
				matches = append(matches, candidate)
			}
		}
		if result, ok := selectUniqueCandidate(matches, "musicbrainz_id", "multiple candidates matched MusicBrainz ID", remotePathPrefix); ok {
			return result
		}
	}

	pathMatches := make([]*navidrome.RemoteSong, 0, 1)
	for _, candidate := range candidates {
		if normalizePath(candidate.Path, remotePathPrefix) == localPath {
			pathMatches = append(pathMatches, candidate)
		}
	}
	if result, ok := selectUniqueCandidate(pathMatches, "path", "multiple candidates matched local path", remotePathPrefix); ok {
		return result
	}

	if result, ok := bestSuffixPathCandidate(localFile, candidates, localPath, remotePathPrefix); ok {
		return result
	}

	return selection{}
}

func selectUniqueCandidate(
	matches []*navidrome.RemoteSong,
	method string,
	ambiguityReason string,
	remotePathPrefix string,
) (selection, bool) {
	switch len(matches) {
	case 0:
		return selection{}, false
	case 1:
		return selection{match: matches[0], method: method}, true
	default:
		return selection{
			reason:     ambiguityReason,
			candidates: candidateEntries(matches, remotePathPrefix, 0),
		}, true
	}
}

func newSearchRateLimiter(interval time.Duration) *searchRateLimiter {
	return &searchRateLimiter{interval: interval}
}

func (l *searchRateLimiter) Wait(ctx context.Context) error {
	if l == nil || l.interval <= 0 {
		return nil
	}

	l.mu.Lock()
	now := time.Now()
	if l.next.IsZero() || !now.Before(l.next) {
		l.next = now.Add(l.interval)
		l.mu.Unlock()
		return nil
	}

	waitUntil := l.next
	l.next = l.next.Add(l.interval)
	l.mu.Unlock()

	timer := time.NewTimer(time.Until(waitUntil))
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func searchQuery(localFile *LocalFile) string {
	queries := searchQueries(localFile)
	if len(queries) == 0 {
		return ""
	}
	return queries[0]
}

func searchQueries(localFile *LocalFile) []string {
	pathArtist, pathAlbum, pathTitle := pathMetadata(localFile.RelPath)

	title := firstNonEmpty(localFile.Title, pathTitle)
	artist := firstNonEmpty(localFile.Artist, pathArtist)
	album := firstNonEmpty(localFile.Album, pathAlbum)

	candidates := []string{
		joinQueryParts(title, artist, album),
		joinQueryParts(title, artist),
		joinQueryParts(title),
	}

	queries := make([]string, 0, len(candidates))
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			continue
		}
		if _, ok := seen[candidate]; ok {
			continue
		}
		seen[candidate] = struct{}{}
		queries = append(queries, candidate)
	}
	return queries
}

func fallbackSearchQuery(localFile *LocalFile) string {
	pathArtist, _, pathTitle := pathMetadata(localFile.RelPath)
	return firstNonEmpty(localFile.Title, pathTitle, pathArtist)
}

func isNoResultUnmatched(item unmatchedFile) bool {
	return item.reason == "search returned no song candidates"
}

func pathMetadata(relPath string) (artist string, album string, title string) {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) == 0 {
		return "", "", ""
	}

	title = trackTitleFromPathPart(parts[len(parts)-1])
	if len(parts) >= 2 {
		album = parts[len(parts)-2]
	}
	if len(parts) >= 3 {
		artist = parts[len(parts)-3]
	}
	return artist, album, title
}

func joinQueryParts(parts ...string) string {
	filtered := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, " ")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizePath(path string, remotePathPrefix string) string {
	path = strings.TrimSpace(path)
	path = strings.TrimPrefix(path, "./")
	path = filepath.ToSlash(path)
	path = strings.TrimPrefix(path, "/")
	if remotePathPrefix != "" {
		prefix := filepath.ToSlash(strings.TrimSpace(remotePathPrefix))
		prefix = strings.TrimPrefix(prefix, "/")
		prefix = strings.TrimSuffix(prefix, "/")
		if prefix != "" {
			path = strings.TrimPrefix(path, prefix+"/")
			if path == prefix {
				path = ""
			}
		}
	}
	return strings.ToLower(path)
}

func bestSuffixPathCandidate(
	localFile *LocalFile,
	candidates []*navidrome.RemoteSong,
	localPath string,
	remotePathPrefix string,
) (selection, bool) {
	bestScore := 0
	var bestCandidate *navidrome.RemoteSong
	tied := make([]*navidrome.RemoteSong, 0, 2)

	for _, candidate := range candidates {
		score := suffixPathScore(localFile, localPath, normalizePath(candidate.Path, remotePathPrefix))
		if score < minSuffixScore {
			continue
		}
		if score > bestScore {
			bestScore = score
			bestCandidate = candidate
			tied = []*navidrome.RemoteSong{candidate}
			continue
		}
		if score == bestScore {
			tied = append(tied, candidate)
		}
	}

	if bestCandidate == nil {
		return selection{}, false
	}
	if len(tied) > 1 {
		return selection{
			reason:     "multiple candidates tied for suffix path match",
			candidates: candidateEntries(tied, remotePathPrefix, bestScore),
		}, true
	}
	return selection{match: bestCandidate, method: "path_suffix"}, true
}

func candidateEntries(candidates []*navidrome.RemoteSong, remotePathPrefix string, score int) []candidatePath {
	entries := make([]candidatePath, 0, len(candidates))
	for _, candidate := range candidates {
		entries = append(entries, candidatePath{
			raw:        candidate.Path,
			normalized: normalizePath(candidate.Path, remotePathPrefix),
			score:      score,
		})
	}
	return entries
}

func suffixPathScore(localFile *LocalFile, localPath string, remotePath string) int {
	localParts := strings.Split(localPath, "/")
	remoteParts := strings.Split(remotePath, "/")
	if len(localParts) == 0 || len(remoteParts) == 0 {
		return 0
	}

	localTitle := localFile.Title
	if localTitle == "" {
		localTitle = trackTitleFromPathPart(localParts[len(localParts)-1])
	}
	remoteTitle := trackTitleFromPathPart(remoteParts[len(remoteParts)-1])
	if !songsMatch(localTitle, remoteTitle) {
		return 0
	}

	score := 1
	for i, j := len(localParts)-2, len(remoteParts)-2; i >= 0 && j >= 0; i, j = i-1, j-1 {
		if localParts[i] != remoteParts[j] {
			break
		}
		score++
	}

	return score
}

func trackTitleFromPathPart(pathPart string) string {
	name := strings.TrimSuffix(pathPart, filepath.Ext(pathPart))
	if matches := trackPrefixRe.FindStringSubmatch(name); len(matches) > 0 {
		name = strings.TrimSpace(strings.TrimPrefix(name, matches[0]))
	}
	return name
}

func matchesByPath(paths []candidatePath, target string) (string, bool) {
	for _, path := range paths {
		if path.normalized == target {
			return path.normalized, true
		}
	}
	return "", false
}

func formatCandidatePaths(candidates []candidatePath) []string {
	formatted := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		line := fmt.Sprintf("%s => %s", candidate.raw, candidate.normalized)
		if candidate.score > 0 {
			line = fmt.Sprintf("%s (score=%d)", line, candidate.score)
		}
		formatted = append(formatted, line)
	}
	return formatted
}

func formatMatchedDryRunMessage(m match, localRating int, remoteRating int) string {
	lines := []string{
		"[DRY-RUN] Matched remote song",
		fmt.Sprintf("  local path: %s", m.local.RelPath),
		fmt.Sprintf("  query: %s", m.query),
		fmt.Sprintf("  method: %s", m.method),
		fmt.Sprintf("  remote path: %s", m.remote.Path),
	}
	if m.remote.MusicBrainzID != "" {
		lines = append(lines, fmt.Sprintf("  remote MBID: %s", m.remote.MusicBrainzID))
	}
	lines = append(lines,
		fmt.Sprintf("  local rating: %d", localRating),
		fmt.Sprintf("  remote rating: %d", remoteRating),
	)
	return strings.Join(lines, "\n")
}

func formatUnmatchedDryRunMessage(unmatched unmatchedFile) string {
	lines := []string{
		"[DRY-RUN] No remote match found",
		fmt.Sprintf("  local path: %s", unmatched.path),
	}
	if unmatched.query != "" {
		lines = append(lines, fmt.Sprintf("  query: %s", unmatched.query))
	}
	lines = append(lines,
		fmt.Sprintf("  reason: %s", unmatched.reason),
		fmt.Sprintf("  normalized local path: %s", unmatched.localPath),
	)
	if len(unmatched.candidates) == 0 {
		lines = append(lines, "  remote candidates: <none>")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "  remote candidates:")
	for _, candidate := range unmatched.candidates {
		lines = append(lines, fmt.Sprintf("    - raw: %s", candidate.raw))
		lines = append(lines, fmt.Sprintf("      normalized: %s", candidate.normalized))
	}
	return strings.Join(lines, "\n")
}

func formatAmbiguousDryRunMessage(ambiguous unmatchedFile) string {
	lines := []string{
		"[DRY-RUN] Ambiguous remote match",
		fmt.Sprintf("  local path: %s", ambiguous.path),
	}
	if ambiguous.query != "" {
		lines = append(lines, fmt.Sprintf("  query: %s", ambiguous.query))
	}
	lines = append(lines,
		fmt.Sprintf("  reason: %s", ambiguous.reason),
		fmt.Sprintf("  normalized local path: %s", ambiguous.localPath),
	)
	if len(ambiguous.candidates) == 0 {
		lines = append(lines, "  remote candidates: <none>")
		return strings.Join(lines, "\n")
	}

	lines = append(lines, "  remote candidates:")
	for _, candidate := range ambiguous.candidates {
		line := fmt.Sprintf("    - raw: %s", candidate.raw)
		if candidate.score > 0 {
			line = fmt.Sprintf("%s (score=%d)", line, candidate.score)
		}
		lines = append(lines, line)
		lines = append(lines, fmt.Sprintf("      normalized: %s", candidate.normalized))
	}
	return strings.Join(lines, "\n")
}

func songsMatch(a, b string) bool {
	if strings.EqualFold(a, b) {
		return true
	}
	clean := func(s string) string {
		s = strings.ToLower(s)
		s = songCleanRe.ReplaceAllString(s, " ")
		s = strings.Join(strings.Fields(s), " ")
		return s
	}
	return clean(a) == clean(b)
}

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

func unresolvedEntry(item unmatchedFile) UnresolvedEntry {
	entry := UnresolvedEntry{
		Path:      item.path,
		Query:     item.query,
		Reason:    item.reason,
		LocalPath: item.localPath,
	}
	if len(item.candidates) == 0 {
		return entry
	}

	entry.Candidates = make([]CandidateEntry, 0, len(item.candidates))
	for _, candidate := range item.candidates {
		entry.Candidates = append(entry.Candidates, CandidateEntry{
			RawPath:        candidate.raw,
			NormalizedPath: candidate.normalized,
			Score:          candidate.score,
		})
	}
	return entry
}

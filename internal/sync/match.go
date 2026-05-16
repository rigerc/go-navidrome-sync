package sync

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/output"
)

var (
	trackPrefixRe   = regexp.MustCompile(`^(\d+(?:-\d+)?)(?:\s*-\s*|\s+)`)
	songCleanRe     = regexp.MustCompile(`[^a-z0-9]+`)
	maxSearchHits   = 5
	maxMatchWorkers = 4
	minSuffixScore  = 3
)

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

type selection struct {
	match      *navidrome.RemoteSong
	method     string
	reason     string
	candidates []candidatePath
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
					sel := selectCandidate(lf, candidates, localPath, remotePathPrefix)

					if sel.match != nil {
						results <- matchResult{
							index: index,
							match: &match{local: lf, remote: sel.match, method: sel.method, query: query},
						}
						log.Debug("Matched remote song",
							"local", lf.RelPath,
							"remote", sel.match.Path,
							"query", query,
							"method", sel.method,
						)
						goto nextFile
					}
					if sel.reason != "" {
						results <- matchResult{
							index: index,
							ambiguous: &unmatchedFile{
								path:       lf.RelPath,
								query:      query,
								reason:     sel.reason,
								localPath:  localPath,
								candidates: sel.candidates,
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
							sel := selectCandidate(lf, candidates, localPath, remotePathPrefix)
							if sel.match != nil {
								results <- matchResult{
									index: index,
									match: &match{local: lf, remote: sel.match, method: sel.method, query: fallbackQuery},
								}
								log.Debug("Matched remote song using native Navidrome fallback",
									"local", lf.RelPath,
									"remote", sel.match.Path,
									"source", "native",
									"title", fallbackQuery,
									"method", sel.method,
								)
								goto nextFile
							}
							if sel.reason != "" {
								results <- matchResult{
									index: index,
									ambiguous: &unmatchedFile{
										path:       lf.RelPath,
										query:      fallbackQuery,
										reason:     sel.reason,
										localPath:  localPath,
										candidates: sel.candidates,
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
	orderedWarnings := make([]IssueEntry, 0, len(sorted))
	orderedErrors := make([]IssueEntry, 0, len(sorted))
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

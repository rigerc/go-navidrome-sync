package sync

import (
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
	"github.com/rigerc/go-navidrome-ratings-sync/internal/tag"
)

type Action int

const (
	ActionPush Action = iota
	ActionPull
	ActionSkip
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

type Result struct {
	Action    Action
	Path      string
	OldLocal  int
	OldRemote int
	NewRating int
	RemoteID  string
}

type LocalFile struct {
	RelPath string
	*tag.LocalFile
}

type scanJob struct {
	path    string
	relPath string
}

var (
	discPrefixRe     = regexp.MustCompile(`^\d+-`)
	trackDashRe      = regexp.MustCompile(`^(\d+(?:-\d+)?)(?:\s*-\s+|\s+)`)
	songCleanRe      = regexp.MustCompile(`[^a-z0-9]+`)
	readLocalFile    = tag.ReadLocalFile
	audioFileExts    = map[string]struct{}{".mp3": {}, ".flac": {}}
	maxScanWorkers   = 32
	maxSearchHits    = 5
	maxMatchWorkers  = 4
	minSuffixScore   = 3
	progressInterval = 25
)

type songSearcher interface {
	SearchSongsByTitle(title string, limit int) ([]*navidrome.RemoteSong, error)
}

type unmatchedFile struct {
	path           string
	query          string
	reason         string
	localPath      string
	localCanonical string
	candidates     []candidatePath
}

type candidatePath struct {
	raw        string
	normalized string
}

type matchReport struct {
	matches   []match
	unmatched []unmatchedFile
}

func ScanLocalFiles(musicPath string, log *log.Logger) ([]*LocalFile, error) {
	workerCount := scanWorkerCount()
	jobs := make(chan scanJob, workerCount*4)
	results := make(chan *LocalFile, workerCount*4)

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for job := range jobs {
				lf, err := readLocalFile(job.path)
				if err != nil {
					log.Warn("Failed to read file metadata", "path", job.relPath, "error", err)
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
	}()

	var files []*LocalFile
	for file := range results {
		files = append(files, file)
	}

	if err := <-errs; err != nil {
		return nil, fmt.Errorf("failed to walk music path: %w", err)
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].RelPath < files[j].RelPath
	})

	log.Info("Scanned local files", "count", len(files))
	return files, nil
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
	musicPath string,
	localFiles []*LocalFile,
	searcher songSearcher,
	remotePathPrefix string,
	prefer string,
	dryRun bool,
	log *log.Logger,
) ([]Result, error) {
	report, err := matchLocalToRemote(localFiles, searcher, remotePathPrefix, log)
	if err != nil {
		return nil, err
	}

	var results []Result

	pushed := 0
	pulled := 0
	skipped := 0
	conflicts := 0

	for _, m := range report.matches {
		localRating := m.local.Rating
		remoteRating := m.remote.UserRating

		if dryRun {
			log.Info(formatMatchedDryRunMessage(m, localRating, remoteRating))
		}

		if localRating == 0 && remoteRating == 0 {
			skipped++
			results = append(results, Result{Action: ActionSkip, Path: m.local.RelPath})
			continue
		}

		if localRating == remoteRating {
			skipped++
			results = append(results, Result{Action: ActionSkip, Path: m.local.RelPath})
			continue
		}

		if localRating > 0 && remoteRating == 0 {
			results = append(results, Result{
				Action: ActionPush, Path: m.local.RelPath,
				OldLocal: localRating, OldRemote: remoteRating, NewRating: localRating,
				RemoteID: m.remote.ID,
			})
			pushed++
			continue
		}

		if remoteRating > 0 && localRating == 0 {
			results = append(results, Result{
				Action: ActionPull, Path: m.local.RelPath,
				OldLocal: localRating, OldRemote: remoteRating, NewRating: remoteRating,
				RemoteID: m.remote.ID,
			})
			pulled++
			continue
		}

		conflicts++
		var chosenRating int
		var action Action
		if prefer == "local" {
			chosenRating = localRating
			action = ActionPush
		} else {
			chosenRating = remoteRating
			action = ActionPull
		}

		results = append(results, Result{
			Action: action, Path: m.local.RelPath,
			OldLocal: localRating, OldRemote: remoteRating, NewRating: chosenRating,
			RemoteID: m.remote.ID,
		})

		if action == ActionPush {
			pushed++
		} else {
			pulled++
		}
	}

	if dryRun {
		for _, unmatched := range report.unmatched {
			log.Info(formatUnmatchedDryRunMessage(unmatched))
		}
	}

	log.Info("Sync summary",
		"pushed", pushed, "pulled", pulled,
		"skipped", skipped, "conflicts_resolved", conflicts,
		"unmatched", len(report.unmatched),
		"dry_run", dryRun,
	)
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

	return results, nil
}

type match struct {
	local  *LocalFile
	remote *navidrome.RemoteSong
	method string
}

type matchResult struct {
	index     int
	match     *match
	unmatched *unmatchedFile
}

func matchLocalToRemote(localFiles []*LocalFile, searcher songSearcher, remotePathPrefix string, log *log.Logger) (*matchReport, error) {
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

	log.Info("Starting remote matching",
		"total", len(sorted),
		"workers", workerCount,
		"remote_path_prefix", remotePathPrefix,
	)

	jobs := make(chan int, workerCount*2)
	results := make(chan matchResult, workerCount*2)
	var startedSearches atomic.Int64
	var inFlightSearches atomic.Int64

	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for index := range jobs {
				lf := sorted[index]
				query := searchQuery(lf)
				log.Debug("Matching local file",
					"index", index+1,
					"total", len(sorted),
					"path", lf.RelPath,
					"query", query,
				)
				if query == "" {
					log.Debug("Skipping remote search for local file with empty query", "path", lf.RelPath)
					results <- matchResult{
						index: index,
						unmatched: &unmatchedFile{
							path:           lf.RelPath,
							reason:         "empty search query",
							localPath:      normalizePath(lf.RelPath, ""),
							localCanonical: canonicalizePath(normalizePath(lf.RelPath, "")),
						},
					}
					continue
				}

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
				candidates, err := searcher.SearchSongsByTitle(query, maxSearchHits)
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
							path:           lf.RelPath,
							query:          query,
							reason:         fmt.Sprintf("search failed: %v", err),
							localPath:      normalizePath(lf.RelPath, ""),
							localCanonical: canonicalizePath(normalizePath(lf.RelPath, "")),
						},
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

				localPath := normalizePath(lf.RelPath, "")
				localCanonicalPath := canonicalizePath(localPath)
				candidatePaths := make([]candidatePath, 0, len(candidates))
				bestCandidate, method := selectCandidate(lf, candidates, localPath, localCanonicalPath, remotePathPrefix)
				for _, candidate := range candidates {
					normalizedRemotePath := normalizePath(candidate.Path, remotePathPrefix)
					candidatePaths = append(candidatePaths, candidatePath{
						raw:        candidate.Path,
						normalized: canonicalizePath(normalizedRemotePath),
					})
				}

				if bestCandidate != nil {
					results <- matchResult{
						index: index,
						match: &match{local: lf, remote: bestCandidate, method: method},
					}
					log.Debug("Matched remote song",
						"local", lf.RelPath,
						"remote", bestCandidate.Path,
						"query", query,
						"method", method,
					)
					continue
				}

				if len(candidatePaths) == 0 {
					log.Debug("Remote search returned no candidates",
						"path", lf.RelPath,
						"query", query,
					)
					results <- matchResult{
						index: index,
						unmatched: &unmatchedFile{
							path:           lf.RelPath,
							query:          query,
							reason:         "search returned no song candidates",
							localPath:      localPath,
							localCanonical: localCanonicalPath,
						},
					}
					continue
				}

				if _, ok := matchesByPath(candidatePaths, localPath); !ok {
					log.Debug("Remote candidates did not match local path",
						"path", lf.RelPath,
						"query", query,
						"candidate_count", len(candidatePaths),
						"local_path", localPath,
						"local_canonical_path", localCanonicalPath,
					)
					results <- matchResult{
						index: index,
						unmatched: &unmatchedFile{
							path:           lf.RelPath,
							query:          query,
							reason:         "candidate paths did not match local path",
							localPath:      localPath,
							localCanonical: localCanonicalPath,
							candidates:     candidatePaths,
						},
					}
				}
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
	processed := 0
	for result := range results {
		processed++
		if result.match != nil {
			matchesByIndex[result.index] = result.match
		}
		if result.unmatched != nil {
			unmatchedByIndex[result.index] = result.unmatched
		}
		if processed%progressInterval == 0 || processed == len(sorted) {
			log.Info("Remote matching progress",
				"processed", processed,
				"total", len(sorted),
				"matched", len(matchesByIndex),
				"unmatched", len(unmatchedByIndex),
			)
		}
	}

	orderedMatches := make([]match, 0, len(matchesByIndex))
	orderedUnmatched := make([]unmatchedFile, 0, len(unmatchedByIndex))
	for i := range sorted {
		if result, ok := matchesByIndex[i]; ok {
			orderedMatches = append(orderedMatches, *result)
			continue
		}
		if result, ok := unmatchedByIndex[i]; ok {
			orderedUnmatched = append(orderedUnmatched, *result)
		}
	}

	return &matchReport{matches: orderedMatches, unmatched: orderedUnmatched}, nil
}

func selectCandidate(
	localFile *LocalFile,
	candidates []*navidrome.RemoteSong,
	localPath string,
	localCanonicalPath string,
	remotePathPrefix string,
) (*navidrome.RemoteSong, string) {
	if localFile.MusicBrainzID != "" {
		for _, candidate := range candidates {
			if strings.EqualFold(candidate.MusicBrainzID, localFile.MusicBrainzID) {
				return candidate, "musicbrainz_id"
			}
		}
	}

	for _, candidate := range candidates {
		if normalizePath(candidate.Path, remotePathPrefix) == localPath {
			return candidate, "path"
		}
	}

	for _, candidate := range candidates {
		if canonicalizePath(normalizePath(candidate.Path, remotePathPrefix)) == localCanonicalPath {
			return candidate, "path_canonical"
		}
	}

	if candidate := bestSuffixPathCandidate(localFile, candidates, localCanonicalPath, remotePathPrefix); candidate != nil {
		return candidate, "path_suffix"
	}

	return nil, ""
}

func searchQuery(localFile *LocalFile) string {
	var parts []string

	if localFile.Title != "" {
		parts = append(parts, localFile.Title)
	}
	if localFile.Artist != "" {
		parts = append(parts, localFile.Artist)
	}
	if localFile.Album != "" {
		parts = append(parts, localFile.Album)
	}
	if len(parts) > 0 {
		return strings.Join(parts, " ")
	}

	base := filepath.Base(localFile.RelPath)
	return strings.TrimSuffix(base, filepath.Ext(base))
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

func canonicalizePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		parts[i] = canonicalizePathPart(part)
	}
	return strings.Join(parts, "/")
}

func canonicalizePathPart(part string) string {
	matches := trackDashRe.FindStringSubmatch(part)
	if len(matches) < 2 {
		return part
	}

	prefix := canonicalizeTrackPrefix(matches[1])
	remainder := strings.TrimSpace(strings.TrimPrefix(part, matches[0]))
	if remainder == "" {
		return prefix
	}
	return prefix + " - " + remainder
}

func canonicalizeTrackPrefix(prefix string) string {
	segments := strings.Split(prefix, "-")
	if len(segments) == 1 {
		segment := strings.TrimLeft(segments[0], "0")
		if segment == "" {
			segment = "0"
		}
		return segment
	}
	for i, segment := range segments {
		segment = strings.TrimLeft(segment, "0")
		if segment == "" {
			segment = "0"
		}
		segments[i] = segment
	}
	if len(segments) == 2 && segments[0] == "1" {
		return segments[1]
	}
	return strings.Join(segments, "-")
}

func bestSuffixPathCandidate(
	localFile *LocalFile,
	candidates []*navidrome.RemoteSong,
	localCanonicalPath string,
	remotePathPrefix string,
) *navidrome.RemoteSong {
	bestScore := 0
	var bestCandidate *navidrome.RemoteSong
	tied := false

	for _, candidate := range candidates {
		score := suffixPathScore(localFile, localCanonicalPath, canonicalizePath(normalizePath(candidate.Path, remotePathPrefix)))
		if score < minSuffixScore {
			continue
		}
		if score > bestScore {
			bestScore = score
			bestCandidate = candidate
			tied = false
			continue
		}
		if score == bestScore {
			tied = true
		}
	}

	if tied {
		return nil
	}
	return bestCandidate
}

func suffixPathScore(localFile *LocalFile, localCanonicalPath string, remoteCanonicalPath string) int {
	localParts := strings.Split(localCanonicalPath, "/")
	remoteParts := strings.Split(remoteCanonicalPath, "/")
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
	if matches := trackDashRe.FindStringSubmatch(name); len(matches) > 0 {
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
		formatted = append(formatted, fmt.Sprintf("%s => %s", candidate.raw, candidate.normalized))
	}
	return formatted
}

func formatMatchedDryRunMessage(m match, localRating int, remoteRating int) string {
	lines := []string{
		"[DRY-RUN] Matched remote song",
		fmt.Sprintf("  local path: %s", m.local.RelPath),
		fmt.Sprintf("  query: %s", searchQuery(m.local)),
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
		fmt.Sprintf("  canonical local path: %s", unmatched.localCanonical),
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
	musicPath string,
	results []Result,
	client *navidrome.Client,
	dryRun bool,
	log *log.Logger,
) error {
	for _, r := range results {
		switch r.Action {
		case ActionSkip:
			continue

		case ActionPush:
			if dryRun {
				log.Info("[DRY-RUN] Would push rating to Navidrome",
					"path", r.Path, "rating", r.NewRating)
			} else {
				if err := client.SetRating(r.RemoteID, r.NewRating); err != nil {
					log.Error("Failed to push rating",
						"path", r.Path, "rating", r.NewRating, "error", err)
					continue
				}
				log.Info("Pushed rating to Navidrome",
					"path", r.Path, "rating", r.NewRating)
			}

		case ActionPull:
			fullPath := filepath.Join(musicPath, r.Path)
			if dryRun {
				log.Info("[DRY-RUN] Would write rating to local file",
					"path", r.Path, "rating", r.NewRating)
			} else {
				if err := tag.WriteRating(fullPath, r.NewRating); err != nil {
					log.Error("Failed to write rating",
						"path", r.Path, "rating", r.NewRating, "error", err)
					continue
				}
				log.Info("Wrote rating to local file",
					"path", r.Path, "rating", r.NewRating)
			}
		}
	}

	return nil
}

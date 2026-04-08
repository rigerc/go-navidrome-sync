package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/charmbracelet/log"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/navidrome"
	"github.com/rigerc/go-navidrome-ratings-sync/internal/tag"
)

func TestScanLocalFiles_SortsFiltersAndKeepsUnreadableFiles(t *testing.T) {
	root := t.TempDir()

	mustWriteFile(t, filepath.Join(root, "artist-b", "02-track.flac"))
	mustWriteFile(t, filepath.Join(root, "artist-a", "01-track.mp3"))
	mustWriteFile(t, filepath.Join(root, "artist-a", "cover.jpg"))

	original := readLocalFile
	t.Cleanup(func() {
		readLocalFile = original
	})

	readLocalFile = func(path string) (*tag.LocalFile, error) {
		switch filepath.Base(path) {
		case "01-track.mp3":
			return &tag.LocalFile{Rating: 4, Artist: "artist-a"}, nil
		case "02-track.flac":
			return nil, fmt.Errorf("bad tags")
		default:
			t.Fatalf("unexpected file read: %s", path)
			return nil, nil
		}
	}

	files, err := ScanLocalFiles(root, testLogger())
	if err != nil {
		t.Fatalf("ScanLocalFiles() error = %v", err)
	}

	if len(files) != 2 {
		t.Fatalf("len(files) = %d, want 2", len(files))
	}
	if files[0].RelPath != "artist-a/01-track.mp3" {
		t.Fatalf("files[0].RelPath = %q, want %q", files[0].RelPath, "artist-a/01-track.mp3")
	}
	if files[1].RelPath != "artist-b/02-track.flac" {
		t.Fatalf("files[1].RelPath = %q, want %q", files[1].RelPath, "artist-b/02-track.flac")
	}
	if files[0].Rating != 4 {
		t.Fatalf("files[0].Rating = %d, want 4", files[0].Rating)
	}
	if files[1].LocalFile == nil {
		t.Fatal("files[1].LocalFile is nil, want zero-value metadata")
	}
	if files[1].Rating != 0 {
		t.Fatalf("files[1].Rating = %d, want 0", files[1].Rating)
	}
}

func TestScanLocalFiles_UsesMultipleWorkers(t *testing.T) {
	root := t.TempDir()

	for i := range 16 {
		mustWriteFile(t, filepath.Join(root, fmt.Sprintf("track-%02d.mp3", i)))
	}

	original := readLocalFile
	t.Cleanup(func() {
		readLocalFile = original
	})

	var current int64
	var peak int64
	var peakMu sync.Mutex

	readLocalFile = func(path string) (*tag.LocalFile, error) {
		active := atomic.AddInt64(&current, 1)
		peakMu.Lock()
		if active > peak {
			peak = active
		}
		peakMu.Unlock()

		time.Sleep(20 * time.Millisecond)
		atomic.AddInt64(&current, -1)
		return &tag.LocalFile{Title: filepath.Base(path)}, nil
	}

	if _, err := ScanLocalFiles(root, testLogger()); err != nil {
		t.Fatalf("ScanLocalFiles() error = %v", err)
	}

	if peak < 2 {
		t.Fatalf("peak concurrent reads = %d, want at least 2", peak)
	}
}

func TestSongsMatch_NormalizesPunctuation(t *testing.T) {
	if !songsMatch("Track Title!!", "track   title") {
		t.Fatal("songsMatch() = false, want true")
	}
}

func TestMatchLocalToRemote_UsesTitleQueryAndPathMatch(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Track Artist Track Album": {
				{ID: "wrong", Path: "other/track.mp3", UserRating: 1},
				{ID: "match", Path: "artist/album/track.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{
		{
			RelPath:   "artist/album/track.mp3",
			LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Track Artist", Album: "Track Album"},
		},
	}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].remote.ID != "match" {
		t.Fatalf("report.matches[0].remote.ID = %q, want %q", report.matches[0].remote.ID, "match")
	}
	if report.matches[0].method != "path" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "path")
	}
	if len(report.unmatched) != 0 {
		t.Fatalf("len(report.unmatched) = %d, want 0", len(report.unmatched))
	}
	if got, want := searcher.queries, []string{"Track Title Track Artist Track Album"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %v, want %v", got, want)
	}
	if got, want := searcher.limits, []int{maxSearchHits}; !reflect.DeepEqual(got, want) {
		t.Fatalf("limits = %v, want %v", got, want)
	}
}

func TestMatchLocalToRemote_FallsBackToFilenameTitle(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"track title artist": {
				{ID: "match", Path: "artist/01-track title.flac", UserRating: 3},
			},
		},
	}

	localFiles := []*LocalFile{
		{
			RelPath:   "artist/01-track title.flac",
			LocalFile: &tag.LocalFile{},
		},
	}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if got, want := searcher.queries, []string{"track title artist"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %v, want %v", got, want)
	}
}

func TestMatchLocalToRemote_DoesNotDeadlockWhenSearchesReturnImmediately(t *testing.T) {
	localFiles := make([]*LocalFile, 0, 64)
	results := make(map[string][]*navidrome.RemoteSong, 64)
	for i := range 64 {
		relPath := filepath.ToSlash(filepath.Join("artist", fmt.Sprintf("track-%02d.mp3", i)))
		query := fmt.Sprintf("Track %02d Artist Album", i)
		localFiles = append(localFiles, &LocalFile{
			RelPath: relPath,
			LocalFile: &tag.LocalFile{
				Title:  fmt.Sprintf("Track %02d", i),
				Artist: "Artist",
				Album:  "Album",
			},
		})
		results[query] = []*navidrome.RemoteSong{{
			ID:   fmt.Sprintf("song-%02d", i),
			Path: relPath,
		}}
	}

	searcher := &stubSongSearcher{results: results}

	done := make(chan struct{})
	var report *matchReport
	var err error
	go func() {
		report, err = matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("matchLocalToRemote() timed out, likely deadlocked")
	}

	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != len(localFiles) {
		t.Fatalf("len(report.matches) = %d, want %d", len(report.matches), len(localFiles))
	}
	if len(report.unmatched) != 0 {
		t.Fatalf("len(report.unmatched) = %d, want 0", len(report.unmatched))
	}
}

func TestMatchLocalToRemote_StripsRemotePathPrefix(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Track Artist Track Album": {
				{ID: "match", Path: "/music/artist/album/track.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "artist/album/track.mp3",
		LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Track Artist", Album: "Track Album"},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "music", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
}

func TestMatchLocalToRemote_PrefersMusicBrainzIDOverPath(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Track Artist Track Album": {
				{ID: "path-match", Path: "artist/album/track.mp3", UserRating: 1},
				{ID: "mbid-match", Path: "different/path.mp3", MusicBrainzID: "mbid-123", UserRating: 5},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "artist/album/track.mp3",
		LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Track Artist", Album: "Track Album", MusicBrainzID: "mbid-123"},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].remote.ID != "mbid-match" {
		t.Fatalf("report.matches[0].remote.ID = %q, want %q", report.matches[0].remote.ID, "mbid-match")
	}
	if report.matches[0].method != "musicbrainz_id" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "musicbrainz_id")
	}
}

func TestMatchLocalToRemote_ReportsUnmatchedCandidates(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Track Artist Track Album": {
				{ID: "wrong", Path: "/library/other/track.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "artist/album/track.mp3",
		LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Track Artist", Album: "Track Album"},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "library", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 0 {
		t.Fatalf("len(report.matches) = %d, want 0", len(report.matches))
	}
	if len(report.unmatched) != 1 {
		t.Fatalf("len(report.unmatched) = %d, want 1", len(report.unmatched))
	}
	if report.unmatched[0].reason != "candidate paths did not match local path" {
		t.Fatalf("reason = %q, want path mismatch", report.unmatched[0].reason)
	}
	if len(report.unmatched[0].candidates) != 1 {
		t.Fatalf("len(report.unmatched[0].candidates) = %d, want 1", len(report.unmatched[0].candidates))
	}
	if report.unmatched[0].candidates[0].raw != "/library/other/track.mp3" {
		t.Fatalf("raw candidate path = %q, want raw Navidrome path", report.unmatched[0].candidates[0].raw)
	}
	if report.unmatched[0].candidates[0].normalized != "other/track.mp3" {
		t.Fatalf("normalized candidate path = %q, want normalized path", report.unmatched[0].candidates[0].normalized)
	}
}

func TestMatchLocalToRemote_UsesSuffixPathFallback(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Why They Hide Their Bodies Under My Garage Blawan His He She & She": {
				{ID: "match", Path: "Blawan/His He She & She/01 - Why They Hide Their Bodies Under My Garage.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath: "Blawan/His He She & She/01 Why They Hide Their Bodies Under.mp3",
		LocalFile: &tag.LocalFile{
			Title:  "Why They Hide Their Bodies Under My Garage",
			Artist: "Blawan",
			Album:  "His He She & She",
		},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].method != "path_suffix" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "path_suffix")
	}
}

func TestMatchLocalToRemote_DoesNotUseWeakSuffixPathFallback(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Passer By Blawan Blueprint Structures & Solutions 1996-2016": {
				{ID: "wrong", Path: "Various Artists/Blueprint Structures & Solutions 1996-2016/14 - Passer By.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath: "Blawan/Blueprint Structures & Solutions 1996-2016/14 Passer By.mp3",
		LocalFile: &tag.LocalFile{
			Title:  "Passer By",
			Artist: "Blawan",
			Album:  "Blueprint Structures & Solutions 1996-2016",
		},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 0 {
		t.Fatalf("len(report.matches) = %d, want 0", len(report.matches))
	}
	if len(report.unmatched) != 1 {
		t.Fatalf("len(report.unmatched) = %d, want 1", len(report.unmatched))
	}
}

func TestSearchQuery_UsesTrackMetadata(t *testing.T) {
	localFile := &LocalFile{
		RelPath: "artist/album/track.mp3",
		LocalFile: &tag.LocalFile{
			Title:  "Track Title",
			Artist: "Track Artist",
			Album:  "Track Album",
		},
	}

	if got, want := searchQuery(localFile), "Track Title Track Artist Track Album"; got != want {
		t.Fatalf("searchQuery() = %q, want %q", got, want)
	}
}

func TestSearchQuery_UsesPathMetadataWhenTagsAreMissing(t *testing.T) {
	localFile := &LocalFile{
		RelPath:   "Chaos In The CBD/Never Again EP/03 Mariana Trench (Original Mix).mp3",
		LocalFile: &tag.LocalFile{},
	}

	if got, want := searchQuery(localFile), "Mariana Trench (Original Mix) Chaos In The CBD Never Again EP"; got != want {
		t.Fatalf("searchQuery() = %q, want %q", got, want)
	}
}

func TestSearchQueries_StripsTrackPrefixAndFallsBackToTitleOnly(t *testing.T) {
	localFile := &LocalFile{
		RelPath:   "Steve Moore/Positronic Neural Pathways/01 Positronic Neural Pathways.mp3",
		LocalFile: &tag.LocalFile{},
	}

	if got, want := searchQueries(localFile), []string{
		"Positronic Neural Pathways Steve Moore Positronic Neural Pathways",
		"Positronic Neural Pathways Steve Moore",
		"Positronic Neural Pathways",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("searchQueries() = %v, want %v", got, want)
	}
}

func TestMatchLocalToRemote_TriesLessSpecificQueriesWhenFirstSearchMisses(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Brænder": {
				{ID: "match", Path: "C.K/Accelerer/02 Brænder.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "C.K/Accelerer/02 Brænder.mp3",
		LocalFile: &tag.LocalFile{Title: "Brænder", Artist: "C.K", Album: "Accelerer"},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if got, want := searcher.queries, []string{
		"Brænder C.K Accelerer",
		"Brænder C.K",
		"Brænder",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %v, want %v", got, want)
	}
}

func TestMatchLocalToRemote_ReportsAmbiguousSuffixMatches(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Artist Album": {
				{ID: "candidate-1", Path: "folder-a/Artist/Album/01 - Track Title.mp3"},
				{ID: "candidate-2", Path: "folder-b/Artist/Album/01 - Track Title.mp3"},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "Artist/Album/01 Track Title.mp3",
		LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Artist", Album: "Album"},
	}}

	report, err := matchLocalToRemote(context.Background(), localFiles, searcher, "", 0, testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 0 {
		t.Fatalf("len(report.matches) = %d, want 0", len(report.matches))
	}
	if len(report.ambiguous) != 1 {
		t.Fatalf("len(report.ambiguous) = %d, want 1", len(report.ambiguous))
	}
	if got := report.ambiguous[0].reason; got != "multiple candidates tied for suffix path match" {
		t.Fatalf("reason = %q, want suffix ambiguity", got)
	}
}

func TestRun_IncludesAmbiguousEntriesInReport(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Artist Album": {
				{ID: "candidate-1", Path: "folder-a/Artist/Album/01 - Track Title.mp3"},
				{ID: "candidate-2", Path: "folder-b/Artist/Album/01 - Track Title.mp3"},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "Artist/Album/01 Track Title.mp3",
		LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Artist", Album: "Album"},
	}}

	output, err := Run(context.Background(), "", localFiles, searcher, "", "local", 0, true, testLogger())
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := output.Report.Summary.Ambiguous; got != 1 {
		t.Fatalf("output.Report.Summary.Ambiguous = %d, want 1", got)
	}
	if len(output.Report.Ambiguous) != 1 {
		t.Fatalf("len(output.Report.Ambiguous) = %d, want 1", len(output.Report.Ambiguous))
	}
}

func TestApplyResults_ReturnsAggregateFailure(t *testing.T) {
	results := []Result{{
		Action:    ActionPull,
		Path:      "track.txt",
		NewRating: 4,
	}}

	err := ApplyResults(context.Background(), t.TempDir(), results, &navidrome.Client{}, false, testLogger())
	if err == nil {
		t.Fatal("ApplyResults() error = nil, want aggregate failure")
	}
}

func TestWriteReportJSON_WritesStructuredReport(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "reports", "sync.json")

	report := RunReport{
		Summary: ReportSummary{Matched: 1, Unmatched: 1, Ambiguous: 1},
		Matched: []MatchedEntry{{Path: "track.mp3"}},
		Unmatched: []UnresolvedEntry{{
			Path:   "missing.mp3",
			Reason: "search returned no song candidates",
		}},
		Ambiguous: []UnresolvedEntry{{
			Path:   "ambiguous.mp3",
			Reason: "multiple candidates tied for suffix path match",
		}},
	}

	if err := WriteReportJSON(path, report); err != nil {
		t.Fatalf("WriteReportJSON() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	var decoded RunReport
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if got := decoded.Summary.Ambiguous; got != 1 {
		t.Fatalf("decoded.Summary.Ambiguous = %d, want 1", got)
	}
}

func mustWriteFile(t *testing.T, path string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
}

func testLogger() *log.Logger {
	return log.NewWithOptions(os.Stderr, log.Options{
		Level:           log.DebugLevel,
		Formatter:       log.TextFormatter,
		ReportTimestamp: false,
	})
}

type stubSongSearcher struct {
	mu      sync.Mutex
	queries []string
	limits  []int
	results map[string][]*navidrome.RemoteSong
	err     error
}

func (s *stubSongSearcher) SearchSongsByTitle(ctx context.Context, title string, limit int) ([]*navidrome.RemoteSong, error) {
	if ctx == nil {
		return nil, fmt.Errorf("context is nil")
	}
	s.mu.Lock()
	s.queries = append(s.queries, title)
	s.limits = append(s.limits, limit)
	s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}
	return s.results[title], nil
}

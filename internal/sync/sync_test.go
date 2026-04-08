package sync

import (
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

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
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
			"01-track title": {
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

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if got, want := searcher.queries, []string{"01-track title"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("queries = %v, want %v", got, want)
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

	report, err := matchLocalToRemote(localFiles, searcher, "music", testLogger())
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

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
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

	report, err := matchLocalToRemote(localFiles, searcher, "library", testLogger())
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

func TestMatchLocalToRemote_CanonicalizesZeroPaddedTrackPrefix(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Track Title Track Artist Track Album": {
				{ID: "match", Path: "artist/album/01-03 - track.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "artist/album/1-03 - track.mp3",
		LocalFile: &tag.LocalFile{Title: "Track Title", Artist: "Track Artist", Album: "Track Album"},
	}}

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].method != "path_canonical" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "path_canonical")
	}
}

func TestMatchLocalToRemote_CanonicalizesMissingDashAfterTrackNumber(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"E L E K T R O Jensen Interceptor The Ultimate Wave Riding Vehicle": {
				{ID: "match", Path: "Jensen Interceptor/The Ultimate Wave Riding Vehicle/03 - E L E K T R O.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath: "Jensen Interceptor/The Ultimate Wave Riding Vehicle/03 E L E K T R O.mp3",
		LocalFile: &tag.LocalFile{
			Title:  "E L E K T R O",
			Artist: "Jensen Interceptor",
			Album:  "The Ultimate Wave Riding Vehicle",
		},
	}}

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].method != "path_canonical" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "path_canonical")
	}
}

func TestMatchLocalToRemote_CanonicalizesNNTitleToDoublePrefixDashTitle(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Kaz Blawan Bohla EP": {
				{ID: "match", Path: "Blawan/Bohla EP/01-02 - Kaz.mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "Blawan/Bohla EP/02 Kaz.mp3",
		LocalFile: &tag.LocalFile{Title: "Kaz", Artist: "Blawan", Album: "Bohla EP"},
	}}

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].method != "path_canonical" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "path_canonical")
	}
}

func TestMatchLocalToRemote_CanonicalizesNNTitleToDashTitle(t *testing.T) {
	searcher := &stubSongSearcher{
		results: map[string][]*navidrome.RemoteSong{
			"Peaches [Coronation] Blawan Peaches": {
				{ID: "match", Path: "Blawan/Peaches/01 - Peaches [Coronation].mp3", UserRating: 4},
			},
		},
	}

	localFiles := []*LocalFile{{
		RelPath:   "Blawan/Peaches/01 Peaches [Coronation].mp3",
		LocalFile: &tag.LocalFile{Title: "Peaches [Coronation]", Artist: "Blawan", Album: "Peaches"},
	}}

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
	if err != nil {
		t.Fatalf("matchLocalToRemote() error = %v", err)
	}
	if len(report.matches) != 1 {
		t.Fatalf("len(report.matches) = %d, want 1", len(report.matches))
	}
	if report.matches[0].method != "path_canonical" {
		t.Fatalf("report.matches[0].method = %q, want %q", report.matches[0].method, "path_canonical")
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

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
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

	report, err := matchLocalToRemote(localFiles, searcher, "", testLogger())
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

func (s *stubSongSearcher) SearchSongsByTitle(title string, limit int) ([]*navidrome.RemoteSong, error) {
	s.mu.Lock()
	s.queries = append(s.queries, title)
	s.limits = append(s.limits, limit)
	s.mu.Unlock()

	if s.err != nil {
		return nil, s.err
	}
	return s.results[title], nil
}

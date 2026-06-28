package tag

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// genAudio synthesizes a ~1s silent file of the given extension with ffmpeg.
// The test is skipped when ffmpeg is not installed.
func genAudio(t *testing.T, ext string) string {
	t.Helper()
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skipf("ffmpeg not available: %v", err)
	}
	path := filepath.Join(t.TempDir(), "track"+ext)
	cmd := exec.Command(ffmpeg, "-nostdin", "-y",
		"-f", "lavfi", "-i", "anullsrc=r=44100:cl=mono", "-t", "1", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg could not encode %s: %v\n%s", ext, err, out)
	}
	return path
}

func TestTaglibHandler_RoundTrip(t *testing.T) {
	t.Cleanup(func() { ConfigureRatingOrder(nil) })
	ConfigureRatingOrder(nil)

	for _, ext := range []string{".ogg", ".opus", ".m4a"} {
		t.Run(ext, func(t *testing.T) {
			path := genAudio(t, ext)
			h := taglibHandler{}

			if err := h.writeRating(path, 4); err != nil {
				t.Fatalf("writeRating() error = %v", err)
			}
			if err := h.writeStarred(path, true); err != nil {
				t.Fatalf("writeStarred() error = %v", err)
			}
			played := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
			if err := h.writePlayStats(path, 7, &played); err != nil {
				t.Fatalf("writePlayStats() error = %v", err)
			}

			lf, err := h.read(path)
			if err != nil {
				t.Fatalf("read() error = %v", err)
			}
			if lf.Rating != 4 {
				t.Errorf("rating = %d, want 4", lf.Rating)
			}
			if !lf.Starred {
				t.Errorf("starred = false, want true")
			}
			if lf.PlayCount != 7 {
				t.Errorf("play count = %d, want 7", lf.PlayCount)
			}
			if lf.Played == nil || !lf.Played.Equal(played) {
				t.Errorf("played = %v, want %v", lf.Played, played)
			}

			// Clearing the rating removes it on re-read.
			if err := h.writeRating(path, 0); err != nil {
				t.Fatalf("writeRating(0) error = %v", err)
			}
			lf, err = h.read(path)
			if err != nil {
				t.Fatalf("read() after clear error = %v", err)
			}
			if lf.Rating != 0 {
				t.Errorf("rating after clear = %d, want 0", lf.Rating)
			}

			// Unstarring removes the favorite tag.
			if err := h.writeStarred(path, false); err != nil {
				t.Fatalf("writeStarred(false) error = %v", err)
			}
			lf, err = h.read(path)
			if err != nil {
				t.Fatalf("read() after unstar error = %v", err)
			}
			if lf.Starred {
				t.Errorf("starred after unstar = true, want false")
			}
		})
	}
}

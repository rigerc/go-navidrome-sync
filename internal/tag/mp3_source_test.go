package tag

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2"
)

// writeMP3WithFrames writes an MP3 carrying the given frames plus a byte of
// fake audio, so readMP3File has something parseable.
func writeMP3WithFrames(t *testing.T, path string, build func(tag *id3v2.Tag)) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	defer f.Close()

	tag := id3v2.NewEmptyTag()
	build(tag)
	if _, err := tag.WriteTo(f); err != nil {
		t.Fatalf("WriteTo() error = %v", err)
	}
	if _, err := f.Write([]byte("audio")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
}

func TestReadMP3_RatingTagOrderDecidesWinner(t *testing.T) {
	t.Cleanup(func() { ConfigureRatingOrder(nil) })

	root := t.TempDir()
	path := filepath.Join(root, "track.mp3")
	writeMP3WithFrames(t, path, func(tag *id3v2.Tag) {
		// WMP POPM -> 5 stars
		tag.AddFrame(tag.CommonID("POPM"), id3v2.PopularimeterFrame{
			Email:   "Windows Media Player 9 Series",
			Rating:  255,
			Counter: big.NewInt(0),
		})
		// MediaMonkey FMPS_Rating 0.2 -> 1 star
		tag.AddFrame(tag.CommonID("TXXX"), id3v2.UserDefinedTextFrame{
			Encoding:    id3v2.EncodingUTF8,
			Description: "FMPS_Rating",
			Value:       "0.2",
		})
	})

	ConfigureRatingOrder([]string{SourceWMP, SourceMediaMonkey})
	lf, err := readMP3File(path)
	if err != nil {
		t.Fatalf("readMP3File() error = %v", err)
	}
	if lf.Rating != 5 {
		t.Fatalf("rating with WMP first = %d, want 5", lf.Rating)
	}

	ConfigureRatingOrder([]string{SourceMediaMonkey, SourceWMP})
	lf, err = readMP3File(path)
	if err != nil {
		t.Fatalf("readMP3File() error = %v", err)
	}
	if lf.Rating != 1 {
		t.Fatalf("rating with MediaMonkey first = %d, want 1", lf.Rating)
	}
}

func TestReadMP3_ITunesPopmUsesITunesScale(t *testing.T) {
	t.Cleanup(func() { ConfigureRatingOrder(nil) })

	root := t.TempDir()
	path := filepath.Join(root, "itunes.mp3")
	writeMP3WithFrames(t, path, func(tag *id3v2.Tag) {
		// iTunes writes POPM byte 60 on its 0–100 scale -> 3 stars.
		tag.AddFrame(tag.CommonID("POPM"), id3v2.PopularimeterFrame{
			Email:   "iTunes v1.0",
			Rating:  60,
			Counter: big.NewInt(0),
		})
	})

	ConfigureRatingOrder([]string{SourceITunes, SourceWMP})
	lf, err := readMP3File(path)
	if err != nil {
		t.Fatalf("readMP3File() error = %v", err)
	}
	if lf.Rating != 3 {
		t.Fatalf("iTunes POPM 60 = %d stars, want 3", lf.Rating)
	}
}

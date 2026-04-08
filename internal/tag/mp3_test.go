package tag

import (
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogem/id3v2"
)

func TestWritePOPMRating_ReplacesExistingFrames(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "track.mp3")

	if err := writeTaggedMP3(path, []id3v2.PopularimeterFrame{
		{Email: "one@example.com", Rating: 64},
		{Email: "two@example.com", Rating: 128},
	}); err != nil {
		t.Fatalf("writeTaggedMP3() error = %v", err)
	}

	if err := WritePOPMRating(path, 4); err != nil {
		t.Fatalf("WritePOPMRating() error = %v", err)
	}

	tag, err := id3v2.Open(path, id3v2.Options{Parse: true})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer tag.Close()

	popmID := tag.CommonID("POPM")
	frames := tag.GetFrames(popmID)
	if len(frames) != 1 {
		t.Fatalf("len(frames) = %d, want 1", len(frames))
	}

	popm, ok := frames[0].(id3v2.PopularimeterFrame)
	if !ok {
		t.Fatalf("frames[0] type = %T, want PopularimeterFrame", frames[0])
	}
	if got, want := int(popm.Rating), int(starsToPopm[4]); got != want {
		t.Fatalf("popm.Rating = %d, want %d", got, want)
	}
}

func writeTaggedMP3(path string, frames []id3v2.PopularimeterFrame) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()

	tag := id3v2.NewEmptyTag()
	for _, frame := range frames {
		if frame.Counter == nil {
			frame.Counter = big.NewInt(0)
		}
		tag.AddFrame(tag.CommonID("POPM"), frame)
	}
	if _, err := tag.WriteTo(file); err != nil {
		return err
	}
	_, err = file.Write([]byte("audio"))
	return err
}

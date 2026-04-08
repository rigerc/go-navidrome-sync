package tag

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteRating_FlacShortFileReturnsError(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "track.flac")

	if err := os.WriteFile(path, []byte("no"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := WriteRating(path, 4)
	if err == nil {
		t.Fatal("WriteRating() error = nil, want invalid flac error")
	}
}

func TestReadLocalFile_UnsupportedExtensionReturnsZeroValue(t *testing.T) {
	lf, err := ReadLocalFile("track.txt")
	if err != nil {
		t.Fatalf("ReadLocalFile() error = %v", err)
	}
	if lf == nil {
		t.Fatal("ReadLocalFile() = nil, want zero-value LocalFile")
	}
}

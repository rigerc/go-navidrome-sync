package tag

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/mewkiz/flac/meta"
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

func TestWriteFlacRating_AddsVorbisCommentWhenMissing(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "track.flac")
	writeMinimalFLAC(t, path)

	if err := WriteFlacRating(path, 4); err != nil {
		t.Fatalf("WriteFlacRating() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if got := data[4]; got != 0x00 {
		t.Fatalf("first metadata header byte = 0x%02x, want 0x00", got)
	}

	secondHeaderOffset := 4 + 4 + 34
	if got := data[secondHeaderOffset]; got != 0x84 {
		t.Fatalf("second metadata header byte = 0x%02x, want 0x84", got)
	}

	commentLen := int(binary.BigEndian.Uint32([]byte{0, data[secondHeaderOffset+1], data[secondHeaderOffset+2], data[secondHeaderOffset+3]}))
	commentBodyStart := secondHeaderOffset + 4
	commentBody, err := parseVorbisCommentBody(data[commentBodyStart : commentBodyStart+commentLen])
	if err != nil {
		t.Fatalf("parseVorbisCommentBody() error = %v", err)
	}
	if got := commentTagValue(commentBody, "RATING"); got != "4" {
		t.Fatalf("RATING = %q, want %q", got, "4")
	}
}

func TestWriteFlacRating_PreservesPermissions(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "track.flac")
	writeMinimalFLAC(t, path)

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	if err := WriteFlacRating(path, 5); err != nil {
		t.Fatalf("WriteFlacRating() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want %o", got, 0o600)
	}
}

func writeMinimalFLAC(t *testing.T, path string) {
	t.Helper()

	data := make([]byte, 0, 4+4+34+4)
	data = append(data, []byte("fLaC")...)
	data = append(data, 0x80, 0x00, 0x00, 0x22)
	data = append(data, make([]byte, 34)...)
	data = append(data, []byte("AUDO")...)

	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
}

func commentTagValue(vc *meta.VorbisComment, key string) string {
	for _, tag := range vc.Tags {
		if tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

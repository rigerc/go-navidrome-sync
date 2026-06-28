package tag

import (
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

// handler reads and writes metadata for one family of audio containers.
type handler interface {
	read(path string) (*LocalFile, error)
	writeRating(path string, rating int) error
	writeStarred(path string, starred bool) error
	writePlayStats(path string, playCount int64, played *time.Time) error
}

// handlers maps a lowercased file extension to its handler.
var handlers = map[string]handler{
	".mp3":  mp3Handler{},
	".flac": flacHandler{},
	".ogg":  taglibHandler{},
	".oga":  taglibHandler{},
	".opus": taglibHandler{},
	".m4a":  taglibHandler{},
	".aac":  taglibHandler{},
	".mp4":  taglibHandler{},
}

func handlerFor(path string) (handler, bool) {
	h, ok := handlers[strings.ToLower(filepath.Ext(path))]
	return h, ok
}

func unsupportedFormatError(path string) error {
	return fmt.Errorf("unsupported file format: %s", strings.ToLower(filepath.Ext(path)))
}

// ReadLocalFile reads all syncable metadata from path. Unsupported extensions
// return a zero-value LocalFile and no error, so callers can scan mixed trees.
func ReadLocalFile(path string) (*LocalFile, error) {
	h, ok := handlerFor(path)
	if !ok {
		return &LocalFile{}, nil
	}
	return h.read(path)
}

// ReadRating returns just the resolved star rating for path.
func ReadRating(path string) (int, error) {
	lf, err := ReadLocalFile(path)
	if err != nil {
		return 0, err
	}
	return lf.Rating, nil
}

// WriteRating writes a star rating (0–5) to the local file at path.
func WriteRating(path string, rating int) error {
	h, ok := handlerFor(path)
	if !ok {
		return unsupportedFormatError(path)
	}
	return h.writeRating(path, rating)
}

// WriteStarred writes the starred/favorite state to the local file at path.
func WriteStarred(path string, starred bool) error {
	h, ok := handlerFor(path)
	if !ok {
		return unsupportedFormatError(path)
	}
	return h.writeStarred(path, starred)
}

// WritePlayStats writes play count and last-played to the local file at path.
func WritePlayStats(path string, playCount int64, played *time.Time) error {
	h, ok := handlerFor(path)
	if !ok {
		return unsupportedFormatError(path)
	}
	return h.writePlayStats(path, playCount, played)
}

type mp3Handler struct{}

func (mp3Handler) read(path string) (*LocalFile, error) { return readMP3File(path) }
func (mp3Handler) writeRating(path string, rating int) error {
	return WritePOPMRating(path, rating)
}
func (mp3Handler) writeStarred(path string, starred bool) error {
	return WriteMP3Starred(path, starred)
}
func (mp3Handler) writePlayStats(path string, playCount int64, played *time.Time) error {
	return WriteMP3PlayStats(path, playCount, played)
}

type flacHandler struct{}

func (flacHandler) read(path string) (*LocalFile, error) { return readFlacFile(path) }
func (flacHandler) writeRating(path string, rating int) error {
	return WriteFlacRating(path, rating)
}
func (flacHandler) writeStarred(path string, starred bool) error {
	return WriteFlacStarred(path, starred)
}
func (flacHandler) writePlayStats(path string, playCount int64, played *time.Time) error {
	return WriteFlacPlayStats(path, playCount, played)
}

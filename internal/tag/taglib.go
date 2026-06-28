package tag

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.senan.xyz/taglib"
)

// taglibHandler backs the containers that lack a dedicated native-Go writer
// (Ogg, Opus, M4A/AAC, MP4) via an embedded WASM build of TagLib. TagLib
// normalises tag keys to uppercase and exposes them as a map[string][]string.
//
// Unlike the MP3/FLAC handlers it cannot see the POPM rater email, so it only
// resolves the Vorbis/MP4 rating sources: MediaMonkey (FMPS_RATING) and
// foobar2000 (RATING). A RATING value outside the 1–5 range is interpreted as
// an iTunes 0–100 scale.
type taglibHandler struct{}

func (taglibHandler) read(path string) (*LocalFile, error) {
	tags, err := taglib.ReadTags(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read tags from %s: %w", path, err)
	}

	lf := &LocalFile{
		MusicBrainzID: firstTag(tags, "MUSICBRAINZ_TRACKID"),
		ISRC:          firstTag(tags, "ISRC"),
		Artist:        firstTag(tags, "ARTIST"),
		Album:         firstTag(tags, "ALBUM"),
		Title:         firstTag(tags, "TITLE"),
	}

	var rc ratingCandidates
	if stars, ok := fmpsToStars(firstTag(tags, "FMPS_RATING")); ok {
		rc.mediaMonkey = stars
	}
	if s := strings.TrimSpace(firstTag(tags, "RATING")); s != "" {
		if stars, ok := ratingIntToStars(s); ok {
			rc.foobar = stars
		} else if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 100 {
			rc.itunes = popmITunesToStars(byte(n))
		}
	}
	lf.Rating = rc.resolve()

	if n, err := strconv.ParseInt(firstNonEmptyTag(tags, "PLAY_COUNT", "PLAYCOUNT"), 10, 64); err == nil && n > 0 {
		lf.PlayCount = n
	}
	if t, err := time.Parse(time.RFC3339, firstTag(tags, "LAST_PLAYED")); err == nil {
		lf.Played = &t
	}
	if fav := firstNonEmptyTag(tags, "FAVORITE", "STARRED"); fav != "" {
		lf.Starred = isTruthyTagValue(fav)
	}

	return lf, nil
}

func (taglibHandler) writeRating(path string, rating int) error {
	// Write the canonical foobar-style RATING and drop FMPS_RATING so a
	// subsequent read resolves to the same value regardless of source order.
	update := map[string][]string{"FMPS_RATING": nil}
	if rating == 0 {
		update["RATING"] = nil
	} else {
		update["RATING"] = []string{strconv.Itoa(rating)}
	}
	return writeTaglib(path, update)
}

func (taglibHandler) writeStarred(path string, starred bool) error {
	update := map[string][]string{"STARRED": nil}
	if starred {
		update["FAVORITE"] = []string{"1"}
	} else {
		update["FAVORITE"] = nil
	}
	return writeTaglib(path, update)
}

func (taglibHandler) writePlayStats(path string, playCount int64, played *time.Time) error {
	update := map[string][]string{}
	if playCount > 0 {
		update["PLAY_COUNT"] = []string{strconv.FormatInt(playCount, 10)}
	}
	if played != nil {
		update["LAST_PLAYED"] = []string{played.UTC().Format(time.RFC3339)}
	}
	if len(update) == 0 {
		return nil
	}
	return writeTaglib(path, update)
}

// writeTaglib merges update into the file's tags (keys with no values are
// removed), leaving all other tags untouched.
func writeTaglib(path string, update map[string][]string) error {
	if err := taglib.WriteTags(path, update, 0); err != nil {
		return fmt.Errorf("failed to write tags to %s: %w", path, err)
	}
	return nil
}

func firstTag(tags map[string][]string, key string) string {
	if v := tags[key]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func firstNonEmptyTag(tags map[string][]string, keys ...string) string {
	for _, key := range keys {
		if v := firstTag(tags, key); v != "" {
			return v
		}
	}
	return ""
}

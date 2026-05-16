package tag

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/bogem/id3v2"
)

var popmThresholds = []struct {
	threshold int
	stars     int
}{
	{1, 1},
	{64, 2},
	{128, 3},
	{196, 4},
	{255, 5},
}

var starsToPopm = map[int]uint8{
	0: 0,
	1: 1,
	2: 64,
	3: 128,
	4: 196,
	5: 255,
}

func ReadPOPMRating(filePath string) (int, error) {
	lf, err := readMP3File(filePath)
	if err != nil {
		return 0, err
	}
	return lf.Rating, nil
}

func WritePOPMRating(filePath string, rating int) error {
	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", filePath, err)
	}
	defer tag.Close()

	popmID := tag.CommonID("POPM")
	var popm id3v2.PopularimeterFrame
	if f := tag.GetLastFrame(popmID); f != nil {
		if existing, ok := f.(id3v2.PopularimeterFrame); ok {
			popm.Email = existing.Email
			popm.Counter = existing.Counter
		}
	}
	tag.DeleteFrames(popmID)
	if popm.Counter == nil {
		popm.Counter = big.NewInt(0)
	}
	popm = id3v2.PopularimeterFrame{
		Email:   popm.Email,
		Rating:  starsToPopm[rating],
		Counter: popm.Counter,
	}

	tag.AddFrame(popmID, popm)

	if err := tag.Save(); err != nil {
		return fmt.Errorf("failed to save %s: %w", filePath, err)
	}

	return nil
}

func WriteMP3Starred(filePath string, starred bool) error {
	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", filePath, err)
	}
	defer tag.Close()

	existing := tag.GetFrames(tag.CommonID("TXXX"))
	tag.DeleteFrames(tag.CommonID("TXXX"))
	for _, f := range existing {
		if fr, ok := f.(id3v2.UserDefinedTextFrame); ok {
			if strings.EqualFold(fr.Description, "FAVORITE") || strings.EqualFold(fr.Description, "STARRED") {
				continue
			}
		}
		tag.AddFrame(tag.CommonID("TXXX"), f)
	}
	if starred {
		tag.AddFrame(tag.CommonID("TXXX"), id3v2.UserDefinedTextFrame{
			Description: "FAVORITE",
			Value:       "1",
		})
	}
	if err := tag.Save(); err != nil {
		return fmt.Errorf("failed to save %s: %w", filePath, err)
	}
	return nil
}

func WriteMP3PlayStats(filePath string, playCount int64, played *time.Time) error {
	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return fmt.Errorf("failed to open %s: %w", filePath, err)
	}
	defer tag.Close()

	existing := tag.GetFrames(tag.CommonID("TXXX"))
	tag.DeleteFrames(tag.CommonID("TXXX"))
	for _, f := range existing {
		if fr, ok := f.(id3v2.UserDefinedTextFrame); ok {
			if strings.EqualFold(fr.Description, "PLAY_COUNT") || strings.EqualFold(fr.Description, "LAST_PLAYED") {
				continue
			}
		}
		tag.AddFrame(tag.CommonID("TXXX"), f)
	}

	if playCount > 0 {
		tag.AddFrame(tag.CommonID("TXXX"), id3v2.UserDefinedTextFrame{
			Description: "PLAY_COUNT",
			Value:       strconv.FormatInt(playCount, 10),
		})
	}
	if played != nil {
		tag.AddFrame(tag.CommonID("TXXX"), id3v2.UserDefinedTextFrame{
			Description: "LAST_PLAYED",
			Value:       played.UTC().Format(time.RFC3339),
		})
	}

	if err := tag.Save(); err != nil {
		return fmt.Errorf("failed to save %s: %w", filePath, err)
	}
	return nil
}

func readMP3File(filePath string) (*LocalFile, error) {
	tag, err := id3v2.Open(filePath, id3v2.Options{Parse: true})
	if err != nil {
		return nil, fmt.Errorf("failed to open %s: %w", filePath, err)
	}
	defer tag.Close()

	lf := &LocalFile{}

	if f := tag.GetLastFrame(tag.CommonID("POPM")); f != nil {
		if popf, ok := f.(id3v2.PopularimeterFrame); ok {
			lf.Rating = mapPopmToStars(int(popf.Rating))
		}
	}

	for _, id := range []string{"UFID", "TXXX"} {
		frames := tag.GetFrames(id)
		for _, f := range frames {
			switch fr := f.(type) {
			case id3v2.UserDefinedTextFrame:
				if strings.EqualFold(fr.Description, "MusicBrainz Release Track Id") {
					lf.MusicBrainzID = fr.Value
				}
				if strings.EqualFold(fr.Description, "ISRC") {
					lf.ISRC = fr.Value
				}
				if strings.EqualFold(fr.Description, "PLAY_COUNT") {
					if n, err := strconv.ParseInt(fr.Value, 10, 64); err == nil && n > 0 {
						lf.PlayCount = n
					}
				}
				if strings.EqualFold(fr.Description, "LAST_PLAYED") {
					if t, err := time.Parse(time.RFC3339, fr.Value); err == nil {
						lf.Played = &t
					}
				}
				if strings.EqualFold(fr.Description, "FAVORITE") || strings.EqualFold(fr.Description, "STARRED") {
					lf.Starred = isTruthyTagValue(fr.Value)
				}
			case id3v2.UnsynchronisedLyricsFrame:
				// UFID stores MusicBrainz ID with owner identifier
			}
		}
	}

	// UFID frame for MusicBrainz
	if frames := tag.GetFrames("UFID"); frames != nil {
		for _, f := range frames {
			if ufid, ok := f.(id3v2.UFIDFrame); ok {
				if strings.Contains(ufid.OwnerIdentifier, "musicbrainz.org") {
					lf.MusicBrainzID = string(ufid.Identifier)
				}
			}
		}
	}

	if f := tag.GetLastFrame("TPE1"); f != nil {
		if tf, ok := f.(id3v2.TextFrame); ok {
			lf.Artist = tf.Text
		}
	}
	if f := tag.GetLastFrame("TALB"); f != nil {
		if tf, ok := f.(id3v2.TextFrame); ok {
			lf.Album = tf.Text
		}
	}
	if f := tag.GetLastFrame("TIT2"); f != nil {
		if tf, ok := f.(id3v2.TextFrame); ok {
			lf.Title = tf.Text
		}
	}
	if f := tag.GetLastFrame("TSRC"); f != nil {
		if tf, ok := f.(id3v2.TextFrame); ok {
			lf.ISRC = tf.Text
		}
	}

	return lf, nil
}

func mapPopmToStars(popm int) int {
	stars := 0
	for _, entry := range popmThresholds {
		if popm >= entry.threshold {
			stars = entry.stars
		}
	}
	return stars
}

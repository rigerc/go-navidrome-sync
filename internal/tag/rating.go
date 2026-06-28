package tag

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// Rating source applications. Each source maps to whatever rating tag(s) that
// application typically writes, in whichever container is in use. The active
// order (see ConfigureRatingOrder) decides which source wins when a file
// carries more than one recognised rating tag.
const (
	SourceMediaMonkey = "MediaMonkey"
	SourceFoobar2000  = "foobar2000"
	SourceWMP         = "WMP"
	SourceITunes      = "iTunes"
)

// DefaultRatingTagOrder mirrors the nd-rating-sync plugin default.
var DefaultRatingTagOrder = []string{SourceWMP, SourceITunes, SourceMediaMonkey, SourceFoobar2000}

// KnownRatingSources is the set of values accepted in a rating tag order.
var KnownRatingSources = map[string]struct{}{
	SourceMediaMonkey: {},
	SourceFoobar2000:  {},
	SourceWMP:         {},
	SourceITunes:      {},
}

const (
	wmp1Star       = 1
	wmp2Star       = 64
	wmp3Star       = 128
	wmp4Star       = 196
	iTunes1StarMax = 20
	iTunes2StarMax = 40
	iTunes3StarMax = 60
	iTunes4StarMax = 80
)

// ratingTagOrder is the process-wide resolution order, configured once per run.
var ratingTagOrder = DefaultRatingTagOrder

// ConfigureRatingOrder sets the process-wide rating source resolution order.
// An empty or nil order resets to DefaultRatingTagOrder. Unknown sources are
// ignored. Call once at startup before scanning.
func ConfigureRatingOrder(order []string) {
	cleaned := make([]string, 0, len(order))
	for _, s := range order {
		s = strings.TrimSpace(s)
		if _, ok := KnownRatingSources[s]; ok {
			cleaned = append(cleaned, s)
		}
	}
	if len(cleaned) == 0 {
		ratingTagOrder = DefaultRatingTagOrder
		return
	}
	ratingTagOrder = cleaned
}

// ratingCandidates holds the star value (1–5, or 0 when absent) resolved for
// each rating source found in a single file. A reader fills only the sources
// its container can carry; resolve then applies the configured order.
type ratingCandidates struct {
	mediaMonkey int
	foobar      int
	wmp         int
	itunes      int
}

// resolve returns the first non-zero star value following the active order.
func (c ratingCandidates) resolve() int {
	for _, src := range ratingTagOrder {
		switch src {
		case SourceMediaMonkey:
			if c.mediaMonkey > 0 {
				return c.mediaMonkey
			}
		case SourceFoobar2000:
			if c.foobar > 0 {
				return c.foobar
			}
		case SourceWMP:
			if c.wmp > 0 {
				return c.wmp
			}
		case SourceITunes:
			if c.itunes > 0 {
				return c.itunes
			}
		}
	}
	return 0
}

// fmpsToStars reads an FMPS_Rating float (0.0–1.0) and turns it into a 1–5 star
// value. Uses ceiling so the canonical values (0.2, 0.4 … 1.0) land exactly on
// whole stars. Returns (0, false) for empty, zero, malformed, or NaN/Inf input.
func fmpsToStars(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" || s == "0" || s == "0.0" {
		return 0, false
	}

	var f float64
	if _, err := fmt.Sscanf(s, "%f", &f); err != nil {
		return 0, false
	}
	// Reject NaN/Inf explicitly: comparisons with NaN are all false, so
	// `f <= 0` would let it slip through and produce garbage from math.Ceil.
	if math.IsNaN(f) || math.IsInf(f, 0) || f <= 0 {
		return 0, false
	}
	if f > 1 {
		f = 1
	}

	stars := int(math.Ceil(f * 5))
	if stars > 5 {
		stars = 5
	}
	return stars, true
}

// ratingIntToStars parses a plain integer star count in the range 1–5, used by
// foobar2000-style tags (TXXX:RATING in MP3, RATING Vorbis comment in
// FLAC/Ogg/Opus). Empty, "0", or out-of-range values are reported as unrated.
func ratingIntToStars(s string) (int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, false
	}
	if n < 1 || n > 5 {
		return 0, false
	}
	return n, true
}

// popmWMPToStars decodes a POPM byte written by Windows Media Player. WMP's
// internal scale runs 0–255 with fixed star breakpoints (1, 64, 128, 196, 255),
// so the byte ranges between those points all collapse to the lower star.
func popmWMPToStars(b byte) int {
	switch {
	case b == 0:
		return 0
	case b <= wmp1Star:
		return 1
	case b <= wmp2Star:
		return 2
	case b <= wmp3Star:
		return 3
	case b <= wmp4Star:
		return 4
	default:
		return 5
	}
}

// popmITunesToStars decodes a POPM byte written by iTunes / Apple Music. iTunes
// spreads its 5 stars evenly across 0–100 in steps of 20; values between steps
// round up to the next star.
func popmITunesToStars(b byte) int {
	switch {
	case b == 0:
		return 0
	case b <= iTunes1StarMax:
		return 1
	case b <= iTunes2StarMax:
		return 2
	case b <= iTunes3StarMax:
		return 3
	case b <= iTunes4StarMax:
		return 4
	default:
		return 5
	}
}

// select.go: which candidate recording becomes THE example call for a
// species. Pure functions, no I/O — the whole selection policy lives here.
package speciesaudio

import (
	"sort"
	"strconv"
)

// Clip length policy. The aim is a short, listenable example: anything in the
// preferred window is taken as-is, longer clips are trimmed at cache time.
const (
	// PreferredSecondsMin/Max bound the ideal clip (per product decision: ~5 s
	// is fine, up to 10 s).
	PreferredSecondsMin = 2.0
	PreferredSecondsMax = 10.0

	// DefaultMaxClipSeconds is the hard cap applied when storing a clip; longer
	// recordings are trimmed to this length.
	DefaultMaxClipSeconds = 10

	// FetchLengthLimitSeconds bounds the provider query so a 10-minute epic is
	// never downloaded just to be trimmed to 10 seconds.
	FetchLengthLimitSeconds = 180

	// APISearchMinSeconds is the bottom of the length window sent to the API.
	// Xeno-Canto's len: tag is in seconds, and sub-second clips are artefacts
	// rather than recordings worth playing.
	APISearchMinSeconds = 1
)

// SelectPolicy configures PickBest. Zero values mean "use the defaults".
type SelectPolicy struct {
	PreferSecondsMin float64
	PreferSecondsMax float64
	MaxSeconds       float64 // hard cap; 0 = no cap (trimming handled elsewhere)
	PreferMP3        bool    // prefer mp3 over wav on an otherwise equal pick
}

// withDefaults fills in unset policy fields.
func (p SelectPolicy) withDefaults() SelectPolicy {
	if p.PreferSecondsMin <= 0 {
		p.PreferSecondsMin = PreferredSecondsMin
	}
	if p.PreferSecondsMax <= 0 {
		p.PreferSecondsMax = PreferredSecondsMax
	}
	if p.PreferSecondsMax < p.PreferSecondsMin {
		p.PreferSecondsMax = p.PreferSecondsMin
	}
	return p
}

// widenTiers are the length windows tried in order. The first window holding
// any recording wins, so a 25-second song still yields an example call — it
// just gets trimmed later.
func widenTiers(p SelectPolicy) [][2]float64 {
	tiers := [][2]float64{
		{p.PreferSecondsMin, p.PreferSecondsMax},
		{p.PreferSecondsMax, 20},
		{20, 45},
	}
	if p.MaxSeconds > 0 {
		tiers = append([][2]float64{{p.PreferSecondsMin, p.MaxSeconds}}, tiers...)
	}
	// Any length, as a last resort.
	return append(tiers, [2]float64{0, 1 << 30})
}

// ratingRank orders quality letters best first; unknown sits after "good" but
// before the explicitly weak ones, because an unrated field recording is
// usually fine and XC's unrated majority includes most new uploads.
func ratingRank(rating string) int {
	switch rating {
	case RatingExcellent:
		return 0
	case RatingVeryGood:
		return 1
	case RatingGood:
		return 2
	case RatingUnknown:
		return 3
	case RatingFair:
		return 4
	case RatingPoor:
		return 5
	default:
		return 3
	}
}

// recordingIDOrder sorts by numeric recording id, lowest first (oldest upload,
// which on Xeno-Canto correlates with the vetted, heavily-rated classics).
// Non-numeric ids sort after numeric ones, then lexically.
func recordingIDOrder(a, b *Sound) bool {
	an, aerr := strconv.ParseInt(a.RecordingID, 10, 64)
	bn, berr := strconv.ParseInt(b.RecordingID, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		if an != bn {
			return an < bn
		}
	case aerr == nil:
		return true
	case berr == nil:
		return false
	case a.RecordingID != b.RecordingID:
		return a.RecordingID < b.RecordingID
	}
	return false
}

// betterThan reports whether a beats b under policy p, ignoring length.
func betterThan(a, b *Sound, p SelectPolicy) bool {
	if ra, rb := ratingRank(a.Rating), ratingRank(b.Rating); ra != rb {
		return ra < rb
	}
	if p.PreferMP3 {
		if a.Format != b.Format {
			if a.Format == "mp3" {
				return true
			}
			if b.Format == "mp3" {
				return false
			}
		}
	}
	return recordingIDOrder(a, b)
}

// PickBest chooses the example call for a species.
//
// Ladder: recordings inside the preferred length window win; if none are in it
// the window widens (up to 20 s, then 45 s, then any length). Within the
// winning window the best-rated recording wins, ties prefer mp3 when asked and
// otherwise the lowest recording id, so the pick never depends on the order the
// provider happened to return.
//
// Recordings longer than FetchLengthLimitSeconds are ignored: they would be
// trimmed to a fraction of their length anyway and waste bandwidth.
func PickBest(sounds []Sound, p SelectPolicy) (Sound, bool) {
	p = p.withDefaults()

	pool := make([]*Sound, 0, len(sounds))
	for i := range sounds {
		s := &sounds[i]
		if s.DownloadURL == "" {
			continue // nothing to play
		}
		if s.LengthSeconds > 0 && s.LengthSeconds > FetchLengthLimitSeconds {
			continue
		}
		pool = append(pool, s)
	}
	if len(pool) == 0 {
		return Sound{}, false
	}

	for _, tier := range widenTiers(p) {
		var best []*Sound
		for _, s := range pool {
			length := s.LengthSeconds
			if length <= 0 {
				// Unknown length: only acceptable in the catch-all window,
				// where anything is better than no example at all.
				continue
			}
			if length >= tier[0] && length <= tier[1] {
				best = append(best, s)
			}
		}
		if len(best) == 0 {
			continue
		}
		sort.SliceStable(best, func(i, j int) bool {
			return betterThan(best[i], best[j], p)
		})
		return *best[0], true
	}

	// Only unknown-length recordings survived the pool filter.
	return Sound{}, false
}

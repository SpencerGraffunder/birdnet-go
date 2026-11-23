package speciesaudio

import (
	"math/rand"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// cand is a compact candidate builder for selection tests.
func cand(id, rating, format string, seconds float64) Sound {
	return Sound{
		RecordingID:   id,
		Rating:        rating,
		Format:        format,
		LengthSeconds: seconds,
		DownloadURL:   "https://xeno-canto.org/sounds/uploaded/X/XC" + id + "." + format,
	}
}

func TestPickBestEmptyInput(t *testing.T) {
	_, ok := PickBest(nil, SelectPolicy{})
	assert.False(t, ok)

	_, ok = PickBest([]Sound{}, SelectPolicy{PreferMP3: true})
	assert.False(t, ok)

	// Candidates without a downloadable file cannot be played.
	_, ok = PickBest([]Sound{{RecordingID: "1", LengthSeconds: 5}}, SelectPolicy{})
	assert.False(t, ok)
}

func TestPickBestPreferredWindowBeatsEverything(t *testing.T) {
	// A highly rated 25 s song must lose to a mediocre clip inside the
	// preferred 2..10 s window: the window is the first rung of the ladder.
	sounds := []Sound{
		cand("1", RatingExcellent, "mp3", 25),
		cand("9", RatingUnknown, "mp3", 5),
	}
	best, ok := PickBest(sounds, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "9", best.RecordingID)
}

func TestPickBestWindowWidening(t *testing.T) {
	// Nothing in 2..10: the 10..20 rung wins even though a better-rated
	// recording waits in the 20..45 rung.
	sounds := []Sound{
		cand("1", RatingExcellent, "mp3", 30),
		cand("2", RatingGood, "mp3", 15),
	}
	best, ok := PickBest(sounds, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "2", best.RecordingID)

	// Only the 20..45 rung holds candidates, and rating still rules within it.
	sounds = []Sound{
		cand("3", RatingVeryGood, "mp3", 44),
		cand("4", RatingGood, "mp3", 22),
	}
	best, ok = PickBest(sounds, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "3", best.RecordingID)

	// Last resort: any length (inside the fetch limit) is still an example.
	sounds = []Sound{
		cand("5", RatingPoor, "mp3", 100),
		cand("6", RatingExcellent, "mp3", 170),
	}
	best, ok = PickBest(sounds, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "6", best.RecordingID, "catch-all window must rank by rating too")

	// Anything beyond the fetch length limit is never downloaded just to be
	// trimmed, so a lone 200 s epic yields no example at all.
	_, ok = PickBest([]Sound{cand("7", RatingExcellent, "mp3", 200)}, SelectPolicy{})
	assert.False(t, ok)

	// A sub-2 s clip misses the preferred window and is only picked by the
	// catch-all rung.
	best, ok = PickBest([]Sound{cand("8", RatingUnknown, "mp3", 1.5)}, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "8", best.RecordingID)
}

func TestPickBestCustomPreferredWindow(t *testing.T) {
	// With a 30..60 preferred window, the 45 s clip wins the first rung even
	// though a better-rated clip sits at 5 s.
	sounds := []Sound{
		cand("1", RatingExcellent, "mp3", 5),
		cand("2", RatingPoor, "mp3", 45),
	}
	best, ok := PickBest(sounds, SelectPolicy{PreferSecondsMin: 30, PreferSecondsMax: 60})
	require.True(t, ok)
	assert.Equal(t, "2", best.RecordingID)
}

func TestPickBestMaxSecondsExtendsFirstRung(t *testing.T) {
	sounds := []Sound{
		cand("1", RatingFair, "mp3", 3),
		cand("2", RatingExcellent, "mp3", 11),
	}

	// Default rungs: only the 3 s clip is in 2..10, so it wins on rung one.
	best, ok := PickBest(sounds, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "1", best.RecordingID)

	// MaxSeconds=12 merges both clips into the first rung; rating then rules.
	best, ok = PickBest(sounds, SelectPolicy{MaxSeconds: 12})
	require.True(t, ok)
	assert.Equal(t, "2", best.RecordingID)
}

func TestPickBestRatingOrder(t *testing.T) {
	// A > B > C > unrated > D > E, all inside the preferred window.
	sounds := []Sound{
		cand("e", RatingPoor, "mp3", 5),
		cand("u", RatingUnknown, "mp3", 5),
		cand("a", RatingExcellent, "mp3", 5),
		cand("d", RatingFair, "mp3", 5),
		cand("c", RatingGood, "mp3", 5),
		cand("b", RatingVeryGood, "mp3", 5),
	}
	want := []string{"a", "b", "c", "u", "d", "e"}
	remaining := slices.Clone(sounds)
	for i, id := range want {
		best, ok := PickBest(remaining, SelectPolicy{})
		require.Truef(t, ok, "round %d", i)
		assert.Equalf(t, id, best.RecordingID, "round %d", i)
		filtered := remaining[:0:0]
		for _, s := range remaining {
			if s.RecordingID != id {
				filtered = append(filtered, s)
			}
		}
		remaining = filtered
	}
}

func TestPickBestMP3Preference(t *testing.T) {
	sounds := []Sound{
		cand("100", RatingUnknown, "wav", 5),
		cand("900", RatingUnknown, "mp3", 5),
	}

	best, ok := PickBest(sounds, SelectPolicy{PreferMP3: true})
	require.True(t, ok)
	assert.Equal(t, "900", best.RecordingID, "mp3 must win the tie")

	best, ok = PickBest(sounds, SelectPolicy{PreferMP3: false})
	require.True(t, ok)
	assert.Equal(t, "100", best.RecordingID, "without PreferMP3 the lowest id wins")

	// Rating still outranks format.
	rated := []Sound{
		cand("100", RatingExcellent, "wav", 5),
		cand("900", RatingGood, "mp3", 5),
	}
	best, ok = PickBest(rated, SelectPolicy{PreferMP3: true})
	require.True(t, ok)
	assert.Equal(t, "100", best.RecordingID)
}

func TestPickBestTieBreakIsDeterministic(t *testing.T) {
	base := []Sound{
		cand("543338", RatingGood, "mp3", 5),
		cand("111", RatingGood, "mp3", 5),
		cand("2222", RatingGood, "mp3", 5),
		cand("33", RatingGood, "mp3", 5),
	}

	best, ok := PickBest(base, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "33", best.RecordingID, "lowest numeric id breaks ties")

	rng := rand.New(rand.NewSource(1))
	for i := range 50 {
		shuffled := slices.Clone(base)
		rng.Shuffle(len(shuffled), func(a, b int) { shuffled[a], shuffled[b] = shuffled[b], shuffled[a] })
		got, ok := PickBest(shuffled, SelectPolicy{})
		require.True(t, ok)
		require.Equalf(t, best.RecordingID, got.RecordingID, "pick must not depend on input order (permutation %d)", i)
	}

	// Non-numeric ids sort after numeric ones.
	mixed := []Sound{
		cand("abc", RatingGood, "mp3", 5),
		cand("500", RatingGood, "mp3", 5),
	}
	best, ok = PickBest(mixed, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "500", best.RecordingID)
}

// TestPickBestUnknownLengthIsLastResort pins the documented behaviour: unknown
// lengths lose to anything with a published length, but they are still usable
// when nothing else exists. Discarding them everywhere made a provider whose
// length field the parser could not read look completely empty.
func TestPickBestUnknownLengthIsLastResort(t *testing.T) {
	// A published length wins even when the unknown-length candidate is rated
	// higher.
	best, ok := PickBest([]Sound{
		cand("1", RatingExcellent, "mp3", 0),
		cand("2", RatingGood, "mp3", 5),
	}, SelectPolicy{})
	require.True(t, ok)
	assert.Equal(t, "2", best.RecordingID, "a known length in the preferred window beats an unrated unknown")

	// Nothing published: the best-rated unknown is picked rather than nothing.
	best, ok = PickBest([]Sound{
		cand("3", RatingGood, "mp3", 0),
		cand("4", RatingExcellent, "wav", 0),
	}, SelectPolicy{})
	require.True(t, ok, "unknown lengths must still serve as the last resort")
	assert.Equal(t, "4", best.RecordingID)

	// Over-long recordings are still rejected outright, length unknown or not.
	_, ok = PickBest([]Sound{{RecordingID: "5", DownloadURL: "https://x/y.mp3", LengthSeconds: 500}}, SelectPolicy{})
	assert.False(t, ok)
}

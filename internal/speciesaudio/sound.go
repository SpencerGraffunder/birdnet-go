// sound.go: the resolved example call for one species and the error
// vocabulary shared by the providers and the cache.
//
// A Sound is a candidate recording offered by a provider; an Entry (cache.go)
// is what this package actually serves: a file already on local disk with its
// attribution sidecar.
package speciesaudio

import "github.com/tphakala/birdnet-go/internal/errors"

// Errors returned by this package. The HTTP layer maps them to status codes,
// so keep them coarse and never wrap them with text that could contain the
// Xeno-Canto API key.
var (
	// ErrSoundNotFound means no usable example recording exists for the species
	// (or the species was already looked up and recorded as a miss).
	ErrSoundNotFound = errors.NewStd("no example recording found for species")

	// ErrProviderUnavailable means Xeno-Canto could not be reached or answered
	// in a way this package cannot use (transport error, unexpected status, bot
	// wall). Callers should keep serving whatever is already cached.
	ErrProviderUnavailable = errors.NewStd("xeno-canto unavailable")

	// ErrKeyRejected means Xeno-Canto refused the configured API key (HTTP
	// 401/403). The key itself is never included in the error.
	ErrKeyRejected = errors.NewStd("xeno-canto rejected the API key")

	// ErrRateLimited means Xeno-Canto answered HTTP 429; back off and retry later.
	ErrRateLimited = errors.NewStd("xeno-canto rate limit reached")

	// ErrInvalidName means the species name is empty, malformed, or hostile
	// (it could escape the cache directory when turned into a file name).
	ErrInvalidName = errors.NewStd("invalid species name")
)

// Quality letters as published on a recording page, best first.
const (
	RatingExcellent = "A"
	RatingVeryGood  = "B"
	RatingGood      = "C"
	RatingFair      = "D"
	RatingPoor      = "E"
	RatingUnknown   = ""
)

// Sound is one candidate recording offered by a provider.
type Sound struct {
	RecordingID    string  // Xeno-Canto recording number, e.g. "543338"
	ScientificName string  // "Corvus brachyrhynchos"
	CommonName     string  // "American Crow"
	Recordist      string  // attribution name as published
	LicenseName    string  // e.g. "Creative Commons Attribution-NonCommercial-ShareAlike 4.0"
	LicenseURL     string  // license deed URL, empty when unknown
	PageURL        string  // human page for the recording
	DownloadURL    string  // direct file URL (no key required)
	PlaybackURL    string  // Xeno-Canto's generated mp3 preview, when the page offers one
	Format         string  // "mp3", "wav", or "" when the provider does not say
	LengthSeconds  float64 // clip length as published
	Rating         string  // RatingExcellent..RatingPoor, RatingUnknown when unrated
}

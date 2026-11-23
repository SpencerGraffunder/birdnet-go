// species_sound_test.go: the URL contract between the two example-call routes.
//
// The pair has to agree with each other: GetSpeciesSoundInfo publishes an
// audioURL and ServeSpeciesSound has to answer it. That is this file's whole
// reason for existing — the publish/accept pair drifted (info published the
// cache slug, "…/species-sound/anser_anser", while the parameter validator only
// accepted the scientific name) and the play button followed the published URL
// straight into a 400. The cache and the providers are covered in
// internal/speciesaudio; nothing here needs the network or the filesystem.
//
// Not parallel-safe: apitest.NewCore publishes the process-global settings
// snapshot the media handlers read.

package media

import (
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/labstack/echo/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/tphakala/birdnet-go/internal/api/v2/apitest"
	"github.com/tphakala/birdnet-go/internal/speciesaudio"
)

// clipRoutePrefix is the path the clip is served from; the published audioURL
// and the parameter validator meet at the segment after it.
const clipRoutePrefix = "/api/v2/media/species-sound/"

// callNameParamResult is what one validation call produced: the resolved
// species name and the status the route wrote, which is the half the client
// actually sees — Core.HandleError writes the response and reports nil, so the
// recorder is the only place a rejection is observable.
type callNameParamResult struct {
	name string
	code int
	err  error
}

// callNameParam runs the route's parameter validation for one raw path segment
// the way Echo hands it over.
func callNameParam(t *testing.T, raw string) callNameParamResult {
	t.Helper()

	h := New(apitest.NewCore(t), nil)
	e := echo.New()
	rec := httptest.NewRecorder()
	c := e.NewContext(
		httptest.NewRequest(http.MethodGet, clipRoutePrefix+"_", http.NoBody),
		rec,
	)
	c.SetParamNames("scientific_name")
	c.SetParamValues(raw)

	name, err := h.speciesSoundNameParam(c)
	return callNameParamResult{name: name, code: rec.Code, err: err}
}

// TestPublishedAudioURLIsAcceptedByTheClipRoute is the regression guard for the
// playback failure: whatever /info publishes must be a URL this handler accepts
// and resolves to the same cache entry.
func TestPublishedAudioURLIsAcceptedByTheClipRoute(t *testing.T) {
	for _, scientificName := range []string{
		"Anser anser",
		"Hylocichla mustelina",
		"Setophaga coronata coronata", // subspecies fold to the binomial
	} {
		slug, err := speciesaudio.Slug(scientificName)
		require.NoError(t, err)

		audioURL := speciesSoundAudioURL(slug)
		raw, found := strings.CutPrefix(audioURL, clipRoutePrefix)
		require.True(t, found, "published URL must sit under the clip route: %s", audioURL)
		require.NotEmpty(t, raw)

		got := callNameParam(t, raw)
		require.NoErrorf(t, got.err, "clip route rejected the URL /info publishes (%s)", audioURL)
		require.Equal(t, http.StatusOK, got.code, "published URL must not be answered with an error")

		resolved, err := speciesaudio.Slug(got.name)
		require.NoError(t, err)
		assert.Equal(t, slug, resolved, "both spellings must land on the same cache entry")
	}
}

// TestSpeciesSoundNameParamAcceptsSpelledOutNames keeps the other legitimate
// spelling: the detail panel knows the scientific name, and so does any
// hand-written link.
func TestSpeciesSoundNameParamAcceptsSpelledOutNames(t *testing.T) {
	got := callNameParam(t, "Anser anser")
	require.NoError(t, got.err)
	assert.Equal(t, "Anser anser", got.name)
}

// TestSpeciesSoundNameParamRejectsNonNames keeps the file-name path closed: the
// slug form is convenient, not a licence to reach outside the cache directory.
func TestSpeciesSoundNameParamRejectsNonNames(t *testing.T) {
	for _, raw := range []string{
		"",
		"   ",
		"..",
		"..%2f..",
		"sounds/anser_anser",
		`anser\anser`,
		"_",
		"anser",         // not a binomial
		"anser_",        // trailing separator
		"_anser",        // leading separator
		"anser__anser",  // doubled separator
		"anser_anser_x", // folds to a different entry than it spells
		"ANser_anSer",   // the cache never writes uppercase
	} {
		got := callNameParam(t, raw)
		require.Errorf(t, got.err, "%q must not resolve to a cache entry", raw)
		assert.Equal(t, http.StatusBadRequest, got.code, "%q must be answered with 400", raw)
		assert.Empty(t, got.name, "%q must not yield a species name", raw)
	}
}

// TestSpeciesSoundAudioURLShape pins the published URL to one path segment, so
// a name can never escape into a different route or a nested path.
func TestSpeciesSoundAudioURLShape(t *testing.T) {
	for _, scientificName := range []string{"Anser anser", "Turdus migratorius"} {
		slug, err := speciesaudio.Slug(scientificName)
		require.NoError(t, err)

		audioURL := speciesSoundAudioURL(slug)
		assert.Equal(t, clipRoutePrefix+slug, audioURL)
		assert.NotContains(t, strings.TrimPrefix(audioURL, clipRoutePrefix), "/",
			"a published URL must stay inside one path segment")
		assert.Equal(t, slug, path.Base(audioURL))
	}
}

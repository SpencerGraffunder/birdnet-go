package speciesaudio

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestProvidersNeverSendABlankUserAgent pins the wire-level behaviour, not the
// struct field: Xeno-Canto answers a request whose User-Agent is empty or
// absent with an HTML error page at HTTP 200, so a blank UA does not fail
// loudly — it turns every lookup into "could not be reached" while the network
// and the API key are both fine. That is how the Settings test button failed
// against a valid key: probeXenoCanto built its provider with NewAPIProvider
// (key, ""), and only the cache path resolved a default.
func TestProvidersNeverSendABlankUserAgent(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"recordings":[]}`)
	}))
	t.Cleanup(srv.Close)

	// Built exactly the way an ad-hoc caller builds it: no UA argument.
	api := NewAPIProvider("test-key", "").(*apiProvider)
	api.endpoint = srv.URL + "/api/3/recordings"
	api.client = srv.Client()
	api.pacer = &pacer{minSpace: 0}
	_, _ = api.Search(t.Context(), "Turdus migratorius") // the empty result is not the point

	require.NotEmpty(t, gotUA, "an empty User-Agent must never reach the wire")
	assert.Equal(t, defaultUserAgent(), gotUA)

	page := NewPageProvider("").(*pageProvider)
	page.endpoint = srv.URL
	page.client = srv.Client()
	page.pacer = &pacer{minSpace: 0}
	gotUA = ""
	_, _ = page.Search(t.Context(), "Turdus migratorius")

	require.NotEmpty(t, gotUA, "the keyless provider must not send a blank User-Agent either")
	assert.Equal(t, defaultUserAgent(), gotUA)
}

// TestProvidersPassACallerSuppliedUserAgentThrough: the default is a fallback,
// not an override — an embedder that identifies itself keeps its own string.
func TestProvidersPassACallerSuppliedUserAgentThrough(t *testing.T) {
	var gotUA string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"recordings":[]}`)
	}))
	t.Cleanup(srv.Close)

	const custom = "BirdNETGo/9.9 (unit test)"
	api := NewAPIProvider("test-key", custom).(*apiProvider)
	api.endpoint = srv.URL + "/api/3/recordings"
	api.client = srv.Client()
	api.pacer = &pacer{minSpace: 0}
	_, _ = api.Search(t.Context(), "Turdus migratorius")

	assert.Equal(t, custom, gotUA)
}

// TestAPIProviderHTMLPageIsAnOutageNotAMissingSpecies: the bot/error page must
// stay a retryable outage. Reported as "no recording" instead, the cache writes
// a permanent marker and that species never gets an example call again.
func TestAPIProviderHTMLPageIsAnOutageNotAMissingSpecies(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = fmt.Fprint(w, "<!doctype html><html><head><title>Something went wrong</title></head><body>hi</body></html>")
	}))
	t.Cleanup(srv.Close)

	p := NewAPIProvider("test-key", "").(*apiProvider)
	p.endpoint = srv.URL + "/api/3/recordings"
	p.client = srv.Client()
	p.pacer = &pacer{minSpace: 0}

	_, err := p.Search(t.Context(), "Turdus migratorius")
	require.Error(t, err)
	require.ErrorIs(t, err, ErrProviderUnavailable)
	require.NotErrorIs(t, err, ErrSoundNotFound, "an error page must not blackhole the species")
	assert.Contains(t, err.Error(), "HTML page")
}

// TestDefaultUserAgentIsUsable guards the shape Xeno-Canto expects: a product
// token, a contact URL, and no blank.
func TestDefaultUserAgentIsUsable(t *testing.T) {
	ua := defaultUserAgent()
	assert.NotEmpty(t, ua)
	assert.Contains(t, ua, "BirdNETGo/")
	assert.Contains(t, ua, "github.com")
	assert.Equal(t, ua, defaultUserAgentOr(""))
	assert.Equal(t, "  odd  ", defaultUserAgentOr("  odd  "), "a supplied UA is used verbatim")
}

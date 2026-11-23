package speciesaudio

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// listingFixture is modeled on the verified live row shape of a Xeno-Canto
// species page (2026-11-20): inline mini-player with the generated mp3/wav,
// name cell with common-name/sci-name spans, bare length cell, contributor
// link, download link, rating list, and the CC license span. Row 1 carries an
// A rating via class='selected', row 2 is unrated (anonymous pages publish no
// marks), row 3 carries a C rating via class='current'. Row 4 is a different
// species and must be filtered out.
const listingFixture = `<!DOCTYPE html>
<html lang="en"><head><title>American Crow (Corvus brachyrhynchos) · Xeno-Canto</title></head>
<body>
<table class="recordings">
<tbody>
<tr>
<td><div class='xc-mini-audio'><audio class='xc-mini-player' id='xc_audio_543338_368' src='https://xeno-canto.org/sounds/uploaded/GVVNEJJEGA/XC543338-American-Crow.mp3' preload='metadata'></audio></div></td>
<td><span class="common-name"><a href='/species/Corvus-brachyrhynchos'>American Crow</a></span> <span class='sci-name'>Corvus brachyrhynchos</span></td>
<td> 0:19</td>
<td> <a href='/contributor/GVVNEJJEGA'>Thomas Magarian</a></td>
<td><a href='https://xeno-canto.org/543338/download'><img src='/img/download.svg' title='Download file &#039;XC543338 - American Crow - Corvus brachyrhynchos.mp3&#039;'></a><div class='rating'><ul><li id='rating-543338-1' class='selected'><span>A</span></li><li id='rating-543338-2' class=''><span>B</span></li><li id='rating-543338-5' class=''><span>E</span></li></ul></div></td>
<td style='white-space: nowrap;'><a href="https://xeno-canto.org/543338">XC543338 <span title="Creative Commons Attribution-NonCommercial-ShareAlike 4.0"><a href="https://creativecommons.org/licenses/by-nc-sa/4.0/"><img src='/img/cc-by-nc-sa.svg'></a></span></a></td>
</tr>
<tr>
<td><div class='xc-mini-audio'><audio class='xc-mini-player' id='xc_audio_544444_120' src='https://xeno-canto.org/sounds/uploaded/CDWRQMOYRS/XC544444-American-Crow.wav' preload='metadata'></audio></div></td>
<td><span class="common-name"><a href='/species/Corvus-brachyrhynchos'>American Crow</a></span> <span class='sci-name'>Corvus brachyrhynchos</span></td>
<td> 0:08</td>
<td> <a href='/contributor/CDWRQMOYRS'>Ana Ribeiro</a></td>
<td><a href='https://xeno-canto.org/544444/download'><img src='/img/download.svg' title='Download file &#039;XC544444 - American Crow - Corvus brachyrhynchos.wav&#039;'></a><div class='rating'><ul><li id='rating-544444-1' class=''><span>A</span></li><li id='rating-544444-2' class=''><span>B</span></li></ul></div></td>
<td style='white-space: nowrap;'><a href="https://xeno-canto.org/544444">XC544444 <span title="Creative Commons Attribution 4.0"><a href="https://creativecommons.org/licenses/by/4.0/"><img src='/img/cc-by.svg'></a></span></a></td>
</tr>
<tr>
<td><div class='xc-mini-audio'><audio class='xc-mini-player' id='xc_audio_555555_77' src='https://xeno-canto.org/sounds/uploaded/TUWXXKHZRT/XC555555-American-Crow.mp3' preload='metadata'></audio></div></td>
<td><span class="common-name"><a href='/species/Corvus-brachyrhynchos'>American Crow</a></span> <span class='sci-name'>Corvus brachyrhynchos</span></td>
<td> 0:45</td>
<td> <a href='/contributor/TUWXXKHZRT'>Karl-Heinz Dingler</a></td>
<td><a href='https://xeno-canto.org/555555/download'><img src='/img/download.svg' title='Download file &#039;XC555555 - American Crow - Corvus brachyrhynchos.mp3&#039;'></a><div class='rating'><ul><li id='rating-555555-2' class=''><span>B</span></li><li id='rating-555555-3' class='current'><span>C</span></li></ul></div></td>
<td style='white-space: nowrap;'><a href="https://xeno-canto.org/555555">XC555555 <span title="Creative Commons Attribution-NonCommercial 2.5"><a href="https://creativecommons.org/licenses/by-nc/2.5/"><img src='/img/cc-by-nc.svg'></a></span></a></td>
</tr>
<tr>
<td><div class='xc-mini-audio'><audio class='xc-mini-player' id='xc_audio_566666_9' src='https://xeno-canto.org/sounds/uploaded/VWXYZABCDE/XC566666-Fish-Crow.mp3' preload='metadata'></audio></div></td>
<td><span class="common-name"><a href='/species/Corvus-ossifragus'>Fish Crow</a></span> <span class='sci-name'>Corvus ossifragus</span></td>
<td> 0:12</td>
<td> <a href='/contributor/VWXYZABCDE'>Other Recordist</a></td>
<td><a href='https://xeno-canto.org/566666/download'><img src='/img/download.svg' title='Download file'></a><div class='rating'><ul><li id='rating-566666-1' class=''><span>A</span></li></ul></div></td>
<td style='white-space: nowrap;'><a href="https://xeno-canto.org/566666">XC566666 <span title="Creative Commons Attribution 4.0"><a href="https://creativecommons.org/licenses/by/4.0/"><img src='/img/cc-by.svg'></a></span></a></td>
</tr>
</tbody>
</table>
</body></html>`

func newPageTestProvider(t *testing.T, handler http.HandlerFunc) (*pageProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &pageProvider{
		userAgent: testUserAgent,
		endpoint:  srv.URL,
		client:    srv.Client(),
		pacer:     &pacer{minSpace: 0},
	}, srv
}

func serveHTML(body string, code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(body))
	}
}

func TestPageProviderFetchesSpeciesPage(t *testing.T) {
	var gotPath, gotUA string
	p, _ := newPageTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotUA = r.URL.Path, r.Header.Get("User-Agent")
		_, _ = w.Write([]byte(listingFixture))
	})

	_, err := p.Search(t.Context(), "corvus brachyrhynchos")
	require.NoError(t, err)
	assert.Equal(t, "/species/Corvus-brachyrhynchos", gotPath)
	assert.Equal(t, testUserAgent, gotUA)
}

func TestPageProviderParsesVerifiedRowShape(t *testing.T) {
	p, _ := newPageTestProvider(t, serveHTML(listingFixture, http.StatusOK))

	sounds, err := p.Search(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	// The Fish Crow row must be filtered out even though it parses fine.
	require.Len(t, sounds, 3)

	s0, s1, s2 := sounds[0], sounds[1], sounds[2]

	assert.Equal(t, "543338", s0.RecordingID)
	assert.Equal(t, "Corvus brachyrhynchos", s0.ScientificName)
	assert.Equal(t, "American Crow", s0.CommonName)
	assert.Equal(t, "Thomas Magarian", s0.Recordist)
	assert.InDelta(t, 19.0, s0.LengthSeconds, 1e-9)
	assert.Equal(t, RatingExcellent, s0.Rating) // class='selected'
	assert.Equal(t, "mp3", s0.Format)
	// The generated /sounds/uploaded mp3 wins over the /download link.
	assert.Equal(t, "https://xeno-canto.org/sounds/uploaded/GVVNEJJEGA/XC543338-American-Crow.mp3", s0.PlaybackURL)
	assert.Equal(t, s0.PlaybackURL, s0.DownloadURL)
	assert.Equal(t, "https://xeno-canto.org/543338", s0.PageURL)
	assert.Equal(t, "Creative Commons Attribution-NonCommercial-ShareAlike 4.0", s0.LicenseName)
	assert.Equal(t, "https://creativecommons.org/licenses/by-nc-sa/4.0/", s0.LicenseURL)

	// Anonymous listing pages publish no rating marks: unrated is normal.
	assert.Equal(t, "544444", s1.RecordingID)
	assert.Equal(t, RatingUnknown, s1.Rating)
	assert.InDelta(t, 8.0, s1.LengthSeconds, 1e-9)
	assert.Equal(t, "Ana Ribeiro", s1.Recordist)
	assert.Equal(t, "wav", s1.Format)
	assert.Equal(t, "https://xeno-canto.org/sounds/uploaded/CDWRQMOYRS/XC544444-American-Crow.wav", s1.DownloadURL)

	assert.Equal(t, "555555", s2.RecordingID)
	assert.Equal(t, RatingGood, s2.Rating) // class='current'
	assert.InDelta(t, 45.0, s2.LengthSeconds, 1e-9)
	assert.Equal(t, "Karl-Heinz Dingler", s2.Recordist)
	assert.Equal(t, "https://creativecommons.org/licenses/by-nc/2.5/", s2.LicenseURL)

	for _, s := range sounds {
		assert.NotEqual(t, "566666", s.RecordingID, "other species leaked through")
	}
}

func TestPageProviderAnubisWallRejected(t *testing.T) {
	bodies := []string{
		`<!DOCTYPE html><html><body><center><h1>Verifying visitor request</h1>` +
			`<script>/* anubis challenge */</script></body></html>`,
		`<html><body><div>Anubis is checking your browser</div>` +
			`<a href="https://xeno-canto.org/123/download">decoy</a></body></html>`,
		// What xeno-canto actually serves to keyless clients since it moved
		// tiers: a Greywall interstitial whose title is the only clue.
		`<!doctype html><html><head><title>Greywall</title>` +
			`<meta name="greywall:challenge" content="1"></head>` +
			`<body><noscript>Enable JavaScript</noscript></body></html>`,
		`<!doctype html><html><head><title>Attention Required</title></head><body>` +
			`<div class="cf-turnstile">verifying you are human</div></body></html>`,
	}
	for _, body := range bodies {
		p, _ := newPageTestProvider(t, serveHTML(body, http.StatusOK))
		_, err := p.Search(t.Context(), "Corvus brachyrhynchos")
		require.ErrorIs(t, err, ErrProviderUnavailable)
		require.NotErrorIs(t, err, ErrSoundNotFound,
			"a challenge page must not be read as \"this species has no recordings\"")
		assert.NotContains(t, err.Error(), "123")
	}
}

func TestPageProviderMalformedHTMLNoPanic(t *testing.T) {
	malformed := `<html><body><table><tr><td><a href="https://xeno-canto.org/999999/download">DL` +
		`</td<><<<span class=unclosed><td> n:o:t::a::clock</td>`
	p, _ := newPageTestProvider(t, serveHTML(malformed, http.StatusOK))

	var sounds []Sound
	var err error
	require.NotPanics(t, func() {
		sounds, err = p.Search(t.Context(), "Corvus brachyrhynchos")
	})
	assert.Nil(t, sounds)
	require.ErrorIs(t, err, ErrSoundNotFound)
}

func TestPageProviderStatusCodes(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusNotFound, `<html><body>Species not found</body></html>`, ErrSoundNotFound},
		{http.StatusTooManyRequests, `slow down`, ErrRateLimited},
		{http.StatusInternalServerError, `boom`, ErrProviderUnavailable},
		{http.StatusOK, `<html><body><p>No recordings found.</p></body></html>`, ErrSoundNotFound},
	}
	for _, tc := range cases {
		status, body := tc.status, tc.body
		p, _ := newPageTestProvider(t, serveHTML(body, status))
		_, err := p.Search(t.Context(), "Corvus brachyrhynchos")
		require.Errorf(t, err, "status %d must fail", status)
		require.ErrorIsf(t, err, tc.want, "status %d", status)
	}
}

func TestPageProviderInvalidNameNeverRequests(t *testing.T) {
	var requests atomic.Int64
	p, _ := newPageTestProvider(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
	})
	for _, name := range []string{"", " ", "Corvus", "../../etc/passwd", "Corvus brachyrhynchos?", "Corvus brachyrhynchos1"} {
		_, err := p.Search(t.Context(), name)
		require.ErrorIsf(t, err, ErrInvalidName, "name %q must be rejected", name)
	}
	assert.Zero(t, requests.Load())
}

package speciesaudio

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	testUserAgent = "speciesaudio-test/1.0 (unit tests)"
	testAPIKey    = "unit-test-key-9f3a"
)

// newAPITestProvider builds an apiProvider pointed at handler so tests never
// touch the network and the pacer never sleeps.
func newAPITestProvider(t *testing.T, apiKey string, handler http.HandlerFunc) (*apiProvider, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return newAPIProviderAt(apiKey, srv), srv
}

func newAPIProviderAt(apiKey string, srv *httptest.Server) *apiProvider {
	return &apiProvider{
		apiKey:    apiKey,
		userAgent: testUserAgent,
		endpoint:  srv.URL + "/api/3/recordings",
		client:    srv.Client(),
		pacer:     &pacer{minSpace: 0},
	}
}

func TestProviderNames(t *testing.T) {
	require.Equal(t, "api", NewAPIProvider("k", testUserAgent).Name())
	require.Equal(t, "page", NewPageProvider(testUserAgent).Name())
}

func TestAPIProviderQueryShape(t *testing.T) {
	var gotQuery, gotKey, gotPage, gotAccept, gotUA string
	handler := func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		gotQuery, gotKey, gotPage = q.Get("query"), q.Get("key"), q.Get("page")
		gotAccept, gotUA = r.Header.Get("Accept"), r.Header.Get("User-Agent")
		_, _ = fmt.Fprint(w, `{"recordings":[{"id":"543338","gen":"Corvus","sp":"brachyrhynchos",`+
			`"file":"https://xeno-canto.org/543338/download","len":"0:23"}]}`)
	}
	p, _ := newAPITestProvider(t, testAPIKey, handler)

	sounds, err := p.Search(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	require.Len(t, sounds, 1)

	// The query is the binomial plus a length window in SECONDS, and it must
	// never carry an nr: clause. nr: is a recording-number range: combining it
	// with sp: makes Xeno-Canto answer "no recordings" for species that have
	// hundreds, and the cache remembered that verdict for good.
	assert.Equal(t,
		fmt.Sprintf("sp:%q len:%d-%d", "corvus brachyrhynchos", APISearchMinSeconds, FetchLengthLimitSeconds),
		gotQuery)
	assert.NotContains(t, gotQuery, "nr:")
	assert.Equal(t, testAPIKey, gotKey)
	assert.Equal(t, "1", gotPage)
	assert.Equal(t, "application/json", gotAccept)
	assert.Equal(t, testUserAgent, gotUA)

	// Odd casing and subspecies names fold to the same indexed binomial.
	_, err = p.Search(t.Context(), "  CORVUS BRACHYRHYNCHOS orientalis ")
	require.NoError(t, err)
	assert.Equal(t,
		fmt.Sprintf("sp:%q len:%d-%d", "corvus brachyrhynchos", APISearchMinSeconds, FetchLengthLimitSeconds),
		gotQuery)
}

func TestAPIProviderLenientDecoding(t *testing.T) {
	body := `{
	  "numRecordings": "3",
	  "numPages": 1,
	  "page": "1",
	  "recordings": [
	    {"id":"543338","gen":"Corvus","sp":"brachyrhynchos","cnt":"American Crow",
	     "len":"0:23","rec":"Thomas Magarian",
	     "url":"https://xeno-canto.org/543338",
	     "file":"https://xeno-canto.org/543338/download"},
	    {"id":599999,"gen":"Corvus","sp":"brachyrhynchos","cnt":"American Crow",
	     "len":45,"rec":"Jane Doe","r":5,
	     "url":"https://xeno-canto.org/599999",
	     "file":"https://xeno-canto.org/599999/XC599999.wav"},
	    {"id":600000,"len":"90","r":"4",
	     "file":"https://xeno-canto.org/600000/download"},
	    {"id":600001,"len":"1:05:30","file":"https://xeno-canto.org/600001/download"},
	    {"id":600002,"file":""}
	  ]
	}`
	p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, body)
	})

	sounds, err := p.Search(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	// The recording with an empty file URL has nothing to play and is dropped.
	require.Len(t, sounds, 4)

	first := sounds[0]
	assert.Equal(t, "543338", first.RecordingID)
	assert.Equal(t, "Corvus brachyrhynchos", first.ScientificName)
	assert.Equal(t, "American Crow", first.CommonName)
	assert.Equal(t, "Thomas Magarian", first.Recordist)
	assert.InDelta(t, 23.0, first.LengthSeconds, 1e-9) // "0:23"
	assert.Empty(t, first.Rating)                      // absent optional field
	assert.Equal(t, "https://xeno-canto.org/543338", first.PageURL)
	assert.Equal(t, "https://xeno-canto.org/543338/download", first.DownloadURL)
	assert.Empty(t, first.Format) // the /download URL carries no extension

	second := sounds[1]
	assert.Equal(t, "599999", second.RecordingID) // numeric id accepted
	assert.InDelta(t, 45.0, second.LengthSeconds, 1e-9)
	assert.Equal(t, RatingExcellent, second.Rating) // r=5 maps to A
	assert.Equal(t, "wav", second.Format)

	third := sounds[2]
	assert.InDelta(t, 90.0, third.LengthSeconds, 1e-9) // plain seconds as string
	assert.Equal(t, RatingVeryGood, third.Rating)      // "r":"4" arrives as a string
	assert.Empty(t, third.CommonName)                  // every optional field may be absent

	fourth := sounds[3]
	assert.InDelta(t, 3930.0, fourth.LengthSeconds, 1e-9) // "hh:mm:ss"
}

func TestAPIProviderStatusMapping(t *testing.T) {
	cases := []struct {
		status int
		body   string
		want   error
	}{
		{http.StatusUnauthorized, `{"error":"invalid key"}`, ErrKeyRejected},
		{http.StatusForbidden, `{"error":"key revoked"}`, ErrKeyRejected},
		{http.StatusTooManyRequests, `{"error":"busy"}`, ErrRateLimited},
		{http.StatusInternalServerError, ``, ErrProviderUnavailable},
		{http.StatusServiceUnavailable, `bad gateway`, ErrProviderUnavailable},
		{http.StatusOK, `not json at all`, ErrProviderUnavailable},
		{http.StatusOK, `{"numRecordings":"0","recordings":[]}`, ErrSoundNotFound},
	}
	for _, tc := range cases {
		status, body := tc.status, tc.body
		p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
		})
		_, err := p.Search(t.Context(), "Corvus brachyrhynchos")
		require.Errorf(t, err, "status %d must fail", status)
		require.ErrorIsf(t, err, tc.want, "status %d", status)
	}
}

func TestAPIProviderKeyNeverLeaks(t *testing.T) {
	ctx := t.Context()

	t.Run("rejected key echoes nothing", func(t *testing.T) {
		p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			// A hostile body that repeats the key must still not leak it.
			_, _ = fmt.Fprintf(w, `{"error":"invalid key %s"}`, testAPIKey)
		})
		_, err := p.Search(ctx, "Corvus brachyrhynchos")
		require.ErrorIs(t, err, ErrKeyRejected)
		assert.NotContains(t, err.Error(), testAPIKey)
	})

	t.Run("error field is sanitized", func(t *testing.T) {
		p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprintf(w, `{"error":"unsupported search operator %s"}`, testAPIKey)
		})
		// An error field is a fault in the request, not a statement about the
		// species: it must never travel as ErrSoundNotFound, which the cache
		// would store as "this species has no example call".
		_, err := p.Search(ctx, "Corvus brachyrhynchos")
		require.ErrorIs(t, err, ErrProviderUnavailable)
		assert.NotContains(t, err.Error(), testAPIKey)
		assert.Contains(t, err.Error(), "[key]")
	})

	t.Run("transport failure does not echo the URL", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		dead := srv.URL
		srv.Close() // every request now fails with a url.Error that embeds the full URL

		live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		t.Cleanup(live.Close)
		p := newAPIProviderAt(testAPIKey, live)
		p.endpoint = dead + "/api/3/recordings"
		_, err := p.Search(ctx, "Corvus brachyrhynchos")
		require.ErrorIs(t, err, ErrProviderUnavailable)
		assert.NotContains(t, err.Error(), testAPIKey)
		assert.NotContains(t, err.Error(), "query=")
	})
}

func TestAPIProviderRequiresKey(t *testing.T) {
	p, _ := newAPITestProvider(t, "", func(w http.ResponseWriter, r *http.Request) {
		t.Error("provider with no key must not send requests")
	})
	_, err := p.Search(t.Context(), "Corvus brachyrhynchos")
	require.ErrorIs(t, err, ErrProviderUnavailable)
}

func TestAPIProviderInvalidName(t *testing.T) {
	var requests atomic.Int64
	p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	for _, name := range []string{"", "   ", "Corvus", "Corvus-brachyrhynchos", "../passwd", "Corvus brachyrhynchos; rm -rf /", "<i>Corvus</i> brachyrhynchos"} {
		_, err := p.Search(t.Context(), name)
		require.ErrorIsf(t, err, ErrInvalidName, "name %q must be rejected", name)
	}
	assert.Zero(t, requests.Load(), "invalid names must never reach the network")
}

// TestAPIProviderRetriesWithoutLengthWindowWhenFilteredQueryIsEmpty pins the
// two-query contract: an empty length-windowed answer is not evidence that a
// species has nothing published, because Xeno-Canto answers "nothing in this
// window" and "nothing published" the same way. The bare species query decides.
func TestAPIProviderRetriesWithoutLengthWindowWhenFilteredQueryIsEmpty(t *testing.T) {
	var queries []string
	p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query().Get("query")
		queries = append(queries, query)
		if strings.Contains(query, "len:") {
			_, _ = fmt.Fprint(w, `{"numRecordings":"0","recordings":[]}`)
			return
		}
		_, _ = fmt.Fprint(w, `{"recordings":[{"id":"77","gen":"Ardea","sp":"ibis",`+
			`"file":"https://xeno-canto.org/77/download","len":"0:08"}]}`)
	})

	sounds, err := p.Search(t.Context(), "Ardea ibis")
	require.NoError(t, err, "a species with recordings must not be reported as having none")
	require.Len(t, sounds, 1)
	require.Len(t, queries, 2, "the empty windowed answer must be checked against the bare species query")
	assert.Contains(t, queries[0], "len:")
	assert.NotContains(t, queries[1], "len:")
}

// TestAPIProviderReportsNoRecordingOnlyAfterBothQueriesComeBackEmpty keeps the
// honest negative available: a species Xeno-Canto truly does not carry must be
// answered as "none" so the cache can stop asking.
func TestAPIProviderReportsNoRecordingOnlyAfterBothQueriesComeBackEmpty(t *testing.T) {
	var requests atomic.Int32
	p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprint(w, `{"numRecordings":"0","recordings":[]}`)
	})

	_, err := p.Search(t.Context(), "Ardea ibis")
	require.ErrorIs(t, err, ErrSoundNotFound)
	assert.Equal(t, int32(2), requests.Load(), "windowed and bare query, one request each")
}

// TestAPIProviderDoesNotRetryAFailedRequest stops the fallback from turning an
// outage or a rejected request into double traffic.
func TestAPIProviderDoesNotRetryAFailedRequest(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		expected error
	}{
		{"rate limited", http.StatusTooManyRequests, `{"error":"too many"}`, ErrRateLimited},
		{"key rejected", http.StatusForbidden, `{"error":"bad key"}`, ErrKeyRejected},
		{"server error", http.StatusBadGateway, `<html>`, ErrProviderUnavailable},
		{"rejected query in a 200 body", http.StatusOK, `{"error":"only accepts queries using tags"}`, ErrProviderUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var requests atomic.Int32
			p, _ := newAPITestProvider(t, testAPIKey, func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprint(w, tc.body)
			})
			_, err := p.Search(t.Context(), "Corvus brachyrhynchos")
			require.ErrorIs(t, err, tc.expected)
			assert.Equal(t, int32(1), requests.Load(), "a failed request must not be repeated with a wider window")
		})
	}
}

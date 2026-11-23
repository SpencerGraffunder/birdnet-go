package speciesaudio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- test doubles ----------------------------------------------------------

// stubProvider counts searches per normalized binomial and answers from a
// pluggable responder. A non-nil gate makes Search block until the gate is
// closed, which is how the prefetch tests prove Warm never blocks the caller.
type stubProvider struct {
	mu      sync.Mutex
	calls   map[string]int
	respond func(bin string) ([]Sound, error)
	gate    chan struct{}
}

func newStubProvider(respond func(bin string) ([]Sound, error)) *stubProvider {
	return &stubProvider{calls: map[string]int{}, respond: respond}
}

func (s *stubProvider) Name() string { return "stub" }

func (s *stubProvider) Search(ctx context.Context, name string) ([]Sound, error) {
	if s.gate != nil {
		select {
		case <-s.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	bin, err := normalizeBinomial(name)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.calls[bin]++
	s.mu.Unlock()
	return s.respond(bin)
}

func (s *stubProvider) count(name string) int {
	bin, err := normalizeBinomial(name)
	if err != nil {
		return 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[bin]
}

func (s *stubProvider) total() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, c := range s.calls {
		n += c
	}
	return n
}

// stubDownloader answers every download with fixed bytes and no network.
type stubDownloader struct {
	mu    sync.Mutex
	calls int
	data  []byte
	mime  string
	err   error
}

func (d *stubDownloader) get(ctx context.Context, rawURL string) (data []byte, mime string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.calls++
	if d.err != nil {
		return nil, "", d.err
	}
	return d.data, d.mime, nil
}

func (d *stubDownloader) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.calls
}

// --- helpers ---------------------------------------------------------------

func newStubCache(t *testing.T, o *Options, respond func(bin string) ([]Sound, error)) (*Cache, *stubProvider, *stubDownloader) {
	t.Helper()
	if o.DataDir == "" {
		o.DataDir = t.TempDir()
	}
	if o.UserAgent == "" {
		o.UserAgent = testUserAgent
	}
	c := NewCache(*o)
	prov := newStubProvider(respond)
	dl := &stubDownloader{data: []byte("FAKE-MP3-PAYLOAD"), mime: "audio/mpeg"}
	c.api = nil
	c.page = prov
	c.download = dl
	t.Cleanup(func() { _ = c.Close() })
	return c, prov, dl
}

func soundFor(bin string) Sound {
	return Sound{
		RecordingID:    "543338",
		ScientificName: bin,
		CommonName:     "American Crow",
		Recordist:      "Thomas Magarian",
		LicenseName:    "Creative Commons Attribution-NonCommercial-ShareAlike 4.0",
		LicenseURL:     "https://creativecommons.org/licenses/by-nc-sa/4.0/",
		PageURL:        "https://xeno-canto.org/543338",
		DownloadURL:    "https://xeno-canto.org/sounds/uploaded/GVVNEJJEGA/XC543338.mp3",
		Format:         "mp3",
		LengthSeconds:  6,
		Rating:         RatingExcellent,
	}
}

func alwaysFound(bin string) ([]Sound, error) { return []Sound{soundFor(bin)}, nil }

func readSidecar(t *testing.T, c *Cache, slug string) Info {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(c.Dir(), slug+sidecarExt))
	require.NoError(t, err, "sidecar must exist after a successful store")
	var info Info
	require.NoError(t, json.Unmarshal(raw, &info), "sidecar must be valid JSON")
	return info
}

// --- Lookup: store once, serve forever --------------------------------------

func TestCacheLookupStoresThenServesFromDisk(t *testing.T) {
	dataDir := t.TempDir()
	c, prov, dl := newStubCache(t, &Options{DataDir: dataDir}, alwaysFound)
	require.Equal(t, filepath.Join(dataDir, "cache", "sounds"), c.Dir())

	entry, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)

	wantPath := filepath.Join(c.Dir(), "corvus_brachyrhynchos.mp3")
	assert.Equal(t, wantPath, entry.Path)
	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	assert.Equal(t, []byte("FAKE-MP3-PAYLOAD"), body)
	assert.Equal(t, 1, prov.count("Corvus brachyrhynchos"))
	assert.Equal(t, 1, dl.count())

	info := readSidecar(t, c, "corvus_brachyrhynchos")
	assert.Equal(t, "Corvus brachyrhynchos", info.Species)
	assert.Equal(t, "543338", info.RecordingID)
	assert.Equal(t, "stub", info.Source)
	assert.Equal(t, "Thomas Magarian", info.Recordist)
	assert.Equal(t, "https://xeno-canto.org/543338", info.PageURL)
	assert.Equal(t, "audio/mpeg", info.MimeType)
	assert.InDelta(t, 6.0, info.DurationSeconds, 1e-9)
	assert.False(t, info.Trimmed)
	require.False(t, info.FetchedAt.IsZero())

	cached, missing, queued := c.Stats()
	assert.Equal(t, 1, cached)
	assert.Equal(t, 0, missing)
	assert.Equal(t, 0, queued)

	// The second lookup must be pure disk: zero provider searches, zero
	// downloads — and it must work with a fresh Cache over the same DataDir,
	// i.e. across restarts.
	again, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	assert.Equal(t, wantPath, again.Path)
	assert.Equal(t, 1, prov.count("Corvus brachyrhynchos"))
	assert.Equal(t, 1, dl.count())

	reopened := NewCache(Options{DataDir: dataDir, UserAgent: testUserAgent})
	t.Cleanup(func() { _ = reopened.Close() })
	reopened.download = &stubDownloader{data: []byte("MUST-NOT-BE-FETCHED"), mime: "audio/mpeg"}
	fromDisk, err := reopened.Lookup(t.Context(), "corvus brachyrhynchos")
	require.NoError(t, err)
	assert.Equal(t, wantPath, fromDisk.Path)
	assert.Equal(t, "Thomas Magarian", fromDisk.Info.Recordist)
	assert.Equal(t, 0, reopened.download.(*stubDownloader).count())
}

func TestCacheMissingMarkerPreventsRequery(t *testing.T) {
	c, prov, _ := newStubCache(t, &Options{}, func(bin string) ([]Sound, error) {
		return nil, fmt.Errorf("%w: xeno-canto has nothing for %s", ErrSoundNotFound, bin)
	})

	_, err := c.Lookup(t.Context(), "Troglodytes hiemalis")
	require.ErrorIs(t, err, ErrSoundNotFound)
	assert.Equal(t, 1, prov.count("Troglodytes hiemalis"))

	marker := filepath.Join(c.Dir(), "troglodytes_hiemalis.missing")
	_, statErr := os.Stat(marker)
	require.NoError(t, statErr, "missing marker must be written")

	// Never re-queried: no provider call, still ErrSoundNotFound.
	_, err = c.Lookup(t.Context(), "Troglodytes hiemalis")
	require.ErrorIs(t, err, ErrSoundNotFound)
	assert.Equal(t, 1, prov.count("Troglodytes hiemalis"))

	cached, missing, queued := c.Stats()
	assert.Equal(t, 0, cached)
	assert.Equal(t, 1, missing)
	assert.Equal(t, 0, queued)
}

func TestCacheProviderOutageDoesNotMarkMissing(t *testing.T) {
	respond := func(bin string) ([]Sound, error) {
		return nil, fmt.Errorf("%w: xeno-canto is down", ErrProviderUnavailable)
	}
	c, prov, _ := newStubCache(t, &Options{}, respond)

	_, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.ErrorIs(t, err, ErrProviderUnavailable)
	_, statErr := os.Stat(filepath.Join(c.Dir(), "corvus_brachyrhynchos.missing"))
	require.ErrorIs(t, statErr, os.ErrNotExist, "an outage must not be recorded as a permanent miss")

	// A second lookup retries the provider.
	_, err = c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.ErrorIs(t, err, ErrProviderUnavailable)
	assert.Equal(t, 2, prov.count("Corvus brachyrhynchos"))

	// When the provider recovers, the species resolves normally.
	respond = alwaysFound
	c.page.(*stubProvider).respond = respond
	entry, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	assert.NotEmpty(t, entry.Path)
}

func TestCacheCorruptSidecarReResolves(t *testing.T) {
	c, prov, dl := newStubCache(t, &Options{}, alwaysFound)

	_, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	require.Equal(t, 1, prov.count("Corvus brachyrhynchos"))

	// Corrupt the sidecar: the entry can no longer be trusted, so the species
	// is resolved again and re-stored (not served garbage).
	sidecar := filepath.Join(c.Dir(), "corvus_brachyrhynchos.json")
	require.NoError(t, os.WriteFile(sidecar, []byte("{n:o:t json{{{"), 0o644))

	entry, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	assert.Equal(t, 2, prov.count("Corvus brachyrhynchos"), "corrupt sidecar must trigger a re-resolve")
	assert.Equal(t, 2, dl.count())
	info := readSidecar(t, c, "corvus_brachyrhynchos")
	assert.Equal(t, "Corvus brachyrhynchos", info.Species)
	assert.Equal(t, entry.Info.Species, info.Species)
}

// --- names ------------------------------------------------------------------

func TestSlugAndPageSlug(t *testing.T) {
	ok := []struct{ name, slug, pageSlug string }{
		{"Corvus brachyrhynchos", "corvus_brachyrhynchos", "Corvus-brachyrhynchos"},
		{"  corvus   BRACHYRHYNCHOS  ", "corvus_brachyrhynchos", "Corvus-brachyrhynchos"},
		{"Tyrannus tyrannus orientalis", "tyrannus_tyrannus", "Tyrannus-tyrannus"},
	}
	for _, tc := range ok {
		slug, err := Slug(tc.name)
		require.NoErrorf(t, err, "Slug(%q)", tc.name)
		assert.Equal(t, tc.slug, slug)
		pageSlug, err := SpeciesPageSlug(tc.name)
		require.NoErrorf(t, err, "SpeciesPageSlug(%q)", tc.name)
		assert.Equal(t, tc.pageSlug, pageSlug)
	}

	bad := []string{"", "   ", "Corvus", "..", "../etc/passwd", "Corvus brachyrhynchos/../..",
		"Corvus brachyrhynchos!", "123 456", "Corvus brachyrhynchos; rm -rf /"}
	for _, name := range bad {
		_, err := Slug(name)
		require.ErrorIsf(t, err, ErrInvalidName, "Slug(%q) must fail", name)
	}

	badPage := []string{"", "Corvus", "Corvus brachyrhynchos1", "Corvus/brachyrhynchos"}
	for _, name := range badPage {
		_, err := SpeciesPageSlug(name)
		require.ErrorIsf(t, err, ErrInvalidName, "SpeciesPageSlug(%q) must fail", name)
	}
}

func TestCacheHostileNamesRejectedWithoutIO(t *testing.T) {
	c, prov, dl := newStubCache(t, &Options{}, alwaysFound)

	for _, name := range []string{"", "   ", "..", "../etc/passwd", "Troglodytes hiemalis/../..", "set<oops>"} {
		entry, err := c.Lookup(t.Context(), name)
		require.ErrorIsf(t, err, ErrInvalidName, "name %q must be rejected", name)
		assert.Nilf(t, entry, "name %q must not return an entry", name)
	}
	assert.Zero(t, prov.total(), "hostile names must never reach the provider")
	assert.Zero(t, dl.count())
	_, statErr := os.Stat(c.Dir())
	require.ErrorIs(t, statErr, os.ErrNotExist, "rejected names must not create cache state")

	c.Prefetch(t.Context(), "../escape")
	c.Warm(t.Context(), []string{"", "  ", "Corvus"})
	time.Sleep(25 * time.Millisecond)
	assert.Zero(t, prov.total())
	cached, missing, queued := c.Stats()
	assert.Equal(t, 0, cached+missing+queued)
}

// --- capacity ---------------------------------------------------------------

func TestCacheMaxCacheSpeciesEnforced(t *testing.T) {
	c, prov, _ := newStubCache(t, &Options{MaxCacheSpecies: 1}, alwaysFound)

	_, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	assert.Equal(t, 1, prov.count("Corvus brachyrhynchos"))

	// A new species is refused while the cap is reached…
	_, err = c.Lookup(t.Context(), "Turdus migratorius")
	require.ErrorIs(t, err, ErrSoundNotFound)
	assert.Zero(t, prov.count("Turdus migratorius"), "full cache must not query for new species")
	_, statErr := filepath.Glob(filepath.Join(c.Dir(), "turdus_migratorius.*"))
	require.NoError(t, statErr)

	entries, err := os.ReadDir(c.Dir())
	require.NoError(t, err)
	for _, e := range entries {
		assert.False(t, strings.HasPrefix(e.Name(), "turdus"), "full cache must not store new species")
	}

	// …while cached species keep being served forever.
	entry, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	assert.Contains(t, entry.Path, "corvus_brachyrhynchos")
	assert.Equal(t, 1, prov.count("Corvus brachyrhynchos"))

	cached, _, _ := c.Stats()
	assert.Equal(t, 1, cached)
}

// --- ffmpeg-absent path ------------------------------------------------------

func TestCacheFFmpegAbsentStoresOriginalBytes(t *testing.T) {
	// Transcode+trim requested, but the configured ffmpeg does not exist:
	// the clip must be stored as published, untouched, and without Trimmed.
	missingFFmpeg := filepath.Join(t.TempDir(), "definitely-not-ffmpeg")
	c, prov, dl := newStubCache(t, &Options{
		Transcode:      true,
		MaxClipSeconds: 5,
		FfmpegPath:     missingFFmpeg,
	}, func(bin string) ([]Sound, error) {
		s := soundFor(bin)
		s.LengthSeconds = 30 // well above the 5 s clip cap
		return []Sound{s}, nil
	})

	entry, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	assert.Equal(t, []byte("FAKE-MP3-PAYLOAD"), body, "bytes must be stored untouched")

	assert.Equal(t, 1, prov.count("Corvus brachyrhynchos"))
	assert.Equal(t, 1, dl.count())
	assert.Equal(t, "audio/mpeg", entry.Info.MimeType)
	assert.False(t, entry.Info.Trimmed)
	assert.Zero(t, entry.Info.OriginalSeconds)
	assert.InDelta(t, 30.0, entry.Info.DurationSeconds, 1e-9)
	assert.True(t, strings.HasSuffix(entry.Path, ".mp3"))
}

// --- prefetch / warm ---------------------------------------------------------

func TestCachePrefetchDedupsAndWarmDoesNotBlock(t *testing.T) {
	gate := make(chan struct{})
	prov := newStubProvider(alwaysFound)
	prov.gate = gate // every Search blocks until the test opens the gate

	c := NewCache(Options{DataDir: t.TempDir(), UserAgent: testUserAgent})
	t.Cleanup(func() { _ = c.Close() })
	c.api = nil
	c.page = prov
	c.download = &stubDownloader{data: []byte("FAKE-MP3-PAYLOAD"), mime: "audio/mpeg"}

	ctx := t.Context()
	names := []string{
		"Corvus brachyrhynchos",
		"Turdus migratorius",
		"Setophaga petechia",
		"Corvus brachyrhynchos",
		"turdus migratorius",
		"Corvus brachyrhynchos",
	}

	started := time.Now()
	c.Warm(ctx, names)
	require.Less(t, time.Since(started), 2*time.Second, "Warm must not block on the providers")

	// While the gate is closed the worker sits in the first Search; the other
	// duplicates were deduped into the queue, and one species is in flight.
	assert.LessOrEqual(t, prov.total(), 1, "at most one fetch may be in flight")

	close(gate)
	require.Eventually(t, func() bool {
		cached, _, queued := c.Stats()
		return cached == 3 && queued == 0
	}, 15*time.Second, 20*time.Millisecond, "all three species must be cached without re-queues")

	assert.Equal(t, 3, prov.total(), "each species must be searched exactly once")
	for _, name := range []string{"Corvus brachyrhynchos", "Turdus migratorius", "Setophaga petechia"} {
		assert.Equalf(t, 1, prov.count(name), "%s fetched more than once", name)
		_, statErr := os.Stat(filepath.Join(c.Dir(), strings.ReplaceAll(strings.ToLower(name), " ", "_")+sidecarExt))
		require.NoError(t, statErr)
	}

	// A species already cached is never re-queued into a provider call.
	c.Prefetch(ctx, "Corvus brachyrhynchos")
	require.Eventually(t, func() bool {
		_, _, queued := c.Stats()
		return queued == 0
	}, 5*time.Second, 20*time.Millisecond)
	assert.Equal(t, 1, prov.count("Corvus brachyrhynchos"))
}

// --- real download path -------------------------------------------------------

func TestCacheDownloadsThroughGuardedClient(t *testing.T) {
	payload := []byte("ID3\x03real-ish mp3 bytes for the guarded download test")
	audio := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/clip.mp3", r.URL.Path)
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(payload)
	}))
	t.Cleanup(audio.Close)

	c, _, _ := newStubCache(t, &Options{}, func(bin string) ([]Sound, error) {
		s := soundFor(bin)
		s.DownloadURL = audio.URL + "/clip.mp3"
		return []Sound{s}, nil
	})
	// Use the production downloader (SSRF-guarded client) instead of the stub.
	c.download = newHTTPDownloader(c.client, testUserAgent)

	entry, err := c.Lookup(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	body, err := os.ReadFile(entry.Path)
	require.NoError(t, err)
	assert.Equal(t, payload, body)
	assert.Equal(t, "audio/mpeg", entry.Info.MimeType)
	assert.Equal(t, filepath.Join(c.Dir(), "corvus_brachyrhynchos.mp3"), entry.Path)
}

// TestUpdateDoesNotDeadlockTheCacheMutex guards the wedge that took the live
// example-call endpoints down: Update held c.mu.Lock() and rebuilt the provider
// chain through accessors that took c.mu.RLock(). An RWMutex blocks that RLock
// from the same goroutine forever, so the first settings hot-reload wedged the
// cache permanently and every request resolving the cache directory hung with
// it — while requests that answered before touching the cache (invalid names)
// still returned, which is what made it look like a routing problem.
func TestUpdateDoesNotDeadlockTheCacheMutex(t *testing.T) {
	t.Parallel()

	opts := &Options{Enabled: true, Transcode: true, UserAgent: testUserAgent}
	c, _, _ := newStubCache(t, opts, func(string) ([]Sound, error) { return nil, ErrSoundNotFound })
	dataDir := opts.DataDir

	updateDone := make(chan struct{})
	go func() {
		defer close(updateDone)
		c.Update(Options{Enabled: true, DataDir: dataDir, Transcode: true, UserAgent: testUserAgent, APIKey: "abc123"})
	}()

	// A concurrent reader is exactly what the HTTP handlers do. Which of the
	// two wins the lock is a scheduling detail, so the assertion is only that
	// both of them finish: a self-deadlock inside Update stops both forever.
	dirCh := make(chan string, 1)
	go func() { dirCh <- c.Dir() }()

	joined := make(chan string, 1)
	go func() {
		<-updateDone
		joined <- <-dirCh
	}()

	select {
	case got := <-joined:
		assert.Equal(t, filepath.Join(dataDir, "cache", cacheDirName), got)
	case <-time.After(5 * time.Second):
		t.Fatal("Update and a concurrent Dir() did not both complete within 5s: c.mu is wedged")
	}

	require.Equal(t, "abc123", c.options().APIKey, "the new settings must be visible after Update")
}

// TestUpdateRebuildsProvidersFromTheNewOptions keeps the hot-reload contract:
// adding a key switches the chain to the API tier, and no provider is rebuilt
// from stale options.
func TestUpdateRebuildsProvidersFromTheNewOptions(t *testing.T) {
	t.Parallel()

	opts := &Options{Enabled: true, UserAgent: testUserAgent}
	c, _, _ := newStubCache(t, opts, func(string) ([]Sound, error) { return nil, ErrSoundNotFound })
	dataDir := opts.DataDir

	require.Len(t, c.providerChain(), 1, "keyless cache uses the page tier only")

	c.Update(Options{Enabled: true, DataDir: dataDir, UserAgent: testUserAgent, APIKey: "abc123"})
	require.Len(t, c.providerChain(), 2, "a configured key must add the API tier")
	assert.Equal(t, "api", c.providerChain()[0].Name())
}

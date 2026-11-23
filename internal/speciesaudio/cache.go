// cache.go: the permanent local store of example calls.
//
// One clip per species, kept forever — no TTL, no eviction. A species is
// fetched at most once in the life of the install; everything after that is
// served from disk, so the feature works with the network down and never
// re-queries Xeno-Canto for something it already answered (including "this
// species has no recording", which is remembered as a marker file).
//
// Layout under <DataDir>/cache/sounds/:
//
//	corvus_brachyrhynchos.mp3   the clip
//	corvus_brachyrhynchos.json  attribution + provenance sidecar
//	tyrannus_verter.marking     (see missing marker below)
//	tyrannus_verter.missing     empty marker: Xeno-Canto has no recording
//
// Writes always go to a temporary file in the same directory and are renamed
// into place, so a crash mid-download can never leave a half-written clip that
// looks cached.
package speciesaudio

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tphakala/birdnet-go/internal/errors"
	"github.com/tphakala/birdnet-go/internal/httpclient"
)

const (
	cacheDirName  = "sounds"
	sidecarExt    = ".json"
	missingExt    = ".missing"
	prefetchQueue = 512
	// writeLockFile names the optional lock file kept next to the clips; it
	// must never be counted as a cached species by listSlugs.
	writeLockFile = ".write.lock"
)

// Info is what the attribution tooltip and the audio response need to know
// about a cached clip. It is persisted next to the clip.
type Info struct {
	Species         string    `json:"species"`
	RecordingID     string    `json:"recordingId,omitempty"`
	Source          string    `json:"source,omitempty"` // provider label: "api" or "page"
	Recordist       string    `json:"recordist,omitempty"`
	LicenseName     string    `json:"licenseName,omitempty"`
	LicenseURL      string    `json:"licenseUrl,omitempty"`
	PageURL         string    `json:"pageUrl,omitempty"`
	MimeType        string    `json:"mimeType"`
	DurationSeconds float64   `json:"durationSeconds"`
	Trimmed         bool      `json:"trimmed,omitempty"`
	OriginalSeconds float64   `json:"originalSeconds,omitempty"`
	FetchedAt       time.Time `json:"fetchedAt"`
}

// Entry is a cached clip ready to be served.
type Entry struct {
	Path string
	Info Info
}

// Options configures a Cache. Zero values mean "use the defaults".
type Options struct {
	DataDir        string // BirdNET-Go data directory; clips live under cache/sounds
	Enabled        bool   // master switch; false disables lookups entirely
	APIKey         string // Xeno-Canto API key, empty = keyless listing fallback
	FfmpegPath     string // empty = "ffmpeg" on PATH
	MaxClipSeconds int    // 0 = DefaultMaxClipSeconds, -1 = never trim
	Transcode      bool   // convert to mp3 / trim with ffmpeg when available
	// Prefetch is the user's "fill species in the background" switch. The cache
	// itself does not read it: an explicit request (someone opened a detection
	// and clicked play) always queues a fetch, while the implicit paths —
	// startup warm-up and new-species sightings — are gated by the wiring layer.
	Prefetch        bool
	MaxCacheSpecies int    // 0 = unlimited (the default: the cache never evicts)
	UserAgent       string // identifying UA; empty = a sensible default
}

// maxClip returns the effective clip cap (0 = no cap).
func (o *Options) maxClip() float64 {
	switch {
	case o.MaxClipSeconds < 0:
		return 0
	case o.MaxClipSeconds == 0:
		return DefaultMaxClipSeconds
	default:
		return float64(o.MaxClipSeconds)
	}
}

// Cache resolves and stores example calls.
type Cache struct {
	mu       sync.RWMutex
	opt      Options
	api      Provider
	page     Provider
	client   *http.Client
	log      loggerType
	download downloader

	inflightMu sync.Mutex
	inflight   map[string]*inflightCall

	queueOnce sync.Once
	queue     chan string
	queuedMu  sync.Mutex
	queued    map[string]struct{}
	cancel    context.CancelFunc
	parentCtx context.Context
	// workerDone is closed when the prefetch worker returns, so Close can wait
	// for an in-flight store to finish instead of leaving it writing into a
	// directory the caller is on its way to removing.
	workerDone chan struct{}

	ffmpegWarned atomic.Bool
	closed       atomic.Bool
}

type inflightCall struct {
	done  chan struct{}
	entry *Entry
	err   error
}

// NewCache builds a cache rooted at o.DataDir. It never contacts the network
// at construction time.
func NewCache(o Options) *Cache { //nolint:gocritic // hugeParam: a config snapshot is taken by value so later caller mutation cannot leak into the cache
	c := &Cache{
		opt:      o,
		client:   httpclient.NewGuardedHTTPClient(requestTimeout),
		log:      newSubsystemLogger(),
		inflight: map[string]*inflightCall{},
		queued:   map[string]struct{}{},
	}
	c.download = newHTTPDownloader(c.client, c.userAgent())
	c.buildProviders(o.APIKey, c.userAgent())
	return c
}

// Update applies new settings without restarting the process. In-flight
// lookups keep the settings they started with; nothing is evicted when a key is
// removed.
func (c *Cache) Update(o Options) { //nolint:gocritic // hugeParam: matches NewCache; Options is copied in on purpose
	c.mu.Lock()
	c.opt = o
	c.buildProviders(o.APIKey, resolveUserAgent(o))
	c.mu.Unlock()
}

// buildProviders rebuilds the provider chain from the values given. It takes
// plain values on purpose: Update calls it while holding c.mu, and reading
// anything back through c.options()/c.userAgent() would take c.mu.RLock() from
// the goroutine that already holds the write lock — a self-deadlock that wedges
// every Dir()/options() reader behind it, which is how the live endpoints came
// to hang on the first settings hot-reload.
func (c *Cache) buildProviders(apiKey, userAgent string) {
	c.api = NewAPIProvider(apiKey, userAgent)
	c.page = NewPageProvider(userAgent)
}

// resolveUserAgent is userAgent for a detached options snapshot.
func resolveUserAgent(o Options) string { //nolint:gocritic // hugeParam: reads the snapshot Update was given
	if o.UserAgent == "" {
		return defaultUserAgent()
	}
	return o.UserAgent
}

// Close stops the prefetch worker and waits for it to return, so a store that
// was already under way is finished by the time Close returns. Safe to call
// more than once, and safe to call before anything was ever queued.
func (c *Cache) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	if c.cancel != nil {
		c.cancel()
	}
	// A worker that starts concurrently with Close is refused by startWorker,
	// so the channel is either nil (nothing was ever queued) or the one the
	// running worker will close.
	done := c.workerDone
	if done != nil {
		<-done
	}
	return nil
}

func (c *Cache) userAgent() string {
	return resolveUserAgent(c.options())
}

// options is a snapshot accessor for paths and limits.
func (c *Cache) options() Options {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.opt
}

// Dir is the directory holding the cached clips.
func (c *Cache) Dir() string {
	return filepath.Join(c.options().DataDir, "cache", cacheDirName)
}

// Lookup returns the cached example call for a species, fetching and storing it
// on first use. It returns ErrSoundNotFound when Xeno-Canto has no recording
// for the species and ErrProviderUnavailable when Xeno-Canto could not be
// reached at all.
func (c *Cache) Lookup(ctx context.Context, scientificName string) (*Entry, error) {
	slug, err := Slug(scientificName)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(scientificName)

	if entry, ok := c.loadFromDisk(slug); ok {
		return entry, nil
	}
	if c.hasMissingMarker(slug) {
		return nil, ErrSoundNotFound
	}

	// One in-flight fetch per species: the detections list, the detail panel and
	// a prefetch can all ask for the same bird at once.
	call := func() (*Entry, error) {
		if entry, ok := c.loadFromDisk(slug); ok {
			return entry, nil
		}
		if c.hasMissingMarker(slug) {
			return nil, ErrSoundNotFound
		}
		opt := c.options()
		if opt.MaxCacheSpecies > 0 && c.countCached() >= opt.MaxCacheSpecies {
			c.info("species sound cache is full (%d species), not fetching %s", opt.MaxCacheSpecies, name)
			return nil, ErrSoundNotFound
		}

		entry, err := c.fetchAndStore(ctx, slug, name)
		if errors.Is(err, ErrSoundNotFound) {
			c.writeMissingMarker(slug)
		}
		return entry, err
	}
	return c.doOnce(slug, call)
}

// fetchAndStore asks each provider in turn and stores the first usable clip.
func (c *Cache) fetchAndStore(ctx context.Context, slug, name string) (*Entry, error) {
	opt := c.options()
	providers := make([]Provider, 0, 2)
	if api := c.providerChain(); len(api) > 0 {
		providers = api
	}

	policy := SelectPolicy{
		PreferSecondsMin: PreferredSecondsMin,
		PreferSecondsMax: PreferredSecondsMax,
		PreferMP3:        opt.Transcode,
	}

	var firstErr error
	for _, provider := range providers {
		sounds, err := provider.Search(ctx, name)
		if err != nil {
			if firstErr == nil || errors.Is(firstErr, ErrSoundNotFound) {
				firstErr = err
			}
			continue
		}
		sound, ok := PickBest(sounds, policy)
		if !ok {
			if firstErr == nil {
				firstErr = ErrSoundNotFound
			}
			continue
		}

		data, mime, err := c.download.get(ctx, sound.DownloadURL)
		if err != nil {
			c.warn("could not download example call for %s from %s: %v", name, provider.Name(), err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}

		return c.store(ctx, slug, name, provider.Name(), &sound, data, mime, &opt)
	}

	if firstErr == nil {
		firstErr = ErrSoundNotFound
	}
	return nil, firstErr
}

// providerChain returns the providers to try, keyed API first when configured.
func (c *Cache) providerChain() []Provider {
	c.mu.RLock()
	defer c.mu.RUnlock()
	chain := make([]Provider, 0, 2)
	if c.opt.APIKey != "" && c.api != nil {
		chain = append(chain, c.api)
	}
	if c.page != nil {
		chain = append(chain, c.page)
	}
	return chain
}

// store writes the clip and its sidecar, converting/trimming when asked.
func (c *Cache) store(ctx context.Context, slug, name, source string, sound *Sound, data []byte, mime string, opt *Options) (*Entry, error) {
	dir := c.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating species sound cache directory: %w", err)
	}

	info := Info{
		Species:         name,
		RecordingID:     sound.RecordingID,
		Source:          source,
		Recordist:       sound.Recordist,
		LicenseName:     sound.LicenseName,
		LicenseURL:      sound.LicenseURL,
		PageURL:         sound.PageURL,
		MimeType:        mime,
		DurationSeconds: sound.LengthSeconds,
		FetchedAt:       time.Now().UTC(),
	}

	ext := sound.Format
	if ext == "" {
		ext = extensionForMime(mime)
	}

	maxClip := opt.maxClip()
	needsTrim := maxClip > 0 && (sound.LengthSeconds <= 0 || sound.LengthSeconds > maxClip)
	wantMP3 := opt.Transcode && mime != "audio/mpeg"

	if opt.Transcode && (needsTrim || wantMP3) {
		if ffmpeg := c.ffmpegPath(opt); ffmpeg != "" {
			converted, trimmed, err := transcodeWithFFmpeg(ctx, ffmpeg, data, maxClip)
			if err == nil {
				data = converted
				mime = "audio/mpeg"
				ext = "mp3"
				info.MimeType = mime
				if trimmed {
					info.Trimmed = true
					info.OriginalSeconds = sound.LengthSeconds
					info.DurationSeconds = maxClip
				}
			} else {
				c.warn("could not convert example call for %s, storing it as published: %v", name, err)
			}
		} else if needsTrim {
			// ffmpeg is a runtime dependency of the container but not of every
			// bare-metal install; say so once, then keep working.
			if !c.ffmpegWarned.Swap(true) {
				c.warn("ffmpeg not found: example calls longer than the clip limit are stored untrimmed")
			}
		}
	}
	if ext == "" {
		ext = "dat"
	}

	finalPath := filepath.Join(dir, slug+"."+ext)
	if err := writeAtomic(finalPath, data); err != nil {
		return nil, err
	}
	// A stale sidecar from an older clip must not survive a re-fetch.
	if err := writeAtomicJSON(filepath.Join(dir, slug+sidecarExt), &info); err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	_ = os.Remove(filepath.Join(dir, slug+missingExt))
	c.removeOtherMedia(dir, slug, ext)

	c.info("cached example call for %s from %s (%s, %.1fs)", name, source, ext, info.DurationSeconds)
	return &Entry{Path: finalPath, Info: info}, nil
}

// loadFromDisk serves an existing clip. A sidecar whose media file has gone
// (or a media file with an unreadable sidecar) is treated as a miss so the
// next Lookup re-resolves the species from scratch.
func (c *Cache) loadFromDisk(slug string) (*Entry, bool) {
	dir := c.Dir()
	matches, err := filepath.Glob(filepath.Join(dir, slug+".*"))
	if err != nil || len(matches) == 0 {
		return nil, false
	}

	var infoPath, mediaPath string
	for _, match := range matches {
		switch filepath.Ext(match) {
		case sidecarExt:
			infoPath = match
		case missingExt:
			return nil, false
		default:
			mediaPath = match
		}
	}
	if infoPath == "" || mediaPath == "" {
		return nil, false
	}

	raw, err := os.ReadFile(infoPath)
	if err != nil {
		return nil, false
	}
	var info Info
	if err := json.Unmarshal(raw, &info); err != nil || info.MimeType == "" {
		return nil, false
	}
	stat, err := os.Stat(mediaPath)
	if err != nil || stat.Size() == 0 {
		return nil, false
	}
	if info.MimeType == "" {
		info.MimeType = mimeTypeForFormat(strings.TrimPrefix(filepath.Ext(mediaPath), "."))
	}
	return &Entry{Path: mediaPath, Info: info}, true
}

func (c *Cache) hasMissingMarker(slug string) bool {
	_, err := os.Stat(filepath.Join(c.Dir(), slug+missingExt))
	return err == nil
}

func (c *Cache) writeMissingMarker(slug string) {
	dir := c.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	path := filepath.Join(dir, slug+missingExt)
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		c.warn("could not write missing-species marker %s: %v", path, err)
	}
}

// countCached counts species that already have a clip or a miss marker.
func (c *Cache) countCached() int {
	return len(c.listSlugs())
}

// listSlugs returns every species the cache has an opinion about.
func (c *Cache) listSlugs() map[string]bool {
	slugs := map[string]bool{}
	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		return slugs
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		base := strings.TrimSuffix(name, sidecarExt)
		base = strings.TrimSuffix(base, missingExt)
		if base == name {
			// A media file: strip its extension.
			if idx := strings.LastIndex(name, "."); idx > 0 {
				base = name[:idx]
			} else {
				continue
			}
		}
		if base != "" && base != strings.TrimPrefix(writeLockFile, ".") {
			slugs[base] = true
		}
	}
	return slugs
}

// Stats reports cache occupancy for the settings page.
func (c *Cache) Stats() (cached, missing, queued int) {
	entries, err := os.ReadDir(c.Dir())
	if err != nil {
		return 0, 0, len(c.queue)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, missingExt):
			missing++
		case strings.HasSuffix(name, sidecarExt):
			seen[strings.TrimSuffix(name, sidecarExt)] = true
		}
	}
	c.queuedMu.Lock()
	queued = len(c.queued)
	c.queuedMu.Unlock()
	return len(seen), missing, queued
}

// Prefetch queues a species for background fetching. It never blocks and never
// duplicates work: species already cached, already missing, or already queued
// are skipped.
func (c *Cache) Prefetch(ctx context.Context, scientificName string) {
	if c.closed.Load() {
		return
	}
	slug, err := Slug(scientificName)
	if err != nil {
		return
	}
	c.startWorker(ctx)

	c.queuedMu.Lock()
	if _, dup := c.queued[slug]; dup {
		c.queuedMu.Unlock()
		return
	}
	c.queued[slug] = struct{}{}
	c.queuedMu.Unlock()

	select {
	case c.queue <- slug:
	default:
		// Queue full: drop this one, the next detection will offer it again.
		c.queuedMu.Lock()
		delete(c.queued, slug)
		c.queuedMu.Unlock()
	}
}

// Warm queues a batch of species (e.g. every species in an import or the
// existing detections table) without blocking the caller.
func (c *Cache) Warm(ctx context.Context, names []string) {
	for _, name := range names {
		c.Prefetch(ctx, name)
	}
}

// startWorker lazily starts the single prefetch worker. Nothing is started once
// the cache is closed: Close has already waited for whatever was in flight.
func (c *Cache) startWorker(ctx context.Context) {
	if c.closed.Load() {
		return
	}
	c.queueOnce.Do(func() {
		c.queue = make(chan string, prefetchQueue)
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		c.cancel = cancel
		c.parentCtx = runCtx
		c.workerDone = make(chan struct{})
		go func() {
			defer close(c.workerDone)
			c.work(runCtx)
		}()
	})
}

// work drains the prefetch queue. One worker keeps the outgoing request rate
// inside the etiquette limits without any extra pacing here.
func (c *Cache) work(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case slug := <-c.queue:
			c.queuedMu.Lock()
			delete(c.queued, slug)
			c.queuedMu.Unlock()

			name := nameFromSlug(slug)
			if _, err := c.Lookup(ctx, name); err != nil {
				if !errors.Is(err, ErrSoundNotFound) {
					c.debug("prefetch of %s did not complete: %v", name, err)
				}
				continue
			}
		}
	}
}

// doOnce runs fn once per slug, letting concurrent callers share the result.
func (c *Cache) doOnce(slug string, fn func() (*Entry, error)) (*Entry, error) {
	c.inflightMu.Lock()
	if call, ok := c.inflight[slug]; ok {
		c.inflightMu.Unlock()
		<-call.done
		return call.entry, call.err
	}
	call := &inflightCall{done: make(chan struct{})}
	c.inflight[slug] = call
	c.inflightMu.Unlock()

	call.entry, call.err = fn()

	c.inflightMu.Lock()
	delete(c.inflight, slug)
	c.inflightMu.Unlock()
	close(call.done)
	return call.entry, call.err
}

// removeOtherMedia deletes stale clips for a species that was re-fetched in a
// different format, so only one file per species is served.
func (c *Cache) removeOtherMedia(dir, slug, keepExt string) {
	matches, err := filepath.Glob(filepath.Join(dir, slug+".*"))
	if err != nil {
		return
	}
	for _, match := range matches {
		switch filepath.Ext(match) {
		case sidecarExt, missingExt, "." + keepExt:
			continue
		}
		if err := os.Remove(match); err == nil {
			c.debug("removed superseded example call %s", filepath.Base(match))
		}
	}
}

// --- file helpers ----------------------------------------------------------

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return fmt.Errorf("creating temporary file for %s: %w", path, err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", path, err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return fmt.Errorf("setting permissions on %s: %w", path, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("storing %s: %w", path, err)
	}
	return nil
}

func writeAtomicJSON(path string, info *Info) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding sidecar for %s: %w", path, err)
	}
	return writeAtomic(path, append(raw, '\n'))
}

// transcodeWithFFmpeg converts to mp3 and trims to maxClip seconds (0 = no
// trim). It reports whether trimming cut the clip shorter than the source.
func transcodeWithFFmpeg(ctx context.Context, ffmpeg string, data []byte, maxClip float64) (converted []byte, wasTrimmed bool, err error) {
	inFile, err := os.CreateTemp("", "birdnet-xc-*")
	if err != nil {
		return nil, false, fmt.Errorf("creating input temp file: %w", err)
	}
	defer func() { _ = os.Remove(inFile.Name()) }()
	defer func() { _ = inFile.Close() }()
	if _, err := inFile.Write(data); err != nil {
		return nil, false, fmt.Errorf("writing input temp file: %w", err)
	}
	if err := inFile.Close(); err != nil {
		return nil, false, fmt.Errorf("closing input temp file: %w", err)
	}

	outFile, err := os.CreateTemp("", "birdnet-mp3-*")
	if err != nil {
		return nil, false, fmt.Errorf("creating output temp file: %w", err)
	}
	outName := outFile.Name()
	_ = outFile.Close()
	defer func() { _ = os.Remove(outName) }()

	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-i", inFile.Name(),
	}
	trimmed := false
	if maxClip > 0 {
		args = append(args, "-t", fmt.Sprintf("%.2f", maxClip))
		trimmed = true
	}
	args = append(args,
		"-vn", "-map", "a:0",
		"-c:a", "libmp3lame", "-b:a", "96k", "-ar", "44100", "-ac", "1",
		outName,
	)

	runCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	cmd := exec.CommandContext(runCtx, ffmpeg, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, false, fmt.Errorf("ffmpeg failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	out, err := os.ReadFile(outName)
	if err != nil {
		return nil, false, fmt.Errorf("reading converted audio: %w", err)
	}
	if len(out) == 0 {
		return nil, false, fmt.Errorf("ffmpeg produced no output")
	}
	return out, trimmed, nil
}

// ffmpegPath resolves the configured ffmpeg binary, returning "" when there is
// none. The configured path wins; otherwise PATH is searched.
func (c *Cache) ffmpegPath(opt *Options) string {
	candidate := strings.TrimSpace(opt.FfmpegPath)
	if candidate == "" {
		candidate = "ffmpeg"
	}
	if resolved, err := exec.LookPath(candidate); err == nil {
		return resolved
	}
	return ""
}

func extensionForMime(mime string) string {
	switch mime {
	case "audio/mpeg":
		return "mp3"
	case "audio/wav", "audio/x-wav", "audio/wave":
		return "wav"
	case "audio/ogg":
		return "ogg"
	case "audio/flac":
		return "flac"
	case "audio/mp4", "audio/m4a", "audio/x-m4a":
		return "m4a"
	default:
		return ""
	}
}

// --- download --------------------------------------------------------------

// downloader fetches audio bytes with the size cap and content sniffing.
type downloader interface {
	get(ctx context.Context, rawURL string) ([]byte, string, error)
}

type httpDownloader struct {
	client    *http.Client
	userAgent string
}

func newHTTPDownloader(client *http.Client, userAgent string) downloader {
	return &httpDownloader{client: client, userAgent: userAgent}
}

func (d *httpDownloader) get(ctx context.Context, rawURL string) (payload []byte, contentType string, err error) {
	if !strings.HasPrefix(rawURL, "https://") && !strings.HasPrefix(rawURL, "http://") {
		return nil, "", fmt.Errorf("%w: refusing non-HTTP download URL", ErrProviderUnavailable)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, http.NoBody)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Accept", "audio/*")
	req.Header.Set("User-Agent", d.userAgent)

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("%w: audio download failed", ErrProviderUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, "", fmt.Errorf("%w (HTTP 429)", ErrRateLimited)
	}
	if resp.StatusCode >= 300 {
		return nil, "", fmt.Errorf("%w: audio download returned HTTP %d", ErrProviderUnavailable, resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAudioBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: reading audio failed", ErrProviderUnavailable)
	}
	if len(data) > maxAudioBytes {
		return nil, "", fmt.Errorf("%w: audio exceeds the %d byte limit", ErrProviderUnavailable, maxAudioBytes)
	}
	if len(data) == 0 {
		return nil, "", fmt.Errorf("%w: audio download was empty", ErrProviderUnavailable)
	}

	mime := strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
	if mime == "" || mime == "application/octet-stream" || strings.HasPrefix(mime, "text/") {
		mime = http.DetectContentType(data)
	}
	if !strings.HasPrefix(mime, "audio/") {
		// A bot-verification or error page served with an audio-looking header:
		// never cache that as a clip.
		if bytes.Contains(data[:min(len(data), 512)], []byte("<html")) {
			return nil, "", fmt.Errorf("%w: got an HTML page instead of audio", ErrProviderUnavailable)
		}
		if ext := extensionForMime(mime); ext == "" {
			return nil, "", fmt.Errorf("%w: unexpected audio content type %q", ErrProviderUnavailable, mime)
		}
	}
	return data, mime, nil
}

// wiring.go: the process-wide cache holder and the settings → options mapping.
//
// Every consumer (HTTP handlers, the detection pipeline, startup warm-up) reads
// the same cache, and the settings page can rebuild it live: Reconfigure is
// called at startup and again whenever realtime.xenocanto.* changes, so a newly
// pasted API key takes effect without a restart.
//
// The small option accessors live here rather than in cache.go so this file is
// the only place that knows how settings map onto a running cache.
package speciesaudio

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/tphakala/birdnet-go/internal/conf"
)

var (
	globalCache   atomic.Pointer[Cache]
	reconfigureMu sync.Mutex
)

// OptionsFromSettings maps the live settings onto cache options.
func OptionsFromSettings(settings *conf.Settings) Options {
	if settings == nil {
		return Options{}
	}
	xc := settings.Realtime.XenoCanto
	return Options{
		Enabled:         xc.Enabled,
		APIKey:          xc.APIKey,
		MaxClipSeconds:  xc.MaxClipSeconds,
		Transcode:       xc.Transcode,
		Prefetch:        xc.Prefetch,
		MaxCacheSpecies: xc.MaxCacheSpecies,
		UserAgent:       defaultUserAgent(),
	}
}

// Reconfigure installs the cache described by settings, updating the existing
// instance in place when there is one so in-flight lookups keep their state.
// It returns nil when example calls are disabled and stops the previous worker
// in that case.
func Reconfigure(settings *conf.Settings) *Cache {
	reconfigureMu.Lock()
	defer reconfigureMu.Unlock()

	opts := OptionsFromSettings(settings)
	previous := globalCache.Load()

	if !opts.Enabled {
		if previous != nil {
			_ = previous.Close()
		}
		globalCache.Store(nil)
		logConfigurationChange(nil, &opts)
		return nil
	}

	if previous != nil {
		keyChanged := previous.currentAPIKey() != opts.APIKey
		previous.Update(opts)
		globalCache.Store(previous)
		logConfigurationChange(previous, &opts)
		if keyChanged {
			// A fresh key invalidates nothing on disk, but it does change which
			// provider answers, so record it once for whoever reads the log.
			if log := newSubsystemLogger(); log != nil {
				log.Info("xeno-canto API key updated; example-call lookups now use " + keyChangedSource(opts.APIKey))
			}
		}
		return previous
	}

	cache := NewCache(opts)
	globalCache.Store(cache)
	logConfigurationChange(cache, &opts)
	return cache
}

// keyChangedSource names the provider chain a key selection implies.
func keyChangedSource(apiKey string) string {
	if apiKey != "" {
		return "the API"
	}
	return "the public species listing page"
}

// Get returns the active cache, or nil when example calls are disabled or the
// subsystem has not been initialised yet.
func Get() *Cache {
	return globalCache.Load()
}

// Enabled reports whether example-call lookups are currently switched on. The
// UI asks this to decide whether to offer the play button at all.
func Enabled() bool {
	return globalCache.Load() != nil
}

// PrefetchSpecies queues one species for background fetching. Safe to call
// before initialisation and from hot paths: it does nothing when the subsystem
// is off or prefetching is disabled, and the queue is bounded and deduplicated.
func PrefetchSpecies(scientificName string) {
	cache := globalCache.Load()
	if cache == nil || !cache.prefetchEnabled() {
		return
	}
	cache.Prefetch(context.Background(), scientificName)
}

// WarmExisting queues every species already known to the install. It runs on
// the cache's own worker, so startup must not wait on it.
func WarmExisting(names []string) {
	cache := globalCache.Load()
	if cache == nil || !cache.prefetchEnabled() {
		return
	}
	cache.Warm(context.Background(), names)
}

// Stop tears the global cache down (shutdown and tests).
func Stop() {
	reconfigureMu.Lock()
	defer reconfigureMu.Unlock()
	if cache := globalCache.Swap(nil); cache != nil {
		_ = cache.Close()
	}
}

// prefetchEnabled reports whether background fetching is switched on.
func (c *Cache) prefetchEnabled() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.opt.Enabled && c.opt.Prefetch
}

// currentAPIKey returns the key the running cache is using, for change
// detection only — it is never logged.
func (c *Cache) currentAPIKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.opt.APIKey
}

// logConfigurationChange records the effective configuration. The key itself is
// never logged, only whether one is present.
func logConfigurationChange(cache *Cache, opts *Options) {
	log := newSubsystemLogger()
	if log == nil {
		return
	}
	if !opts.Enabled {
		log.Info("xeno-canto example calls disabled")
		return
	}
	cached, missing, queued := 0, 0, 0
	if cache != nil {
		cached, missing, queued = cache.Stats()
	}
	log.Info(fmt.Sprintf(
		"xeno-canto example calls enabled: source=%s trim_limit=%ds transcode=%t prefetch=%t cache_cap=%d cached=%d known_missing=%d queued=%d",
		keyChangedSource(opts.APIKey), int(opts.maxClip()), opts.Transcode, opts.Prefetch,
		opts.MaxCacheSpecies, cached, missing, queued,
	))
}

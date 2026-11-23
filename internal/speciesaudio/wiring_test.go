// wiring_test.go covers the process-wide cache lifecycle: startup install and
// the settings hot-reload that follows it. The HTTP handlers and the detection
// pipeline both go through this instance, so a hot-reload that stops serving
// takes the play button with it — which is exactly how the feature failed in
// production.
package speciesaudio

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tphakala/birdnet-go/internal/conf"
)

// TestReconfigureTwiceKeepsTheCacheServing replays the production sequence:
// startup installs the cache through Reconfigure, and the first Xeno-Canto
// settings change arrives at the same function, which updates the instance in
// place. That second pass used to hold c.mu.Lock() while re-reading options
// through an accessor that takes c.mu.RLock() — an RWMutex blocks that for the
// same goroutine forever, so the hot-reload never returned and every later
// request hung behind it.
func TestReconfigureTwiceKeepsTheCacheServing(t *testing.T) {
	// globalCache is package state and this test drives it, so it cannot run
	// in parallel with the rest of the package.
	defer globalCache.Store(nil)

	first := Reconfigure(xenoCantoSettings(""))
	require.NotNil(t, first, "enabled settings must install a cache")

	secondDone := make(chan *Cache, 1)
	go func() { secondDone <- Reconfigure(xenoCantoSettings("abc123")) }()

	var second *Cache
	select {
	case second = <-secondDone:
	case <-time.After(5 * time.Second):
		t.Fatal("second Reconfigure never returned: the settings hot-reload deadlocks the cache")
	}

	require.NotNil(t, second)
	assert.Same(t, first, second, "a hot-reload must reuse the running instance, not replace it")

	// Every request resolves the cache directory through the same mutex; this
	// is the read a wedged writer starves.
	dirCh := make(chan string, 1)
	go func() { dirCh <- Get().Dir() }()

	select {
	case got := <-dirCh:
		assert.Equal(t, filepath.Join("cache", cacheDirName), got)
	case <-time.After(5 * time.Second):
		t.Fatal("Dir() never returned after the hot-reload")
	}

	assert.Equal(t, "abc123", Get().options().APIKey, "the hot-reload must apply the new settings")
}

// TestReconfigureDisableStopsServing pins the other half of the lifecycle:
// turning the feature off must return promptly as well, since it waits for the
// prefetch worker.
func TestReconfigureDisableStopsServing(t *testing.T) {
	defer globalCache.Store(nil)

	require.NotNil(t, Reconfigure(xenoCantoSettings("")))

	off := xenoCantoSettings("")
	off.Realtime.XenoCanto.Enabled = false

	done := make(chan *Cache, 1)
	go func() { done <- Reconfigure(off) }()

	select {
	case got := <-done:
		assert.Nil(t, got, "disabled settings must stop serving example calls")
	case <-time.After(5 * time.Second):
		t.Fatal("disabling the feature never returned")
	}

	assert.Nil(t, Get())
}

// xenoCantoSettings builds settings for the integration without touching the
// network: prefetch stays off so no provider is ever called.
func xenoCantoSettings(apiKey string) *conf.Settings {
	settings := &conf.Settings{}
	settings.Realtime.XenoCanto.Enabled = true
	settings.Realtime.XenoCanto.APIKey = apiKey
	settings.Realtime.XenoCanto.MaxClipSeconds = 10
	settings.Realtime.XenoCanto.Transcode = true
	settings.Realtime.XenoCanto.Prefetch = false
	return settings
}

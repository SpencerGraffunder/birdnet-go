// peek.go: the non-blocking question the HTTP layer asks.
//
// A request for an example call must never sit waiting on Xeno-Canto: the
// frontend needs an immediate answer ("playing", "working on it", "there is
// none"), so the API surface here only ever reads the local cache and, when the
// clip is not there yet, schedules the slow work on the prefetch queue.
package speciesaudio

import (
	"context"
	"fmt"

	"github.com/tphakala/birdnet-go/internal/errors"
)

// LookupState is the outcome of a cache peek.
type LookupState string

const (
	// StateReady means the clip is on disk and can be served.
	StateReady LookupState = "ready"
	// StatePending means the clip is not cached yet; a fetch has been scheduled
	// (when prefetching is enabled) and the caller should retry later.
	StatePending LookupState = "pending"
	// StateNotFound means Xeno-Canto has no recording for this species; this is
	// remembered, so it will not change.
	StateNotFound LookupState = "not-found"
)

// Peek reports what the cache knows about a species without contacting
// Xeno-Canto. A pending result also queues the fetch, so opening a detection
// detail page is enough to make the play button work on the next look.
//
// The returned error is only non-nil for an unusable species name; every other
// situation is expressed by the state, because none of them is the caller's
// problem to handle specially.
func (c *Cache) Peek(ctx context.Context, scientificName string) (*Entry, LookupState, error) {
	if c == nil {
		return nil, StateNotFound, fmt.Errorf("%w: example calls are not enabled", ErrProviderUnavailable)
	}
	slug, err := Slug(scientificName)
	if err != nil {
		return nil, StateNotFound, err
	}

	if entry, ok := c.loadFromDisk(slug); ok {
		return entry, StateReady, nil
	}
	if c.hasMissingMarker(slug) {
		return nil, StateNotFound, nil
	}

	// Not resolved yet. Remember the name so the next detection of this species
	// (or this very request) fills it in without another upstream query storm.
	c.Prefetch(ctx, scientificName)
	return nil, StatePending, nil
}

// Resolve is the blocking path used by the prefetch worker and the integration
// test endpoint: it fetches and stores the clip when it is not cached yet.
// Context cancellation is honoured, so a client that navigates away does not
// leave a download running that nobody wants.
func (c *Cache) Resolve(ctx context.Context, scientificName string) (*Entry, error) {
	if c == nil {
		return nil, fmt.Errorf("%w: example calls are not enabled", ErrProviderUnavailable)
	}
	entry, err := c.Lookup(ctx, scientificName)
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return nil, err
	}
	return entry, err
}

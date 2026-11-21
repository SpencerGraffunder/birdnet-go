// species_sound.go: the example-call endpoints.
//
//	GET /api/v2/media/species-sound/:scientific_name/info  attribution + whether a clip exists
//	GET /api/v2/media/species-sound/:scientific_name       the clip itself (Range supported)
//
// Both are hard boundaries in the same sense as the image proxy: they serve
// bytes from the local cache or they say "not yet" / "none", and they never
// redirect a client to Xeno-Canto. Neither one waits on the network — a species
// that is not cached yet schedules a background fetch and answers 503 with
// Retry-After, which is what keeps a detail panel from stalling behind a slow
// upstream.
package media

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/labstack/echo/v4"
	"github.com/tphakala/birdnet-go/internal/errors"
	"github.com/tphakala/birdnet-go/internal/logger"
	"github.com/tphakala/birdnet-go/internal/speciesaudio"
)

const (
	// SpeciesSoundRetryAfterSeconds is how long a client waits before asking
	// again about a clip that is still being fetched.
	SpeciesSoundRetryAfterSeconds = 5

	// SpeciesSoundCacheSeconds is the browser cache lifetime for a resolved
	// clip. Example calls never change once stored, so this can be long.
	SpeciesSoundCacheSeconds = 86400
)

// ErrSpeciesSoundNotAvailable means the example-call subsystem is switched off
// in settings, so no clip will ever become available.
var ErrSpeciesSoundNotAvailable = errors.NewStd("species example call service not available")

// SpeciesSoundInfo is the metadata answer for GET .../species-sound/:name/info.
type SpeciesSoundInfo struct {
	// State is "ready", "pending" or "not-found" — the client branches on this
	// instead of on status codes so a pending clip is not reported as an error.
	State           string  `json:"state"`
	ScientificName  string  `json:"scientificName"`
	AudioURL        string  `json:"audioURL,omitempty"`
	MimeType        string  `json:"mimeType,omitempty"`
	DurationSeconds float64 `json:"durationSeconds,omitempty"`
	Recordist       string  `json:"recordist,omitempty"`
	LicenseName     string  `json:"licenseName,omitempty"`
	LicenseURL      string  `json:"licenseUrl,omitempty"`
	PageURL         string  `json:"pageUrl,omitempty"`
	RecordingID     string  `json:"recordingId,omitempty"`
	Source          string  `json:"source,omitempty"`
	Trimmed         bool    `json:"trimmed,omitempty"`
	OriginalSeconds float64 `json:"originalSeconds,omitempty"`
}

// speciesSoundCache returns the active cache, or nil when the feature is
// switched off in settings.
func (c *Handler) speciesSoundCache() *speciesaudio.Cache {
	return speciesaudio.Get()
}

// GetSpeciesSoundInfo reports what is known about one species' example call.
//
// A pending species is queued for fetching here, so simply opening a detection
// is enough to make the play button work moments later.
func (c *Handler) GetSpeciesSoundInfo(ctx echo.Context) error {
	scientificName, invalid := c.speciesSoundNameParam(ctx)
	if invalid != nil {
		return invalid
	}

	cache := c.speciesSoundCache()
	if cache == nil {
		return c.HandleError(ctx, ErrSpeciesSoundNotAvailable, "Example calls are disabled", http.StatusServiceUnavailable)
	}

	entry, state, err := cache.Peek(ctx.Request().Context(), scientificName)
	if err != nil {
		// Only an unusable name reaches here.
		return c.HandleError(ctx, err, "Invalid species name", http.StatusBadRequest)
	}

	switch state {
	case speciesaudio.StateNotFound:
		return c.respondSpeciesSoundNotFound(ctx)
	case speciesaudio.StatePending:
		return c.respondSpeciesSoundPending(ctx, scientificName)
	default:
		// StateReady (and any future state that still reports a usable entry)
		// falls through to serving the file.
	}

	name, err := speciesaudio.Slug(scientificName)
	if err != nil {
		return c.HandleError(ctx, err, "Invalid species name", http.StatusBadRequest)
	}

	ctx.Response().Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", SpeciesSoundCacheSeconds))
	return ctx.JSON(http.StatusOK, SpeciesSoundInfo{
		State:           string(state),
		ScientificName:  scientificName,
		AudioURL:        "/api/v2/media/species-sound/" + url.PathEscape(name),
		MimeType:        entry.Info.MimeType,
		DurationSeconds: entry.Info.DurationSeconds,
		Recordist:       entry.Info.Recordist,
		LicenseName:     entry.Info.LicenseName,
		LicenseURL:      entry.Info.LicenseURL,
		PageURL:         entry.Info.PageURL,
		RecordingID:     entry.Info.RecordingID,
		Source:          entry.Info.Source,
		Trimmed:         entry.Info.Trimmed,
		OriginalSeconds: entry.Info.OriginalSeconds,
	})
}

// ServeSpeciesSound streams a cached example call. http.ServeContent handles
// Range requests, so seeking inside a playing clip works without any extra
// machinery.
func (c *Handler) ServeSpeciesSound(ctx echo.Context) error {
	scientificName, invalid := c.speciesSoundNameParam(ctx)
	if invalid != nil {
		return invalid
	}

	cache := c.speciesSoundCache()
	if cache == nil {
		return c.HandleError(ctx, ErrSpeciesSoundNotAvailable, "Example calls are disabled", http.StatusServiceUnavailable)
	}

	entry, state, err := cache.Peek(ctx.Request().Context(), scientificName)
	if err != nil {
		return c.HandleError(ctx, err, "Invalid species name", http.StatusBadRequest)
	}
	switch state {
	case speciesaudio.StateNotFound:
		return c.respondSpeciesSoundNotFound(ctx)
	case speciesaudio.StatePending:
		return c.respondSpeciesSoundPending(ctx, scientificName)
	default:
		// StateReady (and any future state that still reports a usable entry)
		// falls through to serving the file.
	}

	file, err := os.Open(filepath.Clean(entry.Path))
	if err != nil {
		// The cache reported a file that is no longer there (it was deleted by
		// hand, say). Treat it as "not yet" so the cache re-resolves the species.
		c.LogWarnIfEnabled("Cached example call could not be opened",
			logger.String("scientific_name", scientificName),
			logger.String("path", entry.Path),
			logger.Error(err),
		)
		return c.respondSpeciesSoundPending(ctx, scientificName)
	}
	defer func() { _ = file.Close() }()

	stat, err := file.Stat()
	if err != nil {
		return c.HandleError(ctx, err, "Could not read example call", http.StatusInternalServerError)
	}

	header := ctx.Response().Header()
	header.Set("Content-Type", entry.Info.MimeType)
	header.Set("Accept-Ranges", "bytes")
	header.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", SpeciesSoundCacheSeconds))
	if entry.Info.Recordist != "" {
		// Attribution travels with the bytes so any consumer that stores the
		// clip keeps the credit (RFC 6266 style comment in Content-Disposition).
		header.Set("Content-Disposition",
			fmt.Sprintf(`inline; filename=%q; credit=%q`,
				"example-call-"+safeFilenameToken(scientificName)+"."+strings.TrimPrefix(filepath.Ext(entry.Path), "."),
				strings.ReplaceAll(entry.Info.Recordist, `"`, "'")))
	}

	http.ServeContent(ctx.Response(), ctx.Request(), filepath.Base(entry.Path), stat.ModTime(), file)
	return nil
}

// RegisterSpeciesSoundRoutes registers the example-call routes on the v2 group.
// It is called from RegisterRoutes: these routes are datastore-independent.
func (c *Handler) RegisterSpeciesSoundRoutes(g *echo.Group) {
	g.GET("/media/species-sound/:scientific_name/info", c.GetSpeciesSoundInfo)
	g.GET("/media/species-sound/:scientific_name", c.ServeSpeciesSound)
}

// speciesSoundNameParam reads and validates the :scientific_name parameter.
func (c *Handler) speciesSoundNameParam(ctx echo.Context) (string, error) {
	raw := ctx.Param("scientific_name")
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", c.HandleError(ctx, fmt.Errorf("invalid species name encoding"), "Invalid species name", http.StatusBadRequest)
	}
	decoded = strings.TrimSpace(decoded)
	if decoded == "" {
		return "", c.HandleError(ctx, fmt.Errorf("missing scientific name"), "Scientific name is required", http.StatusBadRequest)
	}
	// Defence in depth: the cache turns names into file names through its own
	// slug rules, but a traversal attempt should be reported, not slugged.
	//nolint:gocritic // ruleguard suggests filepath.IsLocal, which still accepts nested local
	// names ("a/b"); a species name must contain no separator at all.
	if strings.Contains(decoded, "/") || strings.Contains(decoded, "\\") || strings.Contains(decoded, "..") {
		return "", c.HandleError(ctx, fmt.Errorf("invalid species name"), "Invalid species name", http.StatusBadRequest)
	}
	if _, err := speciesaudio.Slug(decoded); err != nil {
		return "", c.HandleError(ctx, err, "Invalid species name", http.StatusBadRequest)
	}
	return decoded, nil
}

// respondSpeciesSoundNotFound answers "Xeno-Canto has no recording for this
// species". Cacheable, so a species without a recording costs one lookup per
// browser cache lifetime instead of one per render.
func (c *Handler) respondSpeciesSoundNotFound(ctx echo.Context) error {
	ctx.Response().Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d", NotFoundCacheSeconds))
	return c.HandleError(ctx, speciesaudio.ErrSoundNotFound, "No example call found for species", http.StatusNotFound)
}

// respondSpeciesSoundPending answers "not yet, ask again shortly" without
// routing through HandleError: HandleError reports every status >= 500 to
// Sentry, and a cold detail panel asking for an uncached species is ordinary
// behaviour, not an incident.
func (c *Handler) respondSpeciesSoundPending(ctx echo.Context, scientificName string) error {
	c.LogDebugIfEnabled("Species example call not cached yet, background fetch scheduled",
		logger.String("scientific_name", scientificName))
	header := ctx.Response().Header()
	header.Set("Retry-After", strconv.Itoa(SpeciesSoundRetryAfterSeconds))
	header.Set("Cache-Control", cacheControlNoStore)
	return ctx.JSON(http.StatusServiceUnavailable,
		c.NewErrorResponse(speciesaudio.ErrProviderUnavailable, "Example call is being fetched", http.StatusServiceUnavailable))
}

// safeFilenameToken reduces a species name to something usable inside a
// Content-Disposition filename.
func safeFilenameToken(name string) string {
	slug, err := speciesaudio.Slug(name)
	if err != nil {
		return "species"
	}
	return slug
}

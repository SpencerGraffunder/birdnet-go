// xeno_canto.go: the Xeno-Canto integration test.
//
// POST /api/v2/integrations/xeno-canto/test
//
// The settings page offers a Test button so a key can be validated before it is
// relied on. The test does the smallest real thing possible: ask for one known
// species and report what came back. It deliberately does not download audio —
// validating a key should not push a clip across the user's link, and a clip
// download would make the test slower than the button implies.
package integrations

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/labstack/echo/v4"
	"github.com/tphakala/birdnet-go/internal/api/v2/apicore"
	"github.com/tphakala/birdnet-go/internal/errors"
	"github.com/tphakala/birdnet-go/internal/logger"
	"github.com/tphakala/birdnet-go/internal/speciesaudio"
)

// XenoCantoTestRequest is the body of POST /api/v2/integrations/xeno-canto/test.
//
// APIKey may arrive as the redacted placeholder when the user tests without
// re-entering a stored key; it is restored to the saved value below, exactly as
// the eBird test does.
type XenoCantoTestRequest struct {
	Enabled bool   `json:"enabled"`
	APIKey  string `json:"apiKey"`
	Species string `json:"species,omitempty"` // optional override for the probe species
}

// xenoCantoProbeSpecies is a near-ubiquitous North American bird with abundant
// recordings, which makes it a reliable probe without being location-specific
// in a way that matters: Xeno-Canto's API answers for any species worldwide.
const xenoCantoProbeSpecies = "Turdus migratorius"

// xenoCantoTestTimeout bounds the whole test. Xeno-Canto normally answers well
// inside this; the limit exists so a hung upstream cannot hold a request open.
const xenoCantoTestTimeout = 25 * time.Second

// XenoCantoTestResult is the final line of the streamed test.
type XenoCantoTestResult struct {
	// Via reports which provider answered: "api" (keyed) or "page" (keyless).
	Via         string  `json:"via,omitempty"`
	RecordingID string  `json:"recordingId,omitempty"`
	Species     string  `json:"species,omitempty"`
	Recordist   string  `json:"recordist,omitempty"`
	LengthSecs  float64 `json:"lengthSeconds,omitempty"`
}

// TestXenoCantoConnection handles POST /api/v2/integrations/xeno-canto/test.
func (c *Handler) TestXenoCantoConnection(ctx echo.Context) error {
	var request XenoCantoTestRequest
	if err := ctx.Bind(&request); err != nil {
		return c.HandleError(ctx, err, "Invalid Xeno-Canto test request", http.StatusBadRequest)
	}

	// Restore the redacted API key so testing without re-typing it works.
	apicore.RestoreRedactedSecret(c.CurrentSettings().Realtime.XenoCanto.APIKey, &request.APIKey)

	if !request.Enabled {
		return ctx.JSON(http.StatusOK, map[string]any{
			"success": false,
			"message": "Xeno-Canto example calls are not enabled",
			"state":   "failed",
		})
	}

	probe := strings.TrimSpace(request.Species)
	if probe == "" {
		probe = xenoCantoProbeSpecies
	}

	setStreamingHeaders(ctx, mimeNDJSON)

	testCtx, cancel := context.WithTimeout(ctx.Request().Context(), xenoCantoTestTimeout)
	defer cancel()

	encoder := json.NewEncoder(ctx.Response())
	sendStage := func(stage WeatherTestStage) error {
		apicore.SetStreamWriteDeadline(ctx)
		if err := encoder.Encode(stage); err != nil {
			return err
		}
		ctx.Response().Flush()
		return nil
	}

	// Stage 1: reach Xeno-Canto and pick a recording. A key is tried first when
	// one is present; a rejected key is reported as such rather than silently
	// falling back, because the whole point of the button is to learn whether
	// the key works.
	if err := sendStage(WeatherTestStage{
		ID: "lookup", Title: "Recording Lookup", Status: "in_progress",
	}); err != nil {
		return nil
	}

	result, message, err := probeXenoCanto(testCtx, request.APIKey, probe)
	stage := WeatherTestStage{ID: "lookup", Title: "Recording Lookup"}
	if err != nil {
		// The browser is deliberately shown a sanitised message, which means
		// the real reason only exists server-side. Without this line a failed
		// test is undebuggable from the outside — the blank-User-Agent failure
		// looked exactly like a server with no internet.
		via := "keyless"
		if request.APIKey != "" {
			via = "api"
		}
		apicore.GetLogger().Warn("Xeno-Canto integration test failed",
			logger.String("via", via),
			logger.String("species", probe),
			logger.Error(err),
		)
		stage.Status = "error"
		stage.Error = sanitizeXenoCantoError(err, request.APIKey)
		_ = sendStage(stage)
		return nil
	}
	stage.Status = streamStateCompleted
	stage.Message = message
	_ = sendStage(stage)

	if err := sendStage(WeatherTestStage{ID: streamStateCompleted, Status: streamStateCompleted}); err != nil {
		return nil
	}
	_ = encoder.Encode(result)
	apicore.SetStreamWriteDeadline(ctx)
	ctx.Response().Flush()
	return nil
}

// probeXenoCanto asks Xeno-Canto for one example call without downloading it.
// It returns the human-readable stage message alongside the structured result.
func probeXenoCanto(ctx context.Context, apiKey, species string) (XenoCantoTestResult, string, error) {
	var provider speciesaudio.Provider
	if apiKey != "" {
		provider = speciesaudio.NewAPIProvider(apiKey, "")
	} else {
		provider = speciesaudio.NewPageProvider("")
	}

	sounds, err := provider.Search(ctx, species)
	if err != nil {
		return XenoCantoTestResult{}, "", err
	}
	sound, ok := speciesaudio.PickBest(sounds, speciesaudio.SelectPolicy{PreferMP3: true})
	if !ok {
		return XenoCantoTestResult{}, "", fmt.Errorf("%w: no usable recording in the results", speciesaudio.ErrSoundNotFound)
	}

	via := provider.Name()
	note := fmt.Sprintf("Found %d recording(s); using %s by %s", len(sounds), describeLength(sound.LengthSeconds), sound.Recordist)
	return XenoCantoTestResult{
		Via:         via,
		RecordingID: sound.RecordingID,
		Species:     sound.ScientificName,
		Recordist:   sound.Recordist,
		LengthSecs:  sound.LengthSeconds,
	}, note, nil
}

// describeLength formats a recording length for the test output.
func describeLength(seconds float64) string {
	if seconds <= 0 {
		return "an unlisted-length recording"
	}
	return fmt.Sprintf("a %.0f-second recording", seconds)
}

// sanitizeXenoCantoError turns a provider error into something safe to show in
// the UI: it names the failure, never the key, and keeps enough detail that a
// user can tell a wrong key from Xeno-Canto being unreachable.
func sanitizeXenoCantoError(err error, apiKey string) string {
	if apiKey != "" {
		// Belt and braces: no provider error should carry the key, but a
		// transport error string is outside this package's control.
		if strings.Contains(err.Error(), apiKey) {
			return strings.ReplaceAll(err.Error(), apiKey, "[redacted]")
		}
	}
	switch {
	case errors.Is(err, speciesaudio.ErrKeyRejected):
		return "Xeno-Canto rejected this API key. Check the key from your Xeno-Canto account page."
	case errors.Is(err, speciesaudio.ErrRateLimited):
		return "Xeno-Canto is rate limiting this install. Try again in a few minutes."
	case errors.Is(err, speciesaudio.ErrSoundNotFound):
		return "Xeno-Canto has no recording for the probe species."
	case errors.Is(err, speciesaudio.ErrProviderUnavailable):
		// Say what THIS install actually did: with a key the bot-protection
		// advice is noise, and pointing the user at a key they already entered
		// sends them chasing the wrong thing.
		if apiKey != "" {
			return "Xeno-Canto did not answer the API request. Check this server's internet access, or try again shortly — Xeno-Canto sometimes serves an error page instead of results."
		}
		return "Xeno-Canto could not be reached. Check this server's internet access. With no API key set Xeno-Canto's bot protection usually blocks the lookup, so add a free key from your Xeno-Canto account page."
	default:
		return "Xeno-Canto lookup failed."
	}
}

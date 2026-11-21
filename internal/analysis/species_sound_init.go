// species_sound_init.go: startup wiring for Xeno-Canto example calls.
//
// The cache is a single process-wide instance (speciesaudio.Get) because the
// clips are shared by the HTTP handlers and the detection pipeline, and because
// one queue is what keeps the outgoing request rate inside the limits
// Xeno-Canto asks for. Startup therefore builds it from settings and queues the
// species this install already knows, so the play button works for every bird
// already in the database instead of only for the next detection.
package analysis

import (
	"github.com/tphakala/birdnet-go/internal/conf"
	"github.com/tphakala/birdnet-go/internal/datastore"
	"github.com/tphakala/birdnet-go/internal/logger"
	"github.com/tphakala/birdnet-go/internal/speciesaudio"
)

// speciesSoundStore remembers the datastore handed to initSpeciesSoundCache so
// a settings change later on can queue the species known at that moment. The
// control monitor has no datastore of its own, and reaching for a global one
// here would hide the dependency; this is the same object, kept locally.
var speciesSoundStore datastore.Interface

// initSpeciesSoundCache builds the example-call cache from settings and queues
// the species already detected. It never fails the startup path: with the
// feature disabled — or with no species known yet — there is simply nothing to
// do, and the detail panel falls back to "no example available".
func initSpeciesSoundCache(settings *conf.Settings, ds datastore.Interface) *speciesaudio.Cache {
	log := GetLogger()

	speciesSoundStore = ds
	cache := speciesaudio.Reconfigure(settings)
	if cache == nil {
		log.Debug("example-call cache not active (realtime.xenoCanto.enabled is false)")
		return nil
	}

	warmKnownSpecies("startup")
	return cache
}

// handleReconfigureXenoCanto applies example-call settings without a restart.
// The cache instance is reused so queued prefetches and already-stored clips
// survive; only the settings (API key, clip limit, switches) are swapped.
func (cm *ControlMonitor) handleReconfigureXenoCanto() {
	log := GetLogger()
	log.Info("Applying Xeno-Canto example-call settings")

	settings := conf.Setting()
	if settings == nil {
		log.Error("settings not available while applying Xeno-Canto settings")
		return
	}

	if speciesaudio.Reconfigure(settings) == nil {
		// Disabled; Reconfigure logs that itself, including the cache contents
		// it stopped serving.
		return
	}
	warmKnownSpecies("settings change")
}

// warmKnownSpecies queues every species already in the database. Prefetching is
// deduplicated and bounded inside the cache, so calling this repeatedly is
// cheap; the pacing lives on the cache's worker, which means a large existing
// database trickles in quietly instead of arriving as a burst against
// Xeno-Canto.
func warmKnownSpecies(reason string) {
	log := GetLogger()
	if speciesSoundStore == nil || !conf.Setting().Realtime.XenoCanto.Prefetch {
		return
	}

	speciesList, err := speciesSoundStore.GetAllDetectedSpecies()
	if err != nil {
		// The database may still be warming up on a first run; the play button
		// and the on-detection prefetch still work, so this is not fatal.
		log.Warn("could not list detected species for example-call warm-up",
			logger.String("reason", reason), logger.Error(err))
		return
	}

	names := make([]string, 0, len(speciesList))
	for i := range speciesList {
		if speciesList[i].ScientificName != "" {
			names = append(names, speciesList[i].ScientificName)
		}
	}
	if len(names) == 0 {
		return
	}

	speciesaudio.WarmExisting(names)
	log.Info("queued example-call warm-up for known species",
		logger.String("reason", reason), logger.Int("species", len(names)))
}

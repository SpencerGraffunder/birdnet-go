package speciesaudio

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// api3MourningDove is one recording row copied from a live API 3 response
// (field names, types and all — including the noise fields the parser must
// ignore). API 3 renamed the fields the older API used: "length" for the clip
// length, "q" for the quality grade, "lic" for the Creative Commons deed, and
// "en" for the English name, because "cnt" is the COUNTRY. Reading the old
// names produced zero-second, unlicensed, country-labelled candidates, and a
// candidate list of zero-second clips is discarded by PickBest, which made the
// keyed API look broken and the keyless page tier take over in silence.
const api3MourningDove = `{
  "numRecordings": "228",
  "numPages": "3",
  "page": "1",
  "recordings": [
    {
      "id": "1013493",
      "gen": "Zenaida",
      "sp": "macroura",
      "ssp": "",
      "grp": "birds",
      "status": "identified",
      "en": "Mourning Dove",
      "rec": "Stanislas Wroza",
      "cnt": "Canada",
      "loc": "Charlevoix (near  Saint-Joseph-de-la-Rive), Capitale-Nationale, Québec",
      "regnr": "",
      "rmk": "",
      "alt": 0,
      "lat": 47.425,
      "lon": -70.318,
      "date": "2025-05-25",
      "time": "06:30",
      "uploaded": "2025-07-08",
      "length": "0:09",
      "q": "A",
      "lic": "https://creativecommons.org/licenses/by-nc-sa/4.0/",
      "url": "https://xeno-canto.org/1013493",
      "file": "https://xeno-canto.org/1013493/download",
      "file-name": "XC1013493-Tourterelle-triste-vol-+.wav",
      "smp": "48000",
      "dvc": "Parabolic microphone",
      "type": "flight call, wingbeats",
      "animal-seen": "yes",
      "playback-used": "unknown",
      "method": "field recording",
      "sex": "",
      "stage": "",
      "temp": null,
      "also": [],
      "annotation-set": [],
      "osci": {"small": null, "med": null, "large": null},
      "sono": {"small": "https://xeno-canto.org/sounds/spectrograms/SDPCHKOHRH/1013493.jpg"}
    }
  ]
}`

func TestAPIProviderReadsAPI3Fields(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, api3MourningDove)
	}
	p, _ := newAPITestProvider(t, testAPIKey, handler)

	sounds, err := p.Search(t.Context(), "Zenaida macroura")
	require.NoError(t, err)
	require.Len(t, sounds, 1)

	got := sounds[0]
	assert.Equal(t, "1013493", got.RecordingID)
	assert.Equal(t, "Zenaida macroura", got.ScientificName)
	assert.Equal(t, "Mourning Dove", got.CommonName)
	assert.NotEqual(t, "Canada", got.CommonName, "cnt is the recording country, not the species name")
	assert.Equal(t, "Stanislas Wroza", got.Recordist)
	assert.InDelta(t, 9.0, got.LengthSeconds, 1e-9, `the "length" field ("0:09") is the clip length`)
	assert.Equal(t, RatingExcellent, got.Rating, `the "q" field carries the A–E grade`)
	assert.Equal(t, "https://creativecommons.org/licenses/by-nc-sa/4.0/", got.LicenseURL)
	assert.Equal(t, "Creative Commons Attribution-NonCommercial-ShareAlike 4.0", got.LicenseName)
	assert.Equal(t, "https://xeno-canto.org/1013493/download", got.DownloadURL)
	assert.Equal(t, "https://xeno-canto.org/1013493", got.PageURL)

	// A candidate the picker can actually use is the point of the whole mapping:
	// this is what silently fell to the page tier when the length read 0.
	best, ok := PickBest(sounds, SelectPolicy{})
	require.True(t, ok, "a live API row must survive PickBest, not be discarded as unknown-length")
	assert.Equal(t, "1013493", best.RecordingID)
}

// TestAPIProviderReadsLegacyFieldNames keeps the pre-v3 spelling working: Xeno-
// Canto has changed these names before, and a response that still says "len"
// and a numeric "r" must not come back empty.
func TestAPIProviderReadsLegacyFieldNames(t *testing.T) {
	handler := func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"recordings":[{"id":"543338","gen":"Corvus","sp":"brachyrhynchos",`+
			`"cnt":"American Crow","len":"0:23","r":"4.8",`+
			`"file":"https://xeno-canto.org/543338/download"}]}`)
	}
	p, _ := newAPITestProvider(t, testAPIKey, handler)

	sounds, err := p.Search(t.Context(), "Corvus brachyrhynchos")
	require.NoError(t, err)
	require.Len(t, sounds, 1)
	assert.InDelta(t, 23.0, sounds[0].LengthSeconds, 1e-9)
	assert.Equal(t, RatingExcellent, sounds[0].Rating)
	assert.Empty(t, sounds[0].LicenseURL, "no licence published is no licence, not the recording page")
	assert.Empty(t, sounds[0].LicenseName)
}

func TestAPIGradeMapsLetterAndNumericGrades(t *testing.T) {
	tests := []struct {
		name     string
		quality  string
		legacy   string
		expected string
	}{
		{"letter grade", "A", "", RatingExcellent},
		{"letter grade lowered", "b", "", RatingVeryGood},
		{"letter grade raised", "  c  ", "", RatingGood},
		{"numeric legacy", "", "4.8", RatingExcellent},
		{"numeric legacy mid", "", "3.0", RatingGood},
		{"zero is unrated", "", "0", RatingUnknown},
		{"empty stays unrated", "", "", RatingUnknown},
		{"letter wins over legacy", "D", "5.0", RatingFair},
		{"stray symbol is not a grade", "+", "", RatingUnknown},
		{"unknown letter is not a grade", "Z", "", RatingUnknown},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, apiGrade(tc.quality, tc.legacy))
		})
	}
}

func TestLicenseNameFromURL(t *testing.T) {
	tests := []struct {
		url      string
		expected string
	}{
		{"https://creativecommons.org/licenses/by/4.0/", "Creative Commons Attribution 4.0"},
		{"https://creativecommons.org/licenses/by-sa/4.0/", "Creative Commons Attribution-ShareAlike 4.0"},
		{"https://creativecommons.org/licenses/by-nd/4.0/", "Creative Commons Attribution-NoDerivatives 4.0"},
		{"https://creativecommons.org/licenses/by-nc/3.0/", "Creative Commons Attribution-NonCommercial 3.0"},
		{"https://creativecommons.org/licenses/by-nc-sa/4.0/", "Creative Commons Attribution-NonCommercial-ShareAlike 4.0"},
		{"https://creativecommons.org/licenses/by-nc-nd/2.5/", "Creative Commons Attribution-NonCommercial-NoDerivatives 2.5"},
		{"http://creativecommons.org/licenses/by-nc-sa/2.0/", "Creative Commons Attribution-NonCommercial-ShareAlike 2.0"},
		{"https://creativecommons.org/publicdomain/zero/1.0/", "CC0 1.0 Public Domain Dedication"},
		{"https://creativecommons.org/publicdomain/mark/1.0/", "Public Domain Mark 1.0"},
		// A deed URL with no version still names the licence correctly, so the
		// name is shown on its own (with no trailing space) rather than lost.
		{"https://creativecommons.org/licenses/by-nc-sa/", "Creative Commons Attribution-NonCommercial-ShareAlike"},
		{"https://creativecommons.org/licenses/by-nc-sa", "Creative Commons Attribution-NonCommercial-ShareAlike"},
		{"https://creativecommons.org/publicdomain/zero/", "CC0 Public Domain Dedication"},
		{"https://creativecommons.org/licenses/", ""},
		{"https://creativecommons.org/", ""},
		// A licence this map does not know stays unnamed: guessing would
		// misattribute the recording.
		{"https://creativecommons.org/licenses/by-nc-sa-nd/4.0/", ""},
		{"https://example.org/terms", ""},
		{"", ""},
	}
	for _, tc := range tests {
		t.Run(tc.url, func(t *testing.T) {
			assert.Equal(t, tc.expected, licenseNameFromURL(tc.url))
		})
	}
}

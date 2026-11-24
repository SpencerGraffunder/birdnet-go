package integrations

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/tphakala/birdnet-go/internal/speciesaudio"
)

const probeTestKey = "xc-probe-key-4f7c"

// unavailableErr builds the error the provider returns for the HTML page case;
// %w is enough to carry the sentinel, which is all the message layer reads.
func unavailableErr() error {
	return fmt.Errorf("%w: xeno-canto returned an HTML page instead of JSON",
		speciesaudio.ErrProviderUnavailable)
}

// TestXenoCantoErrorMessageKnowsWhetherAKeyWasUsed: the old text blamed bot
// protection and told the user to add a key they had already added. With a key
// configured the only honest readings are "the server did not answer" or "a
// key is needed", so the message has to branch on the state this install is in.
func TestXenoCantoErrorMessageKnowsWhetherAKeyWasUsed(t *testing.T) {
	withKey := sanitizeXenoCantoError(unavailableErr(), probeTestKey)
	assert.Contains(t, withKey, "did not answer")
	assert.NotContains(t, withKey, "no API key set", "a key is set; telling the user to add one sends them in circles")

	withoutKey := sanitizeXenoCantoError(unavailableErr(), "")
	assert.Contains(t, withoutKey, "bot protection")
	assert.Contains(t, withoutKey, "API key")
}

// TestXenoCantoErrorMessageNeverLeaksTheKey: the error string is rendered
// straight into the settings page, and a transport error can carry the URL —
// which carries the key.
func TestXenoCantoErrorMessageNeverLeaksTheKey(t *testing.T) {
	leaky := fmt.Errorf("%w: Get \"https://xeno-canto.org/api/3/recordings?key=%s\": dial tcp: timeout",
		speciesaudio.ErrProviderUnavailable, probeTestKey)

	got := sanitizeXenoCantoError(leaky, probeTestKey)
	assert.NotContains(t, got, probeTestKey)
	assert.Contains(t, got, "[redacted]")
}

func TestXenoCantoErrorMessagesRemainDistinct(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{"rejected key", fmt.Errorf("%w (HTTP 401)", speciesaudio.ErrKeyRejected),
			"Xeno-Canto rejected this API key. Check the key from your Xeno-Canto account page."},
		{"rate limited", fmt.Errorf("%w (HTTP 429)", speciesaudio.ErrRateLimited),
			"Xeno-Canto is rate limiting this install. Try again in a few minutes."},
		{"nothing published", fmt.Errorf("%w: species has no recordings", speciesaudio.ErrSoundNotFound),
			"Xeno-Canto has no recording for the probe species."},
		{"anything else", fmt.Errorf("unexpected"), "Xeno-Canto lookup failed."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, sanitizeXenoCantoError(tc.err, probeTestKey))
		})
	}
}

// log.go: subsystem logging and the outbound User-Agent.
//
// The logger is resolved lazily because packages import this one before the
// central logger exists, and the User-Agent follows the repo convention of
// identifying the application, its purpose, and a contact URL — forks identify
// themselves through internal/branding rather than the upstream repository.
package speciesaudio

import (
	"fmt"
	"strings"

	"github.com/tphakala/birdnet-go/internal/branding"
	"github.com/tphakala/birdnet-go/internal/logger"
)

type loggerType = logger.Logger

// ModuleName is the log module this package writes under.
const ModuleName = "SpeciesSound"

func newSubsystemLogger() loggerType {
	return logger.Global().Module(ModuleName)
}

// logger returns the module logger, tolerating a nil cache built in tests.
func (c *Cache) logger() loggerType {
	if c == nil {
		return nil
	}
	if c.log != nil {
		return c.log
	}
	return newSubsystemLogger()
}

func (c *Cache) info(format string, args ...any) {
	if l := c.logger(); l != nil {
		l.Info(fmt.Sprintf(format, args...))
	}
}

func (c *Cache) warn(format string, args ...any) {
	if l := c.logger(); l != nil {
		l.Warn(fmt.Sprintf(format, args...))
	}
}

func (c *Cache) debug(format string, args ...any) {
	if l := c.logger(); l != nil {
		l.Debug(fmt.Sprintf(format, args...))
	}
}

// defaultUserAgentOr returns ua when it carries something, and the built-in
// identifying UA when it does not.
//
// This is not a courtesy: Xeno-Canto answers a request with an empty or absent
// User-Agent by serving an HTML error page at HTTP 200. A JSON client then
// reports "could not be reached" while the network and the API key are both
// fine — which is exactly how the Settings test button failed for a valid key.
// Constructors apply it so a caller cannot send a blank UA by accident.
func defaultUserAgentOr(ua string) string {
	if strings.TrimSpace(ua) == "" {
		return defaultUserAgent()
	}
	return ua
}

// defaultUserAgent identifies this install to Xeno-Canto.
func defaultUserAgent() string {
	contact := branding.RepoURL()
	if contact == "" {
		contact = "https://github.com/tphakala/birdnet-go"
	}
	return fmt.Sprintf("BirdNETGo/1.0 (%s; %s) Go-http-client/1.1", userAgentComment, contact)
}

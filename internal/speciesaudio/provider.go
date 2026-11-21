// provider.go: fetching candidate recordings from Xeno-Canto.
//
// Two providers, one interface:
//
//   - apiProvider talks to the documented Xeno-Canto API v3 endpoint and is
//     used whenever the user has entered an API key (Settings → Integrations).
//   - pageProvider reads the public species listing page when no key is set.
//     It is a convenience fallback, not a supported integration: Xeno-Canto
//     added bot protection to parts of the site, so this path degrades to
//     ErrProviderUnavailable as soon as the response is not a listing.
//
// Etiquette, implemented here rather than at the call sites so nothing can
// forget it: a process-wide request pacer (1/s for the API, 3.5 s for the
// listing pages), an identifying User-Agent, and the shared SSRF-guarded HTTP
// client from internal/httpclient.
package speciesaudio

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tphakala/birdnet-go/internal/httpclient"
	"golang.org/x/net/html"
)

const (
	xenocantoHost = "xeno-canto.org"
	apiEndpoint   = "https://xeno-canto.org/api/3/recordings"
	siteBase      = "https://xeno-canto.org"

	// defaultAPISpace / defaultPageSpace are the minimum gaps between outgoing
	// requests. Xeno-Canto asks scrapers for a crawl-delay of a few seconds; the
	// API is documented as rate limited, and one request per species ever (the
	// cache) plus 1/s here is well inside both.
	defaultAPISpace  = time.Second
	defaultPageSpace = 3500 * time.Millisecond

	// maxAudioBytes bounds a downloaded example clip (25 MB). A clip is normally
	// well under 2 MB, so hitting the cap means something is wrong upstream.
	maxAudioBytes = 25 << 20

	requestTimeout   = 25 * time.Second
	maxResponseBytes = 4 << 20 // listing pages are ~150 KB; the cap bounds a runaway response
	userAgentComment = "BirdNET-Go example-call fetcher"
)

// Provider resolves candidate recordings for one species.
type Provider interface {
	// Name is a short label for logs ("api", "page").
	Name() string
	// Search returns candidate recordings for a scientific name, or
	// ErrSoundNotFound when the species has no recordings.
	Search(ctx context.Context, scientificName string) ([]Sound, error)
}

// NewAPIProvider returns the keyed API v3 provider. It reports
// ErrProviderUnavailable when apiKey is empty — call sites decide whether the
// keyless pageProvider or "no example available" is the right answer.
func NewAPIProvider(apiKey, userAgent string) Provider {
	return &apiProvider{
		apiKey:    apiKey,
		userAgent: userAgent,
		endpoint:  apiEndpoint,
		client:    httpclient.NewGuardedHTTPClient(requestTimeout),
		pacer:     &pacer{minSpace: defaultAPISpace},
	}
}

// NewPageProvider returns the keyless species-listing provider.
func NewPageProvider(userAgent string) Provider {
	return &pageProvider{
		userAgent: userAgent,
		endpoint:  siteBase,
		client:    httpclient.NewGuardedHTTPClient(requestTimeout),
		pacer:     &pacer{minSpace: defaultPageSpace},
	}
}

// pacer serialises outgoing requests to at most one per minSpace.
type pacer struct {
	mu       sync.Mutex
	last     time.Time
	minSpace time.Duration
	// sleep is replaced in tests so pacing costs nothing.
	sleep func(ctx context.Context, d time.Duration) error
}

func (p *pacer) wait(ctx context.Context) error {
	if p.minSpace <= 0 {
		return nil
	}
	sleep := p.sleep
	if sleep == nil {
		sleep = realSleep
	}
	for {
		p.mu.Lock()
		wait := p.minSpace - time.Since(p.last)
		if wait <= 0 {
			p.last = time.Now()
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
		if err := sleep(ctx, wait); err != nil {
			return err
		}
	}
}

func realSleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// normalizeBinomial reduces a name to the "genus species" pair Xeno-Canto
// indexes, rejecting anything that could not be a binomial. Subspecies names
// ("Tyrannus tyrannus orientalis") are folded to their parent pair, which is
// what BirdNET-Go reports for many detections.
func normalizeBinomial(name string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(name))
	if len(fields) < 2 {
		return "", fmt.Errorf("%w: %q is not a binomial", ErrInvalidName, name)
	}
	genus, species := fields[0], fields[1]
	for _, part := range []string{genus, species} {
		if !isAlphaOnly(part) {
			return "", fmt.Errorf("%w: unexpected characters in %q", ErrInvalidName, name)
		}
	}
	return strings.ToLower(genus) + " " + strings.ToLower(species), nil
}

func isAlphaOnly(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			continue
		}
		if r >= 'A' && r <= 'Z' {
			continue
		}
		return false
	}
	return true
}

// --- keyed API provider ----------------------------------------------------

// apiProvider queries the documented Xeno-Canto API v3 endpoint.
type apiProvider struct {
	apiKey    string
	userAgent string
	endpoint  string // overridable in tests
	client    *http.Client
	pacer     *pacer
}

type apiResponse struct {
	NumRecordings flexValue  `json:"numRecordings"`
	NumPages      flexValue  `json:"numPages"`
	Page          flexValue  `json:"page"`
	Recordings    []apiSound `json:"recordings"`
	Error         string     `json:"error"`
}

// apiSound is one API recording. Field names follow Xeno-Canto's published
// API documentation; every field is optional and numeric fields arrive as
// strings on some endpoints, hence flexValue.
type apiSound struct {
	ID        flexValue `json:"id"`
	Genus     string    `json:"gen"`
	Species   string    `json:"sp"`
	Common    string    `json:"cnt"`
	Length    flexValue `json:"len"`
	Recordist string    `json:"rec"`
	Rating    flexValue `json:"r"`
	URL       string    `json:"url"`
	File      string    `json:"file"`
	Location  string    `json:"loc"`
}

// flexValue accepts a JSON string or number and exposes it as text/number.
type flexValue struct{ raw string }

func (f *flexValue) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "null" {
		f.raw = ""
		return nil
	}
	if strings.HasPrefix(trimmed, `"`) && strings.HasSuffix(trimmed, `"`) && len(trimmed) >= 2 {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		f.raw = s
		return nil
	}
	f.raw = trimmed
	return nil
}

func (f flexValue) String() string { return f.raw }

func (f flexValue) Float() float64 {
	if f.raw == "" {
		return 0
	}
	v, err := strconv.ParseFloat(f.raw, 64)
	if err != nil {
		return 0
	}
	return v
}

// seconds parses a length that may be "mm:ss", "hh:mm:ss", "ss", or a number.
func (f flexValue) seconds() float64 {
	raw := strings.TrimSpace(f.raw)
	if raw == "" {
		return 0
	}
	if !strings.Contains(raw, ":") {
		return f.Float()
	}
	parts := strings.Split(raw, ":")
	total := 0.0
	for _, part := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil {
			return 0
		}
		total = total*60 + v
	}
	return total
}

func (p *apiProvider) Name() string { return "api" }

func (p *apiProvider) Search(ctx context.Context, scientificName string) ([]Sound, error) {
	if p.apiKey == "" {
		return nil, fmt.Errorf("%w: no API key configured", ErrProviderUnavailable)
	}
	binomial, err := normalizeBinomial(scientificName)
	if err != nil {
		return nil, err
	}
	if err := p.pacer.wait(ctx); err != nil {
		return nil, err
	}

	query := fmt.Sprintf("sp:%q nr:1-%d", binomial, FetchLengthLimitSeconds)
	base := p.endpoint
	if base == "" {
		base = apiEndpoint
	}
	endpoint := fmt.Sprintf("%s?query=%s&key=%s&page=1",
		base, url.QueryEscape(query), url.QueryEscape(p.apiKey))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		// err can echo the URL (and therefore the key) on some transport
		// failures, so the underlying error is deliberately not wrapped.
		return nil, fmt.Errorf("%w: xeno-canto API request failed", ErrProviderUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: reading xeno-canto API response failed", ErrProviderUnavailable)
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrKeyRejected, resp.StatusCode)
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w (HTTP %d)", ErrRateLimited, resp.StatusCode)
	case resp.StatusCode >= 300:
		return nil, fmt.Errorf("%w: xeno-canto API returned HTTP %d", ErrProviderUnavailable, resp.StatusCode)
	}

	var parsed apiResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("%w: xeno-canto API returned an unreadable response", ErrProviderUnavailable)
	}
	if parsed.Error != "" {
		// Query syntax and similar complaints: treat as "no example for this
		// species" rather than an outage, since retrying cannot help.
		return nil, fmt.Errorf("%w: xeno-canto: %s", ErrSoundNotFound, sanitizeProviderMessage(parsed.Error, p.apiKey))
	}

	sounds := make([]Sound, 0, len(parsed.Recordings))
	for i := range parsed.Recordings {
		rec := &parsed.Recordings[i]
		download := rec.File
		if download == "" {
			continue
		}
		sounds = append(sounds, Sound{
			RecordingID:    rec.ID.String(),
			ScientificName: joinBinomial(rec.Genus, rec.Species),
			CommonName:     rec.Common,
			Recordist:      rec.Recordist,
			// The API response carries no licence field; the recording page is
			// linked instead so attribution stays verifiable by a human.
			LicenseURL:    rec.URL,
			PageURL:       rec.URL,
			DownloadURL:   download,
			Format:        formatFromURL(download),
			LengthSeconds: rec.Length.seconds(),
			Rating:        apiRating(rec.Rating.Float()),
		})
	}
	if len(sounds) == 0 {
		return nil, ErrSoundNotFound
	}
	return sounds, nil
}

// joinBinomial builds "Genus species" from the API's split fields, keeping the
// publisher's capitalisation.
func joinBinomial(genus, species string) string {
	genus = strings.TrimSpace(genus)
	species = strings.TrimSpace(species)
	if genus == "" {
		return species
	}
	if species == "" {
		return genus
	}
	return genus + " " + species
}

// apiRating maps the API's numeric rating to the A–E letters this package
// sorts by. Xeno-Canto publishes 5 as the best; anything unmapped stays
// unknown so it neither jumps the queue nor sinks to the bottom.
func apiRating(value float64) string {
	switch {
	case value >= 4.5:
		return RatingExcellent
	case value >= 3.5:
		return RatingVeryGood
	case value >= 2.5:
		return RatingGood
	case value >= 1.5:
		return RatingFair
	case value > 0:
		return RatingPoor
	default:
		return RatingUnknown
	}
}

// sanitizeProviderMessage strips anything that could echo the API key out of a
// provider-supplied string.
func sanitizeProviderMessage(msg, apiKey string) string {
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200]
	}
	if apiKey != "" {
		msg = strings.ReplaceAll(msg, apiKey, "[key]")
	}
	return msg
}

// --- keyless listing provider ---------------------------------------------

var (
	downloadHrefRe = regexp.MustCompile(`^https://xeno-canto\.org/(\d+)/download$`)
	anubisMarkers  = []string{"Verifying visitor request", "Anubis", "anubis"}
)

// pageProvider parses the public species listing page.
type pageProvider struct {
	userAgent string
	endpoint  string // site base, overridable in tests
	client    *http.Client
	pacer     *pacer
}

func (p *pageProvider) Name() string { return "page" }

func (p *pageProvider) Search(ctx context.Context, scientificName string) ([]Sound, error) {
	pageSlug, err := SpeciesPageSlug(scientificName)
	if err != nil {
		return nil, err
	}
	if err := p.pacer.wait(ctx); err != nil {
		return nil, err
	}

	base := p.endpoint
	if base == "" {
		base = siteBase
	}
	endpoint := base + "/species/" + pageSlug
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrProviderUnavailable, err)
	}
	req.Header.Set("Accept", "text/html")
	req.Header.Set("User-Agent", p.userAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: xeno-canto page request failed", ErrProviderUnavailable)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: reading xeno-canto page failed", ErrProviderUnavailable)
	}
	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrSoundNotFound
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w (HTTP %d)", ErrRateLimited, resp.StatusCode)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: xeno-canto returned HTTP %d", ErrProviderUnavailable, resp.StatusCode)
	}
	text := string(body)
	for _, marker := range anubisMarkers {
		if strings.Contains(text, marker) {
			// Bot protection: the page is a challenge, not a listing. Reading
			// it further would produce garbage, so stop here.
			return nil, fmt.Errorf("%w: xeno-canto is serving a bot-verification page", ErrProviderUnavailable)
		}
	}

	sounds := parseRecordingRows(text)
	// Keep only this species: a listing page can legitimately mention relatives
	// ("also" columns), and a mismatched name would attribute the wrong bird.
	binomial, _ := normalizeBinomial(scientificName)
	filtered := make([]Sound, 0, len(sounds))
	for i := range sounds {
		own, err := normalizeBinomial(sounds[i].ScientificName)
		if err == nil && own == binomial {
			filtered = append(filtered, sounds[i])
		}
	}
	if len(filtered) == 0 {
		if len(sounds) == 0 {
			return nil, fmt.Errorf("%w: no recordings on the species page", ErrSoundNotFound)
		}
		return nil, fmt.Errorf("%w: species page listed no matching recordings", ErrSoundNotFound)
	}
	return filtered, nil
}

// parseRecordingRows extracts every recording row from a species listing page.
//
// Each row carries an <a href="…/<id>/download"> link; the row is read by
// walking the enclosing <tr>, never by matching over the whole document.
func parseRecordingRows(page string) []Sound {
	doc, err := html.Parse(strings.NewReader(page))
	if err != nil {
		return nil
	}
	var sounds []Sound
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			if href := attr(n, "href"); href != "" {
				if m := downloadHrefRe.FindStringSubmatch(href); len(m) > 1 && !seen[m[1]] {
					seen[m[1]] = true
					row := ancestor(n, "tr")
					sound := Sound{RecordingID: m[1], DownloadURL: href}
					if row != nil {
						sound = enrichRow(row, &sound)
					}
					sounds = append(sounds, sound)
				}
			}
		}
		for child := n.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(doc)
	return sounds
}

// enrichRow reads the fields this feature needs out of one listing row.
func enrichRow(row *html.Node, sound *Sound) Sound {
	// Attribution: the recordist links to their contributor page.
	if link := findNode(row, func(n *html.Node) bool {
		return n.Data == "a" && strings.Contains(attr(n, "href"), "/contributor/")
	}); link != nil {
		sound.Recordist = strings.TrimSpace(textOf(link))
	}

	// Common and scientific name live in dedicated spans on the name cell.
	if span := findNode(row, func(n *html.Node) bool {
		return n.Data == "span" && hasClass(n, "sci-name")
	}); span != nil {
		sound.ScientificName = strings.TrimSpace(textOf(span))
	}
	if span := findNode(row, func(n *html.Node) bool {
		return n.Data == "span" && hasClass(n, "common-name")
	}); span != nil {
		sound.CommonName = strings.TrimSpace(textOf(span))
	}

	// License: a span titled with the license name wrapping the CC icon link.
	if span := findNode(row, func(n *html.Node) bool {
		title := attr(n, "title")
		return n.Data == "span" && strings.Contains(title, "Creative Commons")
	}); span != nil {
		sound.LicenseName = strings.TrimSpace(attr(span, "title"))
		matchesCC := func(n *html.Node) bool {
			return n.Data == "a" && strings.Contains(attr(n, "href"), "creativecommons.org")
		}
		link := findNode(span, matchesCC)
		if link == nil {
			// The live markup nests the deed link inside another <a>, and
			// the HTML5 parser's adoption-agency lifts it out of the title
			// span; fall back to the deed link elsewhere in the row.
			link = findNode(row, matchesCC)
		}
		if link != nil {
			sound.LicenseURL = attr(link, "href")
		}
	}

	// Rating: the marked <li> inside the rating block. Anonymous listing pages
	// publish no marks, so an empty result is normal and leaves the pick to
	// length and format.
	if li := findNode(row, func(n *html.Node) bool {
		if n.Data != "li" || !strings.HasPrefix(attr(n, "id"), "rating-"+sound.RecordingID+"-") {
			return false
		}
		return hasClass(n, "selected") || hasClass(n, "current")
	}); li != nil {
		rating := strings.TrimSpace(textOf(li))
		if rating != "" {
			sound.Rating = strings.ToUpper(rating[:1])
		}
	}

	// Length and playback URL: the inline <audio> gives the short mp3 Xeno-Canto
	// generates for every upload, which is smaller and ready to serve; its
	// filename also carries the format. The length cell is a bare "m:ss"/"ss".
	if audio := findNode(row, func(n *html.Node) bool { return n.Data == "audio" }); audio != nil {
		if src := attr(audio, "src"); src != "" {
			sound.PlaybackURL = src
			if format := formatFromURL(src); format != "" {
				sound.Format = format
			}
		}
	}
	if sound.Format == "" {
		sound.Format = formatFromURL(sound.DownloadURL)
	}
	sound.LengthSeconds = rowLengthSeconds(row)

	// The playable file the frontend should be pointed at. Xeno-Canto hands out
	// both the original upload (/download) and the generated mp3 (/sounds/…);
	// the mp3 is what we want: short, small, and playable by every browser.
	if sound.PlaybackURL != "" {
		sound.DownloadURL = sound.PlaybackURL
	}
	sound.PageURL = siteBase + "/" + sound.RecordingID
	return *sound
}

// rowLengthSeconds finds the duration cell of a listing row.
func rowLengthSeconds(row *html.Node) float64 {
	var cells []*html.Node
	for cell := row.FirstChild; cell != nil; cell = cell.NextSibling {
		if cell.Type == html.ElementNode && cell.Data == "td" {
			cells = append(cells, cell)
		}
	}
	for _, cell := range cells {
		text := strings.TrimSpace(textOf(cell))
		if text == "" || len(text) > 12 {
			continue
		}
		if seconds, ok := parseClock(text); ok {
			return seconds
		}
	}
	return 0
}

// parseClock reads "m:ss", "h:mm:ss" or plain seconds.
func parseClock(text string) (float64, bool) {
	if strings.ContainsAny(text, " \n\t") {
		return 0, false
	}
	parts := strings.Split(text, ":")
	total := 0.0
	for _, part := range parts {
		if part == "" {
			return 0, false
		}
		v, err := strconv.ParseFloat(part, 64)
		if err != nil || v < 0 {
			return 0, false
		}
		total = total*60 + v
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

// --- shared helpers --------------------------------------------------------

func attr(n *html.Node, name string) string {
	for _, a := range n.Attr {
		if a.Key == name {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for field := range strings.FieldsSeq(attr(n, "class")) {
		if field == class {
			return true
		}
	}
	return false
}

// findNode returns the first node in the subtree matching match.
func findNode(n *html.Node, match func(*html.Node) bool) *html.Node {
	if n.Type == html.ElementNode && match(n) {
		return n
	}
	for child := n.FirstChild; child != nil; child = child.NextSibling {
		if found := findNode(child, match); found != nil {
			return found
		}
	}
	return nil
}

// ancestor walks up to the nearest element with the given tag name.
func ancestor(n *html.Node, tag string) *html.Node {
	for p := n.Parent; p != nil; p = p.Parent {
		if p.Type == html.ElementNode && p.Data == tag {
			return p
		}
	}
	return nil
}

// textOf concatenates the text of a subtree.
func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
			sb.WriteString(" ")
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(sb.String()), " ")
}

// formatFromURL maps a file URL onto "mp3"/"wav"/"ogg"/"flac".
func formatFromURL(rawURL string) string {
	if parsed, err := url.Parse(rawURL); err == nil {
		rawURL = parsed.Path
	}
	if idx := strings.LastIndex(rawURL, "."); idx >= 0 && idx < len(rawURL)-1 {
		switch ext := strings.ToLower(rawURL[idx+1:]); ext {
		case "mp3", "wav", "ogg", "flac", "m4a":
			return ext
		}
	}
	return ""
}

// mimeTypeForFormat maps a known format onto a response content type.
func mimeTypeForFormat(format string) string {
	switch format {
	case "mp3":
		return "audio/mpeg"
	case "wav":
		return "audio/wav"
	case "ogg":
		return "audio/ogg"
	case "flac":
		return "audio/flac"
	case "m4a":
		return "audio/mp4"
	default:
		return "application/octet-stream"
	}
}

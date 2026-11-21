// names.go: turning a scientific name into a file name, and back.
//
// The species name arrives from a detection record, so it is untrusted input
// on the path to the filesystem: everything that becomes a file name is
// reduced to [a-z0-9_], which also keeps the cache browsable.
package speciesaudio

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Slug turns "Corvus brachyrhynchos orientalis" into "corvus_brachyrhynchos".
// Subspecies names are folded to the binomial, matching what Xeno-Canto
// indexes. Anything that cannot produce a safe, non-empty slug is rejected —
// ErrInvalidName here means "this would have escaped the cache directory".
func Slug(scientificName string) (string, error) {
	binomial, err := normalizeBinomial(scientificName)
	if err != nil {
		return "", err
	}
	slug := strings.ReplaceAll(binomial, " ", "_")
	if !filepath.IsLocal(slug) || slug == "." || slug == ".." {
		return "", fmt.Errorf("%w: %q cannot be used as a file name", ErrInvalidName, scientificName)
	}
	return slug, nil
}

// SpeciesPageSlug turns "Corvus brachyrhynchos" into the listing page segment
// "Corvus-brachyrhynchos" (title case, hyphenated, binomial only).
func SpeciesPageSlug(scientificName string) (string, error) {
	fields := strings.Fields(strings.TrimSpace(scientificName))
	if len(fields) < 2 {
		return "", fmt.Errorf("%w: %q is not a binomial", ErrInvalidName, scientificName)
	}
	parts := make([]string, 0, 2)
	for i, field := range fields[:2] {
		if !isAlphaOnly(field) {
			return "", fmt.Errorf("%w: unexpected characters in %q", ErrInvalidName, scientificName)
		}
		// Xeno-Canto capitalizes only the genus in page slugs:
		// "Corvus-brachyrhynchos", never "Corvus-Brachyrhynchos".
		lowered := strings.ToLower(field)
		if i == 0 {
			lowered = strings.ToUpper(lowered[:1]) + lowered[1:]
		}
		parts = append(parts, lowered)
	}
	return parts[0] + "-" + parts[1], nil
}

// nameFromSlug rebuilds a display name from a cache slug. It is a best-effort
// inverse used by the prefetch worker, which only ever stores slugs.
func nameFromSlug(slug string) string {
	parts := strings.FieldsFunc(slug, func(r rune) bool { return r == '_' || r == '-' })
	if len(parts) == 0 {
		return slug
	}
	for i, part := range parts {
		lowered := strings.ToLower(part)
		if lowered == "" {
			continue
		}
		parts[i] = strings.ToUpper(lowered[:1]) + lowered[1:]
	}
	return strings.Join(parts, " ")
}

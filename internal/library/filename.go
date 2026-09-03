package library

import (
	"regexp"
	"strings"
)

// Parsed holds series/issue metadata recovered from a file name.
type Parsed struct {
	Series string // empty when nothing before the number looked like a series name
	Number string // issue number as text ("012", "12.1", "Annual 1" stays in Title fallback)
	Year   string // "2020" when a (YYYY) group is present
	Title  string // fallback: whole cleaned name when no pattern matched
}

var (
	// numberToken matches a whole issue number token: digits with an
	// optional decimal part ("012", "12", "12.1").
	numberToken = regexp.MustCompile(`^\d+(\.\d+)?$`)

	// yearToken matches a bare 4 digit year in the 1900-2099 range.
	yearToken = regexp.MustCompile(`^(19|20)\d{2}$`)

	// trailingParen matches a single "(...)" group at the very end of the
	// string, including any leading whitespace.
	trailingParen = regexp.MustCompile(`\s*\(([^()]*)\)\s*$`)

	// multiSpace matches runs of whitespace to collapse.
	multiSpace = regexp.MustCompile(`\s+`)
)

// knownExts are file extensions stripped before parsing.
var knownExts = []string{".cbz", ".cbr", ".pdf"}

// ParseFilename extracts series name, issue number and year from a comic
// file name (without directory, with or without extension).
func ParseFilename(name string) Parsed {
	cleaned := cleanName(stripKnownExt(name))
	base, year := stripTrailingParens(cleaned)

	// Pattern 1: "Title #012" (optionally followed by a year group already
	// stripped above).
	if idx := strings.LastIndex(base, "#"); idx >= 0 {
		seriesPart := base[:idx]
		numPart := strings.TrimSpace(base[idx+1:])
		if numberToken.MatchString(numPart) {
			return Parsed{
				Series: cleanSeriesSuffix(seriesPart),
				Number: numPart,
				Year:   year,
			}
		}
	}

	// Pattern 2/3: "Title 012 (2020)" or "Title v2 015" — the number is the
	// last standalone whitespace-delimited token, whatever precedes it
	// (including a "v2" volume marker) stays part of the series name.
	if fields := strings.Fields(base); len(fields) > 0 {
		last := fields[len(fields)-1]
		if numberToken.MatchString(last) {
			series := cleanSeriesSuffix(strings.Join(fields[:len(fields)-1], " "))
			return Parsed{
				Series: series,
				Number: last,
				Year:   year,
			}
		}
	}

	// Pattern 4: nothing matched (a one-shot or unconventional name) — the
	// name without the trailing "(...)" groups becomes the title, and a year
	// found in those groups is still kept.
	return Parsed{Title: base, Year: year}
}

// stripKnownExt removes a trailing .cbz/.cbr/.pdf extension (case-insensitive)
// if present.
func stripKnownExt(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range knownExts {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name
}

// cleanName normalizes whitespace: underscores become spaces, runs of
// whitespace collapse to a single space, and the result is trimmed.
func cleanName(name string) string {
	s := strings.ReplaceAll(name, "_", " ")
	s = multiSpace.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// stripTrailingParens repeatedly removes "(...)" groups from the end of s,
// returning the remaining base string and the first (rightmost) group whose
// content looks like a year, if any.
func stripTrailingParens(s string) (base string, year string) {
	for {
		m := trailingParen.FindStringSubmatch(s)
		if m == nil {
			break
		}
		content := strings.TrimSpace(m[1])
		if year == "" && yearToken.MatchString(content) {
			year = content
		}
		s = s[:len(s)-len(m[0])]
	}
	return strings.TrimSpace(s), year
}

// cleanSeriesSuffix trims a trailing " -" or "," left over after removing
// the issue number/year from a series name candidate.
func cleanSeriesSuffix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasSuffix(s, "-") {
		s = strings.TrimSpace(strings.TrimSuffix(s, "-"))
	}
	s = strings.TrimSuffix(s, ",")
	return strings.TrimSpace(s)
}

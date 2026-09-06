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
	Title  string // subtitle after the issue number, or (no number found) the whole cleaned name
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

	// Pattern 2/3: "Title 012 (2020)", "Title v2 015" or "Title 052 - Subtitle
	// 1" — the issue number is the first standalone whitespace-delimited
	// number token, skipping one that looks like an embedded year (e.g. the
	// "2000" in "2000 AD 1957") as long as a later number token exists to use
	// instead. Whatever precedes the chosen number (a "v2" volume marker
	// included) stays the series name; whatever follows becomes the title
	// instead of being mistaken for a second issue number (e.g. a subtitle
	// like "Barbary Coast 1" that itself ends in a digit).
	if series, number, title, ok := parseNumberedTitle(base); ok {
		return Parsed{
			Series: series,
			Number: number,
			Year:   year,
			Title:  title,
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

// cleanTitlePrefix trims a leading "-" or "," left over after removing the
// issue number from a subtitle candidate.
func cleanTitlePrefix(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "-") {
		s = strings.TrimSpace(strings.TrimPrefix(s, "-"))
	}
	s = strings.TrimPrefix(s, ",")
	return strings.TrimSpace(s)
}

// parseNumberedTitle splits base into a series/number/title triple around
// its issue number token, or reports ok=false when it has none.
//
// The issue number is the first whitespace-delimited token made of digits
// only — except when it looks like a bare embedded year (1900-2099, e.g. the
// "2000" in "2000 AD 1957") and a later number token exists to use instead;
// that later token is preferred and the year-like one stays part of the
// series name. This keeps "2000 AD 1957" reading as series "2000 AD" #1957,
// and "The Boys 52 - Barbary Coast 1" as series "The Boys" #52 with subtitle
// "Barbary Coast 1", rather than picking up a subtitle's own trailing digit.
func parseNumberedTitle(base string) (series, number, title string, ok bool) {
	fields := strings.Fields(base)
	var candidates []int
	for i, f := range fields {
		if numberToken.MatchString(f) {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) == 0 {
		return "", "", "", false
	}

	chosen := candidates[len(candidates)-1]
	for ci, idx := range candidates {
		if yearToken.MatchString(fields[idx]) && ci != len(candidates)-1 {
			continue // looks like an embedded year, and a later number exists
		}
		chosen = idx
		break
	}

	series = cleanSeriesSuffix(strings.Join(fields[:chosen], " "))
	number = fields[chosen]
	title = cleanTitlePrefix(strings.Join(fields[chosen+1:], " "))
	return series, number, title, true
}

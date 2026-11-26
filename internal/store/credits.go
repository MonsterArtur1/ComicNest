package store

import "strings"

// SplitList splits s on any of the separator characters, trims every part and
// drops empty ones and case-insensitive duplicates (first spelling wins),
// keeping the original order.
func SplitList(s, seps string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(seps, r) })
	var out []string
	seen := make(map[string]bool, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		key := strings.ToLower(p)
		if p == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, p)
	}
	return out
}

// SplitCredits splits a comma-separated credits string (issues.writer,
// issues.artist) into names, deduplicated case-insensitively.
func SplitCredits(s string) []string {
	return SplitList(s, ",")
}

// NormalizeName is the identity of a person or character name: trimmed,
// inner whitespace collapsed, lower-cased. Names that normalize equal are the
// same creator/character.
func NormalizeName(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// Package textutil holds string helpers shared across logstack. It exists so
// the fleet's rune-boundary truncation is one function with one spelling rather
// than a copy per package: inber-party and agent-store carry an internal/textutil
// for the same reason.
package textutil

import "unicode/utf8"

// TruncateAtRuneBoundary returns the longest prefix of s that is at most
// maxBytes long and does not split a rune.
//
// Cutting a string at a fixed byte offset splits whatever rune straddles that
// offset, and the result is not valid UTF-8. Nothing reports it: encoding/json
// substitutes U+FFFD rather than returning an error, so the reader sees a
// replacement character and no error is raised anywhere along the path.
//
// It also never panics on a short string or a negative budget, which a plain
// s[:maxBytes] does. Callers that cut to a fixed width without a length guard
// depend on that.
//
// Any ellipsis is the caller's and sits outside maxBytes; this function decides
// where the cut lands, not what the budget covers.
func TruncateAtRuneBoundary(s string, maxBytes int) string {
	if maxBytes <= 0 {
		return ""
	}
	if len(s) <= maxBytes {
		return s
	}
	// s[cut] is the first byte past the prefix. While it is a continuation
	// byte, a rune straddles the cut, so move the cut earlier.
	cut := maxBytes
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

package design

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// BatchSlug names a batch folder from a batch name or brief: lowercase ASCII
// letters and digits joined by single dashes, at most 40 characters, and
// "design" when nothing usable remains.
func BatchSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(name) {
		if r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			b.WriteRune(r)
			dash = false
			if b.Len() >= 40 {
				break
			}
			continue
		}
		dash = true
	}
	if b.Len() == 0 {
		return "design"
	}
	return strings.TrimRight(b.String(), "-")
}

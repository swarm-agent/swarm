package runtime

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

var artifactV3TitleIgnored = regexp.MustCompile(`(?is)<!--.*?-->|<script\b[^>]*>.*?</script\s*>|<style\b[^>]*>.*?</style\s*>`)
var artifactV3TitleElement = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title\s*>`)

// Display metadata only, derived from the verified selected Git entrypoint.
// Never executes authored code, trusts a candidate title as head metadata, or
// changes a stored manifest. Untitled projects retain an explicit generic label.
func artifactV3DocumentTitle(body []byte) string {
	match := artifactV3TitleElement.FindSubmatch(artifactV3TitleIgnored.ReplaceAll(body, nil))
	if len(match) < 2 {
		return "Untitled artifact"
	}
	title := html.UnescapeString(string(match[1]))
	title = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, title)
	title = strings.Join(strings.Fields(title), " ")
	runes := []rune(title)
	if len(runes) > 256 {
		title = string(runes[:255]) + "…"
	}
	if title == "" {
		return "Untitled artifact"
	}
	return title
}

package design

import (
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	fencedHTML = regexp.MustCompile("(?is)```(?:html)?\\s*(<!doctype html.*?</html>|<html.*?</html>)\\s*```")
	bareHTML   = regexp.MustCompile("(?is)(<!doctype html.*?</html>|<html.*?</html>)")
)

// ExtractHTML returns the HTML document in a model reply: a fenced html block
// first, then a bare document. A reply with no document is an error.
func ExtractHTML(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if m := fencedHTML.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1]), nil
	}
	if m := bareHTML.FindStringSubmatch(text); len(m) > 1 {
		return strings.TrimSpace(m[1]), nil
	}
	return "", errors.New("the reply contains no complete <html>…</html> document")
}

// StructuralCheck finds problems that need no browser: an incomplete
// document, a <base> element, invalid UTF-8 or an oversized file.
func StructuralCheck(html string) []Diagnostic {
	var out []Diagnostic
	lower := strings.ToLower(html)
	if !strings.Contains(lower, "<html") || !strings.Contains(lower, "</html>") {
		out = append(out, Diagnostic{Kind: "incomplete_document", Message: "the document is not a complete <html>…</html> page"})
	}
	if !strings.Contains(lower, "<body") {
		out = append(out, Diagnostic{Kind: "incomplete_document", Message: "the document has no <body> element"})
	}
	if strings.Contains(lower, "<base ") || strings.Contains(lower, "<base>") {
		out = append(out, Diagnostic{Kind: "base_element", Message: "remove the <base> element; the page must stand alone"})
	}
	if !utf8.ValidString(html) {
		out = append(out, Diagnostic{Kind: "invalid_utf8", Message: "the document is not valid UTF-8"})
	}
	if len(html) > MaxHTMLBytes {
		out = append(out, Diagnostic{Kind: "too_large", Message: "the document is larger than 4 MiB"})
	}
	return out
}

// boundDiagnostics keeps at most maxDiagnostics entries with bounded messages.
func boundDiagnostics(in []Diagnostic) []Diagnostic {
	if len(in) > maxDiagnostics {
		in = in[:maxDiagnostics]
	}
	out := make([]Diagnostic, len(in))
	for i, d := range in {
		d.Message = truncate(d.Message, maxDiagnosticLen)
		d.URL = truncate(d.URL, maxDiagnosticLen)
		out[i] = d
	}
	return out
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

package tool

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"unicode"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

var (
	artifactHTMLIterationManifest = regexp.MustCompile(`(?is)<script\s+([^>]*)>(.*?)</script\s*>`)
	artifactHTMLIterationID       = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*["']swarm-iteration-manifest["'](?:\s|$)`)
	artifactHTMLManifestType      = regexp.MustCompile(`(?i)(?:^|\s)type\s*=\s*["']application/json["'](?:\s|$)`)
	artifactHTMLRegion            = regexp.MustCompile(`(?is)<(header|main|section|article|nav|aside|footer)\b([^>]*)>`)
)

type artifactHTMLIterationManifestValue struct {
	Version    string                         `json:"version"`
	DurationMS int64                          `json:"duration_ms"`
	Sections   []artifactHTMLIterationSection `json:"sections"`
}

type artifactHTMLIterationSection struct {
	ID        string            `json:"id"`
	Label     string            `json:"label"`
	StartMS   int64             `json:"start_ms"`
	EndMS     int64             `json:"end_ms"`
	Narration []json.RawMessage `json:"narration,omitempty"`
}

// deriveArtifactHTMLParts turns authored structure in one complete HTML file into
// source-bound review/edit targets. It never splits or rewrites the HTML bytes and
// therefore does not create an authoritative byte composition. Focused byte-part
// iteration remains reserved for explicit independently stored initial_parts.
func deriveArtifactHTMLParts(body []byte, mediaType string) []pebblestore.SessionArtifactPart {
	if canonicalArtifactMediaType(mediaType) != "text/html" || len(body) == 0 {
		return nil
	}
	parts := make([]pebblestore.SessionArtifactPart, 0, 8)
	seen := make(map[string]struct{})
	appendPart := func(part pebblestore.SessionArtifactPart) {
		part.ID, part.Label = strings.TrimSpace(part.ID), strings.TrimSpace(part.Label)
		if len(parts) >= pebblestore.SessionArtifactMaxParts || !validManagedArtifactStableID(part.ID) || part.Label == "" || len(part.Label) > 256 {
			return
		}
		if _, duplicate := seen[part.ID]; duplicate {
			return
		}
		seen[part.ID] = struct{}{}
		parts = append(parts, part)
	}

	for _, script := range artifactHTMLIterationManifest.FindAllSubmatch(body, -1) {
		if !artifactHTMLIterationID.Match(script[1]) || !artifactHTMLManifestType.Match(script[1]) {
			continue
		}
		var manifest artifactHTMLIterationManifestValue
		decoder := json.NewDecoder(bytes.NewReader(script[2]))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&manifest) != nil || ensureJSONEOF(decoder) != nil || manifest.Version != "swarm.iteration/v1" || manifest.DurationMS <= 0 || len(manifest.Sections) == 0 || len(manifest.Sections) > pebblestore.SessionArtifactMaxParts {
			continue
		}
		previousEnd := int64(0)
		for _, section := range manifest.Sections {
			if section.StartMS < previousEnd || section.EndMS <= section.StartMS || section.EndMS > manifest.DurationMS {
				continue
			}
			appendPart(pebblestore.SessionArtifactPart{ID: section.ID, Label: firstNonEmptyString(section.Label, artifactHTMLPartLabel(section.ID)), Kind: "temporal", StartMs: section.StartMS, EndMs: section.EndMS})
			previousEnd = section.EndMS
		}
	}
	// A short swarm.animation/v1 artifact does not need a long-form iteration
	// manifest, but Video Studio still needs durable timeline metadata to verify
	// that every live candidate matches its stable plan part. Preserve authored
	// iteration sections when present; otherwise project the canonical animation
	// manifest as one whole-animation temporal review target.
	if len(parts) == 0 {
		if manifest, err := parseAnimationManifest(body); err == nil {
			appendPart(pebblestore.SessionArtifactPart{ID: "animation", Label: "Animation", Kind: "temporal", EndMs: int64(manifest.DurationMS)})
		}
	}

	if manifest, err := parseCaptureManifest(body); err == nil {
		for _, state := range manifest.States {
			appendPart(pebblestore.SessionArtifactPart{ID: state.ID, Label: firstNonEmptyString(state.Label, artifactHTMLPartLabel(state.ID)), Kind: "state", StateID: state.ID})
		}
	}

	for _, region := range artifactHTMLRegion.FindAllSubmatch(body, -1) {
		attributes := region[2]
		id := artifactHTMLAttribute(attributes, "id")
		if !validManagedArtifactStableID(id) {
			continue
		}
		label := firstNonEmptyString(
			artifactHTMLAttribute(attributes, "data-swarm-part-label"),
			artifactHTMLAttribute(attributes, "aria-label"),
			artifactHTMLPartLabel(id),
		)
		appendPart(pebblestore.SessionArtifactPart{ID: id, Label: label, Kind: "selector", Selector: "#" + id})
	}
	return parts
}

func artifactHTMLAttribute(attributes []byte, name string) string {
	pattern := regexp.MustCompile(`(?i)(?:^|\s)` + regexp.QuoteMeta(name) + `\s*=\s*["']([^"']+)["']`)
	match := pattern.FindSubmatch(attributes)
	if len(match) != 2 {
		return ""
	}
	return strings.TrimSpace(string(match[1]))
}

func artifactHTMLPartLabel(id string) string {
	words := strings.FieldsFunc(strings.TrimSpace(id), func(character rune) bool {
		return character == '-' || character == '_' || character == '.'
	})
	for index, word := range words {
		runes := []rune(strings.ToLower(word))
		if len(runes) != 0 {
			runes[0] = unicode.ToUpper(runes[0])
			words[index] = string(runes)
		}
	}
	return strings.Join(words, " ")
}

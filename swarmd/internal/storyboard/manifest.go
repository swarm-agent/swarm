package storyboard

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	Version                 = "swarm.storyboard/v1"
	ManifestElementID       = "swarm-storyboard-manifest"
	MaxSections             = 16
	MaxManifestBytes        = 64 << 10
	MaxDurationMs     int64 = 3_600_000
)

const (
	ProductionStatePending = "pending"
	ProductionStateReady   = "ready"
)

var (
	idPattern     = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	scriptPattern = regexp.MustCompile(`(?is)<script\s+([^>]*)>(.*?)</script\s*>`)
	manifestID    = regexp.MustCompile(`(?i)(?:^|\s)id\s*=\s*["']swarm-storyboard-manifest["'](?:\s|$)`)
	manifestType  = regexp.MustCompile(`(?i)(?:^|\s)type\s*=\s*["']application/json["'](?:\s|$)`)
)

type Manifest struct {
	Version  string    `json:"version"`
	Sections []Section `json:"sections"`
}

type Section struct {
	ID                  string   `json:"id"`
	CaptureStateID      string   `json:"capture_state_id"`
	Title               string   `json:"title"`
	DurationMs          int64    `json:"duration_ms"`
	Narration           string   `json:"narration,omitempty"`
	OnScreenText        string   `json:"on_screen_text,omitempty"`
	CreativeDirection   string   `json:"creative_direction"`
	FilmingRequirements []string `json:"filming_requirements"`
	ProductionState     string   `json:"production_state"`
}

type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

func HasManifest(html []byte) bool {
	for _, script := range scriptPattern.FindAllSubmatch(html, -1) {
		if manifestID.Match(script[1]) {
			return true
		}
	}
	return false
}

func ParseHTML(html []byte, captureStateIDs []string) (Manifest, error) {
	if len(html) == 0 || !utf8.Valid(html) {
		return Manifest{}, newError("storyboard_manifest_invalid", "storyboard source is not valid UTF-8 HTML")
	}
	scripts := scriptPattern.FindAllSubmatch(html, -1)
	manifests := make([][]byte, 0, 1)
	for _, script := range scripts {
		if manifestID.Match(script[1]) {
			if !manifestType.Match(script[1]) {
				return Manifest{}, newError("storyboard_manifest_invalid", "storyboard manifest has an invalid media type")
			}
			manifests = append(manifests, script[2])
		}
	}
	if len(manifests) == 0 {
		return Manifest{}, newError("storyboard_manifest_missing", "canonical swarm.storyboard/v1 manifest is missing")
	}
	if len(manifests) != 1 || len(manifests[0]) > MaxManifestBytes {
		return Manifest{}, newError("storyboard_manifest_invalid", "storyboard manifest is duplicated or exceeds fixed bounds")
	}

	decoder := json.NewDecoder(bytes.NewReader(manifests[0]))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, newError("storyboard_manifest_invalid", "storyboard manifest is malformed")
	}
	if err := ensureEOF(decoder); err != nil {
		return Manifest{}, newError("storyboard_manifest_invalid", "storyboard manifest contains trailing data")
	}
	if manifest.Version != Version || len(manifest.Sections) < 1 || len(manifest.Sections) > MaxSections {
		return Manifest{}, newError("storyboard_manifest_invalid", "storyboard manifest version or section count is invalid")
	}

	captureStates := make(map[string]struct{}, len(captureStateIDs))
	for _, id := range captureStateIDs {
		captureStates[id] = struct{}{}
	}
	seenSections := make(map[string]struct{}, len(manifest.Sections))
	seenStates := make(map[string]struct{}, len(manifest.Sections))
	for index := range manifest.Sections {
		section := &manifest.Sections[index]
		if !idPattern.MatchString(section.ID) {
			return Manifest{}, newError("storyboard_section_invalid", "storyboard contains an invalid section id")
		}
		if _, exists := seenSections[section.ID]; exists {
			return Manifest{}, newError("storyboard_section_duplicate", "storyboard contains duplicate section ids")
		}
		seenSections[section.ID] = struct{}{}
		if !idPattern.MatchString(section.CaptureStateID) {
			return Manifest{}, newError("storyboard_capture_state_invalid", "storyboard contains an invalid capture_state_id")
		}
		if _, exists := seenStates[section.CaptureStateID]; exists {
			return Manifest{}, newError("storyboard_capture_state_duplicate", "storyboard sections must map to unique capture states")
		}
		seenStates[section.CaptureStateID] = struct{}{}
		if len(captureStates) > 0 {
			if _, exists := captureStates[section.CaptureStateID]; !exists {
				return Manifest{}, newError("storyboard_capture_state_missing", "storyboard references a state not declared by swarm.capture/v1")
			}
		}
		if err := validateText(section.Title, 200, true); err != nil {
			return Manifest{}, newError("storyboard_section_invalid", "storyboard section title is invalid")
		}
		if section.DurationMs < 1 || section.DurationMs > MaxDurationMs {
			return Manifest{}, newError("storyboard_duration_invalid", "storyboard section duration_ms is outside fixed bounds")
		}
		for _, field := range []struct {
			value    string
			limit    int
			required bool
			name     string
		}{{section.Narration, 4000, false, "narration"}, {section.OnScreenText, 2000, false, "on_screen_text"}, {section.CreativeDirection, 4000, true, "creative_direction"}} {
			if err := validateText(field.value, field.limit, field.required); err != nil {
				return Manifest{}, newError("storyboard_section_invalid", "storyboard section "+field.name+" is invalid")
			}
		}
		if len(section.FilmingRequirements) < 1 || len(section.FilmingRequirements) > 16 {
			return Manifest{}, newError("storyboard_filming_requirements_invalid", "storyboard filming_requirements must contain 1 to 16 entries")
		}
		for _, requirement := range section.FilmingRequirements {
			if err := validateText(requirement, 1000, true); err != nil {
				return Manifest{}, newError("storyboard_filming_requirements_invalid", "storyboard contains an invalid filming requirement")
			}
		}
		switch section.ProductionState {
		case ProductionStatePending, ProductionStateReady:
		default:
			return Manifest{}, newError("storyboard_production_state_invalid", "storyboard production_state must be pending or ready")
		}
	}
	return manifest, nil
}

func CaptureStateIDs(manifest Manifest) []string {
	ids := make([]string, len(manifest.Sections))
	for i := range manifest.Sections {
		ids[i] = manifest.Sections[i].CaptureStateID
	}
	return ids
}

func ErrorCode(err error) string {
	var storyboardErr *Error
	if errors.As(err, &storyboardErr) {
		return storyboardErr.Code
	}
	return "storyboard_manifest_invalid"
}

func SafeMessage(err error) string {
	var storyboardErr *Error
	if errors.As(err, &storyboardErr) {
		return storyboardErr.Message
	}
	return "storyboard manifest is invalid"
}

func newError(code, message string) error { return &Error{Code: code, Message: message} }

func validateText(value string, maxBytes int, required bool) error {
	if strings.TrimSpace(value) != value || (required && value == "") || len(value) > maxBytes || !utf8.ValidString(value) {
		return errors.New("invalid text")
	}
	if strings.IndexFunc(value, func(r rune) bool { return r < 0x20 && r != '\n' && r != '\t' || r == 0x7f }) >= 0 {
		return errors.New("invalid control character")
	}
	return nil
}

func ensureEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	}
	return errors.New("trailing JSON")
}

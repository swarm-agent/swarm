package artifact

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	OutputRequirementsRegistryVersion = "2026-08-14.v1"
	OutputRequirementsReviewedSource  = "Swarm reviewed social-media output presets"
	OutputRequirementsReviewedDate    = "2026-08-14"
	OutputRequirementsMinDimension    = 1
	OutputRequirementsMaxDimension    = 16384
)

var outputRequirementsAllowedOrientations = map[string]struct{}{
	"landscape": {},
	"portrait":  {},
	"square":    {},
}

// OutputRequirementsInput is the model/parent-authored request. ResolveOutputRequirements
// converts it once into an immutable server-owned snapshot.
type OutputRequirementsInput struct {
	Preset      string `json:"preset,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
	AspectRatio string `json:"aspect_ratio,omitempty"`
	Orientation string `json:"orientation,omitempty"`
}

type OutputPreset struct {
	ID              string   `json:"id"`
	Aliases         []string `json:"aliases,omitempty"`
	Width           int      `json:"width"`
	Height          int      `json:"height"`
	AspectRatio     string   `json:"aspect_ratio"`
	Orientation     string   `json:"orientation"`
	RegistryVersion string   `json:"registry_version"`
	ReviewedSource  string   `json:"reviewed_source"`
	ReviewedDate    string   `json:"reviewed_date"`
}

type outputPresetDefinition struct {
	id      string
	aliases []string
	width   int
	height  int
}

var outputPresetDefinitions = []outputPresetDefinition{
	{id: "x_header", aliases: []string{"twitter_header", "twitter_banner"}, width: 1500, height: 500},
	{id: "x_video_landscape", aliases: []string{"x_video", "twitter_video"}, width: 1920, height: 1080},
	{id: "x_video_portrait", width: 1080, height: 1920},
	{id: "full_hd_landscape", width: 1920, height: 1080},
	{id: "vertical_video", width: 1080, height: 1920},
	{id: "square_1080", width: 1080, height: 1080},
}

func init() {
	if err := validateOutputPresetRegistry(outputPresetDefinitions); err != nil {
		panic(err)
	}
}

func ListOutputPresets() []OutputPreset {
	out := make([]OutputPreset, 0, len(outputPresetDefinitions))
	for _, definition := range outputPresetDefinitions {
		out = append(out, OutputPreset{
			ID: definition.id, Aliases: append([]string(nil), definition.aliases...),
			Width: definition.width, Height: definition.height,
			AspectRatio: normalizedRatio(definition.width, definition.height), Orientation: normalizedOrientation(definition.width, definition.height),
			RegistryVersion: OutputRequirementsRegistryVersion, ReviewedSource: OutputRequirementsReviewedSource, ReviewedDate: OutputRequirementsReviewedDate,
		})
	}
	return out
}

// ResolveOutputRequirements applies deterministic precedence: paired dimensions,
// then a canonical preset/alias, then omission. Natural-language inference is
// intentionally outside this resolver; parents provide a structured preset.
func ResolveOutputRequirements(input *OutputRequirementsInput) (*pebblestore.SessionArtifactOutputRequirements, error) {
	if input == nil {
		return nil, nil
	}
	presetName := normalizePresetName(input.Preset)
	if presetName == "" && input.Width == 0 && input.Height == 0 && strings.TrimSpace(input.AspectRatio) == "" && strings.TrimSpace(input.Orientation) == "" {
		return nil, errors.New("output_requirements must include a preset or paired width and height")
	}
	if input.Width < 0 || input.Height < 0 {
		return nil, fmt.Errorf("output_requirements dimensions must be between %d and %d", OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
	}
	if (input.Width == 0) != (input.Height == 0) {
		return nil, errors.New("output_requirements width and height must be supplied together")
	}
	if input.Width > OutputRequirementsMaxDimension || input.Height > OutputRequirementsMaxDimension {
		return nil, fmt.Errorf("output_requirements dimensions must be between %d and %d", OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
	}
	preset, hasPreset := lookupOutputPreset(presetName)
	if presetName != "" && !hasPreset {
		return nil, fmt.Errorf("output_requirements preset %q is unknown", strings.TrimSpace(input.Preset))
	}
	width, height, source, presetID := 0, 0, "", ""
	if input.Width > 0 {
		width, height, source = input.Width, input.Height, "dimensions"
		if hasPreset {
			if preset.Width != width || preset.Height != height {
				return nil, fmt.Errorf("output_requirements dimensions %dx%d conflict with preset %q (%dx%d)", width, height, preset.ID, preset.Width, preset.Height)
			}
			presetID = preset.ID
		}
	} else if hasPreset {
		width, height, source, presetID = preset.Width, preset.Height, "preset", preset.ID
	} else {
		if strings.TrimSpace(input.AspectRatio) != "" || strings.TrimSpace(input.Orientation) != "" {
			return nil, errors.New("output_requirements aspect_ratio and orientation require dimensions or preset")
		}
		return nil, nil
	}
	ratio := normalizedRatio(width, height)
	if strings.TrimSpace(input.AspectRatio) != "" {
		supplied, err := normalizeRatio(input.AspectRatio)
		if err != nil {
			return nil, fmt.Errorf("output_requirements aspect_ratio %q is invalid; expected positive integers in W:H form", input.AspectRatio)
		}
		if supplied != ratio {
			return nil, fmt.Errorf("output_requirements aspect_ratio %q conflicts with dimensions %dx%d (%s)", input.AspectRatio, width, height, ratio)
		}
	}
	orientation := normalizedOrientation(width, height)
	if strings.TrimSpace(input.Orientation) != "" {
		supplied := normalizeOrientationName(input.Orientation)
		if _, ok := outputRequirementsAllowedOrientations[supplied]; !ok {
			return nil, fmt.Errorf("output_requirements orientation %q is invalid", input.Orientation)
		}
		if supplied != orientation {
			return nil, fmt.Errorf("output_requirements orientation %q conflicts with dimensions %dx%d (%s)", input.Orientation, width, height, orientation)
		}
	}
	return &pebblestore.SessionArtifactOutputRequirements{
		PresetID: presetID, Width: width, Height: height, AspectRatio: ratio,
		Orientation: orientation, ResolutionSource: source, RegistryVersion: OutputRequirementsRegistryVersion,
	}, nil
}

func ParseOutputRequirements(raw any) (*pebblestore.SessionArtifactOutputRequirements, error) {
	if raw == nil {
		return nil, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("output_requirements must be an object")
	}
	if len(value) == 0 {
		return nil, errors.New("output_requirements must include a preset or paired width and height")
	}
	for key := range value {
		switch key {
		case "preset", "width", "height", "aspect_ratio", "orientation":
		default:
			return nil, fmt.Errorf("output_requirements contains unsupported field %q", key)
		}
	}
	for _, key := range []string{"preset", "aspect_ratio", "orientation"} {
		if raw, exists := value[key]; exists {
			text, ok := raw.(string)
			if !ok {
				return nil, fmt.Errorf("output_requirements %s must be a string", key)
			}
			if strings.TrimSpace(text) == "" {
				return nil, fmt.Errorf("output_requirements %s must not be empty when supplied", key)
			}
		}
	}
	input := &OutputRequirementsInput{Preset: stringValue(value["preset"]), AspectRatio: stringValue(value["aspect_ratio"]), Orientation: stringValue(value["orientation"])}
	if raw, exists := value["width"]; exists {
		if raw == nil {
			return nil, errors.New("output_requirements width must be an integer")
		}
		if number, err := integerValue(value, "width"); err != nil {
			return nil, err
		} else if number < 1 {
			return nil, fmt.Errorf("output_requirements dimensions must be between %d and %d", OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
		}
	}
	if raw, exists := value["height"]; exists {
		if raw == nil {
			return nil, errors.New("output_requirements height must be an integer")
		}
		if number, err := integerValue(value, "height"); err != nil {
			return nil, err
		} else if number < 1 {
			return nil, fmt.Errorf("output_requirements dimensions must be between %d and %d", OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
		}
	}
	var err error
	if input.Width, err = integerValue(value, "width"); err != nil {
		return nil, err
	}
	if input.Height, err = integerValue(value, "height"); err != nil {
		return nil, err
	}
	return ResolveOutputRequirements(input)
}

func OutputRequirementsToolSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"description": "Designer-only exact output target. Paired dimensions take precedence over preset; aliases resolve to a versioned canonical server preset.",
		"properties": map[string]any{
			"preset": map[string]any{"type": "string", "description": "Canonical preset or supported alias, for example twitter_header or x_video."},
			"width": map[string]any{"type": "integer", "minimum": 1, "maximum": OutputRequirementsMaxDimension},
			"height": map[string]any{"type": "integer", "minimum": 1, "maximum": OutputRequirementsMaxDimension},
			"aspect_ratio": map[string]any{"type": "string", "description": "Optional ratio assertion normalized from dimensions, for example 16:9."},
			"orientation": map[string]any{"type": "string", "enum": []string{"landscape", "portrait", "square"}},
		},
		"anyOf": []any{
			map[string]any{"required": []string{"preset"}},
			map[string]any{"required": []string{"width", "height"}},
		},
		"additionalProperties": false,
	}
}

func lookupOutputPreset(name string) (OutputPreset, bool) {
	if name == "" {
		return OutputPreset{}, false
	}
	for _, preset := range ListOutputPresets() {
		if preset.ID == name {
			return preset, true
		}
		for _, candidate := range preset.Aliases {
			if candidate == name {
				return preset, true
			}
		}
	}
	return OutputPreset{}, false
}

func validateOutputPresetRegistry(definitions []outputPresetDefinition) error {
	seen := map[string]string{}
	for index, definition := range definitions {
		id := normalizePresetName(definition.id)
		if id == "" || id != definition.id {
			return fmt.Errorf("artifact output preset %d has invalid canonical id %q", index, definition.id)
		}
		if definition.width < 1 || definition.height < 1 || definition.width > OutputRequirementsMaxDimension || definition.height > OutputRequirementsMaxDimension {
			return fmt.Errorf("artifact output preset %q dimensions are invalid", id)
		}
		for _, candidate := range append([]string{id}, definition.aliases...) {
			key := normalizePresetName(candidate)
			if key == "" || key != candidate {
				return fmt.Errorf("artifact output preset %q has invalid alias %q", id, candidate)
			}
			if owner, exists := seen[key]; exists {
				return fmt.Errorf("artifact output preset name %q is shared by %q and %q", key, owner, id)
			}
			seen[key] = id
		}
	}
	return nil
}

func normalizePresetName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func normalizeOrientationName(value string) string { return strings.ToLower(strings.TrimSpace(value)) }
func normalizedOrientation(width, height int) string {
	if width == height {
		return "square"
	}
	if width > height {
		return "landscape"
	}
	return "portrait"
}
func normalizedRatio(width, height int) string {
	divisor := gcd(width, height)
	return strconv.Itoa(width/divisor) + ":" + strconv.Itoa(height/divisor)
}
func normalizeRatio(value string) (string, error) {
	value = strings.TrimSpace(value)
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return "", errors.New("ratio must use W:H form")
	}
	left, e1 := strconv.Atoi(strings.TrimSpace(parts[0]))
	right, e2 := strconv.Atoi(strings.TrimSpace(parts[1]))
	if e1 != nil || e2 != nil || left <= 0 || right <= 0 {
		return "", errors.New("ratio components must be positive integers")
	}
	return normalizedRatio(left, right), nil
}
func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a < 0 {
		return -a
	}
	if a == 0 {
		return 1
	}
	return a
}
func stringValue(value any) string { text, _ := value.(string); return strings.TrimSpace(text) }
func integerValue(value map[string]any, key string) (int, error) {
	raw, exists := value[key]
	if !exists || raw == nil {
		return 0, nil
	}
	var number int64
	switch typed := raw.(type) {
	case int:
		number = int64(typed)
	case int32:
		number = int64(typed)
	case int64:
		number = typed
	case uint:
		if uint64(typed) > uint64(OutputRequirementsMaxDimension) {
			return 0, fmt.Errorf("output_requirements %s must be an integer between %d and %d", key, OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
		}
		number = int64(typed)
	case uint32:
		number = int64(typed)
	case uint64:
		if typed > uint64(OutputRequirementsMaxDimension) {
			return 0, fmt.Errorf("output_requirements %s must be an integer between %d and %d", key, OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
		}
		number = int64(typed)
	case float64:
		if typed < float64(OutputRequirementsMinDimension) || typed > float64(OutputRequirementsMaxDimension) || typed != float64(int64(typed)) {
			return 0, fmt.Errorf("output_requirements %s must be an integer between %d and %d", key, OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
		}
		number = int64(typed)
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, fmt.Errorf("output_requirements %s must be an integer", key)
		}
		number = parsed
	default:
		return 0, fmt.Errorf("output_requirements %s must be an integer", key)
	}
	if number < int64(OutputRequirementsMinDimension) || number > int64(OutputRequirementsMaxDimension) {
		return 0, fmt.Errorf("output_requirements %s must be an integer between %d and %d", key, OutputRequirementsMinDimension, OutputRequirementsMaxDimension)
	}
	return int(number), nil
}

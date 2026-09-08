package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"regexp"
	"strings"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	artifactNarrationMaxScenes      = 8
	artifactNarrationMaxText        = 8000
	artifactNarrationMaxBytes       = 128 * 1024
	artifactNarrationSceneIDPattern = `^[a-z][a-z0-9-]{0,47}$`
	// This is a complete executable example, shared with the model-visible schema.
	artifactNarrationCreateExample = `{"action":"create","filename":"narration-plan.html","media_type":"text/html","narration_plan":{"title":"Launch narration draft","scenes":[{"id":"opening","title":"Opening","narration":"Start with one idea.","visual_direction":"A point of light emerges against a dark field.","music_direction":"Quiet opening; leave space for the voice."},{"id":"resolve","title":"Resolve","narration":"What will you build?"}]}}`
)

var artifactNarrationSceneID = regexp.MustCompile(artifactNarrationSceneIDPattern)

type artifactNarrationPlan struct {
	Title  string                   `json:"title"`
	Scenes []artifactNarrationScene `json:"scenes"`
}

type artifactNarrationScene struct {
	ID              string `json:"id"`
	Title           string `json:"title"`
	Narration       string `json:"narration"`
	VisualDirection string `json:"visual_direction,omitempty"`
	MusicDirection  string `json:"music_direction,omitempty"`
}

func artifactNarrationPlanToolSchema() map[string]any {
	text := func(max int) map[string]any {
		return map[string]any{"type": "string", "minLength": 1, "maxLength": max}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []string{"title", "scenes"},
		"description": "Create only: a built-in narration-plan template, not HTML or Markdown. All fields are plain text. Supply 1–8 scenes with persistent unique lowercase IDs; do not renumber IDs when editing text. Each scene produces separately labeled context and narration Parts, plus separate visual/music Parts when supplied. Omit content, parts, entries and initial_parts: the server owns escaped HTML and selectors. Maximum 128 KiB encoded plan; text fields also bounded in UTF-8 bytes. This authors an editorial draft only, not speech, a storyboard conversion, or a video timeline. Complete reusable manage_artifact call: " + artifactNarrationCreateExample,
		"properties": map[string]any{
			"title": text(160),
			"scenes": map[string]any{
				"type": "array", "minItems": 1, "maxItems": artifactNarrationMaxScenes,
				"items": map[string]any{
					"type": "object", "additionalProperties": false, "required": []string{"id", "title", "narration"},
					"properties": map[string]any{
						"id":    map[string]any{"type": "string", "pattern": artifactNarrationSceneIDPattern, "description": "Persistent scene identity, 1–48 ASCII characters; never a selector or positional index."},
						"title": text(160), "narration": text(artifactNarrationMaxText),
						"visual_direction": text(artifactNarrationMaxText), "music_direction": text(artifactNarrationMaxText),
					},
				},
			},
		},
	}
}

// renderArtifactNarrationPlan is a pure, bounded preflight. It owns both template
// bytes and non-overlapping selector Parts. User strings are never trusted HTML,
// CSS, URLs or selectors, and validation finishes before an author turn exists.
func renderArtifactNarrationPlan(raw any) (string, []pebblestore.SessionArtifactPart, error) {
	object, ok := raw.(map[string]any)
	if !ok || object == nil {
		return "", nil, errors.New("manage_artifact narration_plan must be an object")
	}
	// JSON null must not silently become an omitted optional string; invalid
	// UTF-8 must not be repaired by json.Marshal before validation.
	checkStrings := func(fields map[string]any, skip string) bool {
		for key, rawValue := range fields {
			if key == skip {
				continue
			}
			value, ok := rawValue.(string)
			if !ok || !utf8.ValidString(value) || strings.TrimSpace(value) == "" || len(value) > artifactNarrationMaxText || strings.ContainsRune(value, '\x00') {
				return false
			}
		}
		return true
	}
	scenes, ok := object["scenes"].([]any)
	if !ok || len(scenes) < 1 || len(scenes) > artifactNarrationMaxScenes || !checkStrings(object, "scenes") {
		return "", nil, errors.New("manage_artifact narration_plan requires plain UTF-8 text and 1–8 scene objects")
	}
	for _, rawScene := range scenes {
		scene, ok := rawScene.(map[string]any)
		if !ok || !checkStrings(scene, "") {
			return "", nil, errors.New("manage_artifact narration_plan scenes require nonblank plain UTF-8 text fields")
		}
	}
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > artifactNarrationMaxBytes {
		return "", nil, errors.New("manage_artifact narration_plan exceeds the 128 KiB encoded limit or is not JSON")
	}
	// Decode strictly even for internal callers that did not cross tool schema validation.
	var plan artifactNarrationPlan
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return "", nil, errors.New("manage_artifact narration_plan contains unknown fields or invalid field types")
	}
	validText := func(value string, maximum int) bool {
		return utf8.ValidString(value) && strings.TrimSpace(value) != "" && len(value) <= maximum && !strings.ContainsRune(value, '\x00')
	}
	if !validText(plan.Title, 160) || len(plan.Scenes) < 1 || len(plan.Scenes) > artifactNarrationMaxScenes {
		return "", nil, errors.New("manage_artifact narration_plan requires a title of 1–160 UTF-8 bytes and 1–8 scenes")
	}
	parts := make([]pebblestore.SessionArtifactPart, 0, len(plan.Scenes)*4)
	seen := make(map[string]bool, len(plan.Scenes))
	for index, scene := range plan.Scenes {
		if !artifactNarrationSceneID.MatchString(scene.ID) || seen[scene.ID] {
			return "", nil, fmt.Errorf("manage_artifact narration_plan scene %d requires a unique id matching %s", index+1, artifactNarrationSceneIDPattern)
		}
		seen[scene.ID] = true
		if !validText(scene.Title, 160) || !validText(scene.Narration, artifactNarrationMaxText) ||
			(scene.VisualDirection != "" && !validText(scene.VisualDirection, artifactNarrationMaxText)) ||
			(scene.MusicDirection != "" && !validText(scene.MusicDirection, artifactNarrationMaxText)) {
			return "", nil, fmt.Errorf("manage_artifact narration_plan scene %d requires a title and narration within the UTF-8 byte limits; optional direction must be nonblank when supplied", index+1)
		}
		add := func(kind, label string) {
			id := kind + "-" + scene.ID
			parts = append(parts, pebblestore.SessionArtifactPart{ID: id, Label: label + " — " + scene.Title, Kind: "selector", Selector: "#" + id})
		}
		add("context", "Scene")
		add("narration", "Narration")
		if scene.VisualDirection != "" {
			add("visual", "Visual direction")
		}
		if scene.MusicDirection != "" {
			add("music", "Music direction")
		}
	}
	var output bytes.Buffer
	if err := artifactNarrationTemplate.Execute(&output, plan); err != nil {
		return "", nil, err
	}
	return output.String(), parts, nil
}

// Every Part container stays in the viewport; long plain text has its own reading
// pane rather than hiding Parts behind a carousel. No scripts or external assets.
var artifactNarrationTemplate = template.Must(template.New("narration-plan").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>{{.Title}}</title>
<style>
*{box-sizing:border-box}html,body{margin:0;width:100%;height:100%;overflow:hidden}body{background:#080d15;color:#eef4fa;font:16px/1.5 system-ui,sans-serif;padding:24px;display:flex;flex-direction:column;gap:16px}header.plan-heading{flex:none}h1{font-size:26px;line-height:1.2;margin:0;overflow-wrap:anywhere}p{margin:0}header.plan-heading p{font-size:12px;color:#91a6ba;margin-top:6px}main{flex:1;min-height:0;display:grid;grid-template-columns:repeat(2,minmax(0,1fr));grid-auto-rows:minmax(0,1fr);gap:12px}article{min-height:0;min-width:0;background:#101c2a;border:1px solid #26394c;border-radius:10px;padding:12px;display:flex;flex-direction:column;gap:8px}header.scene-heading{flex:none;max-height:35%;overflow:auto}h2{font-size:15px;line-height:1.3;margin:0;overflow-wrap:anywhere}.fields{flex:1;min-height:0;display:flex;gap:12px}.field{min-width:0;min-height:0;flex:1;display:flex;flex-direction:column}.narration{flex:2}h3{font-size:10px;line-height:1.4;letter-spacing:.08em;text-transform:uppercase;color:#87ceeb;margin:0 0 5px;flex:none}.text{min-height:0;overflow:auto;white-space:pre-wrap;overflow-wrap:anywhere;scrollbar-width:thin;font-size:13px}.direction .text{color:#afbdcc;font-size:12px}main:has(article:only-child){grid-template-columns:1fr}@media(max-width:700px){body{padding:12px;gap:8px}h1{font-size:20px}main{gap:8px}article{padding:8px}.fields{gap:6px}.text{font-size:12px}}
</style></head><body><header class="plan-heading"><h1>{{.Title}}</h1><p>Narration plan · Editorial draft · Each narration and direction is a separate Part</p></header>
<main>{{range .Scenes}}<article><header class="scene-heading" id="context-{{.ID}}"><h2>{{.Title}}</h2></header><div class="fields">
<section class="field narration" id="narration-{{.ID}}"><h3>Narration</h3><p class="text">{{.Narration}}</p></section>
{{if .VisualDirection}}<aside class="field direction" id="visual-{{.ID}}"><h3>Visual direction</h3><p class="text">{{.VisualDirection}}</p></aside>{{end}}
{{if .MusicDirection}}<aside class="field direction" id="music-{{.ID}}"><h3>Music direction</h3><p class="text">{{.MusicDirection}}</p></aside>{{end}}
</div></article>{{end}}</main></body></html>`))

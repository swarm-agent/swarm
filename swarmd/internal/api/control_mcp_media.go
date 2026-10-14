package api

import (
	"net/http"
	"strings"
)

// controlMCPMediaSlots maps the tool's argument names to the UI settings
// keys that hold each medium's default model.
var controlMCPMediaSlots = []struct {
	arg, section, field, needs string
}{
	{"image", "image", "default_model", "image: blank or the Codex image model means ChatGPT sign-in (swarm_connect_chatgpt); any other id is a Google Gemini image model and needs a google key"},
	{"video", "video", "default_model", "video: a google (Veo) or openrouter video model id, or provider:model; needs that provider's key"},
	{"video_iteration", "video", "iteration_model", "video_iteration: the cheaper model video drafts iterate on; same providers as video"},
	{"audio", "audio", "default_model", "audio: a Google music/sound model id; needs a google key"},
}

// controlMCPMediaModels reads the media defaults (what swarm_run_task uses
// for the image, video and sound roles).
func (c *controlMCPCall) controlMCPMediaModels() (map[string]any, error) {
	response, err := c.dispatch(http.MethodGet, "/v1/ui/settings", nil, nil)
	if err != nil {
		return nil, err
	}
	tools := controlMCPMap(response["tools"])
	out := map[string]any{}
	for _, slot := range controlMCPMediaSlots {
		section := controlMCPMap(tools[slot.section])
		value, _ := section[slot.field].(string)
		out[slot.arg] = value
	}
	if media := controlMCPMap(response["media"]); media != nil {
		value, _ := media["transcription_model"].(string)
		out["transcription"] = value
	}
	return out, nil
}

// controlMCPSetMediaModels sets the media defaults. Enabling media on a
// machine is this plus the provider's credential (swarm_list_providers says
// which providers are ready; swarm_request_provider_key or
// swarm_connect_chatgpt add one).
func controlMCPSetMediaModels(c *controlMCPCall, args map[string]any) (any, error) {
	if err := c.requireTokenScope("settings:write"); err != nil {
		return nil, err
	}
	tools := map[string]any{}
	changed := 0
	for _, slot := range controlMCPMediaSlots {
		raw, ok := args[slot.arg]
		if !ok {
			continue
		}
		value, isString := raw.(string)
		if !isString {
			return nil, controlMCPToolFailure("%s must be a model id string (empty clears it)", slot.arg)
		}
		section, _ := tools[slot.section].(map[string]any)
		if section == nil {
			section = map[string]any{}
			tools[slot.section] = section
		}
		section[slot.field] = strings.TrimSpace(value)
		changed++
	}
	body := map[string]any{}
	if changed > 0 {
		body["tools"] = tools
	}
	if raw, ok := args["transcription"]; ok {
		value, isString := raw.(string)
		if !isString {
			return nil, controlMCPToolFailure("transcription must be a model id string (empty clears it)")
		}
		body["media"] = map[string]any{"transcription_model": strings.TrimSpace(value)}
		changed++
	}
	if changed == 0 {
		return nil, controlMCPToolFailure("name at least one of image, video, video_iteration, audio, transcription")
	}
	if _, err := c.dispatch(http.MethodPost, "/v1/ui/settings", nil, body); err != nil {
		return nil, err
	}
	media, err := c.controlMCPMediaModels()
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"media": media,
		"next":  "Media runs through swarm_run_task with role image, video or sound once the model's provider is ready (swarm_list_providers; add a key with swarm_request_provider_key or sign in with swarm_connect_chatgpt).",
	}, nil
}

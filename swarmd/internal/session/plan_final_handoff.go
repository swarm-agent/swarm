package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"mime"
	"net/url"
	pathpkg "path"
	"strings"
	"unicode/utf8"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	PlanFinalHandoffSchemaVersion = 1

	PlanFinalHandoffMaxTitleRunes           = 120
	PlanFinalHandoffMaxOverviewRunes        = 600
	PlanFinalHandoffMaxImpactBullets        = 3
	PlanFinalHandoffMaxImpactBulletRunes    = 240
	PlanFinalHandoffMaxSuggestedPrompts     = 3
	PlanFinalHandoffMaxPromptLabelRunes     = 80
	PlanFinalHandoffMaxSuggestedPromptRunes = 800
)

var planFinalHandoffViewableMediaTypes = map[string]string{
	"text/html":       "html",
	"image/png":       "image",
	"image/jpeg":      "image",
	"image/gif":       "image",
	"image/webp":      "image",
	"image/svg+xml":   "image",
	"text/markdown":   "markdown",
	"text/plain":      "text",
	"application/pdf": "pdf",
}

var planFinalHandoffViewableExtensions = map[string]struct {
	mediaType string
	kind      string
}{
	".html":     {mediaType: "text/html", kind: "html"},
	".htm":      {mediaType: "text/html", kind: "html"},
	".png":      {mediaType: "image/png", kind: "image"},
	".jpg":      {mediaType: "image/jpeg", kind: "image"},
	".jpeg":     {mediaType: "image/jpeg", kind: "image"},
	".gif":      {mediaType: "image/gif", kind: "image"},
	".webp":     {mediaType: "image/webp", kind: "image"},
	".svg":      {mediaType: "image/svg+xml", kind: "image"},
	".md":       {mediaType: "text/markdown", kind: "markdown"},
	".markdown": {mediaType: "text/markdown", kind: "markdown"},
	".txt":      {mediaType: "text/plain", kind: "text"},
	".pdf":      {mediaType: "application/pdf", kind: "pdf"},
}

// NormalizePlanCheckpointHandoff validates and normalizes the concise source
// fields authored with a terminal checkpoint outcome. Limits are rejected,
// never silently truncated, so every client receives the same contract.
func NormalizePlanCheckpointHandoff(value pebblestore.SessionPlanCheckpointHandoff) (pebblestore.SessionPlanCheckpointHandoff, error) {
	value.Title = strings.TrimSpace(value.Title)
	value.Overview = strings.TrimSpace(value.Overview)
	value.PullRequestURL = strings.TrimSpace(value.PullRequestURL)
	if value.Overview == "" {
		return pebblestore.SessionPlanCheckpointHandoff{}, errors.New("final handoff overview is required")
	}
	if err := validateFinalHandoffText("title", value.Title, PlanFinalHandoffMaxTitleRunes, false); err != nil {
		return pebblestore.SessionPlanCheckpointHandoff{}, err
	}
	if err := validateFinalHandoffText("overview", value.Overview, PlanFinalHandoffMaxOverviewRunes, true); err != nil {
		return pebblestore.SessionPlanCheckpointHandoff{}, err
	}
	if len(value.ImpactBullets) > PlanFinalHandoffMaxImpactBullets {
		return pebblestore.SessionPlanCheckpointHandoff{}, fmt.Errorf("final handoff impact_bullets supports at most %d items", PlanFinalHandoffMaxImpactBullets)
	}
	value.ImpactBullets = append([]string(nil), value.ImpactBullets...)
	for i := range value.ImpactBullets {
		value.ImpactBullets[i] = strings.TrimSpace(value.ImpactBullets[i])
		if err := validateFinalHandoffText(fmt.Sprintf("impact_bullets[%d]", i), value.ImpactBullets[i], PlanFinalHandoffMaxImpactBulletRunes, true); err != nil {
			return pebblestore.SessionPlanCheckpointHandoff{}, err
		}
	}
	if len(value.SuggestedPrompts) > PlanFinalHandoffMaxSuggestedPrompts {
		return pebblestore.SessionPlanCheckpointHandoff{}, fmt.Errorf("final handoff suggested_prompts supports at most %d items", PlanFinalHandoffMaxSuggestedPrompts)
	}
	value.SuggestedPrompts = append([]pebblestore.PlanFinalHandoffSuggestedPrompt(nil), value.SuggestedPrompts...)
	for i := range value.SuggestedPrompts {
		prompt := &value.SuggestedPrompts[i]
		prompt.Label = strings.TrimSpace(prompt.Label)
		prompt.Prompt = strings.TrimSpace(prompt.Prompt)
		if err := validateFinalHandoffText(fmt.Sprintf("suggested_prompts[%d].label", i), prompt.Label, PlanFinalHandoffMaxPromptLabelRunes, true); err != nil {
			return pebblestore.SessionPlanCheckpointHandoff{}, err
		}
		if err := validateFinalHandoffText(fmt.Sprintf("suggested_prompts[%d].prompt", i), prompt.Prompt, PlanFinalHandoffMaxSuggestedPromptRunes, true); err != nil {
			return pebblestore.SessionPlanCheckpointHandoff{}, err
		}
	}
	if value.PullRequestURL != "" {
		normalized, err := normalizeGitHubPullRequestURL(value.PullRequestURL)
		if err != nil {
			return pebblestore.SessionPlanCheckpointHandoff{}, err
		}
		value.PullRequestURL = normalized
	}
	return value, nil
}

func validateFinalHandoffText(field, value string, maxRunes int, required bool) error {
	if value == "" {
		if required {
			return fmt.Errorf("final handoff %s is required", field)
		}
		return nil
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("final handoff %s must be valid UTF-8", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("final handoff %s exceeds %d characters", field, maxRunes)
	}
	if looksLikeExecutableFinalHandoffDirective(value) {
		return fmt.Errorf("final handoff %s must be display text or an ordinary chat prompt, not an executable directive", field)
	}
	return nil
}

func normalizeGitHubPullRequestURL(value string) (string, error) {
	parsed, err := url.ParseRequestURI(value)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("final handoff pull_request_url must be an https://github.com/<owner>/<repository>/pull/<number> URL")
	}
	segments := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(segments) != 4 || segments[0] == "" || segments[1] == "" || segments[2] != "pull" || segments[3] == "" {
		return "", errors.New("final handoff pull_request_url must be an https://github.com/<owner>/<repository>/pull/<number> URL")
	}
	for _, r := range segments[3] {
		if r < '0' || r > '9' {
			return "", errors.New("final handoff pull_request_url must be an https://github.com/<owner>/<repository>/pull/<number> URL")
		}
	}
	parsed.Host = "github.com"
	parsed.Path = strings.TrimSuffix(parsed.Path, "/")
	return parsed.String(), nil
}

// ProjectPlanFinalHandoffArtifacts filters durable artifact references down to
// concrete, client-viewable deliverables and assigns stable opaque IDs. The ID
// binds the plan/checkpoint identity and canonical durable artifact metadata.
func ProjectPlanFinalHandoffArtifacts(planID, checkpointID string, artifacts []pebblestore.SessionPlanArtifactReference) []pebblestore.PlanFinalHandoffArtifact {
	result := make([]pebblestore.PlanFinalHandoffArtifact, 0, len(artifacts))
	seen := make(map[string]struct{}, len(artifacts))
	for _, artifact := range artifacts {
		if strings.ToLower(strings.TrimSpace(artifact.Role)) != "deliverable" {
			continue
		}
		mediaType, kind, ok := PlanFinalHandoffArtifactPresentation(artifact)
		if !ok {
			continue
		}
		path := strings.TrimSpace(artifact.Path)
		filename := pathpkg.Base(path)
		description := strings.TrimSpace(artifact.Description)
		label := description
		if label == "" {
			label = filename
		}
		canonical := strings.Join([]string{
			"swarm-final-handoff-artifact-v1",
			strings.TrimSpace(planID),
			strings.TrimSpace(checkpointID),
			path,
			"deliverable",
			description,
			mediaType,
		}, "\x00")
		digest := sha256.Sum256([]byte(canonical))
		id := "art_" + base64.RawURLEncoding.EncodeToString(digest[:18])
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, pebblestore.PlanFinalHandoffArtifact{
			ID:          id,
			Label:       label,
			Description: description,
			Filename:    filename,
			MediaType:   mediaType,
			Kind:        kind,
		})
	}
	return result
}

// PlanFinalHandoffArtifactPresentation returns the canonical safe media type
// and presentation kind for an eligible deliverable reference.
func PlanFinalHandoffArtifactPresentation(artifact pebblestore.SessionPlanArtifactReference) (string, string, bool) {
	if strings.ToLower(strings.TrimSpace(artifact.Role)) != "deliverable" {
		return "", "", false
	}
	extension := strings.ToLower(pathpkg.Ext(strings.TrimSpace(artifact.Path)))
	presentation, extensionOK := planFinalHandoffViewableExtensions[extension]
	declared := strings.TrimSpace(artifact.MediaType)
	if declared != "" {
		parsed, _, err := mime.ParseMediaType(declared)
		if err != nil {
			return "", "", false
		}
		parsed = strings.ToLower(strings.TrimSpace(parsed))
		kind, mediaOK := planFinalHandoffViewableMediaTypes[parsed]
		if !mediaOK || !extensionOK || presentation.mediaType != parsed {
			return "", "", false
		}
		return parsed, kind, true
	}
	if !extensionOK {
		return "", "", false
	}
	return presentation.mediaType, presentation.kind, true
}

func looksLikeExecutableFinalHandoffDirective(value string) bool {
	trimmed := strings.TrimSpace(value)
	lower := strings.ToLower(trimmed)
	for _, prefix := range []string{"/", "!", "$ ", "```", "~~~", "<swarm-"} {
		if strings.HasPrefix(lower, prefix) {
			return true
		}
	}
	if strings.HasPrefix(trimmed, "{") {
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &object) == nil {
			for _, key := range []string{"action", "tool", "tool_name", "command", "operation"} {
				if _, ok := object[key]; ok {
					return true
				}
			}
		}
	}
	return false
}

// BuildPlanFinalHandoff joins concise source fields with canonical terminal
// evidence. The evidence is copied rather than duplicated on the checkpoint.
func BuildPlanFinalHandoff(checkpoint pebblestore.SessionPlanCheckpoint) (*pebblestore.PlanFinalHandoff, error) {
	if checkpoint.Handoff == nil {
		return nil, nil
	}
	source, err := NormalizePlanCheckpointHandoff(*checkpoint.Handoff)
	if err != nil {
		return nil, err
	}
	title := source.Title
	if title == "" {
		title = strings.TrimSpace(checkpoint.Title)
	}
	if title == "" {
		title = "Plan complete"
	}
	result := &pebblestore.PlanFinalHandoff{
		SchemaVersion:    PlanFinalHandoffSchemaVersion,
		Title:            title,
		Overview:         source.Overview,
		ImpactBullets:    append([]string(nil), source.ImpactBullets...),
		SuggestedPrompts: append([]pebblestore.PlanFinalHandoffSuggestedPrompt(nil), source.SuggestedPrompts...),
		PullRequestURL:   source.PullRequestURL,
		Details: pebblestore.PlanFinalHandoffDetails{
			Report:       checkpoint.Report,
			Result:       checkpoint.Result,
			ChangedFiles: append([]string(nil), checkpoint.ChangedFiles...),
			Validation:   append([]string(nil), checkpoint.Validation...),
		},
	}
	if checkpoint.Recommendation != nil {
		recommendation := *checkpoint.Recommendation
		result.Recommendation = &recommendation
	}
	return result, nil
}

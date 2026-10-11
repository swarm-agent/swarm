package pebblestore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// WorkerDeliverableKindSocialPost is the worker requirement kind whose
	// completed runs produce a pending_review social_post deliverable.
	WorkerDeliverableKindSocialPost = "social_post"
	// WorkerSocialPostAction and WorkerSocialPostSecretRef are the only action
	// contract an operator may attach to a social_post requirement.
	WorkerSocialPostAction    = "publish_x_post"
	WorkerSocialPostSecretRef = "env:TWITTER"
	// WorkerSocialPostMaxRunes bounds agent-chosen post text.
	WorkerSocialPostMaxRunes = 280
)

// validateWorkerDeliverableActionContract keeps publication policy operator
// owned: social_post requires the single supported X contract with no target
// URL or parameters, and every other kind must not carry a contract.
func validateWorkerDeliverableActionContract(req WorkerDeliverableRequirement) error {
	if req.Kind != WorkerDeliverableKindSocialPost {
		if req.ActionContract != nil {
			return fmt.Errorf("deliverable requirement %q: action_contract is only allowed for kind %q", strings.TrimSpace(req.Name), WorkerDeliverableKindSocialPost)
		}
		return nil
	}
	c := req.ActionContract
	if c == nil {
		return fmt.Errorf("deliverable requirement %q: social_post requires an action_contract", strings.TrimSpace(req.Name))
	}
	if c.Action != WorkerSocialPostAction {
		return fmt.Errorf("deliverable requirement %q: social_post action must be %q", strings.TrimSpace(req.Name), WorkerSocialPostAction)
	}
	if c.TargetSecretRef != WorkerSocialPostSecretRef {
		return fmt.Errorf("deliverable requirement %q: social_post target_secret_ref must be %q", strings.TrimSpace(req.Name), WorkerSocialPostSecretRef)
	}
	if c.TargetURL != "" {
		return fmt.Errorf("deliverable requirement %q: social_post must not set target_url", strings.TrimSpace(req.Name))
	}
	if len(c.Parameters) != 0 {
		return fmt.Errorf("deliverable requirement %q: social_post must not set parameters", strings.TrimSpace(req.Name))
	}
	return nil
}

func cloneDeliverableActionContract(c *DeliverableActionContract) *DeliverableActionContract {
	if c == nil {
		return nil
	}
	out := *c
	out.Parameters = nil
	if c.Parameters != nil {
		// Parameters are JSON values; a round trip yields an unaliased copy.
		if encoded, err := json.Marshal(c.Parameters); err == nil {
			_ = json.Unmarshal(encoded, &out.Parameters)
		}
	}
	return &out
}

func cloneWorkerDeliverableRequirements(in []WorkerDeliverableRequirement) []WorkerDeliverableRequirement {
	if in == nil {
		return nil
	}
	out := make([]WorkerDeliverableRequirement, len(in))
	for i, req := range in {
		out[i] = req
		out[i].ActionContract = cloneDeliverableActionContract(req.ActionContract)
	}
	return out
}

// ParseWorkerSocialPostResult strictly decodes an agent checkpoint result into
// post text. The result must be exactly one JSON object with the single key
// tweet_text (a trivial ```json fence is tolerated). Any other key, including
// action or target fields, is rejected: the agent chooses only the text.
func ParseWorkerSocialPostResult(raw string) (string, error) {
	body := strings.TrimSpace(raw)
	if strings.HasPrefix(body, "```") {
		if !strings.HasSuffix(body, "```") || len(body) < 6 {
			return "", errors.New("result must be a JSON object")
		}
		body = strings.TrimSuffix(strings.TrimPrefix(body, "```"), "```")
		body = strings.TrimPrefix(body, "json")
		if strings.Contains(body, "```") {
			return "", errors.New("result must be a single JSON object")
		}
		body = strings.TrimSpace(body)
	}
	if !strings.HasPrefix(body, "{") {
		return "", errors.New("result must be a JSON object")
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(body)))
	var fields map[string]json.RawMessage
	if err := dec.Decode(&fields); err != nil {
		return "", fmt.Errorf("result is not a JSON object: %w", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return "", errors.New("result must contain exactly one JSON object")
	}
	for key := range fields {
		if key != "tweet_text" {
			return "", fmt.Errorf("result has unsupported field %q; only tweet_text is allowed", key)
		}
	}
	value, ok := fields["tweet_text"]
	if !ok {
		return "", errors.New("result requires tweet_text")
	}
	var text *string
	if err := json.Unmarshal(value, &text); err != nil || text == nil {
		return "", errors.New("tweet_text must be a string")
	}
	return validateWorkerSocialPostText(*text)
}

func validateWorkerSocialPostText(text string) (string, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("tweet_text is empty")
	}
	if !utf8.ValidString(text) {
		return "", errors.New("tweet_text is not valid UTF-8")
	}
	if n := utf8.RuneCountInString(text); n > WorkerSocialPostMaxRunes {
		return "", fmt.Errorf("tweet_text has %d characters; maximum is %d", n, WorkerSocialPostMaxRunes)
	}
	for _, r := range text {
		if r != '\n' && unicode.IsControl(r) {
			return "", errors.New("tweet_text contains control characters")
		}
	}
	return text, nil
}

// WorkerSocialPostDeliverableID is deterministic per run and requirement so a
// retried completion observation finds the record it already created.
func WorkerSocialPostDeliverableID(runID, requirementName string) (string, error) {
	runID = strings.TrimSpace(runID)
	name := strings.TrimSpace(requirementName)
	if runID == "" || name == "" {
		return "", errors.New("run id and requirement name are required")
	}
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	slug := b.String()
	if len(slug) > 64 {
		slug = slug[:64]
	}
	if slug != name {
		// Sanitization is lossy; disambiguate distinct names that map together.
		sum := sha256.Sum256([]byte(name))
		slug += "_" + hex.EncodeToString(sum[:4])
	}
	return "deliv_" + runID + "_" + slug, nil
}

// BuildWorkerSocialPostDeliverable converts one completed worker checkpoint into
// a pending_review social_post deliverable. Only the post text and checkpoint
// artifact references come from the agent; the action contract is a copy of the
// operator's requirement and is never read from the agent result.
func BuildWorkerSocialPostDeliverable(w WorkerRecord, auto WorkerAutomationDefinition, run WorkerRunRecord, req WorkerDeliverableRequirement, cp SessionPlanCheckpoint) (DeliverableRecord, error) {
	if req.Kind != WorkerDeliverableKindSocialPost {
		return DeliverableRecord{}, fmt.Errorf("requirement %q is not a social_post", req.Name)
	}
	if err := validateWorkerDeliverableActionContract(req); err != nil {
		return DeliverableRecord{}, err
	}
	if strings.TrimSpace(w.AccountScopeID) == "" || w.AccountScopeID != run.AccountScopeID || w.ID == "" || w.ID != run.WorkerID {
		return DeliverableRecord{}, errors.New("worker run does not belong to worker")
	}
	if auto.ID == "" || auto.ID != run.AutomationID {
		return DeliverableRecord{}, errors.New("worker run does not belong to automation")
	}
	if cp.Status != "completed" {
		return DeliverableRecord{}, errors.New("checkpoint is not completed")
	}
	text, err := ParseWorkerSocialPostResult(cp.Result)
	if err != nil {
		return DeliverableRecord{}, err
	}
	id, err := WorkerSocialPostDeliverableID(run.ID, req.Name)
	if err != nil {
		return DeliverableRecord{}, err
	}
	title := strings.TrimSpace(auto.Name)
	if title == "" {
		title = strings.TrimSpace(w.Name)
	}
	if title == "" {
		title = strings.TrimSpace(req.Name)
	} else {
		title += ": " + strings.TrimSpace(req.Name)
	}
	var media []SessionPlanArtifactReference
	if len(cp.Artifacts) != 0 {
		media = append([]SessionPlanArtifactReference(nil), cp.Artifacts...)
	}
	return DeliverableRecord{
		ID:             id,
		AccountID:      w.AccountScopeID,
		WorkspaceID:    w.LocalBindings["primary"],
		WorkerID:       w.ID,
		OccurrenceID:   run.ID,
		SessionID:      run.SessionID,
		Title:          title,
		Kind:           WorkerDeliverableKindSocialPost,
		Status:         "pending_review",
		Summary:        strings.TrimSpace(req.Description),
		Payload:        map[string]any{"tweet_text": text},
		MediaRefs:      media,
		ActionContract: cloneDeliverableActionContract(req.ActionContract),
	}, nil
}

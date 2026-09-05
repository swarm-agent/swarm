package tool

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"swarm/packages/swarmd/internal/artifactv2"
)

// ArtifactV2AuthorRunContext is injected by trusted Designer orchestration. It
// is deliberately separate from the legacy managed artifact destination.
type ArtifactV2AuthorRunContext struct {
	Grant artifactv2.AuthorGrant
}

type artifactV2AuthorContextKey struct{}

func WithArtifactV2AuthorRunContext(parent context.Context, run ArtifactV2AuthorRunContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, artifactV2AuthorContextKey{}, run)
}

func artifactV2AuthorDefinition() Definition {
	part := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"key":           map[string]any{"type": "string", "maxLength": 128},
			"label":         map[string]any{"type": "string", "maxLength": 256},
			"role":          map[string]any{"type": "string", "maxLength": 128},
			"media_class":   map[string]any{"type": "string", "maxLength": 128},
			"locator_kind":  map[string]any{"type": "string", "enum": []string{"", "temporal", "spatial", "page", "state", "selector", "semantic"}},
			"locator_value": map[string]any{"type": "string", "maxLength": 512},
			"order":         map[string]any{"type": "integer", "minimum": 0, "maximum": 1024},
		},
		"required": []string{"key", "label", "media_class", "order"}, "additionalProperties": false,
	}
	return Definition{
		Type: "function", Name: "artifact_v2_author",
		Description: "Context-bound Artifact V2 Designer authoring. The server injects owner, destination, capability, output policy, animation profile, limits, and candidate slot. Actions persist exact part bytes before server build/validation; invalid compositions remain durable and repairable. Animated artifacts accept typed motion scene/behavior JSON parts; the server owns HTML, manifests, binder, scheduler, player lifecycle, viewport, Chrome validation, fallback, and MP4 derivatives. This tool cannot publish, select, redirect, invoke legacy artifact writes, or override server policy.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":                             map[string]any{"type": "string", "enum": []string{"inspect_context", "declare_parts", "write_part", "request_build", "submit_candidate"}},
				"parts":                              map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": part},
				"part_id":                            map[string]any{"type": "string", "maxLength": 128},
				"content":                            map[string]any{"type": "string"},
				"content_base64":                     map[string]any{"type": "string"},
				"media_type":                         map[string]any{"type": "string", "maxLength": 255},
				"expected_base_revision_id":          map[string]any{"type": "string", "maxLength": 128, "description": "Exact current part revision; omit only for the first write."},
				"expected_composition_head_revision": map[string]any{"type": "integer", "minimum": 0, "description": "Exact current composition head revision; use 0 before the first composition."},
			},
			"required": []string{"action"}, "additionalProperties": false,
		},
	}
}

func (r *Runtime) SetArtifactV2AuthorService(service *artifactv2.AuthorService) {
	if r != nil {
		r.artifactV2Author = service
	}
}

func (r *Runtime) ArtifactV2AuthorService() *artifactv2.AuthorService {
	if r == nil {
		return nil
	}
	return r.artifactV2Author
}

func (r *Runtime) executeArtifactV2Author(ctx context.Context, scope WorkspaceScope, callID string, args map[string]any) (string, error) {
	if r == nil || r.artifactV2Author == nil {
		return "", errors.New("artifact_v2_author service is not configured")
	}
	run, ok := ctx.Value(artifactV2AuthorContextKey{}).(ArtifactV2AuthorRunContext)
	if !ok || strings.TrimSpace(run.Grant.ID) == "" {
		return "", errors.New("artifact_v2_author requires trusted context-bound capability")
	}
	if strings.TrimSpace(scope.SessionID) != strings.TrimSpace(run.Grant.ProducerSessionID) || strings.TrimSpace(scope.Principal.AccountScopeID) == "" || strings.TrimSpace(scope.Principal.UserID) == "" {
		return "", errors.New("artifact_v2_author producer or authenticated principal does not match the capability")
	}
	principal := artifactv2.Principal{AccountScopeID: scope.Principal.AccountScopeID, UserID: scope.Principal.UserID, SessionID: run.Grant.OwnerSessionID, RunID: run.Grant.ProducerRunID, ActorClass: "designer"}
	action := strings.TrimSpace(mapString(args, "action"))
	requestID := strings.TrimSpace(callID)
	if requestID == "" {
		requestID = "artifact-v2-author"
	}
	var result any
	var err error
	switch action {
	case "inspect_context":
		if err = requireOnlyAuthorFields(args, "action"); err == nil {
			result, err = r.artifactV2Author.Inspect(principal, run.Grant)
		}
	case "declare_parts":
		if err = requireOnlyAuthorFields(args, "action", "parts"); err != nil {
			break
		}
		var declarations []artifactv2.AuthorPartDeclaration
		declarations, err = parseArtifactV2Declarations(args["parts"])
		if err == nil {
			result, err = r.artifactV2Author.DeclareParts(ctx, principal, run.Grant, requestID, declarations)
		}
	case "write_part":
		if err = requireOnlyAuthorFields(args, "action", "part_id", "content", "content_base64", "media_type", "expected_base_revision_id", "expected_composition_head_revision"); err != nil {
			break
		}
		var body []byte
		body, err = parseArtifactV2AuthorBody(args)
		if err == nil {
			result, err = r.artifactV2Author.WritePart(ctx, principal, run.Grant, requestID, artifactv2.AuthorPartWrite{PartID: mapString(args, "part_id"), ExpectedBaseRevisionID: mapString(args, "expected_base_revision_id"), ExpectedCompositionHeadRevision: nonnegativeUint64(args["expected_composition_head_revision"]), MediaType: mapString(args, "media_type"), Body: body})
		}
	case "request_build":
		if err = requireOnlyAuthorFields(args, "action"); err == nil {
			result, err = r.artifactV2Author.RequestBuild(ctx, principal, run.Grant, requestID)
		}
	case "submit_candidate":
		if err = requireOnlyAuthorFields(args, "action"); err == nil {
			result, err = r.artifactV2Author.SubmitCandidate(ctx, principal, run.Grant, requestID)
		}
	default:
		err = fmt.Errorf("artifact_v2_author action %q is unsupported", action)
	}
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]any{"tool": "artifact_v2_author", "action": action, "status": "ok", "candidate": result})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func requireOnlyAuthorFields(args map[string]any, allowed ...string) error {
	set := map[string]bool{}
	for _, key := range allowed {
		set[key] = true
	}
	for key := range args {
		if !set[key] {
			return fmt.Errorf("artifact_v2_author rejects caller-authored field %q", key)
		}
	}
	return nil
}

func parseArtifactV2Declarations(raw any) ([]artifactv2.AuthorPartDeclaration, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > 64 {
		return nil, errors.New("artifact_v2_author declare_parts requires 1 to 64 parts")
	}
	out := make([]artifactv2.AuthorPartDeclaration, 0, len(values))
	seen := map[string]bool{}
	for index, value := range values {
		item, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("artifact_v2_author parts[%d] must be an object", index)
		}
		if err := requireOnlyAuthorFields(item, "key", "label", "role", "media_class", "locator_kind", "locator_value", "order"); err != nil {
			return nil, err
		}
		key := strings.TrimSpace(mapString(item, "key"))
		if key == "" || seen[key] || strings.TrimSpace(mapString(item, "label")) == "" || strings.TrimSpace(mapString(item, "media_class")) == "" {
			return nil, errors.New("artifact_v2_author part declaration is incomplete or duplicated")
		}
		seen[key] = true
		out = append(out, artifactv2.AuthorPartDeclaration{Key: key, Label: mapString(item, "label"), Role: mapString(item, "role"), MediaClass: mapString(item, "media_class"), LocatorKind: mapString(item, "locator_kind"), LocatorValue: mapString(item, "locator_value"), Order: int(nonnegativeUint64(item["order"]))})
	}
	return out, nil
}

func nonnegativeUint64(raw any) uint64 {
	switch value := raw.(type) {
	case float64:
		if value > 0 {
			return uint64(value)
		}
	case int:
		if value > 0 {
			return uint64(value)
		}
	case int64:
		if value > 0 {
			return uint64(value)
		}
	case uint64:
		return value
	}
	return 0
}

func parseArtifactV2AuthorBody(args map[string]any) ([]byte, error) {
	content, hasContent := args["content"]
	encoded, hasEncoded := args["content_base64"]
	if hasContent == hasEncoded {
		return nil, errors.New("artifact_v2_author write_part requires exactly one of content or content_base64")
	}
	if hasContent {
		value, ok := content.(string)
		if !ok || value == "" {
			return nil, errors.New("artifact_v2_author content must be a non-empty string")
		}
		return []byte(value), nil
	}
	value, ok := encoded.(string)
	if !ok || value == "" {
		return nil, errors.New("artifact_v2_author content_base64 must be a non-empty string")
	}
	body, err := base64.StdEncoding.DecodeString(value)
	if err != nil || len(body) == 0 {
		return nil, errors.New("artifact_v2_author content_base64 is invalid or empty")
	}
	return body, nil
}

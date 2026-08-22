package run

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

const mediaInspectToolName = "media_inspect"

// staticMediaInspectToolDefinition makes media inspection a first-class agent
// authorization bit. It is never sent to a provider directly; each request
// replaces it with a schema narrowed to that request's media contract.
func staticMediaInspectToolDefinition() tool.Definition {
	return tool.Definition{
		Type:        "function",
		Name:        mediaInspectToolName,
		Description: "Inspect a supported image from this session or workspace",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"asset_id": map[string]any{"type": "string"},
				"path":     map[string]any{"type": "string"},
			},
			"oneOf": []map[string]any{
				{"required": []string{"asset_id"}},
				{"required": []string{"path"}},
			},
			"additionalProperties": false,
		},
	}
}

func MaterializeSessionMediaTool(definitions []provideriface.ToolDefinition, contract provideriface.SessionMediaContract) []provideriface.ToolDefinition {
	out := make([]provideriface.ToolDefinition, 0, len(definitions)+1)
	for _, definition := range definitions {
		if canonicalToolName(definition.Name) != mediaInspectToolName {
			out = append(out, definition)
		}
	}
	definition, ok := sessionMediaToolDefinition(contract)
	if ok {
		out = append(out, definition)
	}
	return out
}

func sessionMediaToolDefinition(contract provideriface.SessionMediaContract) (provideriface.ToolDefinition, bool) {
	allowed := allowedSessionMediaCapabilities(contract)
	if len(allowed) == 0 || strings.TrimSpace(contract.Hash) == "" {
		return provideriface.ToolDefinition{}, false
	}
	return provideriface.ToolDefinition{
		Type:        "function",
		Name:        mediaInspectToolName,
		Description: "Inspect a supported image by workspace path or immutable session asset ID; the backend resolves and verifies all media metadata",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"asset_id": map[string]any{"type": "string", "description": "Immutable asset ID already attached to this session"},
				"path":     map[string]any{"type": "string", "description": "Workspace-relative or workspace-contained absolute image path"},
			},
			"oneOf": []map[string]any{
				{"required": []string{"asset_id"}},
				{"required": []string{"path"}},
			},
			"additionalProperties": false,
		},
	}, true
}

func sessionMediaContractInstructions(contract provideriface.SessionMediaContract) string {
	allowed := allowedSessionMediaCapabilities(contract)
	if len(allowed) == 0 {
		return ""
	}
	lines := []string{
		"Current run media contract (backend-authoritative):",
		"- Use media_inspect with either a workspace-contained image path or an immutable asset ID already attached to this session.",
		"- Do not ask the user to provide hashes, MIME types, or internal asset metadata; the backend derives and revalidates ownership, contract, type, size, count, and digest.",
		"- Attached images are also delivered to the model natively; use media_inspect when an explicit workspace path or asset re-read is needed.",
	}
	for _, capability := range allowed {
		parts := []string{"- " + capability.Modality, "semantics=" + strings.TrimSpace(capability.Semantics)}
		if len(capability.MIMETypes) > 0 {
			parts = append(parts, "mime_types="+strings.Join(capability.MIMETypes, ","))
		}
		if len(capability.FileTypes) > 0 {
			parts = append(parts, "file_types="+strings.Join(capability.FileTypes, ","))
		}
		if capability.MaxBytes > 0 {
			parts = append(parts, fmt.Sprintf("max_bytes=%d", capability.MaxBytes))
		}
		if capability.MaxCount > 0 {
			parts = append(parts, fmt.Sprintf("max_count=%d", capability.MaxCount))
		}
		lines = append(lines, strings.Join(parts, "; "))
	}
	lines = append(lines, "- All unlisted media kinds, types, processing semantics, generation, transcription, and video options are unsupported for this run.")
	return strings.Join(lines, "\n")
}

func AppendSessionMediaInstructions(instructions string, contract provideriface.SessionMediaContract) string {
	media := sessionMediaContractInstructions(contract)
	if media == "" {
		return strings.TrimSpace(instructions)
	}
	return strings.TrimSpace(strings.TrimSpace(instructions) + "\n\n" + media)
}

func allowedSessionMediaCapabilities(contract provideriface.SessionMediaContract) []provideriface.MediaContractCapability {
	out := make([]provideriface.MediaContractCapability, 0, len(contract.Capabilities))
	for _, capability := range contract.Capabilities {
		if capability.State != provideriface.MediaCapabilityStateAllowed || strings.TrimSpace(capability.Modality) == "" || len(capability.MIMETypes) == 0 || capability.MaxBytes <= 0 || capability.MaxCount <= 0 {
			continue
		}
		capability.Modality = strings.ToLower(strings.TrimSpace(capability.Modality))
		capability.MIMETypes = normalizeMediaStrings(capability.MIMETypes)
		capability.FileTypes = normalizeMediaStrings(capability.FileTypes)
		capability.ContentTypes = normalizeMediaStrings(capability.ContentTypes)
		out = append(out, capability)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Modality < out[j].Modality })
	return out
}

type mediaInspectArguments struct {
	AssetID string `json:"asset_id,omitempty"`
	Path    string `json:"path,omitempty"`
}

func decodeMediaInspectArguments(raw string) (mediaInspectArguments, error) {
	var args mediaInspectArguments
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return mediaInspectArguments{}, fmt.Errorf("decode media_inspect arguments: %w", err)
	}
	args.AssetID = strings.TrimSpace(args.AssetID)
	args.Path = strings.TrimSpace(args.Path)
	if (args.AssetID == "") == (args.Path == "") {
		return mediaInspectArguments{}, errors.New("media_inspect requires exactly one of asset_id or path")
	}
	return args, nil
}

func validateMediaInspectInvocation(contract provideriface.SessionMediaContract, modality, mimeType, fileType string) (provideriface.MediaContractCapability, error) {
	if strings.TrimSpace(contract.Hash) == "" {
		return provideriface.MediaContractCapability{}, errors.New("media_inspect current run contract is unavailable")
	}
	capability, allowed := sessionMediaCapability(contract, modality, mimeType, fileType)
	if !allowed {
		return provideriface.MediaContractCapability{}, errors.New("media_inspect type is denied by the current run contract")
	}
	return capability, nil
}

func sessionMediaCapability(contract provideriface.SessionMediaContract, modality, mimeType, fileType string) (provideriface.MediaContractCapability, bool) {
	if !SessionMediaContractAllows(contract, modality, mimeType, fileType) {
		return provideriface.MediaContractCapability{}, false
	}
	for _, capability := range allowedSessionMediaCapabilities(contract) {
		if capability.Modality == strings.ToLower(strings.TrimSpace(modality)) {
			return capability, true
		}
	}
	return provideriface.MediaContractCapability{}, false
}

func providerToolMediaInputItems(results []tool.Result) []map[string]any {
	out := make([]map[string]any, 0)
	counts := map[string]int{}
	for _, result := range results {
		if result.Media == nil {
			continue
		}
		modality := strings.ToLower(strings.TrimSpace(result.Media.Modality))
		counts[modality]++
		if counts[modality] > pebblestore.SessionMediaDefaultMaxCount {
			continue
		}
		out = append(out, map[string]any{
			"role": "user",
			"content": []map[string]any{
				{"type": "session_media", "media": provideriface.SessionMediaPayload{
					AssetID: result.Media.AssetID, Modality: result.Media.Modality, MIMEType: result.Media.MIMEType,
					FileType: result.Media.FileType, DigestSHA256: result.Media.DigestSHA256, Size: result.Media.Size,
					Bytes: append([]byte(nil), result.Media.Bytes...),
				}},
			},
		})
	}
	return out
}

func mediaInspectResult(asset pebblestore.SessionMediaAsset, capability provideriface.MediaContractCapability, contract provideriface.SessionMediaContract) (string, error) {
	payload, err := json.Marshal(map[string]any{
		"status":        "ok",
		"asset_id":      asset.ID,
		"modality":      asset.Modality,
		"mime_type":     asset.DetectedMIMEType,
		"file_type":     asset.FileType,
		"digest_sha256": asset.DigestSHA256,
		"size":          asset.Size,
		"semantics":     capability.Semantics,
		"contract_hash": contract.Hash,
		"immutable":     true,
	})
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

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
		Description: "Inspect an immutable session media asset admitted by the current run contract",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"asset_id": map[string]any{"type": "string"},
			},
			"required":             []string{"asset_id"},
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
	modalities := make([]string, 0, len(allowed))
	mimeTypes := make([]string, 0)
	fileTypes := make([]string, 0)
	for _, capability := range allowed {
		modalities = append(modalities, capability.Modality)
		mimeTypes = append(mimeTypes, capability.MIMETypes...)
		fileTypes = append(fileTypes, capability.FileTypes...)
	}
	modalities = normalizeMediaStrings(modalities)
	mimeTypes = normalizeMediaStrings(mimeTypes)
	fileTypes = normalizeMediaStrings(fileTypes)
	properties := map[string]any{
		"asset_id":      map[string]any{"type": "string", "description": "Immutable session media asset ID"},
		"contract_hash": map[string]any{"type": "string", "enum": []string{contract.Hash}, "description": "Exact current run media-contract hash"},
		"digest_sha256": map[string]any{"type": "string", "description": "Expected immutable asset SHA-256 digest"},
		"modality":      map[string]any{"type": "string", "enum": modalities},
		"mime_type":     map[string]any{"type": "string", "enum": mimeTypes},
	}
	if len(fileTypes) > 0 {
		properties["file_type"] = map[string]any{"type": "string", "enum": fileTypes}
	}
	return provideriface.ToolDefinition{
		Type:        "function",
		Name:        mediaInspectToolName,
		Description: "Inspect one immutable session-scoped media asset after revalidating ownership, contract, type, limits, and digest",
		Parameters: map[string]any{
			"type":                 "object",
			"properties":           properties,
			"required":             []string{"asset_id", "contract_hash", "digest_sha256", "modality", "mime_type"},
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
		"- Use media_inspect only with immutable asset references already attached to this session; never pass local paths, URLs, or inline bytes.",
		"- Every call must repeat the current contract hash and the asset digest. The backend revalidates ownership, current capability, limits, and digest.",
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
	AssetID      string `json:"asset_id"`
	ContractHash string `json:"contract_hash"`
	DigestSHA256 string `json:"digest_sha256"`
	Modality     string `json:"modality"`
	MIMEType     string `json:"mime_type"`
	FileType     string `json:"file_type,omitempty"`
}

func decodeMediaInspectArguments(raw string) (mediaInspectArguments, error) {
	var args mediaInspectArguments
	decoder := json.NewDecoder(strings.NewReader(strings.TrimSpace(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return mediaInspectArguments{}, fmt.Errorf("decode media_inspect arguments: %w", err)
	}
	args.AssetID = strings.TrimSpace(args.AssetID)
	args.ContractHash = strings.TrimSpace(args.ContractHash)
	args.DigestSHA256 = strings.ToLower(strings.TrimSpace(args.DigestSHA256))
	args.Modality = strings.ToLower(strings.TrimSpace(args.Modality))
	args.MIMEType = strings.ToLower(strings.TrimSpace(args.MIMEType))
	args.FileType = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(args.FileType), "."))
	if args.AssetID == "" || args.ContractHash == "" || args.DigestSHA256 == "" || args.Modality == "" || args.MIMEType == "" {
		return mediaInspectArguments{}, errors.New("media_inspect requires asset_id, contract_hash, digest_sha256, modality, and mime_type")
	}
	return args, nil
}

func validateMediaInspectInvocation(contract provideriface.SessionMediaContract, args mediaInspectArguments) (provideriface.MediaContractCapability, error) {
	if strings.TrimSpace(contract.Hash) == "" || args.ContractHash != contract.Hash {
		return provideriface.MediaContractCapability{}, errors.New("media_inspect call is stale or forged for the current run contract")
	}
	capability, allowed := sessionMediaCapability(contract, args.Modality, args.MIMEType, args.FileType)
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

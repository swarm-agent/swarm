package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"image/png"
	"math"
	"net/http"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	manageArtifactDefaultListLimit  = 50
	manageArtifactMaxListLimit      = 100
	manageArtifactMaxCreateBytes    = 1 << 20
	manageArtifactMaxPackageFiles   = 128
	manageArtifactMaxPackageBytes   = 8 << 20
	manageArtifactDefaultReadBytes  = 32 << 10
	manageArtifactMaxReadBytes      = 256 << 10
	manageArtifactMaxImageReadBytes = 16 << 20
	manageArtifactMaxPromptRunes    = 12000
)

// ArtifactAuthority is the session-owned managed artifact lifecycle boundary.
// Runtime callers inject the canonical authority; the tool never resolves
// storage paths or mutates artifact metadata directly.
type ArtifactAuthority interface {
	Create(context.Context, artifact.Principal, artifact.CreateInput) (pebblestore.SessionArtifactVariant, error)
	CreatePackage(context.Context, artifact.Principal, artifact.CreatePackageInput) (pebblestore.SessionArtifactVariant, error)
	List(artifact.Principal, string, int) ([]pebblestore.SessionArtifactCollection, error)
	ListVariants(artifact.Principal, string, int) ([]pebblestore.SessionArtifactVariant, error)
	Get(artifact.Principal, string) (pebblestore.SessionArtifactVariant, error)
	GetReference(artifact.Principal, pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error)
	Read(context.Context, artifact.Principal, string, int64) ([]byte, pebblestore.SessionArtifactVariant, error)
	ReadReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, int64) ([]byte, pebblestore.SessionArtifactVariant, error)
	ReadPackageReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error)
	MaterializeReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, string, bool) (artifact.Materialized, error)
	Select(artifact.Principal, string, string, string) (pebblestore.SessionArtifactSelectionReference, error)
	DeleteVariant(artifact.Principal, string, string, string) error
	DeleteCollection(artifact.Principal, string, string) error
}

// ArtifactRunContext is trusted lineage supplied by run orchestration. Session
// ownership still comes from WorkspaceScope's authenticated principal. Managed
// destinations use the parent SessionID and the producing child ChildSessionID.
type ArtifactRunContext struct {
	SessionID    string
	RunID        string
	PlanID       string
	CheckpointID string
	AttemptID    string

	// Managed task destinations are injected only by trusted orchestration. When
	// present, create calls are pinned to this parent-owned collection/variant;
	// model-authored target arguments may not redirect the output.
	TaskCallID         string
	ProgramID          string
	ProgramJobID       string
	ChildSessionID     string
	IterationGroupID   string
	IterationGroup     string
	IterationID        string
	IterationIndex     int
	IterationLabel     string
	IterationTheme     string
	CollectionID       string
	VariantID          string
	OutputRequirements *pebblestore.SessionArtifactOutputRequirements
}

type artifactRunContextKey struct{}

func WithArtifactRunContext(parent context.Context, run ArtifactRunContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, artifactRunContextKey{}, run)
}

func manageArtifactDefinition() Definition {
	presentation := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":        map[string]any{"type": "string", "description": "Display kind: download|text|code|image|html|package"},
			"label":       map[string]any{"type": "string", "maxLength": 256},
			"description": map[string]any{"type": "string", "maxLength": 2048},
			"previewable": map[string]any{"type": "boolean"},
			"width":       map[string]any{"type": "integer", "minimum": 0, "maximum": 100000},
			"height":      map[string]any{"type": "integer", "minimum": 0, "maximum": 100000},
		},
		"additionalProperties": false,
	}
	entry := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":    map[string]any{"type": "string", "description": "Relative slash-delimited package entry name"},
			"content": map[string]any{"type": "string", "description": "UTF-8 entry content"},
		},
		"required":             []string{"name", "content"},
		"additionalProperties": false,
	}
	return Definition{
		Type:        "function",
		Name:        "manage_artifact",
		Description: "Generate one provider-billed image with the authenticated account's configured image model and publish it directly as a ready V3 managed artifact; create and manage other durable artifacts; inspect exact ready references as bounded text/package data or bounded image base64; and explicitly materialize an exact reference into the trusted workspace. Provider/model identifiers and private storage paths are never accepted or exposed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"generate_image", "create", "create_package", "list_presets", "list", "get", "read", "materialize", "promote", "select", "delete"}},
				"prompt": map[string]any{"type": "string", "maxLength": manageArtifactMaxPromptRunes, "description": "Image prompt required only for generate_image"},
				"image_settings": map[string]any{"type": "object", "properties": map[string]any{
					"size":         map[string]any{"type": "string", "maxLength": 64, "description": "Optional portable output size, for example auto, 1024x1024, 1536x1024, or 1024x1536. The backend adapts it to the configured image provider."},
					"aspect_ratio": map[string]any{"type": "string", "maxLength": 32, "description": "Optional aspect ratio: 1:1, 2:3, 3:2, 3:4, 4:3, 9:16, 16:9, or 21:9."},
					"image_size":   map[string]any{"type": "string", "maxLength": 32, "description": "Optional portable resolution tier: 512, 1K, 2K, or 4K. Equivalent square pixel aliases such as 1024x1024, 2048x2048, and 4096x4096 are accepted. The backend translates this for the configured provider."},
				}, "additionalProperties": false, "description": "Optional provider-neutral image output controls. Omit unless the user requested a size or aspect ratio. The backend resolves the account's configured provider/model and translates these controls; never pass provider or model."},
				"session_id":             map[string]any{"type": "string", "description": "Authenticated source session from an attached artifact reference; valid only for get/read/materialize/promote"},
				"collection_id":          map[string]any{"type": "string", "description": "Opaque collection reference; optional on create and required for collection-scoped actions"},
				"collection_name":        map[string]any{"type": "string", "maxLength": 256},
				"collection_description": map[string]any{"type": "string", "maxLength": 2048},
				"variant_id":             map[string]any{"type": "string", "description": "Opaque variant reference; optional on create"},
				"filename":               map[string]any{"type": "string", "maxLength": 255},
				"media_type":             map[string]any{"type": "string", "maxLength": 255},
				"content":                map[string]any{"type": "string", "description": "Bounded UTF-8 artifact content for create"},
				"entries":                map[string]any{"type": "array", "maxItems": manageArtifactMaxPackageFiles, "items": entry},
				"presentation":           presentation,
				"output_requirements":    artifact.OutputRequirementsToolSchema(),
				"source_session_id":      map[string]any{"type": "string", "description": "Optional authenticated source session lineage from an attached artifact reference"},
				"source_collection_id":   map[string]any{"type": "string", "description": "Optional opaque source collection lineage"},
				"source_variant_id":      map[string]any{"type": "string", "description": "Optional opaque source variant lineage"},
				"source_event_seq":       map[string]any{"type": "integer", "minimum": 1, "description": "Exact ready event sequence of the source artifact lineage"},
				"event_seq":              map[string]any{"type": "integer", "minimum": 1, "description": "Exact ready event sequence required with session_id for get/read/materialize/promote"},
				"status":                 map[string]any{"type": "string", "description": "Optional list filter: staging|ready|failed|unavailable"},
				"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxListLimit},
				"max_bytes":              map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxImageReadBytes, "description": "Maximum bytes returned by read. Text/package entries are capped at 256 KiB; supported ready images are capped at 16 MiB and returned as base64."},
				"entry":                  map[string]any{"type": "string", "maxLength": 1024, "description": "Optional normalized slash-delimited regular-file entry for application/zip read; omit to return the bounded package manifest"},
				"destination":            map[string]any{"type": "string", "maxLength": 4096, "description": "Canonical workspace-relative file or directory destination required for materialize/promote"},
				"overwrite":              map[string]any{"type": "boolean", "description": "Explicitly permit bounded replacement of destination files; defaults to false"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageArtifact(ctx context.Context, scope WorkspaceScope, callID string, args map[string]any) (string, error) {
	if r == nil {
		return "", errors.New("manage_artifact runtime is not configured")
	}
	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if actionName == "list_presets" {
		for key := range args {
			if key != "action" {
				return "", fmt.Errorf("manage_artifact list_presets contains unsupported field %q", key)
			}
		}
	}
	var principal artifact.Principal
	if actionName != "list_presets" {
		var err error
		principal, err = artifactPrincipal(ctx, scope)
		if err != nil {
			return "", err
		}
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", errors.New("manage_artifact requires a trusted tool call id")
	}
	response := map[string]any{"tool": "manage_artifact", "action": actionName, "status": "ok", "path_id": toolPathID("manage_artifact"), "details_truncated": false}
	requestID := ""
	if actionName != "list_presets" {
		requestID = managedArtifactRequestID(principal.SessionID, callID, actionName)
	}

	if actionName != "create" && actionName != "create_package" && actionName != "generate_image" {
		if _, supplied := args["output_requirements"]; supplied {
			return "", errors.New("manage_artifact output_requirements is valid only for generate_image, create, or create_package")
		}
	}
	if actionName != "list_presets" && r.artifactAuthority == nil {
		return "", errors.New("manage_artifact authority is not configured")
	}

	switch actionName {
	case "generate_image":
		variant, err := r.generateManagedImageArtifact(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "create", "create_package":
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
			if _, supplied := args["output_requirements"]; supplied {
				return "", errors.New("manage_artifact managed create must omit output_requirements; trusted orchestration injects the immutable target")
			}
		}
		input, entries, err := parseArtifactCreate(args, principal.SessionID, callID, actionName == "create_package")
		if err != nil {
			return "", err
		}
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok {
			trustedCollectionID, trustedVariantID := strings.TrimSpace(run.CollectionID), strings.TrimSpace(run.VariantID)
			if trustedCollectionID != "" || trustedVariantID != "" {
				if trustedCollectionID == "" || trustedVariantID == "" {
					return "", errors.New("manage_artifact trusted destination is incomplete")
				}
				if supplied := strings.TrimSpace(asString(args["collection_id"])); supplied != "" {
					return "", errors.New("manage_artifact managed create must omit collection_id; the destination is injected by trusted orchestration")
				}
				if supplied := strings.TrimSpace(asString(args["variant_id"])); supplied != "" {
					return "", errors.New("manage_artifact managed create must omit variant_id; the destination is injected by trusted orchestration")
				}
				input.CollectionID, input.VariantID = trustedCollectionID, trustedVariantID
				input.OutputRequirements = cloneArtifactOutputRequirements(run.OutputRequirements)
				if err := enforceArtifactPresentationRequirements(&input.Presentation, input.OutputRequirements); err != nil {
					return "", err
				}
				// The parent-owned collection already exists. Model-authored collection
				// metadata must neither conflict with nor replace that trusted target.
				input.CollectionName, input.CollectionDescription = "", ""
			}
		}
		input.RequestID = requestID
		var variant pebblestore.SessionArtifactVariant
		if actionName == "create_package" {
			variant, err = r.artifactAuthority.CreatePackage(ctx, principal, artifact.CreatePackageInput{CreateInput: input, Entries: entries})
		} else {
			variant, err = r.artifactAuthority.Create(ctx, principal, input)
		}
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReference(variant.CollectionID, variant.ID)
	case "list_presets":
		presets := artifact.ListOutputPresets()
		response["registry_version"] = artifact.OutputRequirementsRegistryVersion
		response["reviewed_source"] = artifact.OutputRequirementsReviewedSource
		response["reviewed_date"] = artifact.OutputRequirementsReviewedDate
		response["presets"] = presets
		response["count"] = len(presets)
	case "list":
		limit := clampInt(asInt(args["limit"], manageArtifactDefaultListLimit), 1, manageArtifactMaxListLimit)
		status := strings.ToLower(strings.TrimSpace(asString(args["status"])))
		if status != "" && status != pebblestore.SessionArtifactStatusStaging && status != pebblestore.SessionArtifactStatusReady && status != pebblestore.SessionArtifactStatusFailed && status != pebblestore.SessionArtifactStatusUnavailable {
			return "", errors.New("list status must be staging, ready, failed, or unavailable")
		}
		collectionID := strings.TrimSpace(asString(args["collection_id"]))
		if collectionID != "" {
			variants, err := r.artifactAuthority.ListVariants(principal, collectionID, limit)
			if err != nil {
				return "", err
			}
			items := make([]map[string]any, 0, len(variants))
			for _, variant := range variants {
				items = append(items, managedArtifactVariant(variant))
			}
			response["collection_id"], response["artifacts"], response["count"] = collectionID, items, len(items)
		} else {
			collections, err := r.artifactAuthority.List(principal, status, limit)
			if err != nil {
				return "", err
			}
			items := make([]map[string]any, 0, len(collections))
			for _, collection := range collections {
				items = append(items, managedArtifactCollection(collection))
			}
			response["collections"], response["count"] = items, len(items)
		}
	case "get":
		variantID, err := requireArtifactArgument(args, "variant_id")
		if err != nil {
			return "", err
		}
		ref, explicitSource, err := parseArtifactReadReference(args, variantID)
		if err != nil {
			return "", err
		}
		var variant pebblestore.SessionArtifactVariant
		if explicitSource {
			variant, err = r.artifactAuthority.GetReference(principal, ref)
		} else {
			variant, err = r.artifactAuthority.Get(principal, variantID)
		}
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "read":
		variantID, err := requireArtifactArgument(args, "variant_id")
		if err != nil {
			return "", err
		}
		_, maxBytesProvided := args["max_bytes"]
		maxBytes := clampInt(asInt(args["max_bytes"], manageArtifactDefaultReadBytes), 1, manageArtifactMaxImageReadBytes)
		ref, explicitSource, err := parseArtifactReadReference(args, variantID)
		if err != nil {
			return "", err
		}
		entryName, entrySupplied := args["entry"].(string)
		if _, exists := args["entry"]; exists && !entrySupplied {
			return "", errors.New("manage_artifact read entry must be a string")
		}
		if strings.TrimSpace(entryName) != entryName {
			return "", errors.New("manage_artifact read entry must be a normalized package name")
		}
		var body []byte
		var variant pebblestore.SessionArtifactVariant
		if explicitSource {
			variant, err = r.artifactAuthority.GetReference(principal, ref)
		} else {
			variant, err = r.artifactAuthority.Get(principal, variantID)
		}
		if err != nil {
			return "", err
		}
		if managedArtifactPackageMediaType(variant.MediaType) && (explicitSource || entrySupplied) {
			if variant.Status != pebblestore.SessionArtifactStatusReady {
				return "", errors.New("manage_artifact package read requires a ready artifact")
			}
			if !explicitSource {
				return "", errors.New("manage_artifact package read requires session_id, collection_id, variant_id, and event_seq")
			}
			var manifest []artifact.PackageManifestEntry
			manifest, body, variant, err = r.artifactAuthority.ReadPackageReference(ctx, principal, ref, entryName, int64(maxBytes))
			if err != nil {
				return "", err
			}
			if len(manifest) > manageArtifactMaxPackageFiles {
				return "", errors.New("manage_artifact package manifest exceeds bounded file limit")
			}
			response["artifact"] = managedArtifactVariant(variant)
			response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
			if entryName == "" {
				items := make([]map[string]any, 0, len(manifest))
				for _, item := range manifest {
					items = append(items, map[string]any{"name": item.Name, "size": item.Size})
				}
				response["manifest"], response["count"] = items, len(items)
				break
			}
			if !utf8.Valid(body) {
				return "", errors.New("manage_artifact package read returns only UTF-8 regular entries")
			}
			response["entry"], response["content"], response["bytes"] = entryName, string(body), len(body)
			break
		}
		if _, exists := args["entry"]; exists {
			return "", errors.New("manage_artifact read entry is valid only for application/zip artifacts")
		}
		isImage := managedArtifactImageMediaType(variant.MediaType)
		if isImage && (variant.Status != pebblestore.SessionArtifactStatusReady || !explicitSource) {
			return "", errors.New("manage_artifact image read requires an exact ready session_id, collection_id, variant_id, and event_seq reference")
		}
		readLimit := maxBytes
		if isImage && !maxBytesProvided {
			readLimit = manageArtifactMaxImageReadBytes
		} else if !isImage && readLimit > manageArtifactMaxReadBytes {
			readLimit = manageArtifactMaxReadBytes
		}
		if explicitSource {
			body, variant, err = r.artifactAuthority.ReadReference(ctx, principal, ref, int64(readLimit))
		} else {
			body, variant, err = r.artifactAuthority.Read(ctx, principal, variantID, int64(readLimit))
		}
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
		if isImage {
			if !managedArtifactImageDataMatches(variant.MediaType, body) {
				return "", errors.New("manage_artifact image bytes do not match the ready artifact media type")
			}
			response["encoding"], response["base64"], response["bytes"] = "base64", base64.StdEncoding.EncodeToString(body), len(body)
			break
		}
		if !utf8.Valid(body) || !managedArtifactTextMediaType(variant.MediaType) {
			return "", errors.New("manage_artifact read returns only UTF-8 text or supported image artifacts")
		}
		response["content"], response["bytes"] = string(body), len(body)
	case "materialize", "promote":
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
			return "", errors.New("manage_artifact managed Designer runs cannot materialize into the workspace; promotion requires an explicit parent workspace action")
		}
		variantID := strings.TrimSpace(asString(args["variant_id"]))
		ref, explicit, err := parseArtifactReadReference(args, variantID)
		if err != nil {
			return "", err
		}
		if !explicit {
			return "", errors.New("manage_artifact materialize requires session_id, collection_id, variant_id, and event_seq")
		}
		destination, err := requireArtifactArgument(args, "destination")
		if err != nil {
			return "", err
		}
		workspaceRoot := strings.TrimSpace(scope.PrimaryPath)
		if workspaceRoot == "" {
			return "", errors.New("manage_artifact materialize requires a trusted workspace root")
		}
		materialized, err := r.artifactAuthority.MaterializeReference(ctx, principal, ref, workspaceRoot, destination, asBool(args["overwrite"]))
		if err != nil {
			return "", err
		}
		response["reference"] = managedArtifactReferenceWithSession(ref.SessionID, ref.CollectionID, ref.VariantID, ref.EventSeq)
		response["materialized"] = map[string]any{"destination": materialized.Destination, "package": materialized.Package, "files": materialized.Files, "bytes": materialized.Bytes, "overwrite": asBool(args["overwrite"])}
	case "select":
		collectionID, err := requireArtifactArgument(args, "collection_id")
		if err != nil {
			return "", err
		}
		variantID, err := requireArtifactArgument(args, "variant_id")
		if err != nil {
			return "", err
		}
		selection, err := r.artifactAuthority.Select(principal, requestID, collectionID, variantID)
		if err != nil {
			return "", err
		}
		response["reference"] = managedArtifactReference(selection.CollectionID, selection.VariantID)
		response["event_seq"] = selection.EventSeq
	case "delete":
		collectionID, err := requireArtifactArgument(args, "collection_id")
		if err != nil {
			return "", err
		}
		variantID := strings.TrimSpace(asString(args["variant_id"]))
		if variantID == "" {
			err = r.artifactAuthority.DeleteCollection(principal, requestID, collectionID)
			response["deleted"] = map[string]any{"collection_id": collectionID}
		} else {
			err = r.artifactAuthority.DeleteVariant(principal, requestID, collectionID, variantID)
			response["deleted"] = managedArtifactReference(collectionID, variantID)
		}
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("unsupported manage_artifact action %q", actionName)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func (r *Runtime) generateManagedImageArtifact(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	for key := range args {
		switch key {
		case "action", "prompt", "image_settings", "collection_id", "collection_name", "collection_description", "variant_id", "filename", "presentation", "output_requirements":
		default:
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact generate_image contains unsupported field %q", key)
		}
	}
	if r.imageGeneration == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact image generation is not configured")
	}
	if r.uiSettings == nil {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact image model settings are not configured")
	}
	prompt := strings.TrimSpace(asString(args["prompt"]))
	if prompt == "" {
		return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact generate_image requires prompt")
	}
	if len([]rune(prompt)) > manageArtifactMaxPromptRunes {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("manage_artifact image prompt exceeds %d characters", manageArtifactMaxPromptRunes)
	}
	settings, size, err := parseManagedImageSettings(args["image_settings"])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	requestedPresentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}

	collectionID, variantID := managedArtifactOpaqueID("collection", principal.SessionID, callID), managedArtifactOpaqueID("variant", principal.SessionID, callID)
	collectionName, collectionDescription := strings.TrimSpace(asString(args["collection_name"])), strings.TrimSpace(asString(args["collection_description"]))
	if collectionName == "" {
		collectionName = "Generated image"
	}
	var requirements *pebblestore.SessionArtifactOutputRequirements
	if raw, exists := args["output_requirements"]; exists {
		requirements, err = artifact.ParseOutputRequirements(raw)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, err
		}
	}
	managedDestination := false
	if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
		managedDestination = true
		if strings.TrimSpace(run.CollectionID) == "" || strings.TrimSpace(run.VariantID) == "" {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact trusted image destination is incomplete")
		}
		if strings.TrimSpace(asString(args["collection_id"])) != "" || strings.TrimSpace(asString(args["variant_id"])) != "" {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact managed generate_image must omit collection_id and variant_id")
		}
		if _, supplied := args["output_requirements"]; supplied {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact managed generate_image must omit output_requirements; trusted orchestration injects the immutable target")
		}
		collectionID, variantID = strings.TrimSpace(run.CollectionID), strings.TrimSpace(run.VariantID)
		requirements = cloneArtifactOutputRequirements(run.OutputRequirements)
		collectionName, collectionDescription = "", ""
	}
	if !managedDestination {
		if supplied := strings.TrimSpace(asString(args["collection_id"])); supplied != "" {
			collectionID = supplied
		}
		if supplied := strings.TrimSpace(asString(args["variant_id"])); supplied != "" {
			variantID = supplied
		}
	}
	if requirements != nil {
		if int64(requirements.Width)*int64(requirements.Height) > 32<<20 {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact image output requirements exceed the bounded pixel limit")
		}
		if settings == nil {
			settings = map[string]any{}
		}
		applyImageOutputRequirements(settings, &size, requirements)
		delete(settings, "image_size")
	}

	ui, err := r.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve configured image model: %w", err)
	}
	selectionID := strings.TrimSpace(ui.Tools.Image.DefaultModel)
	if selectionID == "" {
		selectionID = imagegen.DefaultModelSelectionID
	}
	generated, err := r.imageGeneration.GenerateManagedImage(identity.ContextWithPrincipal(ctx, scope.Principal), imagegen.ManagedGenerateRequest{
		SelectionID: selectionID, Prompt: prompt, Size: size, Settings: settings, Principal: scope.Principal,
	})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("generate managed image: %w", err)
	}
	if len(generated.Bytes) == 0 || len(generated.Bytes) > manageArtifactMaxImageReadBytes || !managedArtifactImageMediaType(generated.MediaType) {
		return pebblestore.SessionArtifactVariant{}, errors.New("generated image is empty, oversized, or has an unsupported media type")
	}
	if requirements != nil {
		generated.Bytes, generated.MediaType, err = resizeManagedImage(generated.Bytes, requirements.Width, requirements.Height)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, err
		}
	}
	config, _, decodeErr := image.DecodeConfig(bytes.NewReader(generated.Bytes))
	if decodeErr != nil || config.Width < 1 || config.Height < 1 {
		return pebblestore.SessionArtifactVariant{}, errors.New("generated image dimensions could not be verified")
	}
	presentation := requestedPresentation
	presentation.Kind, presentation.Previewable, presentation.Width, presentation.Height = "image", true, config.Width, config.Height
	if strings.TrimSpace(presentation.Label) == "" {
		presentation.Label = strings.TrimSpace(asString(args["collection_name"]))
	}
	if strings.TrimSpace(presentation.Description) == "" {
		presentation.Description = strings.TrimSpace(asString(args["collection_description"]))
	}
	if presentation.Label == "" {
		presentation.Label = "Generated image"
	}
	if err := enforceArtifactPresentationRequirements(&presentation, requirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	filename := strings.TrimSpace(asString(args["filename"]))
	if filename == "" {
		filename = "generated-image" + managedImageExtension(generated.MediaType)
	}
	return r.artifactAuthority.Create(ctx, principal, artifact.CreateInput{
		RequestID: requestID, CollectionID: collectionID, CollectionName: collectionName, CollectionDescription: collectionDescription,
		VariantID: variantID, Filename: filename, MediaType: canonicalArtifactMediaType(generated.MediaType), Presentation: presentation,
		OutputRequirements: requirements, Body: append([]byte(nil), generated.Bytes...),
	})
}

func applyImageOutputRequirements(settings map[string]any, size *string, requirements *pebblestore.SessionArtifactOutputRequirements) {
	if requirements == nil {
		return
	}
	if settings == nil {
		settings = map[string]any{}
	}
	settings["aspect_ratio"] = requirements.AspectRatio
	switch {
	case requirements.Width == 1024 && requirements.Height == 1024:
		settings["size"], *size = "1024x1024", "1024x1024"
	case requirements.Width > requirements.Height:
		settings["size"], *size = "1536x1024", "1536x1024"
	case requirements.Width < requirements.Height:
		settings["size"], *size = "1024x1536", "1024x1536"
	}
}

func resizeManagedImage(data []byte, width, height int) ([]byte, string, error) {
	if width < 1 || height < 1 {
		return nil, "", errors.New("managed image output dimensions are invalid")
	}
	source, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("decode generated image for exact output requirements: %w", err)
	}
	bounds := source.Bounds()
	if bounds.Dx() == width && bounds.Dy() == height {
		return append([]byte(nil), data...), canonicalArtifactMediaType(http.DetectContentType(data)), nil
	}
	target := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		sourceY := bounds.Min.Y + y*bounds.Dy()/height
		for x := 0; x < width; x++ {
			sourceX := bounds.Min.X + x*bounds.Dx()/width
			target.Set(x, y, source.At(sourceX, sourceY))
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, target); err != nil {
		return nil, "", fmt.Errorf("encode generated image for exact output requirements: %w", err)
	}
	if encoded.Len() > manageArtifactMaxImageReadBytes {
		return nil, "", fmt.Errorf("generated image exceeds %d bytes after applying output requirements", manageArtifactMaxImageReadBytes)
	}
	return encoded.Bytes(), "image/png", nil
}

func parseManagedImageSettings(raw any) (map[string]any, string, error) {
	if raw == nil {
		return nil, "", nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return nil, "", errors.New("manage_artifact image_settings must be an object")
	}
	settings := make(map[string]any, len(value))
	for key, rawValue := range value {
		if key != "size" && key != "aspect_ratio" && key != "image_size" {
			return nil, "", fmt.Errorf("manage_artifact image_settings contains unsupported field %q", key)
		}
		text, ok := rawValue.(string)
		if !ok {
			return nil, "", fmt.Errorf("manage_artifact image_settings %s must be a string", key)
		}
		text = strings.TrimSpace(text)
		if text != "" {
			settings[key] = text
		}
	}
	size, _ := settings["size"].(string)
	return settings, size, nil
}

func managedImageExtension(mediaType string) string {
	switch canonicalArtifactMediaType(mediaType) {
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	default:
		return ".png"
	}
}

func artifactPrincipal(ctx context.Context, scope WorkspaceScope) (artifact.Principal, error) {
	run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext)
	if !ok {
		return artifact.Principal{}, errors.New("manage_artifact requires trusted run context")
	}
	sessionID := strings.TrimSpace(run.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(scope.SessionID)
	}
	// scope.Principal.SessionID identifies the authenticated local product login,
	// not the durable V3 conversation. Artifact ownership is bound to the trusted
	// run/scope V3 session plus the authenticated account and user below, so the
	// auth-session identifier must not be compared with a V3 session ID.
	scopeSessionID := strings.TrimSpace(scope.SessionID)
	producerSessionID := strings.TrimSpace(run.ChildSessionID)
	managedDestination := strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != ""
	if producerSessionID == "" {
		if managedDestination {
			return artifact.Principal{}, errors.New("manage_artifact managed destination requires trusted child session lineage")
		}
		producerSessionID = sessionID
	}
	if sessionID == "" || scopeSessionID == "" || scopeSessionID != producerSessionID {
		return artifact.Principal{}, errors.New("manage_artifact trusted session context is missing or inconsistent")
	}
	if managedDestination && (strings.TrimSpace(run.TaskCallID) == "" || strings.TrimSpace(run.CollectionID) == "" || strings.TrimSpace(run.VariantID) == "") {
		return artifact.Principal{}, errors.New("manage_artifact managed destination lineage is incomplete")
	}
	accountScopeID, userID := strings.TrimSpace(scope.Principal.AccountScopeID), strings.TrimSpace(scope.Principal.UserID)
	if accountScopeID == "" || userID == "" {
		return artifact.Principal{}, errors.New("manage_artifact requires authenticated session ownership")
	}
	return artifact.Principal{
		SessionID: sessionID, AccountScopeID: accountScopeID, UserID: userID,
		RunID: strings.TrimSpace(run.RunID), PlanID: strings.TrimSpace(run.PlanID), CheckpointID: strings.TrimSpace(run.CheckpointID), AttemptID: strings.TrimSpace(run.AttemptID),
		TaskCallID: strings.TrimSpace(run.TaskCallID), ProgramID: strings.TrimSpace(run.ProgramID), ProgramJobID: strings.TrimSpace(run.ProgramJobID),
		ChildSessionID: strings.TrimSpace(run.ChildSessionID), IterationGroupID: strings.TrimSpace(run.IterationGroupID), IterationGroup: strings.TrimSpace(run.IterationGroup),
		IterationID: strings.TrimSpace(run.IterationID), IterationIndex: run.IterationIndex, IterationLabel: strings.TrimSpace(run.IterationLabel), IterationTheme: strings.TrimSpace(run.IterationTheme),
	}, nil
}

func parseArtifactCreate(args map[string]any, sessionID, callID string, packageArtifact bool) (artifact.CreateInput, []artifact.PackageEntry, error) {
	collectionID := strings.TrimSpace(asString(args["collection_id"]))
	generatedCollection := collectionID == ""
	if generatedCollection {
		collectionID = managedArtifactOpaqueID("collection", sessionID, callID)
	}
	variantID := strings.TrimSpace(asString(args["variant_id"]))
	if variantID == "" {
		variantID = managedArtifactOpaqueID("variant", sessionID, callID)
	}
	name := strings.TrimSpace(asString(args["collection_name"]))
	if name == "" && generatedCollection {
		name = "Managed artifact"
	}
	filename := strings.TrimSpace(asString(args["filename"]))
	if packageArtifact && filename == "" {
		filename = "artifact.zip"
	}
	if filename == "" {
		return artifact.CreateInput{}, nil, errors.New("create requires filename")
	}
	presentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return artifact.CreateInput{}, nil, err
	}
	if rawRequirements, exists := args["output_requirements"]; exists {
		if rawRequirements == nil {
			return artifact.CreateInput{}, nil, errors.New("output_requirements must be an object")
		}
		if value, ok := rawRequirements.(map[string]any); ok && len(value) == 0 {
			return artifact.CreateInput{}, nil, errors.New("output_requirements must include a preset or paired width and height")
		}
	}
	var requirements *pebblestore.SessionArtifactOutputRequirements
	if rawRequirements, exists := args["output_requirements"]; exists {
		requirements, err = artifact.ParseOutputRequirements(rawRequirements)
		if err != nil {
			return artifact.CreateInput{}, nil, err
		}
	}
	if err := enforceArtifactPresentationRequirements(&presentation, requirements); err != nil {
		return artifact.CreateInput{}, nil, err
	}
	input := artifact.CreateInput{CollectionID: collectionID, CollectionName: name, CollectionDescription: asString(args["collection_description"]), VariantID: variantID, Filename: filename, MediaType: strings.TrimSpace(asString(args["media_type"])), Presentation: presentation, OutputRequirements: requirements, SourceSessionID: strings.TrimSpace(asString(args["source_session_id"])), SourceCollectionID: strings.TrimSpace(asString(args["source_collection_id"])), SourceVariantID: strings.TrimSpace(asString(args["source_variant_id"])), SourceEventSeq: asUint64(args["source_event_seq"])}
	if !packageArtifact {
		content, ok := args["content"].(string)
		if !ok || content == "" {
			return artifact.CreateInput{}, nil, errors.New("create requires non-empty content")
		}
		if len(content) > manageArtifactMaxCreateBytes {
			return artifact.CreateInput{}, nil, fmt.Errorf("create content exceeds %d bytes", manageArtifactMaxCreateBytes)
		}
		input.Body = []byte(content)
		return input, nil, nil
	}
	entries, err := parseArtifactPackageEntries(args["entries"])
	return input, entries, err
}

func parseArtifactPackageEntries(raw any) ([]artifact.PackageEntry, error) {
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return nil, errors.New("create_package requires non-empty entries")
	}
	if len(items) > manageArtifactMaxPackageFiles {
		return nil, fmt.Errorf("create_package entries exceed %d files", manageArtifactMaxPackageFiles)
	}
	entries := make([]artifact.PackageEntry, 0, len(items))
	total := 0
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("entry %d must be an object", index)
		}
		name := strings.TrimSpace(asString(item["name"]))
		content, ok := item["content"].(string)
		if name == "" || !ok {
			return nil, fmt.Errorf("entry %d requires name and content", index)
		}
		if len(content) > manageArtifactMaxCreateBytes {
			return nil, fmt.Errorf("entry %d exceeds %d bytes", index, manageArtifactMaxCreateBytes)
		}
		total += len(content)
		if total > manageArtifactMaxPackageBytes {
			return nil, fmt.Errorf("create_package content exceeds %d bytes", manageArtifactMaxPackageBytes)
		}
		entries = append(entries, artifact.PackageEntry{Name: name, Data: []byte(content)})
	}
	return entries, nil
}

func parseArtifactPresentation(raw any) (pebblestore.SessionArtifactPresentation, error) {
	if raw == nil {
		return pebblestore.SessionArtifactPresentation{}, nil
	}
	value, ok := raw.(map[string]any)
	if !ok {
		return pebblestore.SessionArtifactPresentation{}, errors.New("presentation must be an object")
	}
	return pebblestore.SessionArtifactPresentation{Kind: asString(value["kind"]), Label: asString(value["label"]), Description: asString(value["description"]), Previewable: asBool(value["previewable"]), Width: asInt(value["width"], 0), Height: asInt(value["height"], 0)}, nil
}

func requireArtifactArgument(args map[string]any, key string) (string, error) {
	value := strings.TrimSpace(asString(args[key]))
	if value == "" {
		return "", fmt.Errorf("manage_artifact requires %s", key)
	}
	return value, nil
}

func managedArtifactRequestID(sessionID, callID, action string) string {
	return managedArtifactOpaqueID("request-"+action, sessionID, callID)
}

func managedArtifactOpaqueID(kind, sessionID, callID string) string {
	seed := strings.Join([]string{"manage-artifact", kind, strings.TrimSpace(sessionID), strings.TrimSpace(callID)}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	prefix := strings.ReplaceAll(strings.Trim(strings.ToLower(kind), "-_"), "_", "-")
	if prefix == "" {
		prefix = "artifact"
	}
	if len(prefix) > 32 {
		prefix = prefix[:32]
	}
	return prefix + "-" + hex.EncodeToString(sum[:12])
}

func managedArtifactReference(collectionID, variantID string) map[string]any {
	return map[string]any{"collection_id": collectionID, "variant_id": variantID}
}

func managedArtifactReferenceWithSession(sessionID, collectionID, variantID string, eventSeq uint64) map[string]any {
	return map[string]any{"session_id": sessionID, "collection_id": collectionID, "variant_id": variantID, "event_seq": eventSeq}
}

func parseArtifactReadReference(args map[string]any, variantID string) (pebblestore.SessionArtifactSelectionReference, bool, error) {
	sessionID := strings.TrimSpace(asString(args["session_id"]))
	collectionID := strings.TrimSpace(asString(args["collection_id"]))
	eventSeq := asUint64(args["event_seq"])
	explicit := sessionID != "" || eventSeq != 0
	if !explicit {
		return pebblestore.SessionArtifactSelectionReference{}, false, nil
	}
	if sessionID == "" || collectionID == "" || variantID == "" || eventSeq == 0 {
		return pebblestore.SessionArtifactSelectionReference{}, false, errors.New("manage_artifact source get/read requires session_id, collection_id, variant_id, and event_seq")
	}
	return pebblestore.SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: collectionID, VariantID: variantID, EventSeq: eventSeq}, true, nil
}

func asUint64(value any) uint64 {
	switch typed := value.(type) {
	case float64:
		if typed > 0 && typed <= float64(1<<53) && math.Trunc(typed) == typed {
			return uint64(typed)
		}
	case int:
		if typed > 0 {
			return uint64(typed)
		}
	case uint64:
		return typed
	case json.Number:
		parsed, _ := typed.Int64()
		if parsed > 0 {
			return uint64(parsed)
		}
	}
	return 0
}

func managedArtifactPresentation(p pebblestore.SessionArtifactPresentation) map[string]any {
	return map[string]any{"kind": p.Kind, "label": p.Label, "description": p.Description, "previewable": p.Previewable, "width": p.Width, "height": p.Height}
}

func managedArtifactVariant(v pebblestore.SessionArtifactVariant) map[string]any {
	return map[string]any{"id": v.ID, "collection_id": v.CollectionID, "status": v.Status, "filename": v.Filename, "media_type": v.MediaType, "digest_sha256": v.DigestSHA256, "size": v.Size, "failure_code": v.FailureCode, "presentation": managedArtifactPresentation(v.Presentation), "output_requirements": v.OutputRequirements, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt, "event_seq": v.EventSeq}
}

func cloneArtifactOutputRequirements(input *pebblestore.SessionArtifactOutputRequirements) *pebblestore.SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func enforceArtifactPresentationRequirements(presentation *pebblestore.SessionArtifactPresentation, requirements *pebblestore.SessionArtifactOutputRequirements) error {
	if requirements == nil {
		return nil
	}
	if presentation == nil {
		return errors.New("artifact presentation is required")
	}
	if presentation.Width != 0 && presentation.Width != requirements.Width {
		return fmt.Errorf("artifact presentation width %d conflicts with output requirement %d", presentation.Width, requirements.Width)
	}
	if presentation.Height != 0 && presentation.Height != requirements.Height {
		return fmt.Errorf("artifact presentation height %d conflicts with output requirement %d", presentation.Height, requirements.Height)
	}
	presentation.Width, presentation.Height = requirements.Width, requirements.Height
	return nil
}

func managedArtifactCollection(c pebblestore.SessionArtifactCollection) map[string]any {
	return map[string]any{"id": c.ID, "status": c.Status, "name": c.Name, "description": c.Description, "presentation": managedArtifactPresentation(c.Presentation), "variant_count": c.VariantCount, "selected_variant_id": c.SelectedVariantID, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt, "event_seq": c.EventSeq}
}

func canonicalArtifactMediaType(mediaType string) string {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
}

func managedArtifactImageMediaType(mediaType string) bool {
	switch canonicalArtifactMediaType(mediaType) {
	case "image/png", "image/jpeg", "image/webp":
		return true
	default:
		return false
	}
}

func managedArtifactImageDataMatches(mediaType string, data []byte) bool {
	if len(data) == 0 || canonicalArtifactMediaType(http.DetectContentType(data)) != canonicalArtifactMediaType(mediaType) {
		return false
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	return err == nil && config.Width > 0 && config.Height > 0
}

func managedArtifactTextMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

func managedArtifactPackageMediaType(mediaType string) bool {
	return strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0])) == "application/zip"
}

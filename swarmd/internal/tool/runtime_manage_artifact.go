package tool

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	manageArtifactDefaultListLimit = 50
	manageArtifactMaxListLimit     = 100
	manageArtifactMaxCreateBytes   = 1 << 20
	manageArtifactMaxPackageFiles  = 128
	manageArtifactMaxPackageBytes  = 8 << 20
	manageArtifactDefaultReadBytes = 32 << 10
	manageArtifactMaxReadBytes     = 256 << 10
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
	TaskCallID     string
	ProgramID      string
	ProgramJobID   string
	ChildSessionID string
	IterationID    string
	IterationIndex int
	CollectionID   string
	VariantID      string
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
		Description: "Create and manage durable artifacts owned by the current session, inspect explicitly attached source references, and explicitly materialize an exact ready reference into the trusted current workspace. Uses opaque collection and variant references; ownership, source bytes, and workspace root always come from trusted run context. Never exposes private storage paths.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":                 map[string]any{"type": "string", "enum": []string{"create", "create_package", "list", "get", "read", "materialize", "promote", "select", "delete"}},
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
				"source_session_id":      map[string]any{"type": "string", "description": "Optional authenticated source session lineage from an attached artifact reference"},
				"source_collection_id":   map[string]any{"type": "string", "description": "Optional opaque source collection lineage"},
				"source_variant_id":      map[string]any{"type": "string", "description": "Optional opaque source variant lineage"},
				"source_event_seq":       map[string]any{"type": "integer", "minimum": 1, "description": "Exact ready event sequence of the source artifact lineage"},
				"event_seq":              map[string]any{"type": "integer", "minimum": 1, "description": "Exact ready event sequence required with session_id for get/read/materialize/promote"},
				"status":                 map[string]any{"type": "string", "description": "Optional list filter: staging|ready|failed|unavailable"},
				"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxListLimit},
				"max_bytes":              map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxReadBytes, "description": "Maximum UTF-8 bytes returned by read"},
				"destination":            map[string]any{"type": "string", "maxLength": 4096, "description": "Canonical workspace-relative file or directory destination required for materialize/promote"},
				"overwrite":              map[string]any{"type": "boolean", "description": "Explicitly permit bounded replacement of destination files; defaults to false"},
			},
			"required":             []string{"action"},
			"additionalProperties": false,
		},
	}
}

func (r *Runtime) executeManageArtifact(ctx context.Context, scope WorkspaceScope, callID string, args map[string]any) (string, error) {
	if r == nil || r.artifactAuthority == nil {
		return "", errors.New("manage_artifact authority is not configured")
	}
	principal, err := artifactPrincipal(ctx, scope)
	if err != nil {
		return "", err
	}
	callID = strings.TrimSpace(callID)
	if callID == "" {
		return "", errors.New("manage_artifact requires a trusted tool call id")
	}
	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	response := map[string]any{"tool": "manage_artifact", "action": actionName, "status": "ok", "path_id": toolPathID("manage_artifact"), "details_truncated": false}
	requestID := managedArtifactRequestID(principal.SessionID, callID, actionName)

	switch actionName {
	case "create", "create_package":
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
		maxBytes := clampInt(asInt(args["max_bytes"], manageArtifactDefaultReadBytes), 1, manageArtifactMaxReadBytes)
		ref, explicitSource, err := parseArtifactReadReference(args, variantID)
		if err != nil {
			return "", err
		}
		var body []byte
		var variant pebblestore.SessionArtifactVariant
		if explicitSource {
			body, variant, err = r.artifactAuthority.ReadReference(ctx, principal, ref, int64(maxBytes))
		} else {
			body, variant, err = r.artifactAuthority.Read(ctx, principal, variantID, int64(maxBytes))
		}
		if err != nil {
			return "", err
		}
		if !utf8.Valid(body) || !managedArtifactTextMediaType(variant.MediaType) {
			return "", errors.New("manage_artifact read returns only UTF-8 text artifacts")
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
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

func artifactPrincipal(ctx context.Context, scope WorkspaceScope) (artifact.Principal, error) {
	run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext)
	if !ok {
		return artifact.Principal{}, errors.New("manage_artifact requires trusted run context")
	}
	sessionID := strings.TrimSpace(run.SessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(scope.SessionID)
	}
	principalSessionID := strings.TrimSpace(scope.Principal.SessionID)
	scopeSessionID := strings.TrimSpace(scope.SessionID)
	producerSessionID := strings.TrimSpace(run.ChildSessionID)
	managedDestination := strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != ""
	if producerSessionID == "" {
		if managedDestination {
			return artifact.Principal{}, errors.New("manage_artifact managed destination requires trusted child session lineage")
		}
		producerSessionID = sessionID
	}
	if sessionID == "" || scopeSessionID == "" || scopeSessionID != producerSessionID || (principalSessionID != "" && principalSessionID != sessionID && principalSessionID != producerSessionID) {
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
		ChildSessionID: strings.TrimSpace(run.ChildSessionID), IterationID: strings.TrimSpace(run.IterationID), IterationIndex: run.IterationIndex,
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
	input := artifact.CreateInput{CollectionID: collectionID, CollectionName: name, CollectionDescription: asString(args["collection_description"]), VariantID: variantID, Filename: filename, MediaType: strings.TrimSpace(asString(args["media_type"])), Presentation: presentation, SourceSessionID: strings.TrimSpace(asString(args["source_session_id"])), SourceCollectionID: strings.TrimSpace(asString(args["source_collection_id"])), SourceVariantID: strings.TrimSpace(asString(args["source_variant_id"])), SourceEventSeq: asUint64(args["source_event_seq"])}
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
	return map[string]any{"id": v.ID, "collection_id": v.CollectionID, "status": v.Status, "filename": v.Filename, "media_type": v.MediaType, "digest_sha256": v.DigestSHA256, "size": v.Size, "failure_code": v.FailureCode, "presentation": managedArtifactPresentation(v.Presentation), "created_at": v.CreatedAt, "updated_at": v.UpdatedAt, "event_seq": v.EventSeq}
}

func managedArtifactCollection(c pebblestore.SessionArtifactCollection) map[string]any {
	return map[string]any{"id": c.ID, "status": c.Status, "name": c.Name, "description": c.Description, "presentation": managedArtifactPresentation(c.Presentation), "variant_count": c.VariantCount, "selected_variant_id": c.SelectedVariantID, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt, "event_seq": c.EventSeq}
}

func managedArtifactTextMediaType(mediaType string) bool {
	mediaType = strings.ToLower(strings.TrimSpace(strings.SplitN(mediaType, ";", 2)[0]))
	return strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" || strings.HasSuffix(mediaType, "+json") || strings.HasSuffix(mediaType, "+xml")
}

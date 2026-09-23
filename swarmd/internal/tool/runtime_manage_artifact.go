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
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/audiogen"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/imagegen"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/videogen"
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
	manageArtifactMaxBatchItems     = 64

	manageArtifactReadResponseQuotaCode         = "artifact_read_response_too_large"
	manageArtifactPackageEntryResponseQuotaCode = "artifact_package_entry_response_too_large"
)

// ArtifactAuthority is the session-owned managed artifact lifecycle boundary.
// Runtime callers inject the canonical authority; the tool never resolves
// storage paths or mutates artifact metadata directly.
type ArtifactAuthority interface {
	Create(context.Context, artifact.Principal, artifact.CreateInput) (pebblestore.SessionArtifactVariant, error)
	Reserve(artifact.Principal, artifact.CreateInput) (pebblestore.SessionArtifactVariant, error)
	MarkFailed(artifact.Principal, string, string, string, string) (pebblestore.SessionArtifactVariant, error)
	UpdateProgress(artifact.Principal, string, string, string, pebblestore.SessionArtifactProgress) (pebblestore.SessionArtifactVariant, error)
	CreateInitialComposition(context.Context, artifact.Principal, artifact.CreateInitialCompositionInput) (pebblestore.SessionArtifactVariant, error)
	CreatePackage(context.Context, artifact.Principal, artifact.CreatePackageInput) (pebblestore.SessionArtifactVariant, error)
	List(artifact.Principal, string, int) ([]pebblestore.SessionArtifactCollection, error)
	ListVariants(artifact.Principal, string, int) ([]pebblestore.SessionArtifactVariant, error)
	SearchCatalog(artifact.Principal, pebblestore.SessionArtifactCatalogOptions) (pebblestore.SessionArtifactCatalogPage, error)
	Get(artifact.Principal, string) (pebblestore.SessionArtifactVariant, error)
	GetReference(artifact.Principal, pebblestore.SessionArtifactSelectionReference) (pebblestore.SessionArtifactVariant, error)
	Read(context.Context, artifact.Principal, string, int64) ([]byte, pebblestore.SessionArtifactVariant, error)
	ReadReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, int64) ([]byte, pebblestore.SessionArtifactVariant, error)
	ReadPackageReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, int64) ([]artifact.PackageManifestEntry, []byte, pebblestore.SessionArtifactVariant, error)
	MaterializeReference(context.Context, artifact.Principal, pebblestore.SessionArtifactSelectionReference, string, string, bool) (artifact.Materialized, error)
	MaterializeBatchReferences(context.Context, artifact.Principal, []artifact.MaterializeBatchItem, string, string, bool) ([]artifact.Materialized, []pebblestore.SessionArtifactVariant, error)
	PublishWorkspace(context.Context, artifact.Principal, artifact.CreateFileInput) (pebblestore.SessionArtifactVariant, error)
	Select(artifact.Principal, string, string, string) (pebblestore.SessionArtifactSelectionReference, error)
	DeleteVariant(artifact.Principal, string, string, string) error
	DeleteCollection(artifact.Principal, string, string) error
	Import(context.Context, artifact.Principal, artifact.ImportVariantInput) (pebblestore.SessionArtifactVariant, error)
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
	TaskCallID              string
	ProgramID               string
	ProgramJobID            string
	ChildSessionID          string
	IterationGroupID        string
	IterationGroup          string
	IterationID             string
	IterationIndex          int
	IterationLabel          string
	IterationTheme          string
	IterationSectionID      string
	IterationSectionLabel   string
	IterationSectionStartMs int64
	IterationSectionEndMs   int64
	PartID                  string
	PartLabel               string
	PartKind                string
	Part                    *pebblestore.SessionArtifactPart
	SelectedReviewTargets   []pebblestore.SessionArtifactPart
	SourceArtifact          *pebblestore.SessionArtifactSelectionReference
	SourceComposition       *pebblestore.SessionArtifactComposition
	SourcePartDefinition    *pebblestore.SessionArtifactPartDefinition
	SourcePartRevision      *pebblestore.SessionArtifactPartRevisionReference
	// SourcePartDefinitions and SourcePartRevisions are the canonical bounded
	// multi-part selection. The singular fields remain a compatibility view when
	// exactly one part is selected.
	SourcePartDefinitions []pebblestore.SessionArtifactPartDefinition
	SourcePartRevisions   []pebblestore.SessionArtifactPartRevisionReference
	ArtifactStepID        string
	CandidateIndex        int
	AutoAccept            bool
	CollectionID          string
	VariantID             string
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements
	AnimationProfile      *pebblestore.SessionArtifactAnimationProfile
}

type artifactRunContextKey struct{}

func WithArtifactRunContext(parent context.Context, run ArtifactRunContext) context.Context {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithValue(parent, artifactRunContextKey{}, run)
}

func manageArtifactDefinition() Definition {
	part := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":          map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,127}$"},
			"label":       map[string]any{"type": "string", "maxLength": 256},
			"kind":        map[string]any{"type": "string", "enum": []string{"temporal", "spatial", "page", "state", "selector", "semantic"}, "description": "Locator kind: temporal, spatial, page, state, selector, semantic."},
			"description": map[string]any{"type": "string", "maxLength": 2048},
		},
		"required":             []string{"id", "label", "kind"},
		"additionalProperties": true,
	}
	presentation := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":        map[string]any{"type": "string", "description": "Display kind: download|text|code|image|html|package"},
			"label":       map[string]any{"type": "string", "maxLength": 256},
			"previewable": map[string]any{"type": "boolean"},
		},
		"additionalProperties": true,
	}
	return Definition{
		Type:        "function",
		Name:        "manage_artifact",
		Description: "Create, revise, inspect, import, and export native Artifact V3 documents, HTML animations, and generative AI media (images, video, audio). For retained cross-session artifacts, call action='read_v3' with artifact_v3_reference to inspect, or action='import' to create an editable copy in the current session. For images, call action='image_capabilities' first to read supported options and capability_token, then pass them to action='generate_image'. For audio, call action='audio_capabilities' first to inspect configured model duration limits and capability_token, then pass them to action='generate_audio'. For multi-scene video stories with soundtrack, use action='generate_video_story' with scenes and soundtrack directly. For help, call action='help'. Do not substitute generate_image for native HTML documents.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"artifact_id": map[string]any{"type": "string", "description": "Native artifact identity for source_v3, draft_status_v3."},
				"artifact_v3_reference": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"session_id":   map[string]any{"type": "string", "description": "Owner session ID."},
						"artifact_id":  map[string]any{"type": "string", "description": "Native artifact identity."},
						"revision_ref": map[string]any{"type": "string", "description": "Exact Git revision ref (e.g. revision-<commit_oid>)."},
					},
					"additionalProperties": false,
					"required":             []string{"session_id", "artifact_id", "revision_ref"},
					"description":          "Complete exact ready native Artifact V3 preview reference for read_v3, revise_v3, begin_v3, or import.",
				},
				"artifact_reference": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"session_id":    map[string]any{"type": "string", "description": "Owner session ID."},
						"collection_id": map[string]any{"type": "string", "description": "Collection ID."},
						"variant_id":    map[string]any{"type": "string", "description": "Variant ID."},
						"event_seq":     map[string]any{"type": "integer", "minimum": 1, "description": "Exact event sequence number."},
					},
					"additionalProperties": false,
					"required":             []string{"session_id", "collection_id", "variant_id", "event_seq"},
					"description":          "Complete exact ready legacy managed artifact reference for import. For read/get/materialize, copy the four reference fields to the top level.",
				},
				"revision_intent":        map[string]any{"type": "string", "enum": []string{"whole_project", "focused_parts"}, "description": "Destination-owned revision scope for begin_v3/revise_v3."},
				"target_part_ids":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Exact semantic Part IDs for focused destination revisions."},
				"source_kind":            map[string]any{"type": "string", "enum": []string{"head", "historical_revision", "candidate"}, "description": "Native retained catalog filter."},
				"created_after":          map[string]any{"type": "integer", "minimum": 0, "description": "Catalog creation lower bound in Unix milliseconds."},
				"created_before":         map[string]any{"type": "integer", "minimum": 0, "description": "Catalog creation upper bound in Unix milliseconds."},
				"library":                map[string]any{"type": "string", "enum": []string{"legacy", "native"}, "description": "Optional library discriminator for search/list: 'legacy' for managed artifacts, 'native' for Artifact V3 documents."},
				"resume_draft":           map[string]any{"type": "object", "description": "Draft identity for resume_v3. See action='help' topic='workflow'."},
				"action":                 map[string]any{"type": "string", "enum": []string{"create", "import", "list_v3", "source_v3", "select_v3", "read_v3", "revise_v3", "begin_v3", "author_v3", "resume_v3", "draft_status_v3", "image_capabilities", "audio_capabilities", "generate_image", "generate_video", "generate_video_story", "generate_audio", "extract_video_frame", "chain_video", "export_html_stills", "export_html_animation", "export_html_animation_fallback", "cancel_html_animation_export", "derive_text", "read_part", "publish_part", "read_parts", "publish_parts", "select_parts", "list_presets", "list", "search", "get", "read", "materialize", "materialize_batch", "promote", "publish_workspace", "select", "delete", "help"}, "description": "Artifact operation: create, import, search, get, read, materialize/materialize_batch, promote, publish_workspace, generate_video_story, etc. Call action='help' with optional topic (animation, video, audio, narration, workflow) for complete schemas."},
				"capability_token":       map[string]any{"type": "string", "description": "Fresh token returned by image_capabilities or audio_capabilities; required for Google generative media calls."},
				"scenes":                 map[string]any{"type": "array", "items": map[string]any{"type": "object", "properties": map[string]any{"prompt": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "duration_seconds": map[string]any{"type": "integer"}}, "required": []string{"prompt"}}, "description": "Multi-scene video script for generate_video_story."},
				"soundtrack":             map[string]any{"type": "string", "description": "Soundtrack music/audio description for generate_video_story."},
				"topic":                  map[string]any{"type": "string", "description": "Optional topic for action=help (animation, video, audio, narration, workflow)."},
				"prompt":                 map[string]any{"type": "string", "maxLength": manageArtifactMaxPromptRunes, "description": "Prompt for generation. Call action='help' for schema."},
				"title":                  map[string]any{"type": "string", "maxLength": 160, "description": "Human-readable title."},
				"image_settings":         map[string]any{"type": "object", "properties": map[string]any{"size": map[string]any{"type": "string"}, "aspect_ratio": map[string]any{"type": "string"}, "image_size": map[string]any{"type": "string"}}, "additionalProperties": false, "description": "Optional image settings. Call action='help' for schema."},
				"session_id":             map[string]any{"type": "string", "description": "Combined with collection_id, variant_id, and event_seq for exact artifact reference."},
				"collection_id":          map[string]any{"type": "string", "description": "Combined with session_id, variant_id, and event_seq for exact artifact reference."},
				"variant_id":             map[string]any{"type": "string", "description": "Combined with session_id, collection_id, and event_seq for exact artifact reference."},
				"filename":               map[string]any{"type": "string", "maxLength": 255},
				"media_type":             map[string]any{"type": "string", "maxLength": 255, "description": "Artifact media type."},
				"content":                map[string]any{"type": "string", "description": "Bounded UTF-8 artifact content."},
				"draft_handle":           map[string]any{"type": "object", "description": "Draft handle from begin_v3/create/revise_v3. Call action='help' topic='workflow'."},
				"operation":              map[string]any{"type": "object", "description": "Artifact V3 authoring operation. Call action='help' topic='workflow'."},
				"content_base64":         map[string]any{"type": "string", "description": "Bounded base64 replacement bytes."},
				"initial_parts":          map[string]any{"type": "array", "minItems": 2, "maxItems": pebblestore.SessionArtifactMaxParts, "items": map[string]any{"type": "object"}, "description": "Two or more real independently byte-bearing initial parts for create. Call action='help' for schema."},
				"parts":                  map[string]any{"type": "array", "maxItems": pebblestore.SessionArtifactMaxParts, "items": part, "description": "Optional review/edit targets on one monolithic artifact. Call action='help' for schema."},
				"references":             map[string]any{"type": "array", "minItems": 1, "maxItems": manageArtifactMaxBatchItems, "items": map[string]any{"type": "object"}, "description": "Complete exact ready references imported by materialize_batch into destination directory."},
				"source":                 map[string]any{"type": "string", "maxLength": 4096, "description": "Canonical workspace regular file or directory for publish_workspace."},
				"presentation":           presentation,
				"animation_profile":      map[string]any{"type": "object", "description": "Animation profile: motion_ui, spatial_3d, vector_playback, or final_render. Exports like export_html_animation_fallback inherit the exact source artifact's reviewed animation profile; omit this field for exports. Call action='help' topic='animation'."},
				"source_session_id":      map[string]any{"type": "string", "description": "For every image remix, copy source_session_id from the reusable exact reference."},
				"source_collection_id":   map[string]any{"type": "string", "description": "For every image remix, copy source_collection_id from the reusable exact reference."},
				"source_variant_id":      map[string]any{"type": "string", "description": "For every image remix, copy source_variant_id from the reusable exact reference."},
				"source_event_seq":       map[string]any{"type": "integer", "minimum": 1, "description": "For every image remix, copy source_event_seq from the reusable exact reference."},
				"alternatives":           map[string]any{"type": "array", "items": map[string]any{"type": "object"}, "description": "Multi-candidate revise_v3 request array ([{candidate_index, content}]). See action='help'."},
				"event_seq":              map[string]any{"type": "integer", "minimum": 1, "description": "Combined with session_id, collection_id, and variant_id for exact artifact reference."},
				"query":                  map[string]any{"type": "string", "description": "Authenticated cross-session search; ready items return complete exact references."},
				"status":                 map[string]any{"type": "string", "description": "Status filter: native list_v3/search accepts ready|selected; legacy accepts staging|ready|failed|unavailable."},
				"cursor":                 map[string]any{"type": "string", "description": "Opaque continuation cursor from next_cursor unchanged; never parse or construct it."},
				"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxListLimit, "description": "Maximum list items; use next_cursor/cursor to continue."},
				"max_bytes":              map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxImageReadBytes, "description": "Maximum bytes returned by read. A response-quota error does not mean the artifact is unavailable; use materialize instead."},
				"destination":            map[string]any{"type": "string", "maxLength": 4096, "description": "Canonical workspace path required for materialize/promote and materialize_batch; overwrite defaults to false."},
				"model":                  map[string]any{"type": "string", "description": "Model identifier for audio generation/capability discovery only; not accepted for artifact import."},
				"message":                map[string]any{"type": "string", "description": "Optional native import commit message."},
				"collection_name":        map[string]any{"type": "string", "description": "Optional destination collection label for legacy import."},
				"collection_description": map[string]any{"type": "string", "description": "Optional destination collection description for legacy import."},
				"overwrite":              map[string]any{"type": "boolean", "description": "Permit replacement of destination files; defaults to false."},
			},
			"required":             []string{"action"},
			"additionalProperties": true,
		},
	}
}

func manageArtifactAnimationProfileToolSchema() map[string]any {
	schema := artifact.AnimationProfileToolSchema()
	schema["description"] = "Optional only for create, create_package, publish_workspace, or derive_text. Export actions, including export_html_animation and export_html_animation_fallback, authenticate and inherit the exact source artifact's reviewed animation profile; omit this field for exports. " + strings.TrimSpace(asString(schema["description"]))
	return schema
}

func (r *Runtime) executeManageArtifact(ctx context.Context, scope WorkspaceScope, callID string, args map[string]any) (string, error) {
	if r == nil {
		return "", errors.New("manage_artifact runtime is not configured")
	}
	actionName := strings.ToLower(strings.TrimSpace(asString(args["action"])))
	if _, supplied := args["narration_plan"]; supplied && actionName != "create" {
		return "", errors.New("manage_artifact narration_plan is valid only for create")
	}
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
	if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (run.SourcePartRevision != nil || len(run.SourcePartRevisions) != 0) {
		switch actionName {
		case "read_part", "publish_part", "read_parts", "publish_parts", "list_presets":
		default:
			return "", errors.New("focused managed Designer context permits only read_part/publish_part or read_parts/publish_parts")
		}
	}

	if actionName != "create" && actionName != "create_package" && actionName != "generate_image" && actionName != "publish_workspace" {
		if _, supplied := args["output_requirements"]; supplied {
			return "", errors.New("manage_artifact output_requirements is valid only for generate_image, create, create_package, or publish_workspace")
		}
	}
	if actionName != "create" && actionName != "create_package" && actionName != "publish_workspace" && actionName != "derive_text" {
		if _, supplied := args["animation_profile"]; supplied {
			return "", errors.New("manage_artifact animation_profile is valid only for create, create_package, publish_workspace, or derive_text; export actions inherit the exact source animation profile and must omit animation_profile")
		}
	}
	nativeDiscovery := (actionName == "list" || actionName == "search") && strings.ToLower(strings.TrimSpace(asString(args["library"]))) == "native"
	if actionName != "list_presets" && actionName != "image_capabilities" && actionName != "audio_capabilities" && actionName != "help" && actionName != "import" && !((actionName == "create" || actionName == "list_v3" || actionName == "source_v3" || actionName == "select_v3" || actionName == "read_v3" || actionName == "revise_v3" || actionName == "begin_v3" || actionName == "author_v3" || actionName == "resume_v3" || actionName == "draft_status_v3" || nativeDiscovery) && r.artifactV3Author != nil) && r.artifactAuthority == nil {
		return "", errors.New("manage_artifact authority is not configured")
	}

	switch actionName {
	case "help":
		topic := strings.ToLower(strings.TrimSpace(asString(args["topic"])))
		response["topic"] = topic
		response["help"] = artifactHelpText(topic)
	case "image_capabilities":
		capabilities, err := r.managedImageCapabilities(principal.AccountScopeID)
		if err != nil {
			return "", err
		}
		response["image_capabilities"] = capabilities
	case "audio_capabilities":
		modelArg := strings.TrimSpace(asString(args["model"]))
		capabilities, err := r.managedAudioCapabilitiesWithModel(principal.AccountScopeID, modelArg)
		if err != nil {
			return "", err
		}
		response["audio_capabilities"] = capabilities
	case "read_part":
		part, err := r.readManagedArtifactPart(ctx, principal, args)
		if err != nil {
			return "", err
		}
		response["part"] = part
	case "read_parts":
		parts, err := r.readManagedArtifactParts(ctx, principal, args)
		if err != nil {
			return "", err
		}
		response["parts"] = parts
	case "publish_part":
		variant, err := r.publishManagedArtifactPart(ctx, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "select_parts":
		variant, err := r.selectManagedArtifactParts(ctx, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "publish_parts":
		variant, err := r.publishManagedArtifactParts(ctx, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "export_html_stills":
		exports, sourceRef, requirements, storyboardManifest, err := r.exportHTMLStills(ctx, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["source_reference"] = managedArtifactReferenceWithSession(sourceRef.SessionID, sourceRef.CollectionID, sourceRef.VariantID, sourceRef.EventSeq)
		response["output_requirements"] = requirements
		response["exports"], response["count"] = exports, len(exports)
		if storyboardManifest != nil {
			sections := make([]map[string]any, 0, len(storyboardManifest.Sections))
			exportByState := make(map[string]map[string]any, len(exports))
			for _, exported := range exports {
				exportByState[asString(exported["state_id"])] = exported
			}
			for _, section := range storyboardManifest.Sections {
				exported := exportByState[section.CaptureStateID]
				sections = append(sections, map[string]any{"id": section.ID, "capture_state_id": section.CaptureStateID, "title": section.Title, "duration_ms": section.DurationMs, "narration": section.Narration, "on_screen_text": section.OnScreenText, "creative_direction": section.CreativeDirection, "filming_requirements": section.FilmingRequirements, "production_state": section.ProductionState, "composition": section.Composition, "visual": exported["reference"]})
			}
			response["storyboard_handoff"] = map[string]any{"version": storyboardManifest.Version, "source_reference": response["source_reference"], "compositions": storyboardManifest.Compositions, "sections": sections}
		}
	case "export_html_animation_fallback":
		variant, sourceRef, requirements, err := r.exportHTMLAnimationFallback(ctx, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["source_reference"] = managedArtifactReferenceWithSession(sourceRef.SessionID, sourceRef.CollectionID, sourceRef.VariantID, sourceRef.EventSeq)
		response["output_requirements"] = requirements
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "export_html_animation":
		variant, sourceRef, requirements, err := r.exportHTMLAnimation(ctx, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["source_reference"] = managedArtifactReferenceWithSession(sourceRef.SessionID, sourceRef.CollectionID, sourceRef.VariantID, sourceRef.EventSeq)
		response["output_requirements"] = requirements
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "cancel_html_animation_export":
		variant, err := r.cancelHTMLAnimationExport(principal, callID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "derive_text":
		variant, err := r.deriveManagedTextArtifact(ctx, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "generate_image":
		variant, err := r.generateManagedImageArtifact(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "generate_video":
		if args["scenes"] != nil || args["parts"] != nil || args["script"] != nil {
			variant, details, err := r.generateVideoStory(ctx, scope, principal, callID, requestID, args)
			if err != nil {
				return "", err
			}
			response["status"] = "ok"
			response["artifact"] = managedArtifactVariant(variant)
			response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
			for k, v := range details {
				response[k] = v
			}
			break
		}
		videoResult, err := r.generateManagedVideoArtifact(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["status"] = "ok"
		response["title"] = videoResult.Title
		response["prompt"] = videoResult.Prompt
		response["model"] = videoResult.Model
		response["provider"] = videoResult.Provider
		response["aspect_ratio"] = videoResult.AspectRatio
		response["resolution"] = videoResult.Resolution
		response["duration_seconds"] = videoResult.DurationSeconds
		response["estimated_cost_usd"] = videoResult.TotalEstimatedCostUSD
		response["cost_per_video_usd"] = videoResult.CostPerVideoUSD
		response["price_status"] = videoResult.PriceStatus
		response["pricing_summary"] = videoResult.PricingSummary
		if videoResult.SnapshotID != "" {
			response["snapshot_id"] = videoResult.SnapshotID
		}
		if videoResult.SnapshotVersion != "" {
			response["snapshot_version"] = videoResult.SnapshotVersion
		}
		response["artifact"] = managedArtifactVariant(videoResult.LastVariant)
		response["reference"] = managedArtifactReferenceWithSession(videoResult.LastVariant.SessionID, videoResult.LastVariant.CollectionID, videoResult.LastVariant.ID, videoResult.LastVariant.EventSeq)
		response["variants"] = videoResult.Variants
		response["references"] = videoResult.References
		if videoResult.HasImageInput {
			response["has_image_input"] = true
		}
	case "generate_video_story":
		variant, details, err := r.generateVideoStory(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["status"] = "ok"
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
		for k, v := range details {
			response[k] = v
		}
	case "extract_video_frame":
		variant, err := r.extractVideoFrame(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["status"] = "ok"
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "chain_video":
		variant, details, err := r.chainVideo(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["status"] = "ok"
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
		for k, v := range details {
			response[k] = v
		}
	case "generate_audio":
		audioResult, err := r.generateManagedAudioArtifact(ctx, scope, principal, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["status"] = "ok"
		response["title"] = audioResult.Title
		response["prompt"] = audioResult.Prompt
		if len(audioResult.Prompts) > 0 {
			response["prompts"] = audioResult.Prompts
		}
		response["model"] = audioResult.Model
		response["provider"] = audioResult.Provider
		response["duration_seconds"] = audioResult.DurationSeconds
		response["duration_ms"] = audioResult.DurationMs
		if audioResult.Lyrics != "" {
			response["lyrics"] = audioResult.Lyrics
		}
		response["estimated_cost_usd"] = audioResult.TotalEstimatedCostUSD
		response["cost_per_audio_usd"] = audioResult.CostPerAudioUSD
		response["pricing_summary"] = audioResult.PricingSummary
		response["artifact"] = managedArtifactVariant(audioResult.LastVariant)
		response["reference"] = managedArtifactReferenceWithSession(audioResult.LastVariant.SessionID, audioResult.LastVariant.CollectionID, audioResult.LastVariant.ID, audioResult.LastVariant.EventSeq)
		response["variants"] = audioResult.Variants
		response["references"] = audioResult.References
		response["metadata"] = audioResult.Metadata
		if audioResult.HasImageInput {
			response["has_image_input"] = true
		}
	case "import":
		if err := r.importManagedArtifact(ctx, scope, principal, requestID, args, response); err != nil {
			return "", err
		}
	case "create":
		artifactV3Result, err := r.createDirectArtifactV3HTML(ctx, scope, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = artifactV3Result
		response["reference"] = artifactV3Result["reference"]
	case "select_v3":
		result, err := r.selectDirectArtifactV3(ctx, scope, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = result
	case "list_v3", "source_v3":
		result, err := r.discoverDirectArtifactV3(ctx, scope, principal, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = result
	case "read_v3":
		artifactV3Result, err := r.readDirectArtifactV3HTML(ctx, scope, principal, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = artifactV3Result
		response["reference"] = args["artifact_v3_reference"]
	case "draft_status_v3":
		result, err := r.locateDirectArtifactV3Draft(ctx, scope, principal, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = result
	case "resume_v3":
		result, err := r.resumeDirectArtifactV3Draft(ctx, scope, principal, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = result
	case "author_v3":
		result, err := r.authorDirectArtifactV3Draft(ctx, scope, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = result
	case "revise_v3", "begin_v3":
		artifactV3Result, err := r.reviseDirectArtifactV3HTML(ctx, scope, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["artifact_v3"] = artifactV3Result
		if candidate, ok := artifactV3Result["candidate"]; ok {
			response["reference"] = candidate
		} else {
			response["references"] = artifactV3Result["candidates"]
		}
	case "create_package":
		return "", errors.New("manage_artifact create_package is retired; managed project authoring uses Artifact V3")
		/* Retained below temporarily as unreachable deletion evidence until the V1
		write implementation is removed after legacy-ready read compatibility is
		fully separated. No provider schema or registered action can enter it. */
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
			if _, supplied := args["output_requirements"]; supplied {
				return "", errors.New("manage_artifact managed create must omit output_requirements; trusted orchestration injects the immutable target")
			}
			if _, supplied := args["animation_profile"]; supplied {
				return "", errors.New("manage_artifact managed create must omit animation_profile; trusted orchestration injects the immutable target")
			}
		}
		input, entries, err := parseArtifactCreate(args, principal.SessionID, callID, actionName == "create_package")
		if err != nil {
			return "", err
		}
		// A direct manage_artifact publication is one complete candidate, not a
		// review wave. Accept it atomically when it becomes ready so its exact
		// returned reference can immediately parent a later focused iteration.
		input.AutoAccept = true
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
				if strings.TrimSpace(input.MediaType) == "" && run.SourceArtifact != nil {
					input.MediaType = canonicalArtifactMediaType(mime.TypeByExtension(filepath.Ext(input.Filename)))
				}
				input.OutputRequirements = cloneArtifactOutputRequirements(run.OutputRequirements)
				if run.SourceArtifact != nil {
					if input.SourceSessionID != "" && (input.SourceSessionID != run.SourceArtifact.SessionID || input.SourceCollectionID != run.SourceArtifact.CollectionID || input.SourceVariantID != run.SourceArtifact.VariantID || input.SourceEventSeq != run.SourceArtifact.EventSeq) {
						return "", errors.New("manage_artifact source lineage does not match the trusted task source")
					}
					input.SourceSessionID = run.SourceArtifact.SessionID
					input.SourceCollectionID = run.SourceArtifact.CollectionID
					input.SourceVariantID = run.SourceArtifact.VariantID
					input.SourceEventSeq = run.SourceArtifact.EventSeq
				}
				input.ArtifactStepID, input.CandidateIndex, input.AutoAccept = strings.TrimSpace(run.ArtifactStepID), run.CandidateIndex, run.AutoAccept
				input.AnimationProfile = cloneArtifactAnimationProfile(run.AnimationProfile)
				if err := enforceArtifactPresentationRequirements(&input.Presentation, input.OutputRequirements); err != nil {
					return "", err
				}
				// The parent-owned collection already exists. Model-authored collection
				// metadata must neither conflict with nor replace that trusted target.
				input.CollectionName, input.CollectionDescription = "", ""
			}
		}
		if err := validateArtifactAnimationMedia(input.AnimationProfile, actionName == "create_package", input.Filename, input.MediaType); err != nil {
			return "", err
		}
		input.RequestID = requestID
		if actionName == "create" && len(input.Parts) == 0 {
			input.Parts = deriveArtifactHTMLParts(input.Body, input.MediaType)
		}
		if actionName == "create_package" && len(input.Parts) == 0 {
			for _, entry := range entries {
				if pathClean(entry.Name) == "index.html" {
					input.Parts = deriveArtifactHTMLParts(entry.Data, "text/html")
					break
				}
			}
		}
		initialParts, err := parseArtifactInitialParts(args["initial_parts"], principal.SessionID, input.CollectionID, input.VariantID, callID)
		if err != nil {
			return "", err
		}
		if len(initialParts) != 0 && managedHTMLAnimationProfile(input.AnimationProfile) && canonicalArtifactMediaType(input.MediaType) == "text/html" {
			return "", animationError("animation_source_invalid", "profiled HTML animation must publish one complete preflightable HTML document or package")
		}
		var variant pebblestore.SessionArtifactVariant
		var animationPreflight *managedAnimationPreflight
		if len(initialParts) == 0 {
			animationPreflight, err = r.reserveAndPreflightManagedAnimation(ctx, principal, input, entries, actionName == "create_package")
			if err != nil {
				return "", err
			}
		}
		gatedAnimation := animationPreflight != nil
		switch {
		case len(initialParts) != 0:
			if actionName != "create" {
				return "", errors.New("manage_artifact initial_parts is valid only for create")
			}
			chainID := pebblestore.RootSessionArtifactChainID(principal.SessionID, input.CollectionID, input.VariantID)
			variant, err = r.artifactAuthority.CreateInitialComposition(ctx, principal, artifact.CreateInitialCompositionInput{
				CreateInput: input, ArtifactChainID: chainID,
				CompositionID: managedArtifactOpaqueID("composition", principal.SessionID, callID), Parts: initialParts,
			})
		case actionName == "create_package":
			variant, err = r.artifactAuthority.CreatePackage(ctx, principal, artifact.CreatePackageInput{CreateInput: input, Entries: entries})
		default:
			variant, err = r.artifactAuthority.Create(ctx, principal, input)
		}
		if err != nil {
			return "", err
		}
		if gatedAnimation && variant.Status != pebblestore.SessionArtifactStatusReady {
			return "", animationError("animation_publish_failed", "profiled HTML animation did not finalize as a ready artifact after trusted preflight")
		}
		if gatedAnimation {
			inspectionRefs, inspectionErr := r.publishManagedAnimationInspectionFrames(ctx, principal, variant, animationPreflight)
			if inspectionErr != nil {
				return "", inspectionErr
			}
			response["trusted_animation_preflight"] = true
			response["animation_inspection_references"] = inspectionRefs
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	case "list_presets":
		presets := artifact.ListOutputPresets()
		response["registry_version"] = artifact.OutputRequirementsRegistryVersion
		response["reviewed_source"] = artifact.OutputRequirementsReviewedSource
		response["reviewed_date"] = artifact.OutputRequirementsReviewedDate
		response["presets"] = presets
		response["count"] = len(presets)
	case "list", "search":
		library := strings.ToLower(strings.TrimSpace(asString(args["library"])))
		if library == "native" {
			v3Args := make(map[string]any, len(args))
			for k, v := range args {
				v3Args[k] = v
			}
			v3Args["action"] = "list_v3"
			delete(v3Args, "library")
			result, err := r.discoverDirectArtifactV3(ctx, scope, principal, v3Args)
			if err != nil {
				return "", err
			}
			response["artifact_v3"] = result
			response["artifacts"] = result["artifacts"]
			response["count"] = result["count"]
			response["has_more"] = result["has_more"]
			if nc, ok := result["next_cursor"]; ok && nc != "" {
				response["next_cursor"] = nc
			}
			break
		} else if library != "" && library != "legacy" {
			return "", fmt.Errorf("manage_artifact %s library must be legacy or native, got %q", actionName, library)
		}
		limit := clampInt(asInt(args["limit"], manageArtifactDefaultListLimit), 1, manageArtifactMaxListLimit)
		status := strings.ToLower(strings.TrimSpace(asString(args["status"])))
		if status != "" && status != pebblestore.SessionArtifactStatusStaging && status != pebblestore.SessionArtifactStatusReady && status != pebblestore.SessionArtifactStatusFailed && status != pebblestore.SessionArtifactStatusUnavailable {
			return "", errors.New("list status must be staging, ready, failed, or unavailable")
		}
		collectionID := strings.TrimSpace(asString(args["collection_id"]))
		query, mediaType, cursor := strings.TrimSpace(asString(args["query"])), strings.TrimSpace(asString(args["media_type"])), strings.TrimSpace(asString(args["cursor"]))
		createdAfter, afterSupplied, err := optionalArtifactInt64(args, "created_after")
		if err != nil {
			return "", err
		}
		createdBefore, beforeSupplied, err := optionalArtifactInt64(args, "created_before")
		if err != nil {
			return "", err
		}
		catalogRequested := actionName == "search" || query != "" || mediaType != "" || cursor != "" || afterSupplied || beforeSupplied
		if collectionID != "" && catalogRequested {
			return "", errors.New("manage_artifact list/search collection_id cannot be combined with cross-session discovery filters or cursor")
		}
		if collectionID != "" {
			variants, err := r.artifactAuthority.ListVariants(principal, collectionID, limit)
			if err != nil {
				return "", err
			}
			items := make([]map[string]any, 0, len(variants))
			for _, variant := range variants {
				if status != "" && variant.Status != status {
					continue
				}
				items = append(items, managedArtifactVariant(variant))
			}
			response["collection_id"], response["artifacts"], response["count"] = collectionID, items, len(items)
		} else if catalogRequested {
			page, err := r.artifactAuthority.SearchCatalog(principal, pebblestore.SessionArtifactCatalogOptions{Query: query, Status: status, MediaType: mediaType, CreatedAfter: createdAfter, CreatedBefore: createdBefore, Limit: limit, Cursor: cursor})
			if err != nil {
				return "", err
			}
			items := make([]map[string]any, 0, len(page.Items))
			for _, item := range page.Items {
				entry := map[string]any{"collection": managedArtifactCollection(item.Collection), "artifact": managedArtifactVariant(item.Variant)}
				if item.Reference != nil {
					ref := managedArtifactReferenceWithSession(item.Reference.SessionID, item.Reference.CollectionID, item.Reference.VariantID, item.Reference.EventSeq)
					entry["reference"] = ref
					entry["artifact_reference"] = ref
					if item.Reference.SessionID != principal.SessionID {
						entry["copyable_next_calls"] = []map[string]any{
							{"action": "read", "session_id": item.Reference.SessionID, "collection_id": item.Reference.CollectionID, "variant_id": item.Reference.VariantID, "event_seq": item.Reference.EventSeq},
							{"action": "import", "artifact_reference": ref},
						}
					}
				}
				items = append(items, entry)
			}
			response["artifacts"], response["count"], response["has_more"] = items, len(items), page.HasMore
			if page.NextCursor != "" {
				response["next_cursor"] = page.NextCursor
			}
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
		if err := validateArtifactRetrievalIdentity(args, "get", false); err != nil {
			return "", err
		}
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
		if err := validateArtifactRetrievalIdentity(args, "read", false); err != nil {
			return "", err
		}
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
				if errors.Is(err, artifact.ErrQuotaExceeded) {
					return "", manageArtifactReadResponseQuotaError(manageArtifactPackageEntryResponseQuotaCode)
				}
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
			if errors.Is(err, artifact.ErrQuotaExceeded) {
				return "", manageArtifactReadResponseQuotaError(manageArtifactReadResponseQuotaCode)
			}
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
		if err := validateArtifactRetrievalIdentity(args, actionName, true); err != nil {
			return "", err
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
		response["materialized"] = managedArtifactMaterialized(materialized, asBool(args["overwrite"]))
	case "materialize_batch":
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
			return "", errors.New("manage_artifact managed Designer runs cannot materialize into the workspace; batch import requires an explicit parent workspace action")
		}
		destination, err := requireArtifactArgument(args, "destination")
		if err != nil {
			return "", err
		}
		workspaceRoot := strings.TrimSpace(scope.PrimaryPath)
		if workspaceRoot == "" {
			return "", errors.New("manage_artifact materialize_batch requires a trusted workspace root")
		}
		items, refs, err := parseArtifactBatchReferences(args["references"])
		if err != nil {
			return "", err
		}
		materialized, variants, err := r.artifactAuthority.MaterializeBatchReferences(ctx, principal, items, workspaceRoot, destination, asBool(args["overwrite"]))
		if err != nil {
			return "", err
		}
		if len(materialized) != len(refs) || len(variants) != len(refs) {
			return "", errors.New("manage_artifact materialize_batch authority returned an inconsistent result count")
		}
		outputs := make([]map[string]any, 0, len(materialized))
		var totalFiles int
		var totalBytes int64
		for index, item := range materialized {
			output := managedArtifactMaterialized(item, asBool(args["overwrite"]))
			output["reference"] = managedArtifactReferenceWithSession(refs[index].SessionID, refs[index].CollectionID, refs[index].VariantID, refs[index].EventSeq)
			output["media_type"] = variants[index].MediaType
			output["digest_sha256"] = variants[index].DigestSHA256
			outputs = append(outputs, output)
			totalFiles += item.Files
			totalBytes += item.Bytes
		}
		response["destination"], response["items"], response["count"] = filepath.ToSlash(destination), outputs, len(outputs)
		response["files"], response["bytes"] = totalFiles, totalBytes
	case "publish_workspace":
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
			return "", errors.New("manage_artifact managed Designer runs cannot publish workspace sources")
		}
		variant, sourceInfo, err := r.publishWorkspaceArtifact(ctx, principal, scope, callID, requestID, args)
		if err != nil {
			return "", err
		}
		response["artifact"] = managedArtifactVariant(variant)
		response["reference"] = managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
		response["published"] = sourceInfo
		if preflight, _ := sourceInfo["trusted_animation_preflight"].(bool); preflight {
			response["trusted_animation_preflight"] = true
		}
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

func (r *Runtime) importManagedArtifact(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, requestID string, args map[string]any, response map[string]any) error {
	for key := range args {
		switch key {
		case "action", "artifact_v3_reference", "artifact_reference", "title", "message", "collection_name", "collection_description":
		default:
			return fmt.Errorf("manage_artifact import contains unsupported field %q", key)
		}
	}
	_, hasNative := args["artifact_v3_reference"]
	_, hasLegacy := args["artifact_reference"]

	if hasNative && hasLegacy {
		return errors.New("manage_artifact import accepts exactly one native or legacy reference, not both")
	}
	if !hasNative && !hasLegacy {
		return errors.New("manage_artifact import requires exactly one native or legacy reference")
	}

	if strings.TrimSpace(principal.SessionID) == "" || strings.TrimSpace(principal.SessionID) != strings.TrimSpace(scope.SessionID) {
		return errors.New("manage_artifact import caller is not authenticated for the destination session")
	}

	for _, key := range []string{"title", "message", "collection_name", "collection_description"} {
		if value, supplied := args[key]; supplied {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("manage_artifact import %s must be a string", key)
			}
		}
	}
	if hasNative {
		for _, legacyField := range []string{"collection_id", "variant_id", "event_seq", "collection_name", "collection_description", "destination_collection_id", "destination_variant_id"} {
			if _, supplied := args[legacyField]; supplied {
				return fmt.Errorf("manage_artifact import native reference cannot be combined with legacy field %q", legacyField)
			}
		}
		rawRef, ok := args["artifact_v3_reference"].(map[string]any)
		if !ok {
			return errors.New("manage_artifact import artifact_v3_reference must be an object")
		}
		reference, err := parseDirectArtifactV3RevisionInput(rawRef)
		if err != nil {
			return err
		}
		sourceSessionID, sourceArtifactID := reference.SessionID, reference.ArtifactID
		commitOID := strings.TrimPrefix(reference.RevisionRef, "revision-")
		message := strings.TrimSpace(asString(args["message"]))
		if message == "" {
			message = strings.TrimSpace(asString(args["title"]))
		}
		destArtifactID := "art-" + requestID
		destOwner := pebblestore.ArtifactV3Owner{
			AccountScopeID: principal.AccountScopeID,
			UserID:         principal.UserID,
			SessionID:      principal.SessionID,
		}
		importInput := pebblestore.ArtifactV3ImportInput{
			SourceAccountScopeID:  principal.AccountScopeID,
			SourceUserID:          principal.UserID,
			SourceSessionID:       sourceSessionID,
			SourceArtifactID:      sourceArtifactID,
			SourceCommitOID:       commitOID,
			DestinationOwner:      destOwner,
			DestinationArtifactID: destArtifactID,
			TransactionID:         requestID,
			Message:               message,
			NowUnixMs:             time.Now().UnixMilli(),
		}
		var importer ArtifactV3NativeImporter
		if r.artifactV3Importer != nil {
			importer = r.artifactV3Importer
		} else if r.artifactV3Author != nil && r.artifactV3Author.repository != nil {
			if imp, ok := r.artifactV3Author.repository.(ArtifactV3NativeImporter); ok {
				importer = imp
			}
		}
		if importer == nil {
			return errors.New("native Artifact V3 importer is not configured")
		}
		projection, err := importer.ImportArtifactV3(ctx, importInput)
		if err != nil {
			return err
		}
		repository, revision := projection.Repository, projection.Revision
		if repository == nil || revision == nil || repository.ArtifactID != destArtifactID || repository.OwnerSessionID != principal.SessionID || repository.AccountScopeID != principal.AccountScopeID || repository.UserID != principal.UserID || repository.HeadCommitOID == "" || revision.CommitOID != repository.HeadCommitOID || revision.ArtifactID != repository.ArtifactID || revision.OwnerSessionID != repository.OwnerSessionID || revision.Build.Status != "succeeded" || revision.Preview.Status != "succeeded" {
			return fmt.Errorf("%w: native import did not return a destination-owned ready revision", pebblestore.ErrArtifactV3Integrity)
		}
		ref := map[string]any{
			"session_id":   repository.OwnerSessionID,
			"artifact_id":  repository.ArtifactID,
			"revision_ref": "revision-" + repository.HeadCommitOID,
		}
		response["artifact_v3"] = map[string]any{
			"artifact_id":  repository.ArtifactID,
			"session_id":   repository.OwnerSessionID,
			"head_commit":  repository.HeadCommitOID,
			"revision_ref": "revision-" + repository.HeadCommitOID,
			"status":       "ready",
		}
		response["reference"] = ref
		response["artifact_v3_reference"] = ref
		response["copyable_next_calls"] = []map[string]any{
			{"action": "read_v3", "artifact_v3_reference": ref},
			{"action": "begin_v3", "artifact_v3_reference": ref, "revision_intent": "whole_project"},
		}
		return nil
	}

	// Legacy imports also derive every destination identity from trusted context.
	if _, supplied := args["message"]; supplied {
		return errors.New("manage_artifact import legacy reference does not accept native message")
	}
	if _, supplied := args["title"]; supplied {
		return errors.New("manage_artifact import legacy reference uses collection_name, not title")
	}
	rawRef, ok := args["artifact_reference"].(map[string]any)
	if !ok {
		return errors.New("manage_artifact import artifact_reference must be an object")
	}
	var sourceRef pebblestore.SessionArtifactSelectionReference
	{
		for key := range rawRef {
			if key != "session_id" && key != "collection_id" && key != "variant_id" && key != "event_seq" {
				return fmt.Errorf("manage_artifact import artifact_reference contains unsupported field %q", key)
			}
		}
		eventSeq, _, err := optionalArtifactInt64(rawRef, "event_seq")
		if err != nil {
			return err
		}
		sourceRef = pebblestore.SessionArtifactSelectionReference{
			SessionID:    strings.TrimSpace(asString(rawRef["session_id"])),
			CollectionID: strings.TrimSpace(asString(rawRef["collection_id"])),
			VariantID:    strings.TrimSpace(asString(rawRef["variant_id"])),
			EventSeq:     uint64(eventSeq),
		}
	}
	if sourceRef.SessionID == "" || sourceRef.CollectionID == "" || sourceRef.VariantID == "" || sourceRef.EventSeq == 0 {
		return errors.New("manage_artifact import requires complete session_id, collection_id, variant_id, and non-zero event_seq")
	}

	destCollectionID := "collection-" + requestID
	destVariantID := "variant-" + requestID
	input := artifact.ImportVariantInput{
		RequestID:             requestID,
		CollectionID:          destCollectionID,
		CollectionName:        strings.TrimSpace(asString(args["collection_name"])),
		CollectionDescription: strings.TrimSpace(asString(args["collection_description"])),
		VariantID:             destVariantID,
		Source:                sourceRef,
	}
	if r.artifactAuthority == nil {
		return errors.New("manage_artifact authority is not configured")
	}
	variant, err := r.artifactAuthority.Import(ctx, principal, input)
	if err != nil {
		return err
	}
	ref := managedArtifactReferenceWithSession(variant.SessionID, variant.CollectionID, variant.ID, variant.EventSeq)
	response["artifact"] = managedArtifactVariant(variant)
	response["reference"] = ref
	response["artifact_reference"] = ref
	response["copyable_next_calls"] = []map[string]any{
		{"action": "read", "session_id": variant.SessionID, "collection_id": variant.CollectionID, "variant_id": variant.ID, "event_seq": variant.EventSeq},
	}
	return nil
}

func manageArtifactReadResponseQuotaError(code string) error {
	return fmt.Errorf("manage_artifact read response exceeds the bounded tool quota (code=%s); this does not mean the artifact is unavailable. Use materialize with the complete exact reference for workspace use instead of bulk-reading bytes", code)
}

func managedArtifactMaterialized(value artifact.Materialized, overwrite bool) map[string]any {
	return map[string]any{
		"destination": value.Destination, "package": value.Package, "files": value.Files, "bytes": value.Bytes,
		"digest_sha256": value.DigestSHA256, "media_type": value.MediaType, "overwrite": overwrite,
	}
}

func parseArtifactBatchReferences(raw any) ([]artifact.MaterializeBatchItem, []pebblestore.SessionArtifactSelectionReference, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > manageArtifactMaxBatchItems {
		return nil, nil, fmt.Errorf("manage_artifact materialize_batch references must contain 1 to %d exact ready references", manageArtifactMaxBatchItems)
	}
	items := make([]artifact.MaterializeBatchItem, 0, len(values))
	refs := make([]pebblestore.SessionArtifactSelectionReference, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, rawValue := range values {
		value, ok := rawValue.(map[string]any)
		if !ok {
			return nil, nil, fmt.Errorf("manage_artifact materialize_batch reference %d must be an object", index)
		}
		for key := range value {
			if key != "session_id" && key != "collection_id" && key != "variant_id" && key != "event_seq" {
				return nil, nil, fmt.Errorf("manage_artifact materialize_batch reference %d contains unsupported field %q", index, key)
			}
		}
		ref, explicit, err := parseArtifactReadReference(value, strings.TrimSpace(asString(value["variant_id"])))
		if err != nil || !explicit {
			if err == nil {
				err = errors.New("exact ready reference is required")
			}
			return nil, nil, fmt.Errorf("manage_artifact materialize_batch reference %d: %w", index, err)
		}
		identity := strings.Join([]string{ref.SessionID, ref.CollectionID, ref.VariantID, fmt.Sprint(ref.EventSeq)}, "\x00")
		if _, exists := seen[identity]; exists {
			return nil, nil, errors.New("manage_artifact materialize_batch contains duplicate exact references")
		}
		seen[identity] = struct{}{}
		items = append(items, artifact.MaterializeBatchItem{Reference: ref})
		refs = append(refs, ref)
	}
	return items, refs, nil
}

func (r *Runtime) publishWorkspaceArtifact(ctx context.Context, principal artifact.Principal, scope WorkspaceScope, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, map[string]any, error) {
	for key := range args {
		switch key {
		case "action", "source", "title", "collection_id", "collection_name", "collection_description", "filename", "media_type", "presentation", "output_requirements", "animation_profile", "source_session_id", "source_collection_id", "source_variant_id", "source_event_seq":
		default:
			return pebblestore.SessionArtifactVariant{}, nil, fmt.Errorf("manage_artifact publish_workspace contains unsupported field %q", key)
		}
	}
	workspaceRoot := strings.TrimSpace(scope.PrimaryPath)
	if workspaceRoot == "" {
		return pebblestore.SessionArtifactVariant{}, nil, errors.New("manage_artifact publish_workspace requires a trusted workspace root")
	}
	source, err := requireArtifactArgument(args, "source")
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	absoluteSource, sourceInfo, packageSource, err := validateWorkspacePublishSource(ctx, workspaceRoot, source)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}

	collectionID := strings.TrimSpace(asString(args["collection_id"]))
	generatedCollection := collectionID == ""
	if generatedCollection {
		collectionID = managedArtifactOpaqueID("collection", principal.SessionID, callID)
	}
	variantID := managedArtifactOpaqueID("variant", principal.SessionID, callID)
	filename := strings.TrimSpace(asString(args["filename"]))
	mediaType := canonicalArtifactMediaType(asString(args["media_type"]))
	if packageSource {
		if filename == "" {
			filename = filepath.Base(absoluteSource) + ".zip"
		}
		mediaType = "application/zip"
	} else {
		if filename == "" {
			filename = filepath.Base(absoluteSource)
		}
		if mediaType == "" {
			mediaType = canonicalArtifactMediaType(mime.TypeByExtension(filepath.Ext(filename)))
		}
		if mediaType == "" {
			file, openErr := os.Open(absoluteSource)
			if openErr != nil {
				return pebblestore.SessionArtifactVariant{}, nil, openErr
			}
			buffer := make([]byte, 512)
			count, readErr := file.Read(buffer)
			closeErr := file.Close()
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return pebblestore.SessionArtifactVariant{}, nil, readErr
			}
			if closeErr != nil {
				return pebblestore.SessionArtifactVariant{}, nil, closeErr
			}
			mediaType = canonicalArtifactMediaType(http.DetectContentType(buffer[:count]))
		}
	}
	presentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	if presentation.Kind == "" {
		if packageSource {
			presentation.Kind = "package"
		} else if strings.HasPrefix(mediaType, "text/") || mediaType == "application/json" || mediaType == "application/xml" {
			presentation.Kind, presentation.Previewable = "text", true
		} else if strings.HasPrefix(mediaType, "image/") {
			presentation.Kind, presentation.Previewable = "image", true
		} else if strings.HasPrefix(mediaType, "video/") {
			presentation.Kind, presentation.Previewable = "video", true
		} else {
			presentation.Kind = "download"
		}
	}
	var requirements *pebblestore.SessionArtifactOutputRequirements
	if raw, exists := args["output_requirements"]; exists {
		requirements, err = artifact.ParseOutputRequirements(raw)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, err
		}
	}
	if err := enforceArtifactPresentationRequirements(&presentation, requirements); err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	sourceSessionID, sourceCollectionID := strings.TrimSpace(asString(args["source_session_id"])), strings.TrimSpace(asString(args["source_collection_id"]))
	sourceVariantID, sourceEventSeq := strings.TrimSpace(asString(args["source_variant_id"])), asUint64(args["source_event_seq"])
	var inheritedAnimationProfile *pebblestore.SessionArtifactAnimationProfile
	if raw, exists := args["animation_profile"]; exists {
		inheritedAnimationProfile, err = artifact.ParseAnimationProfile(raw)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, err
		}
	}
	if sourceSessionID != "" || sourceCollectionID != "" || sourceVariantID != "" || sourceEventSeq != 0 {
		if sourceSessionID == "" || sourceCollectionID == "" || sourceVariantID == "" || sourceEventSeq == 0 {
			return pebblestore.SessionArtifactVariant{}, nil, errors.New("manage_artifact publish_workspace source lineage requires all four fields of a complete exact source reference")
		}
		sourceVariant, sourceErr := r.artifactAuthority.GetReference(principal, pebblestore.SessionArtifactSelectionReference{SessionID: sourceSessionID, CollectionID: sourceCollectionID, VariantID: sourceVariantID, EventSeq: sourceEventSeq})
		if sourceErr != nil || sourceVariant.Status != pebblestore.SessionArtifactStatusReady {
			return pebblestore.SessionArtifactVariant{}, nil, errors.New("manage_artifact publish_workspace exact source reference could not be authenticated")
		}
		if sourceVariant.AnimationProfile != nil && sourceVariant.AnimationProfile.ProfileID != "final_render" {
			canonicalProfile, profileErr := artifact.ResolveAnimationProfile(&artifact.AnimationProfileInput{Profile: sourceVariant.AnimationProfile.ProfileID})
			if profileErr != nil || canonicalProfile == nil || *canonicalProfile != *sourceVariant.AnimationProfile {
				return pebblestore.SessionArtifactVariant{}, nil, errors.New("manage_artifact publish_workspace exact source carries an incompatible animation profile snapshot")
			}
			if inheritedAnimationProfile != nil && *inheritedAnimationProfile != *canonicalProfile {
				return pebblestore.SessionArtifactVariant{}, nil, errors.New("manage_artifact publish_workspace animation_profile conflicts with the exact source snapshot")
			}
			inheritedAnimationProfile = cloneArtifactAnimationProfile(canonicalProfile)
		}
	}
	if err := validateArtifactAnimationMedia(inheritedAnimationProfile, packageSource, filename, mediaType); err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	var parts []pebblestore.SessionArtifactPart
	var animationBody []byte
	var animationEntries []artifact.PackageEntry
	if packageSource && managedHTMLAnimationProfile(inheritedAnimationProfile) {
		animationEntries, err = readWorkspaceAnimationPackage(ctx, absoluteSource)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, err
		}
	}
	if !packageSource && mediaType == "text/html" {
		animationBody, err = os.ReadFile(absoluteSource)
		if err != nil {
			return pebblestore.SessionArtifactVariant{}, nil, err
		}
		parts = deriveArtifactHTMLParts(animationBody, mediaType)
	}
	if packageSource && len(animationEntries) != 0 {
		for _, entry := range animationEntries {
			if entry.Name == "index.html" {
				parts = deriveArtifactHTMLParts(entry.Data, "text/html")
				break
			}
		}
	}
	title := strings.TrimSpace(asString(args["title"]))
	collectionName := strings.TrimSpace(asString(args["collection_name"]))
	if collectionName == "" && title != "" {
		collectionName = title
	}
	if presentation.Label == "" && title != "" {
		presentation.Label = title
	}
	create := artifact.CreateInput{
		RequestID: requestID, CollectionID: collectionID, CollectionName: collectionName, CollectionDescription: strings.TrimSpace(asString(args["collection_description"])),
		VariantID: variantID, Filename: filename, MediaType: mediaType, Presentation: presentation, OutputRequirements: requirements, AnimationProfile: inheritedAnimationProfile, Parts: parts, AutoAccept: true,
		SourceSessionID: sourceSessionID, SourceCollectionID: sourceCollectionID, SourceVariantID: sourceVariantID, SourceEventSeq: sourceEventSeq,
	}
	if generatedCollection && create.CollectionName == "" {
		create.CollectionName = "Workspace publication"
	}
	if !generatedCollection {
		create.CollectionName, create.CollectionDescription = "", ""
	}
	create.Body = animationBody
	animationPreflight, err := r.reserveAndPreflightManagedAnimation(ctx, principal, create, animationEntries, packageSource)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	gatedAnimation := animationPreflight != nil
	var variant pebblestore.SessionArtifactVariant
	if gatedAnimation && packageSource {
		variant, err = r.artifactAuthority.CreatePackage(ctx, principal, artifact.CreatePackageInput{CreateInput: create, Entries: animationEntries})
	} else if gatedAnimation {
		variant, err = r.artifactAuthority.Create(ctx, principal, create)
	} else {
		variant, err = r.artifactAuthority.PublishWorkspace(ctx, principal, artifact.CreateFileInput{CreateInput: create, SourcePath: absoluteSource, Package: packageSource})
	}
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	if gatedAnimation && variant.Status != pebblestore.SessionArtifactStatusReady {
		return pebblestore.SessionArtifactVariant{}, nil, animationError("animation_publish_failed", "profiled workspace HTML animation did not finalize as a ready artifact after trusted preflight")
	}
	published := map[string]any{"source": filepath.ToSlash(source), "package": packageSource, "files": sourceInfo.files, "bytes": sourceInfo.bytes, "digest_sha256": variant.DigestSHA256, "media_type": variant.MediaType}
	if gatedAnimation {
		inspectionRefs, inspectionErr := r.publishManagedAnimationInspectionFrames(ctx, principal, variant, animationPreflight)
		if inspectionErr != nil {
			return pebblestore.SessionArtifactVariant{}, nil, inspectionErr
		}
		published["trusted_animation_preflight"] = true
		published["animation_inspection_references"] = inspectionRefs
	}
	return variant, published, nil
}

type workspacePublishSourceInfo struct {
	files int
	bytes int64
}

func readWorkspaceAnimationPackage(ctx context.Context, root string) ([]artifact.PackageEntry, error) {
	entries := make([]artifact.PackageEntry, 0, manageArtifactMaxPackageFiles)
	var total int
	err := filepath.WalkDir(root, func(candidate string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if candidate == root || entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			return errors.New("manage_artifact publish_workspace animation package contains an unsafe entry")
		}
		relative, err := filepath.Rel(root, candidate)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(relative)
		if pathClean(name) != name || len(entries) >= manageArtifactMaxPackageFiles || info.Size() > manageArtifactMaxPackageBytes-int64(total) {
			return artifact.ErrQuotaExceeded
		}
		body, err := os.ReadFile(candidate)
		if err != nil {
			return err
		}
		total += len(body)
		entries = append(entries, artifact.PackageEntry{Name: name, Data: body})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(entries) == 0 {
		return nil, errors.New("manage_artifact publish_workspace animation package is empty")
	}
	return entries, nil
}

func validateWorkspacePublishSource(ctx context.Context, workspaceRoot, source string) (string, workspacePublishSourceInfo, bool, error) {
	root, _, err := validateWorkspaceRelativeSource(workspaceRoot, source)
	if err != nil {
		return "", workspacePublishSourceInfo{}, false, err
	}
	absolute := filepath.Join(root, filepath.FromSlash(source))
	info, err := os.Lstat(absolute)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (!info.Mode().IsRegular() && !info.IsDir()) {
		return "", workspacePublishSourceInfo{}, false, errors.New("manage_artifact publish_workspace source must be a regular non-symlink file or directory")
	}
	result := workspacePublishSourceInfo{}
	ignored, err := workspacePathIgnored(ctx, root, absolute)
	if err != nil {
		return "", result, false, err
	}
	if ignored {
		return "", result, false, errors.New("manage_artifact publish_workspace rejects ignored private workspace state")
	}
	if info.Mode().IsRegular() {
		result.files, result.bytes = 1, info.Size()
		return absolute, result, false, nil
	}
	err = filepath.WalkDir(absolute, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == absolute {
			return nil
		}
		entryInfo, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 || (!entry.IsDir() && !entryInfo.Mode().IsRegular()) {
			return errors.New("manage_artifact publish_workspace package contains a symlink or special file")
		}
		ignored, err := workspacePathIgnored(ctx, root, path)
		if err != nil {
			return err
		}
		if ignored {
			return errors.New("manage_artifact publish_workspace rejects ignored private workspace state")
		}
		if entryInfo.Mode().IsRegular() {
			result.files++
			result.bytes += entryInfo.Size()
		}
		return nil
	})
	if err != nil {
		return "", workspacePublishSourceInfo{}, false, err
	}
	if result.files == 0 || result.files > artifact.DefaultMaxPackageFiles || result.bytes > artifact.DefaultMaxPackageBytes {
		return "", workspacePublishSourceInfo{}, false, artifact.ErrQuotaExceeded
	}
	return absolute, result, true, nil
}

func validateWorkspaceRelativeSource(workspaceRoot, source string) (string, string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	absoluteRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", "", err
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return "", "", errors.New("manage_artifact publish_workspace trusted workspace root is unsafe")
	}
	source = strings.TrimSpace(source)
	if source == "" || filepath.IsAbs(source) || strings.Contains(source, "\\") || filepath.Clean(source) != source || source == "." || source == ".." || strings.HasPrefix(source, ".."+string(filepath.Separator)) {
		return "", "", errors.New("manage_artifact publish_workspace source must be a canonical workspace-relative path")
	}
	current := absoluteRoot
	parts := strings.Split(filepath.FromSlash(source), string(filepath.Separator))
	for index, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", errors.New("manage_artifact publish_workspace source must be a canonical workspace-relative path")
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", "", err
		}
		if info.Mode()&os.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) {
			return "", "", errors.New("manage_artifact publish_workspace source path contains a symlink or non-directory")
		}
	}
	return absoluteRoot, source, nil
}

func workspacePathIgnored(ctx context.Context, workspaceRoot, absolutePath string) (bool, error) {
	relative, err := filepath.Rel(workspaceRoot, absolutePath)
	if err != nil || relative == "." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return false, errors.New("manage_artifact publish_workspace source escapes its trusted workspace")
	}
	relativeSlash := filepath.ToSlash(relative)
	base := strings.ToLower(filepath.Base(relative))
	if base == ".env" || strings.HasPrefix(base, ".env.") || base == ".git" || strings.HasPrefix(relativeSlash, ".git/") {
		return true, nil
	}
	if ignoredByRootGitignore(workspaceRoot, relativeSlash) {
		return true, nil
	}
	cmd := exec.CommandContext(ctx, "git", "-C", workspaceRoot, "check-ignore", "--no-index", "--quiet", "--", filepath.ToSlash(relative))
	err = cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		switch exitErr.ExitCode() {
		case 1, 128:
			return false, nil
		}
	}
	if errors.Is(err, exec.ErrNotFound) {
		return false, nil
	}
	return false, fmt.Errorf("check workspace publication ignore policy: %w", err)
}

func ignoredByRootGitignore(workspaceRoot, relative string) bool {
	data, err := os.ReadFile(filepath.Join(workspaceRoot, ".gitignore"))
	if err != nil || len(data) > 1<<20 {
		return false
	}
	ignored := false
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := strings.HasPrefix(line, "!")
		if negated {
			line = strings.TrimPrefix(line, "!")
		}
		anchored := strings.HasPrefix(filepath.ToSlash(line), "/")
		line = strings.TrimPrefix(filepath.ToSlash(line), "/")
		if line == "" {
			continue
		}
		directory := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		matched, _ := path.Match(line, relative)
		if !anchored && !strings.Contains(line, "/") {
			for _, part := range strings.Split(relative, "/") {
				if partMatch, _ := path.Match(line, part); partMatch {
					matched = true
					break
				}
			}
		}
		if directory && (relative == line || strings.HasPrefix(relative, line+"/")) {
			matched = true
		}
		if matched {
			ignored = !negated
		}
	}
	return ignored
}

func (r *Runtime) managedImageCapabilities(accountScopeID string) (imagegen.ManagedImageCapabilities, error) {
	if r == nil || r.imageGeneration == nil {
		return imagegen.ManagedImageCapabilities{}, errors.New("manage_artifact image generation is not configured")
	}
	if r.uiSettings == nil {
		return imagegen.ManagedImageCapabilities{}, errors.New("manage_artifact image model settings are not configured")
	}
	ui, err := r.uiSettings.GetForAccount(strings.TrimSpace(accountScopeID))
	if err != nil {
		return imagegen.ManagedImageCapabilities{}, fmt.Errorf("resolve configured image model: %w", err)
	}
	selectionID := strings.TrimSpace(ui.Tools.Image.DefaultModel)
	if selectionID == "" {
		selectionID = imagegen.DefaultModelSelectionID
	}
	return r.imageGeneration.ManagedImageCapabilities(selectionID)
}

func (r *Runtime) managedAudioCapabilities(accountScopeID string) (audiogen.ManagedAudioCapabilities, error) {
	return r.managedAudioCapabilitiesWithModel(accountScopeID, "")
}

func (r *Runtime) managedAudioCapabilitiesWithModel(accountScopeID, modelOverride string) (audiogen.ManagedAudioCapabilities, error) {
	if r == nil || r.audioGeneration == nil {
		return audiogen.ManagedAudioCapabilities{}, errors.New("manage_artifact audio generation is not configured")
	}
	modelID := strings.TrimSpace(modelOverride)
	if modelID == "" && r.uiSettings != nil && strings.TrimSpace(accountScopeID) != "" {
		if ui, err := r.uiSettings.GetForAccount(strings.TrimSpace(accountScopeID)); err == nil {
			modelID = strings.TrimSpace(ui.Tools.Audio.DefaultModel)
		}
	}
	if modelID == "" {
		modelID = audiogen.DefaultAudioSongModel
	}
	return r.audioGeneration.ManagedAudioCapabilities(modelID)
}

func (r *Runtime) generateManagedImageArtifact(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	for key := range args {
		switch key {
		case "action", "prompt", "title", "label", "image_settings", "capability_token", "collection_id", "collection_name", "collection_description", "variant_id", "filename", "presentation", "output_requirements", "source_session_id", "source_collection_id", "source_variant_id", "source_event_seq":
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
		collectionName = strings.TrimSpace(asString(args["title"]))
	}
	if collectionName == "" {
		collectionName = strings.TrimSpace(asString(args["label"]))
	}
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
			// An existing collection owns its durable metadata. Appending a generated
			// variant must not send the standalone generation defaults as replacement
			// metadata; the artifact mutation boundary preserves the stored values.
			collectionName, collectionDescription = "", ""
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

	var sourceRef *pebblestore.SessionArtifactSelectionReference
	var source *imagegen.ManagedImageSource
	sourceFields := 0
	for _, key := range []string{"source_session_id", "source_collection_id", "source_variant_id", "source_event_seq"} {
		if _, supplied := args[key]; supplied {
			sourceFields++
		}
	}
	if sourceFields != 0 {
		if sourceFields != 4 {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact image remix requires source_session_id, source_collection_id, source_variant_id, and source_event_seq from the same exact ready reference")
		}
		sourceEventSeq := asUint64(args["source_event_seq"])
		ref := pebblestore.SessionArtifactSelectionReference{
			SessionID: strings.TrimSpace(asString(args["source_session_id"])), CollectionID: strings.TrimSpace(asString(args["source_collection_id"])),
			VariantID: strings.TrimSpace(asString(args["source_variant_id"])), EventSeq: sourceEventSeq,
		}
		if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || sourceEventSeq == 0 {
			return pebblestore.SessionArtifactVariant{}, errors.New("manage_artifact image remix requires non-empty source_session_id, source_collection_id, source_variant_id, and source_event_seq")
		}
		body, variant, readErr := r.artifactAuthority.ReadReference(ctx, principal, ref, manageArtifactMaxImageReadBytes)
		if readErr != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve image remix source: %w", readErr)
		}
		if len(body) == 0 || len(body) > manageArtifactMaxImageReadBytes || !managedArtifactImageMediaType(variant.MediaType) || !managedArtifactImageDataMatches(variant.MediaType, body) {
			return pebblestore.SessionArtifactVariant{}, errors.New("image remix source is empty, oversized, or not a supported ready image")
		}
		sourceRef = &ref
		source = &imagegen.ManagedImageSource{Bytes: append([]byte(nil), body...), MediaType: canonicalArtifactMediaType(variant.MediaType)}
	}

	ui, err := r.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, fmt.Errorf("resolve configured image model: %w", err)
	}
	selectionID := strings.TrimSpace(ui.Tools.Image.DefaultModel)
	if selectionID == "" {
		selectionID = imagegen.DefaultModelSelectionID
	}
	capabilityToken := strings.TrimSpace(asString(args["capability_token"]))
	if capabilityToken == "" {
		if capabilities, err := r.managedImageCapabilities(principal.AccountScopeID); err == nil && capabilities.CapabilityToken != "" {
			capabilityToken = capabilities.CapabilityToken
		}
	}
	generated, err := r.imageGeneration.GenerateManagedImage(identity.ContextWithPrincipal(ctx, scope.Principal), imagegen.ManagedGenerateRequest{
		SelectionID: selectionID, Prompt: prompt, Size: size, Settings: settings,
		CapabilityToken: capabilityToken, Principal: scope.Principal, Source: source,
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
	if strings.TrimSpace(presentation.Label) == "" {
		presentation.Label = strings.TrimSpace(asString(args["title"]))
	}
	if strings.TrimSpace(presentation.Label) == "" {
		presentation.Label = strings.TrimSpace(asString(args["label"]))
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
	create := artifact.CreateInput{
		RequestID: requestID, CollectionID: collectionID, CollectionName: collectionName, CollectionDescription: collectionDescription,
		VariantID: variantID, Filename: filename, MediaType: canonicalArtifactMediaType(generated.MediaType), Presentation: presentation,
		OutputRequirements: requirements, Body: append([]byte(nil), generated.Bytes...), AutoAccept: !managedDestination,
	}
	if managedDestination {
		run, _ := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext)
		create.ArtifactStepID, create.CandidateIndex, create.AutoAccept = strings.TrimSpace(run.ArtifactStepID), run.CandidateIndex, run.AutoAccept
	}
	if sourceRef != nil {
		create.SourceSessionID, create.SourceCollectionID, create.SourceVariantID, create.SourceEventSeq = sourceRef.SessionID, sourceRef.CollectionID, sourceRef.VariantID, sourceRef.EventSeq
	}
	var estimate pebblestore.MediaCostEstimate
	imageProvider := "codex"
	imageModel := "gpt-image-1"
	if selectionID != "" && selectionID != imagegen.DefaultModelSelectionID && selectionID != "codex-image-gen" {
		imageProvider = "google"
		imageModel = selectionID
	}
	if r.sessions != nil {
		requestedAspectRatio := ""
		if ar, ok := settings["aspect_ratio"].(string); ok {
			requestedAspectRatio = ar
		}
		requestedImageSize := size
		if is, ok := settings["image_size"].(string); ok && is != "" {
			requestedImageSize = is
		}
		effectiveImageResolution := fmt.Sprintf("%dx%d", config.Width, config.Height)
		if requestedImageSize != "" && (config.Width < 1 || config.Height < 1) {
			effectiveImageResolution = requestedImageSize
		}

		estimate = r.sessions.EstimateMediaCostWithOptions(pebblestore.MediaCostEstimateOptions{
			Provider:     imageProvider,
			Model:        imageModel,
			Kind:         "image",
			Count:        1,
			OutputTokens: generated.OutputTokens,
			Resolution:   effectiveImageResolution,
			ImageSize:    requestedImageSize,
			AspectRatio:  requestedAspectRatio,
			IsIteration:  sourceRef != nil,
		})
	}

	mediaID := variantID
	if mediaID == "" {
		mediaID = fmt.Sprintf("media_%s_%s", callID, requestID)
	}
	mediaRec := pebblestore.SessionMediaUsageRecord{
		ID:              mediaID,
		SessionID:       principal.SessionID,
		AccountScopeID:  principal.AccountScopeID,
		UserID:          principal.UserID,
		MediaType:       canonicalArtifactMediaType(generated.MediaType),
		Kind:            "image",
		Provider:        imageProvider,
		Model:           imageModel,
		Filename:        filename,
		Label:           presentation.Label,
		Size:            int64(len(generated.Bytes)),
		CostUSD:         estimate.CostUSD,
		PriceStatus:     estimate.PriceStatus,
		PricingSummary:  estimate.PricingSummary,
		SnapshotID:      estimate.SnapshotID,
		SnapshotVersion: estimate.SnapshotVersion,
		CreatedAt:       time.Now().UnixMilli(),
	}
	if r.sessions != nil {
		if recErr := r.sessions.RecordMediaUsage(mediaRec); recErr != nil {
			return pebblestore.SessionArtifactVariant{}, fmt.Errorf("record image media usage: %w", recErr)
		}
	}

	published, err := r.artifactAuthority.Create(ctx, principal, create)
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, err
	}
	return published, nil
}

type managedVideoArtifactResult struct {
	LastVariant           pebblestore.SessionArtifactVariant
	Variants              []map[string]any
	References            []map[string]any
	Title                 string
	Prompt                string
	Model                 string
	Provider              string
	AspectRatio           string
	Resolution            string
	DurationSeconds       int
	CostPerVideoUSD       float64
	TotalEstimatedCostUSD float64
	PriceStatus           string
	PricingSummary        string
	SnapshotID            string
	SnapshotVersion       string
	HasImageInput         bool `json:"has_image_input,omitempty"`
}

type managedAudioArtifactResult struct {
	LastVariant           pebblestore.SessionArtifactVariant
	Variants              []map[string]any
	References            []map[string]any
	Title                 string
	Prompt                string
	Prompts               []string
	Model                 string
	Provider              string
	DurationSeconds       int
	DurationMs            int
	Lyrics                string
	CostPerAudioUSD       float64
	TotalEstimatedCostUSD float64
	PricingSummary        string
	HasImageInput         bool `json:"has_image_input,omitempty"`
	Metadata              audiogen.AudioMetadata
}

func deriveVideoTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "Generated video"
	}
	clean := prompt
	for _, prefix := range []string{
		"generate a video of ", "generate a video showing ", "generate video of ",
		"a video of ", "video of ", "a clip of ", "clip of ", "show a video of ",
		"create a video of ", "please generate a video of ",
	} {
		if strings.HasPrefix(strings.ToLower(clean), prefix) {
			clean = clean[len(prefix):]
			break
		}
	}
	clean = strings.TrimLeft(clean, "\"'`# ")
	for _, sep := range []string{". ", "! ", "? ", "; ", "\n"} {
		if idx := strings.Index(clean, sep); idx > 0 {
			clean = clean[:idx]
			break
		}
	}
	runes := []rune(clean)
	if len(runes) > 50 {
		cut := string(runes[:50])
		if lastSpace := strings.LastIndex(cut, " "); lastSpace > 20 {
			cut = cut[:lastSpace]
		}
		clean = strings.TrimSpace(cut)
	}
	clean = strings.TrimRight(clean, ".,;:-! ")
	if clean == "" {
		return "Generated video"
	}
	r := []rune(clean)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func deriveAudioTitle(prompt string) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return "Generated audio"
	}
	clean := prompt
	for _, prefix := range []string{
		"generate an audio clip of ", "generate audio clip of ", "generate an audio of ",
		"generate audio of ", "generate music for ", "generate music of ",
		"create an audio clip of ", "create audio clip of ", "create music for ",
		"create a song about ", "generate a song about ", "a song about ",
		"audio clip of ", "music for ", "sound clip of ", "clip of ",
		"please generate audio of ", "please create music for ",
	} {
		if strings.HasPrefix(strings.ToLower(clean), prefix) {
			clean = clean[len(prefix):]
			break
		}
	}
	clean = strings.TrimLeft(clean, "\"'`# ")
	for _, sep := range []string{". ", "! ", "? ", "; ", "\n"} {
		if idx := strings.Index(clean, sep); idx > 0 {
			clean = clean[:idx]
			break
		}
	}
	runes := []rune(clean)
	if len(runes) > 50 {
		cut := string(runes[:50])
		if lastSpace := strings.LastIndex(cut, " "); lastSpace > 20 {
			cut = cut[:lastSpace]
		}
		clean = strings.TrimSpace(cut)
	}
	clean = strings.TrimRight(clean, ".,;:-! ")
	if clean == "" {
		return "Generated audio"
	}
	r := []rune(clean)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func ArtifactHelpText(topic string) string {
	return artifactHelpText(topic)
}

func artifactHelpText(topic string) string {
	switch topic {
	case "animation", "motion":
		return `Artifact Animation & Motion Contract:
- Direct native HTML accepts animation_profile motion_ui or spatial_3d (pinned offline Three.js; import from 'three' in module script).
- Requires exactly one #swarm-animation-manifest (application/json, version swarm.animation/v1, duration_ms >= 100, fps 1-60, ceil(duration_ms*fps/1000) <= 36000 at 1920x1080) matching ready(). Unknown manifest fields are strictly disallowed.
- Requires at least one semantic region with an id attribute on <main id="...">. Semantic regions with data-swarm-capture-ui are excluded from derived Parts; Parts must target output regions only.
- Expose globalThis.__SWARM_ANIMATION_V1__ with version swarm.animation/v1, ready() returning { duration_ms, fps } matching the manifest, and seek(ms) returning { time_ms: ms }.
- seek must pause all rAF/timers and deterministically render the exact timestamp.
- To use the animation in Video Studio, call manage_video action="convert_artifact_v3" with project_id, base_revision_id, artifact_v3_session_id, artifact_v3_artifact_id, and artifact_v3_revision_ref.
- data-swarm-capture-ui are excluded from derived Parts
- explicit Parts must target output regions only
- remaining meaningful output regions still required
- explicit kind=temporal Parts with stable output-region IDs
- start_ms/end_ms within the manifest duration
- Never edit the server-owned swarm-artifact.json
- retain the exact draft and repair source through its authorized handle`
	case "video", "veo":
		return `Artifact AI Video (Veo 3.1) Contract:
- action="generate_video": prompt, duration_seconds (4, 6, 8), aspect_ratio (16:9, 9:16), resolution (720p, 1080p, 4k).
- Image-to-video: pass image or image_path alongside prompt.
- Video iteration/remix: pass source_session_id, source_collection_id, source_variant_id, source_event_seq with delta prompt.
- For stunning video generation, write prompts with rich visual cinematography and synchronized sound direction (video models like Veo 3.1 natively generate audio and video in one pass; leaving sound unprompted causes hallucinated audio): specify concrete camera dynamics (smooth orbit, tracking dolly, sweeping crane, macro push-in), explicit multi-stage transitions and transformations across the 8-second timeline (e.g. geometric dimension shift, 4D folding, radiant particle murmuration, 3D mesh crystallization, raster ASCII disintegration), lighting dynamics (volumetric rays, caustic refractions, bioluminescence, specular reflections, depth of field), material textures (frosted glass, brushed chrome, optical fiber lattices, luminous energy nodes), and explicit tempo/pacing.
- Always direct the soundscape: provide concrete action-tied sound effects (e.g. SFX: heavy metallic latch engaging, boots crunching on gravel), ambient acoustic scale and room tone (e.g. Ambient noise: deep subterranean rumble, wet cavern drips), and musical score mood (e.g. Music: dark ambient synth with driving sub-bass or Music: none).
- Never use quotation marks in video prompts unless spoken dialogue is explicitly requested, as models interpret quoted text as lip-synced speech. When silent video or text-free output is requested, specify both visual and acoustic constraints: "purely visual, zero text, no titles, no subtitles, no words, no letters, no watermarks" and "Ambient noise: near-total silence, dead room tone, no background music, no dialogue".
- For multi-part video generation, chaining, and audio continuity across multiple clips:
  (1) Generate Part 1 with manage_artifact action=generate_video.
  (2) Generate consecutive parts by passing chain_from with the previous video's exact ready reference (session_id, collection_id, variant_id, event_seq) alongside prompt: the backend automatically extracts the exact last keyframe of the preceding clip and feeds it to Veo 3.1 image-to-video, guaranteeing seamless visual seam continuity.
  (3) Generate a continuous soundtrack matching the total combined duration (e.g. 16s, 24s) with manage_artifact action=generate_audio duration_seconds=N.
  (4) Combine the parts into a single seamless master deliverable with manage_artifact action=chain_video, passing videos array of video references, optional audio reference, and audio_mode ('mix_ducked' to layer continuous music at 100% with ducked native Foley sound effects at 35%, or 'override' for pure music replacement).
  (5) Use manage_artifact action=extract_video_frame with frame='last' or 'first' to sample keyframes.
- action="generate_video_story": end-to-end multi-scene story with automatic Lyria soundtrack and Foley ducking in one atomic operation.`
	case "audio", "lyria":
		return `Artifact AI Audio (Google Lyria) Contract:
- Before generating audio, call manage_artifact action="audio_capabilities" to inspect the configured audio model, duration limits, and capability_token.
- When the user asks to generate audio, sound clips, music, or sound effects, use manage_artifact action=generate_audio. You can specify model (e.g. lyria-3.5, lyria-3-clip-preview, lyria-3-pro-preview), or omit it to resolve your account-configured audio model (Google Lyria).
- The AI can generate multiple sound clips or audio variations in ONE tool call: provide an array of descriptive style/mood prompts via prompts: ["...", "..."] (up to 8 clips, e.g. prompts: ["Upbeat funk groove with slapping bass", "Ambient calm piano with rain sounds", "High-energy rock guitar solo"]), or specify count: N (1 to 8) to generate multiple variations from a single prompt.
- Each generated sound clip is published as a distinct variant in the artifact collection with its own title and description, and Desktop renders an interactive Sound Clips selector so users can preview and play each clip directly.
- Specify duration_seconds (respecting model limits from audio_capabilities, e.g. 5-30s for clip preview, 30-300s for full songs) and optional image or image_path for multimodal audio inspiration.
- To iterate, remix, or continue an existing audio artifact, provide source_session_id, source_collection_id, source_variant_id, and source_event_seq from its exact ready reference alongside your delta prompt.
- Video Studio Ingestion: To use a generated audio artifact as soundtrack audio in Video Studio timeline projects, call manage_video action="import_audio_artifact" with its exact reference ({session_id, collection_id, variant_id, event_seq}). This persists an authenticated AudioSourceRecord and returns the exact audio_source object ({ref, name, mime_type, size_bytes, source_fingerprint, fingerprint_version: "v1"}) ready for create_edit_proposal operations: [{type: "add_clip", clip: {source_kind: "source_audio", audio_source: ...}}] or create_project initial_timeline.`
	case "narration":
		return `Narration Plan Draft Contract:
- For narration-plan drafts, use manage_artifact action=create with the built-in narration_plan object and its complete schema example instead of inventing HTML, selectors, or Parts.
- Supply persistent scene IDs and plain narration/title text with optional visual_direction and music_direction.
- Complete reusable example:
` + artifactNarrationCreateExample + `
- The server renders escaped HTML with separate narration, scene-context, visual and music Parts. Omit content/parts/entries/initial_parts.
- Later changes use the exact native reference, read_v3 and revise_v3 with the selected target_part_ids; do not recreate the plan as a separate artifact.`
	case "workflow", "lifecycle":
		return `Artifact V3 Lifecycle & Workflow Contract:
- For a retained failed draft from an earlier terminal run, use resume_v3 with resume_draft (session_id, artifact_id, expected_sequence, expected_projection_seq, expected_head); obtain exact CAS values using draft_status_v3 with the artifact_id already in chat context (no Git revision or raw grant needed). expected_head is required and is empty only before the first publication. This renews the producer grant, invalidates the old handle and requires rebuilding; active producers cannot be rebound.
- For incremental native artifact edits, call begin_v3 with artifact_v3_reference and target_part_ids, then author_v3 with the returned exact draft_handle and operation object using inspect_context/list_files/read_file/edit_file/diff/build_preview/finish_turn. A fixing create/revise result retains source and current gate diagnostics: repair that handle, never allocate another create. Read decoded Content before literal edits; preserve stable Parts and unrelated bytes. Stop unchanged retries and report expiry/authorization conflicts honestly. Finish only after a current ready build; candidate selection remains explicit.
- Before generating or remixing an image, call action=image_capabilities to read the configured model's current snapshot-backed options and capability_token, then pass only listed options plus that token to action=generate_image. A selected ready image is a reusable exact source for repeated edits: on every remix, copy its source_session_id, source_collection_id, source_variant_id, and source_event_seq together with the new edit request; the authenticated artifact authority supplies bounded source bytes directly to a supported provider, so never replace the source with a preview/download or re-prompt from scratch.
- export_html_stills accepts one complete exact ready text/html or canonical HTML-package reference containing the swarm.capture/v1 manifest/runtime contract, optionally selects declared state_ids, and returns managed 1920x1080 image/png references in manifest order; when the same HTML also declares swarm.storyboard/v1, the response includes a storyboard_handoff that binds every stable section to its capture state, filming requirements, production state, exact source, and exported PNG. For pre-production, pass that complete handoff to manage_video import_storyboard so Video Studio receives the pending storyboard in the same workflow; do not stop after export or manually rebuild plan parts. The trusted renderer removes data-swarm-capture-ui and rejects blockers or unstable states.
- export_html_animation accepts one complete exact ready HTML/package reference with a reviewed animation profile and the separate swarm.animation/v1 manifest/runtime; long exports return a durable staging reference promptly for list/status inspection or cancel_html_animation_export, then publish one silent managed video/mp4 with exact source lineage after background renderer-controlled sampling.
- Use search (or list with cross-session filters) to discover the authenticated user's prior-session artifact library without scanning transcripts or storage folders. Discovery results are flattened explicit candidates, ready items include complete exact references, and next_cursor is an opaque continuation that must be passed back unchanged as cursor. Never infer a selection when human names are ambiguous.
- For retained cross-session artifacts, the source session and source artifact are immutable. To use or edit a retained artifact from another session, call action='read_v3' with artifact_v3_reference to inspect the HTML/manifest, or call action='import' with artifact_v3_reference (for native Artifact V3) or artifact_reference (for legacy managed artifacts) to import it into your session as a new, destination-owned editable head before calling revise_v3 or begin_v3.
- Collection-list results are not complete ready references and cannot be passed directly to get/read; when a list result contains only collection metadata, call list again with collection_id (and session_id for an attached cross-session artifact) to list its artifacts and obtain variant_id and event_seq. To retrieve, read, materialize, promote, or export an attached ready artifact, copy session_id, collection_id, variant_id, and event_seq together from the same artifact reference into the call.
- For repository or other workspace end products, prefer materialize or atomic materialize_batch over bulk read responses, manipulate the imported files with normal workspace tools, then use publish_workspace to publish the finished file or package; copy the original exact reference into source_session_id, source_collection_id, source_variant_id, and source_event_seq when the result derives from one source. Provider/model identifiers, browser/runtime overrides, arbitrary capture dimensions, and private storage paths are never accepted or exposed.`
	default:
		return `Artifact V3 Overview & Help Topics:
For detailed schema contracts and instructions, call action='help' with topic="<name>":
- "animation": Native HTML animations, swarm.animation/v1 manifest, Three.js spatial 3D, deterministic seek.
- "video": Veo 3.1 AI video generation, prompt direction, soundscapes, keyframe extraction, chaining, and master story generation.
- "audio": Google Lyria music, sound effects, multi-prompt variations, and audio iteration.
- "narration": Multi-scene narration plan drafts.
- "workflow": Lifecycle, draft repair, CAS tokens, cross-session search, materialization, and workspace publication.

Quick Action References:
- create (HTML V3): action='create', content='<!doctype html>...', parts=[{id, label, kind: 'temporal|spatial|selector|semantic', description}]. Do NOT substitute generate_image for native HTML documents.
- import: action='import', artifact_v3_reference={session_id, artifact_id, revision_ref} (or artifact_reference={session_id, collection_id, variant_id, event_seq}). Imports an immutable retained artifact into the current session as a new destination-owned head.
- read_v3: action='read_v3', artifact_v3_reference={session_id, artifact_id, revision_ref}. Reads complete project HTML and manifest for current or retained cross-session references.
- list_v3: action='list_v3', cursor='...', limit=50. Queries the retained native Artifact V3 catalog across sessions.
- source_v3: action='source_v3', artifact_id='...', session_id='...' (optional). Resolves exact source and candidate selection calls.
- create (narration): action='create', narration_plan={title, scenes:[{id, title, narration, visual_direction, music_direction}]}.
- revise_v3 (candidate revision): action='revise_v3', session_id, artifact_id, content='...', revision_intent='whole_project|focused_parts', target_part_ids=['...'].
- begin_v3 / author_v3 (incremental repair): begin_v3 returns draft_handle; author_v3 accepts draft_handle and operation={action:'read_file|edit_file|build_preview|finish_turn', path, old_string, new_string}.
- image_capabilities: action='image_capabilities'. Inspect image settings and get capability_token.
- generate_image: action='generate_image', prompt='...', capability_token='...' (call action='image_capabilities' first).
- audio_capabilities: action='audio_capabilities', model='...' (optional). Inspect duration constraints and get capability_token.
- generate_video: action='generate_video', prompt='...', duration_seconds=8, aspect_ratio='16:9'.
- generate_audio: action='generate_audio', prompt='...' (or prompts=['...']), model='...' (optional), duration_seconds=30, capability_token='...' (call action='audio_capabilities' first).
- materialize: action='materialize', session_id, collection_id, variant_id, event_seq, destination='path/to/file'.
- publish_workspace: action='publish_workspace', source='path/to/file', media_type='...'.`
	}
}

func (r *Runtime) resolveVideoImageInput(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, raw any) (*videogen.ManagedVideoImage, *pebblestore.SessionArtifactSelectionReference, error) {
	if raw == nil {
		return nil, nil, nil
	}
	switch v := raw.(type) {
	case string:
		str := strings.TrimSpace(v)
		if str == "" {
			return nil, nil, nil
		}
		if strings.HasPrefix(str, "data:image/") {
			idx := strings.Index(str, ",")
			if idx < 0 {
				return nil, nil, errors.New("invalid image data URI: missing comma separator")
			}
			header := str[:idx]
			encoded := str[idx+1:]
			mimeType := "image/png"
			if semi := strings.Index(header, ";"); semi > 5 {
				mimeType = strings.TrimPrefix(header[:semi], "data:")
			} else {
				mimeType = strings.TrimPrefix(header, "data:")
			}
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return nil, nil, fmt.Errorf("decode image data URI: %w", err)
			}
			if len(decoded) == 0 {
				return nil, nil, errors.New("image data URI payload is empty")
			}
			return &videogen.ManagedVideoImage{
				Bytes:     decoded,
				MediaType: canonicalArtifactMediaType(mimeType),
			}, nil, nil
		}

		// Try resolving as a workspace path
		rooted, err := openRootedWorkspacePath(scope, str)
		if err == nil {
			defer rooted.Close()
			info, statErr := rooted.stat()
			if statErr == nil && info.Mode().IsRegular() {
				if info.Size() > int64(manageArtifactMaxImageReadBytes) {
					return nil, nil, fmt.Errorf("image file %q exceeds maximum limit of %d bytes", str, manageArtifactMaxImageReadBytes)
				}
				f, openErr := rooted.open()
				if openErr != nil {
					return nil, nil, fmt.Errorf("open image file %q: %w", str, openErr)
				}
				defer f.Close()
				data, readErr := io.ReadAll(io.LimitReader(f, manageArtifactMaxImageReadBytes+1))
				if readErr != nil {
					return nil, nil, fmt.Errorf("read image file %q: %w", str, readErr)
				}
				if len(data) == 0 {
					return nil, nil, fmt.Errorf("image file %q is empty", str)
				}
				mimeType := http.DetectContentType(data)
				ext := strings.ToLower(filepath.Ext(str))
				switch ext {
				case ".png":
					mimeType = "image/png"
				case ".jpg", ".jpeg":
					mimeType = "image/jpeg"
				case ".webp":
					mimeType = "image/webp"
				case ".gif":
					mimeType = "image/gif"
				case ".svg":
					mimeType = "image/svg+xml"
				}
				return &videogen.ManagedVideoImage{
					Bytes:     data,
					MediaType: canonicalArtifactMediaType(mimeType),
				}, nil, nil
			}
		}

		// Try as base64 string if sufficiently long and does not look like a filesystem path
		if len(str) > 64 && !strings.ContainsAny(str, "/\\") {
			decoded, b64Err := base64.StdEncoding.DecodeString(str)
			if b64Err == nil && len(decoded) > 0 {
				detected := http.DetectContentType(decoded)
				if strings.HasPrefix(detected, "image/") {
					return &videogen.ManagedVideoImage{
						Bytes:     decoded,
						MediaType: canonicalArtifactMediaType(detected),
					}, nil, nil
				}
			}
		}

		if err != nil {
			return nil, nil, fmt.Errorf("resolve image path %q: %w", str, err)
		}
		return nil, nil, fmt.Errorf("image file %q not found or is not a regular file", str)

	case map[string]any:
		if pathVal := strings.TrimSpace(asString(v["path"])); pathVal != "" {
			return r.resolveVideoImageInput(ctx, scope, principal, pathVal)
		}
		if b64Val := strings.TrimSpace(firstNonEmptyString(asString(v["bytes_base64"]), asString(v["data"]))); b64Val != "" {
			decoded, err := base64.StdEncoding.DecodeString(b64Val)
			if err != nil {
				return nil, nil, fmt.Errorf("decode image base64: %w", err)
			}
			mime := strings.TrimSpace(asString(v["mime_type"]))
			if mime == "" {
				mime = http.DetectContentType(decoded)
			}
			return &videogen.ManagedVideoImage{
				Bytes:     decoded,
				MediaType: canonicalArtifactMediaType(mime),
			}, nil, nil
		}
		if assetID := strings.TrimSpace(asString(v["asset_id"])); assetID != "" {
			if r.sessions == nil {
				return nil, nil, errors.New("sessions service is not configured to resolve asset_id")
			}
			asset, payload, err := r.sessions.ReadSessionMediaAsset(principal.AccountScopeID, principal.SessionID, assetID)
			if err != nil {
				return nil, nil, fmt.Errorf("read image asset %q: %w", assetID, err)
			}
			mime := asset.DetectedMIMEType
			if mime == "" {
				mime = http.DetectContentType(payload)
			}
			return &videogen.ManagedVideoImage{
				Bytes:     payload,
				MediaType: canonicalArtifactMediaType(mime),
			}, nil, nil
		}
		if artRefRaw, ok := v["artifact_reference"].(map[string]any); ok {
			return r.resolveVideoArtifactReference(ctx, principal, artRefRaw)
		}
		if artV3Raw, ok := v["artifact_v3_reference"].(map[string]any); ok {
			return r.resolveVideoArtifactV3Reference(ctx, principal, artV3Raw)
		}
		if strings.TrimSpace(asString(v["collection_id"])) != "" && strings.TrimSpace(asString(v["variant_id"])) != "" {
			return r.resolveVideoArtifactReference(ctx, principal, v)
		}
		if strings.TrimSpace(asString(v["artifact_id"])) != "" && strings.TrimSpace(asString(v["revision_ref"])) != "" {
			return r.resolveVideoArtifactV3Reference(ctx, principal, v)
		}
		return nil, nil, errors.New("unsupported image input object: expected path, asset_id, artifact_reference, or bytes_base64")

	default:
		return nil, nil, fmt.Errorf("unsupported image input type %T", raw)
	}
}

func (r *Runtime) resolveVideoArtifactReference(ctx context.Context, principal artifact.Principal, m map[string]any) (*videogen.ManagedVideoImage, *pebblestore.SessionArtifactSelectionReference, error) {
	if r.artifactAuthority == nil {
		return nil, nil, errors.New("artifact authority is not configured to resolve artifact reference")
	}
	sessionID := strings.TrimSpace(asString(m["session_id"]))
	if sessionID == "" {
		sessionID = principal.SessionID
	}
	collectionID := strings.TrimSpace(asString(m["collection_id"]))
	variantID := strings.TrimSpace(asString(m["variant_id"]))
	eventSeq := asUint64(m["event_seq"])
	if collectionID == "" || variantID == "" || eventSeq == 0 {
		return nil, nil, errors.New("artifact reference requires collection_id, variant_id, and non-zero event_seq")
	}
	ref := pebblestore.SessionArtifactSelectionReference{
		SessionID:    sessionID,
		CollectionID: collectionID,
		VariantID:    variantID,
		EventSeq:     eventSeq,
	}
	body, variant, err := r.artifactAuthority.ReadReference(ctx, principal, ref, manageArtifactMaxImageReadBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("read image artifact reference: %w", err)
	}
	if len(body) == 0 || !strings.HasPrefix(canonicalArtifactMediaType(variant.MediaType), "image/") {
		return nil, nil, errors.New("artifact reference is empty or not a supported ready image")
	}
	return &videogen.ManagedVideoImage{
		Bytes:     append([]byte(nil), body...),
		MediaType: canonicalArtifactMediaType(variant.MediaType),
	}, &ref, nil
}

func (r *Runtime) resolveVideoArtifactV3Reference(ctx context.Context, principal artifact.Principal, m map[string]any) (*videogen.ManagedVideoImage, *pebblestore.SessionArtifactSelectionReference, error) {
	if r.artifactV3Author == nil {
		return nil, nil, errors.New("artifact v3 authority is not configured to resolve artifact_v3_reference")
	}
	sessionID := strings.TrimSpace(asString(m["session_id"]))
	if sessionID == "" {
		sessionID = principal.SessionID
	}
	artifactID := strings.TrimSpace(asString(m["artifact_id"]))
	revisionRef := strings.TrimSpace(asString(m["revision_ref"]))
	if artifactID == "" || revisionRef == "" {
		return nil, nil, errors.New("artifact_v3_reference requires artifact_id and revision_ref")
	}
	payload, err := r.artifactV3Author.ReadPreviewEvidence(ctx, principal.AccountScopeID, principal.UserID, sessionID, artifactID, revisionRef)
	if err != nil {
		return nil, nil, fmt.Errorf("read artifact v3 preview evidence: %w", err)
	}
	mime := http.DetectContentType(payload)
	return &videogen.ManagedVideoImage{
		Bytes:     payload,
		MediaType: canonicalArtifactMediaType(mime),
	}, nil, nil
}

func (r *Runtime) generateManagedVideoArtifact(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID, requestID string, args map[string]any) (managedVideoArtifactResult, error) {
	if strings.TrimSpace(principal.SessionID) == "" {
		return managedVideoArtifactResult{}, identity.ErrPrincipalRequired
	}
	for key := range args {
		switch key {
		case "action", "prompt", "title", "aspect_ratio", "resolution", "duration_seconds", "count",
			"collection_id", "collection_name", "collection_description", "variant_id", "filename", "presentation",
			"source_session_id", "source_collection_id", "source_variant_id", "source_event_seq",
			"image", "image_path", "chain_from", "chain", "includes_audio":
		default:
			return managedVideoArtifactResult{}, fmt.Errorf("manage_artifact generate_video contains unsupported field %q", key)
		}
	}
	if r.videoGeneration == nil {
		return managedVideoArtifactResult{}, errors.New("manage_artifact video generation is not configured")
	}
	prompt := strings.TrimSpace(asString(args["prompt"]))
	if prompt == "" {
		return managedVideoArtifactResult{}, errors.New("manage_artifact generate_video requires prompt")
	}
	if len([]rune(prompt)) > manageArtifactMaxPromptRunes {
		return managedVideoArtifactResult{}, fmt.Errorf("manage_artifact video prompt exceeds %d characters", manageArtifactMaxPromptRunes)
	}

	aspectRatio := strings.TrimSpace(asString(args["aspect_ratio"]))
	resolution := strings.TrimSpace(asString(args["resolution"]))
	durationSeconds := int(asUint64(args["duration_seconds"]))

	count := int(asUint64(args["count"]))
	if count <= 0 {
		count = 1
	}
	if count > 8 {
		return managedVideoArtifactResult{}, errors.New("manage_artifact generate_video count exceeds maximum of 8")
	}

	requestedPresentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return managedVideoArtifactResult{}, err
	}

	rawTitle := strings.TrimSpace(firstNonEmptyString(asString(args["title"]), asString(args["collection_name"]), asString(args["label"])))
	if rawTitle == "" || strings.EqualFold(rawTitle, "Generated video") {
		rawTitle = deriveVideoTitle(prompt)
	}
	title := rawTitle

	collectionID, variantID := managedArtifactOpaqueID("collection", principal.SessionID, callID), managedArtifactOpaqueID("variant", principal.SessionID, callID)
	collectionName := title
	collectionDescription := strings.TrimSpace(asString(args["collection_description"]))
	if collectionDescription == "" {
		collectionDescription = prompt
	}
	managedDestination := false
	if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
		managedDestination = true
		if strings.TrimSpace(run.CollectionID) == "" || strings.TrimSpace(run.VariantID) == "" {
			return managedVideoArtifactResult{}, errors.New("manage_artifact trusted video destination is incomplete")
		}
		if strings.TrimSpace(asString(args["collection_id"])) != "" || strings.TrimSpace(asString(args["variant_id"])) != "" {
			return managedVideoArtifactResult{}, errors.New("manage_artifact managed generate_video must omit collection_id and variant_id")
		}
		collectionID, variantID = strings.TrimSpace(run.CollectionID), strings.TrimSpace(run.VariantID)
		collectionName, collectionDescription = "", ""
	}
	if !managedDestination {
		if supplied := strings.TrimSpace(asString(args["collection_id"])); supplied != "" {
			collectionID = supplied
			collectionName, collectionDescription = "", ""
		}
		if supplied := strings.TrimSpace(asString(args["variant_id"])); supplied != "" {
			variantID = supplied
		}
	}

	var sourceRef *pebblestore.SessionArtifactSelectionReference
	var source *videogen.ManagedVideoSource
	var videoImage *videogen.ManagedVideoImage

	sourceFields := 0
	for _, key := range []string{"source_session_id", "source_collection_id", "source_variant_id", "source_event_seq"} {
		if _, supplied := args[key]; supplied {
			sourceFields++
		}
	}
	if sourceFields != 0 {
		if sourceFields != 4 {
			return managedVideoArtifactResult{}, errors.New("manage_artifact video iteration requires source_session_id, source_collection_id, source_variant_id, and source_event_seq from the same exact ready reference")
		}
		sourceEventSeq := asUint64(args["source_event_seq"])
		ref := pebblestore.SessionArtifactSelectionReference{
			SessionID:    strings.TrimSpace(asString(args["source_session_id"])),
			CollectionID: strings.TrimSpace(asString(args["source_collection_id"])),
			VariantID:    strings.TrimSpace(asString(args["source_variant_id"])),
			EventSeq:     sourceEventSeq,
		}
		if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || sourceEventSeq == 0 {
			return managedVideoArtifactResult{}, errors.New("manage_artifact video iteration requires non-empty source_session_id, source_collection_id, source_variant_id, and source_event_seq")
		}
		body, variant, readErr := r.artifactAuthority.ReadReference(ctx, principal, ref, 512<<20)
		if readErr != nil {
			return managedVideoArtifactResult{}, fmt.Errorf("resolve video iteration source: %w", readErr)
		}
		if strings.HasPrefix(variant.MediaType, "image/") {
			if len(body) == 0 {
				return managedVideoArtifactResult{}, errors.New("image source artifact is empty")
			}
			videoImage = &videogen.ManagedVideoImage{
				Bytes:     append([]byte(nil), body...),
				MediaType: canonicalArtifactMediaType(variant.MediaType),
			}
			sourceRef = &ref
		} else if variant.MediaType == "video/mp4" || strings.HasPrefix(variant.MediaType, "video/") {
			if len(body) == 0 {
				return managedVideoArtifactResult{}, errors.New("video iteration source is empty")
			}
			sourceRef = &ref
			if chainVal, ok := args["chain"].(bool); ok && chainVal {
				keyframeBytes, err := extractLastKeyframeBytes(ctx, body)
				if err != nil {
					return managedVideoArtifactResult{}, fmt.Errorf("extract last keyframe for video chaining: %w", err)
				}
				videoImage = &videogen.ManagedVideoImage{
					Bytes:     keyframeBytes,
					MediaType: "image/png",
				}
			} else {
				interactionID := strings.TrimSpace(variant.Lineage.IterationID)
				source = &videogen.ManagedVideoSource{
					Bytes:         append([]byte(nil), body...),
					MediaType:     "video/mp4",
					InteractionID: interactionID,
				}
			}
		} else {
			return managedVideoArtifactResult{}, errors.New("video iteration source is empty or not a supported ready video/mp4 or image")
		}
	}

	if chainFromRaw := args["chain_from"]; chainFromRaw != nil && videoImage == nil {
		body, ref, _, err := r.resolveVideoSourceBytes(ctx, scope, principal, chainFromRaw)
		if err != nil {
			return managedVideoArtifactResult{}, fmt.Errorf("resolve chain_from video: %w", err)
		}
		keyframeBytes, err := extractLastKeyframeBytes(ctx, body)
		if err != nil {
			return managedVideoArtifactResult{}, fmt.Errorf("extract last keyframe from chain_from video: %w", err)
		}
		videoImage = &videogen.ManagedVideoImage{
			Bytes:     keyframeBytes,
			MediaType: "image/png",
		}
		sourceRef = ref
	}

	rawImage := args["image"]
	if rawImage == nil {
		rawImage = args["image_path"]
	}
	if rawImage != nil {
		img, imgRef, err := r.resolveVideoImageInput(ctx, scope, principal, rawImage)
		if err != nil {
			return managedVideoArtifactResult{}, fmt.Errorf("resolve image input: %w", err)
		}
		videoImage = img
		if imgRef != nil && sourceRef == nil {
			sourceRef = imgRef
		}
	}

	if videoImage != nil {
		isSVG := videoImage.MediaType == "image/svg+xml" || (len(videoImage.Bytes) > 4 && strings.Contains(string(videoImage.Bytes[:min(len(videoImage.Bytes), 256)]), "<svg"))
		if isSVG && r.svgRasterizer != nil {
			pngBytes, err := r.svgRasterizer.RasterizeSVG(ctx, videoImage.Bytes)
			if err != nil {
				return managedVideoArtifactResult{}, fmt.Errorf("rasterize SVG image to PNG: %w", err)
			}
			videoImage.Bytes = pngBytes
			videoImage.MediaType = "image/png"
		}
	}

	var lastVariant pebblestore.SessionArtifactVariant
	var allVariants []map[string]any
	var allReferences []map[string]any
	var costPerVideo, totalCost float64
	var pricingSummary, priceStatus, snapshotID, snapshotVersion, lastModel, lastProvider string
	var lastAspectRatio, lastResolution string
	var lastDurationSeconds int

	for i := 0; i < count; i++ {
		currentVariantID := variantID
		if i > 0 && !managedDestination {
			currentVariantID = fmt.Sprintf("%s-%d", variantID, i+1)
		}
		generated, err := r.videoGeneration.GenerateManagedVideo(identity.ContextWithPrincipal(ctx, scope.Principal), videogen.ManagedVideoRequest{
			Prompt:          prompt,
			AspectRatio:     aspectRatio,
			Resolution:      resolution,
			DurationSeconds: durationSeconds,
			Principal:       scope.Principal,
			Source:          source,
			Image:           videoImage,
		})
		if err != nil {
			return managedVideoArtifactResult{}, fmt.Errorf("generate managed video: %w", err)
		}
		if len(generated.Bytes) == 0 {
			return managedVideoArtifactResult{}, errors.New("generated video output is empty")
		}

		variantTitle := title
		if count > 1 {
			variantTitle = fmt.Sprintf("%s · Variant %d", title, i+1)
		}

		presentation := requestedPresentation
		presentation.Kind, presentation.Previewable = "video", true
		presentation.Label = variantTitle
		presentation.Description = prompt

		filename := strings.TrimSpace(asString(args["filename"]))
		if filename == "" {
			filename = "generated-video.mp4"
		}
		if i > 0 && !strings.Contains(filename, fmt.Sprintf("-%d", i+1)) {
			filename = fmt.Sprintf("generated-video-%d.mp4", i+1)
		}

		iterationIndex := i + 1
		iterationLabel := variantTitle
		autoAccept := true
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok {
			if run.IterationIndex > 0 {
				iterationIndex = run.IterationIndex
			}
			if strings.TrimSpace(run.IterationLabel) != "" {
				iterationLabel = strings.TrimSpace(run.IterationLabel)
			}
			if strings.TrimSpace(run.IterationTheme) != "" {
				presentation.Description = strings.TrimSpace(run.IterationTheme)
			}
			if managedDestination {
				autoAccept = run.AutoAccept
			}
		}

		create := artifact.CreateInput{
			RequestID:             fmt.Sprintf("%s-%d", requestID, i),
			CollectionID:          collectionID,
			CollectionName:        collectionName,
			CollectionDescription: collectionDescription,
			VariantID:             currentVariantID,
			Filename:              filename,
			MediaType:             "video/mp4",
			Presentation:          presentation,
			IterationID:           generated.InteractionID,
			IterationIndex:        iterationIndex,
			IterationLabel:        iterationLabel,
			Body:                  append([]byte(nil), generated.Bytes...),
			AutoAccept:            autoAccept,
		}
		if sourceRef != nil {
			create.SourceSessionID = sourceRef.SessionID
			create.SourceCollectionID = sourceRef.CollectionID
			create.SourceVariantID = sourceRef.VariantID
			create.SourceEventSeq = sourceRef.EventSeq
		}

		effectiveVideoDurationSeconds := generated.DurationSeconds
		if effectiveVideoDurationSeconds <= 0 {
			effectiveVideoDurationSeconds = durationSeconds
			if generated.DurationMs > 0 {
				effectiveVideoDurationSeconds = (generated.DurationMs + 500) / 1000
			}
		}
		effectiveVideoResolution := generated.Resolution
		if effectiveVideoResolution == "" {
			effectiveVideoResolution = resolution
			if effectiveVideoResolution == "" && generated.Height > 0 {
				effectiveVideoResolution = fmt.Sprintf("%dp", generated.Height)
			}
		}
		effectiveAspectRatio := generated.AspectRatio
		if effectiveAspectRatio == "" {
			effectiveAspectRatio = aspectRatio
		}

		effectiveIncludesAudio := true
		if incVal, ok := args["includes_audio"].(bool); ok && !strings.Contains(strings.ToLower(generated.Model), "veo") {
			effectiveIncludesAudio = incVal
		}

		estimate := pebblestore.MediaCostEstimate{
			CostUSD:         generated.EstimatedCostUSD,
			PriceStatus:     generated.PriceStatus,
			PricingSummary:  generated.PricingSummary,
			SnapshotID:      generated.SnapshotID,
			SnapshotVersion: generated.SnapshotVersion,
		}
		if estimate.PriceStatus == "" {
			if r.sessions != nil {
				estimate = r.sessions.EstimateMediaCostWithOptions(pebblestore.MediaCostEstimateOptions{
					Provider:        generated.Provider,
					Model:           generated.Model,
					Kind:            "video",
					Count:           1,
					DurationSeconds: effectiveVideoDurationSeconds,
					Resolution:      effectiveVideoResolution,
					AspectRatio:     effectiveAspectRatio,
					IncludesAudio:   effectiveIncludesAudio,
					IsIteration:     sourceRef != nil,
					ServiceTier:     "standard",
				})
			} else {
				estimate.PriceStatus = "unknown"
			}
		}
		mediaRec := pebblestore.SessionMediaUsageRecord{
			ID:              currentVariantID,
			SessionID:       principal.SessionID,
			AccountScopeID:  principal.AccountScopeID,
			UserID:          principal.UserID,
			MediaType:       "video/mp4",
			Kind:            "video",
			Provider:        generated.Provider,
			Model:           generated.Model,
			Filename:        filename,
			Label:           presentation.Label,
			Size:            int64(len(generated.Bytes)),
			CostUSD:         estimate.CostUSD,
			PriceStatus:     estimate.PriceStatus,
			PricingSummary:  estimate.PricingSummary,
			SnapshotID:      estimate.SnapshotID,
			SnapshotVersion: estimate.SnapshotVersion,
			CreatedAt:       time.Now().UnixMilli(),
		}
		if r.sessions != nil {
			if recErr := r.sessions.RecordMediaUsage(mediaRec); recErr != nil {
				return managedVideoArtifactResult{}, fmt.Errorf("record video media usage: %w", recErr)
			}
		}

		published, err := r.artifactAuthority.Create(ctx, principal, create)
		if err != nil {
			return managedVideoArtifactResult{}, fmt.Errorf("publish video artifact: %w", err)
		}
		lastVariant = published
		allVariants = append(allVariants, managedArtifactVariant(published))
		allReferences = append(allReferences, managedArtifactReferenceWithSession(published.SessionID, published.CollectionID, published.ID, published.EventSeq))
		costPerVideo = estimate.CostUSD
		totalCost += estimate.CostUSD
		pricingSummary = estimate.PricingSummary
		// A later priced result must not hide an unpriced member of the batch.
		if priceStatus != "unknown" {
			priceStatus = estimate.PriceStatus
		}
		snapshotID = estimate.SnapshotID
		snapshotVersion = estimate.SnapshotVersion
		lastModel = generated.Model
		lastProvider = generated.Provider
		lastResolution = effectiveVideoResolution
		lastDurationSeconds = effectiveVideoDurationSeconds
		lastAspectRatio = effectiveAspectRatio
	}

	return managedVideoArtifactResult{
		LastVariant:           lastVariant,
		Variants:              allVariants,
		References:            allReferences,
		Title:                 title,
		Prompt:                prompt,
		Model:                 lastModel,
		Provider:              lastProvider,
		AspectRatio:           lastAspectRatio,
		Resolution:            lastResolution,
		DurationSeconds:       lastDurationSeconds,
		CostPerVideoUSD:       costPerVideo,
		TotalEstimatedCostUSD: totalCost,
		PriceStatus:           priceStatus,
		PricingSummary:        pricingSummary,
		SnapshotID:            snapshotID,
		SnapshotVersion:       snapshotVersion,
		HasImageInput:         videoImage != nil,
	}, nil
}

func (r *Runtime) generateManagedAudioArtifact(
	ctx context.Context,
	scope WorkspaceScope,
	principal artifact.Principal,
	callID, requestID string,
	args map[string]any,
) (managedAudioArtifactResult, error) {
	for key := range args {
		switch key {
		case "action", "prompt", "prompts", "title", "label", "duration_seconds", "count",
			"collection_id", "collection_name", "collection_description", "variant_id", "filename", "presentation",
			"source_session_id", "source_collection_id", "source_variant_id", "source_event_seq",
			"image", "image_path", "model", "capability_token":
		default:
			return managedAudioArtifactResult{}, fmt.Errorf("manage_artifact generate_audio contains unsupported field %q", key)
		}
	}
	if r.audioGeneration == nil {
		return managedAudioArtifactResult{}, errors.New("manage_artifact audio generation is not configured")
	}

	var prompts []string
	if rawPrompts, exists := args["prompts"]; exists {
		switch v := rawPrompts.(type) {
		case []any:
			if len(v) > 8 {
				return managedAudioArtifactResult{}, errors.New("manage_artifact generate_audio prompts exceeds maximum of 8")
			}
			for _, item := range v {
				s := strings.TrimSpace(asString(item))
				if s != "" {
					prompts = append(prompts, s)
				}
			}
		case []string:
			if len(v) > 8 {
				return managedAudioArtifactResult{}, errors.New("manage_artifact generate_audio prompts exceeds maximum of 8")
			}
			for _, item := range v {
				s := strings.TrimSpace(item)
				if s != "" {
					prompts = append(prompts, s)
				}
			}
		default:
			return managedAudioArtifactResult{}, errors.New("manage_artifact generate_audio prompts must be an array of strings")
		}
	}

	prompt := strings.TrimSpace(asString(args["prompt"]))
	if prompt == "" && len(prompts) > 0 {
		prompt = prompts[0]
	}
	if prompt == "" {
		return managedAudioArtifactResult{}, errors.New("manage_artifact generate_audio requires prompt")
	}
	if len([]rune(prompt)) > manageArtifactMaxPromptRunes {
		return managedAudioArtifactResult{}, fmt.Errorf("manage_artifact audio prompt exceeds %d characters", manageArtifactMaxPromptRunes)
	}
	for _, p := range prompts {
		if len([]rune(p)) > manageArtifactMaxPromptRunes {
			return managedAudioArtifactResult{}, fmt.Errorf("manage_artifact audio prompt exceeds %d characters", manageArtifactMaxPromptRunes)
		}
	}

	count := int(asUint64(args["count"]))
	if count <= 0 {
		if len(prompts) > 0 {
			count = len(prompts)
		} else {
			count = 1
		}
	}
	if count > 8 {
		return managedAudioArtifactResult{}, errors.New("manage_artifact generate_audio count exceeds maximum of 8")
	}

	durationSeconds := int(asUint64(args["duration_seconds"]))

	requestedPresentation, err := parseArtifactPresentation(args["presentation"])
	if err != nil {
		return managedAudioArtifactResult{}, err
	}

	rawTitle := strings.TrimSpace(firstNonEmptyString(asString(args["title"]), asString(args["collection_name"]), asString(args["label"])))
	if rawTitle == "" || strings.EqualFold(rawTitle, "Generated audio") {
		rawTitle = deriveAudioTitle(prompt)
	}
	title := rawTitle

	collectionID, variantID := managedArtifactOpaqueID("collection", principal.SessionID, callID), managedArtifactOpaqueID("variant", principal.SessionID, callID)
	collectionName := title
	collectionDescription := strings.TrimSpace(asString(args["collection_description"]))
	if collectionDescription == "" {
		collectionDescription = prompt
	}
	managedDestination := false
	if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok && (strings.TrimSpace(run.CollectionID) != "" || strings.TrimSpace(run.VariantID) != "") {
		managedDestination = true
		if strings.TrimSpace(run.CollectionID) == "" || strings.TrimSpace(run.VariantID) == "" {
			return managedAudioArtifactResult{}, errors.New("manage_artifact trusted audio destination is incomplete")
		}
		if strings.TrimSpace(asString(args["collection_id"])) != "" || strings.TrimSpace(asString(args["variant_id"])) != "" {
			return managedAudioArtifactResult{}, errors.New("manage_artifact managed generate_audio must omit collection_id and variant_id")
		}
		collectionID, variantID = strings.TrimSpace(run.CollectionID), strings.TrimSpace(run.VariantID)
		collectionName, collectionDescription = "", ""
	}
	if !managedDestination {
		if supplied := strings.TrimSpace(asString(args["collection_id"])); supplied != "" {
			collectionID = supplied
			collectionName, collectionDescription = "", ""
		}
		if supplied := strings.TrimSpace(asString(args["variant_id"])); supplied != "" {
			variantID = supplied
		}
	}

	var sourceRef *pebblestore.SessionArtifactSelectionReference
	var source *audiogen.ManagedAudioSource
	var audioImage *audiogen.ManagedAudioImage

	sourceFields := 0
	for _, key := range []string{"source_session_id", "source_collection_id", "source_variant_id", "source_event_seq"} {
		if _, supplied := args[key]; supplied {
			sourceFields++
		}
	}
	if sourceFields != 0 {
		if sourceFields != 4 {
			return managedAudioArtifactResult{}, errors.New("manage_artifact audio iteration requires source_session_id, source_collection_id, source_variant_id, and source_event_seq from the same exact ready reference")
		}
		sourceEventSeq := asUint64(args["source_event_seq"])
		ref := pebblestore.SessionArtifactSelectionReference{
			SessionID:    strings.TrimSpace(asString(args["source_session_id"])),
			CollectionID: strings.TrimSpace(asString(args["source_collection_id"])),
			VariantID:    strings.TrimSpace(asString(args["source_variant_id"])),
			EventSeq:     sourceEventSeq,
		}
		if ref.SessionID == "" || ref.CollectionID == "" || ref.VariantID == "" || sourceEventSeq == 0 {
			return managedAudioArtifactResult{}, errors.New("manage_artifact audio iteration requires non-empty source_session_id, source_collection_id, source_variant_id, and source_event_seq")
		}
		body, variant, readErr := r.artifactAuthority.ReadReference(ctx, principal, ref, 512<<20)
		if readErr != nil {
			return managedAudioArtifactResult{}, fmt.Errorf("resolve audio iteration source: %w", readErr)
		}
		if strings.HasPrefix(variant.MediaType, "image/") {
			if len(body) == 0 {
				return managedAudioArtifactResult{}, errors.New("image source artifact is empty")
			}
			audioImage = &audiogen.ManagedAudioImage{
				Bytes:     append([]byte(nil), body...),
				MediaType: canonicalArtifactMediaType(variant.MediaType),
			}
			sourceRef = &ref
		} else if strings.HasPrefix(variant.MediaType, "audio/") || variant.MediaType == "audio/mp3" || variant.MediaType == "audio/mpeg" {
			if len(body) == 0 {
				return managedAudioArtifactResult{}, errors.New("audio iteration source is empty")
			}
			sourceRef = &ref
			interactionID := strings.TrimSpace(variant.Lineage.IterationID)
			source = &audiogen.ManagedAudioSource{
				Bytes:         append([]byte(nil), body...),
				MediaType:     canonicalArtifactMediaType(variant.MediaType),
				InteractionID: interactionID,
			}
		} else {
			return managedAudioArtifactResult{}, errors.New("audio iteration source is empty or not a supported ready audio (audio/mp3) or image")
		}
	}

	rawImage := args["image"]
	if rawImage == nil {
		rawImage = args["image_path"]
	}
	if rawImage != nil {
		img, imgRef, err := r.resolveVideoImageInput(ctx, scope, principal, rawImage)
		if err != nil {
			return managedAudioArtifactResult{}, fmt.Errorf("resolve image input: %w", err)
		}
		if img != nil {
			audioImage = &audiogen.ManagedAudioImage{
				Bytes:     img.Bytes,
				MediaType: img.MediaType,
			}
		}
		if imgRef != nil && sourceRef == nil {
			sourceRef = imgRef
		}
	}

	if audioImage != nil {
		isSVG := audioImage.MediaType == "image/svg+xml" || (len(audioImage.Bytes) > 4 && strings.Contains(string(audioImage.Bytes[:min(len(audioImage.Bytes), 256)]), "<svg"))
		if isSVG && r.svgRasterizer != nil {
			pngBytes, err := r.svgRasterizer.RasterizeSVG(ctx, audioImage.Bytes)
			if err != nil {
				return managedAudioArtifactResult{}, fmt.Errorf("rasterize SVG image to PNG: %w", err)
			}
			audioImage.Bytes = pngBytes
			audioImage.MediaType = "image/png"
		}
	}

	var lastVariant pebblestore.SessionArtifactVariant
	var allVariants []map[string]any
	var allReferences []map[string]any
	var costPerAudio, totalCost float64
	var pricingSummary, lastModel, lastProvider, lastLyrics string
	var lastDurationMs int
	var lastMetadata audiogen.AudioMetadata

	requestedModel := strings.TrimSpace(asString(args["model"]))
	if requestedModel == "" && r.uiSettings != nil && strings.TrimSpace(principal.AccountScopeID) != "" {
		if ui, err := r.uiSettings.GetForAccount(principal.AccountScopeID); err == nil {
			requestedModel = strings.TrimSpace(ui.Tools.Audio.DefaultModel)
		}
	}
	if requestedModel == "" {
		requestedModel = audiogen.DefaultAudioSongModel
	}

	capabilityToken := strings.TrimSpace(asString(args["capability_token"]))
	caps, capsErr := r.managedAudioCapabilitiesWithModel(principal.AccountScopeID, requestedModel)
	if capsErr == nil && caps.Available {
		if capabilityToken != "" && caps.CapabilityToken != "" && capabilityToken != caps.CapabilityToken {
			return managedAudioArtifactResult{}, errors.New("manage_artifact generate_audio capability_token does not match current audio model capabilities; call action='audio_capabilities' first")
		}
		if durationSeconds > 0 {
			if caps.DurationSeconds.MaxSeconds > 0 && durationSeconds > caps.DurationSeconds.MaxSeconds {
				return managedAudioArtifactResult{}, fmt.Errorf("requested duration %ds exceeds maximum duration of %ds for audio model %q; call action='audio_capabilities' to check model limits or choose a full-song model in Settings -> Media", durationSeconds, caps.DurationSeconds.MaxSeconds, caps.Model)
			}
			if caps.DurationSeconds.MinSeconds > 0 && durationSeconds < caps.DurationSeconds.MinSeconds {
				return managedAudioArtifactResult{}, fmt.Errorf("requested duration %ds is below minimum duration of %ds for audio model %q", durationSeconds, caps.DurationSeconds.MinSeconds, caps.Model)
			}
		} else if caps.DurationSeconds.DefaultValue > 0 {
			durationSeconds = caps.DurationSeconds.DefaultValue
		}
	}

	for i := 0; i < count; i++ {
		currentVariantID := variantID
		if i > 0 && !managedDestination {
			currentVariantID = fmt.Sprintf("%s-%d", variantID, i+1)
		}

		currentPrompt := prompt
		if i < len(prompts) && prompts[i] != "" {
			currentPrompt = prompts[i]
		}

		generated, err := r.audioGeneration.GenerateManagedAudio(identity.ContextWithPrincipal(ctx, scope.Principal), audiogen.ManagedAudioRequest{
			Prompt:          currentPrompt,
			DurationSeconds: durationSeconds,
			Principal:       scope.Principal,
			Model:           requestedModel,
			Source:          source,
			Image:           audioImage,
			CapabilityToken: capabilityToken,
		})
		if err != nil {
			return managedAudioArtifactResult{}, fmt.Errorf("generate managed audio: %w", err)
		}
		if len(generated.Bytes) == 0 {
			return managedAudioArtifactResult{}, errors.New("generated audio output is empty")
		}

		variantTitle := title
		if count > 1 {
			if len(prompts) > 1 && i < len(prompts) && prompts[i] != "" {
				derived := deriveAudioTitle(prompts[i])
				if derived != "" && !strings.EqualFold(derived, "Generated audio") {
					variantTitle = fmt.Sprintf("%d. %s", i+1, derived)
				} else {
					variantTitle = fmt.Sprintf("%s · Variant %d", title, i+1)
				}
			} else {
				variantTitle = fmt.Sprintf("%s · Variant %d", title, i+1)
			}
		}

		presentation := requestedPresentation
		presentation.Kind, presentation.Previewable = "audio", true
		presentation.Label = variantTitle
		presentation.Description = currentPrompt

		filename := strings.TrimSpace(asString(args["filename"]))
		if filename == "" {
			filename = "generated-audio.mp3"
		}
		if i > 0 && !strings.Contains(filename, fmt.Sprintf("-%d", i+1)) {
			filename = fmt.Sprintf("generated-audio-%d.mp3", i+1)
		}

		iterationIndex := i + 1
		iterationLabel := variantTitle
		autoAccept := true
		if run, ok := ctx.Value(artifactRunContextKey{}).(ArtifactRunContext); ok {
			if run.IterationIndex > 0 {
				iterationIndex = run.IterationIndex
			}
			if strings.TrimSpace(run.IterationLabel) != "" {
				iterationLabel = strings.TrimSpace(run.IterationLabel)
			}
			if strings.TrimSpace(run.IterationTheme) != "" {
				presentation.Description = strings.TrimSpace(run.IterationTheme)
			}
			if managedDestination {
				autoAccept = run.AutoAccept
			}
		}

		mediaType := strings.TrimSpace(generated.MediaType)
		if mediaType == "" {
			mediaType = audiogen.DefaultAudioMIMEType
		}

		create := artifact.CreateInput{
			RequestID:             fmt.Sprintf("%s-%d", requestID, i),
			CollectionID:          collectionID,
			CollectionName:        collectionName,
			CollectionDescription: collectionDescription,
			VariantID:             currentVariantID,
			Filename:              filename,
			MediaType:             mediaType,
			Presentation:          presentation,
			IterationID:           generated.InteractionID,
			IterationIndex:        iterationIndex,
			IterationLabel:        iterationLabel,
			Body:                  append([]byte(nil), generated.Bytes...),
			AutoAccept:            autoAccept,
		}
		if sourceRef != nil {
			create.SourceSessionID = sourceRef.SessionID
			create.SourceCollectionID = sourceRef.CollectionID
			create.SourceVariantID = sourceRef.VariantID
			create.SourceEventSeq = sourceRef.EventSeq
		}

		effectiveAudioDurationSeconds := durationSeconds
		if generated.DurationMs > 0 {
			effectiveAudioDurationSeconds = (generated.DurationMs + 500) / 1000
		}

		estimate := pebblestore.MediaCostEstimate{CostUSD: generated.EstimatedCostUSD, PriceStatus: "known", PricingSummary: generated.PricingSummary}
		if r.sessions != nil {
			estimate = r.sessions.EstimateMediaCostWithOptions(pebblestore.MediaCostEstimateOptions{
				Provider:        lastProvider,
				Model:           requestedModel,
				Kind:            "audio",
				Count:           1,
				DurationSeconds: effectiveAudioDurationSeconds,
				IsIteration:     sourceRef != nil,
			})
		}
		mediaRec := pebblestore.SessionMediaUsageRecord{
			ID:              currentVariantID,
			SessionID:       principal.SessionID,
			AccountScopeID:  principal.AccountScopeID,
			UserID:          principal.UserID,
			MediaType:       mediaType,
			Kind:            "audio",
			Provider:        lastProvider,
			Model:           requestedModel,
			Filename:        filename,
			Label:           presentation.Label,
			Size:            int64(len(generated.Bytes)),
			CostUSD:         estimate.CostUSD,
			PriceStatus:     estimate.PriceStatus,
			PricingSummary:  estimate.PricingSummary,
			SnapshotID:      estimate.SnapshotID,
			SnapshotVersion: estimate.SnapshotVersion,
			CreatedAt:       time.Now().UnixMilli(),
		}
		if r.sessions != nil {
			if recErr := r.sessions.RecordMediaUsage(mediaRec); recErr != nil {
				return managedAudioArtifactResult{}, fmt.Errorf("record audio media usage: %w", recErr)
			}
		}

		published, err := r.artifactAuthority.Create(ctx, principal, create)
		if err != nil {
			return managedAudioArtifactResult{}, fmt.Errorf("publish audio artifact: %w", err)
		}
		lastVariant = published
		allVariants = append(allVariants, managedArtifactVariant(published))
		allReferences = append(allReferences, managedArtifactReferenceWithSession(published.SessionID, published.CollectionID, published.ID, published.EventSeq))
		costPerAudio = estimate.CostUSD
		totalCost += generated.EstimatedCostUSD
		pricingSummary = generated.PricingSummary
		lastModel = generated.Model
		lastProvider = generated.Provider
		lastLyrics = generated.Lyrics
		lastDurationMs = generated.DurationMs
		lastMetadata = generated.Metadata
	}

	return managedAudioArtifactResult{
		LastVariant:           lastVariant,
		Variants:              allVariants,
		References:            allReferences,
		Title:                 title,
		Prompt:                prompt,
		Prompts:               prompts,
		Model:                 lastModel,
		Provider:              lastProvider,
		DurationSeconds:       durationSeconds,
		DurationMs:            lastDurationMs,
		Lyrics:                lastLyrics,
		CostPerAudioUSD:       costPerAudio,
		TotalEstimatedCostUSD: totalCost,
		PricingSummary:        pricingSummary,
		HasImageInput:         audioImage != nil,
		Metadata:              lastMetadata,
	}, nil
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
		IterationSectionID: strings.TrimSpace(run.IterationSectionID), IterationSectionLabel: strings.TrimSpace(run.IterationSectionLabel), IterationSectionStartMs: run.IterationSectionStartMs, IterationSectionEndMs: run.IterationSectionEndMs,
		PartID: strings.TrimSpace(run.PartID), PartLabel: strings.TrimSpace(run.PartLabel), PartKind: strings.TrimSpace(run.PartKind),
		SelectedReviewTargetIDs: artifactReviewTargetIDs(run.SelectedReviewTargets),
	}, nil
}

func artifactReviewTargetIDs(targets []pebblestore.SessionArtifactPart) string {
	if len(targets) == 0 {
		return ""
	}
	ids := make([]string, 0, len(targets))
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		id := strings.TrimSpace(target.ID)
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	return strings.Join(ids, ",")
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
	var animationProfile *pebblestore.SessionArtifactAnimationProfile
	if rawProfile, exists := args["animation_profile"]; exists {
		animationProfile, err = artifact.ParseAnimationProfile(rawProfile)
		if err != nil {
			return artifact.CreateInput{}, nil, err
		}
	}
	if err := validateArtifactAnimationMedia(animationProfile, packageArtifact, filename, strings.TrimSpace(asString(args["media_type"]))); err != nil {
		return artifact.CreateInput{}, nil, err
	}
	if err := enforceArtifactPresentationRequirements(&presentation, requirements); err != nil {
		return artifact.CreateInput{}, nil, err
	}
	parts, err := parseArtifactParts(args["parts"])
	if err != nil {
		return artifact.CreateInput{}, nil, err
	}
	_, hasInitialParts := args["initial_parts"]
	if hasInitialParts {
		if packageArtifact {
			return artifact.CreateInput{}, nil, errors.New("manage_artifact initial_parts is valid only for create")
		}
		if len(parts) != 0 {
			return artifact.CreateInput{}, nil, errors.New("manage_artifact create cannot combine locator-only parts with real initial_parts; put optional locator metadata on each initial part")
		}
		if _, supplied := args["content"]; supplied {
			return artifact.CreateInput{}, nil, errors.New("manage_artifact create cannot combine monolithic content with real initial_parts")
		}
		if _, supplied := args["entries"]; supplied {
			return artifact.CreateInput{}, nil, errors.New("manage_artifact create cannot combine package entries with real initial_parts")
		}
	}
	input := artifact.CreateInput{CollectionID: collectionID, CollectionName: name, CollectionDescription: asString(args["collection_description"]), VariantID: variantID, Filename: filename, MediaType: strings.TrimSpace(asString(args["media_type"])), Presentation: presentation, OutputRequirements: requirements, AnimationProfile: animationProfile, Parts: parts, SourceSessionID: strings.TrimSpace(asString(args["source_session_id"])), SourceCollectionID: strings.TrimSpace(asString(args["source_collection_id"])), SourceVariantID: strings.TrimSpace(asString(args["source_variant_id"])), SourceEventSeq: asUint64(args["source_event_seq"])}
	if !packageArtifact {
		if hasInitialParts {
			return input, nil, nil
		}
		content, ok := args["content"].(string)
		if !ok || content == "" {
			return artifact.CreateInput{}, nil, errors.New("create requires non-empty content or real initial_parts")
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

func artifactPartNumber(value any) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		return 0
	}
}

func parseArtifactInitialParts(raw any, sessionID, collectionID, variantID, callID string) ([]artifact.InitialPartInput, error) {
	if raw == nil {
		return nil, nil
	}
	items, ok := raw.([]any)
	if !ok || len(items) < 2 || len(items) > pebblestore.SessionArtifactMaxParts {
		return nil, fmt.Errorf("manage_artifact initial_parts must contain 2 to %d independently byte-bearing parts", pebblestore.SessionArtifactMaxParts)
	}
	chainID := pebblestore.RootSessionArtifactChainID(sessionID, collectionID, variantID)
	parts := make([]artifact.InitialPartInput, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	total := 0
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("manage_artifact initial part %d must be an object", index)
		}
		id := strings.TrimSpace(asString(item["id"]))
		if !validManagedArtifactStableID(id) {
			return nil, fmt.Errorf("manage_artifact initial part %d has an invalid stable id", index)
		}
		if _, duplicate := seen[id]; duplicate {
			return nil, fmt.Errorf("manage_artifact initial_parts contains duplicate stable part id %q", id)
		}
		seen[id] = struct{}{}
		label := strings.TrimSpace(asString(item["label"]))
		description := strings.TrimSpace(asString(item["description"]))
		mediaType := canonicalArtifactMediaType(asString(item["media_type"]))
		if label == "" || len(label) > 256 || len(description) > 2048 || mediaType == "" || len(mediaType) > 255 {
			return nil, fmt.Errorf("manage_artifact initial part %q requires bounded label, description, and media_type", id)
		}
		text, hasText := item["content"]
		encoded, hasBase64 := item["content_base64"]
		if hasText == hasBase64 {
			return nil, fmt.Errorf("manage_artifact initial part %q requires exactly one of content or content_base64", id)
		}
		var body []byte
		if hasText {
			value, ok := text.(string)
			if !ok {
				return nil, fmt.Errorf("manage_artifact initial part %q content must be a string", id)
			}
			body = []byte(value)
		} else {
			value, ok := encoded.(string)
			if !ok {
				return nil, fmt.Errorf("manage_artifact initial part %q content_base64 must be a string", id)
			}
			var err error
			body, err = base64.StdEncoding.Strict().DecodeString(value)
			if err != nil {
				return nil, fmt.Errorf("manage_artifact initial part %q content_base64 is invalid", id)
			}
		}
		if len(body) == 0 || len(body) > manageArtifactMaxCreateBytes {
			return nil, fmt.Errorf("manage_artifact initial part %q content must be between 1 and %d bytes", id, manageArtifactMaxCreateBytes)
		}
		total += len(body)
		if total > manageArtifactMaxPackageBytes {
			return nil, fmt.Errorf("manage_artifact initial_parts content exceeds %d bytes", manageArtifactMaxPackageBytes)
		}
		locator, err := parseArtifactInitialPartLocator(item["locator"])
		if err != nil {
			return nil, fmt.Errorf("manage_artifact initial part %q locator: %w", id, err)
		}
		revisionSeed := strings.Join([]string{"initial-part-revision-v1", strings.TrimSpace(sessionID), strings.TrimSpace(collectionID), strings.TrimSpace(variantID), strings.TrimSpace(callID), id}, "\x00")
		revisionDigest := sha256.Sum256([]byte(revisionSeed))
		parts = append(parts, artifact.InitialPartInput{
			Definition: pebblestore.SessionArtifactPartDefinition{ArtifactChainID: chainID, ID: id, OwnerSessionID: strings.TrimSpace(sessionID), Label: label, Description: description, Locator: locator},
			RevisionID: "part-revision-" + hex.EncodeToString(revisionDigest[:12]), MediaType: mediaType, Body: body,
		})
	}
	return parts, nil
}

func parseArtifactInitialPartLocator(raw any) (*pebblestore.SessionArtifactPartLocator, error) {
	if raw == nil {
		return nil, nil
	}
	item, ok := raw.(map[string]any)
	if !ok {
		return nil, errors.New("must be an object")
	}
	kind := strings.ToLower(strings.TrimSpace(asString(item["kind"])))
	locator := &pebblestore.SessionArtifactPartLocator{
		Kind: kind, StartMs: int64(asInt(item["start_ms"], 0)), EndMs: int64(asInt(item["end_ms"], 0)),
		X: artifactPartNumber(item["x"]), Y: artifactPartNumber(item["y"]), Width: artifactPartNumber(item["width"]), Height: artifactPartNumber(item["height"]),
		Page: asInt(item["page"], 0), StateID: strings.TrimSpace(asString(item["state_id"])), Selector: strings.TrimSpace(asString(item["selector"])),
	}
	switch kind {
	case "temporal":
		if locator.StartMs < 0 || locator.EndMs <= locator.StartMs {
			return nil, errors.New("temporal locator requires a valid start_ms/end_ms range")
		}
	case "spatial":
		if locator.X < 0 || locator.Y < 0 || locator.Width <= 0 || locator.Height <= 0 || locator.X+locator.Width > 1 || locator.Y+locator.Height > 1 {
			return nil, errors.New("spatial locator requires normalized x/y/width/height")
		}
	case "page":
		if locator.Page < 1 {
			return nil, errors.New("page locator requires page")
		}
	case "state":
		if locator.StateID == "" || len(locator.StateID) > 128 {
			return nil, errors.New("state locator requires bounded state_id")
		}
	case "selector":
		if locator.Selector == "" || len(locator.Selector) > 512 {
			return nil, errors.New("selector locator requires bounded selector")
		}
	case "semantic":
	default:
		return nil, errors.New("kind is invalid")
	}
	return locator, nil
}

func validManagedArtifactStableID(value string) bool {
	if value == "" || len(value) > 128 || value == "." || value == ".." {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || (index > 0 && (character == '_' || character == '-' || character == '.')) {
			continue
		}
		return false
	}
	return true
}

func parseArtifactParts(raw any) ([]pebblestore.SessionArtifactPart, error) {
	if raw == nil {
		return nil, nil
	}
	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case []map[string]any:
		items = make([]any, len(v))
		for i, m := range v {
			items[i] = m
		}
	default:
		return nil, errors.New("parts must be a bounded array")
	}
	if len(items) > pebblestore.SessionArtifactMaxParts {
		return nil, errors.New("parts must be a bounded array")
	}
	parts := make([]pebblestore.SessionArtifactPart, 0, len(items))
	for index, rawItem := range items {
		item, ok := rawItem.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("part %d must be an object", index)
		}
		parts = append(parts, pebblestore.SessionArtifactPart{
			ID: asString(item["id"]), Label: asString(item["label"]), Kind: asString(item["kind"]), Description: asString(item["description"]),
			StartMs: int64(asInt(item["start_ms"], 0)), EndMs: int64(asInt(item["end_ms"], 0)), X: artifactPartNumber(item["x"]), Y: artifactPartNumber(item["y"]), Width: artifactPartNumber(item["width"]), Height: artifactPartNumber(item["height"]),
			Page: asInt(item["page"], 0), StateID: asString(item["state_id"]), Selector: asString(item["selector"]),
		})
	}
	return parts, nil
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

func validateArtifactRetrievalIdentity(args map[string]any, action string, exactRequired bool) error {
	sessionID := strings.TrimSpace(asString(args["session_id"]))
	collectionID := strings.TrimSpace(asString(args["collection_id"]))
	variantID := strings.TrimSpace(asString(args["variant_id"]))
	eventSeq := asUint64(args["event_seq"])
	hasExactField := sessionID != "" || collectionID != "" || eventSeq != 0
	if !hasExactField && !exactRequired && variantID != "" {
		return nil
	}
	if sessionID == "" || collectionID == "" || variantID == "" || eventSeq == 0 {
		return fmt.Errorf("manage_artifact %s requires the complete ready reference; copy session_id, collection_id, variant_id, and event_seq together from the same returned reference", action)
	}
	return nil
}

func parseArtifactReadReference(args map[string]any, variantID string) (pebblestore.SessionArtifactSelectionReference, bool, error) {
	sessionID := strings.TrimSpace(asString(args["session_id"]))
	collectionID := strings.TrimSpace(asString(args["collection_id"]))
	eventSeq := asUint64(args["event_seq"])
	explicit := sessionID != "" || collectionID != "" || eventSeq != 0
	if !explicit {
		return pebblestore.SessionArtifactSelectionReference{}, false, nil
	}
	if sessionID == "" || collectionID == "" || variantID == "" || eventSeq == 0 {
		return pebblestore.SessionArtifactSelectionReference{}, false, errors.New("manage_artifact exact source reference is incomplete; copy session_id, collection_id, variant_id, and event_seq together from the same ready reference")
	}
	return pebblestore.SessionArtifactSelectionReference{SessionID: sessionID, CollectionID: collectionID, VariantID: variantID, EventSeq: eventSeq}, true, nil
}

func optionalArtifactInt64(args map[string]any, key string) (int64, bool, error) {
	value, supplied := args[key]
	if !supplied {
		return 0, false, nil
	}
	var parsed int64
	switch typed := value.(type) {
	case float64:
		if typed < 0 || typed > float64(1<<53) || math.Trunc(typed) != typed {
			return 0, true, fmt.Errorf("manage_artifact %s must be a non-negative integer", key)
		}
		parsed = int64(typed)
	case int:
		parsed = int64(typed)
	case int64:
		parsed = typed
	case uint64:
		if typed > math.MaxInt64 {
			return 0, true, fmt.Errorf("manage_artifact %s is too large", key)
		}
		parsed = int64(typed)
	case json.Number:
		value, err := typed.Int64()
		if err != nil {
			return 0, true, fmt.Errorf("manage_artifact %s must be a non-negative integer", key)
		}
		parsed = value
	default:
		return 0, true, fmt.Errorf("manage_artifact %s must be a non-negative integer", key)
	}
	if parsed < 0 {
		return 0, true, fmt.Errorf("manage_artifact %s must be a non-negative integer", key)
	}
	return parsed, true, nil
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
	result := map[string]any{"id": v.ID, "collection_id": v.CollectionID, "session_id": v.SessionID, "status": v.Status, "filename": v.Filename, "media_type": v.MediaType, "digest_sha256": v.DigestSHA256, "size": v.Size, "failure_code": v.FailureCode, "progress": v.Progress, "presentation": managedArtifactPresentation(v.Presentation), "output_requirements": v.OutputRequirements, "animation_profile": v.AnimationProfile, "part_graph_state": v.PartGraphState, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt, "event_seq": v.EventSeq, "label": firstNonEmptyString(v.Presentation.Label, v.Filename), "description": v.Presentation.Description, "kind": v.Presentation.Kind, "previewable": v.Presentation.Previewable}
	if v.Lineage != (pebblestore.SessionArtifactLineage{}) {
		result["lineage"] = v.Lineage
	}
	if v.PartGraphState == pebblestore.SessionArtifactGraphAuthoritative && v.Composition != nil {
		result["artifact_chain_id"] = v.ArtifactChainID
		result["part_definitions"] = v.PartDefinitions
		result["composition"] = v.Composition
	}
	return result
}

func cloneArtifactOutputRequirements(input *pebblestore.SessionArtifactOutputRequirements) *pebblestore.SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func cloneArtifactAnimationProfile(input *pebblestore.SessionArtifactAnimationProfile) *pebblestore.SessionArtifactAnimationProfile {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func validateArtifactAnimationMedia(profile *pebblestore.SessionArtifactAnimationProfile, packageArtifact bool, filename, mediaType string) error {
	if profile == nil || profile.ProfileID != "final_render" {
		return nil
	}
	if packageArtifact || canonicalArtifactMediaType(mediaType) != "video/mp4" || !strings.HasSuffix(strings.ToLower(strings.TrimSpace(filename)), ".mp4") {
		return errors.New("animation_profile final_render requires a non-package .mp4 artifact with media_type video/mp4")
	}
	return nil
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

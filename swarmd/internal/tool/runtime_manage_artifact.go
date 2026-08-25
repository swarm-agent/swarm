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
	"io"
	"math"
	"mime"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
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
			"kind":        map[string]any{"type": "string", "enum": []string{"temporal", "spatial", "page", "state", "selector", "semantic"}, "description": "Locator contract for this review target. temporal requires start_ms/end_ms; spatial requires normalized x/y/width/height; page requires page; state requires state_id; selector requires selector; semantic needs no locator beyond id/label/description."},
			"description": map[string]any{"type": "string", "maxLength": 2048, "description": "Concise explanation of the actual authored region or section and the changes it can receive."},
			"start_ms":    map[string]any{"type": "integer", "minimum": 0, "description": "Temporal start in milliseconds; required only for kind=temporal."},
			"end_ms":      map[string]any{"type": "integer", "minimum": 1, "description": "Exclusive temporal end in milliseconds greater than start_ms; required only for kind=temporal."},
			"x":           map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Normalized left coordinate; required only for kind=spatial."},
			"y":           map[string]any{"type": "number", "minimum": 0, "maximum": 1, "description": "Normalized top coordinate; required only for kind=spatial."},
			"width":       map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1, "description": "Normalized width with x+width <= 1; required only for kind=spatial."},
			"height":      map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1, "description": "Normalized height with y+height <= 1; required only for kind=spatial."},
			"page":        map[string]any{"type": "integer", "minimum": 1, "description": "One-based page number; required only for kind=page."},
			"state_id":    map[string]any{"type": "string", "maxLength": 128, "description": "Exact authored state identifier; required only for kind=state."},
			"selector":    map[string]any{"type": "string", "maxLength": 512, "description": "Stable selector for an element in the authored artifact; required only for kind=selector."},
		},
		"required":             []string{"id", "label", "kind"},
		"additionalProperties": false,
	}
	locator := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"kind":     map[string]any{"type": "string", "enum": []string{"temporal", "spatial", "page", "state", "selector", "semantic"}},
			"start_ms": map[string]any{"type": "integer", "minimum": 0},
			"end_ms":   map[string]any{"type": "integer", "minimum": 1},
			"x":        map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"y":        map[string]any{"type": "number", "minimum": 0, "maximum": 1},
			"width":    map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1},
			"height":   map[string]any{"type": "number", "exclusiveMinimum": 0, "maximum": 1},
			"page":     map[string]any{"type": "integer", "minimum": 1},
			"state_id": map[string]any{"type": "string", "maxLength": 128},
			"selector": map[string]any{"type": "string", "maxLength": 512},
		},
		"required":             []string{"kind"},
		"additionalProperties": false,
	}
	replacementPart := map[string]any{
		"type": "object", "properties": map[string]any{
			"part_id":        map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,127}$"},
			"content":        map[string]any{"type": "string"},
			"content_base64": map[string]any{"type": "string"},
			"media_type":     map[string]any{"type": "string", "maxLength": 255},
			"filename":       map[string]any{"type": "string", "maxLength": 255},
			"locked":         map[string]any{"type": "boolean"},
		}, "required": []string{"part_id"}, "additionalProperties": false,
	}
	initialPart := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"id":             map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,127}$", "description": "Stable independently replaceable part identity."},
			"label":          map[string]any{"type": "string", "maxLength": 256},
			"description":    map[string]any{"type": "string", "maxLength": 2048},
			"media_type":     map[string]any{"type": "string", "maxLength": 255, "description": "Media type for this part's independently stored immutable bytes."},
			"content":        map[string]any{"type": "string", "description": "Non-empty UTF-8 bytes for this independent part; mutually exclusive with content_base64."},
			"content_base64": map[string]any{"type": "string", "description": "Non-empty base64 bytes for this independent part; mutually exclusive with content."},
			"locator":        locator,
		},
		"required":             []string{"id", "label", "media_type"},
		"additionalProperties": false,
	}
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
	partChoice := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"part_id": map[string]any{"type": "string"},
			"revision": map[string]any{"type": "object", "properties": map[string]any{
				"artifact_chain_id": map[string]any{"type": "string"}, "part_id": map[string]any{"type": "string"}, "part_revision_id": map[string]any{"type": "string"}, "owner_session_id": map[string]any{"type": "string"}, "digest_sha256": map[string]any{"type": "string"}, "size": map[string]any{"type": "integer", "minimum": 1}, "media_type": map[string]any{"type": "string"},
			}, "required": []string{"artifact_chain_id", "part_id", "part_revision_id", "owner_session_id", "digest_sha256", "size", "media_type"}, "additionalProperties": false},
			"revision_event_seq": map[string]any{"type": "integer", "minimum": 1},
			"locked":             map[string]any{"type": "boolean"},
		},
		"required": []string{"part_id", "revision", "revision_event_seq", "locked"}, "additionalProperties": false,
	}
	reference := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"session_id":    map[string]any{"type": "string"},
			"collection_id": map[string]any{"type": "string"},
			"variant_id":    map[string]any{"type": "string"},
			"event_seq":     map[string]any{"type": "integer", "minimum": 1},
		},
		"required":             []string{"session_id", "collection_id", "variant_id", "event_seq"},
		"additionalProperties": false,
	}
	return Definition{
		Type:        "function",
		Name:        "manage_artifact",
		Description: "Before generating or remixing an image, call action=image_capabilities to read the configured model's current snapshot-backed options and capability_token, then pass only listed options plus that token to action=generate_image. A selected ready image is a reusable exact source for repeated edits: on every remix, copy its source_session_id, source_collection_id, source_variant_id, and source_event_seq together with the new edit request; the authenticated artifact authority supplies bounded source bytes directly to a supported provider, so never replace the source with a preview/download or re-prompt from scratch. export_html_stills accepts one complete exact ready text/html or canonical HTML-package reference containing the swarm.capture/v1 manifest/runtime contract, optionally selects declared state_ids, and returns managed 1920x1080 image/png references in manifest order for direct use as manage_video propose_plan visuals; the trusted renderer removes data-swarm-capture-ui and rejects blockers or unstable states. export_html_animation accepts one complete exact ready HTML/package reference with a reviewed animation profile and the separate swarm.animation/v1 manifest/runtime; long exports return a durable staging reference promptly for list/status inspection or cancel_html_animation_export, then publish one silent managed video/mp4 with exact source lineage after background renderer-controlled sampling. Generate one provider-billed image and publish it directly as a ready V3 managed artifact; create and manage other durable artifacts; inspect exact ready references as bounded text/package data or bounded image base64; and explicitly materialize exact references into the trusted workspace. Use search (or list with cross-session filters) to discover the authenticated user's prior-session artifact library without scanning transcripts or storage folders. Discovery results are flattened explicit candidates, ready items include complete exact references, and next_cursor is an opaque continuation that must be passed back unchanged as cursor. Never infer a selection when human names are ambiguous. Collection-list results are not complete ready references and cannot be passed directly to get/read; when a list result contains only collection metadata, call list again with collection_id (and session_id for an attached cross-session artifact) to list its artifacts and obtain variant_id and event_seq. To retrieve, read, materialize, promote, or export an attached ready artifact, copy session_id, collection_id, variant_id, and event_seq together from the same artifact reference into the call. For repository or other workspace end products, prefer materialize or atomic materialize_batch over bulk read responses, manipulate the imported files with normal workspace tools, then use publish_workspace to publish the finished file or package; copy the original exact reference into source_session_id, source_collection_id, source_variant_id, and source_event_seq when the result derives from one source. Provider/model identifiers, browser/runtime overrides, arbitrary capture dimensions, and private storage paths are never accepted or exposed.",
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"action":           map[string]any{"type": "string", "enum": []string{"image_capabilities", "generate_image", "export_html_stills", "export_html_animation", "cancel_html_animation_export", "read_part", "publish_part", "read_parts", "publish_parts", "select_parts", "create", "create_package", "list_presets", "list", "search", "get", "read", "materialize", "materialize_batch", "promote", "publish_workspace", "select", "delete"}, "description": "Artifact operation. Focused managed Designers use read_part then publish_part for one selected part, or read_parts then publish_parts for a bounded multi-part selection; those actions are bound entirely to trusted exact composition context and publish one atomic candidate. Supports search, materialize/materialize_batch, and publish_workspace. export_html_stills captures declared swarm.capture/v1 states into managed PNGs. export_html_animation captures a bounded deterministic swarm.animation/v1 timeline into one silent managed MP4 valid as a managed video timeline clip."},
				"prompt":           map[string]any{"type": "string", "maxLength": manageArtifactMaxPromptRunes, "description": "Image prompt required only for generate_image. For a remix, describe only the requested changes while preserving the attached exact source through all source_* fields."},
				"capability_token": map[string]any{"type": "string", "description": "Fresh token returned by image_capabilities; required for each Google generate_image call, including every repeated remix"},
				"image_settings": map[string]any{"type": "object", "properties": map[string]any{
					"size":         map[string]any{"type": "string", "maxLength": 64, "description": "Optional portable output size, for example auto, 1024x1024, 1536x1024, or 1024x1536. The backend adapts it to the configured image provider."},
					"aspect_ratio": map[string]any{"type": "string", "maxLength": 32, "description": "Optional aspect ratio: 1:1, 2:3, 3:2, 3:4, 4:3, 9:16, 16:9, or 21:9."},
					"image_size":   map[string]any{"type": "string", "maxLength": 32, "description": "Optional portable resolution tier: 512, 1K, 2K, or 4K. Equivalent square pixel aliases such as 1024x1024, 2048x2048, and 4096x4096 are accepted. The backend translates this for the configured provider."},
				}, "additionalProperties": false, "description": "Optional provider-neutral image output controls. Omit unless the user requested a size or aspect ratio. The backend resolves the account's configured provider/model and translates these controls; never pass provider or model."},
				"session_id":             map[string]any{"type": "string", "description": "Authenticated source session. For get/read/materialize/promote of an attached ready artifact, copy this together with collection_id, variant_id, and event_seq from the same returned reference."},
				"collection_id":          map[string]any{"type": "string", "description": "Opaque collection reference. For get/read/materialize/promote of an attached ready artifact, copy this together with session_id, variant_id, and event_seq from the same returned reference. For standalone generate_image, omit to create a new collection or pass an existing collection to append the generated variant without replacing collection metadata. Required for collection-scoped actions."},
				"collection_name":        map[string]any{"type": "string", "maxLength": 256},
				"collection_description": map[string]any{"type": "string", "maxLength": 2048},
				"variant_id":             map[string]any{"type": "string", "description": "Opaque variant reference. For get/read/materialize/promote of an attached ready artifact, copy this together with session_id, collection_id, and event_seq from the same returned reference; otherwise optional on create."},
				"filename":               map[string]any{"type": "string", "maxLength": 255},
				"media_type":             map[string]any{"type": "string", "maxLength": 255, "description": "Artifact media type for create; optional exact canonical media type filter for list/search discovery"},
				"content":                map[string]any{"type": "string", "description": "Bounded UTF-8 artifact content for monolithic create or focused publish_part replacement bytes"},
				"content_base64":         map[string]any{"type": "string", "description": "Bounded base64 replacement bytes for focused publish_part; mutually exclusive with content"},
				"part_choices":           map[string]any{"type": "array", "minItems": 1, "maxItems": pebblestore.SessionArtifactMaxParts, "items": partChoice, "description": "Exact immutable part revisions and desired lock states to combine atomically into one accepted complete composition."},
				"replacements":           map[string]any{"type": "array", "minItems": 1, "maxItems": pebblestore.SessionArtifactMaxParts, "items": replacementPart, "description": "Canonical publish_parts payload. Include exactly one replacement for every authenticated selected part; publication is one atomic candidate composition."},
				"entries":                map[string]any{"type": "array", "maxItems": manageArtifactMaxPackageFiles, "items": entry},
				"initial_parts":          map[string]any{"type": "array", "minItems": 2, "maxItems": pebblestore.SessionArtifactMaxParts, "items": initialPart, "description": "Two or more real independently byte-bearing initial parts for create. Every item has its own stable id, media type, and non-empty content/content_base64. The server owns all chain, composition, and part-revision identities. Mutually exclusive with top-level content, entries, and locator-only parts."},
				"parts":                  map[string]any{"type": "array", "maxItems": pebblestore.SessionArtifactMaxParts, "items": part, "description": "Optional source-bound review/edit targets on one complete monolithic artifact. These locators never create or prove independently replaceable bytes. For text/html, omit parts to let the server derive useful targets from swarm.iteration/v1 sections, swarm.capture/v1 states, and stable IDs on semantic HTML regions without splitting or rewriting the file; explicitly supplied parts remain authoritative for the complete revision. Use initial_parts only when independently stored byte payloads are intentionally required."},
				"references":             map[string]any{"type": "array", "minItems": 1, "maxItems": manageArtifactMaxBatchItems, "items": reference, "description": "Complete exact ready references from discovery results, imported atomically by materialize_batch into one destination directory. The whole batch is preflighted; filenames come from trusted artifact metadata."},
				"state_ids":              map[string]any{"type": "array", "minItems": 1, "maxItems": 16, "uniqueItems": true, "items": map[string]any{"type": "string", "pattern": "^[a-z0-9][a-z0-9._-]{0,63}$"}, "description": "Optional bounded declared swarm.capture/v1 state IDs for export_html_stills; omitted exports every manifest state. Caller order never overrides canonical manifest order."},
				"source":                 map[string]any{"type": "string", "maxLength": 4096, "description": "Trusted canonical workspace-relative regular file or bounded package directory for publish_workspace. Build or revise it with normal workspace tools before publication."},
				"presentation":           presentation,
				"output_requirements":    artifact.OutputRequirementsToolSchema(),
				"animation_profile":      artifact.AnimationProfileToolSchema(),
				"source_session_id":      map[string]any{"type": "string", "description": "For every image remix or lineage operation, copy the authenticated source session from the reusable exact ready attached-artifact reference together with all other source_* fields"},
				"source_collection_id":   map[string]any{"type": "string", "description": "For every image remix or lineage operation, copy the opaque source collection from the same reusable exact ready reference"},
				"source_variant_id":      map[string]any{"type": "string", "description": "For every image remix or lineage operation, copy the opaque source variant from the same reusable exact ready reference"},
				"source_event_seq":       map[string]any{"type": "integer", "minimum": 1, "description": "For every image remix or lineage operation, copy the exact ready event sequence from the same reusable source reference"},
				"event_seq":              map[string]any{"type": "integer", "minimum": 1, "description": "Exact ready event sequence. For get/read/materialize/promote/export_html_stills/export_html_animation of an attached ready artifact, copy this together with session_id, collection_id, and variant_id from the same returned reference."},
				"query":                  map[string]any{"type": "string", "maxLength": 1024, "description": "Optional authenticated cross-session metadata search across collection and variant display fields; search results are explicit candidates and ready items carry complete exact references."},
				"status":                 map[string]any{"type": "string", "description": "Optional list/search filter: staging|ready|failed|unavailable"},
				"created_after":          map[string]any{"type": "integer", "minimum": 0, "description": "Optional inclusive variant created_at lower bound in Unix milliseconds for cross-session discovery"},
				"created_before":         map[string]any{"type": "integer", "minimum": 0, "description": "Optional inclusive variant created_at upper bound in Unix milliseconds for cross-session discovery"},
				"cursor":                 map[string]any{"type": "string", "description": "Opaque cross-session discovery continuation cursor. Copy next_cursor from the prior search/list response unchanged; never parse or construct it."},
				"limit":                  map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxListLimit, "description": "Maximum list/search items for this page; use next_cursor/cursor to continue cross-session discovery."},
				"max_bytes":              map[string]any{"type": "integer", "minimum": 1, "maximum": manageArtifactMaxImageReadBytes, "description": "Maximum bytes returned by read for bounded inspection. Text/package entries are capped at 256 KiB; supported ready images are capped at 16 MiB and returned as base64. A response-quota error does not mean the artifact is unavailable; use materialize for workspace use instead of bulk-reading it."},
				"entry":                  map[string]any{"type": "string", "maxLength": 1024, "description": "Optional normalized slash-delimited regular-file entry for application/zip read; omit to return the bounded package manifest. Materialize the package for whole-package workspace work."},
				"destination":            map[string]any{"type": "string", "maxLength": 4096, "description": "Canonical workspace-relative file or directory destination required for materialize/promote and materialize_batch; overwrite defaults to false."},
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
	if actionName != "create" && actionName != "create_package" {
		if _, supplied := args["animation_profile"]; supplied {
			return "", errors.New("manage_artifact animation_profile is valid only for create or create_package")
		}
	}
	if actionName != "list_presets" && actionName != "image_capabilities" && r.artifactAuthority == nil {
		return "", errors.New("manage_artifact authority is not configured")
	}

	switch actionName {
	case "image_capabilities":
		capabilities, err := r.managedImageCapabilities(principal.AccountScopeID)
		if err != nil {
			return "", err
		}
		response["image_capabilities"] = capabilities
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
		exports, sourceRef, requirements, err := r.exportHTMLStills(ctx, principal, callID, args)
		if err != nil {
			return "", err
		}
		response["source_reference"] = managedArtifactReferenceWithSession(sourceRef.SessionID, sourceRef.CollectionID, sourceRef.VariantID, sourceRef.EventSeq)
		response["output_requirements"] = requirements
		response["exports"], response["count"] = exports, len(exports)
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
		initialParts, err := parseArtifactInitialParts(args["initial_parts"], principal.SessionID, input.CollectionID, input.VariantID, callID)
		if err != nil {
			return "", err
		}
		var variant pebblestore.SessionArtifactVariant
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
					entry["reference"] = managedArtifactReferenceWithSession(item.Reference.SessionID, item.Reference.CollectionID, item.Reference.VariantID, item.Reference.EventSeq)
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
		case "action", "source", "collection_id", "collection_name", "collection_description", "filename", "media_type", "presentation", "output_requirements", "source_session_id", "source_collection_id", "source_variant_id", "source_event_seq":
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
			inheritedAnimationProfile = cloneArtifactAnimationProfile(canonicalProfile)
		}
	}
	if err := validateArtifactAnimationMedia(inheritedAnimationProfile, packageSource, filename, mediaType); err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	create := artifact.CreateInput{
		RequestID: requestID, CollectionID: collectionID, CollectionName: strings.TrimSpace(asString(args["collection_name"])), CollectionDescription: strings.TrimSpace(asString(args["collection_description"])),
		VariantID: variantID, Filename: filename, MediaType: mediaType, Presentation: presentation, OutputRequirements: requirements, AnimationProfile: inheritedAnimationProfile, AutoAccept: true,
		SourceSessionID: sourceSessionID, SourceCollectionID: sourceCollectionID, SourceVariantID: sourceVariantID, SourceEventSeq: sourceEventSeq,
	}
	if generatedCollection && create.CollectionName == "" {
		create.CollectionName = "Workspace publication"
	}
	if !generatedCollection {
		create.CollectionName, create.CollectionDescription = "", ""
	}
	variant, err := r.artifactAuthority.PublishWorkspace(ctx, principal, artifact.CreateFileInput{CreateInput: create, SourcePath: absoluteSource, Package: packageSource})
	if err != nil {
		return pebblestore.SessionArtifactVariant{}, nil, err
	}
	published := map[string]any{"source": filepath.ToSlash(source), "package": packageSource, "files": sourceInfo.files, "bytes": sourceInfo.bytes, "digest_sha256": variant.DigestSHA256, "media_type": variant.MediaType}
	return variant, published, nil
}

type workspacePublishSourceInfo struct {
	files int
	bytes int64
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
		line = strings.TrimPrefix(filepath.ToSlash(line), "/")
		if line == "" {
			continue
		}
		directory := strings.HasSuffix(line, "/")
		line = strings.TrimSuffix(line, "/")
		matched, _ := path.Match(line, relative)
		if !strings.Contains(line, "/") {
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

func (r *Runtime) generateManagedImageArtifact(ctx context.Context, scope WorkspaceScope, principal artifact.Principal, callID, requestID string, args map[string]any) (pebblestore.SessionArtifactVariant, error) {
	for key := range args {
		switch key {
		case "action", "prompt", "image_settings", "capability_token", "collection_id", "collection_name", "collection_description", "variant_id", "filename", "presentation", "output_requirements", "source_session_id", "source_collection_id", "source_variant_id", "source_event_seq":
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
	generated, err := r.imageGeneration.GenerateManagedImage(identity.ContextWithPrincipal(ctx, scope.Principal), imagegen.ManagedGenerateRequest{
		SelectionID: selectionID, Prompt: prompt, Size: size, Settings: settings,
		CapabilityToken: strings.TrimSpace(asString(args["capability_token"])), Principal: scope.Principal, Source: source,
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
	return r.artifactAuthority.Create(ctx, principal, create)
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
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || (index > 0 && (character == '_' || character == '-' || character == '.')) {
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
	items, ok := raw.([]any)
	if !ok || len(items) > pebblestore.SessionArtifactMaxParts {
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
	result := map[string]any{"id": v.ID, "collection_id": v.CollectionID, "session_id": v.SessionID, "status": v.Status, "filename": v.Filename, "media_type": v.MediaType, "digest_sha256": v.DigestSHA256, "size": v.Size, "failure_code": v.FailureCode, "presentation": managedArtifactPresentation(v.Presentation), "output_requirements": v.OutputRequirements, "animation_profile": v.AnimationProfile, "part_graph_state": v.PartGraphState, "created_at": v.CreatedAt, "updated_at": v.UpdatedAt, "event_seq": v.EventSeq}
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

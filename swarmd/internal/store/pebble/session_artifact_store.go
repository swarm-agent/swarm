package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/cockroachdb/pebble"

	"swarm/packages/swarmd/internal/privacy"
)

const (
	SessionArtifactVersion                  = 1
	SessionArtifactMaxVariantsPerCollection = 128
	SessionArtifactMaxList                  = 256
	SessionArtifactMaxMessageSelections     = 16
	SessionArtifactMaxParts                 = 64
	SessionArtifactMaxStepCandidates        = 128
	SessionArtifactMaxCommitParents         = 64
	SessionArtifactMaxLocks                 = 64

	SessionArtifactGraphProjection = "git_projection"
	// SessionArtifactGraphAuthoritative is retained as a source compatibility
	// name while its value explicitly describes rebuildable Git projection state.
	SessionArtifactGraphAuthoritative = SessionArtifactGraphProjection
	// LegacyUnproven is read-only migration vocabulary. New mutation validation
	// never emits or accepts it as an execution authority.
	SessionArtifactGraphLegacyUnproven = "legacy_unproven"

	SessionArtifactStatusStaging     = "staging"
	SessionArtifactStatusReady       = "ready"
	SessionArtifactStatusFailed      = "failed"
	SessionArtifactStatusUnavailable = "unavailable"
)

// SessionArtifactLineage records durable generation ancestry without granting
// ownership. Account, user, and session ownership always comes from the trusted
// V3 mutation envelope and is not model-authored artifact metadata.
type SessionArtifactLineage struct {
	// ParentSessionID is the trusted destination session that owns the
	// collection. SourceSessionID identifies the producing child when output is
	// routed into a parent-owned managed collection.
	ParentSessionID         string `json:"parent_session_id,omitempty"`
	SourceSessionID         string `json:"source_session_id,omitempty"`
	SourceCollectionID      string `json:"source_collection_id,omitempty"`
	SourceVariantID         string `json:"source_variant_id,omitempty"`
	SourceEventSeq          uint64 `json:"source_event_seq,omitempty"`
	TaskCallID              string `json:"task_call_id,omitempty"`
	ProgramID               string `json:"program_id,omitempty"`
	ProgramJobID            string `json:"program_job_id,omitempty"`
	ChildSessionID          string `json:"child_session_id,omitempty"`
	IterationGroupID        string `json:"iteration_group_id,omitempty"`
	IterationGroup          string `json:"iteration_group,omitempty"`
	IterationID             string `json:"iteration_id,omitempty"`
	IterationIndex          int    `json:"iteration_index,omitempty"`
	IterationLabel          string `json:"iteration_label,omitempty"`
	IterationTheme          string `json:"iteration_theme,omitempty"`
	IterationSectionID      string `json:"iteration_section_id,omitempty"`
	IterationSectionLabel   string `json:"iteration_section_label,omitempty"`
	IterationSectionStartMs int64  `json:"iteration_section_start_ms,omitempty"`
	IterationSectionEndMs   int64  `json:"iteration_section_end_ms,omitempty"`
	PartID                  string `json:"part_id,omitempty"`
	PartLabel               string `json:"part_label,omitempty"`
	PartKind                string `json:"part_kind,omitempty"`
	SelectedReviewTargetIDs string `json:"selected_review_target_ids,omitempty"`
	RunID                   string `json:"run_id,omitempty"`
	PlanID                  string `json:"plan_id,omitempty"`
	CheckpointID            string `json:"checkpoint_id,omitempty"`
	AttemptID               string `json:"attempt_id,omitempty"`
	VideoProjectID          string `json:"video_project_id,omitempty"`
	VideoRevisionID         string `json:"video_revision_id,omitempty"`
	VideoRevisionEventSeq   uint64 `json:"video_revision_event_seq,omitempty"`
}

// SessionArtifactPresentation contains bounded client display hints. It is
// metadata only and cannot name a filesystem location or embed artifact bytes.
type SessionArtifactPresentation struct {
	Kind        string `json:"kind,omitempty"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Previewable bool   `json:"previewable,omitempty"`
	Width       int    `json:"width,omitempty"`
	Height      int    `json:"height,omitempty"`
}

// SessionArtifactOutputRequirements is the immutable resolved target supplied by
// trusted orchestration. It is distinct from model-authored Presentation hints.
type SessionArtifactOutputRequirements struct {
	PresetID         string `json:"preset_id,omitempty"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	AspectRatio      string `json:"aspect_ratio"`
	Orientation      string `json:"orientation"`
	ResolutionSource string `json:"resolution_source"`
	RegistryVersion  string `json:"registry_version"`
}

// SessionArtifactAnimationProfile is the immutable, server-resolved animation
// execution contract. Model-authored input selects only ProfileID; runtimes and
// budgets are copied from the closed server registry.
type SessionArtifactAnimationProfile struct {
	ProfileID               string                          `json:"profile_id"`
	RegistryVersion         string                          `json:"registry_version"`
	RuntimeKind             string                          `json:"runtime_kind"`
	RuntimePackage          string                          `json:"runtime_package,omitempty"`
	RuntimeVersion          string                          `json:"runtime_version,omitempty"`
	SecondaryRuntimePackage string                          `json:"secondary_runtime_package,omitempty"`
	SecondaryRuntimeVersion string                          `json:"secondary_runtime_version,omitempty"`
	Heavy                   bool                            `json:"heavy,omitempty"`
	ImportedPlaybackOnly    bool                            `json:"imported_playback_only,omitempty"`
	EditableSourceRequired  bool                            `json:"editable_source_required,omitempty"`
	Budgets                 SessionArtifactAnimationBudgets `json:"budgets"`
}

type SessionArtifactAnimationBudgets struct {
	MaxSimultaneousLivePreviews int     `json:"max_simultaneous_live_previews"`
	MaxWebGLContexts            int     `json:"max_webgl_contexts"`
	MaxDevicePixelRatio         float64 `json:"max_device_pixel_ratio"`
	MaxCanvasPixels             int     `json:"max_canvas_pixels"`
	MaxParticles                int     `json:"max_particles"`
	MaxDrawCallsPerFrame        int     `json:"max_draw_calls_per_frame"`
	PauseWhenOffscreen          bool    `json:"pause_when_offscreen"`
	StopWhenDocumentHidden      bool    `json:"stop_when_document_hidden"`
	ReducedMotionBehavior       string  `json:"reduced_motion_behavior"`
	NetworkAllowed              bool    `json:"network_allowed"`
}

// SessionArtifactPart is legacy locator-only review metadata. It is retained for
// rendering historical records, but it is never authoritative part identity or
// byte-preservation metadata. New composed revisions use PartDefinitions and
// Composition.
type SessionArtifactPart struct {
	ID          string  `json:"id"`
	Label       string  `json:"label"`
	Kind        string  `json:"kind"`
	Description string  `json:"description,omitempty"`
	StartMs     int64   `json:"start_ms,omitempty"`
	EndMs       int64   `json:"end_ms,omitempty"`
	X           float64 `json:"x,omitempty"`
	Y           float64 `json:"y,omitempty"`
	Width       float64 `json:"width,omitempty"`
	Height      float64 `json:"height,omitempty"`
	Page        int     `json:"page,omitempty"`
	StateID     string  `json:"state_id,omitempty"`
	Selector    string  `json:"selector,omitempty"`
}

// SessionArtifactChain is a rebuildable catalog projection. OfficialCommitOID
// and OfficialRef mirror the authoritative Git ref; neither field makes Pebble a
// graph or byte authority.
type SessionArtifactChain struct {
	Version           int                               `json:"version"`
	GraphState        string                            `json:"graph_state"`
	ID                string                            `json:"id"`
	AccountScopeID    string                            `json:"account_scope_id"`
	UserID            string                            `json:"user_id"`
	Name              string                            `json:"name,omitempty"`
	RepositoryID      string                            `json:"repository_id"`
	OfficialRef       string                            `json:"official_ref"`
	OfficialCommitOID string                            `json:"official_commit_oid,omitempty"`
	Root              SessionArtifactSelectionReference `json:"root,omitempty"`
	Head              SessionArtifactSelectionReference `json:"head,omitempty"`
	RevisionCount     int                               `json:"revision_count"`
	LastRoundID       string                            `json:"last_round_id,omitempty"`
	CreatedAt         int64                             `json:"created_at"`
	UpdatedAt         int64                             `json:"updated_at"`
	EventSeq          uint64                            `json:"event_seq"`
}

// SessionArtifactStep projects one Git transaction and its candidate refs. The
// parent list is copied from Git and supports arbitrary forks and merges.
type SessionArtifactStep struct {
	Version          int                                 `json:"version"`
	GraphState       string                              `json:"graph_state"`
	ID               string                              `json:"id"`
	ArtifactChainID  string                              `json:"artifact_chain_id"`
	AccountScopeID   string                              `json:"account_scope_id"`
	UserID           string                              `json:"user_id"`
	RepositoryID     string                              `json:"repository_id"`
	TransactionRef   string                              `json:"transaction_ref"`
	CandidateRef     string                              `json:"candidate_ref,omitempty"`
	CommitOID        string                              `json:"commit_oid,omitempty"`
	ParentCommitOIDs []string                            `json:"parent_commit_oids,omitempty"`
	ExpectedOldOID   string                              `json:"expected_old_oid,omitempty"`
	ResultingOID     string                              `json:"resulting_oid,omitempty"`
	Parent           SessionArtifactSelectionReference   `json:"parent,omitempty"`
	RevisionNumber   int                                 `json:"revision_number"`
	Candidates       []SessionArtifactSelectionReference `json:"candidates"`
	Accepted         *SessionArtifactSelectionReference  `json:"accepted,omitempty"`
	CreatedAt        int64                               `json:"created_at"`
	UpdatedAt        int64                               `json:"updated_at"`
	EventSeq         uint64                              `json:"event_seq"`
}

const SessionArtifactRoleRenderOnly = "render_only"

type SessionArtifactProgress struct {
	Stage                string  `json:"stage"`
	Completed            int     `json:"completed,omitempty"`
	Total                int     `json:"total,omitempty"`
	Percent              float64 `json:"percent,omitempty"`
	ElapsedMS            int64   `json:"elapsed_ms,omitempty"`
	EstimatedRemainingMS int64   `json:"estimated_remaining_ms,omitempty"`
	HeartbeatAt          int64   `json:"heartbeat_at"`
}

type SessionArtifactVariant struct {
	Version               int                                `json:"version"`
	ID                    string                             `json:"id"`
	CollectionID          string                             `json:"collection_id"`
	AccountScopeID        string                             `json:"account_scope_id"`
	SessionID             string                             `json:"session_id"`
	Status                string                             `json:"status"`
	Filename              string                             `json:"filename,omitempty"`
	MediaType             string                             `json:"media_type,omitempty"`
	Role                  string                             `json:"role,omitempty"`
	DigestSHA256          string                             `json:"digest_sha256,omitempty"`
	Size                  int64                              `json:"size,omitempty"`
	FailureCode           string                             `json:"failure_code,omitempty"`
	Progress              *SessionArtifactProgress           `json:"progress,omitempty"`
	Lineage               SessionArtifactLineage             `json:"lineage,omitempty"`
	Presentation          SessionArtifactPresentation        `json:"presentation,omitempty"`
	OutputRequirements    *SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	AnimationProfile      *SessionArtifactAnimationProfile   `json:"animation_profile,omitempty"`
	ArtifactChainID       string                             `json:"artifact_chain_id,omitempty"`
	ArtifactStepID        string                             `json:"artifact_step_id,omitempty"`
	RepositoryID          string                             `json:"repository_id,omitempty"`
	CommitOID             string                             `json:"commit_oid,omitempty"`
	TreeOID               string                             `json:"tree_oid,omitempty"`
	CandidateRef          string                             `json:"candidate_ref,omitempty"`
	ParentCommitOIDs      []string                           `json:"parent_commit_oids,omitempty"`
	GraphState            string                             `json:"graph_state,omitempty"`
	PartGraphState        string                             `json:"part_graph_state,omitempty"`
	ParentArtifact        *SessionArtifactSelectionReference `json:"parent_artifact,omitempty"`
	RevisionNumber        int                                `json:"revision_number,omitempty"`
	RevisionRoundID       string                             `json:"revision_round_id,omitempty"`
	CandidateIndex        int                                `json:"candidate_index,omitempty"`
	AutoAccept            bool                               `json:"auto_accept,omitempty"`
	ProjectionReservation bool                               `json:"projection_reservation,omitempty"`
	// Parts is legacy locator-only review metadata. It cannot prove part bytes.
	Parts           []SessionArtifactPart           `json:"parts,omitempty"`
	PartDefinitions []SessionArtifactPartDefinition `json:"part_definitions,omitempty"`
	Composition     *SessionArtifactComposition     `json:"composition,omitempty"`
	CreatedAt       int64                           `json:"created_at"`
	UpdatedAt       int64                           `json:"updated_at"`
	EventSeq        uint64                          `json:"event_seq"`
}

type SessionArtifactCollection struct {
	Version           int                         `json:"version"`
	ID                string                      `json:"id"`
	AccountScopeID    string                      `json:"account_scope_id"`
	SessionID         string                      `json:"session_id"`
	Status            string                      `json:"status"`
	Name              string                      `json:"name"`
	Description       string                      `json:"description,omitempty"`
	Lineage           SessionArtifactLineage      `json:"lineage,omitempty"`
	Presentation      SessionArtifactPresentation `json:"presentation,omitempty"`
	VariantCount      int                         `json:"variant_count"`
	StagingCount      int                         `json:"staging_count"`
	ReadyCount        int                         `json:"ready_count"`
	FailedCount       int                         `json:"failed_count"`
	UnavailableCount  int                         `json:"unavailable_count"`
	SelectedVariantID string                      `json:"selected_variant_id,omitempty"`
	CreatedAt         int64                       `json:"created_at"`
	UpdatedAt         int64                       `json:"updated_at"`
	EventSeq          uint64                      `json:"event_seq"`
}

// SessionArtifactSelectionReference is the portable, opaque reference placed in
// messages and handoffs. Label and description are bounded display metadata;
// PendingRequest is hidden Studio context. The reference contains no bytes,
// digest, or storage path.
type SessionArtifactSelectionReference struct {
	ArtifactID              string               `json:"artifact_id,omitempty"`
	RevisionRef             string               `json:"revision_ref,omitempty"`
	TargetPartIDs           *[]string            `json:"target_part_ids,omitempty"`
	CommitOID               string               `json:"commit_oid,omitempty"`
	ProjectionSeq           uint64               `json:"projection_seq,omitempty"`
	SessionID               string               `json:"session_id"`
	CollectionID            string               `json:"collection_id,omitempty"`
	VariantID               string               `json:"variant_id,omitempty"`
	EventSeq                uint64               `json:"event_seq,omitempty"`
	Label                   string               `json:"label,omitempty"`
	Description             string               `json:"description,omitempty"`
	PendingRequest          string               `json:"pending_request,omitempty"`
	Action                  string               `json:"action,omitempty"`
	IterationID             string               `json:"iteration_id,omitempty"`
	IterationIndex          int                  `json:"iteration_index,omitempty"`
	IterationLabel          string               `json:"iteration_label,omitempty"`
	IterationTheme          string               `json:"iteration_theme,omitempty"`
	IterationSectionID      string               `json:"iteration_section_id,omitempty"`
	IterationSectionLabel   string               `json:"iteration_section_label,omitempty"`
	IterationSectionStartMs int64                `json:"iteration_section_start_ms,omitempty"`
	IterationSectionEndMs   int64                `json:"iteration_section_end_ms,omitempty"`
	PartID                  string               `json:"part_id,omitempty"`
	PartLabel               string               `json:"part_label,omitempty"`
	PartKind                string               `json:"part_kind,omitempty"`
	Part                    *SessionArtifactPart `json:"part,omitempty"`
}

// ValidateSessionArtifactMessageSelections resolves portable message references
// against authoritative artifact metadata. The returned values are normalized,
// bounded display metadata only; managed bytes and private storage paths never
// enter the message envelope.
func (s *SessionStore) ValidateSessionArtifactMessageSelections(accountScopeID, userID string, selections []SessionArtifactSelectionReference) ([]SessionArtifactSelectionReference, error) {
	if len(selections) == 0 {
		return nil, nil
	}
	if len(selections) > SessionArtifactMaxMessageSelections {
		return nil, errors.New("message artifact selection count limit exceeded")
	}
	accountScopeID, userID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID)
	if accountScopeID == "" || userID == "" {
		return nil, errors.New("artifact selection ownership is required")
	}
	out := make([]SessionArtifactSelectionReference, 0, len(selections))
	seen := make(map[string]struct{}, len(selections))
	for index, incoming := range selections {
		if incoming.ArtifactID != "" || incoming.RevisionRef != "" || incoming.TargetPartIDs != nil || incoming.CommitOID != "" || incoming.ProjectionSeq != 0 {
			ref, err := s.validateNativeArtifactMessageSelection(accountScopeID, userID, incoming)
			if err != nil {
				return nil, fmt.Errorf("artifact selection %d: %w", index, err)
			}
			key := strings.Join([]string{"native", ref.SessionID, ref.ArtifactID}, "\x00")
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("artifact selection %d is duplicated", index)
			}
			seen[key] = struct{}{}
			out = append(out, ref)
			continue
		}
		ref := SessionArtifactSelectionReference{
			SessionID: strings.TrimSpace(incoming.SessionID), CollectionID: strings.TrimSpace(incoming.CollectionID),
			VariantID: strings.TrimSpace(incoming.VariantID), EventSeq: incoming.EventSeq,
			Label: strings.TrimSpace(incoming.Label), Description: strings.TrimSpace(incoming.Description),
			PendingRequest: strings.TrimSpace(incoming.PendingRequest), Action: strings.ToLower(strings.TrimSpace(incoming.Action)),
			PartID: strings.TrimSpace(incoming.PartID),
		}
		if len(ref.SessionID) > 256 || ref.SessionID == "" || ref.SessionID == "." || ref.SessionID == ".." || strings.ContainsAny(ref.SessionID, `/\\`) {
			return nil, fmt.Errorf("artifact selection %d session id is invalid", index)
		}
		if err := validateArtifactID("selection collection", ref.CollectionID); err != nil {
			return nil, fmt.Errorf("artifact selection %d: %w", index, err)
		}
		if err := validateArtifactID("selection variant", ref.VariantID); err != nil {
			return nil, fmt.Errorf("artifact selection %d: %w", index, err)
		}
		if ref.EventSeq == 0 {
			return nil, fmt.Errorf("artifact selection %d event sequence is required", index)
		}
		if len(ref.Label) > 256 || len(ref.Description) > 2048 || len(ref.PendingRequest) > 16<<10 || len(ref.PartID) > 128 {
			return nil, fmt.Errorf("artifact selection %d message context exceeds bounds", index)
		}
		ref.Label = strings.TrimSpace(privacy.SanitizeText(ref.Label))
		ref.Description = strings.TrimSpace(privacy.SanitizeText(ref.Description))
		ref.PendingRequest = strings.TrimSpace(privacy.SanitizeText(ref.PendingRequest))
		if ref.Action == "" {
			ref.Action = "use"
		}
		if ref.Action != "select" && ref.Action != "use" {
			return nil, fmt.Errorf("artifact selection %d action must be select or use", index)
		}
		if ref.PendingRequest != "" && ref.Action != "use" {
			return nil, fmt.Errorf("artifact selection %d pending request requires use action", index)
		}
		key := strings.Join([]string{ref.SessionID, ref.CollectionID, ref.VariantID}, "\x00")
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("artifact selection %d is duplicated", index)
		}
		seen[key] = struct{}{}
		session, ok, err := s.GetSession(ref.SessionID)
		if err != nil {
			return nil, err
		}
		if !ok || session.AccountScopeID != accountScopeID || session.UserID != userID {
			return nil, fmt.Errorf("artifact selection %d source session is not owned by the principal", index)
		}
		collection, ok, err := s.GetSessionArtifactCollection(accountScopeID, ref.SessionID, ref.CollectionID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("artifact selection %d collection was not found", index)
		}
		variant, ok, err := s.GetSessionArtifactVariant(accountScopeID, ref.SessionID, ref.CollectionID, ref.VariantID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("artifact selection %d variant was not found", index)
		}
		if variant.Status != SessionArtifactStatusReady {
			return nil, fmt.Errorf("artifact selection %d variant is not ready", index)
		}
		readySequence := variant.EventSeq == ref.EventSeq
		selectedSequence := collection.SelectedVariantID == variant.ID && collection.EventSeq == ref.EventSeq
		if !readySequence && !selectedSequence {
			return nil, fmt.Errorf("artifact selection %d event sequence is stale", index)
		}
		if ref.Label == "" {
			ref.Label = firstNonEmptyArtifactString(variant.Presentation.Label, collection.Name, variant.Filename)
		}
		if ref.Description == "" {
			ref.Description = firstNonEmptyArtifactString(variant.Presentation.Description, collection.Description)
		}
		ref.Label = strings.TrimSpace(privacy.SanitizeText(ref.Label))
		ref.Description = strings.TrimSpace(privacy.SanitizeText(ref.Description))
		// Selection lineage is always copied from the authenticated exact variant.
		// Never trust client-supplied iteration or section metadata for provider context.
		ref.IterationID = variant.Lineage.IterationID
		ref.IterationIndex = variant.Lineage.IterationIndex
		ref.IterationLabel = variant.Lineage.IterationLabel
		ref.IterationTheme = variant.Lineage.IterationTheme
		ref.IterationSectionID = variant.Lineage.IterationSectionID
		ref.IterationSectionLabel = variant.Lineage.IterationSectionLabel
		ref.IterationSectionStartMs = variant.Lineage.IterationSectionStartMs
		ref.IterationSectionEndMs = variant.Lineage.IterationSectionEndMs
		if ref.PartID != "" {
			foundPart := false
			if variant.PartGraphState == SessionArtifactGraphAuthoritative && variant.Composition != nil {
				for _, slot := range variant.Composition.Parts {
					if slot.PartID != ref.PartID {
						continue
					}
					definition, ok, definitionErr := s.GetSessionArtifactPartDefinition(accountScopeID, userID, slot.DefinitionOwnerSessionID, variant.Composition.ArtifactChainID, slot.PartID)
					if definitionErr != nil {
						return nil, definitionErr
					}
					if !ok || definition.GraphState != SessionArtifactGraphAuthoritative {
						break
					}
					part := SessionArtifactPart{ID: definition.ID, Label: definition.Label, Description: definition.Description, Kind: "semantic"}
					if locator := definition.Locator; locator != nil {
						part.Kind, part.StartMs, part.EndMs = locator.Kind, locator.StartMs, locator.EndMs
						part.X, part.Y, part.Width, part.Height = locator.X, locator.Y, locator.Width, locator.Height
						part.Page, part.StateID, part.Selector = locator.Page, locator.StateID, locator.Selector
					}
					ref.PartLabel, ref.PartKind, ref.Part, foundPart = part.Label, part.Kind, &part, true
					break
				}
			} else {
				// Locator-only review parts are exact metadata on this authenticated
				// immutable variant even though they are not independently stored
				// composition parts. Preserve that exact target for Studio changes.
				for _, declared := range variant.Parts {
					if strings.TrimSpace(declared.ID) != ref.PartID {
						continue
					}
					part := declared
					ref.PartLabel, ref.PartKind, ref.Part, foundPart = strings.TrimSpace(part.Label), strings.TrimSpace(part.Kind), &part, true
					break
				}
			}
			if !foundPart {
				return nil, fmt.Errorf("artifact selection %d part was not found on the exact revision", index)
			}
		}
		out = append(out, ref)
	}
	return out, nil
}

func firstNonEmptyArtifactString(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

// V3ArtifactMutation is the metadata payload accepted only by artifact V3
// mutation kinds. Embedded ownership is ignored and replaced by the trusted
// mutation envelope before persistence.
type V3ArtifactMutation struct {
	// ProjectionOnly reserves catalog rows before any Git bytes or commit exist.
	// It is excluded from JSON so only trusted in-process orchestration can use it.
	ProjectionOnly  bool                               `json:"-"`
	Collection      SessionArtifactCollection          `json:"collection"`
	Variant         *SessionArtifactVariant            `json:"variant,omitempty"`
	Selection       *SessionArtifactSelectionReference `json:"selection,omitempty"`
	Chain           *SessionArtifactChain              `json:"chain,omitempty"`
	Step            *SessionArtifactStep               `json:"step,omitempty"`
	Transaction     *SessionArtifactGitTransaction     `json:"transaction,omitempty"`
	Locks           []SessionArtifactGitLock           `json:"locks,omitempty"`
	PartDefinitions []SessionArtifactPartDefinition    `json:"part_definitions,omitempty"`
	PartRevisions   []SessionArtifactPartRevision      `json:"part_revisions,omitempty"`
	Composition     *SessionArtifactComposition        `json:"composition,omitempty"`
}

// V3ArtifactProjection is safe for session events and realtime delivery. It is
// a metadata snapshot with no body bytes, storage keys, or filesystem paths.
type V3ArtifactProjection struct {
	Collection      SessionArtifactCollection          `json:"collection"`
	Variant         *SessionArtifactVariant            `json:"variant,omitempty"`
	Selection       *SessionArtifactSelectionReference `json:"selection,omitempty"`
	Chain           *SessionArtifactChain              `json:"chain,omitempty"`
	Step            *SessionArtifactStep               `json:"step,omitempty"`
	Transaction     *SessionArtifactGitTransaction     `json:"transaction,omitempty"`
	Locks           []SessionArtifactGitLock           `json:"locks,omitempty"`
	PartDefinitions []SessionArtifactPartDefinition    `json:"part_definitions,omitempty"`
	PartRevisions   []SessionArtifactPartRevision      `json:"part_revisions,omitempty"`
	Composition     *SessionArtifactComposition        `json:"composition,omitempty"`
}

type preparedV3ArtifactMutation struct {
	Projection         V3ArtifactProjection
	PreviousCollection *SessionArtifactCollection
	PreviousVariant    *SessionArtifactVariant
	DeletedVariants    []SessionArtifactVariant
	DeleteVariant      bool
	DeleteCollection   bool
	PreviousChain      *SessionArtifactChain
	PreviousStep       *SessionArtifactStep
	Transaction        *SessionArtifactGitTransaction
	Locks              []SessionArtifactGitLock
	PartDefinitions    []SessionArtifactPartDefinition
	PartRevisions      []SessionArtifactPartRevision
	Composition        *SessionArtifactComposition
}

func KeySessionArtifactChain(accountScopeID, userID, chainID string) string {
	return fmt.Sprintf("v3/session_artifact/chains/%s/%s/%s", keyPart(accountScopeID), keyPart(userID), keyPart(chainID))
}

func SessionArtifactChainPrefix(accountScopeID, userID string) string {
	return fmt.Sprintf("v3/session_artifact/chains/%s/%s/", keyPart(accountScopeID), keyPart(userID))
}

func KeySessionArtifactStep(accountScopeID, userID, chainID, stepID string) string {
	return fmt.Sprintf("v3/session_artifact/steps/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(userID), keyPart(chainID), keyPart(stepID))
}

func SessionArtifactStepPrefix(accountScopeID, userID, chainID string) string {
	return fmt.Sprintf("v3/session_artifact/steps/%s/%s/%s/", keyPart(accountScopeID), keyPart(userID), keyPart(chainID))
}

func KeySessionArtifactCollection(accountScopeID, sessionID, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID))
}

func SessionArtifactCollectionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactCollectionStatus(accountScopeID, sessionID, status, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections_by_status/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(status), keyPart(collectionID))
}

func SessionArtifactCollectionStatusPrefix(accountScopeID, sessionID, status string) string {
	return fmt.Sprintf("v3/session_artifact/collections_by_status/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(status))
}

func SessionArtifactCollectionStatusSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/collections_by_status/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantPrefix(accountScopeID, sessionID, collectionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(collectionID))
}

func SessionArtifactVariantSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariantStatus(accountScopeID, sessionID, status, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(status), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantStatusPrefix(accountScopeID, sessionID, status string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(status))
}

func SessionArtifactVariantStatusSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_status/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariantDigest(accountScopeID, sessionID, digest, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(digest), keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantDigestPrefix(accountScopeID, sessionID, digest string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(digest))
}

func SessionArtifactVariantDigestSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_digest/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func KeySessionArtifactVariantLineage(accountScopeID, sessionID, dimension, value, collectionID, variantID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_lineage/%s/%s/%s/%s/%s/%s", keyPart(accountScopeID), keyPart(sessionID), keyPart(dimension), value, keyPart(collectionID), keyPart(variantID))
}

func SessionArtifactVariantLineagePrefix(accountScopeID, sessionID, dimension, value string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_lineage/%s/%s/%s/%s/", keyPart(accountScopeID), keyPart(sessionID), keyPart(dimension), value)
}

func SessionArtifactVariantLineageSessionPrefix(accountScopeID, sessionID string) string {
	return fmt.Sprintf("v3/session_artifact/variants_by_lineage/%s/%s/", keyPart(accountScopeID), keyPart(sessionID))
}

func artifactLineageIndexValue(value string) string {
	return hex.EncodeToString([]byte(strings.TrimSpace(value)))
}

func artifactVariantLineageIndexKeys(variant SessionArtifactVariant) []string {
	lineage := variant.Lineage
	values := []struct{ dimension, value string }{
		{"parent_session", lineage.ParentSessionID},
		{"task_call", lineage.TaskCallID},
		{"program", lineage.ProgramID},
		{"program_job", lineage.ProgramJobID},
		{"child_session", lineage.ChildSessionID},
		{"iteration", lineage.IterationID},
	}
	keys := make([]string, 0, len(values))
	for _, item := range values {
		if strings.TrimSpace(item.value) == "" {
			continue
		}
		keys = append(keys, KeySessionArtifactVariantLineage(variant.AccountScopeID, variant.SessionID, item.dimension, artifactLineageIndexValue(item.value), variant.CollectionID, variant.ID))
	}
	return keys
}

func (s *SessionStore) GetSessionArtifactChain(accountScopeID, userID, chainID string) (SessionArtifactChain, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactChain{}, false, errors.New("session store is not configured")
	}
	accountScopeID, userID, chainID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(chainID)
	if accountScopeID == "" || userID == "" || chainID == "" {
		return SessionArtifactChain{}, false, nil
	}
	var chain SessionArtifactChain
	ok, err := s.store.GetJSON(KeySessionArtifactChain(accountScopeID, userID, chainID), &chain)
	if err != nil || !ok {
		return SessionArtifactChain{}, ok, err
	}
	if chain.AccountScopeID != accountScopeID || chain.UserID != userID || chain.ID != chainID {
		return SessionArtifactChain{}, false, errors.New("artifact chain ownership metadata is inconsistent")
	}
	return chain, true, nil
}

func (s *SessionStore) GetSessionArtifactStep(accountScopeID, userID, chainID, stepID string) (SessionArtifactStep, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactStep{}, false, errors.New("session store is not configured")
	}
	accountScopeID, userID, chainID, stepID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(chainID), strings.TrimSpace(stepID)
	if accountScopeID == "" || userID == "" || chainID == "" || stepID == "" {
		return SessionArtifactStep{}, false, nil
	}
	var step SessionArtifactStep
	ok, err := s.store.GetJSON(KeySessionArtifactStep(accountScopeID, userID, chainID, stepID), &step)
	if err != nil || !ok {
		return SessionArtifactStep{}, ok, err
	}
	if step.AccountScopeID != accountScopeID || step.UserID != userID || step.ArtifactChainID != chainID || step.ID != stepID {
		return SessionArtifactStep{}, false, errors.New("artifact step ownership metadata is inconsistent")
	}
	return step, true, nil
}

func (s *SessionStore) ListSessionArtifactSteps(accountScopeID, userID, chainID string, limit int) ([]SessionArtifactStep, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxList)
	out := make([]SessionArtifactStep, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactStepPrefix(strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), strings.TrimSpace(chainID)), limit, func(_ string, value []byte) error {
		var step SessionArtifactStep
		if err := json.Unmarshal(value, &step); err != nil {
			return err
		}
		if step.AccountScopeID != accountScopeID || step.UserID != userID || step.ArtifactChainID != chainID {
			return errors.New("artifact step ownership metadata is inconsistent")
		}
		out = append(out, step)
		return nil
	})
	return out, err
}

func (s *SessionStore) ListSessionArtifactChains(accountScopeID, userID string, limit int) ([]SessionArtifactChain, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	accountScopeID, userID = strings.TrimSpace(accountScopeID), strings.TrimSpace(userID)
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxList)
	out := make([]SessionArtifactChain, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactChainPrefix(accountScopeID, userID), limit, func(_ string, value []byte) error {
		var chain SessionArtifactChain
		if err := json.Unmarshal(value, &chain); err != nil {
			return err
		}
		if chain.AccountScopeID != accountScopeID || chain.UserID != userID || chain.ID == "" {
			return errors.New("artifact chain ownership metadata is inconsistent")
		}
		out = append(out, chain)
		return nil
	})
	return out, err
}

// RootSessionArtifactChainID returns the deterministic server-owned chain
// identity for an initial artifact revision. Callers supply only the already
// authenticated destination identity; model-authored chain IDs are never used.
func RootSessionArtifactChainID(sessionID, collectionID, variantID string) string {
	seed := strings.Join([]string{"artifact-chain-v1", strings.TrimSpace(sessionID), strings.TrimSpace(collectionID), strings.TrimSpace(variantID)}, "\x00")
	digest := sha256.Sum256([]byte(seed))
	return "artifact-chain-" + hex.EncodeToString(digest[:12])
}

func artifactChainIDForRoot(ref SessionArtifactSelectionReference) string {
	return RootSessionArtifactChainID(ref.SessionID, ref.CollectionID, ref.VariantID)
}

func artifactRevisionRoundID(variant SessionArtifactVariant) string {
	for _, value := range []string{variant.Lineage.IterationGroupID, variant.Lineage.TaskCallID, variant.CollectionID} {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return variant.ID
}

func artifactStepID(variant SessionArtifactVariant) string {
	if value := strings.TrimSpace(variant.RevisionRoundID); value != "" {
		return value
	}
	return artifactRevisionRoundID(variant)
}

func artifactSelectionForVariant(variant SessionArtifactVariant) SessionArtifactSelectionReference {
	return SessionArtifactSelectionReference{SessionID: variant.SessionID, CollectionID: variant.CollectionID, VariantID: variant.ID, EventSeq: variant.EventSeq}
}

func sameArtifactReference(left, right SessionArtifactSelectionReference) bool {
	return left.SessionID == right.SessionID && left.CollectionID == right.CollectionID && left.VariantID == right.VariantID && left.EventSeq == right.EventSeq
}

func (s *SessionStore) projectSessionArtifactVariantChain(accountScopeID, userID string, variant SessionArtifactVariant) (SessionArtifactVariant, SessionArtifactChain, error) {
	visited := map[string]struct{}{}
	path := make([]SessionArtifactVariant, 0, 8)
	current := variant
	var persisted SessionArtifactChain
	for {
		key := strings.Join([]string{current.SessionID, current.CollectionID, current.ID}, "\x00")
		if _, duplicate := visited[key]; duplicate {
			return SessionArtifactVariant{}, SessionArtifactChain{}, errors.New("artifact source lineage contains a cycle")
		}
		visited[key] = struct{}{}
		path = append(path, current)
		if current.ArtifactChainID != "" {
			if chain, ok, err := s.GetSessionArtifactChain(accountScopeID, userID, current.ArtifactChainID); err != nil {
				return SessionArtifactVariant{}, SessionArtifactChain{}, err
			} else if ok {
				persisted = chain
				break
			}
		}
		lineage := current.Lineage
		if lineage.SourceSessionID == "" || lineage.SourceCollectionID == "" || lineage.SourceVariantID == "" || lineage.SourceEventSeq == 0 {
			break
		}
		sourceSession, ok, err := s.GetSession(lineage.SourceSessionID)
		if err != nil {
			return SessionArtifactVariant{}, SessionArtifactChain{}, err
		}
		if !ok || sourceSession.AccountScopeID != accountScopeID || sourceSession.UserID != userID {
			break
		}
		source, ok, err := s.GetSessionArtifactVariant(accountScopeID, lineage.SourceSessionID, lineage.SourceCollectionID, lineage.SourceVariantID)
		if err != nil {
			return SessionArtifactVariant{}, SessionArtifactChain{}, err
		}
		if !ok || source.Status != SessionArtifactStatusReady || source.EventSeq != lineage.SourceEventSeq {
			break
		}
		current = source
	}
	rootVariant := path[len(path)-1]
	rootRef := SessionArtifactSelectionReference{SessionID: rootVariant.SessionID, CollectionID: rootVariant.CollectionID, VariantID: rootVariant.ID, EventSeq: rootVariant.EventSeq}
	chainID := artifactChainIDForRoot(rootRef)
	if persisted.ID != "" {
		chainID = persisted.ID
		rootRef = persisted.Root
		if persisted.GraphState == "" {
			return SessionArtifactVariant{}, SessionArtifactChain{}, errors.New("artifact chain is missing its Git projection state")
		}
	}
	baseRevision := 1
	if persisted.ID != "" && path[len(path)-1].RevisionNumber > 0 {
		baseRevision = path[len(path)-1].RevisionNumber
	}
	for index := len(path) - 1; index >= 0; index-- {
		path[index].ArtifactChainID = chainID
		if path[index].RevisionNumber <= 0 {
			path[index].RevisionNumber = baseRevision + len(path) - 1 - index
		}
		if path[index].RevisionRoundID == "" {
			path[index].RevisionRoundID = artifactRevisionRoundID(path[index])
		}
		if path[index].CandidateIndex <= 0 {
			path[index].CandidateIndex = path[index].Lineage.IterationIndex
			if path[index].CandidateIndex <= 0 {
				path[index].CandidateIndex = 1
			}
		}
	}
	projected := path[0]
	chain := persisted
	if chain.ID == "" || chain.GraphState != SessionArtifactGraphProjection || chain.RepositoryID == "" || chain.OfficialRef == "" {
		return SessionArtifactVariant{}, SessionArtifactChain{}, errors.New("artifact variant has no authoritative Git projection")
	}
	projected.GraphState = SessionArtifactGraphProjection
	return projected, chain, nil
}

// ProjectSessionArtifactVariantChain returns only authenticated, rebuildable Git
// projection metadata. It never fabricates graph facts from application lineage.
func (s *SessionStore) ProjectSessionArtifactVariantChain(accountScopeID, userID string, variant SessionArtifactVariant) (SessionArtifactVariant, SessionArtifactChain, error) {
	return s.projectSessionArtifactVariantChain(strings.TrimSpace(accountScopeID), strings.TrimSpace(userID), variant)
}

func (s *SessionStore) GetSessionArtifactCollection(accountScopeID, sessionID, collectionID string) (SessionArtifactCollection, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactCollection{}, false, errors.New("session store is not configured")
	}
	var collection SessionArtifactCollection
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	collectionID = strings.TrimSpace(collectionID)
	ok, err := s.store.GetJSON(KeySessionArtifactCollection(accountScopeID, sessionID, collectionID), &collection)
	if err != nil || !ok {
		return SessionArtifactCollection{}, ok, err
	}
	if collection.AccountScopeID != accountScopeID || collection.SessionID != sessionID || collection.ID != collectionID {
		return SessionArtifactCollection{}, false, errors.New("artifact collection ownership metadata is inconsistent")
	}
	if collection.Lineage.ParentSessionID != "" && collection.Lineage.ParentSessionID != sessionID {
		return SessionArtifactCollection{}, false, errors.New("artifact collection parent lineage is inconsistent")
	}
	if err := validateArtifactCollectionProgress(collection); err != nil {
		return SessionArtifactCollection{}, false, err
	}
	return collection, true, nil
}

func (s *SessionStore) GetSessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID string) (SessionArtifactVariant, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactVariant{}, false, errors.New("session store is not configured")
	}
	var variant SessionArtifactVariant
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	collectionID = strings.TrimSpace(collectionID)
	variantID = strings.TrimSpace(variantID)
	ok, err := s.store.GetJSON(KeySessionArtifactVariant(accountScopeID, sessionID, collectionID, variantID), &variant)
	if err != nil || !ok {
		return SessionArtifactVariant{}, ok, err
	}
	if variant.AccountScopeID != accountScopeID || variant.SessionID != sessionID || variant.CollectionID != collectionID || variant.ID != variantID {
		return SessionArtifactVariant{}, false, errors.New("artifact variant ownership metadata is inconsistent")
	}
	if variant.Lineage.ParentSessionID != "" && variant.Lineage.ParentSessionID != sessionID {
		return SessionArtifactVariant{}, false, errors.New("artifact variant parent lineage is inconsistent")
	}
	if variant.Lineage.ChildSessionID != "" && variant.Lineage.SourceSessionID != variant.Lineage.ChildSessionID && (variant.Lineage.SourceCollectionID == "" || variant.Lineage.SourceVariantID == "") {
		return SessionArtifactVariant{}, false, errors.New("artifact variant child lineage is inconsistent")
	}
	return variant, true, nil
}

// GetSessionArtifactVariantByID resolves an opaque session-scoped variant id
// without exposing or requiring any storage location.
func (s *SessionStore) GetSessionArtifactVariantByID(accountScopeID, sessionID, variantID string) (SessionArtifactVariant, bool, error) {
	if s == nil || s.store == nil {
		return SessionArtifactVariant{}, false, errors.New("session store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	variantID = strings.TrimSpace(variantID)
	if variantID == "" {
		return SessionArtifactVariant{}, false, nil
	}
	collections, err := s.ListAllSessionArtifactCollections(accountScopeID, sessionID, "")
	if err != nil {
		return SessionArtifactVariant{}, false, err
	}
	var found SessionArtifactVariant
	matches := 0
	for _, collection := range collections {
		variant, ok, err := s.GetSessionArtifactVariant(accountScopeID, sessionID, collection.ID, variantID)
		if err != nil {
			return SessionArtifactVariant{}, false, err
		}
		if ok {
			found, matches = variant, matches+1
		}
	}
	if matches > 1 {
		return SessionArtifactVariant{}, false, errors.New("artifact variant id is ambiguous in session")
	}
	return found, matches == 1, nil
}

// ListSessionArtifactCollections returns bounded metadata from one trusted
// account/session scope. An optional status uses the repaired status index.
// The request bound is independent from the session's lifetime collection count.
func (s *SessionStore) ListSessionArtifactCollections(accountScopeID, sessionID, status string, limit int) ([]SessionArtifactCollection, error) {
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxList)
	return s.listSessionArtifactCollections(accountScopeID, sessionID, status, limit)
}

// ListAllSessionArtifactCollections scans every collection in a session. It is
// reserved for correctness paths such as repair, catalog projection, and opaque
// variant resolution that must not silently omit older collections.
func (s *SessionStore) ListAllSessionArtifactCollections(accountScopeID, sessionID, status string) ([]SessionArtifactCollection, error) {
	const iterateAll = int(^uint(0) >> 1)
	return s.listSessionArtifactCollections(accountScopeID, sessionID, status, iterateAll)
}

func (s *SessionStore) listSessionArtifactCollections(accountScopeID, sessionID, status string, limit int) ([]SessionArtifactCollection, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	status = strings.TrimSpace(status)
	prefix := SessionArtifactCollectionPrefix(accountScopeID, sessionID)
	indexed := false
	if status != "" {
		prefix = SessionArtifactCollectionStatusPrefix(accountScopeID, sessionID, status)
		indexed = true
	}
	capacity := limit
	if capacity > SessionArtifactMaxList {
		capacity = SessionArtifactMaxList
	}
	out := make([]SessionArtifactCollection, 0, capacity)
	err := s.store.IteratePrefix(prefix, limit, func(_ string, value []byte) error {
		var collection SessionArtifactCollection
		if indexed {
			collectionID := string(value)
			stored, ok, err := s.GetSessionArtifactCollection(accountScopeID, sessionID, collectionID)
			if err != nil || !ok {
				if err == nil {
					err = errors.New("artifact collection status index is dangling")
				}
				return err
			}
			collection = stored
		} else if err := json.Unmarshal(value, &collection); err != nil {
			return err
		}
		if collection.AccountScopeID != accountScopeID || collection.SessionID != sessionID {
			return errors.New("artifact collection ownership metadata is inconsistent")
		}
		if collection.Lineage.ParentSessionID != "" && collection.Lineage.ParentSessionID != sessionID {
			return errors.New("artifact collection parent lineage is inconsistent")
		}
		if err := validateArtifactCollectionProgress(collection); err != nil {
			return err
		}
		if status != "" && collection.Status != status {
			return errors.New("artifact collection status index is inconsistent")
		}
		out = append(out, collection)
		return nil
	})
	return out, err
}

// ListSessionArtifactVariantsByLineage resolves a bounded native catalog view
// without scanning transcripts or workspace folders.
func (s *SessionStore) ListSessionArtifactVariantsByLineage(accountScopeID, sessionID, dimension, value string, limit int) ([]SessionArtifactVariant, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	accountScopeID, sessionID = strings.TrimSpace(accountScopeID), strings.TrimSpace(sessionID)
	dimension, value = strings.ToLower(strings.TrimSpace(dimension)), strings.TrimSpace(value)
	allowed := dimension == "parent_session" || dimension == "task_call" || dimension == "program" || dimension == "program_job" || dimension == "child_session" || dimension == "iteration"
	if !allowed || value == "" {
		return nil, errors.New("artifact lineage filter is invalid")
	}
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxList)
	out := make([]SessionArtifactVariant, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactVariantLineagePrefix(accountScopeID, sessionID, dimension, artifactLineageIndexValue(value)), limit, func(_ string, indexed []byte) error {
		parts := strings.SplitN(string(indexed), "\x00", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return errors.New("artifact lineage index is malformed")
		}
		variant, ok, err := s.GetSessionArtifactVariant(accountScopeID, sessionID, parts[0], parts[1])
		if err != nil || !ok {
			if err == nil {
				err = errors.New("artifact lineage index is dangling")
			}
			return err
		}
		out = append(out, variant)
		return nil
	})
	return out, err
}

// ListSessionArtifactVariants returns bounded metadata for one collection.
func (s *SessionStore) ListSessionArtifactVariants(accountScopeID, sessionID, collectionID string, limit int) ([]SessionArtifactVariant, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("session store is not configured")
	}
	limit = boundedArtifactListLimit(limit, SessionArtifactMaxVariantsPerCollection)
	accountScopeID = strings.TrimSpace(accountScopeID)
	sessionID = strings.TrimSpace(sessionID)
	collectionID = strings.TrimSpace(collectionID)
	out := make([]SessionArtifactVariant, 0, limit)
	err := s.store.IteratePrefix(SessionArtifactVariantPrefix(accountScopeID, sessionID, collectionID), limit, func(_ string, value []byte) error {
		var variant SessionArtifactVariant
		if err := json.Unmarshal(value, &variant); err != nil {
			return err
		}
		if variant.AccountScopeID != accountScopeID || variant.SessionID != sessionID || variant.CollectionID != collectionID {
			return errors.New("artifact variant ownership metadata is inconsistent")
		}
		if variant.Lineage.ParentSessionID != "" && variant.Lineage.ParentSessionID != sessionID {
			return errors.New("artifact variant parent lineage is inconsistent")
		}
		if variant.Lineage.ChildSessionID != "" && variant.Lineage.SourceSessionID != variant.Lineage.ChildSessionID && (variant.Lineage.SourceCollectionID == "" || variant.Lineage.SourceVariantID == "") {
			return errors.New("artifact variant child lineage is inconsistent")
		}
		out = append(out, variant)
		return nil
	})
	return out, err
}

func boundedArtifactListLimit(limit, maximum int) int {
	if maximum <= 0 || maximum > SessionArtifactMaxList {
		maximum = SessionArtifactMaxList
	}
	if limit <= 0 || limit > maximum {
		return maximum
	}
	return limit
}

func isV3ArtifactMutationKind(kind string) bool {
	switch kind {
	case V3SessionMutationCreateArtifact, V3SessionMutationUpdateArtifact, V3SessionMutationFinalizeArtifact, V3SessionMutationFailArtifact, V3SessionMutationUnavailableArtifact, V3SessionMutationSelectArtifact, V3SessionMutationDeleteArtifactVariant, V3SessionMutationDeleteArtifactCollection:
		return true
	default:
		return false
	}
}

func normalizeV3ArtifactMutation(input *V3SessionMutationInput) {
	if input == nil || input.Artifact == nil {
		return
	}
	input.Artifact.Collection.ID = strings.TrimSpace(input.Artifact.Collection.ID)
	input.Artifact.Collection.Name = strings.TrimSpace(input.Artifact.Collection.Name)
	input.Artifact.Collection.Description = strings.TrimSpace(input.Artifact.Collection.Description)
	input.Artifact.Collection.Status = strings.ToLower(strings.TrimSpace(input.Artifact.Collection.Status))
	normalizeArtifactLineage(&input.Artifact.Collection.Lineage)
	normalizeArtifactPresentation(&input.Artifact.Collection.Presentation)
	if input.Artifact.Variant != nil {
		variant := input.Artifact.Variant
		variant.ID = strings.TrimSpace(variant.ID)
		variant.CollectionID = strings.TrimSpace(variant.CollectionID)
		variant.Status = strings.ToLower(strings.TrimSpace(variant.Status))
		variant.Filename = strings.TrimSpace(variant.Filename)
		variant.MediaType = strings.ToLower(strings.TrimSpace(variant.MediaType))
		variant.Role = strings.ToLower(strings.TrimSpace(variant.Role))
		variant.DigestSHA256 = strings.ToLower(strings.TrimSpace(variant.DigestSHA256))
		variant.FailureCode = strings.ToLower(strings.TrimSpace(variant.FailureCode))
		variant.ArtifactChainID = strings.TrimSpace(variant.ArtifactChainID)
		variant.ArtifactStepID = strings.TrimSpace(variant.ArtifactStepID)
		variant.RepositoryID = strings.TrimSpace(variant.RepositoryID)
		variant.CommitOID = strings.ToLower(strings.TrimSpace(variant.CommitOID))
		variant.TreeOID = strings.ToLower(strings.TrimSpace(variant.TreeOID))
		variant.CandidateRef = strings.TrimSpace(variant.CandidateRef)
		for index := range variant.ParentCommitOIDs {
			variant.ParentCommitOIDs[index] = strings.ToLower(strings.TrimSpace(variant.ParentCommitOIDs[index]))
		}
		variant.GraphState = strings.ToLower(strings.TrimSpace(variant.GraphState))
		variant.RevisionRoundID = strings.TrimSpace(variant.RevisionRoundID)
		for index := range variant.Parts {
			normalizeArtifactPart(&variant.Parts[index])
		}
		for index := range variant.PartDefinitions {
			definition := &variant.PartDefinitions[index]
			definition.ID = strings.TrimSpace(definition.ID)
			definition.Label = strings.TrimSpace(definition.Label)
			definition.Description = strings.TrimSpace(definition.Description)
			definition.ArtifactChainID = strings.TrimSpace(definition.ArtifactChainID)
			definition.OwnerSessionID = strings.TrimSpace(definition.OwnerSessionID)
			normalizeArtifactPartLocator(definition.Locator)
		}
		if composition := variant.Composition; composition != nil {
			composition.ID = strings.TrimSpace(composition.ID)
			composition.ArtifactChainID = strings.TrimSpace(composition.ArtifactChainID)
			composition.OwnerSessionID = strings.TrimSpace(composition.OwnerSessionID)
			for index := range composition.Parts {
				composition.Parts[index].PartID = strings.TrimSpace(composition.Parts[index].PartID)
				composition.Parts[index].DefinitionOwnerSessionID = strings.TrimSpace(composition.Parts[index].DefinitionOwnerSessionID)
				normalizeArtifactPartRevisionReference(&composition.Parts[index].Revision)
			}
		}
		normalizeArtifactLineage(&variant.Lineage)
		normalizeArtifactPresentation(&variant.Presentation)
	}
	for index := range input.Artifact.PartDefinitions {
		definition := &input.Artifact.PartDefinitions[index]
		definition.ID = strings.TrimSpace(definition.ID)
		definition.Label = strings.TrimSpace(definition.Label)
		definition.Description = strings.TrimSpace(definition.Description)
		definition.ArtifactChainID = strings.TrimSpace(definition.ArtifactChainID)
		definition.OwnerSessionID = strings.TrimSpace(definition.OwnerSessionID)
		normalizeArtifactPartLocator(definition.Locator)
	}
	for index := range input.Artifact.PartRevisions {
		revision := &input.Artifact.PartRevisions[index]
		revision.ID = strings.TrimSpace(revision.ID)
		revision.PartID = strings.TrimSpace(revision.PartID)
		revision.ArtifactChainID = strings.TrimSpace(revision.ArtifactChainID)
		revision.OwnerSessionID = strings.TrimSpace(revision.OwnerSessionID)
		revision.RepositoryID = strings.TrimSpace(revision.RepositoryID)
		revision.CommitOID = strings.ToLower(strings.TrimSpace(revision.CommitOID))
		revision.BlobOID = strings.ToLower(strings.TrimSpace(revision.BlobOID))
		revision.DigestSHA256 = strings.ToLower(strings.TrimSpace(revision.DigestSHA256))
		revision.MediaType = strings.ToLower(strings.TrimSpace(revision.MediaType))
		for parentIndex := range revision.ParentCommitOIDs {
			revision.ParentCommitOIDs[parentIndex] = strings.ToLower(strings.TrimSpace(revision.ParentCommitOIDs[parentIndex]))
		}
	}
	if composition := input.Artifact.Composition; composition != nil {
		composition.ID = strings.TrimSpace(composition.ID)
		composition.ArtifactChainID = strings.TrimSpace(composition.ArtifactChainID)
		composition.OwnerSessionID = strings.TrimSpace(composition.OwnerSessionID)
		composition.RepositoryID = strings.TrimSpace(composition.RepositoryID)
		composition.CommitOID = strings.ToLower(strings.TrimSpace(composition.CommitOID))
		composition.TreeOID = strings.ToLower(strings.TrimSpace(composition.TreeOID))
		for parentIndex := range composition.ParentCommitOIDs {
			composition.ParentCommitOIDs[parentIndex] = strings.ToLower(strings.TrimSpace(composition.ParentCommitOIDs[parentIndex]))
		}
		for index := range composition.Parts {
			composition.Parts[index].PartID = strings.TrimSpace(composition.Parts[index].PartID)
			composition.Parts[index].DefinitionOwnerSessionID = strings.TrimSpace(composition.Parts[index].DefinitionOwnerSessionID)
			normalizeArtifactPartRevisionReference(&composition.Parts[index].Revision)
		}
	}
	normalizeArtifactGitTransaction(input.Artifact.Transaction)
	for index := range input.Artifact.Locks {
		lock := &input.Artifact.Locks[index]
		lock.PartID = strings.TrimSpace(lock.PartID)
		lock.RepositoryID = strings.TrimSpace(lock.RepositoryID)
		lock.CommitOID = strings.ToLower(strings.TrimSpace(lock.CommitOID))
		lock.BlobOID = strings.ToLower(strings.TrimSpace(lock.BlobOID))
	}
	if input.Artifact.Selection != nil {
		input.Artifact.Selection.SessionID = strings.TrimSpace(input.Artifact.Selection.SessionID)
		input.Artifact.Selection.CollectionID = strings.TrimSpace(input.Artifact.Selection.CollectionID)
		input.Artifact.Selection.VariantID = strings.TrimSpace(input.Artifact.Selection.VariantID)
		input.Artifact.Selection.Label = strings.TrimSpace(input.Artifact.Selection.Label)
		input.Artifact.Selection.Description = strings.TrimSpace(input.Artifact.Selection.Description)
		input.Artifact.Selection.PartID = strings.TrimSpace(input.Artifact.Selection.PartID)
		input.Artifact.Selection.PartLabel = strings.TrimSpace(input.Artifact.Selection.PartLabel)
		input.Artifact.Selection.PartKind = strings.ToLower(strings.TrimSpace(input.Artifact.Selection.PartKind))
		input.Artifact.Selection.Action = strings.ToLower(strings.TrimSpace(input.Artifact.Selection.Action))
	}
}

func normalizeArtifactLineage(lineage *SessionArtifactLineage) {
	lineage.ParentSessionID = strings.TrimSpace(lineage.ParentSessionID)
	lineage.SourceSessionID = strings.TrimSpace(lineage.SourceSessionID)
	lineage.SourceCollectionID = strings.TrimSpace(lineage.SourceCollectionID)
	lineage.SourceVariantID = strings.TrimSpace(lineage.SourceVariantID)
	lineage.TaskCallID = strings.TrimSpace(lineage.TaskCallID)
	lineage.ProgramID = strings.TrimSpace(lineage.ProgramID)
	lineage.ProgramJobID = strings.TrimSpace(lineage.ProgramJobID)
	lineage.ChildSessionID = strings.TrimSpace(lineage.ChildSessionID)
	lineage.IterationID = strings.TrimSpace(lineage.IterationID)
	lineage.IterationSectionID = strings.TrimSpace(lineage.IterationSectionID)
	lineage.IterationSectionLabel = strings.TrimSpace(lineage.IterationSectionLabel)
	lineage.PartID = strings.TrimSpace(lineage.PartID)
	lineage.PartLabel = strings.TrimSpace(lineage.PartLabel)
	lineage.PartKind = strings.ToLower(strings.TrimSpace(lineage.PartKind))
	lineage.SelectedReviewTargetIDs = strings.TrimSpace(lineage.SelectedReviewTargetIDs)
	lineage.RunID = strings.TrimSpace(lineage.RunID)
	lineage.PlanID = strings.TrimSpace(lineage.PlanID)
	lineage.CheckpointID = strings.TrimSpace(lineage.CheckpointID)
	lineage.AttemptID = strings.TrimSpace(lineage.AttemptID)
}

func normalizeArtifactPart(part *SessionArtifactPart) {
	if part == nil {
		return
	}
	part.ID = strings.TrimSpace(part.ID)
	part.Label = strings.TrimSpace(part.Label)
	part.Kind = strings.ToLower(strings.TrimSpace(part.Kind))
	part.Description = strings.TrimSpace(part.Description)
	part.StateID = strings.TrimSpace(part.StateID)
	part.Selector = strings.TrimSpace(part.Selector)
}

func normalizeArtifactPresentation(presentation *SessionArtifactPresentation) {
	presentation.Kind = strings.ToLower(strings.TrimSpace(presentation.Kind))
	presentation.Label = strings.TrimSpace(presentation.Label)
	presentation.Description = strings.TrimSpace(presentation.Description)
}

func validateV3ArtifactMutation(input V3SessionMutationInput) error {
	if !isV3ArtifactMutationKind(input.Kind) {
		if input.Artifact != nil {
			return errors.New("artifact payload requires an artifact mutation kind")
		}
		return nil
	}
	if input.Artifact == nil {
		return errors.New("artifact mutation payload is required")
	}
	if len(input.EventPayload) != 0 || input.EventType != "" {
		return errors.New("artifact event type and payload are derived from projected Git metadata")
	}
	collection := input.Artifact.Collection
	if err := validateArtifactID("collection", collection.ID); err != nil {
		return err
	}
	if len(collection.Name) > 256 || len(collection.Description) > 2048 {
		return errors.New("artifact collection metadata exceeds bounds")
	}
	if err := validateArtifactLineage(collection.Lineage); err != nil {
		return err
	}
	if err := validateArtifactPresentation(collection.Presentation); err != nil {
		return err
	}
	if variant := input.Artifact.Variant; variant != nil {
		if err := validateArtifactID("variant", variant.ID); err != nil {
			return err
		}
		if variant.CollectionID != "" && variant.CollectionID != collection.ID {
			return errors.New("artifact variant collection id does not match collection")
		}
		if len(variant.Filename) > 255 || len(variant.MediaType) > 255 || len(variant.Role) > 64 || len(variant.FailureCode) > 128 {
			return errors.New("artifact variant metadata exceeds bounds")
		}
		if variant.Role != "" && variant.Role != SessionArtifactRoleRenderOnly {
			return errors.New("artifact variant role is unsupported")
		}
		if variant.FailureCode != "" {
			if err := validateArtifactID("failure code", variant.FailureCode); err != nil {
				return err
			}
		}
		if variant.Filename != "" && (variant.Filename == "." || variant.Filename == ".." || strings.ContainsAny(variant.Filename, `/\\`)) {
			return errors.New("artifact filename must be a basename")
		}
		if variant.Size < 0 {
			return errors.New("artifact variant size must not be negative")
		}
		if err := validateArtifactLineage(variant.Lineage); err != nil {
			return err
		}
		if err := validateArtifactPresentation(variant.Presentation); err != nil {
			return err
		}
		if err := validateArtifactOutputRequirements(variant.OutputRequirements); err != nil {
			return err
		}
		if err := validateArtifactAnimationProfile(variant.AnimationProfile); err != nil {
			return err
		}
		if err := validateArtifactRevisionMetadata(*variant); err != nil {
			return err
		}
	}
	if input.Artifact.Transaction == nil {
		if !input.Artifact.ProjectionOnly {
			return errors.New("artifact mutation requires an authoritative Git transaction projection")
		}
		if (input.Kind != V3SessionMutationCreateArtifact && input.Kind != V3SessionMutationUpdateArtifact && input.Kind != V3SessionMutationFailArtifact && input.Kind != V3SessionMutationUnavailableArtifact) || len(input.Artifact.Locks) != 0 || len(input.Artifact.PartDefinitions) != 0 || len(input.Artifact.PartRevisions) != 0 || input.Artifact.Composition != nil || input.Artifact.Selection != nil {
			return errors.New("artifact projection-only mutation accepts only collection and non-byte variant metadata")
		}
		if variant := input.Artifact.Variant; variant != nil && (variant.RepositoryID != "" || variant.CommitOID != "" || variant.TreeOID != "" || variant.CandidateRef != "" || len(variant.ParentCommitOIDs) != 0 || variant.GraphState != "" || variant.PartGraphState != "" || variant.Composition != nil || len(variant.PartDefinitions) != 0 || variant.DigestSHA256 != "" || variant.Size != 0) {
			return errors.New("artifact projection-only reservation cannot claim Git identity or bytes")
		}
	} else {
		if input.Artifact.ProjectionOnly {
			return errors.New("artifact projection-only reservation cannot include a Git transaction")
		}
		if err := validateArtifactGitTransaction(*input.Artifact.Transaction, input); err != nil {
			return err
		}
		if err := validateArtifactGitLocks(input.Artifact.Locks, input.Artifact.Transaction.RepositoryID); err != nil {
			return err
		}
	}
	if len(input.Artifact.PartDefinitions) > SessionArtifactMaxParts || len(input.Artifact.PartRevisions) > SessionArtifactMaxParts {
		return errors.New("artifact projected part count limit exceeded")
	}
	definitionIDs := make(map[string]struct{}, len(input.Artifact.PartDefinitions))
	for _, definition := range input.Artifact.PartDefinitions {
		if err := validateArtifactID("part", definition.ID); err != nil {
			return err
		}
		if definition.Label == "" || len(definition.Label) > 256 || len(definition.Description) > 2048 || len(definition.ArtifactChainID) > 128 || len(definition.OwnerSessionID) > 128 {
			return errors.New("artifact part definition is incomplete or exceeds bounds")
		}
		if definition.ArtifactChainID == "" || definition.OwnerSessionID == "" {
			return errors.New("artifact part definition requires chain and owner session")
		}
		if _, duplicate := definitionIDs[definition.ID]; duplicate {
			return errors.New("artifact part definition ids must be unique")
		}
		definitionIDs[definition.ID] = struct{}{}
		if err := validateArtifactPartLocator(definition.Locator); err != nil {
			return err
		}
	}
	revisionIDs := make(map[string]struct{}, len(input.Artifact.PartRevisions))
	for _, revision := range input.Artifact.PartRevisions {
		if err := validateArtifactID("part revision", revision.ID); err != nil {
			return err
		}
		if err := validateArtifactID("part", revision.PartID); err != nil {
			return err
		}
		if revision.ArtifactChainID == "" || revision.OwnerSessionID == "" || len(revision.ArtifactChainID) > 128 || len(revision.OwnerSessionID) > 128 || len(revision.MediaType) > 255 || revision.Size <= 0 {
			return errors.New("artifact part revision requires chain, owner, media type, and positive size")
		}
		if err := validateArtifactRepositoryID(revision.RepositoryID); err != nil {
			return err
		}
		if !validGitOID(revision.CommitOID) || !validGitOID(revision.BlobOID) {
			return errors.New("artifact part revision requires exact Git commit and blob oids")
		}
		if err := validateCommitOIDs(revision.ParentCommitOIDs); err != nil {
			return err
		}
		if revision.DigestSHA256 != "" && !validArtifactDigest(revision.DigestSHA256) {
			return errors.New("artifact part revision digest is invalid")
		}
		key := revision.OwnerSessionID + "\x00" + revision.ArtifactChainID + "\x00" + revision.PartID + "\x00" + revision.ID
		if _, duplicate := revisionIDs[key]; duplicate {
			return errors.New("artifact part revisions must be unique")
		}
		revisionIDs[key] = struct{}{}
	}
	if composition := input.Artifact.Composition; composition != nil {
		if err := validateArtifactID("composition", composition.ID); err != nil {
			return err
		}
		if composition.ArtifactChainID == "" || composition.OwnerSessionID == "" || len(composition.Parts) == 0 || len(composition.Parts) > SessionArtifactMaxParts {
			return errors.New("artifact composition requires chain, owner, and bounded parts")
		}
		if err := validateArtifactRepositoryID(composition.RepositoryID); err != nil {
			return err
		}
		if !validGitOID(composition.CommitOID) || !validGitOID(composition.TreeOID) {
			return errors.New("artifact composition requires exact Git commit and tree oids")
		}
		if err := validateCommitOIDs(composition.ParentCommitOIDs); err != nil {
			return err
		}
		seenParts := make(map[string]struct{}, len(composition.Parts))
		for _, part := range composition.Parts {
			if err := validateArtifactID("part", part.PartID); err != nil {
				return err
			}
			if part.DefinitionOwnerSessionID == "" {
				return errors.New("artifact composition part requires definition ownership")
			}
			if _, duplicate := seenParts[part.PartID]; duplicate {
				return errors.New("artifact composition stable part ids must be unique")
			}
			seenParts[part.PartID] = struct{}{}
			if err := validateArtifactPartRevisionReference(part.Revision); err != nil {
				return err
			}
			if part.Revision.ArtifactChainID != composition.ArtifactChainID || part.Revision.PartID != part.PartID {
				return errors.New("artifact composition revision must match its chain and stable part")
			}
		}
	}
	switch input.Kind {
	case V3SessionMutationCreateArtifact:
		if collection.Name == "" && input.Artifact.Variant == nil {
			return errors.New("artifact collection name is required")
		}
	case V3SessionMutationUpdateArtifact, V3SessionMutationFinalizeArtifact, V3SessionMutationFailArtifact, V3SessionMutationUnavailableArtifact, V3SessionMutationDeleteArtifactVariant:
		if input.Artifact.Variant == nil {
			return errors.New("artifact variant is required")
		}
	case V3SessionMutationSelectArtifact:
		if input.Artifact.Selection == nil || input.Artifact.Selection.CollectionID != collection.ID || input.Artifact.Selection.VariantID == "" {
			return errors.New("artifact selection must identify the collection and variant")
		}
		if input.Artifact.Selection.SessionID != "" && input.Artifact.Selection.SessionID != input.SessionID {
			return errors.New("artifact selection session does not match mutation session")
		}
		if input.Artifact.Selection.Action != "" && input.Artifact.Selection.Action != "select" && input.Artifact.Selection.Action != "use" {
			return errors.New("artifact selection action must be select or use")
		}
		if len(input.Artifact.Selection.Label) > 256 || len(input.Artifact.Selection.Description) > 2048 || len(input.Artifact.Selection.PartID) > 128 || len(input.Artifact.Selection.PartLabel) > 256 || len(input.Artifact.Selection.PartKind) > 32 {
			return errors.New("artifact selection display metadata exceeds bounds")
		}
	case V3SessionMutationDeleteArtifactCollection:
		if input.Artifact.Variant != nil || input.Artifact.Selection != nil {
			return errors.New("artifact collection deletion accepts only a collection id")
		}
	}
	return nil
}

func validateArtifactID(label, value string) error {
	if value == "" {
		return fmt.Errorf("artifact %s id is required", label)
	}
	if len(value) > 128 {
		return fmt.Errorf("artifact %s id exceeds bounds", label)
	}
	if value == "." || value == ".." {
		return fmt.Errorf("artifact %s id contains unsupported characters", label)
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return fmt.Errorf("artifact %s id contains unsupported characters", label)
	}
	return nil
}

func validateArtifactLineage(lineage SessionArtifactLineage) error {
	for _, value := range []string{lineage.ParentSessionID, lineage.SourceSessionID, lineage.SourceCollectionID, lineage.SourceVariantID, lineage.TaskCallID, lineage.ProgramID, lineage.ProgramJobID, lineage.ChildSessionID, lineage.IterationID, lineage.IterationSectionID, lineage.IterationSectionLabel, lineage.PartID, lineage.PartLabel, lineage.PartKind, lineage.SelectedReviewTargetIDs, lineage.RunID, lineage.PlanID, lineage.CheckpointID, lineage.AttemptID} {
		if len(value) > 256 {
			return errors.New("artifact lineage metadata exceeds bounds")
		}
	}
	if lineage.IterationIndex < 0 || lineage.IterationIndex > 1_000_000 {
		return errors.New("artifact iteration index is invalid")
	}
	if lineage.ChildSessionID != "" && lineage.SourceSessionID != lineage.ChildSessionID && (lineage.SourceCollectionID == "" || lineage.SourceVariantID == "") {
		return errors.New("artifact child lineage requires the child as source session unless authenticated source artifact lineage is present")
	}
	if lineage.IterationSectionID == "" {
		if lineage.IterationSectionLabel != "" || lineage.IterationSectionStartMs != 0 || lineage.IterationSectionEndMs != 0 {
			return errors.New("artifact iteration section metadata requires a section id")
		}
	} else if lineage.IterationSectionLabel == "" || lineage.IterationSectionStartMs < 0 || lineage.IterationSectionEndMs <= lineage.IterationSectionStartMs || lineage.SourceCollectionID == "" || lineage.SourceVariantID == "" || lineage.SourceEventSeq == 0 {
		return errors.New("artifact iteration section lineage requires a label, valid range, and exact source artifact")
	}
	if lineage.PartID == "" {
		if lineage.PartLabel != "" || lineage.PartKind != "" {
			return errors.New("artifact part target metadata requires a part id")
		}
	} else if lineage.PartLabel == "" || lineage.PartKind == "" || lineage.SourceCollectionID == "" || lineage.SourceVariantID == "" || lineage.SourceEventSeq == 0 {
		return errors.New("artifact part target lineage requires a label, kind, and exact source artifact")
	}
	if lineage.SelectedReviewTargetIDs != "" {
		if lineage.PartID != "" || lineage.SourceSessionID == "" || lineage.SourceCollectionID == "" || lineage.SourceVariantID == "" || lineage.SourceEventSeq == 0 {
			return errors.New("artifact multi-target review lineage requires only a bounded id set and exact source artifact")
		}
		seen := map[string]struct{}{}
		for _, id := range strings.Split(lineage.SelectedReviewTargetIDs, ",") {
			if err := validateArtifactID("review target", id); err != nil {
				return err
			}
			if _, duplicate := seen[id]; duplicate {
				return errors.New("artifact multi-target review lineage contains duplicate ids")
			}
			seen[id] = struct{}{}
		}
		if len(seen) < 2 || len(seen) > SessionArtifactMaxParts {
			return errors.New("artifact multi-target review lineage requires a bounded multi-target id set")
		}
	}
	return nil
}

func validateArtifactRevisionMetadata(variant SessionArtifactVariant) error {
	if len(variant.ArtifactChainID) > 128 || len(variant.ArtifactStepID) > 256 || len(variant.RevisionRoundID) > 256 || variant.RevisionNumber < 0 || variant.RevisionNumber > 1_000_000 || variant.CandidateIndex < 0 || variant.CandidateIndex > 1_000_000 {
		return errors.New("artifact revision metadata is invalid")
	}
	if variant.RepositoryID != "" {
		if err := validateArtifactRepositoryID(variant.RepositoryID); err != nil {
			return err
		}
		if !validGitOID(variant.CommitOID) || (variant.TreeOID != "" && !validGitOID(variant.TreeOID)) || (variant.CandidateRef != "" && !validArtifactGitRef(variant.CandidateRef)) {
			return errors.New("artifact variant Git projection is invalid")
		}
		if err := validateCommitOIDs(variant.ParentCommitOIDs); err != nil {
			return err
		}
	}
	if (len(variant.PartDefinitions) > 0 || variant.Composition != nil) && len(variant.Parts) > 0 {
		return errors.New("locator-only parts cannot be composition authority")
	}
	if len(variant.Parts) > SessionArtifactMaxParts {
		return errors.New("artifact part count limit exceeded")
	}
	seen := make(map[string]struct{}, len(variant.Parts))
	for _, part := range variant.Parts {
		if err := validateArtifactReviewPart(part); err != nil {
			return err
		}
		if _, duplicate := seen[part.ID]; duplicate {
			return errors.New("artifact part ids must be unique")
		}
		seen[part.ID] = struct{}{}
	}
	return nil
}

func validateArtifactPresentation(presentation SessionArtifactPresentation) error {
	if len(presentation.Kind) > 64 || len(presentation.Label) > 256 || len(presentation.Description) > 2048 {
		return errors.New("artifact presentation metadata exceeds bounds")
	}
	if presentation.Width < 0 || presentation.Height < 0 || presentation.Width > 100000 || presentation.Height > 100000 {
		return errors.New("artifact presentation dimensions are invalid")
	}
	return nil
}

func validateArtifactOutputRequirements(requirements *SessionArtifactOutputRequirements) error {
	if requirements == nil {
		return nil
	}
	if requirements.Width < 1 || requirements.Height < 1 || requirements.Width > 16384 || requirements.Height > 16384 {
		return errors.New("artifact output requirement dimensions are invalid")
	}
	if len(requirements.PresetID) > 128 || len(requirements.AspectRatio) > 64 || len(requirements.Orientation) > 32 || len(requirements.ResolutionSource) > 32 || len(requirements.RegistryVersion) > 128 {
		return errors.New("artifact output requirements exceed bounds")
	}
	if requirements.AspectRatio == "" || (requirements.Orientation != "landscape" && requirements.Orientation != "portrait" && requirements.Orientation != "square") || (requirements.ResolutionSource != "preset" && requirements.ResolutionSource != "dimensions") || (requirements.ResolutionSource == "preset" && requirements.PresetID == "") || requirements.RegistryVersion == "" {
		return errors.New("artifact output requirements are incomplete")
	}
	if requirements.PresetID != "" {
		if len(requirements.PresetID) > 128 || requirements.PresetID == "." || requirements.PresetID == ".." {
			return errors.New("artifact output preset id is invalid")
		}
		for _, character := range requirements.PresetID {
			if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
				continue
			}
			return errors.New("artifact output preset id is invalid")
		}
	}
	expectedOrientation := "portrait"
	if requirements.Width == requirements.Height {
		expectedOrientation = "square"
	} else if requirements.Width > requirements.Height {
		expectedOrientation = "landscape"
	}
	if requirements.Orientation != expectedOrientation {
		return errors.New("artifact output requirement orientation conflicts with dimensions")
	}
	divisor := artifactDimensionGCD(requirements.Width, requirements.Height)
	expectedRatio := fmt.Sprintf("%d:%d", requirements.Width/divisor, requirements.Height/divisor)
	if requirements.AspectRatio != expectedRatio {
		return errors.New("artifact output requirement aspect ratio conflicts with dimensions")
	}
	return nil
}

func validateArtifactAnimationProfile(profile *SessionArtifactAnimationProfile) error {
	if profile == nil {
		return nil
	}
	if len(profile.ProfileID) > 64 || len(profile.RegistryVersion) > 128 || len(profile.RuntimeKind) > 64 || len(profile.RuntimePackage) > 128 || len(profile.RuntimeVersion) > 64 || len(profile.SecondaryRuntimePackage) > 128 || len(profile.SecondaryRuntimeVersion) > 64 {
		return errors.New("artifact animation profile metadata exceeds bounds")
	}
	if profile.ProfileID == "" || profile.RegistryVersion != "2026-08-16.v1" || profile.RuntimeKind == "" || (profile.RuntimePackage == "") != (profile.RuntimeVersion == "") || (profile.SecondaryRuntimePackage == "") != (profile.SecondaryRuntimeVersion == "") {
		return errors.New("artifact animation profile is incomplete")
	}
	var expectedBudgets SessionArtifactAnimationBudgets
	switch profile.ProfileID {
	case "motion_ui":
		expectedBudgets = canonicalArtifactAnimationBudgets(3, 0, 2, 4_194_304, 0, 400)
		if profile.RuntimeKind != "native_css_waapi_svg" || profile.RuntimePackage != "" || profile.Heavy || profile.ImportedPlaybackOnly || profile.EditableSourceRequired {
			return errors.New("artifact animation profile runtime does not match profile")
		}
	case "spatial_3d":
		expectedBudgets = canonicalArtifactAnimationBudgets(1, 1, 1.5, 2_073_600, 2_000, 200)
		if profile.RuntimeKind != "three_webgl" || profile.RuntimePackage != "three" || profile.RuntimeVersion != "0.185.1" || !profile.Heavy || profile.ImportedPlaybackOnly || profile.EditableSourceRequired {
			return errors.New("artifact animation profile runtime does not match profile")
		}
	case "vector_playback":
		expectedBudgets = canonicalArtifactAnimationBudgets(3, 0, 2, 4_194_304, 0, 300)
		if profile.RuntimeKind != "imported_vector_playback" || profile.RuntimePackage != "@lottiefiles/dotlottie-web" || profile.RuntimeVersion != "0.79.0" || profile.SecondaryRuntimePackage != "@rive-app/canvas" || profile.SecondaryRuntimeVersion != "2.39.2" || profile.Heavy || !profile.ImportedPlaybackOnly || profile.EditableSourceRequired {
			return errors.New("artifact animation profile runtime does not match profile")
		}
	case "final_render":
		expectedBudgets = canonicalArtifactAnimationBudgets(3, 0, 2, 8_294_400, 0, 0)
		if profile.RuntimeKind != "mp4_playback" || profile.RuntimePackage != "" || profile.Heavy || profile.ImportedPlaybackOnly || !profile.EditableSourceRequired {
			return errors.New("artifact animation profile runtime does not match profile")
		}
	default:
		return errors.New("artifact animation profile is unknown")
	}
	if profile.Budgets != expectedBudgets {
		return errors.New("artifact animation profile budgets do not match registry")
	}
	return nil
}

func canonicalArtifactAnimationBudgets(live, webgl int, dpr float64, pixels, particles, drawCalls int) SessionArtifactAnimationBudgets {
	return SessionArtifactAnimationBudgets{
		MaxSimultaneousLivePreviews: live, MaxWebGLContexts: webgl, MaxDevicePixelRatio: dpr,
		MaxCanvasPixels: pixels, MaxParticles: particles, MaxDrawCallsPerFrame: drawCalls,
		PauseWhenOffscreen: true, StopWhenDocumentHidden: true,
		ReducedMotionBehavior: "static_first_frame", NetworkAllowed: false,
	}
}

func artifactDimensionGCD(left, right int) int {
	for right != 0 {
		left, right = right, left%right
	}
	if left < 1 {
		return 1
	}
	return left
}

func equalArtifactOutputRequirements(left, right *SessionArtifactOutputRequirements) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneSessionArtifactOutputRequirements(input *SessionArtifactOutputRequirements) *SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func equalArtifactAnimationProfile(left, right *SessionArtifactAnimationProfile) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func cloneSessionArtifactAnimationProfile(input *SessionArtifactAnimationProfile) *SessionArtifactAnimationProfile {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func (s *SessionStore) prepareV3ArtifactMutation(input V3SessionMutationInput, seq uint64, now int64) (preparedV3ArtifactMutation, error) {
	if !isV3ArtifactMutationKind(input.Kind) {
		return preparedV3ArtifactMutation{}, nil
	}
	storedSession, ok, err := s.GetSession(input.SessionID)
	if err != nil {
		return preparedV3ArtifactMutation{}, err
	}
	if !ok || storedSession.AccountScopeID != input.AccountScopeID || storedSession.UserID != input.UserID {
		return preparedV3ArtifactMutation{}, errors.New("artifact mutation session ownership does not match")
	}
	incoming := *input.Artifact
	collection, collectionOK, err := s.GetSessionArtifactCollection(input.AccountScopeID, input.SessionID, incoming.Collection.ID)
	if err != nil {
		return preparedV3ArtifactMutation{}, err
	}
	prepared := preparedV3ArtifactMutation{}
	var transaction *SessionArtifactGitTransaction
	var definitions []SessionArtifactPartDefinition
	var revisions []SessionArtifactPartRevision
	var composition *SessionArtifactComposition
	if input.Artifact.Transaction != nil {
		copy := *input.Artifact.Transaction
		copy.Version = SessionArtifactVersion
		copy.AccountScopeID = input.AccountScopeID
		copy.UserID = input.UserID
		copy.OwnerSessionID = input.SessionID
		copy.CreatedAt = now
		copy.EventSeq = seq
		if existing, ok, transactionErr := s.GetSessionArtifactGitTransaction(input.AccountScopeID, input.UserID, input.SessionID, copy.ID); transactionErr != nil {
			return preparedV3ArtifactMutation{}, transactionErr
		} else if ok {
			// The same projection may be replayed after a crash. Event sequencing is
			// intentionally excluded from the authoritative Git identity comparison.
			existing.EventSeq, existing.CreatedAt = copy.EventSeq, copy.CreatedAt
			if !reflect.DeepEqual(existing, copy) {
				return preparedV3ArtifactMutation{}, errors.New("artifact Git transaction projection conflicts with an existing transaction")
			}
		}
		transaction = &copy
		prepared.Transaction = transaction
		prepared.Locks = append([]SessionArtifactGitLock(nil), input.Artifact.Locks...)
		var partsErr error
		definitions, revisions, composition, partsErr = s.prepareAuthoritativeArtifactParts(input, seq, now)
		if partsErr != nil {
			return preparedV3ArtifactMutation{}, partsErr
		}
		prepared.PartDefinitions, prepared.PartRevisions, prepared.Composition = definitions, revisions, composition
	}
	if composition != nil && incoming.Variant != nil {
		// The variant and projection must carry the exact normalized authoritative
		// records committed by this mutation, including graph state, event sequence,
		// ownership, and timestamps. Retaining the pre-normalized caller copies would
		// make an otherwise exact focused-part context appear stale immediately.
		incoming.Variant.Composition = composition
		incoming.Variant.PartDefinitions = append([]SessionArtifactPartDefinition(nil), definitions...)
		incoming.Variant.PartGraphState = SessionArtifactGraphAuthoritative
	}
	if collectionOK {
		copy := collection
		prepared.PreviousCollection = &copy
	}

	if input.Kind == V3SessionMutationUpdateArtifact && !collectionOK {
		return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q was not found", incoming.Collection.ID)
	}
	if input.Kind == V3SessionMutationDeleteArtifactCollection {
		if !collectionOK {
			return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q was not found", incoming.Collection.ID)
		}
		variants, err := s.ListSessionArtifactVariants(input.AccountScopeID, input.SessionID, collection.ID, SessionArtifactMaxVariantsPerCollection)
		if err != nil {
			return preparedV3ArtifactMutation{}, err
		}
		if len(variants) != collection.VariantCount {
			return preparedV3ArtifactMutation{}, errors.New("artifact collection variant count is inconsistent")
		}
		deletedCollection := collection
		deletedCollection.UpdatedAt = now
		deletedCollection.EventSeq = seq
		prepared.Projection = V3ArtifactProjection{Collection: deletedCollection, Transaction: prepared.Transaction, Locks: prepared.Locks}
		prepared.DeletedVariants = variants
		prepared.DeleteCollection = true
		return prepared, nil
	}
	if input.Kind == V3SessionMutationCreateArtifact {
		if collectionOK && incoming.Variant == nil {
			return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q already exists", incoming.Collection.ID)
		}
		if collectionOK && incoming.Collection.Name != "" && incoming.Collection.Name != collection.Name {
			return preparedV3ArtifactMutation{}, errors.New("existing artifact collection metadata cannot be replaced by variant creation")
		}
		if collectionOK && incoming.Collection.Lineage != (SessionArtifactLineage{}) && collection.Lineage != (SessionArtifactLineage{}) && !artifactCollectionLineageCompatible(collection.Lineage, incoming.Collection.Lineage) {
			return preparedV3ArtifactMutation{}, errors.New("existing artifact collection lineage cannot be replaced by variant creation")
		}
		if collectionOK && collection.Lineage == (SessionArtifactLineage{}) && incoming.Collection.Lineage != (SessionArtifactLineage{}) {
			collection.Lineage = incoming.Collection.Lineage
		}
		if collectionOK && incoming.Variant != nil {
			if existing, duplicate, err := s.GetSessionArtifactVariantByID(input.AccountScopeID, input.SessionID, incoming.Variant.ID); err != nil {
				return preparedV3ArtifactMutation{}, err
			} else if duplicate && existing.CollectionID != collection.ID {
				return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists in session", incoming.Variant.ID)
			}
		}
		if !collectionOK {
			if incoming.Collection.Name == "" {
				return preparedV3ArtifactMutation{}, errors.New("artifact collection name is required")
			}
			if incoming.Variant != nil {
				if _, duplicate, err := s.GetSessionArtifactVariantByID(input.AccountScopeID, input.SessionID, incoming.Variant.ID); err != nil {
					return preparedV3ArtifactMutation{}, err
				} else if duplicate {
					return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists in session", incoming.Variant.ID)
				}
			}
			collection = incoming.Collection
			collection.Version = SessionArtifactVersion
			collection.AccountScopeID = input.AccountScopeID
			collection.SessionID = input.SessionID
			collection.Status = SessionArtifactStatusStaging
			collection.VariantCount = 0
			collection.StagingCount = 0
			collection.ReadyCount = 0
			collection.FailedCount = 0
			collection.UnavailableCount = 0
			collection.SelectedVariantID = ""
			collection.CreatedAt = now
		}
	} else if !collectionOK {
		return preparedV3ArtifactMutation{}, fmt.Errorf("artifact collection %q was not found", incoming.Collection.ID)
	}
	collection.UpdatedAt = now
	collection.EventSeq = seq

	var variant *SessionArtifactVariant
	if incoming.Variant != nil {
		current, variantOK, err := s.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, collection.ID, incoming.Variant.ID)
		if err != nil {
			return preparedV3ArtifactMutation{}, err
		}
		if variantOK {
			copy := current
			prepared.PreviousVariant = &copy
		}
		if variantOK && incoming.Variant.Lineage != (SessionArtifactLineage{}) && current.Lineage != (SessionArtifactLineage{}) && incoming.Variant.Lineage != current.Lineage {
			return preparedV3ArtifactMutation{}, errors.New("artifact variant lineage is immutable")
		}
		if variantOK && !equalArtifactOutputRequirements(current.OutputRequirements, incoming.Variant.OutputRequirements) {
			if current.OutputRequirements == nil || incoming.Variant.OutputRequirements != nil {
				return preparedV3ArtifactMutation{}, errors.New("artifact output requirements are immutable")
			}
		}
		if variantOK && !equalArtifactAnimationProfile(current.AnimationProfile, incoming.Variant.AnimationProfile) {
			if current.AnimationProfile == nil || incoming.Variant.AnimationProfile != nil {
				return preparedV3ArtifactMutation{}, errors.New("artifact animation profile is immutable")
			}
		}
		if variantOK {
			incoming.Variant.OutputRequirements = cloneSessionArtifactOutputRequirements(current.OutputRequirements)
			incoming.Variant.AnimationProfile = cloneSessionArtifactAnimationProfile(current.AnimationProfile)
			incoming.Variant.AutoAccept = current.AutoAccept
			if current.OutputRequirements != nil {
				if (incoming.Variant.Presentation.Width != 0 && incoming.Variant.Presentation.Width != current.OutputRequirements.Width) || (incoming.Variant.Presentation.Height != 0 && incoming.Variant.Presentation.Height != current.OutputRequirements.Height) {
					return preparedV3ArtifactMutation{}, errors.New("artifact presentation dimensions conflict with immutable output requirements")
				}
				incoming.Variant.Presentation.Width = current.OutputRequirements.Width
				incoming.Variant.Presentation.Height = current.OutputRequirements.Height
			}
		}
		if variantOK && current.Lineage == (SessionArtifactLineage{}) && incoming.Variant.Lineage != (SessionArtifactLineage{}) {
			current.Lineage = incoming.Variant.Lineage
		}
		if input.Kind == V3SessionMutationDeleteArtifactVariant {
			if !variantOK {
				return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q was not found", incoming.Variant.ID)
			}
			if collection.VariantCount <= 0 {
				return preparedV3ArtifactMutation{}, errors.New("artifact collection variant count is inconsistent")
			}
			if collection.SelectedVariantID != "" && collection.SelectedVariantID != current.ID {
				selected, ok, err := s.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, collection.ID, collection.SelectedVariantID)
				if err != nil {
					return preparedV3ArtifactMutation{}, err
				}
				if !ok || selected.Status != SessionArtifactStatusReady {
					return preparedV3ArtifactMutation{}, errors.New("artifact collection selection metadata is inconsistent")
				}
			}
			collection.VariantCount--
			if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil {
				return preparedV3ArtifactMutation{}, err
			}
			if collection.SelectedVariantID == current.ID {
				collection.SelectedVariantID = ""
			}
			collection.Status = artifactCollectionStatusFromCounts(collection)
			collection.UpdatedAt = now
			collection.EventSeq = seq
			deleted := current
			if err := validateArtifactCollectionProgress(collection); err != nil {
				return preparedV3ArtifactMutation{}, err
			}
			prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: &deleted, Transaction: prepared.Transaction, Locks: prepared.Locks}
			prepared.DeletedVariants = []SessionArtifactVariant{current}
			prepared.DeleteVariant = true
			return prepared, nil
		}
		if variantOK && input.Kind == V3SessionMutationCreateArtifact && !(current.GraphState == "" && transaction != nil) {
			return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q already exists", incoming.Variant.ID)
		}
		if !variantOK && input.Kind != V3SessionMutationCreateArtifact {
			return preparedV3ArtifactMutation{}, fmt.Errorf("artifact variant %q was not found", incoming.Variant.ID)
		}
		next := *incoming.Variant
		if variantOK && (input.Kind == V3SessionMutationFinalizeArtifact || input.Kind == V3SessionMutationFailArtifact || input.Kind == V3SessionMutationUnavailableArtifact) {
			next = mergeTerminalArtifactVariant(current, next)
			if input.Kind == V3SessionMutationFinalizeArtifact && transaction != nil && current.GraphState == "" {
				// A projection-only managed reservation contributes identity and trusted
				// lineage, never graph state. The produced Git commit is the first and
				// only authority for its repository/commit/part projection.
				next.RepositoryID, next.CommitOID, next.TreeOID, next.CandidateRef = "", "", "", ""
				next.ParentCommitOIDs, next.Composition, next.PartDefinitions = nil, nil, nil
				next.GraphState, next.PartGraphState = "", ""
			}
			if input.Kind == V3SessionMutationFinalizeArtifact && incoming.Composition != nil && current.GraphState != "" && current.Composition != nil {
				if !reflect.DeepEqual(current.Composition, incoming.Composition) || !reflect.DeepEqual(current.PartDefinitions, incoming.PartDefinitions) {
					return preparedV3ArtifactMutation{}, errors.New("finalized artifact composition must match its immutable staged composition")
				}
			}
		}
		next.Version = SessionArtifactVersion
		next.ProjectionReservation = input.Artifact.ProjectionOnly && transaction == nil
		next.AccountScopeID = input.AccountScopeID
		next.SessionID = input.SessionID
		next.CollectionID = collection.ID
		if transaction != nil {
			next.RepositoryID = transaction.RepositoryID
			next.CommitOID = transaction.CommitOID
			next.ParentCommitOIDs = append([]string(nil), transaction.ParentCommitOIDs...)
			next.CandidateRef = transaction.CandidateRef
		}
		if composition != nil {
			next.TreeOID = composition.TreeOID
		}
		if variantOK {
			next.CreatedAt = current.CreatedAt
		} else {
			next.CreatedAt = now
			collection.VariantCount++
			collection.StagingCount++
		}
		if !variantOK && collection.VariantCount > SessionArtifactMaxVariantsPerCollection {
			return preparedV3ArtifactMutation{}, errors.New("artifact collection variant limit exceeded")
		}
		if !variantOK || current.GraphState == "" {
			if next.Composition == nil {
				next.PartGraphState = ""
			} else {
				next.PartGraphState = SessionArtifactGraphProjection
			}
			if next.ArtifactStepID == "" {
				next.ArtifactStepID = artifactStepID(next)
			}
			if next.RevisionRoundID == "" {
				next.RevisionRoundID = next.ArtifactStepID
			}
			if next.RevisionRoundID != next.ArtifactStepID {
				return preparedV3ArtifactMutation{}, errors.New("artifact candidate step identity conflicts with revision round")
			}
			if transaction != nil {
				next.GraphState = SessionArtifactGraphAuthoritative
			}
			if next.CandidateIndex <= 0 {
				next.CandidateIndex = next.Lineage.IterationIndex
				if next.CandidateIndex <= 0 {
					next.CandidateIndex = collection.VariantCount
				}
			}
			if transaction != nil {
				var parent SessionArtifactSelectionReference
				if lineage := next.Lineage; lineage.SourceSessionID != "" && lineage.SourceCollectionID != "" && lineage.SourceVariantID != "" && lineage.SourceEventSeq > 0 {
					sourceSession, ok, sourceErr := s.GetSession(lineage.SourceSessionID)
					if sourceErr != nil {
						return preparedV3ArtifactMutation{}, sourceErr
					}
					if !ok || sourceSession.AccountScopeID != input.AccountScopeID || sourceSession.UserID != input.UserID {
						return preparedV3ArtifactMutation{}, errors.New("artifact source chain ownership is inconsistent")
					}
					source, ok, sourceErr := s.GetSessionArtifactVariant(input.AccountScopeID, lineage.SourceSessionID, lineage.SourceCollectionID, lineage.SourceVariantID)
					if sourceErr != nil {
						return preparedV3ArtifactMutation{}, sourceErr
					}
					if !ok || source.Status != SessionArtifactStatusReady || source.EventSeq != lineage.SourceEventSeq {
						return preparedV3ArtifactMutation{}, errors.New("artifact source chain requires an exact ready source")
					}
					projectedSource, chain, sourceErr := s.projectSessionArtifactVariantChain(input.AccountScopeID, input.UserID, source)
					if sourceErr != nil {
						return preparedV3ArtifactMutation{}, sourceErr
					}
					if chain.GraphState != SessionArtifactGraphProjection || source.RepositoryID != incoming.Transaction.RepositoryID || source.CommitOID == "" {
						return preparedV3ArtifactMutation{}, errors.New("artifact source is not an exact Git projection in the transaction repository")
					}
					next.ArtifactChainID, next.RevisionNumber = chain.ID, projectedSource.RevisionNumber+1
					parent = artifactSelectionForVariant(source)
				} else {
					// Root chain identity is derived from the immutable destination, not a
					// caller- or round-authored step label. Initial byte-bearing parts can
					// therefore be staged against the exact server-owned chain before the
					// canonical artifact mutation is committed.
					identity := SessionArtifactSelectionReference{SessionID: input.SessionID, CollectionID: next.CollectionID, VariantID: next.ID}
					next.ArtifactChainID, next.RevisionNumber = artifactChainIDForRoot(identity), 1
				}
				if parent.VariantID != "" {
					parentCopy := parent
					next.ParentArtifact = &parentCopy
				}
				chain, chainOK, err := s.GetSessionArtifactChain(input.AccountScopeID, input.UserID, next.ArtifactChainID)
				if err != nil {
					return preparedV3ArtifactMutation{}, err
				}
				if !chainOK {
					initialOfficial := incoming.Transaction.ResultingOfficial
					if next.AutoAccept && incoming.Transaction.State == "committed" {
						// A projection-only managed reservation has no chain row until its
						// terminal byte publication. Keep the new projection at the CAS base
						// until the sole-candidate auto-accept block below applies the already
						// completed transaction; initializing it at the result would make that
						// same valid CAS look stale.
						initialOfficial = incoming.Transaction.ExpectedOldOID
					}
					chain = SessionArtifactChain{Version: SessionArtifactVersion, GraphState: SessionArtifactGraphProjection, ID: next.ArtifactChainID, AccountScopeID: input.AccountScopeID, UserID: input.UserID, Name: firstNonEmptyArtifactString(next.Presentation.Label, collection.Name, next.Filename), RepositoryID: incoming.Transaction.RepositoryID, OfficialRef: incoming.Transaction.OfficialRef, OfficialCommitOID: initialOfficial, RevisionCount: next.RevisionNumber, LastRoundID: next.ArtifactStepID, CreatedAt: now, UpdatedAt: now, EventSeq: seq}
				} else if chain.GraphState != SessionArtifactGraphProjection || chain.RepositoryID != incoming.Transaction.RepositoryID || chain.OfficialRef != incoming.Transaction.OfficialRef {
					return preparedV3ArtifactMutation{}, errors.New("artifact Git chain projection conflicts with transaction identity")
				}
				step, stepOK, err := s.GetSessionArtifactStep(input.AccountScopeID, input.UserID, chain.ID, next.ArtifactStepID)
				if err != nil {
					return preparedV3ArtifactMutation{}, err
				}
				if stepOK {
					copy := step
					prepared.PreviousStep = &copy
					if step.RepositoryID != incoming.Transaction.RepositoryID {
						return preparedV3ArtifactMutation{}, errors.New("artifact step conflicts with the authoritative Git repository")
					}
					// One iteration step owns sibling candidate transactions. The step-level
					// transaction fields remain the first candidate's representative identity;
					// every candidate's exact transaction stays authoritative on its variant.
				} else {
					step = SessionArtifactStep{Version: SessionArtifactVersion, GraphState: SessionArtifactGraphProjection, ID: next.ArtifactStepID, ArtifactChainID: chain.ID, AccountScopeID: input.AccountScopeID, UserID: input.UserID, RepositoryID: incoming.Transaction.RepositoryID, TransactionRef: incoming.Transaction.TransactionRef, CandidateRef: incoming.Transaction.CandidateRef, CommitOID: incoming.Transaction.CommitOID, ParentCommitOIDs: append([]string(nil), incoming.Transaction.ParentCommitOIDs...), ExpectedOldOID: incoming.Transaction.ExpectedOldOID, ResultingOID: incoming.Transaction.ResultingOfficial, Parent: parent, RevisionNumber: next.RevisionNumber, CreatedAt: now}
				}
				if len(step.Candidates) >= SessionArtifactMaxStepCandidates {
					return preparedV3ArtifactMutation{}, errors.New("artifact step candidate limit exceeded")
				}
				for _, candidate := range step.Candidates {
					if candidate.SessionID == next.SessionID && candidate.CollectionID == next.CollectionID && candidate.VariantID == next.ID {
						return preparedV3ArtifactMutation{}, errors.New("artifact candidate is duplicated in its step")
					}
				}
				candidate := artifactSelectionForVariant(next)
				candidate.EventSeq = seq
				step.Candidates = append(step.Candidates, candidate)
				step.UpdatedAt, step.EventSeq = now, seq
				chain.RevisionCount, chain.LastRoundID, chain.UpdatedAt, chain.EventSeq = max(chain.RevisionCount, next.RevisionNumber), next.ArtifactStepID, now, seq
				// Persist the chain identity and step atomically at turn start, but do not
				// expose a head-moving chain projection until explicit acceptance.
				prepared.Projection.Chain, prepared.Projection.Step = &chain, &step
			}
		}
		switch input.Kind {
		case V3SessionMutationCreateArtifact, V3SessionMutationUpdateArtifact:
			if variantOK && current.Status == SessionArtifactStatusReady {
				return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable")
			}
			next.Status = SessionArtifactStatusStaging
			if variantOK && current.Status != next.Status {
				if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil {
					return preparedV3ArtifactMutation{}, err
				}
				if err := adjustArtifactCollectionStatusCount(&collection, next.Status, 1); err != nil {
					return preparedV3ArtifactMutation{}, err
				}
				collection.Status = artifactCollectionStatusFromCounts(collection)
			}
			next.DigestSHA256 = ""
			next.Size = 0
			next.FailureCode = ""
			if next.Progress != nil {
				progress := *next.Progress
				progress.Stage = strings.TrimSpace(progress.Stage)
				if progress.Stage == "" || len(progress.Stage) > 64 || progress.Completed < 0 || progress.Total < 0 || progress.Completed > progress.Total || progress.Percent < 0 || progress.Percent > 100 || progress.ElapsedMS < 0 || progress.EstimatedRemainingMS < 0 || progress.HeartbeatAt <= 0 {
					return preparedV3ArtifactMutation{}, errors.New("artifact staging progress is invalid")
				}
				next.Progress = &progress
			}
		case V3SessionMutationFinalizeArtifact:
			if current.Status == SessionArtifactStatusReady {
				return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable")
			}
			if !validArtifactDigest(next.DigestSHA256) || next.Size <= 0 || next.MediaType == "" || next.Filename == "" {
				return preparedV3ArtifactMutation{}, errors.New("finalized artifact requires filename, media type, digest, and positive size")
			}
			next.Status = SessionArtifactStatusReady
			next.FailureCode = ""
			next.Progress = nil
			if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil {
				return preparedV3ArtifactMutation{}, err
			}
			if err := adjustArtifactCollectionStatusCount(&collection, next.Status, 1); err != nil {
				return preparedV3ArtifactMutation{}, err
			}
			collection.Status = artifactCollectionStatusFromCounts(collection)
		case V3SessionMutationFailArtifact, V3SessionMutationUnavailableArtifact:
			if current.Status == SessionArtifactStatusReady {
				return preparedV3ArtifactMutation{}, errors.New("finalized artifact variant is immutable")
			}
			if next.FailureCode == "" {
				return preparedV3ArtifactMutation{}, errors.New("failed artifact variant requires a failure code")
			}
			if input.Kind == V3SessionMutationUnavailableArtifact {
				next.Status = SessionArtifactStatusUnavailable
			} else {
				next.Status = SessionArtifactStatusFailed
			}
			if err := adjustArtifactCollectionStatusCount(&collection, current.Status, -1); err != nil {
				return preparedV3ArtifactMutation{}, err
			}
			if err := adjustArtifactCollectionStatusCount(&collection, next.Status, 1); err != nil {
				return preparedV3ArtifactMutation{}, err
			}
			collection.Status = artifactCollectionStatusFromCounts(collection)
			next.DigestSHA256 = ""
			next.Size = 0
			next.Progress = nil
		}
		next.UpdatedAt = now
		next.EventSeq = seq
		variant = &next
		if input.Kind == V3SessionMutationFinalizeArtifact && next.GraphState == SessionArtifactGraphAuthoritative {
			var step SessionArtifactStep
			if prepared.Projection.Step != nil && prepared.Projection.Step.ID == next.ArtifactStepID {
				step = *prepared.Projection.Step
			} else {
				storedStep, ok, err := s.GetSessionArtifactStep(input.AccountScopeID, input.UserID, next.ArtifactChainID, next.ArtifactStepID)
				if err != nil {
					return preparedV3ArtifactMutation{}, err
				}
				if !ok {
					return preparedV3ArtifactMutation{}, errors.New("artifact candidate step is missing")
				}
				step = storedStep
				copy := step
				prepared.PreviousStep = &copy
			}
			found := false
			for index := range step.Candidates {
				if step.Candidates[index].SessionID == next.SessionID && step.Candidates[index].CollectionID == next.CollectionID && step.Candidates[index].VariantID == next.ID {
					step.Candidates[index].EventSeq, found = seq, true
					break
				}
			}
			if !found {
				return preparedV3ArtifactMutation{}, errors.New("artifact candidate is not registered in its step")
			}
			step.UpdatedAt, step.EventSeq = now, seq
			prepared.Projection.Step = &step
			// A one-candidate publication has no unresolved product choice. Accept it
			// atomically with readiness so the exact returned reference is immediately
			// reusable as the canonical continuation head. Multi-candidate steps remain
			// pending for explicit review even when their candidates finalize at
			// different times.
			if next.AutoAccept && len(step.Candidates) == 1 && step.Accepted == nil && incoming.Transaction.State == "committed" {
				var chain SessionArtifactChain
				if prepared.Projection.Chain != nil && prepared.Projection.Chain.ID == next.ArtifactChainID {
					chain = *prepared.Projection.Chain
				} else {
					storedChain, chainOK, chainErr := s.GetSessionArtifactChain(input.AccountScopeID, input.UserID, next.ArtifactChainID)
					if chainErr != nil {
						return preparedV3ArtifactMutation{}, chainErr
					}
					if !chainOK || storedChain.GraphState != SessionArtifactGraphAuthoritative {
						return preparedV3ArtifactMutation{}, errors.New("authoritative artifact chain was not found")
					}
					chain = storedChain
				}
				if chain.OfficialCommitOID != incoming.Transaction.ExpectedOldOID {
					return preparedV3ArtifactMutation{}, errors.New("artifact Git official ref projection does not match the completed CAS transaction")
				}
				candidate := artifactSelectionForVariant(next)
				step.Accepted = &candidate
				chain.Head = candidate
				chain.OfficialCommitOID = incoming.Transaction.ResultingOfficial
				collection.SelectedVariantID = next.ID
				if step.RevisionNumber == 1 && chain.Root.EventSeq == 0 {
					chain.Root = candidate
				}
				chain.RevisionCount, chain.LastRoundID, chain.UpdatedAt, chain.EventSeq = max(chain.RevisionCount, step.RevisionNumber), step.ID, now, seq
				prepared.Projection.Chain, prepared.Projection.Step = &chain, &step
			}
		}
	}

	var selection *SessionArtifactSelectionReference
	if input.Kind == V3SessionMutationSelectArtifact {
		selected, ok, err := s.GetSessionArtifactVariant(input.AccountScopeID, input.SessionID, collection.ID, incoming.Selection.VariantID)
		if err != nil {
			return preparedV3ArtifactMutation{}, err
		}
		if !ok || selected.Status != SessionArtifactStatusReady {
			return preparedV3ArtifactMutation{}, errors.New("only a ready artifact variant can be selected")
		}
		if incoming.Selection.EventSeq != 0 && incoming.Selection.EventSeq != selected.EventSeq {
			return preparedV3ArtifactMutation{}, errors.New("artifact selection event sequence is stale")
		}
		action := incoming.Selection.Action
		if action == "" {
			action = "select"
		}
		ref := SessionArtifactSelectionReference{SessionID: input.SessionID, CollectionID: collection.ID, VariantID: selected.ID, EventSeq: selected.EventSeq, Label: incoming.Selection.Label, Description: incoming.Selection.Description, Action: action, PartID: incoming.Selection.PartID}
		if ref.PartID != "" {
			for _, part := range selected.Parts {
				if part.ID == ref.PartID {
					partCopy := part
					ref.PartLabel, ref.PartKind, ref.Part = part.Label, part.Kind, &partCopy
					break
				}
			}
			if ref.PartLabel == "" {
				return preparedV3ArtifactMutation{}, errors.New("artifact selection part was not found on the exact revision")
			}
		}
		selection = &ref
		if selected.GraphState != SessionArtifactGraphProjection || selected.ArtifactChainID == "" || selected.ArtifactStepID == "" || !validGitOID(selected.CommitOID) {
			return preparedV3ArtifactMutation{}, errors.New("artifact selection is missing an exact Git projection")
		}
		chain, ok, err := s.GetSessionArtifactChain(input.AccountScopeID, input.UserID, selected.ArtifactChainID)
		if err != nil {
			return preparedV3ArtifactMutation{}, err
		}
		if !ok || chain.GraphState != SessionArtifactGraphAuthoritative {
			return preparedV3ArtifactMutation{}, errors.New("authoritative artifact chain was not found")
		}
		copyChain := chain
		prepared.PreviousChain = &copyChain
		step, ok, err := s.GetSessionArtifactStep(input.AccountScopeID, input.UserID, chain.ID, selected.ArtifactStepID)
		if err != nil {
			return preparedV3ArtifactMutation{}, err
		}
		if !ok || step.GraphState != SessionArtifactGraphAuthoritative {
			return preparedV3ArtifactMutation{}, errors.New("authoritative artifact step was not found")
		}
		if action == "use" {
			if !validGitOID(selected.CommitOID) || selected.RepositoryID != incoming.Transaction.RepositoryID {
				return preparedV3ArtifactMutation{}, errors.New("artifact continuation requires an exact Git commit in the transaction repository")
			}
			prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: variant, Selection: selection, Chain: &chain, Step: &step, Transaction: prepared.Transaction, Locks: prepared.Locks}
			return prepared, nil
		}
		collection.Status = artifactCollectionStatusFromCounts(collection)
		collection.UpdatedAt = now
		collection.EventSeq = seq
		copyStep := step
		prepared.PreviousStep = &copyStep
		if step.Accepted != nil {
			if step.Accepted.VariantID == selected.ID && step.Accepted.EventSeq == selected.EventSeq {
				// Re-selecting an automatically accepted sole candidate is an idempotent
				// explicit confirmation. Preserve the canonical head and still project the
				// user's selection action for normal API/UI behavior.
				collection.SelectedVariantID = selected.ID
				collection.Status = artifactCollectionStatusFromCounts(collection)
				collection.UpdatedAt = now
				collection.EventSeq = seq
				prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: variant, Selection: selection, Chain: &chain, Step: &step, Transaction: prepared.Transaction, Locks: prepared.Locks}
				return prepared, nil
			}
			return preparedV3ArtifactMutation{}, errors.New("artifact step already accepted a different candidate")
		}
		if incoming.Transaction.State != "committed" || chain.OfficialCommitOID != incoming.Transaction.ExpectedOldOID {
			return preparedV3ArtifactMutation{}, errors.New("artifact selection requires a completed Git CAS transaction against the projected official ref")
		}
		candidate := artifactSelectionForVariant(selected)
		member := false
		for _, item := range step.Candidates {
			if sameArtifactReference(item, candidate) {
				member = true
				break
			}
		}
		if !member {
			return preparedV3ArtifactMutation{}, errors.New("selected artifact is not a candidate of its step")
		}
		step.Accepted, step.UpdatedAt, step.EventSeq = &candidate, now, seq
		chain.Head = candidate
		chain.OfficialCommitOID = incoming.Transaction.ResultingOfficial
		collection.SelectedVariantID = selected.ID
		if step.RevisionNumber == 1 && chain.Root.EventSeq == 0 {
			chain.Root = candidate
		}
		chain.RevisionCount, chain.LastRoundID, chain.UpdatedAt, chain.EventSeq = max(chain.RevisionCount, step.RevisionNumber), step.ID, now, seq
		prepared.Projection.Chain, prepared.Projection.Step = &chain, &step
	}
	if err := validateArtifactCollectionProgress(collection); err != nil {
		return preparedV3ArtifactMutation{}, err
	}
	chain, step := prepared.Projection.Chain, prepared.Projection.Step
	prepared.Projection = V3ArtifactProjection{Collection: collection, Variant: variant, Selection: selection, Chain: chain, Step: step, Transaction: prepared.Transaction, Locks: prepared.Locks, PartDefinitions: prepared.PartDefinitions, PartRevisions: prepared.PartRevisions, Composition: prepared.Composition}
	return prepared, nil
}

func artifactCollectionLineageCompatible(existing, incoming SessionArtifactLineage) bool {
	return existing.ParentSessionID == incoming.ParentSessionID && existing.TaskCallID == incoming.TaskCallID && existing.ProgramID == incoming.ProgramID
}

func artifactCollectionProgressTotal(collection SessionArtifactCollection) int {
	return collection.StagingCount + collection.ReadyCount + collection.FailedCount + collection.UnavailableCount
}

func validateArtifactCollectionProgress(collection SessionArtifactCollection) error {
	if collection.VariantCount < 0 || collection.StagingCount < 0 || collection.ReadyCount < 0 || collection.FailedCount < 0 || collection.UnavailableCount < 0 || artifactCollectionProgressTotal(collection) != collection.VariantCount {
		return errors.New("artifact collection progress is inconsistent")
	}
	return nil
}

func adjustArtifactCollectionStatusCount(collection *SessionArtifactCollection, status string, delta int) error {
	if collection == nil {
		return errors.New("artifact collection is required")
	}
	if delta == 0 {
		return nil
	}
	counter := (*int)(nil)
	switch status {
	case SessionArtifactStatusStaging:
		counter = &collection.StagingCount
	case SessionArtifactStatusReady:
		counter = &collection.ReadyCount
	case SessionArtifactStatusFailed:
		counter = &collection.FailedCount
	case SessionArtifactStatusUnavailable:
		counter = &collection.UnavailableCount
	}
	if counter == nil {
		return fmt.Errorf("artifact variant status %q is invalid", status)
	}
	if delta < 0 && *counter < -delta {
		return errors.New("artifact collection status count would underflow")
	}
	*counter += delta
	return nil
}

func artifactCollectionStatusFromCounts(collection SessionArtifactCollection) string {
	switch {
	case collection.ReadyCount > 0:
		return SessionArtifactStatusReady
	case collection.StagingCount > 0:
		return SessionArtifactStatusStaging
	case collection.UnavailableCount > 0:
		return SessionArtifactStatusUnavailable
	case collection.FailedCount > 0:
		return SessionArtifactStatusFailed
	default:
		return SessionArtifactStatusStaging
	}
}

func mergeTerminalArtifactVariant(current, incoming SessionArtifactVariant) SessionArtifactVariant {
	next := current
	if incoming.Filename != "" {
		next.Filename = incoming.Filename
	}
	if incoming.MediaType != "" {
		next.MediaType = incoming.MediaType
	}
	if incoming.Role != "" {
		next.Role = incoming.Role
	}
	if incoming.DigestSHA256 != "" {
		next.DigestSHA256 = incoming.DigestSHA256
	}
	if incoming.Size != 0 {
		next.Size = incoming.Size
	}
	if incoming.FailureCode != "" {
		next.FailureCode = incoming.FailureCode
	}
	if incoming.Progress != nil {
		progress := *incoming.Progress
		next.Progress = &progress
	}
	if incoming.Lineage != (SessionArtifactLineage{}) {
		next.Lineage = incoming.Lineage
	}
	if incoming.Presentation != (SessionArtifactPresentation{}) {
		next.Presentation = incoming.Presentation
	}
	if incoming.Parts != nil {
		next.Parts = append([]SessionArtifactPart(nil), incoming.Parts...)
	}
	if incoming.Composition != nil {
		composition := *incoming.Composition
		composition.Parts = append([]SessionArtifactCompositionPart(nil), incoming.Composition.Parts...)
		next.Composition = &composition
		next.PartDefinitions = append([]SessionArtifactPartDefinition(nil), incoming.PartDefinitions...)
		next.PartGraphState = incoming.PartGraphState
	}
	return next
}

func validArtifactDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func setV3ArtifactMutationInBatch(batch *pebble.Batch, prepared preparedV3ArtifactMutation) error {
	collection := prepared.Projection.Collection
	if collection.ID == "" {
		return nil
	}
	if prepared.DeleteCollection || len(prepared.DeletedVariants) != 0 {
		for _, variant := range prepared.DeletedVariants {
			keys := []string{
				KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID),
				KeySessionArtifactVariantStatus(variant.AccountScopeID, variant.SessionID, variant.Status, variant.CollectionID, variant.ID),
			}
			keys = append(keys, artifactVariantLineageIndexKeys(variant)...)
			for _, key := range keys {
				if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
			if variant.DigestSHA256 != "" {
				if err := batch.Delete([]byte(KeySessionArtifactVariantDigest(variant.AccountScopeID, variant.SessionID, variant.DigestSHA256, variant.CollectionID, variant.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
		}
		if prepared.DeleteCollection {
			if prepared.PreviousCollection == nil {
				return errors.New("artifact collection deletion is missing previous metadata")
			}
			if prepared.PreviousCollection.VariantCount != len(prepared.DeletedVariants) {
				return errors.New("artifact collection variant count is inconsistent")
			}
			for _, key := range []string{
				KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID),
				KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, prepared.PreviousCollection.Status, collection.ID),
			} {
				if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
			return nil
		}
	}
	if transaction := prepared.Transaction; transaction != nil {
		payload, err := json.Marshal(transaction)
		if err != nil {
			return fmt.Errorf("marshal artifact Git transaction: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactGitTransaction(transaction.AccountScopeID, transaction.OwnerSessionID, transaction.ID)), payload, nil); err != nil {
			return err
		}
	}
	if step := prepared.Projection.Step; step != nil {
		payload, err := json.Marshal(step)
		if err != nil {
			return fmt.Errorf("marshal artifact step: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactStep(step.AccountScopeID, step.UserID, step.ArtifactChainID, step.ID)), payload, nil); err != nil {
			return err
		}
	}
	if chain := prepared.Projection.Chain; chain != nil {
		payload, err := json.Marshal(chain)
		if err != nil {
			return fmt.Errorf("marshal artifact chain: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactChain(chain.AccountScopeID, chain.UserID, chain.ID)), payload, nil); err != nil {
			return err
		}
	}
	for _, definition := range prepared.PartDefinitions {
		payload, err := json.Marshal(definition)
		if err != nil {
			return fmt.Errorf("marshal artifact part definition: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactPartDefinition(definition.AccountScopeID, definition.OwnerSessionID, definition.ArtifactChainID, definition.ID)), payload, nil); err != nil {
			return err
		}
	}
	for _, revision := range prepared.PartRevisions {
		payload, err := json.Marshal(revision)
		if err != nil {
			return fmt.Errorf("marshal artifact part revision: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactPartRevision(revision.AccountScopeID, revision.OwnerSessionID, revision.ArtifactChainID, revision.PartID, revision.ID)), payload, nil); err != nil {
			return err
		}
	}
	if composition := prepared.Composition; composition != nil {
		payload, err := json.Marshal(composition)
		if err != nil {
			return fmt.Errorf("marshal artifact composition: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactComposition(composition.AccountScopeID, composition.OwnerSessionID, composition.ArtifactChainID, composition.ID)), payload, nil); err != nil {
			return err
		}
	}
	if prepared.PreviousCollection != nil && prepared.PreviousCollection.Status != collection.Status {
		if err := batch.Delete([]byte(KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, prepared.PreviousCollection.Status, collection.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
			return err
		}
	}
	collectionPayload, err := json.Marshal(collection)
	if err != nil {
		return fmt.Errorf("marshal artifact collection: %w", err)
	}
	if err := batch.Set([]byte(KeySessionArtifactCollection(collection.AccountScopeID, collection.SessionID, collection.ID)), collectionPayload, nil); err != nil {
		return err
	}
	if err := batch.Set([]byte(KeySessionArtifactCollectionStatus(collection.AccountScopeID, collection.SessionID, collection.Status, collection.ID)), []byte(collection.ID), nil); err != nil {
		return err
	}
	if variant := prepared.Projection.Variant; variant != nil && !prepared.DeleteVariant {
		if previous := prepared.PreviousVariant; previous != nil {
			for _, key := range artifactVariantLineageIndexKeys(*previous) {
				if err := batch.Delete([]byte(key), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
			if previous.Status != variant.Status {
				if err := batch.Delete([]byte(KeySessionArtifactVariantStatus(variant.AccountScopeID, variant.SessionID, previous.Status, variant.CollectionID, variant.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
			if previous.DigestSHA256 != "" && previous.DigestSHA256 != variant.DigestSHA256 {
				if err := batch.Delete([]byte(KeySessionArtifactVariantDigest(variant.AccountScopeID, variant.SessionID, previous.DigestSHA256, variant.CollectionID, variant.ID)), nil); err != nil && !errors.Is(err, pebble.ErrNotFound) {
					return err
				}
			}
		}
		variantPayload, err := json.Marshal(variant)
		if err != nil {
			return fmt.Errorf("marshal artifact variant: %w", err)
		}
		if err := batch.Set([]byte(KeySessionArtifactVariant(variant.AccountScopeID, variant.SessionID, variant.CollectionID, variant.ID)), variantPayload, nil); err != nil {
			return err
		}
		if err := batch.Set([]byte(KeySessionArtifactVariantStatus(variant.AccountScopeID, variant.SessionID, variant.Status, variant.CollectionID, variant.ID)), []byte(variant.ID), nil); err != nil {
			return err
		}
		if variant.DigestSHA256 != "" {
			if err := batch.Set([]byte(KeySessionArtifactVariantDigest(variant.AccountScopeID, variant.SessionID, variant.DigestSHA256, variant.CollectionID, variant.ID)), []byte(variant.ID), nil); err != nil {
				return err
			}
		}
		for _, key := range artifactVariantLineageIndexKeys(*variant) {
			if err := batch.Set([]byte(key), []byte(variant.CollectionID+"\x00"+variant.ID), nil); err != nil {
				return err
			}
		}
	}
	return nil
}

package api

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/artifact"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	sessionsV3ArtifactMaxBytes              int64 = 32 << 20
	sessionsV3ArtifactVideoMaxBytes         int64 = 512 << 20
	sessionsV3ArtifactPreviewTokenTTL             = 5 * time.Minute
	sessionsV3ArtifactPreviewRuntimeVersion       = "preview-runtime-v2"
	sessionsV3ArtifactPreviewAccessPath           = "access/"
	sessionsV3ArtifactPackageEntryPath            = "__swarm_artifact_entry__.html"
	sessionsV3ArtifactCatalogDefaultLimit         = 500
	sessionsV3ArtifactCatalogMaxLimit             = 2_000
	sessionsV3ArtifactCatalogSessionLimit         = 10_000
	// Preview HTML executes in an opaque origin (sandbox deliberately omits
	// allow-same-origin). The scoped bearer capability controls framing because
	// frame-ancestors cannot name an opaque parent. Scripts, Canvas, nested
	// srcdoc frames, and same-artifact package resources are supported; outbound
	// connections, forms, objects, and top-level navigation remain unavailable.
	sessionsV3ArtifactPreviewHTMLCSP           = "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' blob:; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' data: blob:; frame-src 'self' data: blob:; connect-src 'none'; worker-src blob:; object-src 'none'; base-uri 'self'; form-action 'none'"
	sessionsV3ArtifactPreviewPermissionsPolicy = "accelerometer=(), ambient-light-sensor=(), autoplay=(self), bluetooth=(), camera=(), clipboard-read=(), clipboard-write=(), display-capture=(), geolocation=(), gyroscope=(), hid=(), idle-detection=(), local-fonts=(), magnetometer=(), microphone=(), midi=(), payment=(), publickey-credentials-get=(), screen-wake-lock=(), serial=(), usb=(), web-share=(), window-management=(), xr-spatial-tracking=()"
)

type sessionsV3ArtifactPreviewTokenClaims struct {
	UserID         string `json:"user_id"`
	AccountScopeID string `json:"account_scope_id"`
	SessionID      string `json:"session_id"`
	ArtifactID     string `json:"artifact_id"`
	ExpiresAt      int64  `json:"expires_at"`
}

var sessionsV3ArtifactPackageMediaTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".gif":   "image/gif",
	".htm":   "text/html; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".jpeg":  "image/jpeg",
	".jpg":   "image/jpeg",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".mjs":   "text/javascript; charset=utf-8",
	".otf":   "font/otf",
	".png":   "image/png",
	".svg":   "image/svg+xml",
	".ttf":   "font/ttf",
	".txt":   "text/plain; charset=utf-8",
	".wasm":  "application/wasm",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

type sessionsV3ResolvedArtifact struct {
	Reference  pebblestore.SessionPlanArtifactReference
	Descriptor pebblestore.PlanFinalHandoffArtifact
	Managed    *pebblestore.SessionArtifactVariant
}

type sessionsV3ArtifactPartDefinitionProjection struct {
	ID          string                                  `json:"id"`
	Label       string                                  `json:"label"`
	Description string                                  `json:"description,omitempty"`
	Locator     *pebblestore.SessionArtifactPartLocator `json:"locator,omitempty"`
}

type sessionsV3ArtifactPartRevisionProjection struct {
	Reference        pebblestore.SessionArtifactPartRevisionReference `json:"reference"`
	ParentCommitOIDs []string                                         `json:"parent_commit_oids,omitempty"`
	IterationTurnID  string                                           `json:"iteration_turn_id,omitempty"`
	IterationGroupID string                                           `json:"iteration_group_id,omitempty"`
	CreatedAt        int64                                            `json:"created_at,omitempty"`
	EventSeq         uint64                                           `json:"event_seq,omitempty"`
}

type sessionsV3ArtifactCompositionProjection struct {
	ID               string                                       `json:"id"`
	ArtifactChainID  string                                       `json:"artifact_chain_id"`
	RepositoryID     string                                       `json:"repository_id"`
	CommitOID        string                                       `json:"commit_oid"`
	TreeOID          string                                       `json:"tree_oid"`
	ParentCommitOIDs []string                                     `json:"parent_commit_oids,omitempty"`
	IterationTurnID  string                                       `json:"iteration_turn_id,omitempty"`
	IterationGroupID string                                       `json:"iteration_group_id,omitempty"`
	Construction     pebblestore.SessionArtifactConstruction      `json:"construction"`
	Parts            []pebblestore.SessionArtifactCompositionPart `json:"parts"`
}

type sessionsV3ArtifactCatalogItem struct {
	ArtifactID            string                                         `json:"artifact_id"`
	SourceRef             string                                         `json:"source_ref,omitempty"`
	CollectionID          string                                         `json:"collection_id,omitempty"`
	SessionID             string                                         `json:"session_id"`
	SessionTitle          string                                         `json:"session_title"`
	WorkspacePath         string                                         `json:"workspace_path,omitempty"`
	WorkspaceName         string                                         `json:"workspace_name,omitempty"`
	PlanID                string                                         `json:"plan_id,omitempty"`
	PlanTitle             string                                         `json:"plan_title,omitempty"`
	CheckpointID          string                                         `json:"checkpoint_id,omitempty"`
	CheckpointTitle       string                                         `json:"checkpoint_title,omitempty"`
	Label                 string                                         `json:"label"`
	Description           string                                         `json:"description"`
	CollectionName        string                                         `json:"collection_name,omitempty"`
	CollectionDescription string                                         `json:"collection_description,omitempty"`
	Filename              string                                         `json:"filename"`
	MediaType             string                                         `json:"media_type"`
	Kind                  string                                         `json:"kind"`
	Status                string                                         `json:"status,omitempty"`
	FailureCode           string                                         `json:"failure_code,omitempty"`
	Previewable           bool                                           `json:"previewable"`
	Selected              bool                                           `json:"selected,omitempty"`
	Category              string                                         `json:"category"`
	UpdatedAt             int64                                          `json:"updated_at"`
	EventSeq              uint64                                         `json:"event_seq,omitempty"`
	Progress              *sessionsV3ArtifactCollectionProgress          `json:"progress,omitempty"`
	Lineage               *pebblestore.SessionArtifactLineage            `json:"lineage,omitempty"`
	OutputRequirements    *pebblestore.SessionArtifactOutputRequirements `json:"output_requirements,omitempty"`
	AnimationProfile      *pebblestore.SessionArtifactAnimationProfile   `json:"animation_profile,omitempty"`
	Chain                 *pebblestore.SessionArtifactChain              `json:"chain,omitempty"`
	Step                  *pebblestore.SessionArtifactStep               `json:"step,omitempty"`
	GraphState            string                                         `json:"graph_state,omitempty"`
	ParentArtifact        *pebblestore.SessionArtifactSelectionReference `json:"parent_artifact,omitempty"`
	ArtifactChainID       string                                         `json:"artifact_chain_id,omitempty"`
	ArtifactStepID        string                                         `json:"artifact_step_id,omitempty"`
	RevisionNumber        int                                            `json:"revision_number,omitempty"`
	RevisionRoundID       string                                         `json:"revision_round_id,omitempty"`
	CandidateIndex        int                                            `json:"candidate_index,omitempty"`
	Parts                 []pebblestore.SessionArtifactPart              `json:"parts,omitempty"`
	PartGraphState        string                                         `json:"part_graph_state,omitempty"`
	PartDefinitions       []sessionsV3ArtifactPartDefinitionProjection   `json:"part_definitions,omitempty"`
	PartRevisions         []sessionsV3ArtifactPartRevisionProjection     `json:"part_revisions,omitempty"`
	Composition           *sessionsV3ArtifactCompositionProjection       `json:"composition,omitempty"`
	TargetedPartID        string                                         `json:"targeted_part_id,omitempty"`
	TargetedPartIDs       []string                                       `json:"targeted_part_ids,omitempty"`
	AcceptedPartHeads     []pebblestore.SessionArtifactCompositionPart   `json:"accepted_part_heads,omitempty"`
	Content               string                                         `json:"content,omitempty"`
}

func cloneSessionsV3ArtifactOutputRequirements(input *pebblestore.SessionArtifactOutputRequirements) *pebblestore.SessionArtifactOutputRequirements {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func cloneSessionsV3ArtifactAnimationProfile(input *pebblestore.SessionArtifactAnimationProfile) *pebblestore.SessionArtifactAnimationProfile {
	if input == nil {
		return nil
	}
	cloned := *input
	return &cloned
}

func (s *Server) projectSessionsV3ArtifactComposition(session pebblestore.SessionSnapshot, variant pebblestore.SessionArtifactVariant, chain pebblestore.SessionArtifactChain) ([]sessionsV3ArtifactPartDefinitionProjection, []sessionsV3ArtifactPartRevisionProjection, *sessionsV3ArtifactCompositionProjection, string, []pebblestore.SessionArtifactCompositionPart, error) {
	if variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || variant.Composition == nil {
		return nil, nil, nil, "", nil, nil
	}
	composition := *variant.Composition
	if composition.GraphState != pebblestore.SessionArtifactGraphAuthoritative || composition.ArtifactChainID != variant.ArtifactChainID || composition.OwnerSessionID != variant.SessionID || len(composition.Parts) == 0 {
		return nil, nil, nil, "", nil, errors.New("authoritative artifact composition is inconsistent")
	}
	definitions := make([]sessionsV3ArtifactPartDefinitionProjection, 0, len(composition.Parts))
	revisions := make([]sessionsV3ArtifactPartRevisionProjection, 0, len(composition.Parts))
	for _, slot := range composition.Parts {
		definition, ok, err := s.sessions.GetSessionArtifactPartDefinition(session.AccountScopeID, session.UserID, slot.DefinitionOwnerSessionID, composition.ArtifactChainID, slot.PartID)
		if err != nil {
			return nil, nil, nil, "", nil, err
		}
		if !ok || definition.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
			return nil, nil, nil, "", nil, errors.New("authoritative artifact part definition is missing")
		}
		revision, ok, err := s.sessions.GetSessionArtifactPartRevision(session.AccountScopeID, session.UserID, slot.Revision.OwnerSessionID, slot.Revision.ArtifactChainID, slot.Revision.PartID, slot.Revision.PartRevisionID)
		if err != nil {
			return nil, nil, nil, "", nil, err
		}
		if !ok || revision.GraphState != pebblestore.SessionArtifactGraphAuthoritative || revision.Reference() != slot.Revision {
			return nil, nil, nil, "", nil, errors.New("authoritative artifact part revision is missing or stale")
		}
		var locator *pebblestore.SessionArtifactPartLocator
		if definition.Locator != nil {
			copy := *definition.Locator
			locator = &copy
		}
		definitions = append(definitions, sessionsV3ArtifactPartDefinitionProjection{ID: definition.ID, Label: definition.Label, Description: definition.Description, Locator: locator})
		revisions = append(revisions, sessionsV3ArtifactPartRevisionProjection{Reference: revision.Reference(), ParentCommitOIDs: append([]string(nil), revision.ParentCommitOIDs...), IterationTurnID: revision.IterationTurnID, IterationGroupID: revision.IterationGroupID, CreatedAt: revision.CreatedAt, EventSeq: revision.EventSeq})
	}
	projection := &sessionsV3ArtifactCompositionProjection{ID: composition.ID, ArtifactChainID: composition.ArtifactChainID, RepositoryID: composition.RepositoryID, CommitOID: composition.CommitOID, TreeOID: composition.TreeOID, ParentCommitOIDs: append([]string(nil), composition.ParentCommitOIDs...), IterationTurnID: composition.IterationTurnID, IterationGroupID: composition.IterationGroupID, Construction: composition.Construction, Parts: append([]pebblestore.SessionArtifactCompositionPart(nil), composition.Parts...)}
	targetedPartID, err := s.sessionsV3ArtifactTargetedPartID(session, variant, composition)
	if err != nil {
		return nil, nil, nil, "", nil, err
	}
	acceptedHeads, err := s.sessionsV3ArtifactAcceptedPartHeads(session, chain)
	if err != nil {
		return nil, nil, nil, "", nil, err
	}
	return definitions, revisions, projection, targetedPartID, acceptedHeads, nil
}

func sessionsV3ArtifactCatalogPartGraphState(variant pebblestore.SessionArtifactVariant) string {
	if variant.PartGraphState != "" {
		return variant.PartGraphState
	}
	if len(variant.Parts) != 0 {
		return pebblestore.SessionArtifactGraphLegacyUnproven
	}
	return ""
}

func sessionsV3ArtifactTargetedPartIDs(variant pebblestore.SessionArtifactVariant, composition *sessionsV3ArtifactCompositionProjection) []string {
	if variant.ParentArtifact == nil || composition == nil {
		return nil
	}
	changed := make([]string, 0, len(composition.Parts))
	// Part definitions are exactly the newly authenticated members of this immutable
	// composition; untouched slots are referenced without re-authoring rows.
	seen := make(map[string]struct{}, len(variant.PartDefinitions))
	for _, definition := range variant.PartDefinitions {
		if _, ok := seen[definition.ID]; ok || definition.ID == "" {
			continue
		}
		seen[definition.ID] = struct{}{}
		changed = append(changed, definition.ID)
	}
	sort.Strings(changed)
	return changed
}

func (s *Server) sessionsV3ArtifactTargetedPartID(session pebblestore.SessionSnapshot, variant pebblestore.SessionArtifactVariant, composition pebblestore.SessionArtifactComposition) (string, error) {
	if variant.ParentArtifact == nil || variant.ParentArtifact.VariantID == "" {
		return "", nil
	}
	parentRef := *variant.ParentArtifact
	parentSession, ok, err := s.sessions.GetSession(parentRef.SessionID)
	if err != nil {
		return "", err
	}
	if !ok || parentSession.AccountScopeID != session.AccountScopeID || parentSession.UserID != session.UserID {
		return "", errors.New("artifact composition parent ownership is inconsistent")
	}
	parent, ok, err := s.sessions.GetSessionArtifactVariant(parentSession.AccountScopeID, parentRef.SessionID, parentRef.CollectionID, parentRef.VariantID)
	if err != nil {
		return "", err
	}
	if !ok || parent.EventSeq != parentRef.EventSeq || parent.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || parent.Composition == nil {
		return "", errors.New("artifact composition parent is missing or stale")
	}
	if len(parent.Composition.Parts) != len(composition.Parts) {
		return "", errors.New("artifact composition changed its stable part set")
	}
	changed := ""
	changedCount := 0
	for index, slot := range composition.Parts {
		previous := parent.Composition.Parts[index]
		if slot.PartID != previous.PartID || slot.DefinitionOwnerSessionID != previous.DefinitionOwnerSessionID {
			return "", errors.New("artifact composition reordered or replaced a stable part definition")
		}
		if slot.Revision != previous.Revision || slot.Locked != previous.Locked {
			changedCount++
			if changedCount == 1 {
				changed = slot.PartID
			} else {
				// Empty identifies a valid multi-part turn. The exact changed set is
				// derived by Desktop from the immutable parent/current compositions.
				changed = ""
			}
		}
	}
	return changed, nil
}

func (s *Server) sessionsV3ArtifactAcceptedPartHeads(session pebblestore.SessionSnapshot, chain pebblestore.SessionArtifactChain) ([]pebblestore.SessionArtifactCompositionPart, error) {
	if chain.GraphState != pebblestore.SessionArtifactGraphAuthoritative || chain.Head.VariantID == "" {
		return nil, nil
	}
	headSession, ok, err := s.sessions.GetSession(chain.Head.SessionID)
	if err != nil {
		return nil, err
	}
	if !ok || headSession.AccountScopeID != session.AccountScopeID || headSession.UserID != session.UserID {
		return nil, errors.New("artifact chain head ownership is inconsistent")
	}
	head, ok, err := s.sessions.GetSessionArtifactVariant(session.AccountScopeID, chain.Head.SessionID, chain.Head.CollectionID, chain.Head.VariantID)
	if err != nil {
		return nil, err
	}
	if !ok || head.EventSeq != chain.Head.EventSeq || head.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || head.Composition == nil {
		return nil, errors.New("authoritative artifact chain head composition is missing or stale")
	}
	return append([]pebblestore.SessionArtifactCompositionPart(nil), head.Composition.Parts...), nil
}

type sessionsV3ArtifactCollectionProgress struct {
	Total       int `json:"total"`
	Staging     int `json:"staging"`
	Ready       int `json:"ready"`
	Failed      int `json:"failed"`
	Unavailable int `json:"unavailable"`
}

func (s *Server) handleSessionsV3Artifacts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	limit, ok := parseRequestPositiveLimit(w, r, sessionsV3ArtifactCatalogDefaultLimit)
	if !ok {
		return
	}
	if limit > sessionsV3ArtifactCatalogMaxLimit {
		writeError(w, http.StatusBadRequest, errors.New("artifact limit cannot exceed 2000"))
		return
	}
	requestedSessionID := strings.TrimSpace(r.URL.Query().Get("session_id"))

	sessions, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, sessionsV3ArtifactCatalogSessionLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if requestedSessionID != "" {
		requestedSession, found, err := s.sessions.GetSession(requestedSessionID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		if found && strings.TrimSpace(requestedSession.AccountScopeID) == principal.AccountScopeID && strings.TrimSpace(requestedSession.UserID) == principal.UserID {
			sessions = sessionsV3ArtifactCatalogEnsureSession(sessions, requestedSession)
		}
	}
	artifacts := make([]sessionsV3ArtifactCatalogItem, 0, limit)
	seen := make(map[string]struct{})
	for _, session := range sessions {
		if sessionsV3SystemSidechat(session) || !sessionsV3ArtifactCatalogIncludesSession(session, requestedSessionID) {
			continue
		}
		workspacePath, workspaceName := sessionsV3ArtifactCatalogWorkspace(session)
		nativeHandoffs := make(map[string]struct{})
		if s.artifacts != nil {
			// Repair redundant progress indexes before projection so interrupted historical metadata cannot hide valid artifacts.
			if _, err := s.sessions.RepairSessionArtifactCollections(session.ID); err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			collections, err := s.sessions.ListAllSessionArtifactCollections(session.AccountScopeID, session.ID, "")
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
			for _, collection := range collections {
				variants, err := s.sessions.ListSessionArtifactVariants(session.AccountScopeID, session.ID, collection.ID, pebblestore.SessionArtifactMaxVariantsPerCollection)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err)
					return
				}
				progress := sessionsV3ArtifactCollectionProgress{Total: collection.VariantCount, Staging: collection.StagingCount, Ready: collection.ReadyCount, Failed: collection.FailedCount, Unavailable: collection.UnavailableCount}
				visibleVariants := 0
				for _, candidate := range variants {
					if candidate.GraphState == pebblestore.SessionArtifactGraphProjection || candidate.ProjectionReservation {
						visibleVariants++
					}
				}
				if progress.Total != progress.Staging+progress.Ready+progress.Failed+progress.Unavailable || progress.Total != visibleVariants {
					writeError(w, http.StatusInternalServerError, errors.New("artifact collection progress is inconsistent"))
					return
				}
				for _, variant := range variants {
					if variant.GraphState != pebblestore.SessionArtifactGraphProjection && !variant.ProjectionReservation {
						continue
					}
					var chain pebblestore.SessionArtifactChain
					if variant.ProjectionReservation {
						// Reservations are visible turn progress only. They intentionally have no
						// chain/commit projection until a managed worker publishes real Git bytes.
						chain = pebblestore.SessionArtifactChain{}
					} else {
						projectedVariant, projectedChain, projectionErr := s.sessions.ProjectSessionArtifactVariantChain(session.AccountScopeID, session.UserID, variant)
						if projectionErr != nil {
							writeError(w, http.StatusInternalServerError, projectionErr)
							return
						}
						variant, chain = projectedVariant, projectedChain
					}
					if variant.Status != pebblestore.SessionArtifactStatusStaging && variant.Status != pebblestore.SessionArtifactStatusReady && variant.Status != pebblestore.SessionArtifactStatusFailed && variant.Status != pebblestore.SessionArtifactStatusUnavailable {
						writeError(w, http.StatusInternalServerError, errors.New("artifact variant status is inconsistent"))
						return
					}
					if collection.Lineage.TaskCallID != "" && variant.Lineage.TaskCallID != "" && collection.Lineage.TaskCallID != variant.Lineage.TaskCallID {
						writeError(w, http.StatusInternalServerError, errors.New("artifact variant task lineage is inconsistent"))
						return
					}
					if collection.Lineage.ProgramID != "" && variant.Lineage.ProgramID != "" && collection.Lineage.ProgramID != variant.Lineage.ProgramID {
						writeError(w, http.StatusInternalServerError, errors.New("artifact variant program lineage is inconsistent"))
						return
					}
					lineage := variant.Lineage
					kind, previewable := sessionsV3ArtifactPresentation(variant)
					if variant.Status == pebblestore.SessionArtifactStatusReady {
						handoffKind, handoffMediaType := kind, variant.MediaType
						if handoffKind == "package" && handoffMediaType == "application/zip" {
							handoffKind, handoffMediaType = "html", "text/html"
						}
						nativeHandoffs[sessionsV3NativeHandoffKey(lineage.PlanID, lineage.CheckpointID, lineage.RunID, lineage.AttemptID, variant.Filename, handoffMediaType, handoffKind)] = struct{}{}
					}
					if kind == "package" && variant.Status == pebblestore.SessionArtifactStatusReady && variant.MediaType == "application/zip" {
						kind, previewable = "html", true
					}
					var step *pebblestore.SessionArtifactStep
					var partDefinitions []sessionsV3ArtifactPartDefinitionProjection
					var partRevisions []sessionsV3ArtifactPartRevisionProjection
					var composition *sessionsV3ArtifactCompositionProjection
					var targetedPartID string
					var acceptedPartHeads []pebblestore.SessionArtifactCompositionPart
					if variant.GraphState == pebblestore.SessionArtifactGraphAuthoritative && variant.ArtifactChainID != "" && variant.ArtifactStepID != "" {
						persistedStep, found, stepErr := s.sessions.GetSessionArtifactStep(session.AccountScopeID, session.UserID, variant.ArtifactChainID, variant.ArtifactStepID)
						if stepErr != nil {
							writeError(w, http.StatusInternalServerError, stepErr)
							return
						}
						if !found || persistedStep.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
							writeError(w, http.StatusInternalServerError, errors.New("authoritative artifact step is missing"))
							return
						}
						step = &persistedStep
					}
					if variant.PartGraphState == pebblestore.SessionArtifactGraphAuthoritative {
						var projectionErr error
						partDefinitions, partRevisions, composition, targetedPartID, acceptedPartHeads, projectionErr = s.projectSessionsV3ArtifactComposition(session, variant, chain)
						if projectionErr != nil {
							writeError(w, http.StatusInternalServerError, projectionErr)
							return
						}
					}
					appendCatalogArtifact(&artifacts, seen, session.ID+"\x00"+variant.ID, sessionsV3ArtifactCatalogItem{
						ArtifactID: variant.ID, CollectionID: collection.ID, SessionID: session.ID, SessionTitle: session.Title,
						PlanID: lineage.PlanID, CheckpointID: lineage.CheckpointID,
						WorkspacePath: workspacePath, WorkspaceName: workspaceName,
						Label: firstNonEmpty(variant.Presentation.Label, collection.Name, variant.Filename), Description: firstNonEmpty(variant.Presentation.Description, collection.Description),
						CollectionName: collection.Name, CollectionDescription: collection.Description, Filename: variant.Filename, MediaType: variant.MediaType, Kind: kind, Status: variant.Status, FailureCode: variant.FailureCode,
						Previewable: previewable, Selected: chain.Head.SessionID == variant.SessionID && chain.Head.CollectionID == variant.CollectionID && chain.Head.VariantID == variant.ID,
						Category: sessionsV3ManagedArtifactCategory(variant), UpdatedAt: variant.UpdatedAt, EventSeq: variant.EventSeq, Progress: &progress, Lineage: &lineage, OutputRequirements: cloneSessionsV3ArtifactOutputRequirements(variant.OutputRequirements), AnimationProfile: cloneSessionsV3ArtifactAnimationProfile(variant.AnimationProfile),
						Chain: &chain, Step: step, GraphState: variant.GraphState, ParentArtifact: variant.ParentArtifact, ArtifactChainID: variant.ArtifactChainID, ArtifactStepID: variant.ArtifactStepID, RevisionNumber: variant.RevisionNumber, RevisionRoundID: variant.RevisionRoundID, CandidateIndex: variant.CandidateIndex, Parts: append([]pebblestore.SessionArtifactPart(nil), variant.Parts...),
						PartGraphState: sessionsV3ArtifactCatalogPartGraphState(variant), PartDefinitions: partDefinitions, PartRevisions: partRevisions, Composition: composition, TargetedPartID: targetedPartID, TargetedPartIDs: sessionsV3ArtifactTargetedPartIDs(variant, composition), AcceptedPartHeads: acceptedPartHeads,
					})
				}
			}
		}
		plans, _, err := s.sessions.ListPlans(session.ID, sessionsV3PlansPageMaxLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		for _, plan := range plans {
			if plan.AccountScopeID != "" && strings.TrimSpace(plan.AccountScopeID) != principal.AccountScopeID || plan.UserID != "" && strings.TrimSpace(plan.UserID) != principal.UserID {
				continue
			}
			planText := strings.TrimSpace(plan.Plan)
			if plan.Document != nil {
				if display := strings.TrimSpace(plan.Document.DisplayText); display != "" {
					planText = display
				} else if rendered := strings.TrimSpace(plan.Document.RenderedText); rendered != "" {
					planText = rendered
				}
			}
			planTitle := strings.TrimSpace(plan.Title)
			if planTitle == "" && plan.Document != nil {
				planTitle = strings.TrimSpace(plan.Document.Title)
			}
			if planTitle == "" {
				planTitle = "Plan"
			}
			planArtifactID := sessionsV3PlanArtifactID(session.ID, plan.ID)
			appendCatalogArtifact(&artifacts, seen, session.ID+"\x00"+planArtifactID, sessionsV3ArtifactCatalogItem{
				ArtifactID: planArtifactID, SessionID: session.ID, SessionTitle: session.Title,
				WorkspacePath: workspacePath, WorkspaceName: workspaceName,
				PlanID: plan.ID, PlanTitle: planTitle, Label: planTitle,
				Description: "Durable session plan", Filename: "plan.md", MediaType: "text/markdown",
				Kind: "markdown", Previewable: true, Category: "plan", UpdatedAt: plan.UpdatedAt, Content: planText,
			})

			if plan.Document == nil {
				continue
			}
			for _, checkpoint := range plan.Document.Checkpoints {
				if checkpoint.Handoff == nil || (strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted && strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusNeedsReview) {
					continue
				}
				references := append([]pebblestore.SessionPlanArtifactReference(nil), plan.Document.Artifacts...)
				references = append(references, checkpoint.Artifacts...)
				for _, descriptor := range sessionruntime.ProjectPlanFinalHandoffArtifacts(plan.ID, checkpoint.ID, references) {
					if _, ok := nativeHandoffs[sessionsV3NativeHandoffKey(plan.ID, checkpoint.ID, checkpoint.RunID, checkpoint.AttemptID, descriptor.Filename, descriptor.MediaType, descriptor.Kind)]; ok {
						// Native managed bytes are already cataloged; do not project a second unavailable workspace-backed compatibility item.
						continue
					}
					category := "document"
					if descriptor.Kind == "html" || descriptor.Kind == "image" || descriptor.Kind == "pdf" || descriptor.Kind == "video" {
						category = "visual"
					}
					updatedAt := checkpoint.CompletedAt
					if updatedAt == 0 {
						updatedAt = plan.UpdatedAt
					}
					artifactID := descriptor.ID
					appendCatalogArtifact(&artifacts, seen, session.ID+"\x00"+artifactID, sessionsV3ArtifactCatalogItem{
						ArtifactID: artifactID, SourceRef: descriptor.SourceRef, SessionID: session.ID, SessionTitle: session.Title,
						WorkspacePath: workspacePath, WorkspaceName: workspaceName,
						PlanID: plan.ID, PlanTitle: planTitle, CheckpointID: checkpoint.ID, CheckpointTitle: checkpoint.Title,
						Label: descriptor.Label, Description: descriptor.Description, Filename: descriptor.Filename,
						MediaType: descriptor.MediaType, Kind: descriptor.Kind, Previewable: descriptor.Previewable,
						Category: category, UpdatedAt: updatedAt,
					})
				}
			}
		}
	}
	sort.Slice(artifacts, func(i, j int) bool {
		if artifacts[i].UpdatedAt == artifacts[j].UpdatedAt {
			if artifacts[i].SessionID == artifacts[j].SessionID {
				return artifacts[i].ArtifactID < artifacts[j].ArtifactID
			}
			return artifacts[i].SessionID < artifacts[j].SessionID
		}
		return artifacts[i].UpdatedAt > artifacts[j].UpdatedAt
	})
	if len(artifacts) > limit {
		artifacts = artifacts[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "artifacts": artifacts, "local_reveal_available": sessionV3ArtifactRevealIsLoopback(r)})
}

func sessionsV3ArtifactCatalogEnsureSession(sessions []pebblestore.SessionSnapshot, requested pebblestore.SessionSnapshot) []pebblestore.SessionSnapshot {
	requestedSessionID := strings.TrimSpace(requested.ID)
	if requestedSessionID == "" {
		return sessions
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.ID) == requestedSessionID {
			return sessions
		}
	}
	return append(sessions, requested)
}

func sessionsV3ArtifactCatalogIncludesSession(session pebblestore.SessionSnapshot, requestedSessionID string) bool {
	if requestedSessionID == "" || session.ID == requestedSessionID {
		return true
	}
	return strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "parent_session_id")) == requestedSessionID
}

func sessionsV3NativeHandoffKey(planID, checkpointID, runID, attemptID, filename, mediaType, kind string) string {
	return strings.Join([]string{strings.TrimSpace(planID), strings.TrimSpace(checkpointID), strings.TrimSpace(runID), strings.TrimSpace(attemptID), strings.TrimSpace(filename), strings.ToLower(strings.TrimSpace(mediaType)), strings.ToLower(strings.TrimSpace(kind))}, "\x00")
}

func sessionsV3ArtifactPresentation(variant pebblestore.SessionArtifactVariant) (string, bool) {
	kind, previewable := variant.Presentation.Kind, variant.Presentation.Previewable
	if variant.Status != pebblestore.SessionArtifactStatusReady {
		return kind, previewable
	}
	mediaType := strings.ToLower(strings.TrimSpace(variant.MediaType))
	if mediaType == "image/svg+xml" {
		return "image", true
	}
	if mediaType == "text/html" && (kind == "" || kind == "html") {
		return "html", true
	}
	if mediaType == "text/markdown" && (kind == "" || kind == "markdown" || kind == "text") {
		return "markdown", true
	}
	if mediaType == "text/plain" && (kind == "" || kind == "text") {
		return "text", true
	}
	if kind == "package" && mediaType == "application/zip" {
		return "html", true
	}
	if mediaType == "video/mp4" {
		// MP4 bytes are validated as browser-safe before a variant becomes ready.
		// Repair historical and explicitly download-labelled publications so the
		// catalog still exposes the existing inline player and thumbnails.
		return "video", true
	}
	if strings.HasPrefix(mediaType, "video/") && (kind == "" || kind == "video") {
		return "video", true
	}
	return kind, previewable
}

func sessionsV3ManagedArtifactCategory(variant pebblestore.SessionArtifactVariant) string {
	kind, _ := sessionsV3ArtifactPresentation(variant)
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "html", "package", "image", "pdf", "video":
		return "visual"
	default:
		return "document"
	}
}

func appendCatalogArtifact(artifacts *[]sessionsV3ArtifactCatalogItem, seen map[string]struct{}, key string, artifact sessionsV3ArtifactCatalogItem) {
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}
	*artifacts = append(*artifacts, artifact)
}

func sessionsV3ArtifactCatalogWorkspace(session pebblestore.SessionSnapshot) (string, string) {
	workspacePath := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
	if workspacePath == "" {
		workspacePath = strings.TrimSpace(session.WorkspacePath)
	}
	workspaceName := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_name"))
	if workspaceName == "" {
		workspaceName = strings.TrimSpace(session.WorkspaceName)
	}
	if workspaceName == "" {
		workspaceName = filepath.Base(workspacePath)
	}
	return workspacePath, workspaceName
}

func sessionsV3PlanArtifactID(sessionID, planID string) string {
	canonical := strings.Join([]string{"swarm-plan-artifact-v1", strings.TrimSpace(sessionID), strings.TrimSpace(planID)}, "\x00")
	digest := sha256.Sum256([]byte(canonical))
	return "plan_art_" + base64.RawURLEncoding.EncodeToString(digest[:18])
}

func (s *Server) handleSessionV3ArtifactPreviewAccess(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if _, found, err := s.requireSessionV3Access(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		ArtifactID string `json:"artifact_id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	artifactID := strings.TrimSpace(req.ArtifactID)
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || artifact.Descriptor.Kind != "html" {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error_code": "artifact_preview_not_found", "error": "artifact preview not found"})
		return
	}
	if artifact.Managed != nil && (artifact.Managed.Status != pebblestore.SessionArtifactStatusReady || (artifact.Managed.MediaType != "application/zip" && artifact.Managed.MediaType != "text/html")) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error_code": "artifact_preview_not_ready", "error": "artifact preview is not ready"})
		return
	}
	expiresAt := time.Now().Add(sessionsV3ArtifactPreviewTokenTTL)
	token, err := s.issueSessionV3ArtifactPreviewToken(principal, sessionID, artifactID, expiresAt)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	previewURL := fmt.Sprintf("/v3/sessions/%s/artifacts/%s/content/access/%s/%s", sessionID, artifactID, token, sessionsV3ArtifactPackageEntryPath)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":            true,
		"token":         token,
		"expires_at":    expiresAt.Unix(),
		"preview_url":   previewURL,
		"media_type":    "text/html; charset=utf-8",
		"sandbox":       "allow-scripts",
		"opaque_origin": true,
	})
}

func (s *Server) issueSessionV3ArtifactPreviewToken(principal identity.Principal, sessionID, artifactID string, expiresAt time.Time) (string, error) {
	if s == nil || len(s.artifactPreviewKey) < 32 || !principal.Valid() {
		return "", errors.New("artifact preview access is unavailable")
	}
	claims := sessionsV3ArtifactPreviewTokenClaims{
		UserID:         strings.TrimSpace(principal.UserID),
		AccountScopeID: strings.TrimSpace(principal.AccountScopeID),
		SessionID:      strings.TrimSpace(sessionID),
		ArtifactID:     strings.TrimSpace(artifactID),
		ExpiresAt:      expiresAt.Unix(),
	}
	if claims.SessionID == "" || claims.ArtifactID == "" || claims.ExpiresAt <= time.Now().Unix() {
		return "", errors.New("invalid artifact preview access scope")
	}
	payload, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.artifactPreviewKey)
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nonce, nonce, payload, []byte("swarm-v3-artifact-preview-v1"))
	return base64.RawURLEncoding.EncodeToString(sealed), nil
}

func (s *Server) validateSessionV3ArtifactPreviewRequest(r *http.Request) (identity.Principal, bool) {
	if s == nil || r == nil || r.Method != http.MethodGet || len(s.artifactPreviewKey) < 32 {
		return identity.Principal{}, false
	}
	sessionID, subpath, ok := parseSessionsV3PrimaryPath(r.URL.Path)
	if !ok || !strings.HasPrefix(subpath, "artifacts/") || !strings.Contains(subpath, "/content/") {
		return identity.Principal{}, false
	}
	artifactPath := strings.TrimPrefix(subpath, "artifacts/")
	artifactID, contentPath, ok := strings.Cut(artifactPath, "/content/")
	artifactID = strings.TrimSpace(artifactID)
	if !ok || artifactID == "" || strings.Contains(artifactID, "/") {
		return identity.Principal{}, false
	}
	token, _, ok := strings.Cut(strings.TrimPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath), "/")
	if !ok || !strings.HasPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath) {
		return identity.Principal{}, false
	}
	sealed, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return identity.Principal{}, false
	}
	block, err := aes.NewCipher(s.artifactPreviewKey)
	if err != nil {
		return identity.Principal{}, false
	}
	aead, err := cipher.NewGCM(block)
	if err != nil || len(sealed) < aead.NonceSize() {
		return identity.Principal{}, false
	}
	nonce, ciphertext := sealed[:aead.NonceSize()], sealed[aead.NonceSize():]
	payload, err := aead.Open(nil, nonce, ciphertext, []byte("swarm-v3-artifact-preview-v1"))
	if err != nil {
		return identity.Principal{}, false
	}
	var claims sessionsV3ArtifactPreviewTokenClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return identity.Principal{}, false
	}
	now := time.Now().Unix()
	if strings.TrimSpace(claims.SessionID) != sessionID || strings.TrimSpace(claims.ArtifactID) != artifactID || claims.ExpiresAt <= now || claims.ExpiresAt > now+int64(sessionsV3ArtifactPreviewTokenTTL/time.Second)+30 {
		return identity.Principal{}, false
	}
	principal := identity.Principal{
		Type:               identity.PrincipalTypeUser,
		UserID:             strings.TrimSpace(claims.UserID),
		AccountScopeID:     strings.TrimSpace(claims.AccountScopeID),
		AccountScopeSource: identity.AccountScopeSourceSession,
		TokenExpires:       time.Unix(claims.ExpiresAt, 0),
	}
	return principal, principal.Valid()
}

func (s *Server) handleSessionV3ArtifactSelection(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s == nil || s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	if _, found, err := s.requireSessionV3Access(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		ClientRequestID string `json:"client_request_id"`
		EventSeq        uint64 `json:"event_seq"`
		Action          string `json:"action,omitempty"`
		ArtifactChainID string `json:"artifact_chain_id,omitempty"`
		ArtifactStepID  string `json:"artifact_step_id,omitempty"`
		PartID          string `json:"part_id,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	req.ClientRequestID = strings.TrimSpace(req.ClientRequestID)
	if req.ClientRequestID == "" || len(req.ClientRequestID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required and must be 256 characters or fewer"))
		return
	}
	action := strings.ToLower(strings.TrimSpace(req.Action))
	if action == "" {
		action = "select"
	}
	if action != "select" && action != "use" {
		writeError(w, http.StatusBadRequest, errors.New("artifact selection action must be select or use"))
		return
	}
	variant, found, err := s.sessions.GetSessionArtifactVariantByID(principal.AccountScopeID, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found || variant.Status != pebblestore.SessionArtifactStatusReady {
		writeError(w, http.StatusBadRequest, errors.New("only a ready artifact variant can be selected"))
		return
	}
	if req.EventSeq == 0 || req.EventSeq != variant.EventSeq {
		writeError(w, http.StatusConflict, errors.New("artifact selection event sequence is stale"))
		return
	}
	if strings.TrimSpace(req.ArtifactChainID) == "" || strings.TrimSpace(req.ArtifactStepID) == "" || strings.TrimSpace(req.ArtifactChainID) != variant.ArtifactChainID || strings.TrimSpace(req.ArtifactStepID) != variant.ArtifactStepID {
		writeError(w, http.StatusConflict, errors.New("artifact selection chain or step identity is stale"))
		return
	}
	collection, found, err := s.sessions.GetSessionArtifactCollection(principal.AccountScopeID, sessionID, variant.CollectionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeError(w, http.StatusBadRequest, errors.New("artifact collection was not found"))
		return
	}
	partID := strings.TrimSpace(req.PartID)
	partLabel, partKind := "", ""
	if partID != "" {
		if variant.PartGraphState != pebblestore.SessionArtifactGraphAuthoritative || variant.Composition == nil {
			writeError(w, http.StatusBadRequest, errors.New("artifact part selection requires an authoritative composition"))
			return
		}
		definitionOwner := ""
		for _, slot := range variant.Composition.Parts {
			if slot.PartID == partID {
				definitionOwner = slot.DefinitionOwnerSessionID
				break
			}
		}
		definition, ok, definitionErr := s.sessions.GetSessionArtifactPartDefinition(principal.AccountScopeID, principal.UserID, definitionOwner, variant.Composition.ArtifactChainID, partID)
		if definitionErr != nil {
			writeError(w, http.StatusInternalServerError, definitionErr)
			return
		}
		if !ok || definition.GraphState != pebblestore.SessionArtifactGraphAuthoritative {
			writeError(w, http.StatusBadRequest, errors.New("artifact part was not found in the exact authoritative composition"))
			return
		}
		partLabel, partKind = definition.Label, "semantic"
		if definition.Locator != nil {
			partKind = definition.Locator.Kind
		}
	}
	selection := &pebblestore.SessionArtifactSelectionReference{
		SessionID: sessionID, CollectionID: collection.ID, VariantID: variant.ID, EventSeq: req.EventSeq, Action: action,
		PartID: partID, PartLabel: partLabel, PartKind: partKind,
		Label:       firstNonEmpty(variant.Presentation.Label, collection.Name, variant.Filename),
		Description: firstNonEmpty(variant.Presentation.Description, collection.Description),
	}
	authority := artifact.NewAuthority(s.artifacts, s.sessions)
	selectedRef, err := authority.SelectReference(artifact.Principal{SessionID: sessionID, AccountScopeID: principal.AccountScopeID, UserID: principal.UserID}, req.ClientRequestID, collection.ID, *selection)
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error_code": "idempotency_conflict", "error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "stale") || strings.Contains(err.Error(), "already accepted") {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error_code": "artifact_acceptance_conflict", "error": err.Error()})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response := map[string]any{"ok": true, "session_id": sessionID, "action": action, "selection": selectedRef}
	writeJSON(w, http.StatusOK, response)
}

func sessionsV3ArtifactSelectionPayloadHash(sessionID, action string, selection pebblestore.SessionArtifactSelectionReference) (string, error) {
	canonical := struct {
		Operation string                                        `json:"operation"`
		SessionID string                                        `json:"session_id"`
		Action    string                                        `json:"action"`
		Selection pebblestore.SessionArtifactSelectionReference `json:"selection"`
	}{sessionruntime.SessionMutationSelectArtifact, strings.TrimSpace(sessionID), strings.TrimSpace(action), selection}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), nil
}

func (s *Server) handleSessionV3Artifact(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	var file sessionsV3ReadSeekCloser
	var info os.FileInfo
	mediaType := artifact.Descriptor.MediaType
	if artifact.Descriptor.Kind == "html" && artifact.Managed != nil && artifact.Managed.MediaType == "application/zip" {
		file, info, mediaType, err = s.openSessionV3ArtifactPackageFile(r.Context(), session, artifact, sessionsV3ArtifactPackageEntryPath)
	} else {
		file, info, err = s.openSessionV3Artifact(r.Context(), session, artifact)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact file is unavailable"))
		return
	}
	defer file.Close()
	maxAllowedBytes := sessionsV3ArtifactMaxBytes
	if artifact.Descriptor.Kind == "video" || artifact.Descriptor.MediaType == "video/mp4" || strings.HasPrefix(artifact.Descriptor.MediaType, "video/") {
		maxAllowedBytes = sessionsV3ArtifactVideoMaxBytes
	}
	if info.Size() > maxAllowedBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("artifact exceeds the preview size limit"))
		return
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": artifact.Descriptor.Filename})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setSessionV3ArtifactCacheHeaders(w, artifact, "")
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src data: blob:; media-src 'self' data: blob:; style-src 'unsafe-inline'; font-src data:; frame-ancestors 'self'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if sessionV3ArtifactNotModified(w, r, artifact, "") {
		return
	}
	http.ServeContent(w, r, artifact.Descriptor.Filename, info.ModTime(), file)
}

func sessionV3ArtifactRequiresBundle(artifact sessionsV3ResolvedArtifact) bool {
	if artifact.Managed != nil {
		return strings.EqualFold(strings.TrimSpace(artifact.Managed.MediaType), "application/zip") || strings.EqualFold(strings.TrimSpace(artifact.Managed.Presentation.Kind), "package")
	}
	return strings.EqualFold(strings.TrimSpace(artifact.Descriptor.MediaType), "application/zip") || strings.EqualFold(strings.TrimSpace(artifact.Descriptor.Kind), "package")
}

func (s *Server) handleSessionV3ArtifactBundle(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}

	file, info, err := s.openSessionV3Artifact(r.Context(), session, artifact)
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact bundle is unavailable"))
		return
	}
	defer file.Close()
	if !sessionV3ArtifactRequiresBundle(artifact) {
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": artifact.Descriptor.Filename})
		if disposition == "" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Type", artifact.Descriptor.MediaType)
		w.Header().Set("Content-Disposition", disposition)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Accept-Ranges", "bytes")
		w.Header().Set("Referrer-Policy", "no-referrer")
		http.ServeContent(w, r, artifact.Descriptor.Filename, info.ModTime(), file)
		return
	}

	bundleName := sessionV3ArtifactBundleFilename(artifact.Descriptor.Filename)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": bundleName})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if r.Method == http.MethodHead {
		return
	}
	if artifact.Managed != nil && artifact.Managed.MediaType == "application/zip" {
		_, _ = io.Copy(w, file)
		return
	}
	archive := zip.NewWriter(w)
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		_ = archive.Close()
		return
	}
	header.Name = path.Join(sessionV3ArtifactBundleRootName(artifact.Descriptor.Filename), artifact.Descriptor.Filename)
	header.Method = zip.Deflate
	header.Modified = time.Time{}
	header.SetMode(0o600)
	entry, err := archive.CreateHeader(header)
	if err != nil {
		_ = archive.Close()
		return
	}
	if _, err := io.Copy(entry, file); err != nil {
		_ = archive.Close()
		return
	}
	_ = archive.Close()
}

func (s *Server) handleSessionV3ArtifactReveal(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !sessionV3ArtifactRevealIsLoopback(r) {
		writeError(w, http.StatusForbidden, errors.New("artifact reveal is available only from this machine"))
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	resolved, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, errors.New("artifact not found"))
		return
	}
	if resolved.Managed == nil {
		artifactPath, err := resolveSessionV3ArtifactFilePath(sessionV3ArtifactWorkspaceRoot(session), resolved.Reference.Path)
		if err != nil {
			writeError(w, http.StatusNotFound, errors.New("artifact file is unavailable"))
			return
		}
		method, err := revealLocalPath(artifactPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("artifact file manager could not open it: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "method": method, "display_location": artifactPath})
		return
	}
	if resolved.Managed.Status != pebblestore.SessionArtifactStatusReady {
		writeError(w, http.StatusNotFound, errors.New("ready managed artifact not found"))
		return
	}
	destination := sessionV3ArtifactLibraryName(firstNonEmpty(resolved.Managed.Presentation.Label, resolved.Managed.Filename), resolved.Managed.ID)
	s.handleSessionV3ArtifactLibraryPublish(w, r, principal, session, destination, []pebblestore.SessionArtifactVariant{*resolved.Managed})
}

func (s *Server) handleSessionV3ArtifactCollectionReveal(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, collectionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if !sessionV3ArtifactRevealIsLoopback(r) {
		writeError(w, http.StatusForbidden, errors.New("artifact reveal is available only from this machine"))
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	collection, found, err := s.sessions.GetSessionArtifactCollection(principal.AccountScopeID, sessionID, collectionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || collection.SessionID != sessionID || collection.AccountScopeID != principal.AccountScopeID {
		writeError(w, http.StatusNotFound, errors.New("artifact collection not found"))
		return
	}
	variants, err := s.sessions.ListSessionArtifactVariants(principal.AccountScopeID, sessionID, collectionID, pebblestore.SessionArtifactMaxVariantsPerCollection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ready := make([]pebblestore.SessionArtifactVariant, 0, len(variants))
	for _, variant := range variants {
		if variant.Status == pebblestore.SessionArtifactStatusReady {
			ready = append(ready, variant)
		}
	}
	if len(ready) == 0 {
		writeError(w, http.StatusNotFound, errors.New("artifact collection has no ready variants"))
		return
	}
	destination := sessionV3ArtifactLibraryName(firstNonEmpty(collection.Name, "Artifact collection"), collection.ID)
	s.handleSessionV3ArtifactLibraryPublish(w, r, principal, session, destination, ready)
}

func (s *Server) handleSessionV3ArtifactLibraryPublish(w http.ResponseWriter, r *http.Request, principal identity.Principal, session pebblestore.SessionSnapshot, destination string, variants []pebblestore.SessionArtifactVariant) {
	if s == nil || s.artifacts == nil || s.uiSettings == nil {
		writeError(w, http.StatusInternalServerError, errors.New("artifact Git authority is not configured"))
		return
	}
	settings, err := s.uiSettings.GetForAccount(principal.AccountScopeID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	cacheRoot, cacheErr := sessionV3ArtifactWorkingCopyCacheRoot()
	if cacheErr != nil {
		cacheRoot = ""
	}
	libraryRoot, err := artifact.ResolveLibraryRoot(settings.Artifacts.LibraryDirectory, cacheRoot)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := artifact.EnsureLibraryRoot(libraryRoot); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	authority := artifact.NewAuthority(s.artifacts, s.sessions)
	inputs := make([]artifact.BatchMaterializeInput, 0, len(variants))
	for _, variant := range variants {
		if variant.SessionID != session.ID || variant.AccountScopeID != principal.AccountScopeID {
			writeError(w, http.StatusNotFound, errors.New("artifact ownership does not match session"))
			return
		}
		body, readErr := authority.ReadVariant(r.Context(), artifact.Principal{SessionID: session.ID, AccountScopeID: session.AccountScopeID, UserID: session.UserID}, variant, sessionsV3ArtifactMaxBytes)
		if readErr != nil {
			writeError(w, http.StatusBadRequest, readErr)
			return
		}
		inputs = append(inputs, artifact.BatchMaterializeInput{Variant: variant, Body: body})
	}
	sessionDirectory := sessionV3ArtifactLibraryName(firstNonEmpty(session.Title, "Session"), session.ID)
	relative := filepath.Join(sessionDirectory, destination)
	if _, err := artifact.MaterializeBatch(r.Context(), artifact.Limits{}, inputs, libraryRoot, relative, true); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	published := filepath.Join(libraryRoot, relative)
	method, err := revealLocalPath(published)
	if err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("artifact published but file manager could not open it: %w", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "method": method, "display_location": published})
}

func sessionV3ArtifactWorkingCopyCacheRoot() (string, error) {
	for _, environmentVariable := range []string{"CACHE_DIRECTORY", "SWARMD_CACHE_DIR"} {
		if configured := strings.TrimSpace(os.Getenv(environmentVariable)); configured != "" {
			if !filepath.IsAbs(configured) {
				return "", fmt.Errorf("%s must be an absolute path", environmentVariable)
			}
			return filepath.Clean(configured), nil
		}
	}
	return os.UserCacheDir()
}

func sessionV3ArtifactRevealIsLoopback(r *http.Request) bool {
	if r == nil {
		return false
	}
	if admission, ok := admittedDesktopOrigin(r); ok && admission.tailscaleServe {
		return false
	}
	ip := remoteRequestIP(r)
	return ip != nil && ip.IsLoopback()
}

func sessionV3ArtifactLibraryName(label, stableID string) string {
	name := strings.TrimSpace(label)
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == ' ' || r == '.' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, " .-")
	name = strings.Join(strings.Fields(name), " ")
	if len(name) > 80 {
		name = strings.TrimSpace(name[:80])
	}
	id := sessionV3ArtifactSafeDownloadName(stableID)
	if len(id) > 12 {
		id = id[:12]
	}
	if name == "" {
		name = "Artifact"
	}
	if id != "" {
		name += " - " + id
	}
	return name
}

func (s *Server) handleSessionV3ArtifactCollectionBundle(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, collectionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	collection, found, err := s.sessions.GetSessionArtifactCollection(principal.AccountScopeID, sessionID, collectionID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || collection.SessionID != sessionID || collection.AccountScopeID != principal.AccountScopeID {
		writeError(w, http.StatusNotFound, errors.New("artifact collection not found"))
		return
	}
	variants, err := s.sessions.ListSessionArtifactVariants(principal.AccountScopeID, sessionID, collectionID, pebblestore.SessionArtifactMaxVariantsPerCollection)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	ready := make([]pebblestore.SessionArtifactVariant, 0, len(variants))
	for _, variant := range variants {
		if variant.Status == pebblestore.SessionArtifactStatusReady {
			ready = append(ready, variant)
		}
	}
	if len(ready) == 0 {
		writeError(w, http.StatusNotFound, errors.New("artifact collection has no ready variants"))
		return
	}
	if r.Method == http.MethodHead {
		bundleName := sessionV3ArtifactCollectionBundleFilename(collection.Name, collection.ID)
		disposition := mime.FormatMediaType("attachment", map[string]string{"filename": bundleName})
		if disposition == "" {
			disposition = "attachment"
		}
		w.Header().Set("Content-Type", "application/zip")
		w.Header().Set("Content-Disposition", disposition)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Referrer-Policy", "no-referrer")
		return
	}

	type collectionBundleFile struct {
		variant pebblestore.SessionArtifactVariant
		file    sessionsV3ReadSeekCloser
	}
	files := make([]collectionBundleFile, 0, len(ready))
	for _, variant := range ready {
		resolved, ok, resolveErr := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, variant.ID)
		if resolveErr != nil || !ok {
			for _, opened := range files {
				_ = opened.file.Close()
			}
			writeError(w, http.StatusNotFound, errors.New("artifact collection bundle is unavailable"))
			return
		}
		file, _, openErr := s.openSessionV3Artifact(r.Context(), session, resolved)
		if openErr != nil {
			for _, opened := range files {
				_ = opened.file.Close()
			}
			writeError(w, http.StatusNotFound, errors.New("artifact collection bundle is unavailable"))
			return
		}
		files = append(files, collectionBundleFile{variant: variant, file: file})
	}
	defer func() {
		for _, opened := range files {
			_ = opened.file.Close()
		}
	}()

	bundleName := sessionV3ArtifactCollectionBundleFilename(collection.Name, collection.ID)
	disposition := mime.FormatMediaType("attachment", map[string]string{"filename": bundleName})
	if disposition == "" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	archive := zip.NewWriter(w)
	usedNames := make(map[string]int, len(files))
	for index, opened := range files {
		filename := sessionV3ArtifactCollectionEntryName(opened.variant.Filename, opened.variant.ID, index+1, usedNames)
		header := &zip.FileHeader{Name: filename, Method: zip.Deflate, Modified: time.Time{}}
		header.SetMode(0o600)
		entry, createErr := archive.CreateHeader(header)
		if createErr == nil {
			_, createErr = io.Copy(entry, opened.file)
		}
		if createErr != nil {
			_ = archive.Close()
			return
		}
	}
	_ = archive.Close()
}

func sessionV3ArtifactCollectionEntryName(filename, variantID string, index int, used map[string]int) string {
	name := sessionV3ArtifactSafeDownloadName(filename)
	if name == "" {
		name = sessionV3ArtifactSafeDownloadName(variantID)
	}
	if name == "" {
		name = fmt.Sprintf("variant-%d", index)
	}
	key := strings.ToLower(name)
	used[key]++
	if used[key] == 1 {
		return name
	}
	extension := filepath.Ext(name)
	stem := strings.TrimSuffix(name, extension)
	return fmt.Sprintf("%s-%d%s", stem, used[key], extension)
}

func sessionV3ArtifactCollectionBundleFilename(collectionName, collectionID string) string {
	name := sessionV3ArtifactSafeDownloadName(collectionName)
	if name == "" {
		name = sessionV3ArtifactSafeDownloadName(collectionID)
	}
	name = strings.TrimSuffix(name, filepath.Ext(name))
	if name == "" {
		name = "artifact-collection"
	}
	return name + ".zip"
}

func sessionV3ArtifactSafeDownloadName(value string) string {
	name := filepath.Base(strings.TrimSpace(value))
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	return strings.Trim(name, ".-")
}

func sessionV3ArtifactBundleRootName(value string) string {
	name := strings.TrimSpace(value)
	name = strings.TrimSuffix(name, filepath.Ext(name))
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return '-'
	}, name)
	name = strings.Trim(name, ".-")
	if name == "" {
		return "artifact"
	}
	return name
}

func sessionV3ArtifactBundleFilename(artifactFilename string) string {
	return sessionV3ArtifactBundleRootName(artifactFilename) + ".zip"
}

func (s *Server) handleSessionV3ArtifactContent(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, artifactID, contentPath string) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		methodNotAllowed(w)
		return
	}
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if strings.HasPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath) {
		_, scopedPath, ok := strings.Cut(strings.TrimPrefix(contentPath, sessionsV3ArtifactPreviewAccessPath), "/")
		if !ok || strings.TrimSpace(scopedPath) == "" {
			writeError(w, http.StatusNotFound, errors.New("artifact package file is unavailable"))
			return
		}
		contentPath = scopedPath
	}
	artifact, found, err := s.resolveSessionV3Artifact(r.Context(), principal, sessionID, artifactID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if !found || artifact.Descriptor.Kind != "html" || (artifact.Managed != nil && artifact.Managed.Status != pebblestore.SessionArtifactStatusReady) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error_code": "artifact_preview_not_found", "error": "artifact preview not found"})
		return
	}

	var file sessionsV3ReadSeekCloser
	var info os.FileInfo
	var mediaType string
	if artifact.Managed != nil && artifact.Managed.MediaType == "text/html" {
		if contentPath != sessionsV3ArtifactPackageEntryPath {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error_code": "artifact_preview_resource_not_found", "error": "artifact preview resource not found"})
			return
		}
		file, info, err = s.openSessionV3Artifact(r.Context(), session, artifact)
		mediaType = "text/html; charset=utf-8"
	} else {
		file, info, mediaType, err = s.openSessionV3ArtifactPackageFile(r.Context(), session, artifact, contentPath)
	}
	if err != nil {
		writeError(w, http.StatusNotFound, errors.New("artifact package file is unavailable"))
		return
	}
	defer file.Close()
	if info.Size() > sessionsV3ArtifactMaxBytes {
		writeError(w, http.StatusRequestEntityTooLarge, errors.New("artifact package file exceeds the preview size limit"))
		return
	}
	if strings.HasPrefix(mediaType, "text/html") {
		if bootstrap := sessionV3ArtifactPreviewRuntimeBootstrap(artifact); bootstrap != "" {
			data, readErr := io.ReadAll(io.LimitReader(file, sessionsV3ArtifactMaxBytes+1))
			if readErr != nil || int64(len(data)) > sessionsV3ArtifactMaxBytes {
				writeError(w, http.StatusInternalServerError, errors.New("artifact preview runtime could not prepare the document"))
				return
			}
			data = injectSessionV3ArtifactPreviewRuntime(data, bootstrap)
			info = sessionsV3MemoryFileInfo{name: filepath.Base(contentPath), size: int64(len(data)), modTime: info.ModTime()}
			file = sessionsV3NewMemoryFile(data)
		}
	}

	disposition := mime.FormatMediaType("inline", map[string]string{"filename": filepath.Base(contentPath)})
	if disposition == "" {
		disposition = "inline"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", disposition)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	setSessionV3ArtifactCacheHeaders(w, artifact, contentPath)
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if strings.HasPrefix(mediaType, "text/html") {
		setSessionV3ArtifactPreviewSecurityHeaders(w, r, artifact)
	}
	if sessionV3ArtifactNotModified(w, r, artifact, contentPath) {
		return
	}
	http.ServeContent(w, r, filepath.Base(contentPath), info.ModTime(), file)
}

func setSessionV3ArtifactPreviewSecurityHeaders(w http.ResponseWriter, r *http.Request, artifact sessionsV3ResolvedArtifact) {
	w.Header().Set("Content-Security-Policy", sessionV3ArtifactPreviewHTMLCSP(r, artifact))
	w.Header().Set("Permissions-Policy", sessionsV3ArtifactPreviewPermissionsPolicy)
	// Reviewed runtime modules are fetched from the same install by an opaque
	// sandbox origin. Send only the origin (never the bearer-capability path) so
	// the Desktop boundary can admit those exact runtime requests.
	w.Header().Set("Referrer-Policy", "origin")
}

func sessionV3ArtifactPreviewHTMLCSP(r *http.Request, artifact sessionsV3ResolvedArtifact) string {
	profileID := ""
	if artifact.Managed != nil && artifact.Managed.AnimationProfile != nil {
		profileID = strings.TrimSpace(artifact.Managed.AnimationProfile.ProfileID)
	}
	if profileID != "spatial_3d" && profileID != "vector_playback" {
		return sessionsV3ArtifactPreviewHTMLCSP
	}
	admission, ok := admittedDesktopOrigin(r)
	if !ok {
		return sessionsV3ArtifactPreviewHTMLCSP
	}
	runtimeSource := strings.TrimRight(admission.origin, "/") + desktopAnimationRuntimePath
	connectSource := "'none'"
	if profileID == "vector_playback" {
		connectSource = runtimeSource
	}
	return "sandbox allow-scripts; default-src 'none'; script-src 'self' 'unsafe-inline' blob: " + runtimeSource +
		"; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob:; font-src 'self' data:; media-src 'self' data: blob:; frame-src 'self' data: blob:; connect-src " + connectSource +
		"; worker-src blob: " + runtimeSource + "; object-src 'none'; base-uri 'self'; form-action 'none'"
}

func sessionV3ArtifactPreviewRuntimeBootstrap(artifact sessionsV3ResolvedArtifact) string {
	if artifact.Managed == nil || artifact.Managed.AnimationProfile == nil {
		return ""
	}
	profileID := strings.TrimSpace(artifact.Managed.AnimationProfile.ProfileID)
	modules := map[string]string{}
	wasm := map[string]string{}
	switch profileID {
	case "spatial_3d":
		modules["three"] = "/swarm-animation-runtime/three.module.js"
	case "vector_playback":
		modules["@lottiefiles/dotlottie-web"] = "/swarm-animation-runtime/dotlottie.js"
		modules["@rive-app/canvas"] = "/swarm-animation-runtime/rive.js"
		wasm["dotLottie"] = "/swarm-animation-runtime/dotlottie-player.wasm"
		wasm["rive"] = "/swarm-animation-runtime/rive.wasm"
		wasm["riveFallback"] = "/swarm-animation-runtime/rive_fallback.wasm"
	default:
		return ""
	}
	config, err := json.Marshal(map[string]any{"modules": modules, "wasm": wasm})
	if err != nil {
		return ""
	}
	imports, err := json.Marshal(map[string]any{"imports": modules})
	if err != nil {
		return ""
	}
	config = bytes.ReplaceAll(config, []byte("<"), []byte(`\u003c`))
	imports = bytes.ReplaceAll(imports, []byte("<"), []byte(`\u003c`))
	return `<script>globalThis.__SWARM_ANIMATION_RUNTIME__=` + string(config) + `;</script>` +
		`<script type="importmap">` + string(imports) + `</script>`
}

func injectSessionV3ArtifactPreviewRuntime(source []byte, bootstrap string) []byte {
	if bootstrap == "" {
		return source
	}
	lower := strings.ToLower(string(source))
	if head := strings.Index(lower, "<head"); head >= 0 {
		if end := strings.Index(lower[head:], ">"); end >= 0 {
			insertAt := head + end + 1
			return append(append(append([]byte(nil), source[:insertAt]...), bootstrap...), source[insertAt:]...)
		}
	}
	if html := strings.Index(lower, "<html"); html >= 0 {
		if end := strings.Index(lower[html:], ">"); end >= 0 {
			insertAt := html + end + 1
			head := "<head>" + bootstrap + "</head>"
			return append(append(append([]byte(nil), source[:insertAt]...), head...), source[insertAt:]...)
		}
	}
	return append([]byte("<!doctype html><head>"+bootstrap+"</head>"), source...)
}

func sessionV3ArtifactETag(artifact sessionsV3ResolvedArtifact, resourcePath string) string {
	if artifact.Managed == nil || len(artifact.Managed.DigestSHA256) != sha256.Size*2 {
		return ""
	}
	identity := artifact.Managed.DigestSHA256
	if artifact.Managed.AnimationProfile != nil {
		identity += "-" + sessionsV3ArtifactPreviewRuntimeVersion
	}
	if resourcePath != "" {
		sum := sha256.Sum256([]byte(filepath.ToSlash(resourcePath)))
		identity += "-" + fmt.Sprintf("%x", sum[:8])
	}
	return `"sha256-` + identity + `"`
}

func setSessionV3ArtifactCacheHeaders(w http.ResponseWriter, artifact sessionsV3ResolvedArtifact, resourcePath string) {
	etag := sessionV3ArtifactETag(artifact, resourcePath)
	if etag == "" {
		w.Header().Set("Cache-Control", "no-store")
		return
	}
	w.Header().Set("ETag", etag)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
}

func sessionV3ArtifactNotModified(w http.ResponseWriter, r *http.Request, artifact sessionsV3ResolvedArtifact, resourcePath string) bool {
	etag := sessionV3ArtifactETag(artifact, resourcePath)
	if etag == "" || strings.TrimSpace(r.Header.Get("If-None-Match")) != etag {
		return false
	}
	w.WriteHeader(http.StatusNotModified)
	return true
}

func (s *Server) resolveSessionV3Artifact(ctx context.Context, principal identity.Principal, sessionID, artifactID string) (sessionsV3ResolvedArtifact, bool, error) {
	artifactID = strings.TrimSpace(artifactID)
	if artifactID == "" || strings.Contains(artifactID, "/") || !principal.Valid() {
		return sessionsV3ResolvedArtifact{}, false, nil
	}
	session, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	}
	if !found || session.UserID != principal.UserID || session.AccountScopeID != principal.AccountScopeID {
		return sessionsV3ResolvedArtifact{}, false, nil
	}
	plans := make([]pebblestore.SessionPlanSnapshot, 0, sessionsV3PlansPageMaxLimit+1)
	if active, ok, err := s.sessions.GetActivePlan(sessionID); err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	} else if ok {
		plans = append(plans, active)
	}
	listed, _, err := s.sessions.ListPlans(sessionID, sessionsV3PlansPageMaxLimit)
	if err != nil {
		return sessionsV3ResolvedArtifact{}, false, err
	}
	for _, plan := range listed {
		if len(plans) == 0 || plan.ID != plans[0].ID {
			plans = append(plans, plan)
		}
	}
	if s.artifacts != nil {
		if managed, ok, err := s.sessions.GetSessionArtifactVariantByID(principal.AccountScopeID, sessionID, artifactID); err != nil {
			return sessionsV3ResolvedArtifact{}, false, err
		} else if ok {
			if managed.SessionID != sessionID || managed.AccountScopeID != principal.AccountScopeID || (managed.Lineage.ParentSessionID != "" && managed.Lineage.ParentSessionID != sessionID) {
				return sessionsV3ResolvedArtifact{}, false, nil
			}
			if managed.Status != pebblestore.SessionArtifactStatusReady {
				return sessionsV3ResolvedArtifact{}, false, nil
			}
			resolved := sessionsV3ResolvedArtifact{Managed: &managed}
			// Path is an in-memory filename hint for packaged managed artifacts.
			// Native managed bytes remain addressed by opaque IDs.
			resolved.Reference = pebblestore.SessionPlanArtifactReference{Path: managed.Filename, Description: managed.Presentation.Description}
			kind, previewable := sessionsV3ArtifactPresentation(managed)
			resolved.Descriptor = pebblestore.PlanFinalHandoffArtifact{
				ID: managed.ID, Label: firstNonEmpty(managed.Presentation.Label, managed.Filename), Description: managed.Presentation.Description,
				Filename: managed.Filename, MediaType: managed.MediaType, Kind: kind, Previewable: previewable,
			}
			return resolved, true, nil
		}
	}
	for _, plan := range plans {
		if plan.Document == nil || (plan.SessionID != "" && plan.SessionID != sessionID) {
			continue
		}
		if plan.SessionID == "" {
			plan.SessionID = sessionID
		}
		for _, checkpoint := range plan.Document.Checkpoints {
			if checkpoint.Handoff == nil || (strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusCompleted && strings.TrimSpace(checkpoint.Status) != sessionruntime.PlanCheckpointStatusNeedsReview) {
				continue
			}
			artifacts := append([]pebblestore.SessionPlanArtifactReference(nil), plan.Document.Artifacts...)
			artifacts = append(artifacts, checkpoint.Artifacts...)
			for _, reference := range artifacts {
				descriptors := sessionruntime.ProjectPlanFinalHandoffArtifacts(plan.ID, checkpoint.ID, []pebblestore.SessionPlanArtifactReference{reference})
				if len(descriptors) != 1 {
					continue
				}
				descriptor := descriptors[0]
				if artifactID != descriptor.ID || (plan.UserID != "" && plan.UserID != principal.UserID) || (plan.AccountScopeID != "" && plan.AccountScopeID != principal.AccountScopeID) {
					continue
				}
				return sessionsV3ResolvedArtifact{Reference: reference, Descriptor: descriptor}, true, nil
			}
		}
	}
	return sessionsV3ResolvedArtifact{}, false, nil
}

func (s *Server) openSessionV3Artifact(ctx context.Context, session pebblestore.SessionSnapshot, resolved sessionsV3ResolvedArtifact) (sessionsV3ReadSeekCloser, os.FileInfo, error) {
	if resolved.Managed == nil {
		return openSessionV3ArtifactFile(sessionV3ArtifactWorkspaceRoot(session), resolved.Reference.Path)
	}
	if resolved.Managed.Status != pebblestore.SessionArtifactStatusReady || s.artifacts == nil {
		return nil, nil, artifact.ErrNotReady
	}
	authority := artifact.NewAuthority(s.artifacts, s.sessions)
	body, _, err := authority.ReadReference(ctx, artifact.Principal{SessionID: session.ID, AccountScopeID: session.AccountScopeID, UserID: session.UserID}, pebblestore.SessionArtifactSelectionReference{SessionID: resolved.Managed.SessionID, CollectionID: resolved.Managed.CollectionID, VariantID: resolved.Managed.ID, EventSeq: resolved.Managed.EventSeq}, sessionsV3ArtifactMaxBytes)
	if err != nil {
		return nil, nil, err
	}
	info := sessionsV3MemoryFileInfo{name: resolved.Managed.Filename, size: int64(len(body)), modTime: time.UnixMilli(resolved.Managed.UpdatedAt)}
	return sessionsV3NewMemoryFile(body), info, nil
}

func (s *Server) openSessionV3ArtifactPackageFile(ctx context.Context, session pebblestore.SessionSnapshot, resolved sessionsV3ResolvedArtifact, contentPath string) (sessionsV3ReadSeekCloser, os.FileInfo, string, error) {
	if resolved.Managed == nil {
		return openWorkspaceSessionV3ArtifactPackageFile(sessionV3ArtifactWorkspaceRoot(session), resolved.Reference.Path, contentPath)
	}
	file, _, err := s.openSessionV3Artifact(ctx, session, resolved)
	if err != nil {
		return nil, nil, "", err
	}
	defer file.Close()
	archive, err := zip.NewReader(file, resolved.Managed.Size)
	if err != nil {
		return nil, nil, "", err
	}
	contentPath = strings.TrimSpace(contentPath)
	entryName := filepath.ToSlash(filepath.Clean(filepath.FromSlash(contentPath)))
	if filepath.IsAbs(filepath.FromSlash(contentPath)) || strings.HasPrefix(entryName, "/") {
		return nil, nil, "", errors.New("artifact package path must be relative")
	}
	if contentPath == sessionsV3ArtifactPackageEntryPath {
		entryName = managedSessionV3ArtifactPackageEntry(archive)
	}
	if entryName == "." || entryName == ".." || strings.HasPrefix(entryName, "../") || strings.Contains(contentPath, "\\") {
		return nil, nil, "", errors.New("artifact package path escapes its directory")
	}
	mediaType, ok := sessionsV3ArtifactPackageMediaTypes[strings.ToLower(filepath.Ext(entryName))]
	if !ok {
		return nil, nil, "", errors.New("artifact package file type is not previewable")
	}
	for _, entry := range archive.File {
		cleanEntry := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name)))
		if cleanEntry != entryName || cleanEntry == ".." || strings.HasPrefix(cleanEntry, "../") || strings.Contains(entry.Name, "\\") || !entry.Mode().IsRegular() {
			continue
		}
		if entry.UncompressedSize64 > uint64(sessionsV3ArtifactMaxBytes) {
			return nil, nil, "", errors.New("artifact package file exceeds the preview size limit")
		}
		reader, err := entry.Open()
		if err != nil {
			return nil, nil, "", err
		}
		data, err := io.ReadAll(io.LimitReader(reader, sessionsV3ArtifactMaxBytes+1))
		_ = reader.Close()
		if err != nil || int64(len(data)) > sessionsV3ArtifactMaxBytes {
			return nil, nil, "", errors.New("artifact package file is unavailable")
		}
		info := sessionsV3MemoryFileInfo{name: filepath.Base(entryName), size: int64(len(data)), modTime: entry.ModTime()}
		return sessionsV3NewMemoryFile(data), info, mediaType, nil
	}
	return nil, nil, "", errors.New("artifact package file is unavailable")
}

func managedSessionV3ArtifactPackageEntry(archive *zip.Reader) string {
	if archive == nil {
		return "index.html"
	}
	for _, candidate := range []string{"index.html", "index.htm"} {
		for _, entry := range archive.File {
			if filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name))) == candidate && entry.Mode().IsRegular() {
				return candidate
			}
		}
	}
	for _, entry := range archive.File {
		name := filepath.ToSlash(filepath.Clean(filepath.FromSlash(entry.Name)))
		if entry.Mode().IsRegular() && name != "." && name != ".." && !strings.HasPrefix(name, "/") && !strings.Contains(entry.Name, "\\") && !strings.HasPrefix(name, "../") && (strings.HasSuffix(strings.ToLower(name), ".html") || strings.HasSuffix(strings.ToLower(name), ".htm")) {
			return name
		}
	}
	return "index.html"
}

type sessionsV3MemoryFileInfo struct {
	name    string
	size    int64
	modTime time.Time
}

func (i sessionsV3MemoryFileInfo) Name() string       { return i.name }
func (i sessionsV3MemoryFileInfo) Size() int64        { return i.size }
func (i sessionsV3MemoryFileInfo) Mode() os.FileMode  { return 0o600 }
func (i sessionsV3MemoryFileInfo) ModTime() time.Time { return i.modTime }
func (i sessionsV3MemoryFileInfo) IsDir() bool        { return false }
func (i sessionsV3MemoryFileInfo) Sys() any           { return nil }

type sessionsV3ReadSeekCloser interface {
	io.ReadSeeker
	io.ReaderAt
	io.Closer
}

type sessionsV3MemoryFile struct{ *bytes.Reader }

func (sessionsV3MemoryFile) Close() error { return nil }

func sessionsV3NewMemoryFile(data []byte) sessionsV3ReadSeekCloser {
	return sessionsV3MemoryFile{Reader: bytes.NewReader(data)}
}

func sessionV3ArtifactWorkspaceRoot(session pebblestore.SessionSnapshot) string {
	for _, candidate := range []string{
		sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_workspace_path"),
		session.WorktreeRootPath,
		session.WorkspacePath,
	} {
		if candidate = strings.TrimSpace(candidate); candidate != "" {
			return candidate
		}
	}
	return ""
}

func openWorkspaceSessionV3ArtifactPackageFile(workspaceRoot, artifactPath, contentPath string) (*os.File, os.FileInfo, string, error) {
	artifactPath = strings.TrimSpace(artifactPath)
	contentPath = strings.TrimSpace(contentPath)
	if contentPath == sessionsV3ArtifactPackageEntryPath {
		contentPath = filepath.Base(filepath.FromSlash(artifactPath))
	}
	if artifactPath == "" || contentPath == "" || filepath.IsAbs(contentPath) || strings.Contains(contentPath, "\\") {
		return nil, nil, "", errors.New("invalid artifact package path")
	}
	packageRoot := filepath.Clean(filepath.Dir(filepath.FromSlash(artifactPath)))
	if packageRoot == ".." || strings.HasPrefix(packageRoot, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("html artifact package escapes workspace")
	}
	cleanContent := filepath.Clean(filepath.FromSlash(contentPath))
	if cleanContent == "." || cleanContent == ".." || strings.HasPrefix(cleanContent, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("artifact package path escapes its directory")
	}
	mediaType, ok := sessionsV3ArtifactPackageMediaTypes[strings.ToLower(filepath.Ext(cleanContent))]
	if !ok {
		return nil, nil, "", errors.New("artifact package file type is not previewable")
	}
	candidate := filepath.Join(packageRoot, cleanContent)
	relative, err := filepath.Rel(packageRoot, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, "", errors.New("artifact package path escapes its directory")
	}
	file, info, err := openSessionV3ArtifactFile(workspaceRoot, filepath.ToSlash(candidate))
	if err != nil {
		return nil, nil, "", err
	}
	return file, info, mediaType, nil
}

func resolveSessionV3ArtifactFilePath(workspaceRoot, artifactPath string) (string, error) {
	workspaceRoot = strings.TrimSpace(workspaceRoot)
	artifactPath = strings.TrimSpace(artifactPath)
	if workspaceRoot == "" || artifactPath == "" || filepath.IsAbs(artifactPath) || strings.Contains(artifactPath, "\\") {
		return "", errors.New("invalid artifact path")
	}
	cleanRelative := filepath.Clean(filepath.FromSlash(artifactPath))
	if cleanRelative == "." || cleanRelative == ".." || strings.HasPrefix(cleanRelative, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes workspace")
	}
	resolvedRoot, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return "", err
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return "", err
	}
	candidate := filepath.Join(resolvedRoot, cleanRelative)
	resolvedCandidate, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	resolvedCandidate, err = filepath.Abs(resolvedCandidate)
	if err != nil {
		return "", err
	}
	if filepath.Clean(candidate) != filepath.Clean(resolvedCandidate) {
		return "", errors.New("artifact path contains a symlink")
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("artifact path escapes workspace")
	}
	info, err := os.Stat(resolvedCandidate)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("artifact is not a regular file")
	}
	return resolvedCandidate, nil
}

func openSessionV3ArtifactFile(workspaceRoot, artifactPath string) (*os.File, os.FileInfo, error) {
	resolvedCandidate, err := resolveSessionV3ArtifactFilePath(workspaceRoot, artifactPath)
	if err != nil {
		return nil, nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(strings.TrimSpace(workspaceRoot))
	if err != nil {
		return nil, nil, err
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return nil, nil, err
	}
	relative, err := filepath.Rel(resolvedRoot, resolvedCandidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, nil, errors.New("artifact path escapes workspace")
	}
	root, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	file, err := root.Open(relative)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		file.Close()
		return nil, nil, errors.New("artifact is not a regular file")
	}
	return file, info, nil
}

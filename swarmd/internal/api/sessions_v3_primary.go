package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/modelpolicy"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	sessionsV3PrimaryPrefix                  = "/v3/sessions/"
	sessionsV3PrimaryDefaultMessageTailLimit = 500
	sessionsV3PrimaryDefaultEventLimit       = 0
	sessionsV3MessagesPageDefaultLimit       = 200
	sessionsV3MessagesPageMaxLimit           = 200
	sessionsV3PlansPageDefaultLimit          = 100
	sessionsV3PlansPageMaxLimit              = 100
)

// V3 primary write handlers delegate through the ApplySessionMutation boundary.

type sessionsV3CreateRequest struct {
	SessionID                      string                        `json:"session_id,omitempty"`
	ClientRequestID                string                        `json:"client_request_id,omitempty"`
	IdempotencyKey                 string                        `json:"idempotency_key,omitempty"`
	Title                          string                        `json:"title,omitempty"`
	WorkspacePath                  string                        `json:"workspace_path"`
	WorkspaceName                  string                        `json:"workspace_name,omitempty"`
	WorkspaceBindingID             string                        `json:"workspace_binding_id,omitempty"`
	SwarmID                        string                        `json:"swarm_id,omitempty"`
	TargetKind                     string                        `json:"target_kind,omitempty"`
	TargetRelationship             string                        `json:"target_relationship,omitempty"`
	HostWorkspacePath              string                        `json:"host_workspace_path,omitempty"`
	RuntimeWorkspacePath           string                        `json:"runtime_workspace_path,omitempty"`
	Mode                           string                        `json:"mode,omitempty"`
	AgentName                      string                        `json:"agent_name"`
	Preference                     pebblestore.ModelPreference   `json:"preference,omitempty"`
	LegacyManagedWorktreeRequested json.RawMessage               `json:"managed_worktree_requested,omitempty"` // rolling decode only
	WorktreeMode                   string                        `json:"worktree_mode,omitempty"`
	WorktreeUseCurrentBranch       *bool                         `json:"worktree_use_current_branch,omitempty"`
	WorktreeBaseBranch             string                        `json:"worktree_base_branch,omitempty"`
	WorktreeBranchName             string                        `json:"worktree_branch_name,omitempty"`
	WorktreeExistingPath           string                        `json:"worktree_existing_path,omitempty"`
	Metadata                       map[string]any                `json:"metadata,omitempty"`
	ModelProfile                   *sessionsV3ModelProfileChoice `json:"model_profile,omitempty"`
}

type sessionsV3ModelProfileInline struct {
	Name        string `json:"name,omitempty"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Thinking    string `json:"thinking"`
	ServiceTier string `json:"service_tier,omitempty"`
	ContextMode string `json:"context_mode,omitempty"`
}

type sessionsV3ModelProfileChoice struct {
	UseAccountDefault *bool                         `json:"use_account_default,omitempty"`
	SavedProfileID    string                        `json:"saved_profile_id,omitempty"`
	Temporary         *sessionsV3ModelProfileInline `json:"temporary,omitempty"`
	UseAgentDefault   *bool                         `json:"use_agent_default,omitempty"`
}

type sessionsV3ModelProfileApplyRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	IfProjectionSeq *uint64                      `json:"if_projection_seq,omitempty"`
	Choice          sessionsV3ModelProfileChoice `json:"choice"`
}

type sessionsV3MessageRequest struct {
	ClientRequestID    string                                          `json:"client_request_id,omitempty"`
	IdempotencyKey     string                                          `json:"idempotency_key,omitempty"`
	MessageID          string                                          `json:"message_id,omitempty"`
	RunID              string                                          `json:"run_id,omitempty"`
	Role               string                                          `json:"role"`
	Content            string                                          `json:"content"`
	Metadata           map[string]any                                  `json:"metadata,omitempty"`
	Media              []pebblestore.SessionMediaReference             `json:"media,omitempty"`
	VideoAttachments   []pebblestore.SessionVideoAttachmentReference   `json:"video_attachments,omitempty"`
	ArtifactSelections []pebblestore.SessionArtifactSelectionReference `json:"artifact_selections,omitempty"`
	DispatchAuthority  map[string]any                                  `json:"dispatch_authority,omitempty"`
}

type sessionsV3StopRequest struct {
	Type          string `json:"type,omitempty"`
	RunID         string `json:"run_id"`
	TargetSwarmID string `json:"target_swarm_id"`
	Reason        string `json:"reason,omitempty"`
}

type sessionsV3SubagentStopRequest struct {
	SessionID string `json:"session_id"`
}

type sessionsV3CompactRequest struct {
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
	RunID           string `json:"run_id,omitempty"`
	Note            string `json:"note,omitempty"`
	AgentName       string `json:"agent_name,omitempty"`
	Instructions    string `json:"instructions,omitempty"`
}

type sessionsV3AgentRequest struct {
	AgentName       string `json:"agent_name"`
	ClientRequestID string `json:"client_request_id,omitempty"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

type sessionsV3PreferenceRequest struct {
	ClientRequestID string  `json:"client_request_id,omitempty"`
	IdempotencyKey  string  `json:"idempotency_key,omitempty"`
	Provider        *string `json:"provider,omitempty"`
	Model           *string `json:"model,omitempty"`
	Thinking        *string `json:"thinking,omitempty"`
	ServiceTier     *string `json:"service_tier,omitempty"`
	ContextMode     *string `json:"context_mode,omitempty"`
}

type sessionsV3SettingsPatchRequest struct {
	ClientRequestID string                       `json:"client_request_id"`
	IfProjectionSeq *uint64                      `json:"if_projection_seq,omitempty"`
	AgentName       *string                      `json:"agent_name,omitempty"`
	Preference      *sessionsV3PreferenceRequest `json:"preference,omitempty"`
}

type sessionsV3MetadataRequest struct {
	Metadata        map[string]any `json:"metadata"`
	ClientRequestID string         `json:"client_request_id,omitempty"`
	IdempotencyKey  string         `json:"idempotency_key,omitempty"`
}

type sessionsV3TitleRequest struct {
	Title           string `json:"title"`
	ClientRequestID string `json:"client_request_id"`
	IdempotencyKey  string `json:"idempotency_key,omitempty"`
}

type sessionsV3PlanUpsertRequest struct {
	ID            string                            `json:"id"`
	PlanID        string                            `json:"plan_id"`
	Title         string                            `json:"title"`
	Plan          string                            `json:"plan"`
	Document      *pebblestore.SessionPlanDocument  `json:"document"`
	DocumentPatch *sessionruntime.PlanDocumentPatch `json:"document_patch"`
	Status        string                            `json:"status"`
	ApprovalState string                            `json:"approval_state"`
	UpdateSummary string                            `json:"update_summary"`
	UpdateScope   string                            `json:"update_scope"`
	Scope         string                            `json:"scope"`
	UpdateKind    string                            `json:"update_kind"`
	RevisionKind  string                            `json:"revision_kind"`
	Checkpoint    bool                              `json:"checkpoint"`
	Activate      *bool                             `json:"activate"`
}

type sessionsV3HydratedSession struct {
	Session                pebblestore.SessionSnapshot       `json:"session"`
	Projection             sessionruntime.SessionProjection  `json:"projection"`
	Messages               []pebblestore.MessageSnapshot     `json:"messages"`
	Events                 []sessionruntime.SessionEvent     `json:"events"`
	PendingPermissions     []pebblestore.PermissionRecord    `json:"pending_permissions"`
	UsageSummary           *pebblestore.SessionUsageSummary  `json:"usage_summary,omitempty"`
	ActiveRunIntent        *pebblestore.V3SessionRunIntent   `json:"active_run_intent,omitempty"`
	Preference             pebblestore.ModelPreference       `json:"preference"`
	ContextWindow          int                               `json:"context_window"`
	MaxOutputTokens        int                               `json:"max_output_tokens"`
	AgentModelPolicy       sessionsV3AgentModelPolicy        `json:"agent_model_policy"`
	HasActivePlan          bool                              `json:"has_active_plan"`
	ActivePlan             pebblestore.SessionPlanSnapshot   `json:"active_plan,omitempty"`
	PlanRevisions          []pebblestore.SessionPlanSnapshot `json:"plan_revisions"`
	AppliedSeq             uint64                            `json:"applied_seq"`
	HighWatermark          uint64                            `json:"high_watermark"`
	SnapshotEndpointCursor string                            `json:"snapshot_endpoint_cursor"`
}

type sessionsV3AgentModelPolicy = modelpolicy.AgentModelPolicy

type sessionsV3ArchiveBatchRequest struct {
	SessionIDs []string `json:"session_ids"`
}

type sessionsV3DeleteRequest struct {
	SessionIDs        []string                   `json:"session_ids,omitempty"`
	UpdatedBefore     *int64                     `json:"updated_before,omitempty"`
	ArchivedMode      string                     `json:"archived_mode,omitempty"`
	Global            bool                       `json:"global,omitempty"`
	Workspace         sessionsV3WorksetWorkspace `json:"workspace,omitempty"`
	WorkspacePath     string                     `json:"workspace_path,omitempty"`
	WorkspacePaths    []string                   `json:"workspace_paths,omitempty"`
	DryRun            bool                       `json:"dry_run,omitempty"`
	ConfirmationToken string                     `json:"confirmation_token,omitempty"`
	ConfirmRecent     bool                       `json:"confirm_recent,omitempty"`
}

type sessionsV3DeletePreview struct {
	ConversationCount int      `json:"conversation_count"`
	SessionCount      int      `json:"session_count"`
	ChildCount        int      `json:"child_count"`
	LogicalBytes      int64    `json:"logical_bytes"`
	ActiveRunCount    int      `json:"active_run_count"`
	PendingCount      int      `json:"pending_approval_count"`
	Recent75Overlap   int      `json:"recent_75_overlap_count"`
	SessionIDs        []string `json:"session_ids"`
	ConfirmationToken string   `json:"confirmation_token"`
}

func (s *Server) handleSessionsV3Primary(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}

	if r.URL.Path == "/v3/sessions:archive" {
		s.handleSessionsV3PrimaryArchiveBatch(w, r, principal)
		return
	}

	switch r.Method {
	case http.MethodGet:
		s.handleSessionsV3PrimaryList(w, r, principal)
	case http.MethodPost:
		s.handleSessionsV3PrimaryCreate(w, r, principal)
	default:
		methodNotAllowed(w)
	}
}

func (s *Server) handleSessionV3PrimaryByID(w http.ResponseWriter, r *http.Request) {
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 service is not configured"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	sessionID, subpath, ok := parseSessionsV3PrimaryPath(r.URL.Path)
	if !ok {
		writeError(w, http.StatusBadRequest, errors.New("invalid sessions v3 path"))
		return
	}
	switch subpath {
	case "":
		if r.Method == http.MethodDelete {
			s.handleSessionV3PrimaryDelete(w, r, principal, sessionID)
			return
		}
		// Legacy/debug/compat full-hydrate endpoint only. Desktop V3
		// canonical boot, hydrate, replay, and realtime must use
		// /v3/sync/bootstrap, /v3/sync/hydrate, /v3/sync/stream, and
		// /v3/realtime/stream instead of this per-session snapshot shape.
		if r.Method != http.MethodGet {
			methodNotAllowed(w)
			return
		}
		messageLimit, eventLimit, ok := parseSessionsV3HydrationLimits(w, r)
		if !ok {
			return
		}
		hydrated, found, err := s.hydrateSessionsV3PrimaryWithLimits(principal, sessionID, messageLimit, eventLimit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !found {
			writeSessionNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, sessionsV3HydratedResponse(hydrated, gitStatusResponseForSession(hydrated.Session)))
	case "archive":
		s.handleSessionV3PrimaryArchive(w, r, principal, sessionID)
	case "messages":
		s.handleSessionV3PrimaryMessages(w, r, principal, sessionID)
	case "media":
		s.handleSessionV3MediaUpload(w, r, principal, sessionID)
	case "media-capability":
		s.handleSessionV3MediaCapability(w, r, principal, sessionID)
	case "events":
		s.handleSessionV3PrimaryEvents(w, r, principal, sessionID)
	case "run/stop":
		s.handleSessionV3PrimaryRunStop(w, r, principal, sessionID)
	case "run/stream":
		s.handleSessionV3PrimaryRunStreamControl(w, r, sessionID, principal)
	case "compact":
		s.handleSessionV3PrimaryCompact(w, r, principal, sessionID)
	case "settings":
		s.handleSessionV3PrimarySettings(w, r, principal, sessionID)
	case "agent":
		s.handleSessionV3PrimaryAgent(w, r, principal, sessionID)
	case "preference":
		s.handleSessionV3PrimaryPreference(w, r, principal, sessionID)
	case "model-profile":
		s.handleSessionV3PrimaryModelProfile(w, r, principal, sessionID)
	case "usage":
		s.handleSessionV3PrimaryUsage(w, r, principal, sessionID)
	case "metadata":
		s.handleSessionV3PrimaryMetadata(w, r, principal, sessionID)
	case "title":
		s.handleSessionV3PrimaryTitle(w, r, principal, sessionID)
	case "plans":
		s.handleSessionV3PrimaryPlans(w, r, principal, sessionID)
	case "plans/active":
		s.handleSessionV3PrimaryActivePlan(w, r, principal, sessionID)
	case "permissions":
		s.handleSessionV3PrimaryPermissions(w, r, principal, sessionID)
	case "sidechats/plan":
		s.handleSessionV3SystemSidechat(w, r, principal, sessionID, "plan")
	case "sidechats/ai":
		s.handleSessionV3SystemSidechat(w, r, principal, sessionID, "ai")
	case "permissions/resolve_all":
		s.handleSessionV3PrimaryPermissionResolveAll(w, r, principal, sessionID)
	case "artifacts/preview-access":
		s.handleSessionV3ArtifactPreviewAccess(w, r, principal, sessionID)
	case "video/sources/media":
		s.handleSessionV3VideoSourceMedia(w, r, principal, sessionID)
	case "video/projects":
		s.handleSessionV3VideoProjects(w, r, principal, sessionID)
	default:
		if strings.HasPrefix(subpath, "video/") {
			s.handleSessionV3VideoSubpath(w, r, principal, sessionID, strings.TrimPrefix(subpath, "video/"))
			return
		}
		if subpath == "artifacts-v3" || strings.HasPrefix(subpath, "artifacts-v3/") {
			s.handleSessionV3ArtifactsV3(w, r, principal, sessionID, strings.TrimPrefix(subpath, "artifacts-v3"))
			return
		}
		if subpath == "artifact-v2" || strings.HasPrefix(subpath, "artifact-v2/") {
			s.handleSessionV3ArtifactV2(w, r, principal, sessionID, strings.TrimPrefix(subpath, "artifact-v2"))
			return
		}
		if strings.HasPrefix(subpath, "artifacts/") {
			artifactPath := strings.TrimSpace(strings.TrimPrefix(subpath, "artifacts/"))
			if collectionPath, hasCollection := strings.CutPrefix(artifactPath, "collections/"); hasCollection {
				collectionID, action, ok := strings.Cut(collectionPath, "/")
				collectionID = strings.TrimSpace(collectionID)
				if !ok || collectionID == "" || strings.Contains(collectionID, "/") {
					writeError(w, http.StatusBadRequest, errors.New("invalid artifact collection path"))
					return
				}
				switch action {
				case "bundle":
					s.handleSessionV3ArtifactCollectionBundle(w, r, principal, sessionID, collectionID)
				case "reveal":
					s.handleSessionV3ArtifactCollectionReveal(w, r, principal, sessionID, collectionID)
				default:
					writeError(w, http.StatusBadRequest, errors.New("invalid artifact collection path"))
				}
				return
			}
			if artifactID, hasPartSelection := strings.CutSuffix(artifactPath, "/part-selection"); hasPartSelection {
				artifactID = strings.TrimSpace(artifactID)
				if artifactID == "" || strings.Contains(artifactID, "/") {
					writeError(w, http.StatusBadRequest, errors.New("artifact id is required"))
					return
				}
				s.handleSessionV3ArtifactPartSelection(w, r, principal, sessionID, artifactID)
				return
			}
			if artifactID, hasSelection := strings.CutSuffix(artifactPath, "/selection"); hasSelection {
				artifactID = strings.TrimSpace(artifactID)
				if artifactID == "" || strings.Contains(artifactID, "/") {
					writeError(w, http.StatusBadRequest, errors.New("artifact id is required"))
					return
				}
				s.handleSessionV3ArtifactSelection(w, r, principal, sessionID, artifactID)
				return
			}
			if artifactID, hasReveal := strings.CutSuffix(artifactPath, "/reveal"); hasReveal {
				artifactID = strings.TrimSpace(artifactID)
				if artifactID == "" || strings.Contains(artifactID, "/") {
					writeError(w, http.StatusBadRequest, errors.New("artifact id is required"))
					return
				}
				s.handleSessionV3ArtifactReveal(w, r, principal, sessionID, artifactID)
				return
			}
			if artifactID, hasBundle := strings.CutSuffix(artifactPath, "/bundle"); hasBundle {
				artifactID = strings.TrimSpace(artifactID)
				if artifactID == "" || strings.Contains(artifactID, "/") {
					writeError(w, http.StatusBadRequest, errors.New("artifact id is required"))
					return
				}
				s.handleSessionV3ArtifactBundle(w, r, principal, sessionID, artifactID)
				return
			}
			artifactID, contentPath, hasContent := strings.Cut(artifactPath, "/content/")
			artifactID = strings.TrimSpace(artifactID)
			if artifactID == "" || strings.Contains(artifactID, "/") {
				writeError(w, http.StatusBadRequest, errors.New("artifact id is required"))
				return
			}
			if hasContent {
				s.handleSessionV3ArtifactContent(w, r, principal, sessionID, artifactID, contentPath)
				return
			}
			if strings.Contains(artifactPath, "/") {
				writeError(w, http.StatusBadRequest, errors.New("invalid artifact path"))
				return
			}
			s.handleSessionV3Artifact(w, r, principal, sessionID, artifactID)
			return
		}
		if strings.HasPrefix(subpath, "plan-mode/") {
			if locked, found, _ := s.requireSessionV3Access(principal, sessionID); found && s.rejectSystemSidechatMutation(w, locked) {
				return
			}
			s.handleSessionV3PrimaryPlanMode(w, r, principal, sessionID, strings.TrimPrefix(subpath, "plan-mode/"))
			return
		}
		if strings.HasPrefix(subpath, "permissions/") && strings.HasSuffix(subpath, "/resolve") {
			s.handleSessionV3PrimaryPermissionResolve(w, r, principal, sessionID, strings.TrimSuffix(strings.TrimPrefix(subpath, "permissions/"), "/resolve"))
			return
		}
		if strings.HasPrefix(subpath, "plans/") {
			planTail := strings.TrimPrefix(subpath, "plans/")
			if strings.TrimSpace(planTail) == "execution" {
				writeError(w, http.StatusBadRequest, errors.New("unknown sessions v3 path"))
				return
			}
			s.handleSessionV3PrimaryPlanByID(w, r, principal, sessionID, planTail)
			return
		}
		writeError(w, http.StatusBadRequest, errors.New("unknown sessions v3 path"))
	}
}

func sessionsV3SystemSidechatMetadata(parentSessionID, kind string, profile pebblestore.AgentProfile) map[string]any {
	return map[string]any{
		"agent_name":             profile.Name,
		"resolved_agent_name":    profile.Name,
		"agent_mode":             profile.Mode,
		"runtime_mode":           profile.RuntimeMode,
		"exit_plan_mode_enabled": false,
		"agent_profile":          profile,
		"parent_session_id":      strings.TrimSpace(parentSessionID),
		"lineage_kind":           "system_sidechat",
		"presentation_kind":      "system_sidechat",
		"navigation_hidden":      true,
		"transient":              false,
		"system_session":         true,
		"system_sidechat":        true,
		"system_sidechat_kind":   strings.ToLower(strings.TrimSpace(kind)),
		"system_agent_id":        profile.Name,
		"settings_locked":        true,
	}
}

func inheritSessionsV3SystemSidechatWorkspace(parent pebblestore.SessionSnapshot, sidechat *pebblestore.SessionSnapshot, metadata map[string]any) {
	if sidechat == nil {
		return
	}
	sidechat.WorkspacePath = strings.TrimSpace(parent.WorkspacePath)
	sidechat.WorkspaceName = strings.TrimSpace(parent.WorkspaceName)
	sidechat.TemporaryWorkspaceRoots = append([]string(nil), parent.TemporaryWorkspaceRoots...)
	sidechat.WorktreeEnabled = parent.WorktreeEnabled
	sidechat.WorktreeRootPath = strings.TrimSpace(parent.WorktreeRootPath)
	sidechat.WorktreeBaseBranch = strings.TrimSpace(parent.WorktreeBaseBranch)
	sidechat.WorktreeBranch = strings.TrimSpace(parent.WorktreeBranch)
	sidechat.WorkspaceGrants = append([]pebblestore.WorkspaceGrant(nil), parent.WorkspaceGrants...)
	sidechat.WorkspaceUsage = append([]pebblestore.WorkspaceUsageProjection(nil), parent.WorkspaceUsage...)
	for _, key := range []string{
		"workspace_id",
		"swarm_v3_workspace_binding_id",
		"swarm_v3_source_workspace_id",
		"swarm_v3_source_workspace_generation",
		"swarm_v3_source_workspace_name",
		"swarm_v3_source_workspace_path",
		"swarm_v3_runtime_workspace_path",
		"swarm_v3_runtime_swarm_id",
		"swarm_v3_runtime_kind",
		"swarm_v3_authority_host_swarm_id",
		"swarm_v3_placement_generation",
		"swarm_v3_binding_generation",
		"local_workspace_binding_id",
	} {
		if value, ok := parent.Metadata[key]; ok && value != nil {
			metadata[key] = value
		}
	}
}

func sessionsV3SystemSidechatID(parentSessionID, kind string) (string, string) {
	binding := strings.TrimSpace(parentSessionID) + "\x00" + strings.ToLower(strings.TrimSpace(kind))
	sum := sha256.Sum256([]byte(binding))
	return "system-sidechat-" + strings.ToLower(strings.TrimSpace(kind)) + "-" + hex.EncodeToString(sum[:16]), hex.EncodeToString(sum[:])
}

func sessionsV3PlanSidechatPreference(parent pebblestore.SessionSnapshot) pebblestore.ModelPreference {
	preference, _ := sessionsV3PlanSidechatSnapshotPreference(parent)
	return preference
}

func sessionsV3PlanSidechatModelProfile(parent pebblestore.SessionSnapshot) *pebblestore.SessionModelProfileSnapshot {
	if parent.ModelProfile == nil || parent.ModelProfile.Plan == nil {
		return nil
	}
	profile := pebblestore.CloneSessionModelProfileSnapshot(parent.ModelProfile)
	// The Plan sidechat is durably auto mode, so its executor-visible current
	// slot must be the parent's immutable Plan selection. Retaining the parent's
	// Action slot here would make the auto-mode executor silently run the parent
	// Action model even though Session.Preference is bound to Plan.
	profile.Action = *pebblestore.CloneModelProfileSelection(parent.ModelProfile.Plan)
	profile.ActionFavoriteID = parent.ModelProfile.PlanFavoriteID
	profile.ActionFavoriteName = parent.ModelProfile.PlanFavoriteName
	return profile
}

func sessionsV3PlanSidechatSnapshotPreference(parent pebblestore.SessionSnapshot) (pebblestore.ModelPreference, bool) {
	if parent.ModelProfile == nil || parent.ModelProfile.Plan == nil {
		return pebblestore.ModelPreference{}, false
	}
	selection := parent.ModelProfile.Plan
	if strings.TrimSpace(selection.Provider) == "" || strings.TrimSpace(selection.Model) == "" {
		return pebblestore.ModelPreference{}, false
	}
	return pebblestore.ModelPreference{
		Provider:    strings.ToLower(strings.TrimSpace(selection.Provider)),
		Model:       strings.TrimSpace(selection.Model),
		Thinking:    strings.TrimSpace(selection.Thinking),
		ServiceTier: strings.TrimSpace(selection.ServiceTier),
		ContextMode: strings.TrimSpace(selection.ContextMode),
		UpdatedAt:   parent.ModelProfile.AppliedAt,
	}, true
}

func sessionsV3SidechatInt64(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func (s *Server) handleSessionV3SystemSidechat(w http.ResponseWriter, r *http.Request, principal identity.Principal, parentSessionID, kind string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	parent, ok, err := s.requireSessionV3Access(principal, parentSessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		PermissionID string `json:"permission_id"`
		PlanID       string `json:"plan_id"`
		PlanRevision int64  `json:"plan_revision"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	if kind != "plan" && kind != "ai" {
		writeError(w, http.StatusBadRequest, errors.New("sidechat kind must be plan or ai"))
		return
	}
	req.PermissionID, req.PlanID = strings.TrimSpace(req.PermissionID), strings.TrimSpace(req.PlanID)
	if kind == "plan" && (req.PermissionID == "" || req.PlanID == "" || req.PlanRevision <= 0) {
		writeError(w, http.StatusBadRequest, errors.New("permission_id, plan_id, and positive plan_revision are required for Plan"))
		return
	}
	permissions, err := s.perm.ListPermissions(parentSessionID, 1000)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	bound := kind == "ai"
	var planPermission pebblestore.PermissionRecord
	for _, record := range permissions {
		toolName := strings.TrimSpace(record.ToolName)
		status := strings.TrimSpace(record.Status)
		if strings.TrimSpace(record.ID) == req.PermissionID && (status == pebblestore.PermissionStatusPending || status == pebblestore.PermissionStatusApproved) && (toolName == "exit_plan_mode" || toolName == "plan_manage") {
			bound = true
			planPermission = record
			break
		}
	}
	if !bound {
		writeError(w, http.StatusBadRequest, errors.New("permission does not belong to parent session"))
		return
	}
	// The permission projection is authoritative until approval. Never attach
	// client-supplied plan JSON to the reserved agent prompt.
	var planContext map[string]any
	if kind == "plan" {
		if err := json.Unmarshal([]byte(planPermission.ToolArguments), &planContext); err != nil {
			writeError(w, http.StatusConflict, fmt.Errorf("pending plan proposal payload is invalid: %w", err))
			return
		}
		backendPlanID := strings.TrimSpace(firstNonEmpty(sessionsV3MapString(planContext, "plan_id"), sessionsV3MapString(planContext, "id")))
		backendRevision := sessionsV3SidechatInt64(planContext["proposal_revision"])
		if backendPlanID == "" || backendRevision <= 0 || planContext["document"] == nil {
			writeError(w, http.StatusConflict, errors.New("pending plan proposal is missing plan_id, proposal_revision, or document"))
			return
		}
		req.PlanID, req.PlanRevision = backendPlanID, backendRevision
	}
	sidecarID, bindingHash := sessionsV3SystemSidechatID(parentSessionID, kind)
	clientRequestID := "system-sidechat:" + kind + ":" + bindingHash
	parentProfile, err := sessionV3AgentProfileFromMetadata(parent.Metadata)
	if err != nil {
		writeError(w, http.StatusConflict, fmt.Errorf("resolve parent agent snapshot: %w", err))
		return
	}
	profile, err := s.agents.ResolveSystemSidechat(kind, parentProfile)
	if err != nil {
		writeError(w, http.StatusInternalServerError, fmt.Errorf("resolve system sidechat %q: %w", kind, err))
		return
	}
	var planPreference pebblestore.ModelPreference
	if kind == "plan" {
		// Plan sidechats are bound to the parent's immutable session snapshot.
		// Never inherit the mutable current preference or re-resolve account
		// settings, because either can drift after session creation.
		var planEnabled bool
		planPreference, planEnabled = sessionsV3PlanSidechatSnapshotPreference(parent)
		if !planEnabled {
			writeError(w, http.StatusConflict, errors.New("Plan is disabled for the parent session model snapshot"))
			return
		}
		profile.Provider = planPreference.Provider
		profile.Model = planPreference.Model
		profile.Thinking = planPreference.Thinking
		profile.AutoServiceTier = planPreference.ServiceTier
		profile.ContextMode = planPreference.ContextMode
		contextJSON, marshalErr := json.Marshal(planContext)
		if marshalErr != nil {
			writeError(w, http.StatusConflict, fmt.Errorf("encode pending plan context: %w", marshalErr))
			return
		}
		profile.Prompt = agentruntime.PlanSidechatAgentPromptWithContext(string(contextJSON))
	}
	profile = pebblestore.NormalizeAgentProfile(profile)
	metadata := sessionsV3SystemSidechatMetadata(parentSessionID, kind, profile)
	var modelProfile *pebblestore.SessionModelProfileSnapshot
	if kind == "plan" {
		modelProfile = sessionsV3PlanSidechatModelProfile(parent)
		metadata = sessionsV3ModelProfileMetadata(metadata, modelProfile)
		metadata["plan_permission_id"], metadata["plan_id"], metadata["plan_revision"] = req.PermissionID, req.PlanID, req.PlanRevision
		metadata["plan_context_source"] = "permission_projection"
	}
	metadata["originating_agent_name"] = firstNonEmpty(sessionsV3MetadataString(parent.Metadata, "resolved_agent_name"), sessionsV3MetadataString(parent.Metadata, "agent_name"), parentProfile.Name)
	metadata["originating_provider"], metadata["originating_model"] = profile.Provider, profile.Model
	now := time.Now().UnixMilli()
	title := agentruntime.PlanSidechatAgentName
	if kind == "ai" {
		title = agentruntime.AISidechatAgentName
	}
	preference := planPreference
	if kind == "ai" {
		preference = pebblestore.ModelPreference{Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, ServiceTier: profile.AutoServiceTier}
	}
	if existing, exists, getErr := s.sessions.GetSession(sidecarID); getErr != nil {
		writeError(w, http.StatusBadRequest, getErr)
		return
	} else if exists {
		next := existing
		inheritSessionsV3SystemSidechatWorkspace(parent, &next, metadata)
		next.Metadata = metadata
		next.Preference = preference
		next.ModelProfile = pebblestore.CloneSessionModelProfileSnapshot(modelProfile)
		next.Mode = sessionruntime.ModeAuto
		next.UpdatedAt = time.Now().UnixMilli()
		updateKey := fmt.Sprintf("system-sidechat-bind:%s:%s:%d", kind, req.PermissionID, req.PlanRevision)
		updateKind := sessionruntime.SessionMutationUpdateMetadata
		if kind == "plan" {
			updateKind = sessionruntime.SessionMutationUpdateModelProfile
		}
		updateHash, hashErr := sessionsV3UpdatePayloadHash(sidecarID, updateKind, map[string]any{"metadata": metadata, "preference": preference, "model_profile": modelProfile})
		if hashErr != nil {
			writeError(w, http.StatusBadRequest, hashErr)
			return
		}
		updateEventPayload, marshalErr := json.Marshal(map[string]any{"session_id": sidecarID, "metadata": metadata, "preference": preference, "model_profile": modelProfile, "updated_at": next.UpdatedAt})
		if marshalErr != nil {
			writeError(w, http.StatusBadRequest, marshalErr)
			return
		}
		updateResult, updateErr := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sidecarID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: updateKey, IdempotencyKey: updateKey, PayloadHash: updateHash, RequestHash: updateHash, Kind: updateKind, EventPayload: updateEventPayload, Session: &next, NowUnixMs: next.UpdatedAt})
		if updateErr != nil {
			writeError(w, http.StatusBadRequest, updateErr)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": kind, "session_id": sidecarID, "parent_session_id": parentSessionID, "permission_id": req.PermissionID, "plan_id": req.PlanID, "plan_revision": req.PlanRevision, "provider": profile.Provider, "model": profile.Model, "runtime_swarm_id": sessionsV3MetadataString(parent.Metadata, "swarm_v3_runtime_swarm_id"), "replayed": updateResult.Replayed})
		return
	}
	sidecar := pebblestore.SessionSnapshot{ID: sidecarID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, Title: title, Mode: sessionruntime.ModeAuto, Preference: preference, ModelProfile: pebblestore.CloneSessionModelProfileSnapshot(modelProfile), Metadata: metadata, CreatedAt: now, UpdatedAt: now}
	inheritSessionsV3SystemSidechatWorkspace(parent, &sidecar, metadata)
	payload, _ := json.Marshal(struct {
		Parent, Permission, Plan string
		Revision                 int64
	}{parentSessionID, req.PermissionID, req.PlanID, req.PlanRevision})
	payloadSum := sha256.Sum256(payload)
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sidecarID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: hex.EncodeToString(payloadSum[:]), RequestHash: hex.EncodeToString(payloadSum[:]), Kind: sessionruntime.SessionMutationCreateSession, Session: &sidecar, NowUnixMs: now})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "kind": kind, "session_id": sidecarID, "parent_session_id": parentSessionID, "permission_id": req.PermissionID, "plan_id": req.PlanID, "plan_revision": req.PlanRevision, "originating_agent_name": metadata["originating_agent_name"], "provider": profile.Provider, "model": profile.Model, "runtime_swarm_id": sessionsV3MetadataString(parent.Metadata, "swarm_v3_runtime_swarm_id"), "replayed": result.Replayed})
}

func (s *Server) handleSessionsV3PrimaryCreate(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	var req sessionsV3CreateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	if sessionID == "" {
		sessionID = stableSessionsV3PrimarySessionID(principal, clientRequestID)
	}
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	// managed_worktree_requested is a tolerated rolling-client field only. The
	// canonical request uses worktree_mode and never echoes the retired toggle.
	requestedWorktreeMode, err := validateSessionsV3CreateWorktreeRequest(req.WorktreeMode, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName, req.WorktreeExistingPath)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	binding, err := s.resolveSessionsV3PrimaryBinding(principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if strings.TrimSpace(req.Mode) == "" {
		req.Mode = pebblestore.AgentProfileDefaultSessionMode(resolvedAgent.Profile)
	}
	workspacePath := binding.SourceWorkspacePath
	workspaceName := binding.SourceWorkspaceName
	if workspaceName == "" {
		workspaceName = filepath.Base(workspacePath)
		if workspaceName == "." || workspaceName == string(filepath.Separator) {
			workspaceName = "workspace"
		}
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = "New Session"
	}
	now := time.Now().UnixMilli()
	modelProfileSnapshot, err := s.resolveSessionsV3ModelProfileChoice(identity.ContextWithPrincipal(r.Context(), principal), req.ModelProfile, now)
	if err == nil && req.ModelProfile == nil && strings.EqualFold(strings.TrimSpace(resolvedAgent.Profile.Name), agentruntime.SwarmAgentID) {
		modelProfileSnapshot, err = s.sessionModelProfileSnapshotFromAccountDefault(identity.ContextWithPrincipal(r.Context(), principal), now)
	}
	if err != nil {
		writeModelProfileError(w, err)
		return
	}
	initialWorkspaceGrants, err := s.sessionsV3InitialWorkspaceGrants(principal, binding)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	session := pebblestore.SessionSnapshot{
		ID:              sessionID,
		UserID:          strings.TrimSpace(principal.UserID),
		AccountScopeID:  strings.TrimSpace(principal.AccountScopeID),
		WorkspacePath:   workspacePath,
		WorkspaceName:   workspaceName,
		WorkspaceGrants: initialWorkspaceGrants,
		WorkspaceUsage:  pebblestore.WorkspaceUsageFromGrants(initialWorkspaceGrants),
		Title:           title,
		Mode:            sessionruntime.NormalizeMode(req.Mode),
		Preference:      normalizeSessionsV3ModelPreference(req.Preference),
		ModelProfile:    modelProfileSnapshot,
		Metadata:        sessionsV3ModelProfileMetadata(sessionsV3CreateServerMetadata(req.Metadata, resolvedAgent, binding), modelProfileSnapshot),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if profilePreference, ok := sessionsV3ProfilePreference(session); ok {
		session.Preference = normalizeSessionsV3ModelPreference(profilePreference)
	}
	payloadHash, err := sessionsV3CreatePayloadHash(sessionID, req, workspacePath, workspaceName, title, session.Metadata)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if s.handleSessionsV3CreateReplay(w, principal, sessionID, clientRequestID, payloadHash, session) {
		return
	}
	if requestedWorktreeMode == runruntime.RunWorktreeModeOn {
		allocation, err := s.resolveSessionsV3CreateWorktree(principal, workspacePath, sessionID, req.WorktreeUseCurrentBranch, req.WorktreeBaseBranch, req.WorktreeBranchName, req.WorktreeExistingPath)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		session.WorktreeEnabled = true
		session.WorktreeRootPath = strings.TrimSpace(allocation.WorkspacePath)
		session.WorktreeBaseBranch = strings.TrimSpace(allocation.BaseBranch)
		session.WorktreeBranch = strings.TrimSpace(allocation.BranchName)
		if session.Metadata == nil {
			session.Metadata = make(map[string]any, 4)
		}
		session.Metadata["swarm_v3_source_workspace_path"] = binding.SourceWorkspacePath
		session.Metadata["swarm_v3_runtime_workspace_path"] = strings.TrimSpace(allocation.WorkspacePath)
		available := true
		session.WorkspaceGrants = append(session.WorkspaceGrants, pebblestore.WorkspaceGrant{
			Kind: pebblestore.WorkspaceGrantWorktree, Path: strings.TrimSpace(allocation.WorkspacePath), Available: &available,
		})
		session.WorkspaceUsage = pebblestore.WorkspaceUsageFromGrants(session.WorkspaceGrants)
	} else {
		session.WorktreeBranch = sessionruntime.DetectCurrentBranch(session.WorkspacePath)
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       now,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict, "result": result})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	response, err := sessionV3CreateResultResponse(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionsV3PrimaryList(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	limit, ok := parseRequestPositiveLimit(w, r, 100)
	if !ok {
		return
	}
	sessions, err := s.sessions.ListSessionsForAccountUser(principal.AccountScopeID, principal.UserID, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	items := make([]map[string]any, 0, len(sessions))
	for _, item := range sessions {
		if sessionsV3SystemSidechat(item) {
			continue
		}
		projection, projectionOK, err := s.sessions.GetSessionProjection(item.ID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !projectionOK {
			continue
		}
		items = append(items, map[string]any{"session": item, "projection": projection})
		if len(items) >= limit {
			break
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "sessions": items})
}

func (s *Server) handleSessionsV3Delete(w http.ResponseWriter, r *http.Request) {
	principal, ok := s.sessionsV3SearchPrincipal(w, r)
	if !ok {
		return
	}
	var req sessionsV3DeleteRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if (len(req.SessionIDs) > 0) == (req.UpdatedBefore != nil) {
		writeError(w, http.StatusBadRequest, errors.New("provide exactly one of session_ids or updated_before"))
		return
	}
	workspaceReq := sessionsV3SearchRequest{Global: req.Global, Workspace: req.Workspace, WorkspacePath: req.WorkspacePath, WorkspacePaths: req.WorkspacePaths, ArchivedMode: req.ArchivedMode}
	searchOptions, err := sessionsV3SearchOptionsFromRequest(principal, workspaceReq)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	all, err := s.sessions.ListSessionsForAccount(principal.AccountScopeID, 100000)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	byID := make(map[string]pebblestore.SessionSnapshot, len(all))
	archivedByID := make(map[string]bool)
	workspaceAllowed := func(path string) bool {
		if searchOptions.Global {
			return true
		}
		for _, candidate := range searchOptions.WorkspacePaths {
			if candidate == path {
				return true
			}
		}
		return false
	}
	if searchOptions.ArchivedMode != "only" {
		for _, session := range all {
			if session.UserID == principal.UserID && workspaceAllowed(session.WorkspacePath) {
				byID[session.ID] = session
			}
		}
	}
	if searchOptions.ArchivedMode != "exclude" {
		tombstones, tombstoneErr := s.sessions.ListSessionTombstonesForAccount(principal.AccountScopeID, 100000)
		if tombstoneErr != nil {
			writeError(w, http.StatusInternalServerError, tombstoneErr)
			return
		}
		for _, tombstone := range tombstones {
			if tombstone.Archived && !tombstone.Deleted && tombstone.UserID == principal.UserID && workspaceAllowed(tombstone.Session.WorkspacePath) {
				byID[tombstone.Session.ID] = tombstone.Session
				archivedByID[tombstone.Session.ID] = true
			}
		}
	}
	lineageInput := make(map[string]pebblestore.SessionSnapshot, len(byID))
	for id, session := range byID {
		lineageInput[id] = session
	}
	lineage := pebblestore.ResolveV3SessionLineage(lineageInput)
	rootUpdated := make(map[string]int64)
	for id, metric := range lineage {
		if byID[id].UpdatedAt > rootUpdated[metric.RootSessionID] {
			rootUpdated[metric.RootSessionID] = byID[id].UpdatedAt
		}
	}
	selectedRoots := map[string]struct{}{}
	if len(req.SessionIDs) > 0 {
		for _, raw := range req.SessionIDs {
			id := strings.TrimSpace(raw)
			metric, exists := lineage[id]
			if !exists {
				writeSessionNotFound(w)
				return
			}
			selectedRoots[metric.RootSessionID] = struct{}{}
		}
	} else {
		for id, metric := range lineage {
			if id == metric.RootSessionID && rootUpdated[id] < *req.UpdatedBefore {
				selectedRoots[id] = struct{}{}
			}
		}
	}
	candidates := make([]pebblestore.SessionSnapshot, 0)
	preview := sessionsV3DeletePreview{ConversationCount: len(selectedRoots)}
	for id, metric := range lineage {
		if _, ok := selectedRoots[metric.RootSessionID]; !ok {
			continue
		}
		session := byID[id]
		candidates = append(candidates, session)
		preview.SessionIDs = append(preview.SessionIDs, id)
		if metric.ParentSessionID != "" && metric.RootSessionID != metric.SessionID {
			preview.ChildCount++
		}
		if storedMetric, found, metricErr := s.sessions.GetSessionLibraryMetric(id); metricErr != nil {
			writeError(w, http.StatusInternalServerError, metricErr)
			return
		} else if found {
			preview.LogicalBytes += storedMetric.LogicalBytes
		}
		if _, active, activeErr := s.sessions.GetSessionActiveRunIntent(id); activeErr != nil {
			writeError(w, http.StatusInternalServerError, activeErr)
			return
		} else if active {
			preview.ActiveRunCount++
		}
		if s.perm != nil {
			if count, countErr := s.perm.PendingCount(id); countErr != nil {
				writeError(w, http.StatusInternalServerError, countErr)
				return
			} else {
				preview.PendingCount += count
			}
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		if rootUpdated[lineage[candidates[i].ID].RootSessionID] == rootUpdated[lineage[candidates[j].ID].RootSessionID] {
			return candidates[i].ID < candidates[j].ID
		}
		return rootUpdated[lineage[candidates[i].ID].RootSessionID] > rootUpdated[lineage[candidates[j].ID].RootSessionID]
	})
	preview.SessionCount = len(candidates)
	newestRoots := make(map[string]struct{}, 75)
	roots := make([]string, 0)
	for id, metric := range lineage {
		if metric.RootSessionID == id {
			roots = append(roots, id)
		}
	}
	sort.Slice(roots, func(i, j int) bool {
		if rootUpdated[roots[i]] == rootUpdated[roots[j]] {
			return roots[i] < roots[j]
		}
		return rootUpdated[roots[i]] > rootUpdated[roots[j]]
	})
	for i, root := range roots {
		if i >= 75 {
			break
		}
		newestRoots[root] = struct{}{}
	}
	for root := range selectedRoots {
		if _, ok := newestRoots[root]; ok {
			preview.Recent75Overlap++
		}
	}
	sort.Strings(preview.SessionIDs)
	fingerprintParts := make([]string, 0, len(preview.SessionIDs))
	for _, id := range preview.SessionIDs {
		fingerprintParts = append(fingerprintParts, fmt.Sprintf("%s:%d:%t", id, byID[id].UpdatedAt, archivedByID[id]))
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%s|%d|%s", principal.AccountScopeID, time.Now().Unix()/300, strings.Join(fingerprintParts, ","))))
	preview.ConfirmationToken = hex.EncodeToString(hash[:])
	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "dry_run": true, "preview": preview})
		return
	}
	if req.ConfirmationToken != preview.ConfirmationToken {
		writeError(w, http.StatusConflict, errors.New("deletion candidate set changed; preview again"))
		return
	}
	if preview.ActiveRunCount > 0 || preview.PendingCount > 0 {
		writeError(w, http.StatusConflict, errors.New("deletion blocked by active runs or pending approvals"))
		return
	}
	if preview.Recent75Overlap > 0 && !req.ConfirmRecent {
		writeError(w, http.StatusConflict, errors.New("deletion overlaps newest 75 conversations; explicit confirmation required"))
		return
	}
	expectedUpdatedAt := make(map[string]int64, len(preview.SessionIDs))
	for _, id := range preview.SessionIDs {
		expectedUpdatedAt[id] = byID[id].UpdatedAt
	}
	// Recheck protection immediately before entering the session service's
	// version-checked mutation lock. The version check closes the preview-to-
	// delete race for session activity; live run/approval state remains a hard
	// deletion guard.
	for _, id := range preview.SessionIDs {
		if _, active, activeErr := s.sessions.GetSessionActiveRunIntent(id); activeErr != nil {
			writeError(w, http.StatusInternalServerError, activeErr)
			return
		} else if active {
			writeError(w, http.StatusConflict, errors.New("deletion blocked by active run"))
			return
		}
		if s.perm != nil {
			if pending, pendingErr := s.perm.PendingCount(id); pendingErr != nil {
				writeError(w, http.StatusInternalServerError, pendingErr)
				return
			} else if pending > 0 {
				writeError(w, http.StatusConflict, errors.New("deletion blocked by pending approval"))
				return
			}
		}
	}
	events, err := s.sessions.DeleteSessionsWithEventsIfUnchanged(preview.SessionIDs, expectedUpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "changed after deletion preview") {
			writeError(w, http.StatusConflict, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.publishSessionsV3DeleteRealtime(candidates, events)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": true, "preview": preview})
}

func (s *Server) publishSessionsV3DeleteRealtime(sessions []pebblestore.SessionSnapshot, events []*pebblestore.EventEnvelope) {
	if head, err := s.sessions.CurrentRealtimeOutboxRevision(); err == nil {
		for _, session := range sessions {
			if record, ok, recordErr := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(session.ID, head); recordErr == nil && ok && record.Event.EventType == "session.deleted" {
				_ = s.publishCommittedV3RealtimeOutbox(record)
			}
		}
	}
	if s.hub != nil {
		for _, event := range events {
			if event != nil {
				s.hub.Publish(*event)
			}
		}
	}
}

func (s *Server) handleSessionV3PrimaryDelete(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	s.handleSessionV3PrimaryTombstone(w, r, principal, sessionID, "deleted")
}

func (s *Server) handleSessionV3PrimaryArchive(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	s.handleSessionV3PrimaryTombstone(w, r, principal, sessionID, "archived")
}

func (s *Server) handleSessionsV3PrimaryArchiveBatch(w http.ResponseWriter, r *http.Request, principal identity.Principal) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3ArchiveBatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if len(req.SessionIDs) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("session_ids is required"))
		return
	}
	sessions := make([]pebblestore.SessionSnapshot, 0, len(req.SessionIDs))
	seen := make(map[string]struct{}, len(req.SessionIDs))
	for _, rawID := range req.SessionIDs {
		sessionID := strings.TrimSpace(rawID)
		if sessionID == "" {
			writeError(w, http.StatusBadRequest, errors.New("session_ids must not contain empty ids"))
			return
		}
		if _, exists := seen[sessionID]; exists {
			continue
		}
		seen[sessionID] = struct{}{}
		session, found, err := s.requireSessionV3Access(principal, sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !found {
			writeSessionNotFound(w)
			return
		}
		sessions = append(sessions, session)
	}
	if len(sessions) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("session_ids must include at least one session id"))
		return
	}
	events, err := s.sessions.ArchiveSessionsWithEvents(sessionIDsFromSnapshots(sessions))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	s.publishSessionsV3ArchiveRealtime(sessions, events)
	results := make([]map[string]any, 0, len(sessions))
	for _, session := range sessions {
		results = append(results, sessionV3ArchiveResponse(session))
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "archived": true, "results": results})
}

func (s *Server) handleSessionV3PrimaryTombstone(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, kind string) {
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	kind = strings.TrimSpace(strings.ToLower(kind))
	if kind != "archived" && s.rejectSystemSidechatMutation(w, session) {
		return
	}
	if kind == "deleted" {
		if _, active, activeErr := s.sessions.GetSessionActiveRunIntent(session.ID); activeErr != nil {
			writeError(w, http.StatusInternalServerError, activeErr)
			return
		} else if active {
			writeError(w, http.StatusConflict, errors.New("deletion blocked by active run"))
			return
		}
		if s.perm != nil {
			if pending, pendingErr := s.perm.PendingCount(session.ID); pendingErr != nil {
				writeError(w, http.StatusInternalServerError, pendingErr)
				return
			} else if pending > 0 {
				writeError(w, http.StatusConflict, errors.New("deletion blocked by pending approval"))
				return
			}
		}
	}
	eventType := "session.deleted"
	if kind == "archived" {
		eventType = "session.archived"
	}
	var event *pebblestore.EventEnvelope
	if kind == "archived" {
		event, err = s.sessions.ArchiveSessionWithEvent(session.ID)
	} else {
		event, err = s.sessions.DeleteSessionWithEvent(session.ID)
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if kind == "archived" {
		s.publishSessionsV3ArchiveRealtime([]pebblestore.SessionSnapshot{session}, []*pebblestore.EventEnvelope{event})
		writeJSON(w, http.StatusOK, sessionV3ArchiveResponse(session))
		return
	}
	if head, headErr := s.sessions.CurrentRealtimeOutboxRevision(); headErr == nil && head > 0 {
		if record, ok, recordErr := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(session.ID, head); recordErr == nil && ok && record.Event.EventType == eventType {
			if publishErr := s.publishCommittedV3RealtimeOutbox(record); publishErr != nil {
				// Durable commit succeeded; realtime wake is an accelerator only.
				_ = publishErr
			}
		}
	}
	if event != nil && s.hub != nil {
		s.hub.Publish(*event)
	}
	response := map[string]any{
		"ok":         true,
		"session_id": session.ID,
		"deleted":    true,
		"tombstone": map[string]any{
			"session_id":     session.ID,
			"workspace_path": session.WorkspacePath,
			"kind":           "deleted",
			"deleted":        true,
		},
	}
	writeJSON(w, http.StatusOK, response)
}

func sessionIDsFromSnapshots(sessions []pebblestore.SessionSnapshot) []string {
	ids := make([]string, 0, len(sessions))
	for _, session := range sessions {
		ids = append(ids, session.ID)
	}
	return ids
}

func sessionV3ArchiveResponse(session pebblestore.SessionSnapshot) map[string]any {
	return map[string]any{
		"ok":         true,
		"session_id": session.ID,
		"archived":   true,
		"tombstone": map[string]any{
			"session_id":     session.ID,
			"workspace_path": session.WorkspacePath,
			"kind":           "archived",
			"archived":       true,
		},
	}
}

func (s *Server) publishSessionsV3ArchiveRealtime(sessions []pebblestore.SessionSnapshot, events []*pebblestore.EventEnvelope) {
	if head, headErr := s.sessions.CurrentRealtimeOutboxRevision(); headErr == nil && head > 0 {
		for _, session := range sessions {
			if record, ok, recordErr := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(session.ID, head); recordErr == nil && ok && record.Event.EventType == "session.archived" {
				if publishErr := s.publishCommittedV3RealtimeOutbox(record); publishErr != nil {
					// Durable commit succeeded; realtime wake is an accelerator only.
					_ = publishErr
				}
			}
		}
	}
	if s.hub == nil {
		return
	}
	for _, event := range events {
		if event != nil {
			s.hub.Publish(*event)
		}
	}
}

func (s *Server) handleSessionV3PrimaryMessages(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		query, ok := parseSessionsV3MessagesPageQuery(w, r)
		if !ok {
			return
		}
		if found, err := s.authorizeReadableSessionV3Access(principal, sessionID); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		} else if !found {
			writeSessionNotFound(w)
			return
		}
		var messages []pebblestore.MessageSnapshot
		var err error
		fetchLimit := query.Limit + 1
		if query.Tail {
			messages, err = s.sessions.ListSessionMessageTail(sessionID, fetchLimit)
		} else if query.HasBeforeSeq {
			messages, err = s.sessions.ListSessionMessagesBefore(sessionID, query.BeforeSeq, fetchLimit)
		} else {
			messages, err = s.sessions.ListSessionMessages(sessionID, query.AfterSeq, fetchLimit)
		}
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, sessionsV3MessagesPageResponse(sessionID, messages, query))
		return
	}

	var req sessionsV3MessageRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	req.ClientRequestID = clientRequestID
	req.IdempotencyKey = clientRequestID
	result, enqueueJob, err := s.acceptSessionsV3Message(principal, sessionID, req)
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error_code": "idempotency_conflict", "error": err.Error()})
			return
		}
		if strings.Contains(err.Error(), "not found") {
			writeSessionNotFound(w)
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	var currentRunState *pebblestore.V3SessionRunState
	if state, ok, err := s.sessions.GetSessionRunState(sessionID); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	} else if ok {
		currentRunState = &state
	}
	writeJSON(w, http.StatusOK, sessionV3MessageMutationResponse(sessionID, result, currentRunState))
	if enqueueJob != nil {
		s.v3SessionExecutor.EnqueueRun(*enqueueJob)
	}
}

func cloneSessionsV3PlanDocument(doc *pebblestore.SessionPlanDocument) (*pebblestore.SessionPlanDocument, error) {
	if doc == nil {
		return nil, nil
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	var clone pebblestore.SessionPlanDocument
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (s *Server) acceptSessionsV3Message(principal identity.Principal, sessionID string, req sessionsV3MessageRequest) (sessionruntime.SessionMutationResult, *sessionV3ExecutorJob, error) {
	session, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	if !found {
		session, found, err = s.requireArchivedSessionV3Access(principal, sessionID)
		if err != nil {
			return sessionruntime.SessionMutationResult{}, nil, err
		}
	}
	if !found {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("session not found")
	}
	if err := validateSessionsV3CreateMetadata(req.Metadata); err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	message := pebblestore.MessageSnapshot{
		ID: strings.TrimSpace(req.MessageID), Role: strings.TrimSpace(req.Role), Content: req.Content,
		Metadata: cloneSessionsV3Metadata(req.Metadata), Media: append([]pebblestore.SessionMediaReference(nil), req.Media...),
		VideoAttachments:   append([]pebblestore.SessionVideoAttachmentReference(nil), req.VideoAttachments...),
		ArtifactSelections: append([]pebblestore.SessionArtifactSelectionReference(nil), req.ArtifactSelections...),
	}
	if message.Role == "" {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("message role is required")
	}
	if len(message.ArtifactSelections) > 0 && !strings.EqualFold(message.Role, "user") {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("artifact selections are allowed only on user messages")
	}
	if message.Content == "" && len(message.Media) == 0 && len(message.VideoAttachments) == 0 && len(message.ArtifactSelections) == 0 {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("message content, media, video attachment, or artifact selection is required")
	}
	message.ArtifactSelections, err = s.sessions.ValidateSessionArtifactMessageSelections(principal.AccountScopeID, principal.UserID, message.ArtifactSelections)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	if err := s.validateSessionsV3MessageMedia(principal, session, message.Media); err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	message.VideoAttachments, err = s.sessions.Store().ValidateSessionVideoAttachments(principal.AccountScopeID, session, message.VideoAttachments)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	now := time.Now().UnixMilli()
	runStatus, blockedReason := s.sessionsV3PrimaryRunIntentStatus(principal, session, req)
	runIntent := &pebblestore.V3SessionRunIntent{RunID: strings.TrimSpace(req.RunID), SourceMessageID: strings.TrimSpace(message.ID), Status: runStatus, BlockedReason: blockedReason}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey))
	if clientRequestID == "" {
		return sessionruntime.SessionMutationResult{}, nil, errors.New("client_request_id is required")
	}
	if runIntent.RunID == "" {
		runIntent.RunID = stableSessionsV3PrimaryRunID(sessionID, clientRequestID)
	}
	payloadHash, err := sessionsV3MessagePayloadHash(sessionID, req, message, runIntent.Status, runIntent.BlockedReason)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, nil, err
	}
	var reactivatedPlanSave *sessionruntime.PreparedPlanSave
	if plan, ok, planErr := s.sessions.GetActivePlan(sessionID); planErr != nil {
		return sessionruntime.SessionMutationResult{}, nil, planErr
	} else if ok && plan.Document != nil && plan.Document.ExecutionState != nil && strings.EqualFold(strings.TrimSpace(plan.Document.ExecutionState.Status), sessionruntime.PlanExecutionStateInProgress) && strings.TrimSpace(plan.Document.ExecutionState.CurrentRunID) != "" && strings.TrimSpace(plan.Document.ExecutionState.CurrentRunID) != runIntent.RunID {
		priorRun, priorRunOK, priorRunErr := s.sessions.GetV3SessionRunIntent(sessionID, strings.TrimSpace(plan.Document.ExecutionState.CurrentRunID))
		if priorRunErr != nil {
			return sessionruntime.SessionMutationResult{}, nil, priorRunErr
		}
		if priorRunOK && priorRun.Status != sessionruntime.RunIntentPendingExecutor && priorRun.Status != sessionruntime.RunIntentRunning {
			doc, cloneErr := cloneSessionsV3PlanDocument(plan.Document)
			if cloneErr != nil {
				return sessionruntime.SessionMutationResult{}, nil, cloneErr
			}
			if _, changed, rebindErr := sessionruntime.RebindInProgressPlanForUserMessage(doc, runIntent.RunID, sessionID, sessionID, now); rebindErr != nil {
				return sessionruntime.SessionMutationResult{}, nil, rebindErr
			} else if changed {
				prepared, prepareErr := s.sessions.PreparePlanSaveWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, sessionruntime.PlanSaveMetadata{UpdateSummary: "Transferred in-progress checkpoint to user message run", UpdateScope: strings.TrimSpace(doc.ActiveCheckpointID), UpdateKind: "rebind_in_progress_user_message", RevisionKind: sessionruntime.PlanRevisionKindExecution, Checkpoint: true, Document: doc})
				if prepareErr != nil {
					return sessionruntime.SessionMutationResult{}, nil, prepareErr
				}
				reactivatedPlanSave = &prepared
				runIntent.PlanID = strings.TrimSpace(plan.ID)
				runIntent.CheckpointID = strings.TrimSpace(doc.ActiveCheckpointID)
				runIntent.AttemptID = strings.TrimSpace(doc.ExecutionState.ActiveAttemptID)
				runIntent.RunSessionID = sessionID
				runIntent.ParentSessionID = sessionID
				runIntent.ResumeContext = true
				markSessionsV3CheckpointResumeRouting(&message, runIntent.RunID, doc.ActiveCheckpointID, "rebind_in_progress_user_message")
			}
		}
	} else if ok && plan.Document != nil && plan.Document.ExecutionState != nil && strings.EqualFold(strings.TrimSpace(plan.Document.ExecutionState.Status), sessionruntime.PlanExecutionStatePaused) {
		doc, cloneErr := cloneSessionsV3PlanDocument(plan.Document)
		if cloneErr != nil {
			return sessionruntime.SessionMutationResult{}, nil, cloneErr
		}
		if _, changed, reactivateErr := sessionruntime.ReactivatePausedPlanForUserMessage(doc, runIntent.RunID, sessionID, sessionID, now); reactivateErr != nil {
			return sessionruntime.SessionMutationResult{}, nil, reactivateErr
		} else if changed {
			prepared, prepareErr := s.sessions.PreparePlanSaveWithMetadata(sessionID, plan.ID, plan.Title, plan.Plan, plan.Status, plan.ApprovalState, true, sessionruntime.PlanSaveMetadata{UpdateSummary: "Reactivated paused checkpoint for user message", UpdateScope: strings.TrimSpace(doc.ActiveCheckpointID), UpdateKind: "resume_paused_user_message", RevisionKind: sessionruntime.PlanRevisionKindExecution, Checkpoint: true, Document: doc})
			if prepareErr != nil {
				return sessionruntime.SessionMutationResult{}, nil, prepareErr
			}
			reactivatedPlanSave = &prepared
			runIntent.PlanID = strings.TrimSpace(plan.ID)
			runIntent.CheckpointID = strings.TrimSpace(doc.ActiveCheckpointID)
			runIntent.AttemptID = strings.TrimSpace(doc.ExecutionState.ActiveAttemptID)
			runIntent.RunSessionID = sessionID
			runIntent.ParentSessionID = sessionID
			runIntent.ResumeContext = true
			markSessionsV3CheckpointResumeRouting(&message, runIntent.RunID, doc.ActiveCheckpointID, "resume_paused_user_message")
		}
	} else if ok && plan.Document != nil && plan.Document.ExecutionState != nil && strings.EqualFold(strings.TrimSpace(plan.Document.ExecutionState.Status), sessionruntime.PlanExecutionStateWaitingReview) {
		// Final-handoff persistence already sealed the checkpoint epoch and opened
		// its successor. This ordinary parent turn therefore stays unowned by the
		// completed checkpoint and is appended to that active successor epoch.
		runIntent.PlanID = ""
		runIntent.CheckpointID = ""
		runIntent.AttemptID = ""
		runIntent.RunSessionID = sessionID
		runIntent.ParentSessionID = ""
		runIntent.ResumeContext = false
	}
	var result sessionruntime.SessionMutationResult
	mutation := sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationAppendMessage,
		Message:         &message,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	}
	if reactivatedPlanSave != nil {
		prepared := *reactivatedPlanSave
		mutation.PlanSave = &pebblestore.V3PlanSaveMutation{Plan: prepared.Plan, ArchivedRevision: prepared.ArchivedRevision, Activate: prepared.Activate, ExpectedParentVersion: prepared.Plan.ParentRevision}
	}
	result, err = s.applySessionV3PrimaryMutation(mutation)
	if err != nil {
		return result, nil, err
	}
	var enqueueJob *sessionV3ExecutorJob
	if !result.Replayed && result.RunIntent != nil && result.RunIntent.Status == sessionruntime.RunIntentPendingExecutor && s.v3SessionExecutor != nil {
		sourceMessageID := ""
		if result.Message != nil {
			sourceMessageID = strings.TrimSpace(result.Message.ID)
		}
		enqueueJob = &sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: result.RunIntent.RunID, EpochID: result.RunIntent.EpochID, SourceMessageID: sourceMessageID}
		if checkpointJob, ok, err := s.sessionsV3ActiveCheckpointMessageRunJob(principal, sessionID, result.RunIntent.RunID, result.RunIntent.EpochID); err != nil {
			return result, nil, err
		} else if ok {
			enqueueJob = &checkpointJob
		}
	}
	return result, enqueueJob, nil
}

func (s *Server) sessionsV3ActiveCheckpointMessageRunJob(principal identity.Principal, sessionID, runID, epochID string) (sessionV3ExecutorJob, bool, error) {
	job := sessionV3ExecutorJob{Principal: principal, SessionID: strings.TrimSpace(sessionID), RunID: strings.TrimSpace(runID), EpochID: strings.TrimSpace(epochID)}
	if s == nil || s.sessions == nil || job.SessionID == "" || job.RunID == "" {
		return job, false, nil
	}
	plan, ok, err := s.sessions.GetActivePlan(job.SessionID)
	if err != nil {
		return job, false, err
	}
	if !ok || strings.TrimSpace(plan.ID) == "" || plan.Document == nil {
		return job, false, nil
	}
	if !strings.EqualFold(strings.TrimSpace(plan.Status), "approved") || !strings.EqualFold(strings.TrimSpace(plan.ApprovalState), "approved") {
		return job, false, nil
	}
	doc := plan.Document
	if !strings.EqualFold(strings.TrimSpace(doc.ExecutionPolicy.Shape), sessionruntime.PlanExecutionShapeCheckpointed) {
		return job, false, nil
	}
	if doc.ExecutionState == nil {
		return job, false, nil
	}
	stateStatus := strings.TrimSpace(doc.ExecutionState.Status)
	if stateStatus != "" && stateStatus != sessionruntime.PlanExecutionStateIdle && stateStatus != sessionruntime.PlanExecutionStateInProgress {
		return job, false, nil
	}
	checkpointID := strings.TrimSpace(doc.ActiveCheckpointID)
	if checkpointID == "" {
		return job, false, nil
	}
	checkpoint, ok := sessionV3PlanDocumentCheckpointByID(doc, checkpointID)
	if !ok {
		return job, false, nil
	}
	switch strings.TrimSpace(checkpoint.Status) {
	case sessionruntime.PlanCheckpointStatusPending, sessionruntime.PlanCheckpointStatusInProgress:
	default:
		return job, false, nil
	}
	if checkpoint.RunID != "" && checkpoint.RunID != job.RunID {
		return job, false, nil
	}
	if checkpoint.SessionID != "" && checkpoint.SessionID != job.SessionID {
		return job, false, nil
	}
	if activeRun, ok, err := s.sessions.GetSessionActiveRunIntent(job.SessionID); err != nil {
		return job, false, err
	} else if ok {
		if strings.TrimSpace(activeRun.RunID) != "" && strings.TrimSpace(activeRun.RunID) != job.RunID {
			switch strings.TrimSpace(activeRun.Status) {
			case sessionruntime.RunIntentPendingExecutor, sessionruntime.RunIntentRunning:
				return job, false, nil
			}
		} else {
			job.ResumeContext = activeRun.ResumeContext
		}
	}
	job.PlanID = strings.TrimSpace(plan.ID)
	job.CheckpointID = checkpointID
	job.AttemptID = strings.TrimSpace(checkpoint.AttemptID)
	job.ParentSessionID = job.SessionID
	if doc.ExecutionState != nil && strings.TrimSpace(doc.ExecutionState.ParentSessionID) != "" {
		job.ParentSessionID = strings.TrimSpace(doc.ExecutionState.ParentSessionID)
	}
	return job, true, nil
}

func sessionV3PlanDocumentCheckpointByID(doc *pebblestore.SessionPlanDocument, checkpointID string) (pebblestore.SessionPlanCheckpoint, bool) {
	if doc == nil {
		return pebblestore.SessionPlanCheckpoint{}, false
	}
	checkpointID = strings.TrimSpace(checkpointID)
	if checkpointID == "" {
		return pebblestore.SessionPlanCheckpoint{}, false
	}
	for _, checkpoint := range doc.Checkpoints {
		if strings.TrimSpace(checkpoint.ID) == checkpointID {
			return checkpoint, true
		}
	}
	return pebblestore.SessionPlanCheckpoint{}, false
}

func (s *Server) handleSessionV3PrimaryEvents(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	afterSeq, limit, ok := parseAfterSeqAndLimit(w, r, 500)
	if !ok {
		return
	}
	if found, err := s.authorizeReadableSessionV3Access(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	replay, err := s.sessions.ReplaySessionEvents(sessionID, afterSeq, limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":                 true,
		"session_id":         sessionID,
		"events":             replay.Events,
		"projection":         replay.Projection,
		"lifecycle":          replay.Lifecycle,
		"messages":           replay.Messages,
		"run_intents":        replay.RunIntents,
		"high_watermark_seq": replay.HighWatermarkSeq,
		"next_seq":           replay.NextSeq,
		"applied_seq":        replay.NextSeq,
		"high_watermark":     replay.Projection.ProjectionHighWatermarkSeq,
	})
}

func sessionsV3SystemSidechat(session pebblestore.SessionSnapshot) bool {
	hidden, _ := session.Metadata["system_sidechat"].(bool)
	return hidden || strings.EqualFold(sessionsV3MetadataString(session.Metadata, "lineage_kind"), "system_sidechat")
}

func (s *Server) rejectSystemSidechatMutation(w http.ResponseWriter, session pebblestore.SessionSnapshot) bool {
	if !sessionsV3SystemSidechat(session) {
		return false
	}
	writeError(w, http.StatusConflict, errors.New("reserved system sidechat settings, mode, agent, and plan lifecycle are parent-owned and locked"))
	return true
}

func (s *Server) handleSessionV3PrimaryAgent(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3AgentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID, ok := sessionsV3MutationClientRequestID(w, r, req.ClientRequestID, req.IdempotencyKey)
	if !ok {
		return
	}
	resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, req.AgentName)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	current, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if s.rejectSystemSidechatMutation(w, current) {
		return
	}
	next := current
	next.Metadata = sessionsV3AgentSwitchMetadata(current.Metadata, resolvedAgent)
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"agent_name": resolvedAgent.Name})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "agent_name": resolvedAgent.Name, "resolved_agent_name": resolvedAgent.ResolvedName, "agent_mode": resolvedAgent.Mode, "runtime_mode": resolvedAgent.RuntimeMode, "metadata": next.Metadata, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventType:       "session.agent.updated",
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	preference := normalizeSessionsV3ModelPreference(next.Preference)
	contextWindow := 0
	maxOutputTokens := 0
	if s.model != nil {
		if resolved, err := s.model.ResolvePreference(preference); err == nil {
			preference = normalizeSessionsV3ModelPreference(resolved.Preference)
			contextWindow = resolved.ContextWindow
			maxOutputTokens = resolved.MaxOutputTokens
		}
	}
	writeJSON(w, http.StatusOK, sessionV3AgentMutationResponse(sessionID, next, s.sessionsV3AgentModelPolicy(next, preference, contextWindow, maxOutputTokens), result))
}

func (s *Server) handleSessionV3PrimarySettings(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPatch {
		methodNotAllowed(w)
		return
	}
	var req sessionsV3SettingsPatchRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	if req.AgentName == nil && req.Preference == nil {
		writeError(w, http.StatusBadRequest, errors.New("at least one setting field is required"))
		return
	}
	current, found, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	if s.rejectSystemSidechatMutation(w, current) {
		return
	}
	next := current
	payload := map[string]any{"session_id": sessionID}
	if req.AgentName != nil {
		resolvedAgent, err := s.resolveSessionsV3PrimaryCreateAgent(principal, *req.AgentName)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		next.Metadata = sessionsV3AgentSwitchMetadata(next.Metadata, resolvedAgent)
		payload["agent_name"] = resolvedAgent.Name
		payload["resolved_agent_name"] = resolvedAgent.ResolvedName
		payload["agent_mode"] = resolvedAgent.Mode
		payload["runtime_mode"] = resolvedAgent.RuntimeMode
		payload["metadata"] = next.Metadata
	}
	if req.Preference != nil {
		next.Preference = normalizeSessionsV3ModelPreference(next.Preference)
		agentModelPolicy := s.sessionsV3AgentModelPolicy(next, next.Preference, 0, 0)
		if agentModelPolicy.Locked {
			writeError(w, http.StatusBadRequest, errors.New(agentModelPolicy.Reason))
			return
		}
		if s.model == nil {
			writeError(w, http.StatusInternalServerError, errors.New("model service is not configured"))
			return
		}
		pref := mergeSessionsV3PreferenceUpdate(next.Preference, *req.Preference)
		resolved, err := s.model.ResolvePreference(pref)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		next.Preference = normalizeSessionsV3ModelPreference(resolved.Preference)
		payload["preference"] = next.Preference
	}
	next.UpdatedAt = time.Now().UnixMilli()
	payload["updated_at"] = next.UpdatedAt
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateSettings, payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(payload)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:            sessionID,
		UserID:               principal.UserID,
		AccountScopeID:       principal.AccountScopeID,
		ClientRequestID:      clientRequestID,
		IdempotencyKey:       clientRequestID,
		PayloadHash:          payloadHash,
		RequestHash:          payloadHash,
		Kind:                 sessionruntime.SessionMutationUpdateSettings,
		EventPayload:         eventPayload,
		Session:              &next,
		ExpectedLastEventSeq: req.IfProjectionSeq,
		NowUnixMs:            next.UpdatedAt,
	})
	if err != nil {
		var conflict *pebblestore.V3ProjectionConflictError
		if errors.As(err, &conflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "code": fmt.Sprint(http.StatusConflict), "conflict": conflict})
			return
		}
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "code": fmt.Sprint(http.StatusConflict), "conflict": result.Conflict, "result": result})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	view, err := s.buildSessionsV3SessionView(principal, next, result.Projection, nil, false)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"session_view":    view,
		"mutation":        sessionV3MutationResultResponse(result),
		"realtime_outbox": result.RealtimeOutbox,
	})
}

func (s *Server) handleSessionV3PrimaryUsage(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	response, found, err := s.sessionsV3PrimaryUsageResponse(principal, sessionID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !found {
		writeSessionNotFound(w)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (s *Server) handleSessionV3PrimaryPreference(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if r.Method == http.MethodGet {
		response, found, err := s.sessionsV3PrimaryPreferenceResponse(principal, sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !found {
			writeSessionNotFound(w)
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}
	var req sessionsV3PreferenceRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	if len(clientRequestID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id must be 256 bytes or fewer"))
		return
	}
	requestPreference := sessionsV3PreferenceRequest{
		Provider: req.Provider, Thinking: req.Thinking, Model: req.Model,
		ServiceTier: req.ServiceTier, ContextMode: req.ContextMode,
	}
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdatePreference, map[string]any{"preference_update": requestPreference})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
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
	session.Preference = normalizeSessionsV3ModelPreference(session.Preference)
	agentModelPolicy := s.sessionsV3AgentModelPolicy(session, session.Preference, 0, 0)
	if agentModelPolicy.Locked {
		writeError(w, http.StatusBadRequest, errors.New(agentModelPolicy.Reason))
		return
	}
	pref := mergeSessionsV3PreferenceUpdate(session.Preference, req)
	if s.model == nil {
		writeError(w, http.StatusInternalServerError, errors.New("model service is not configured"))
		return
	}
	resolved, err := s.model.ResolvePreference(pref)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	next := session
	next.Preference = normalizeSessionsV3ModelPreference(resolved.Preference)
	next.UpdatedAt = time.Now().UnixMilli()
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "preference": next.Preference, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdatePreference,
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	responsePreference := next.Preference
	if result.Replayed && result.Session != nil {
		responsePreference = result.Session.Preference
	}
	writeJSON(w, http.StatusOK, s.sessionV3PreferenceMutationResponse(sessionID, responsePreference, resolved.ContextWindow, resolved.MaxOutputTokens, result))
}

func (s *Server) handleSessionV3PrimaryTitle(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
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
	var req sessionsV3TitleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, errors.New("title is required"))
		return
	}
	if len([]rune(title)) > 200 {
		writeError(w, http.StatusBadRequest, errors.New("title must be 200 characters or fewer"))
		return
	}
	clientRequestID := strings.TrimSpace(firstNonEmpty(req.ClientRequestID, req.IdempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return
	}
	next := session
	next.Title = title
	next.Metadata = authoritativeSessionTitleMetadata(session.Metadata, "manual")
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateTitle, map[string]any{"title": title, "metadata": next.Metadata})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "title": title, "metadata": next.Metadata, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationUpdateTitle, EventPayload: eventPayload, Session: &next, NowUnixMs: next.UpdatedAt})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "title": title, "metadata": next.Metadata, "mutation": sessionV3MutationResultResponse(result), "realtime_outbox": result.RealtimeOutbox})
}

func (s *Server) handleSessionV3PrimaryMetadata(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
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
	if r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "metadata": session.Metadata})
		return
	}
	var req sessionsV3MetadataRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID, ok := sessionsV3MutationClientRequestID(w, r, req.ClientRequestID, req.IdempotencyKey)
	if !ok {
		return
	}
	next := session
	next.Metadata = mergeSessionsV3MetadataUpdate(session.Metadata, req.Metadata)
	next.UpdatedAt = time.Now().UnixMilli()
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationUpdateMetadata, map[string]any{"metadata": req.Metadata})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	eventPayload, err := json.Marshal(map[string]any{"session_id": sessionID, "metadata": next.Metadata, "updated_at": next.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationUpdateMetadata,
		EventPayload:    eventPayload,
		Session:         &next,
		NowUnixMs:       next.UpdatedAt,
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict})
			return
		}
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, sessionV3MetadataMutationResponse(sessionID, next.Metadata, result))
}

func (s *Server) handleSessionV3PrimaryActivePlan(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		plan, ok, err := s.sessions.GetActivePlan(sessionID)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": false, "active_plan": nil})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "has_active": true, "active_plan": plan})
		return
	}
	var req struct {
		PlanID string `json:"plan_id"`
		ID     string `json:"id"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	prepared, err := s.sessions.PreparePlanActivation(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := fmt.Sprintf("plan-activate:%s:%s:%d", sessionID, planID, prepared.Plan.UpdatedAt)
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationSavePlan, map[string]any{"event_payload": string(prepared.EventPayload), "plan_id": planID, "updated_at": prepared.Plan.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash, Kind: sessionruntime.SessionMutationSavePlan, EventType: "session.plan.active", EventPayload: prepared.EventPayload, PlanSave: &pebblestore.V3PlanSaveMutation{Plan: prepared.Plan, Activate: true, ExpectedParentVersion: prepared.Plan.Version}, NowUnixMs: prepared.Plan.UpdatedAt})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	prepared.Plan.Active = true
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan": prepared.Plan, "mutation": sessionV3MutationResultResponse(result), "realtime_outbox": result.RealtimeOutbox})
}

func (s *Server) handleSessionV3PrimaryPlans(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if r.Method == http.MethodGet {
		limit, ok := parseSessionsV3PlansLimit(w, r)
		if !ok {
			return
		}
		plans, activeID, err := s.sessions.ListPlans(sessionID, limit)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "active_plan_id": activeID, "count": len(plans), "plans": plans})
		return
	}
	var req sessionsV3PlanUpsertRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	planID := strings.TrimSpace(req.PlanID)
	if planID == "" {
		planID = strings.TrimSpace(req.ID)
	}
	activate := true
	if req.Activate != nil {
		activate = *req.Activate
	}
	updateScope := strings.TrimSpace(req.UpdateScope)
	if updateScope == "" {
		updateScope = strings.TrimSpace(req.Scope)
	}
	metadata := sessionruntime.PlanSaveMetadata{UpdateSummary: req.UpdateSummary, UpdateScope: updateScope, UpdateKind: req.UpdateKind, RevisionKind: req.RevisionKind, Checkpoint: req.Checkpoint, Document: req.Document}
	var prepared sessionruntime.PreparedPlanSave
	var err error
	if req.DocumentPatch != nil {
		activatePtr := &activate
		prepared, err = s.sessions.PreparePlanPatch(sessionID, sessionruntime.PlanPatchOptions{PlanID: planID, Title: req.Title, Status: req.Status, ApprovalState: req.ApprovalState, Activate: activatePtr, Document: req.Document, DocumentPatch: req.DocumentPatch, Metadata: metadata})
	} else {
		prepared, err = s.sessions.PreparePlanSaveWithMetadata(sessionID, planID, req.Title, req.Plan, req.Status, req.ApprovalState, activate, metadata)
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	clientRequestID := fmt.Sprintf("plan-save:%s:%s:v%d", sessionID, prepared.Plan.ID, prepared.Plan.Version)
	payloadHash, err := sessionsV3UpdatePayloadHash(sessionID, sessionruntime.SessionMutationSavePlan, map[string]any{"event_payload": string(prepared.EventPayload), "plan_id": prepared.Plan.ID, "version": prepared.Plan.Version})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID: sessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID,
		ClientRequestID: clientRequestID, IdempotencyKey: clientRequestID, PayloadHash: payloadHash, RequestHash: payloadHash,
		Kind: sessionruntime.SessionMutationSavePlan, EventType: "session.plan.saved", EventPayload: prepared.EventPayload,
		PlanSave: &pebblestore.V3PlanSaveMutation{Plan: prepared.Plan, ArchivedRevision: prepared.ArchivedRevision, Activate: prepared.Activate, ExpectedParentVersion: prepared.Plan.ParentRevision}, NowUnixMs: prepared.Plan.UpdatedAt,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": prepared.Plan, "mutation": sessionV3MutationResultResponse(result), "realtime_outbox": result.RealtimeOutbox})
}

func (s *Server) preflightSessionsV3PlanFreshRun(_ *http.Request, _ identity.Principal, _ string) (int, error) {
	if s.runner == nil {
		return http.StatusInternalServerError, errors.New("run service not configured")
	}
	if s.runStreams == nil {
		return http.StatusInternalServerError, errors.New("run stream manager not configured")
	}
	if s.isShuttingDown() {
		return http.StatusServiceUnavailable, errors.New("daemon is shutting down")
	}
	return http.StatusOK, nil
}

func (s *Server) handleSessionV3PrimaryPlanByID(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, tail string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	if strings.HasSuffix(tail, "/history") {
		planID := strings.TrimSpace(strings.TrimSuffix(tail, "/history"))
		if planID == "" || strings.Contains(planID, "/") {
			writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
			return
		}
		limit, ok := parseSessionsV3PlansLimit(w, r)
		if !ok {
			return
		}
		revisionKind := strings.ToLower(strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("revision_kind"), r.URL.Query().Get("kind"))))
		if revisionKind == "" {
			revisionKind = sessionruntime.PlanRevisionKindDefinition
		}
		revisions, err := s.sessions.ListPlanRevisionsByKind(sessionID, planID, limit, revisionKind)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan_id": planID, "revision_kind": revisionKind, "count": len(revisions), "revisions": revisions})
		return
	}
	planID := strings.TrimSpace(tail)
	if planID == "" || strings.Contains(planID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("plan id is required"))
		return
	}
	plan, ok, err := s.sessions.GetPlan(sessionID, planID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "plan not found"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "plan": plan})
}

func (s *Server) handleSessionsV3SubagentStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.sessions == nil || s.runner == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 runtime is not configured"))
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req sessionsV3SubagentStopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		writeError(w, http.StatusBadRequest, errors.New("session_id is required"))
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
	if sessionsV3MetadataString(session.Metadata, "lineage_kind") != "delegated_subagent" {
		writeError(w, http.StatusBadRequest, errors.New("session is not a delegated subagent"))
		return
	}
	const reason = "user stopped subagent"
	if err := s.runner.StopSessionRun(sessionID, "", reason); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, runruntime.ErrSessionRunNotActive) {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"status":     "cancelled",
		"reason":     reason,
	})
}

func (s *Server) handleSessionV3PrimaryRunStop(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.v3SessionExecutor == nil {
		writeError(w, http.StatusInternalServerError, errors.New("sessions v3 executor is not configured"))
		return
	}
	var req sessionsV3StopRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	runID := strings.TrimSpace(req.RunID)
	if runID == "" {
		writeError(w, http.StatusBadRequest, errors.New("run_id is required"))
		return
	}
	targetSwarmID := strings.TrimSpace(req.TargetSwarmID)
	if targetSwarmID == "" {
		writeError(w, http.StatusBadRequest, errors.New("target_swarm_id is required"))
		return
	}
	if found, err := s.validateSessionsV3PrimaryStopTarget(principal, sessionID, targetSwarmID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = sessionV3RunStopDefaultReason
	}
	result, cancelled, err := s.v3SessionExecutor.CancelRun(sessionV3ExecutorJob{Principal: principal, SessionID: sessionID, RunID: runID}, reason)
	if err != nil {
		status := http.StatusBadRequest
		if !cancelled {
			status = http.StatusNotFound
		}
		writeError(w, status, err)
		return
	}
	if s.perm != nil {
		_, _ = s.perm.CancelRunPending(sessionID, runID, reason)
	}
	status := sessionruntime.RunIntentCancelled
	if result.RunIntent != nil && strings.TrimSpace(result.RunIntent.Status) != "" {
		status = strings.TrimSpace(result.RunIntent.Status)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":         true,
		"session_id": sessionID,
		"run_id":     runID,
		"status":     status,
		"reason":     reason,
		"mutation":   result,
	})
}

func (s *Server) handleSessionV3PrimaryPermissions(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	limit := 200
	if rawLimit := strings.TrimSpace(r.URL.Query().Get("limit")); rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a non-negative integer"))
			return
		}
		if parsed > 0 {
			limit = parsed
		}
	}
	status := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("status")))
	var records []pebblestore.PermissionRecord
	var err error
	switch status {
	case "", "all":
		records, err = s.perm.ListPermissions(sessionID, limit)
	case "pending":
		records, err = s.perm.ListPending(sessionID, limit)
	default:
		writeError(w, http.StatusBadRequest, errors.New("unsupported permission status"))
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(records), "permissions": records})
}

func (s *Server) handleSessionV3PrimaryPermissionResolve(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID, permissionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	permissionID = strings.Trim(permissionID, "/")
	if permissionID == "" || strings.Contains(permissionID, "/") {
		writeError(w, http.StatusBadRequest, errors.New("permission id is required"))
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		Action            string          `json:"action"`
		Reason            string          `json:"reason"`
		ApprovedArguments json.RawMessage `json:"approved_arguments,omitempty"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	record, savedRule, err := s.perm.ResolveWithPolicyAndArguments(sessionID, permissionID, req.Action, req.Reason, string(req.ApprovedArguments))
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mutation, published, err := s.publishSessionV3PermissionUpdatedFromRecord(principal, sessionID, record)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "permission": record, "saved_rule": savedRule, "mutation": mutation, "published": published})
}

func (s *Server) handleSessionV3PrimaryPermissionResolveAll(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	if s.perm == nil {
		writeError(w, http.StatusInternalServerError, errors.New("permission service is not configured"))
		return
	}
	if found, err := s.authorizeSessionsV3PrimarySession(principal, sessionID); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	} else if !found {
		writeSessionNotFound(w)
		return
	}
	var req struct {
		Action string `json:"action"`
		Reason string `json:"reason"`
		Limit  int    `json:"limit"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	resolved, err := s.perm.ResolveAll(sessionID, req.Action, req.Reason, req.Limit)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	mutations := make([]sessionruntime.SessionMutationResult, 0, len(resolved))
	for _, record := range resolved {
		mutation, published, err := s.publishSessionV3PermissionUpdatedFromRecord(principal, sessionID, record)
		if err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if published {
			mutations = append(mutations, mutation)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "session_id": sessionID, "count": len(resolved), "resolved": resolved, "mutations": mutations})
}

func (s *Server) publishSessionV3PermissionUpdatedFromRecord(principal identity.Principal, sessionID string, record pebblestore.PermissionRecord) (sessionruntime.SessionMutationResult, bool, error) {
	if s == nil || s.sessions == nil {
		return sessionruntime.SessionMutationResult{}, false, errors.New("sessions v3 service is not configured")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(record.SessionID)
	}
	if sessionID == "" {
		return sessionruntime.SessionMutationResult{}, false, errors.New("session id is required")
	}
	if strings.TrimSpace(record.SessionID) != "" && strings.TrimSpace(record.SessionID) != sessionID {
		return sessionruntime.SessionMutationResult{}, false, errors.New("permission belongs to a different session")
	}
	runID := strings.TrimSpace(record.RunID)
	callID := strings.TrimSpace(record.CallID)
	if runID == "" || callID == "" {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	existingIntent, ok, err := s.sessions.GetSessionRunIntent(sessionID, runID)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	if !ok || strings.TrimSpace(existingIntent.Status) != sessionruntime.RunIntentRunning {
		return sessionruntime.SessionMutationResult{}, false, nil
	}
	step := record.Step
	if step <= 0 {
		step = 1
	}
	toolName := strings.TrimSpace(record.ToolName)
	if toolName == "" {
		toolName = "tool"
	}
	arguments := strings.TrimSpace(firstNonEmpty(record.ToolCallArguments, record.ToolArguments))
	payload := sessionV3PermissionUpdatedPayload(sessionID, runID, step, toolName, callID, arguments, record)
	raw, err := json.Marshal(payload)
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	payloadHash, err := sessionV3ExecutorPayloadHash(sessionID, runID, sessionruntime.RunIntentRunning, "", "permission.updated", string(raw))
	if err != nil {
		return sessionruntime.SessionMutationResult{}, false, err
	}
	deltaIndex := 0
	if record.ProposalRevision > 0 {
		deltaIndex = int(record.ProposalRevision) * 10
		if !strings.EqualFold(strings.TrimSpace(record.Status), pebblestore.PermissionStatusPending) {
			deltaIndex++
		}
	}
	clientRequestID := sessionV3ProviderToolEventClientRequestID("permission.updated", runID, step, callID, deltaIndex)
	now := time.Now().UnixMilli()
	intent := pebblestore.V3SessionRunIntent{RunID: runID, Status: sessionruntime.RunIntentRunning, UpdatedAt: now}
	if principal.Valid() {
		intent.UserID = strings.TrimSpace(principal.UserID)
		intent.AccountScopeID = strings.TrimSpace(principal.AccountScopeID)
	} else if session, ok, sessionErr := s.sessions.GetSession(sessionID); sessionErr != nil {
		return sessionruntime.SessionMutationResult{}, false, sessionErr
	} else if ok {
		intent.UserID = strings.TrimSpace(session.UserID)
		intent.AccountScopeID = strings.TrimSpace(session.AccountScopeID)
		principal = identity.Principal{Type: identity.PrincipalTypeUser, UserID: session.UserID, AccountScopeID: session.AccountScopeID}
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          strings.TrimSpace(principal.UserID),
		AccountScopeID:  strings.TrimSpace(principal.AccountScopeID),
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationRecordRunIntent,
		EventType:       "permission.updated",
		EventPayload:    raw,
		RunIntent:       &intent,
		NowUnixMs:       now,
	})
	if err != nil {
		return result, false, err
	}
	return result, !result.Replayed, nil
}

func (s *Server) authorizeSessionsV3PrimarySession(principal identity.Principal, sessionID string) (bool, error) {
	_, ok, err := s.requireSessionV3Access(principal, sessionID)
	return ok, err
}

func (s *Server) authorizeReadableSessionV3Access(principal identity.Principal, sessionID string) (bool, error) {
	_, ok, err := s.requireSessionV3Access(principal, sessionID)
	if err != nil || ok {
		return ok, err
	}
	_, ok, err = s.requireArchivedSessionV3Access(principal, sessionID)
	return ok, err
}

func (s *Server) requireSessionV3Access(principal identity.Principal, sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return pebblestore.SessionSnapshot{}, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	if strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	return session, true, nil
}

func (s *Server) requireArchivedSessionV3Access(principal identity.Principal, sessionID string) (pebblestore.SessionSnapshot, bool, error) {
	store := s.sessions.Store()
	if store == nil {
		return pebblestore.SessionSnapshot{}, false, errors.New("session store is not configured")
	}
	tombstone, ok, err := store.GetV3SessionTombstone(sessionID)
	if err != nil || !ok || !tombstone.Archived || tombstone.Deleted || tombstone.Session.ID == "" {
		return pebblestore.SessionSnapshot{}, false, err
	}
	session := tombstone.Session
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	if strings.TrimSpace(session.UserID) == "" || strings.TrimSpace(session.UserID) != strings.TrimSpace(principal.UserID) {
		return pebblestore.SessionSnapshot{}, false, nil
	}
	return session, true, nil
}

func (s *Server) validateSessionsV3PrimaryStopTarget(principal identity.Principal, sessionID, targetSwarmID string) (bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return false, nil
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return false, err
	}
	primarySwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || primarySwarmID == "" {
		return false, errors.New("sessions v3 primary local node identity is required")
	}
	if strings.TrimSpace(targetSwarmID) == "" {
		return false, errors.New("target_swarm_id is required")
	}
	if strings.TrimSpace(targetSwarmID) != primarySwarmID {
		return false, fmt.Errorf("target_swarm_id %q is not the primary runtime", strings.TrimSpace(targetSwarmID))
	}
	metadataSwarmID := sessionsV3MetadataString(session.Metadata, "swarm_v3_runtime_swarm_id")
	if metadataSwarmID != "" && metadataSwarmID != primarySwarmID {
		return false, fmt.Errorf("session runtime swarm_id %q is not the primary runtime", metadataSwarmID)
	}
	return true, nil
}

func (s *Server) hydrateSessionsV3Primary(principal identity.Principal, sessionID string) (sessionsV3HydratedSession, bool, error) {
	return s.hydrateSessionsV3PrimaryWithLimitsForSurface(principal, sessionID, sessionsV3PrimaryDefaultMessageTailLimit, sessionsV3PrimaryDefaultEventLimit, "desktop")
}

func (s *Server) hydrateSessionsV3PrimaryForSurface(principal identity.Principal, sessionID, surface string) (sessionsV3HydratedSession, bool, error) {
	return s.hydrateSessionsV3PrimaryWithLimitsForSurface(principal, sessionID, sessionsV3PrimaryDefaultMessageTailLimit, sessionsV3PrimaryDefaultEventLimit, surface)
}

func (s *Server) sessionsV3PrimaryUsageResponse(principal identity.Principal, sessionID string) (map[string]any, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return nil, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return nil, false, nil
	}
	summary, hasSummary, err := s.sessions.GetUsageSummary(sessionID)
	if err != nil {
		return nil, false, err
	}
	var summaryPayload any
	if hasSummary {
		summaryPayload = summary
	}
	return map[string]any{
		"ok":                true,
		"session_id":        session.ID,
		"has_usage_summary": hasSummary,
		"usage_summary":     summaryPayload,
	}, true, nil
}

func (s *Server) sessionV3PreferenceMutationResponse(sessionID string, preference pebblestore.ModelPreference, contextWindow, maxOutputTokens int, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":                true,
		"session_id":        sessionID,
		"preference":        preference,
		"context_window":    contextWindow,
		"max_output_tokens": maxOutputTokens,
		"mutation":          sessionV3MutationResultResponse(result),
		"realtime_outbox":   result.RealtimeOutbox,
	}
}

func (s *Server) sessionsV3PrimaryPreferenceResponse(principal identity.Principal, sessionID string) (map[string]any, bool, error) {
	session, ok, err := s.sessions.GetSession(sessionID)
	if err != nil || !ok {
		return nil, ok, err
	}
	if strings.TrimSpace(session.AccountScopeID) == "" || strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return nil, false, nil
	}
	preference := normalizeSessionsV3ModelPreference(session.Preference)
	contextWindow := 0
	maxOutputTokens := 0
	if s.model != nil {
		resolved, err := s.model.ResolvePreference(preference)
		if err != nil {
			return nil, false, err
		}
		preference = normalizeSessionsV3ModelPreference(resolved.Preference)
		contextWindow = resolved.ContextWindow
		maxOutputTokens = resolved.MaxOutputTokens
	}
	agentModelPolicy := s.sessionsV3AgentModelPolicy(session, preference, contextWindow, maxOutputTokens)
	if agentModelPolicy.Locked {
		preference = agentModelPolicy.Preference
		contextWindow = agentModelPolicy.ContextWindow
		maxOutputTokens = agentModelPolicy.MaxOutputTokens
	}
	return map[string]any{
		"ok":                 true,
		"session_id":         session.ID,
		"preference":         preference,
		"context_window":     contextWindow,
		"max_output_tokens":  maxOutputTokens,
		"agent_model_policy": agentModelPolicy,
	}, true, nil
}

func (s *Server) hydrateSessionsV3PrimaryWithLimits(principal identity.Principal, sessionID string, messageLimit, eventLimit int) (sessionsV3HydratedSession, bool, error) {
	return s.hydrateSessionsV3PrimaryWithLimitsForSurface(principal, sessionID, messageLimit, eventLimit, "desktop")
}

func (s *Server) hydrateSessionsV3PrimaryWithLimitsForSurface(principal identity.Principal, sessionID string, messageLimit, eventLimit int, surface string) (sessionsV3HydratedSession, bool, error) {
	hydrated, ok, err := s.sessions.HydrateSessionSnapshot(sessionID, messageLimit, eventLimit)
	if err != nil || !ok {
		return sessionsV3HydratedSession{}, ok, err
	}
	if strings.TrimSpace(hydrated.Session.AccountScopeID) == "" || strings.TrimSpace(hydrated.Session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3HydratedSession{}, false, nil
	}
	if strings.TrimSpace(hydrated.Session.UserID) == "" || strings.TrimSpace(hydrated.Session.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionsV3HydratedSession{}, false, nil
	}
	projection := pebblestore.V3SessionProjection{
		SessionID:                  hydrated.Projection.SessionID,
		LastEventSeq:               hydrated.Projection.LastEventSeq,
		ProjectionHighWatermarkSeq: hydrated.Projection.ProjectionHighWatermarkSeq,
		UpdatedAt:                  hydrated.Projection.UpdatedAt,
	}
	view, err := s.buildSessionsV3SessionView(principal, hydrated.Session, projection, nil, false)
	if err != nil {
		return sessionsV3HydratedSession{}, false, err
	}
	var activeRunIntent *pebblestore.V3SessionRunIntent
	if intent, ok, err := s.sessions.GetSessionActiveRunIntent(sessionID); err != nil {
		return sessionsV3HydratedSession{}, false, err
	} else if ok && sessionV3RunIntentStatusActive(intent.Status) {
		activeRunIntent = &intent
	}
	preference := view.AgenticSettings.EffectivePreference
	contextWindow := view.AgenticSettings.ContextWindow
	maxOutputTokens := view.AgenticSettings.MaxOutputTokens
	agentModelPolicy := view.AgenticSettings.AgentModelPolicy
	snapshotEndpointCursor, err := s.signV3SyncEndpointCursorFromLegacy(v3SyncCursorScopeForRealtime(principal, surface), hydrated.SnapshotEndpointCursor)
	if err != nil {
		return sessionsV3HydratedSession{}, false, err
	}
	return sessionsV3HydratedSession{Session: hydrated.Session, Projection: hydrated.Projection, Messages: hydrated.Messages, Events: hydrated.Events, PendingPermissions: view.PendingPermissions, UsageSummary: view.UsageSummary, ActiveRunIntent: activeRunIntent, Preference: preference, ContextWindow: contextWindow, MaxOutputTokens: maxOutputTokens, AgentModelPolicy: agentModelPolicy, PlanRevisions: []pebblestore.SessionPlanSnapshot{}, AppliedSeq: hydrated.Projection.LastEventSeq, HighWatermark: hydrated.Projection.ProjectionHighWatermarkSeq, SnapshotEndpointCursor: snapshotEndpointCursor}, true, nil
}

func (s *Server) resolveSessionsV3ModeTransition(session pebblestore.SessionSnapshot, targetMode string) (modelpolicy.ModeTransition, error) {
	if s == nil || s.model == nil {
		return modelpolicy.ModeTransition{}, errors.New("model service is not configured")
	}
	if s.agents == nil {
		return modelpolicy.ModeTransition{}, errors.New("agent service is not configured")
	}
	name := firstNonEmpty(sessionsV3MetadataString(session.Metadata, "resolved_agent_name"), sessionsV3MetadataString(session.Metadata, "agent_name"))
	if name == "" {
		return modelpolicy.ModeTransition{}, errors.New("session active agent is not configured")
	}
	profile, err := s.agents.ResolveAgentForAccount(session.AccountScopeID, name)
	if err != nil {
		return modelpolicy.ModeTransition{}, fmt.Errorf("resolve active agent %q: %w", name, err)
	}
	return modelpolicy.ResolveModeTransition(session, profile, targetMode, func(preference pebblestore.ModelPreference) (modelpolicy.ResolvedPreference, error) {
		resolved, err := s.model.ResolvePreference(preference)
		return modelpolicy.ResolvedPreference{Preference: resolved.Preference, ContextWindow: resolved.ContextWindow, MaxOutputTokens: resolved.MaxOutputTokens}, err
	})
}

func (s *Server) sessionsV3AgentModelPolicy(session pebblestore.SessionSnapshot, defaultPreference pebblestore.ModelPreference, defaultContextWindow, defaultMaxOutputTokens int) sessionsV3AgentModelPolicy {
	resolvedPreferenceCache := make(map[sessionsV3SyncPreferenceCacheKey]sessionsV3SyncResolvedPreference, 2)
	resolvePreference := func(preference pebblestore.ModelPreference) sessionsV3SyncResolvedPreference {
		return s.resolveSessionsV3SyncPreference(preference, resolvedPreferenceCache)
	}
	return s.sessionsV3AgentModelPolicyWithResolver(session, defaultPreference, defaultContextWindow, defaultMaxOutputTokens, resolvePreference)
}

func sessionsV3AgentPresetPreference(profile pebblestore.AgentProfile) pebblestore.ModelPreference {
	provider := strings.ToLower(strings.TrimSpace(profile.Provider))
	model := strings.TrimSpace(profile.Model)
	if provider == "" || model == "" {
		return pebblestore.ModelPreference{}
	}
	return pebblestore.ModelPreference{
		Provider:    provider,
		Model:       model,
		Thinking:    normalizeSessionV3ThinkingWithProvider(provider, profile.Thinking),
		ServiceTier: strings.TrimSpace(profile.AutoServiceTier),
		UpdatedAt:   profile.UpdatedAt,
	}
}

func sessionsV3MetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func sessionsV3MutationClientRequestID(w http.ResponseWriter, r *http.Request, requestID, idempotencyKey string) (string, bool) {
	clientRequestID := strings.TrimSpace(firstNonEmpty(requestID, idempotencyKey, r.Header.Get("Idempotency-Key")))
	if clientRequestID == "" {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id is required"))
		return "", false
	}
	if len(clientRequestID) > 256 {
		writeError(w, http.StatusBadRequest, errors.New("client_request_id must be 256 characters or fewer"))
		return "", false
	}
	return clientRequestID, true
}

func sessionV3MutationResultResponse(result sessionruntime.SessionMutationResult) sessionruntime.SessionMutationResult {
	mutation := result
	mutation.Session = nil
	return mutation
}

func sessionV3MessageMutationResponse(sessionID string, result sessionruntime.SessionMutationResult, currentRunState *pebblestore.V3SessionRunState) map[string]any {
	return map[string]any{
		"ok":                true,
		"session_id":        sessionID,
		"session":           result.Session,
		"projection":        result.Projection,
		"message":           result.Message,
		"run_intent":        result.RunIntent,
		"current_run_state": currentRunState,
		"turn_usage":        result.TurnUsage,
		"usage_summary":     result.UsageSummary,
		"mutation":          sessionV3MutationResultResponse(result),
		"realtime_outbox":   result.RealtimeOutbox,
	}
}

func sessionV3MetadataMutationResponse(sessionID string, metadata map[string]any, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"metadata":        metadata,
		"mutation":        sessionV3MutationResultResponse(result),
		"realtime_outbox": result.RealtimeOutbox,
	}
}

func sessionV3AgentMutationResponse(sessionID string, session pebblestore.SessionSnapshot, agentModelPolicy sessionsV3AgentModelPolicy, result sessionruntime.SessionMutationResult) map[string]any {
	return map[string]any{
		"ok":                 true,
		"session_id":         sessionID,
		"agent":              sessionsV3AgentResource(session),
		"metadata":           session.Metadata,
		"agent_model_policy": agentModelPolicy,
		"mutation":           sessionV3MutationResultResponse(result),
		"realtime_outbox":    result.RealtimeOutbox,
	}
}

func sessionsV3AgentResource(session pebblestore.SessionSnapshot) map[string]any {
	metadata := session.Metadata
	return map[string]any{
		"agent_name":             sessionsV3MetadataString(metadata, "agent_name"),
		"resolved_agent_name":    sessionsV3MetadataString(metadata, "resolved_agent_name"),
		"agent_mode":             sessionsV3MetadataString(metadata, "agent_mode"),
		"runtime_mode":           sessionsV3MetadataString(metadata, "runtime_mode"),
		"exit_plan_mode_enabled": metadata["exit_plan_mode_enabled"],
		"tool_contract_preset":   sessionsV3MetadataString(metadata, "tool_contract_preset"),
	}
}

func sessionV3CreateResultResponse(result sessionruntime.SessionMutationResult) (map[string]any, error) {
	if result.Session == nil {
		return nil, errors.New("created sessions v3 session was not returned")
	}
	if strings.TrimSpace(result.SessionID) == "" {
		return nil, errors.New("created sessions v3 session_id was not returned")
	}
	return map[string]any{
		"ok":              true,
		"session_id":      result.SessionID,
		"session":         result.Session,
		"projection":      result.Projection,
		"mutation":        sessionV3MutationResultResponse(result),
		"realtime_outbox": result.RealtimeOutbox,
	}, nil
}

func sessionsV3HydratedResponse(hydrated sessionsV3HydratedSession, fields gitStatusResponseFields) map[string]any {
	response := map[string]any{
		"ok":                       true,
		"session":                  hydratedSessionSummaryResponse(hydrated.Session, fields),
		"projection":               hydrated.Projection,
		"messages":                 hydrated.Messages,
		"events":                   hydrated.Events,
		"pending_permissions":      hydrated.PendingPermissions,
		"usage_summary":            hydrated.UsageSummary,
		"active_run_intent":        hydrated.ActiveRunIntent,
		"preference":               hydrated.Preference,
		"context_window":           hydrated.ContextWindow,
		"max_output_tokens":        hydrated.MaxOutputTokens,
		"agent_model_policy":       hydrated.AgentModelPolicy,
		"has_active_plan":          hydrated.HasActivePlan,
		"active_plan":              nil,
		"plan_revisions":           hydrated.PlanRevisions,
		"applied_seq":              hydrated.AppliedSeq,
		"high_watermark":           hydrated.HighWatermark,
		"snapshot_endpoint_cursor": hydrated.SnapshotEndpointCursor,
	}
	if hydrated.HasActivePlan {
		response["active_plan"] = hydrated.ActivePlan
	}
	return response
}

func parseSessionsV3PrimaryPath(path string) (string, string, bool) {
	if !strings.HasPrefix(path, sessionsV3PrimaryPrefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(path, sessionsV3PrimaryPrefix)
	if rest == "" || strings.HasPrefix(rest, "/") || strings.HasSuffix(rest, "/") || strings.Contains(rest, "//") {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return "", "", false
		}
	}
	sessionID := strings.TrimSpace(parts[0])
	if sessionID == "" {
		return "", "", false
	}
	if len(parts) == 1 {
		return sessionID, "", true
	}
	return sessionID, strings.Join(parts[1:], "/"), true
}

func parseSessionsV3PlansLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	limit, ok := parseRequestPositiveLimit(w, r, sessionsV3PlansPageDefaultLimit)
	if !ok {
		return 0, false
	}
	if limit > sessionsV3PlansPageMaxLimit {
		writeError(w, http.StatusBadRequest, errors.New("plan limit cannot exceed 100"))
		return 0, false
	}
	return limit, true
}

func parseSessionsV3HydrationLimits(w http.ResponseWriter, r *http.Request) (int, int, bool) {
	messageLimit := sessionsV3PrimaryDefaultMessageTailLimit
	if raw := strings.TrimSpace(firstNonEmpty(r.URL.Query().Get("message_limit"), r.URL.Query().Get("tail_limit"))); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("message_limit must be a non-negative integer"))
			return 0, 0, false
		}
		messageLimit = parsed
	}
	eventLimit := sessionsV3PrimaryDefaultEventLimit
	if raw := strings.TrimSpace(r.URL.Query().Get("event_limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			writeError(w, http.StatusBadRequest, errors.New("event_limit must be a non-negative integer"))
			return 0, 0, false
		}
		eventLimit = parsed
	}
	return messageLimit, eventLimit, true
}

type sessionsV3MessagesPageQuery struct {
	AfterSeq     uint64
	BeforeSeq    uint64
	HasBeforeSeq bool
	Tail         bool
	Limit        int
}

func parseSessionsV3MessagesPageQuery(w http.ResponseWriter, r *http.Request) (sessionsV3MessagesPageQuery, bool) {
	query := sessionsV3MessagesPageQuery{Limit: sessionsV3MessagesPageDefaultLimit}
	hasAfterSeq := false
	if raw := strings.TrimSpace(r.URL.Query().Get("after_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("after_seq must be an unsigned integer"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.AfterSeq = parsed
		hasAfterSeq = true
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("before_seq")); raw != "" {
		parsed, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("before_seq must be an unsigned integer"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.BeforeSeq = parsed
		query.HasBeforeSeq = true
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("tail")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, errors.New("tail must be a boolean"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.Tail = parsed
	}
	if query.HasBeforeSeq && hasAfterSeq {
		writeError(w, http.StatusBadRequest, errors.New("after_seq and before_seq cannot be combined"))
		return sessionsV3MessagesPageQuery{}, false
	}
	if query.Tail && (query.HasBeforeSeq || hasAfterSeq) {
		writeError(w, http.StatusBadRequest, errors.New("tail cannot be combined with after_seq or before_seq"))
		return sessionsV3MessagesPageQuery{}, false
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			writeError(w, http.StatusBadRequest, errors.New("limit must be a positive integer"))
			return sessionsV3MessagesPageQuery{}, false
		}
		query.Limit = parsed
	}
	if query.Limit > sessionsV3MessagesPageMaxLimit {
		query.Limit = sessionsV3MessagesPageMaxLimit
	}
	return query, true
}

func sessionsV3MessagesPageResponse(sessionID string, messages []pebblestore.MessageSnapshot, query sessionsV3MessagesPageQuery) map[string]any {
	hasMoreOlder := false
	hasMoreNewer := false
	if query.Tail || query.HasBeforeSeq {
		if len(messages) > query.Limit {
			hasMoreOlder = true
			messages = messages[len(messages)-query.Limit:]
		}
	} else if len(messages) > query.Limit {
		hasMoreNewer = true
		messages = messages[:query.Limit]
	}

	oldestSeq := uint64(0)
	newestSeq := uint64(0)
	if len(messages) > 0 {
		oldestSeq = messages[0].GlobalSeq
		newestSeq = messages[len(messages)-1].GlobalSeq
	}
	response := map[string]any{
		"ok":              true,
		"session_id":      sessionID,
		"messages":        messages,
		"count":           len(messages),
		"limit":           query.Limit,
		"oldest_seq":      oldestSeq,
		"newest_seq":      newestSeq,
		"has_more":        hasMoreOlder || hasMoreNewer,
		"has_more_older":  hasMoreOlder,
		"has_more_newer":  hasMoreNewer,
		"next_before_seq": uint64(0),
		"next_after_seq":  uint64(0),
		"page_cursor":     nil,
	}
	if len(messages) > 0 {
		response["next_before_seq"] = oldestSeq
		response["next_after_seq"] = newestSeq
	}
	if query.Tail {
		response["tail"] = true
		response["has_more"] = hasMoreOlder
		if len(messages) > 0 {
			response["page_cursor"] = oldestSeq
		}
		return response
	}
	if query.HasBeforeSeq {
		hasMoreNewer = len(messages) > 0 && query.BeforeSeq > newestSeq
		response["before_seq"] = query.BeforeSeq
		response["has_more_newer"] = hasMoreNewer
		response["has_more"] = hasMoreOlder || hasMoreNewer
		if len(messages) > 0 {
			response["page_cursor"] = oldestSeq
		}
		return response
	}
	hasMoreOlder = len(messages) > 0 && query.AfterSeq > 0
	response["after_seq"] = query.AfterSeq
	response["has_more_older"] = hasMoreOlder
	response["has_more"] = hasMoreOlder || hasMoreNewer
	if len(messages) > 0 {
		response["page_cursor"] = newestSeq
	}
	return response
}

type sessionsV3PrimaryBinding struct {
	RuntimeSwarmID            string
	WorkspaceBindingID        string
	SourceWorkspaceID         string
	SourceWorkspaceGeneration int64
	SourceWorkspaceName       string
	SourceWorkspacePath       string
	RuntimeWorkspacePath      string
	PlacementGeneration       int
	BindingGeneration         int
}

func (s *Server) resolveSessionsV3PrimaryBinding(principal identity.Principal, req sessionsV3CreateRequest) (sessionsV3PrimaryBinding, error) {
	if s == nil || s.topology == nil {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary topology is not configured")
	}
	localNode, localOK, err := s.swarmLocalNode()
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	primarySwarmID := strings.TrimSpace(localNode.SwarmID)
	if !localOK || primarySwarmID == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary local node identity is required")
	}
	swarmID := strings.TrimSpace(req.SwarmID)
	if swarmID == "" {
		swarmID = primarySwarmID
	}
	if swarmID != primarySwarmID {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary swarm_id %q is not the primary runtime", swarmID)
	}
	if targetKind := strings.ToLower(strings.TrimSpace(req.TargetKind)); targetKind != "" && targetKind != "host" && targetKind != "self" {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary target_kind %q is not primary host", strings.TrimSpace(req.TargetKind))
	}
	if targetRelationship := strings.ToLower(strings.TrimSpace(req.TargetRelationship)); targetRelationship != "" && targetRelationship != "self" {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary target_relationship %q is not self", strings.TrimSpace(req.TargetRelationship))
	}
	workspaceBindingID := strings.TrimSpace(req.WorkspaceBindingID)
	defaultWorkspacePath := ""
	if workspaceBindingID == "" {
		workspacePath := strings.TrimSpace(req.WorkspacePath)
		if workspacePath == "" {
			workspacePath = strings.TrimSpace(req.HostWorkspacePath)
		}
		if workspacePath == "" {
			if s.workspace == nil {
				return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary default workspace service is not configured")
			}
			current, ok, currentErr := s.workspace.CurrentBindingForPrincipal(principal)
			if currentErr != nil {
				return sessionsV3PrimaryBinding{}, currentErr
			}
			if ok {
				defaultWorkspacePath = strings.TrimSpace(current.WorkspacePath)
			}
		}
		if workspacePath == "" {
			if defaultWorkspacePath == "" {
				return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary account has no default workspace")
			}
			workspacePath = defaultWorkspacePath
		}
		bindings, listErr := s.topology.ListWorkspaceBindingsBySourcePathForAccount(principal.AccountScopeID, workspacePath, 100)
		if listErr != nil {
			return sessionsV3PrimaryBinding{}, listErr
		}
		for _, candidate := range bindings {
			if filepath.Clean(strings.TrimSpace(candidate.SourceWorkspacePath)) != filepath.Clean(workspacePath) || strings.TrimSpace(candidate.DestinationRuntimeSwarmID) != primarySwarmID || strings.TrimSpace(candidate.State) != pebblestore.TopologyWorkspaceBindingStateBound {
				continue
			}
			if workspaceBindingID != "" && workspaceBindingID != strings.TrimSpace(candidate.BindingID) {
				return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary default workspace has multiple canonical local bindings")
			}
			workspaceBindingID = strings.TrimSpace(candidate.BindingID)
		}
		if workspaceBindingID == "" {
			return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace is missing its canonical local binding")
		}
	}
	runtimeRecord, runtimeOK, err := s.topology.GetRuntimeForAccount(principal.AccountScopeID, swarmID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !runtimeOK {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary runtime %q was not found", swarmID)
	}
	if strings.TrimSpace(runtimeRecord.SwarmID) != swarmID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime identity mismatch")
	}
	if strings.TrimSpace(runtimeRecord.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime account scope does not match principal")
	}
	if strings.TrimSpace(runtimeRecord.UserID) != "" && strings.TrimSpace(runtimeRecord.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime user does not match principal")
	}
	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, swarmID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !placementOK {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary runtime placement for %q was not found", swarmID)
	}
	if strings.TrimSpace(placement.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement account scope does not match principal")
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement is not active")
	}
	if strings.TrimSpace(placement.RuntimeSwarmID) != swarmID || strings.TrimSpace(placement.AuthorityHostSwarmID) != swarmID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement does not match selected self authority")
	}
	if strings.TrimSpace(placement.RuntimeKind) != pebblestore.TopologyRuntimeKindHost {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement kind must be host")
	}
	if strings.TrimSpace(placement.AuthorityContainerID) != "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement authority container id must be empty")
	}
	if placement.PlacementGeneration <= 0 {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime placement generation is required")
	}
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, workspaceBindingID)
	if err != nil {
		return sessionsV3PrimaryBinding{}, err
	}
	if !bindingOK {
		return sessionsV3PrimaryBinding{}, fmt.Errorf("sessions v3 primary workspace binding %q was not found", workspaceBindingID)
	}
	if strings.TrimSpace(binding.BindingID) != workspaceBindingID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding id mismatch")
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding account scope does not match principal")
	}
	if strings.TrimSpace(binding.UserID) != "" && strings.TrimSpace(binding.UserID) != strings.TrimSpace(principal.UserID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding user does not match principal")
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding is not bound")
	}
	if strings.TrimSpace(binding.SourceWorkspaceID) == "" || binding.SourceWorkspaceGeneration <= 0 || strings.TrimSpace(binding.SourceWorkspacePath) == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding source workspace identity is incomplete")
	}
	if s.workspace != nil {
		entry, entryOK, entryErr := s.workspace.GetByWorkspaceIDForPrincipal(principal, binding.SourceWorkspaceID)
		if entryErr != nil {
			return sessionsV3PrimaryBinding{}, entryErr
		}
		if !entryOK || entry.WorkspaceGeneration != binding.SourceWorkspaceGeneration || !strings.EqualFold(strings.TrimSpace(entry.State), "active") || filepath.Clean(strings.TrimSpace(entry.Path)) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
			return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding source workspace is stale")
		}
	}
	if strings.TrimSpace(binding.DestinationWorkspacePath) == "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding destination workspace path is required")
	}
	if binding.PlacementGeneration != placement.PlacementGeneration || binding.BindingGeneration <= 0 {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding generation does not match placement")
	}
	if strings.TrimSpace(binding.AttestedByHostSwarmID) != strings.TrimSpace(placement.AuthorityHostSwarmID) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding attesting host does not match authority host")
	}
	if strings.TrimSpace(binding.AccessMode) != pebblestore.TopologyWorkspaceBindingAccessModeReadWrite || !binding.Writable {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding must be read_write and writable")
	}
	if strings.TrimSpace(binding.MaterializationKind) != pebblestore.TopologyWorkspaceBindingMaterializationSource {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding must be a local source materialization")
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != swarmID || strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != swarmID {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding does not match selected self authority")
	}
	if strings.TrimSpace(binding.DestinationRuntimeKind) != pebblestore.TopologyRuntimeKindHost {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding destination runtime kind must be host")
	}
	if strings.TrimSpace(binding.DestinationContainerID) != "" {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace binding destination container id must be empty")
	}
	requestedWorkspacePath := strings.TrimSpace(req.WorkspacePath)
	if requestedWorkspacePath == "" && strings.TrimSpace(req.WorkspaceBindingID) == "" {
		requestedWorkspacePath = defaultWorkspacePath
	}
	if requestedWorkspacePath != "" && filepath.Clean(requestedWorkspacePath) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary workspace_path does not match workspace binding source")
	}
	if requestedHostWorkspacePath := strings.TrimSpace(req.HostWorkspacePath); requestedHostWorkspacePath != "" && filepath.Clean(requestedHostWorkspacePath) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary host_workspace_path does not match workspace binding source")
	}
	if requestedRuntimeWorkspacePath := strings.TrimSpace(req.RuntimeWorkspacePath); requestedRuntimeWorkspacePath != "" && filepath.Clean(requestedRuntimeWorkspacePath) != filepath.Clean(strings.TrimSpace(binding.DestinationWorkspacePath)) {
		return sessionsV3PrimaryBinding{}, errors.New("sessions v3 primary runtime_workspace_path does not match workspace binding destination")
	}
	return sessionsV3PrimaryBinding{
		RuntimeSwarmID:            swarmID,
		WorkspaceBindingID:        workspaceBindingID,
		SourceWorkspaceID:         strings.TrimSpace(binding.SourceWorkspaceID),
		SourceWorkspaceGeneration: binding.SourceWorkspaceGeneration,
		SourceWorkspaceName:       strings.TrimSpace(binding.SourceWorkspaceName),
		SourceWorkspacePath:       strings.TrimSpace(binding.SourceWorkspacePath),
		RuntimeWorkspacePath:      strings.TrimSpace(binding.DestinationWorkspacePath),
		PlacementGeneration:       placement.PlacementGeneration,
		BindingGeneration:         binding.BindingGeneration,
	}, nil
}

func validateSessionsV3CreateWorktreeRequest(rawMode string, useCurrentBranch *bool, baseBranch, branchName, existingPath string) (string, error) {
	mode := runruntime.NormalizeRunWorktreeMode(rawMode)
	if strings.TrimSpace(rawMode) != "" && mode == "" {
		return "", fmt.Errorf("unsupported worktree_mode %q", strings.TrimSpace(rawMode))
	}
	switch mode {
	case runruntime.RunWorktreeModeOff:
		return "", errors.New("worktree_mode off is not supported; Swarm sessions require managed worktree isolation")
	case "", runruntime.RunWorktreeModeInherit:
		if useCurrentBranch != nil || strings.TrimSpace(baseBranch) != "" || strings.TrimSpace(branchName) != "" || strings.TrimSpace(existingPath) != "" {
			return "", errors.New("worktree fields are only allowed when worktree_mode is on")
		}
		return mode, nil
	case runruntime.RunWorktreeModeOn:
		if strings.TrimSpace(branchName) == "" {
			return "", errors.New("worktree_branch_name is required when worktree_mode is on")
		}
		if strings.TrimSpace(existingPath) != "" {
			if useCurrentBranch != nil || strings.TrimSpace(baseBranch) != "" {
				return "", errors.New("worktree base fields are not allowed when reusing an existing worktree")
			}
			return mode, nil
		}
		if useCurrentBranch != nil {
			if *useCurrentBranch && strings.TrimSpace(baseBranch) != "" {
				return "", errors.New("worktree_use_current_branch cannot be true when worktree_base_branch is set")
			}
			if !*useCurrentBranch && strings.TrimSpace(baseBranch) == "" {
				return "", errors.New("worktree_base_branch is required when worktree_use_current_branch is false")
			}
		}
		return mode, nil
	default:
		return "", fmt.Errorf("unsupported worktree_mode %q", strings.TrimSpace(rawMode))
	}
}

func (s *Server) handleSessionsV3CreateReplay(w http.ResponseWriter, principal identity.Principal, sessionID, clientRequestID, payloadHash string, session pebblestore.SessionSnapshot) bool {
	if s == nil || s.sessions == nil || s.sessions.Store() == nil {
		return false
	}
	if _, ok, err := s.sessions.Store().GetV3SessionOperationIdempotencyRecord(principal.AccountScopeID, sessionID, sessionruntime.SessionMutationCreateSession, clientRequestID); err != nil || !ok {
		return false
	}
	result, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{
		SessionID:       sessionID,
		UserID:          principal.UserID,
		AccountScopeID:  principal.AccountScopeID,
		ClientRequestID: clientRequestID,
		IdempotencyKey:  clientRequestID,
		PayloadHash:     payloadHash,
		RequestHash:     payloadHash,
		Kind:            sessionruntime.SessionMutationCreateSession,
		Session:         &session,
		NowUnixMs:       time.Now().UnixMilli(),
	})
	if err != nil {
		if errors.Is(err, sessionruntime.ErrSessionIdempotencyConflict) {
			writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": err.Error(), "conflict": result.Conflict, "result": result})
			return true
		}
		writeError(w, http.StatusBadRequest, err)
		return true
	}
	response, err := sessionV3CreateResultResponse(result)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return true
	}
	writeJSON(w, http.StatusOK, response)
	return true
}

func (s *Server) resolveSessionsV3CreateWorktree(principal identity.Principal, workspacePath, sessionID string, requestedUseCurrentBranch *bool, requestedBaseBranch, requestedBranchName, requestedExistingPath string) (worktreeruntime.Allocation, error) {
	if strings.TrimSpace(requestedExistingPath) != "" {
		return s.reuseSessionsV3CreateWorktree(principal, workspacePath, requestedBranchName, requestedExistingPath)
	}
	return s.allocateSessionsV3CreateWorktree(principal, workspacePath, sessionID, requestedUseCurrentBranch, requestedBaseBranch, requestedBranchName)
}

func (s *Server) allocateSessionsV3CreateWorktree(principal identity.Principal, workspacePath, sessionID string, requestedUseCurrentBranch *bool, requestedBaseBranch, requestedBranchName string) (worktreeruntime.Allocation, error) {
	if s == nil || s.worktrees == nil {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on requires worktree service")
	}
	baseBranch := strings.TrimSpace(requestedBaseBranch)
	if requestedUseCurrentBranch != nil && *requestedUseCurrentBranch {
		baseBranch = ""
	}
	allocation, err := s.worktrees.AllocateDetachedWorkspaceRequestedForPrincipal(principal, workspacePath, sessionID, baseBranch, strings.TrimSpace(requestedBranchName))
	if err != nil {
		return worktreeruntime.Allocation{}, fmt.Errorf("realize sessions v3 worktree: %w", err)
	}
	if strings.TrimSpace(allocation.WorkspacePath) == "" || strings.TrimSpace(allocation.BranchName) == "" || strings.TrimSpace(allocation.WorkspaceID) == "" {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on did not resolve complete worktree facts")
	}
	expectedWorkspaceID, err := worktreeruntime.WorkspaceIdentityForRequestedBranch(strings.TrimSpace(requestedBranchName))
	if err != nil {
		return worktreeruntime.Allocation{}, err
	}
	if workspaceID := strings.TrimSpace(allocation.WorkspaceID); workspaceID != expectedWorkspaceID {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on allocation workspace identity mismatch")
	}
	return allocation, nil
}

func (s *Server) reuseSessionsV3CreateWorktree(principal identity.Principal, workspacePath, requestedBranchName, requestedExistingPath string) (worktreeruntime.Allocation, error) {
	if s == nil || s.worktrees == nil {
		return worktreeruntime.Allocation{}, errors.New("worktree_mode on requires worktree service")
	}
	branchName := strings.TrimSpace(requestedBranchName)
	if branchName == "" {
		return worktreeruntime.Allocation{}, errors.New("worktree_branch_name is required when reusing an existing worktree")
	}
	existingPath := filepath.Clean(strings.TrimSpace(requestedExistingPath))
	if existingPath == "." || existingPath == "" {
		return worktreeruntime.Allocation{}, errors.New("worktree_existing_path is required when reusing an existing worktree")
	}
	managed, err := s.worktrees.ListManagedForPrincipal(principal, workspacePath)
	if err != nil {
		return worktreeruntime.Allocation{}, fmt.Errorf("list managed worktrees: %w", err)
	}
	for _, entry := range managed {
		entryPath := filepath.Clean(strings.TrimSpace(entry.Path))
		if entryPath == "." || entryPath == "" || entryPath != existingPath {
			continue
		}
		if !entry.Managed || !entry.Exists {
			return worktreeruntime.Allocation{}, errors.New("selected worktree is not an existing managed worktree")
		}
		if entry.Detached {
			return worktreeruntime.Allocation{}, errors.New("selected worktree is detached and cannot be reused by branch")
		}
		if strings.TrimSpace(entry.Branch) != branchName {
			return worktreeruntime.Allocation{}, errors.New("selected worktree branch does not match requested worktree_branch_name")
		}
		workspaceID := strings.TrimSpace(entry.WorkspaceID)
		if workspaceID == "" {
			var workspaceIDErr error
			workspaceID, workspaceIDErr = worktreeruntime.WorkspaceIdentityForRequestedBranch(branchName)
			if workspaceIDErr != nil {
				return worktreeruntime.Allocation{}, workspaceIDErr
			}
		}
		return worktreeruntime.Allocation{
			WorkspacePath: entryPath,
			BaseBranch:    "",
			BranchName:    branchName,
			WorkspaceID:   workspaceID,
		}, nil
	}
	return worktreeruntime.Allocation{}, errors.New("selected worktree was not found in managed worktrees")
}

func sessionsV3CreatePayloadHash(sessionID string, req sessionsV3CreateRequest, workspacePath, workspaceName, title string, metadata map[string]any) (string, error) {
	canonical := struct {
		Operation                string                        `json:"operation"`
		SessionID                string                        `json:"session_id"`
		Title                    string                        `json:"title"`
		WorkspacePath            string                        `json:"workspace_path"`
		WorkspaceName            string                        `json:"workspace_name"`
		WorkspaceBindingID       string                        `json:"workspace_binding_id"`
		SwarmID                  string                        `json:"swarm_id"`
		Mode                     string                        `json:"mode"`
		AgentName                string                        `json:"agent_name,omitempty"`
		Preference               pebblestore.ModelPreference   `json:"preference"`
		WorktreeMode             string                        `json:"worktree_mode,omitempty"`
		WorktreeUseCurrentBranch *bool                         `json:"worktree_use_current_branch,omitempty"`
		WorktreeBaseBranch       string                        `json:"worktree_base_branch,omitempty"`
		WorktreeBranchName       string                        `json:"worktree_branch_name,omitempty"`
		WorktreeExistingPath     string                        `json:"worktree_existing_path,omitempty"`
		Metadata                 map[string]any                `json:"metadata,omitempty"`
		ModelProfile             *sessionsV3ModelProfileChoice `json:"model_profile,omitempty"`
	}{
		Operation:                sessionruntime.SessionMutationCreateSession,
		SessionID:                strings.TrimSpace(sessionID),
		Title:                    title,
		WorkspacePath:            strings.TrimSpace(workspacePath),
		WorkspaceName:            workspaceName,
		WorkspaceBindingID:       strings.TrimSpace(req.WorkspaceBindingID),
		SwarmID:                  strings.TrimSpace(req.SwarmID),
		Mode:                     sessionruntime.NormalizeMode(req.Mode),
		AgentName:                strings.TrimSpace(req.AgentName),
		Preference:               normalizeSessionsV3ModelPreference(req.Preference),
		WorktreeMode:             runruntime.NormalizeRunWorktreeMode(req.WorktreeMode),
		WorktreeUseCurrentBranch: req.WorktreeUseCurrentBranch,
		WorktreeBaseBranch:       strings.TrimSpace(req.WorktreeBaseBranch),
		WorktreeBranchName:       strings.TrimSpace(req.WorktreeBranchName),
		WorktreeExistingPath:     strings.TrimSpace(req.WorktreeExistingPath),
		Metadata:                 cloneSessionsV3Metadata(metadata),
		ModelProfile:             req.ModelProfile,
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 create payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func sessionsV3UpdatePayloadHash(sessionID, operation string, payload map[string]any) (string, error) {
	canonical := map[string]any{
		"operation":  strings.TrimSpace(operation),
		"session_id": strings.TrimSpace(sessionID),
		"payload":    cloneSessionsV3Metadata(payload),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 update payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func mergeSessionsV3PreferenceUpdate(current pebblestore.ModelPreference, req sessionsV3PreferenceRequest) pebblestore.ModelPreference {
	next := normalizeSessionsV3ModelPreference(current)
	if req.Provider != nil {
		next.Provider = strings.ToLower(strings.TrimSpace(*req.Provider))
	}
	if req.Model != nil {
		next.Model = strings.TrimSpace(*req.Model)
	}
	if req.Thinking != nil {
		next.Thinking = strings.ToLower(strings.TrimSpace(*req.Thinking))
	}
	if req.ServiceTier != nil {
		next.ServiceTier = strings.TrimSpace(*req.ServiceTier)
	}
	if req.ContextMode != nil {
		next.ContextMode = strings.TrimSpace(*req.ContextMode)
	}
	return normalizeSessionsV3ModelPreference(next)
}

func sessionsV3MessagePayloadHash(sessionID string, req sessionsV3MessageRequest, message pebblestore.MessageSnapshot, runStatus, blockedReason string) (string, error) {
	canonical := struct {
		Operation          string                                          `json:"operation"`
		SessionID          string                                          `json:"session_id"`
		MessageID          string                                          `json:"message_id,omitempty"`
		RunID              string                                          `json:"run_id,omitempty"`
		Role               string                                          `json:"role"`
		Content            string                                          `json:"content"`
		Metadata           map[string]any                                  `json:"metadata,omitempty"`
		Media              []pebblestore.SessionMediaReference             `json:"media,omitempty"`
		VideoAttachments   []pebblestore.SessionVideoAttachmentReference   `json:"video_attachments,omitempty"`
		ArtifactSelections []pebblestore.SessionArtifactSelectionReference `json:"artifact_selections,omitempty"`
		RunStatus          string                                          `json:"run_status"`
		BlockedReason      string                                          `json:"blocked_reason"`
		AuthorityStatus    string                                          `json:"authority_status"`
		DispatchAuthority  map[string]any                                  `json:"dispatch_authority,omitempty"`
	}{
		Operation:          sessionruntime.SessionMutationAppendMessage,
		SessionID:          strings.TrimSpace(sessionID),
		MessageID:          strings.TrimSpace(message.ID),
		RunID:              strings.TrimSpace(req.RunID),
		Role:               strings.TrimSpace(message.Role),
		Content:            message.Content,
		Metadata:           cloneSessionsV3Metadata(message.Metadata),
		Media:              append([]pebblestore.SessionMediaReference(nil), message.Media...),
		VideoAttachments:   append([]pebblestore.SessionVideoAttachmentReference(nil), message.VideoAttachments...),
		ArtifactSelections: append([]pebblestore.SessionArtifactSelectionReference(nil), message.ArtifactSelections...),
		RunStatus:          runStatus,
		BlockedReason:      blockedReason,
		AuthorityStatus:    sessionsV3PrimaryAuthorityStatus(req),
		DispatchAuthority:  cloneSessionsV3Metadata(req.DispatchAuthority),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal sessions v3 message payload hash: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func stableSessionsV3PrimarySessionID(principal identity.Principal, clientRequestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(principal.AccountScopeID) + "\x00" + strings.TrimSpace(clientRequestID)))
	return hex.EncodeToString(sum[:16])
}

func stableSessionsV3PrimaryRunID(sessionID, clientRequestID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(sessionID) + "\x00" + strings.TrimSpace(clientRequestID)))
	return "v3run_" + hex.EncodeToString(sum[:16])
}

func normalizeSessionsV3ModelPreference(pref pebblestore.ModelPreference) pebblestore.ModelPreference {
	pref.Provider = strings.TrimSpace(pref.Provider)
	pref.Model = strings.TrimSpace(pref.Model)
	pref.Thinking = strings.TrimSpace(pref.Thinking)
	pref.ServiceTier = strings.TrimSpace(pref.ServiceTier)
	pref.ContextMode = strings.TrimSpace(pref.ContextMode)
	pref.AccountScopeID = ""
	pref.UserID = ""
	pref.UpdatedAt = 0
	return pref
}

func validateSessionsV3CreateMetadata(metadata map[string]any) error {
	for key := range metadata {
		if isProtectedSessionsV3MetadataKey(key) {
			return fmt.Errorf("metadata key %q is reserved for primary authority state", key)
		}
	}
	return nil
}

func mergeSessionsV3MetadataUpdate(current map[string]any, requested map[string]any) map[string]any {
	metadata := cloneSessionsV3Metadata(current)
	if metadata == nil {
		metadata = make(map[string]any)
	}
	for key := range metadata {
		if !isProtectedSessionsV3MetadataKey(key) {
			delete(metadata, key)
		}
	}
	for key, value := range requested {
		if isProtectedSessionsV3MetadataKey(key) {
			continue
		}
		metadata[key] = cloneSessionsV3MetadataValue(value)
	}
	return metadata
}

type sessionsV3ResolvedAgentIdentity struct {
	Name                string
	ResolvedName        string
	Mode                string
	RuntimeMode         string
	ExitPlanModeEnabled bool
	ToolContractPreset  string
	Profile             pebblestore.AgentProfile
}

type sessionsV3StoredAgentToolContractCompiler interface {
	CompileStoredV3AgentToolContract(accountScopeID string, profile pebblestore.AgentProfile) (runruntime.ResolvedAgentToolContract, map[string]bool, error)
}

func (s *Server) resolveSessionsV3PrimaryCreateAgent(principal identity.Principal, requestedName string) (sessionsV3ResolvedAgentIdentity, error) {
	requestedName = strings.TrimSpace(requestedName)
	if requestedName == "" {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("agent_name is required")
	}
	if s == nil || s.agents == nil {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("agent service is not configured")
	}
	profile, ok, err := s.agents.GetProfileForAccount(principal.AccountScopeID, requestedName)
	if err != nil {
		return sessionsV3ResolvedAgentIdentity{}, err
	}
	if !ok {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q not found", requestedName)
	}
	name := strings.TrimSpace(profile.Name)
	if name == "" {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing name", requestedName)
	}
	mode := strings.TrimSpace(profile.Mode)
	if mode == "" {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing mode", name)
	}
	if !profile.Enabled {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q is disabled", name)
	}
	if profile.ToolContract == nil {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q tool_contract is not configured", name)
	}
	runtimeMode := strings.TrimSpace(profile.RuntimeMode)
	if runtimeMode == "" {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing runtime_mode", name)
	}
	if profile.ExitPlanModeEnabled == nil {
		return sessionsV3ResolvedAgentIdentity{}, fmt.Errorf("agent %q saved profile is missing exit_plan_mode_enabled", name)
	}
	compiler, ok := s.runner.(sessionsV3StoredAgentToolContractCompiler)
	if !ok || compiler == nil {
		return sessionsV3ResolvedAgentIdentity{}, errors.New("v3 tool contract compiler is not configured")
	}
	profile = cloneSessionsV3AgentProfile(profile)
	if _, _, err := compiler.CompileStoredV3AgentToolContract(principal.AccountScopeID, profile); err != nil {
		return sessionsV3ResolvedAgentIdentity{}, err
	}
	return sessionsV3ResolvedAgentIdentity{
		Name:                name,
		ResolvedName:        name,
		Mode:                mode,
		RuntimeMode:         runtimeMode,
		ExitPlanModeEnabled: *profile.ExitPlanModeEnabled,
		ToolContractPreset:  strings.TrimSpace(profile.ToolContract.Preset),
		Profile:             profile,
	}, nil
}

func (s *Server) sessionsV3InitialWorkspaceGrants(principal identity.Principal, binding sessionsV3PrimaryBinding) ([]pebblestore.WorkspaceGrant, error) {
	available := true
	primaryID := strings.TrimSpace(binding.SourceWorkspaceID)
	grants := []pebblestore.WorkspaceGrant{{
		Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: primaryID,
		WorkspaceGeneration: binding.SourceWorkspaceGeneration, Path: strings.TrimSpace(binding.SourceWorkspacePath),
		Name: strings.TrimSpace(binding.SourceWorkspaceName), Available: &available,
	}}
	if s == nil || s.workspace == nil {
		return grants, nil
	}
	entries, err := s.workspace.ListKnownForPrincipal(principal, 2000)
	if err != nil {
		return nil, fmt.Errorf("list account workspaces for session scope: %w", err)
	}
	for _, entry := range entries {
		workspaceID := strings.TrimSpace(entry.WorkspaceID)
		path := strings.TrimSpace(entry.Path)
		if workspaceID == "" || workspaceID == primaryID || path == "" || entry.WorkspaceGeneration <= 0 || !strings.EqualFold(strings.TrimSpace(entry.State), "active") {
			continue
		}
		grants = append(grants, pebblestore.WorkspaceGrant{
			Kind: pebblestore.WorkspaceGrantAdditional, WorkspaceID: workspaceID,
			WorkspaceGeneration: entry.WorkspaceGeneration, Path: path,
			Name: strings.TrimSpace(entry.WorkspaceName), Available: &available,
		})
	}
	return pebblestore.NormalizeSessionWorkspaceGrants(pebblestore.SessionSnapshot{WorkspaceGrants: grants}), nil
}

func sessionsV3CreateServerMetadata(clientMetadata map[string]any, agent sessionsV3ResolvedAgentIdentity, binding sessionsV3PrimaryBinding) map[string]any {
	metadata := cloneSessionsV3Metadata(clientMetadata)
	if metadata == nil {
		metadata = make(map[string]any, 24)
	}
	metadata["agent_name"] = agent.Name
	metadata["resolved_agent_name"] = agent.ResolvedName
	metadata["agent_mode"] = agent.Mode
	metadata["runtime_mode"] = agent.RuntimeMode
	metadata["default_session_mode"] = pebblestore.AgentProfileDefaultSessionMode(agent.Profile)
	metadata["exit_plan_mode_enabled"] = agent.ExitPlanModeEnabled
	metadata["agent_profile"] = cloneSessionsV3AgentProfile(agent.Profile)
	metadata["swarm_v3_execution_class"] = "primary"
	metadata["swarm_v3_runtime_swarm_id"] = binding.RuntimeSwarmID
	metadata["swarm_v3_runtime_kind"] = pebblestore.TopologyRuntimeKindHost
	metadata["swarm_v3_authority_host_swarm_id"] = binding.RuntimeSwarmID
	metadata["swarm_v3_workspace_binding_id"] = binding.WorkspaceBindingID
	metadata["swarm_v3_source_workspace_id"] = binding.SourceWorkspaceID
	metadata["swarm_v3_source_workspace_generation"] = fmt.Sprintf("%d", binding.SourceWorkspaceGeneration)
	metadata["swarm_v3_source_workspace_name"] = binding.SourceWorkspaceName
	metadata["swarm_v3_source_workspace_path"] = binding.SourceWorkspacePath
	metadata["swarm_v3_runtime_workspace_path"] = binding.RuntimeWorkspacePath
	metadata["swarm_v3_placement_generation"] = binding.PlacementGeneration
	metadata["swarm_v3_binding_generation"] = binding.BindingGeneration
	metadata["local_workspace_binding_id"] = binding.WorkspaceBindingID
	delete(metadata, "managed_worktree_requested")
	if agent.ToolContractPreset != "" {
		metadata["tool_contract_preset"] = agent.ToolContractPreset
	}
	return metadata
}

func sessionsV3AgentSwitchMetadata(current map[string]any, agent sessionsV3ResolvedAgentIdentity) map[string]any {
	metadata := cloneSessionsV3Metadata(current)
	if metadata == nil {
		metadata = make(map[string]any, 8)
	}
	for _, key := range []string{"agent_name", "agent_profile", "resolved_agent_name", "agent_mode", "runtime_mode", "default_session_mode", "exit_plan_mode_enabled", "tool_contract_preset", "subagent", "requested_subagent"} {
		delete(metadata, key)
	}
	metadata["agent_name"] = agent.Name
	metadata["resolved_agent_name"] = agent.ResolvedName
	metadata["agent_mode"] = agent.Mode
	metadata["runtime_mode"] = agent.RuntimeMode
	metadata["default_session_mode"] = pebblestore.AgentProfileDefaultSessionMode(agent.Profile)
	metadata["exit_plan_mode_enabled"] = agent.ExitPlanModeEnabled
	metadata["agent_profile"] = cloneSessionsV3AgentProfile(agent.Profile)
	if agent.ToolContractPreset != "" {
		metadata["tool_contract_preset"] = agent.ToolContractPreset
	}
	return metadata
}

func sessionsV3PrimaryAuthorityStatus(req sessionsV3MessageRequest) string {
	if len(req.DispatchAuthority) > 0 {
		return "invalid"
	}
	return "absent"
}

func (s *Server) sessionsV3PrimaryRunIntentStatus(principal identity.Principal, session pebblestore.SessionSnapshot, req sessionsV3MessageRequest) (string, string) {
	if reason := s.sessionsV3PrimaryDispatchBlockedReason(principal, session, req); reason != "" {
		return sessionruntime.RunIntentDispatchBlocked, reason
	}
	return sessionruntime.RunIntentPendingExecutor, ""
}

func (s *Server) sessionsV3PrimaryDispatchBlockedReason(principal identity.Principal, session pebblestore.SessionSnapshot, req sessionsV3MessageRequest) string {
	authority := req.DispatchAuthority
	if len(authority) == 0 {
		return ""
	}
	if strings.TrimSpace(session.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	accountScopeID := sessionsV3AuthorityString(authority, "account_scope_id")
	if accountScopeID != "" && accountScopeID != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	runtimeSwarmID := sessionsV3AuthorityString(authority, "runtime_swarm_id", "swarm_id", "target_swarm_id")
	if runtimeSwarmID == "" {
		return "dispatch authority missing executor runtime"
	}
	runtimeKind := sessionsV3AuthorityString(authority, "runtime_kind", "target_kind")
	workspaceBindingID := sessionsV3AuthorityString(authority, "workspace_binding_id", "local_workspace_binding_id")
	placementGeneration := sessionsV3AuthorityInt(authority, "placement_generation")
	bindingGeneration := sessionsV3AuthorityInt(authority, "binding_generation")
	authorityHostSwarmID := sessionsV3AuthorityString(authority, "authority_host_swarm_id", "host_swarm_id")
	authorityContainerID := sessionsV3AuthorityString(authority, "authority_container_id", "host_container_id", "container_id")
	sourceWorkspacePath := sessionsV3AuthorityString(authority, "source_workspace_path", "workspace_path", "host_workspace_path")
	runtimeWorkspacePath := sessionsV3AuthorityString(authority, "runtime_workspace_path", "destination_workspace_path")

	if s == nil || s.topology == nil {
		return "dispatch authority unavailable: topology is not configured"
	}
	placement, placementOK, err := s.topology.GetRuntimePlacementForAccount(principal.AccountScopeID, runtimeSwarmID)
	if err != nil {
		return "dispatch authority unavailable: " + err.Error()
	}
	if !placementOK {
		return "dispatch authority unavailable: runtime placement not found"
	}
	if strings.TrimSpace(placement.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	if strings.TrimSpace(placement.State) != pebblestore.TopologyRuntimePlacementStateActive {
		return "dispatch authority stale: runtime placement is not active"
	}
	if placementGeneration > 0 && placement.PlacementGeneration != placementGeneration {
		return "dispatch authority stale: runtime placement generation mismatch"
	}
	if runtimeKind != "" && strings.TrimSpace(placement.RuntimeKind) != runtimeKind {
		return "dispatch authority runtime kind mismatch"
	}
	if authorityHostSwarmID != "" && strings.TrimSpace(placement.AuthorityHostSwarmID) != authorityHostSwarmID {
		return "dispatch authority placement authority host mismatch"
	}
	if authorityContainerID != "" && strings.TrimSpace(placement.AuthorityContainerID) != authorityContainerID {
		return "dispatch authority placement container mismatch"
	}
	if workspaceBindingID == "" {
		return "dispatch authority missing workspace binding"
	}
	binding, bindingOK, err := s.topology.GetWorkspaceBindingForAccount(principal.AccountScopeID, workspaceBindingID)
	if err != nil {
		return "dispatch authority unavailable: " + err.Error()
	}
	if !bindingOK {
		return "dispatch authority unavailable: workspace binding not found"
	}
	if strings.TrimSpace(binding.AccountScopeID) != strings.TrimSpace(principal.AccountScopeID) {
		return "dispatch authority account mismatch"
	}
	if strings.TrimSpace(binding.State) != pebblestore.TopologyWorkspaceBindingStateBound {
		return "dispatch authority stale: workspace binding is not bound"
	}
	if placementGeneration > 0 && binding.PlacementGeneration != placementGeneration {
		return "dispatch authority stale: workspace binding placement generation mismatch"
	}
	if bindingGeneration > 0 && binding.BindingGeneration != bindingGeneration {
		return "dispatch authority stale: workspace binding generation mismatch"
	}
	if strings.TrimSpace(binding.DestinationRuntimeSwarmID) != runtimeSwarmID {
		return "dispatch authority workspace binding runtime mismatch"
	}
	if authorityHostSwarmID != "" && strings.TrimSpace(binding.DestinationAuthorityHostSwarmID) != authorityHostSwarmID {
		return "dispatch authority workspace binding authority host mismatch"
	}
	if runtimeKind != "" && strings.TrimSpace(binding.DestinationRuntimeKind) != runtimeKind {
		return "dispatch authority workspace binding runtime kind mismatch"
	}
	if authorityContainerID != "" && strings.TrimSpace(binding.DestinationContainerID) != authorityContainerID {
		return "dispatch authority workspace binding container mismatch"
	}
	if sourceWorkspacePath != "" && filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) != filepath.Clean(sourceWorkspacePath) {
		return "dispatch authority source workspace path mismatch"
	}
	if strings.TrimSpace(session.WorkspacePath) != "" && strings.TrimSpace(binding.SourceWorkspacePath) != "" && filepath.Clean(strings.TrimSpace(session.WorkspacePath)) != filepath.Clean(strings.TrimSpace(binding.SourceWorkspacePath)) {
		return "dispatch authority session workspace path mismatch"
	}
	if runtimeWorkspacePath != "" && filepath.Clean(strings.TrimSpace(binding.DestinationWorkspacePath)) != filepath.Clean(runtimeWorkspacePath) {
		return "dispatch authority runtime workspace path mismatch"
	}
	return ""
}

func sessionsV3AuthorityString(authority map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := authority[key]
		if !ok {
			value, ok = authority[strings.ToLower(key)]
		}
		if !ok {
			continue
		}
		s := strings.TrimSpace(fmt.Sprint(value))
		if s != "" {
			return s
		}
	}
	return ""
}

func sessionsV3AuthorityInt(authority map[string]any, keys ...string) int {
	raw := sessionsV3AuthorityString(authority, keys...)
	if raw == "" {
		return 0
	}
	var parsed int
	if _, err := fmt.Sscanf(raw, "%d", &parsed); err != nil || parsed < 0 {
		return 0
	}
	return parsed
}

func isProtectedSessionsV3MetadataKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "agent_name",
		"agent_profile",
		"model_profile",
		"resolved_agent_name",
		"agent_mode",
		"runtime_mode",
		"exit_plan_mode_enabled",
		"tool_contract_preset",
		"workspace_binding_id",
		"local_workspace_binding_id",
		"source_workspace_id",
		"source_workspace_path",
		"runtime_workspace_path",
		"runtime_swarm_id",
		"runtime_kind",
		"authority_host_swarm_id",
		"authority_container_id",
		"target_swarm_id",
		"target_kind",
		"target_name",
		"swarm_target_swarm_id",
		"route",
		"routes",
		"topology",
		"swarm_v3_execution_class",
		"swarm_v3_runtime_swarm_id",
		"swarm_v3_runtime_kind",
		"swarm_v3_authority_host_swarm_id",
		"swarm_v3_authority_container_id",
		"swarm_v3_workspace_binding_id",
		"swarm_v3_source_workspace_id",
		"swarm_v3_source_workspace_generation",
		"swarm_v3_source_workspace_name",
		"swarm_v3_source_workspace_path",
		"swarm_v3_runtime_workspace_path",
		"swarm_v3_placement_generation",
		"swarm_v3_binding_generation",
		"swarm_v3_tui_directory_session",
		"swarm_v3_tui_cwd_path",
		"swarm_v3_tui_original_cwd_path",
		"base_commit":
		return true
	default:
		return false
	}
}

func sessionV3AgentProfileFromMetadata(metadata map[string]any) (pebblestore.AgentProfile, error) {
	raw, ok := metadata["agent_profile"]
	if !ok || raw == nil {
		return pebblestore.AgentProfile{}, errors.New("v3 session is missing stored agent profile")
	}
	switch typed := raw.(type) {
	case pebblestore.AgentProfile:
		return cloneSessionsV3AgentProfile(typed), nil
	case map[string]any:
		var profile pebblestore.AgentProfile
		encoded, err := json.Marshal(typed)
		if err != nil {
			return pebblestore.AgentProfile{}, err
		}
		if err := json.Unmarshal(encoded, &profile); err != nil {
			return pebblestore.AgentProfile{}, err
		}
		return cloneSessionsV3AgentProfile(profile), nil
	default:
		encoded, err := json.Marshal(raw)
		if err != nil {
			return pebblestore.AgentProfile{}, err
		}
		var profile pebblestore.AgentProfile
		if err := json.Unmarshal(encoded, &profile); err != nil {
			return pebblestore.AgentProfile{}, err
		}
		return cloneSessionsV3AgentProfile(profile), nil
	}
}

func cloneSessionsV3AgentProfile(profile pebblestore.AgentProfile) pebblestore.AgentProfile {
	profile.ExitPlanModeEnabled = pebblestore.CloneBoolPtr(profile.ExitPlanModeEnabled)
	profile.ToolScope = pebblestore.CloneAgentToolScope(profile.ToolScope)
	profile.ToolContract = pebblestore.CloneAgentToolContract(profile.ToolContract)
	return profile
}

func cloneSessionsV3Metadata(in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = cloneSessionsV3MetadataValue(value)
	}
	return out
}

func cloneSessionsV3MetadataValue(value any) any {
	switch typed := value.(type) {
	case pebblestore.AgentProfile:
		return cloneSessionsV3AgentProfile(typed)
	case pebblestore.SessionModelProfileSnapshot:
		return cloneSessionsV3ModelProfileSnapshot(typed)
	case *pebblestore.SessionModelProfileSnapshot:
		if typed == nil {
			return (*pebblestore.SessionModelProfileSnapshot)(nil)
		}
		cloned := cloneSessionsV3ModelProfileSnapshot(*typed)
		return &cloned
	case map[string]any:
		return cloneSessionsV3Metadata(typed)
	case []any:
		out := make([]any, len(typed))
		for i, child := range typed {
			out[i] = cloneSessionsV3MetadataValue(child)
		}
		return out
	default:
		return value
	}
}

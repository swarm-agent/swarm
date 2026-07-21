package run

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// AITaskPreparation is Compact's complete metadata-only response. Prompt,
// execution mode, and managed-worktree policy remain server-owned.
type AITaskPreparation struct {
	Title        string `json:"title"`
	WorktreeName string `json:"worktree_name"`
}

func ParseAITaskPreparation(raw string) (AITaskPreparation, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &fields); err != nil {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation must be one JSON object: %w", err)
	}
	for key := range fields {
		switch key {
		case "title", "worktree_name":
		default:
			return AITaskPreparation{}, fmt.Errorf("AI task preparation rejects field %q", key)
		}
	}
	if len(fields) != 2 {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation requires title and worktree_name")
	}
	var out AITaskPreparation
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &out); err != nil {
		return AITaskPreparation{}, err
	}
	out.Title = strings.TrimSpace(out.Title)
	rawWorktreeName := strings.TrimSpace(out.WorktreeName)
	if out.Title == "" || rawWorktreeName == "" {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation title and worktree_name are required")
	}
	out.WorktreeName = canonicalDeployWorktreeBranchSuffix(rawWorktreeName, "")
	if out.WorktreeName == "" {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation worktree_name must contain letters or digits")
	}
	if len([]rune(out.Title)) > 120 {
		return AITaskPreparation{}, fmt.Errorf("AI task preparation title is too long")
	}
	return out, nil
}

// ExecutePreparedAITask creates and starts the managed session through the
// canonical V3 deployment contract. V2 supplies the final preparation directly;
// there is no hidden provider/preparer run before the visible session exists.
func (s *Service) ExecutePreparedAITask(ctx context.Context, parentSessionID, userID, accountScopeID, workspacePath, taskID, originalRequest, mode string, preparation AITaskPreparation, apply func(sessionruntime.SessionMutationInput) (sessionruntime.SessionMutationResult, error)) (string, error) {
	if s == nil || s.aiTaskBinder == nil {
		return "", fmt.Errorf("AI task binder is not configured")
	}
	workspacePath, taskID = strings.TrimSpace(workspacePath), strings.TrimSpace(taskID)
	mode = strings.ToLower(strings.TrimSpace(mode))
	if workspacePath == "" || taskID == "" || strings.TrimSpace(originalRequest) == "" {
		return "", fmt.Errorf("AI task workspace, id, and original request are required")
	}
	if mode != sessionruntime.ModePlan && mode != sessionruntime.ModeAuto {
		return "", fmt.Errorf("AI task mode must be plan or auto")
	}
	if _, err := ParseAITaskPreparation(marshalAITaskPreparation(preparation)); err != nil {
		return "", err
	}
	// Queued AI tasks always deploy through the managed-worktree branch of the
	// canonical session deploy path. The manifest builder resolves the user's
	// worktree base branch and branch family for both plan and auto modes.
	arguments, _ := json.Marshal(map[string]any{"action": "deploy", "proposals": []map[string]any{{"title": preparation.Title, "prompt": originalRequest, "mode": mode, "agent": agentruntime.SwarmAgentID, "workspace_path": workspacePath, "worktree": true, "worktree_name": preparation.WorktreeName}}})
	call := tool.Call{Name: "manage_sessions", Arguments: string(arguments)}
	aiTaskBinding := &AITaskDeployBinding{UserID: userID, AccountScopeID: accountScopeID, WorkspacePath: workspacePath, TaskID: taskID, PreparationSessionID: parentSessionID}
	manifest, err := s.buildManageSessionsDeployManifestBound(parentSessionID, call, aiTaskBinding)
	if err != nil {
		return "", err
	}
	approved, _ := json.Marshal(manifest.ApprovedArguments)
	if parentSessionID != "" {
		parent, _, _ := s.sessions.GetSession(parentSessionID)
		aiTaskBinding.PreparationRunID = mapString(parent.Metadata, "ai_task_preparation_run_id")
	}
	result, err := s.executeManageSessionsDeployBound(ctx, parentSessionID, call, string(approved), apply, aiTaskBinding)
	if err != nil {
		return result, err
	}
	var outcome struct {
		Results []manageSessionsDeployResult `json:"results"`
	}
	if json.Unmarshal([]byte(result), &outcome) == nil {
		for _, item := range outcome.Results {
			if item.Status == "error" {
				return result, fmt.Errorf("AI task canonical deployment failed: %s", item.Error)
			}
		}
	}
	return result, nil
}

func marshalAITaskPreparation(value any) string { raw, _ := json.Marshal(value); return string(raw) }

// PrepareAITaskMetadata invokes Compact exactly once with no tools, no session
// state, and no mutation capability. Only the validated metadata object escapes.
func (s *Service) PrepareAITaskMetadata(ctx context.Context, taskID, request string, basePreference pebblestore.ModelPreference, principal identity.Principal) (AITaskPreparation, error) {
	if !principal.Valid() {
		return AITaskPreparation{}, identity.ErrPrincipalRequired
	}
	if s == nil || s.providers == nil {
		return AITaskPreparation{}, fmt.Errorf("AI task Compact provider registry is not configured")
	}
	resolved, profile, err := s.resolveCompactPreference(principal.AccountScopeID, basePreference)
	if err != nil {
		return AITaskPreparation{}, err
	}
	compactModel, err := resolveCompactModelRuntime(s.model, resolved)
	if err != nil {
		return AITaskPreparation{}, fmt.Errorf("resolve AI task Compact model runtime: %w", err)
	}
	providerID := compactModel.ProviderID
	runner, ok := s.providers.GetRunner(providerID)
	if !ok {
		return AITaskPreparation{}, fmt.Errorf("AI task Compact provider %q is not runnable", providerID)
	}
	instructions := strings.TrimSpace(strings.Join([]string{
		profile.Prompt,
		"AI-task metadata-only case. Return exactly one JSON object with only title and worktree_name.",
		"title: a concise user-visible task title, at most 120 characters.",
		"worktree_name: a short lowercase branch seed using letters, digits, and hyphens.",
		"Do not rewrite, summarize, or return an execution prompt. Do not include markdown or explanation.",
	}, "\n"))
	lineage := provideriface.ShortProviderLineageKey("ai_task_metadata", taskID, compactModel.Preference.Model, compactModel.Preference.Thinking, instructions)
	req := provideriface.Request{
		ProviderLineageID: lineage, ProviderCacheKey: providerScopedKey("cache", lineage), SessionAffinityKey: providerScopedKey("affinity", lineage),
		BoundaryReason: "ai_task_metadata", NativeContinuationAllowed: false, ForceFreshProviderContext: true,
		Instructions: instructions,
		Input:        []map[string]any{{"role": "user", "content": []map[string]any{{"type": "input_text", "text": request}}}}, ToolChoice: "none",
	}
	req = compactModel.apply(req)
	trustedCtx := identity.ContextWithPrincipal(ctx, principal)
	callCtx, cancel := context.WithTimeout(trustedCtx, 30*time.Second)
	defer cancel()
	response, err := runCompactProviderCall(callCtx, runner, req, nil)
	if err != nil {
		return AITaskPreparation{}, fmt.Errorf("AI task Compact metadata request: %w", err)
	}
	preparation, err := ParseAITaskPreparation(firstNonEmptyString(response.Text, response.ReasoningSummary))
	if err != nil {
		return AITaskPreparation{}, fmt.Errorf("parse AI task Compact metadata: %w", err)
	}
	return preparation, nil
}

package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const reviewCommitMetadataKey = "review_commit_job"

func (s *Server) startSessionsV3ReviewCommits(ctx context.Context, principal identity.Principal, workspacePath string, requested []string, searchItems []pebblestore.V3SessionSearchItem, now time.Time) (string, error) {
	if s == nil || s.runner == nil || s.agents == nil || s.sessions == nil {
		return "", errors.New("review commit agent is not configured")
	}
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return "", errors.New("workspace_path is required for review commits")
	}
	ids := compactStrings(requested)
	if len(ids) == 0 {
		return "", errors.New("commit_session_ids is required")
	}
	allowed := make(map[string]struct{}, len(searchItems))
	for _, item := range searchItems {
		allowed[item.ID] = struct{}{}
	}
	parent, err := s.resolveSessionsV3ReviewCommitParent(principal.AccountScopeID)
	if err != nil {
		return "", err
	}
	profile, err := s.agents.ResolveSystemAgent(agentruntime.ReviewCommitAgentID, parent)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(profile.Provider) == "" || strings.TrimSpace(profile.Model) == "" {
		return "", errors.New("review commit agent requires a configured auto model")
	}
	batchID := reviewCommitBatchID(principal, workspacePath, ids, now)
	type candidate struct {
		session pebblestore.SessionSnapshot
		path    string
	}
	candidates := make([]candidate, 0, len(ids))
	seenPaths := map[string]string{}
	for _, id := range ids {
		if _, ok := allowed[id]; !ok {
			return "", fmt.Errorf("review commit selection contains unavailable session %s", id)
		}
		session, found, getErr := s.sessions.GetSession(id)
		if getErr != nil || !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
			return "", fmt.Errorf("review commit selection contains unavailable session %s", id)
		}
		path := strings.TrimSpace(session.WorktreeRootPath)
		if !session.WorktreeEnabled || path == "" {
			path = strings.TrimSpace(session.WorkspacePath)
		}
		snapshot, snapshotErr := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{})
		if snapshotErr != nil || !snapshot.HasGit || snapshot.Clean || snapshot.ConflictCount != 0 || snapshot.StagedCount != 0 {
			return "", fmt.Errorf("session %s is not a clean-index, conflict-free commit candidate", id)
		}
		repoKey := gitstatus.NormalizePath(snapshot.RepoRoot)
		if prior, exists := seenPaths[repoKey]; exists {
			return "", fmt.Errorf("sessions %s and %s share one dirty repository; select one attribution before committing", prior, id)
		}
		seenPaths[repoKey] = id
		candidates = append(candidates, candidate{session: session, path: path})
	}
	if !s.claimReviewCommitBatch(workspacePath, batchID) {
		return "", errors.New("a review commit batch is already running for this workspace")
	}
	for _, item := range candidates {
		if err := s.updateSessionsV3ReviewCommitJob(item.session, sessionreview.CommitJob{BatchID: batchID, Status: "pending", UpdatedAt: now.UnixMilli()}); err != nil {
			s.releaseReviewCommitBatch(workspacePath, batchID)
			return "", err
		}
	}
	runCtx := s.runCtx
	if runCtx == nil {
		runCtx = context.Background()
	}
	if !s.beginActiveRun() {
		s.releaseReviewCommitBatch(workspacePath, batchID)
		return "", errors.New("daemon is shutting down")
	}
	go func() {
		defer s.endActiveRun()
		defer s.releaseReviewCommitBatch(workspacePath, batchID)
		var wg sync.WaitGroup
		for _, item := range candidates {
			item := item
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.runSessionsV3ReviewCommit(runCtx, principal, batchID, profile, item.session, item.path)
			}()
		}
		wg.Wait()
	}()
	return batchID, nil
}

func (s *Server) resolveSessionsV3ReviewCommitParent(accountScopeID string) (pebblestore.AgentProfile, error) {
	state, err := s.agents.ListStateForAccount(accountScopeID, 2000)
	if err != nil {
		return pebblestore.AgentProfile{}, err
	}
	for _, profile := range state.Profiles {
		if strings.EqualFold(strings.TrimSpace(profile.Name), agentruntime.SwarmAgentID) && profile.Enabled && pebblestore.AgentProfileRuntimeMode(profile) == pebblestore.AgentRuntimeModePlanAuto {
			return profile, nil
		}
	}
	eligible := make([]pebblestore.AgentProfile, 0, 1)
	for _, profile := range state.Profiles {
		if profile.Enabled && !profile.Protected && !agentruntime.IsReservedSystemAgentName(profile.Name) && (profile.Mode == agentruntime.ModePrimary || profile.Mode == agentruntime.ModeBackground) {
			eligible = append(eligible, profile)
		}
	}
	if len(eligible) != 1 {
		return pebblestore.AgentProfile{}, errors.New("review commits require configured Swarm plan/auto or exactly one enabled agent")
	}
	return eligible[0], nil
}

func (s *Server) runSessionsV3ReviewCommit(ctx context.Context, principal identity.Principal, batchID string, profile pebblestore.AgentProfile, session pebblestore.SessionSnapshot, path string) {
	runSessionID := "review-commit-" + reviewCommitHash(batchID+"\x00"+session.ID)
	now := time.Now().UnixMilli()
	job := sessionreview.CommitJob{BatchID: batchID, Status: "running", RunSessionID: runSessionID, UpdatedAt: now}
	if err := s.updateSessionsV3ReviewCommitJob(session, job); err != nil {
		return
	}
	preference := pebblestore.ModelPreference{Provider: profile.Provider, Model: profile.Model, Thinking: profile.Thinking, ServiceTier: profile.AutoServiceTier}
	metadata := map[string]any{"system_session": true, "navigation_hidden": true, "source": "review_commit", "review_commit_batch_id": batchID, "review_commit_source_session_id": session.ID, "agent_name": profile.Name, "resolved_agent_name": profile.Name}
	runSession := pebblestore.SessionSnapshot{ID: runSessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, WorkspacePath: path, WorkspaceName: session.WorkspaceName, Title: agentruntime.ReviewCommitAgentName, Mode: sessionruntime.ModeAuto, Preference: preference, Metadata: metadata, CreatedAt: now, UpdatedAt: now}
	key := "review-commit:create:" + batchID + ":" + session.ID
	if existing, found, getErr := s.sessions.GetSession(runSessionID); getErr != nil {
		s.finishSessionsV3ReviewCommit(session, job, "", getErr)
		return
	} else if found {
		if existing.AccountScopeID != principal.AccountScopeID || existing.UserID != principal.UserID || reviewCommitString(existing.Metadata["review_commit_batch_id"]) != batchID || reviewCommitString(existing.Metadata["review_commit_source_session_id"]) != session.ID {
			s.finishSessionsV3ReviewCommit(session, job, "", errors.New("review commit run session binding mismatch"))
			return
		}
	} else if _, err := s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: runSessionID, UserID: principal.UserID, AccountScopeID: principal.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: reviewCommitHash(key), RequestHash: reviewCommitHash(key), Kind: sessionruntime.SessionMutationCreateSession, Session: &runSession, NowUnixMs: now}); err != nil {
		s.finishSessionsV3ReviewCommit(session, job, "", err)
		return
	}
	prompt := buildSessionsV3ReviewCommitPrompt(session, path)
	policy := permission.NormalizePolicy(permission.Policy{Version: 1, Rules: []permission.PolicyRule{
		{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "read"},
		{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "git_status"},
		{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "git_diff"},
		{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "git_add"},
		{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "git_commit"},
	}})
	runID := "review-commit-run:" + reviewCommitHash(batchID+":"+session.ID)
	initialSnapshot, _ := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{})
	result, runErr := s.runner.RunTurn(ctx, runSessionID, runruntime.RunRequest{Prompt: prompt, AgentName: profile.Name, Instructions: profile.Prompt, TargetKind: runruntime.RunTargetKindSubagent, TargetName: profile.Name, Background: true, ExecutionContext: &runruntime.RunExecutionContext{WorkspacePath: path, CWD: path, WorktreeMode: runruntime.RunWorktreeModeOff}}, runruntime.RunStartMeta{AllowSubagent: true, TrustedAgentProfile: &profile, RunID: runID, PermissionSessionID: runSessionID, CompiledPolicy: &policy, Principal: principal, ApplySessionMutation: s.applySessionV3PrimaryMutation})
	_ = result
	if runErr != nil {
		s.finishSessionsV3ReviewCommit(session, job, "", runErr)
		return
	}
	snapshot, snapshotErr := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{})
	if snapshotErr != nil || !snapshot.HasGit || !snapshot.Clean || strings.TrimSpace(snapshot.HeadOID) == strings.TrimSpace(initialSnapshot.HeadOID) {
		if snapshotErr == nil {
			snapshotErr = errors.New("agent completed without creating a clean new commit")
		}
		s.finishSessionsV3ReviewCommit(session, job, "", snapshotErr)
		return
	}
	s.finishSessionsV3ReviewCommit(session, job, snapshot.HeadOID, nil)
}

func buildSessionsV3ReviewCommitPrompt(session pebblestore.SessionSnapshot, path string) string {
	return fmt.Sprintf("Create the one review commit for session %q in the bound repository. Repository: %s\nInspect status and the complete working-tree diff first. Include all intended dirty changes in this isolated worktree, choose the commit message yourself, commit exactly once, then verify status is clean.", session.Title, path)
}

func (s *Server) finishSessionsV3ReviewCommit(session pebblestore.SessionSnapshot, job sessionreview.CommitJob, commitHash string, runErr error) {
	job.UpdatedAt = time.Now().UnixMilli()
	if runErr != nil {
		job.Status, job.Error = "failed", strings.TrimSpace(runErr.Error())
		if len(job.Error) > 500 {
			job.Error = job.Error[:500]
		}
	} else {
		job.Status, job.CommitHash = "completed", strings.TrimSpace(commitHash)
	}
	_ = s.updateSessionsV3ReviewCommitJob(session, job)
}

func (s *Server) updateSessionsV3ReviewCommitJob(session pebblestore.SessionSnapshot, job sessionreview.CommitJob) error {
	latest, found, err := s.sessions.GetSession(session.ID)
	if err != nil || !found {
		return errors.New("review commit session is unavailable")
	}
	metadata := cloneStringAnyMap(latest.Metadata)
	metadata[reviewCommitMetadataKey] = map[string]any{"batch_id": job.BatchID, "status": job.Status, "run_session_id": job.RunSessionID, "commit_hash": job.CommitHash, "error": job.Error, "updated_at": job.UpdatedAt}
	next := latest
	next.Metadata, next.UpdatedAt = metadata, time.Now().UnixMilli()
	key := fmt.Sprintf("review-commit-job:%s:%s:%s:%d", job.BatchID, session.ID, job.Status, job.UpdatedAt)
	hash := reviewCommitHash(key)
	_, err = s.applySessionV3PrimaryMutation(sessionruntime.SessionMutationInput{SessionID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID, ClientRequestID: key, IdempotencyKey: key, PayloadHash: hash, RequestHash: hash, Kind: sessionruntime.SessionMutationUpdateMetadata, Session: &next, NowUnixMs: next.UpdatedAt})
	return err
}

func sessionReviewCommitJob(session pebblestore.SessionSnapshot) *sessionreview.CommitJob {
	raw, ok := session.Metadata[reviewCommitMetadataKey].(map[string]any)
	if !ok {
		return nil
	}
	job := &sessionreview.CommitJob{BatchID: reviewCommitString(raw["batch_id"]), Status: reviewCommitString(raw["status"]), RunSessionID: reviewCommitString(raw["run_session_id"]), CommitHash: reviewCommitString(raw["commit_hash"]), Error: reviewCommitString(raw["error"]), UpdatedAt: reviewCommitInt64(raw["updated_at"])}
	if job.BatchID == "" || job.Status == "" {
		return nil
	}
	return job
}

func reviewCommitString(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func reviewCommitInt64(value any) int64 {
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	default:
		return 0
	}
}

func reviewCommitBatchID(principal identity.Principal, workspacePath string, ids []string, now time.Time) string {
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	return "review-commit-batch:" + reviewCommitHash(principal.AccountScopeID+"\x00"+workspacePath+"\x00"+strings.Join(sorted, "\x00")+fmt.Sprint(now.UnixNano()))
}

func reviewCommitHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}

func (s *Server) claimReviewCommitBatch(workspacePath, batchID string) bool {
	s.reviewCommitMu.Lock()
	defer s.reviewCommitMu.Unlock()
	if s.reviewCommitActive == nil {
		s.reviewCommitActive = make(map[string]string)
	}
	if current := s.reviewCommitActive[workspacePath]; current != "" {
		return false
	}
	s.reviewCommitActive[workspacePath] = batchID
	return true
}

func (s *Server) releaseReviewCommitBatch(workspacePath, batchID string) {
	s.reviewCommitMu.Lock()
	defer s.reviewCommitMu.Unlock()
	if s.reviewCommitActive[workspacePath] == batchID {
		delete(s.reviewCommitActive, workspacePath)
	}
}

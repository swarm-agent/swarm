package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const reviewCommitMetadataKey = "review_commit_job"

func (s *Server) startSessionsV3ReviewCommits(ctx context.Context, principal identity.Principal, workspacePath string, requested []string, searchItems []pebblestore.V3SessionSearchItem, now time.Time) (string, error) {
	if s == nil || s.providers == nil || s.model == nil || s.agents == nil || s.agentModelSettings == nil || s.sessions == nil {
		return "", errors.New("review commit AI utility is not configured")
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
				s.runSessionsV3ReviewCommit(runCtx, principal, batchID, item.session, item.path)
			}()
		}
		wg.Wait()
	}()
	return batchID, nil
}

func (s *Server) runSessionsV3ReviewCommit(ctx context.Context, principal identity.Principal, batchID string, session pebblestore.SessionSnapshot, path string) {
	now := time.Now().UnixMilli()
	job := sessionreview.CommitJob{BatchID: batchID, Status: "running", UpdatedAt: now}
	if err := s.updateSessionsV3ReviewCommitJob(session, job); err != nil {
		return
	}
	initialSnapshot, snapshotErr := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{})
	if snapshotErr != nil {
		s.finishSessionsV3ReviewCommit(session, job, "", snapshotErr)
		return
	}
	if err := s.commitSessionsV3ReviewChanges(ctx, principal, path); err != nil {
		s.finishSessionsV3ReviewCommit(session, job, "", err)
		return
	}
	snapshot, snapshotErr := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{})
	if snapshotErr != nil || !snapshot.HasGit || !snapshot.Clean || strings.TrimSpace(snapshot.HeadOID) == strings.TrimSpace(initialSnapshot.HeadOID) {
		if snapshotErr == nil {
			snapshotErr = errors.New("AI utility completed without creating a clean new commit")
		}
		s.finishSessionsV3ReviewCommit(session, job, "", snapshotErr)
		return
	}
	s.finishSessionsV3ReviewCommit(session, job, snapshot.HeadOID, nil)
}

func (s *Server) commitSessionsV3ReviewChanges(ctx context.Context, principal identity.Principal, path string) error {
	changes, err := collectWorkspaceGitSuggestionContext(ctx, path)
	if err != nil {
		return fmt.Errorf("collect review commit changes: %w", err)
	}
	input, err := json.Marshal(changes)
	if err != nil {
		return fmt.Errorf("encode review commit changes: %w", err)
	}
	response, err := s.invokeConfiguredRouterOnce(ctx, principal, workspaceGitCommitSuggestionInstructions(), string(input), maxWorkspaceGitSuggestionOutputBytes)
	if err != nil {
		return fmt.Errorf("generate review commit message: %w", err)
	}
	message, err := decodeConfiguredRouterGitCommitSuggestion(response.Text)
	if err != nil {
		return err
	}
	result, err := runWorkspaceGitCommit(ctx, path, message, true)
	if err != nil {
		return err
	}
	if !result.OK {
		return errors.New("review commit did not complete")
	}
	if s.gitRealtime != nil {
		s.gitRealtime.signal(path)
	}
	return nil
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

package api

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const sessionsV3ReviewWorktreeLimit = 200

type sessionsV3ReviewWorktreesRequest struct {
	WorkspacePath string   `json:"workspace_path,omitempty"`
	ArchiveIDs    []string `json:"archive_session_ids,omitempty"`
	ArchiveAll    bool     `json:"archive_all,omitempty"`
	IntegrateIDs  []string `json:"integrate_session_ids,omitempty"`
	Automatic     bool     `json:"automatic,omitempty"`
	GraceHours    string   `json:"grace_hours,omitempty"`
}

type sessionsV3UnarchiveRequest struct {
	SessionIDs []string         `json:"session_ids"`
	Versions   map[string]int64 `json:"expected_updated_at_by_id"`
}

func (s *Server) handleSessionsV3ReviewWorktrees(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req sessionsV3ReviewWorktreesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	result, err := s.classifySessionsV3ReviewWorktrees(r.Context(), principal, req)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) classifySessionsV3ReviewWorktrees(ctx context.Context, principal identity.Principal, req sessionsV3ReviewWorktreesRequest) (map[string]any, error) {
	search, err := s.sessions.SearchSessions(pebblestore.V3SessionSearchOptions{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Global:         strings.TrimSpace(req.WorkspacePath) == "",
		WorkspacePaths: compactStrings([]string{req.WorkspacePath}),
		State:          "needs_review",
		ArchivedMode:   "exclude",
		Limit:          sessionsV3ReviewWorktreeLimit,
	})
	if err != nil {
		return nil, err
	}
	archivedSearch, err := s.sessions.SearchSessions(pebblestore.V3SessionSearchOptions{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Global:         strings.TrimSpace(req.WorkspacePath) == "",
		WorkspacePaths: compactStrings([]string{req.WorkspacePath}),
		State:          "needs_review",
		ArchivedMode:   "only",
		Limit:          sessionsV3ReviewWorktreeLimit,
	})
	if err != nil {
		return nil, err
	}
	recentlyArchived := make([]map[string]any, 0, len(archivedSearch.Items))
	for _, item := range archivedSearch.Items {
		recentlyArchived = append(recentlyArchived, map[string]any{"session_id": item.ID, "title": item.Title, "updated_at": item.UpdatedAt, "worktree_branch": item.WorktreeBranch, "target_branch": item.WorktreeBaseBranch})
	}
	grace := sessionreview.ParseGraceHours(req.GraceHours)
	now := time.Now()
	if len(compactStrings(req.IntegrateIDs)) > 0 {
		if err := s.integrateSessionsV3ReviewWorktrees(ctx, principal, req.WorkspacePath, req.IntegrateIDs, now); err != nil {
			return nil, err
		}
		search, err = s.sessions.SearchSessions(pebblestore.V3SessionSearchOptions{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID,
			Global: strings.TrimSpace(req.WorkspacePath) == "", WorkspacePaths: compactStrings([]string{req.WorkspacePath}),
			State: "needs_review", ArchivedMode: "exclude", Limit: sessionsV3ReviewWorktreeLimit,
		})
		if err != nil {
			return nil, err
		}
	}
	checkoutSnapshot, checkoutCommonDir := sessionsV3ReviewCheckoutTarget(ctx, req.WorkspacePath)
	checkoutBranch := strings.TrimSpace(checkoutSnapshot.Branch)
	retained := make([]sessionreview.Classification, 0)
	done := make([]sessionreview.Classification, 0)
	byID := make(map[string]sessionreview.Classification)
	versions := make(map[string]int64)
	for _, item := range search.Items {
		session, found, getErr := s.sessions.GetSession(item.ID)
		if getErr != nil || !found {
			continue
		}
		targetBranch := session.WorktreeBaseBranch
		var classification sessionreview.Classification
		if sessionsV3ReviewSessionUsesCheckout(ctx, session, checkoutSnapshot, checkoutCommonDir) {
			classification = sessionreview.ClassifyCurrentCheckout(session, checkoutSnapshot, now, grace)
		} else {
			if checkoutBranch != "" && sessionsV3ReviewWorktreeMatchesCheckout(ctx, session.WorktreeRootPath, checkoutCommonDir) {
				targetBranch = checkoutBranch
			}
			classification = sessionreview.ClassifyAgainstTarget(ctx, sessionreview.ExecGitRunner{}, session, now, grace, targetBranch)
		}
		if classification.Classification == "done" {
			doneAt := sessionReviewDoneAt(session)
			if doneAt == 0 && req.Automatic && len(compactStrings(req.ArchiveIDs)) > 0 {
				classification.Classification = "retained"
				classification.Reason = "done_timestamp_missing"
				byID[classification.SessionID] = classification
				versions[classification.SessionID] = classification.UpdatedAt
				retained = append(retained, classification)
				continue
			}
			if doneAt == 0 {
				doneAt = now.UnixMilli()
				metadata := cloneStringAnyMap(session.Metadata)
				metadata["review_done_at"] = doneAt
				updated, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata)
				if updateErr != nil {
					return nil, updateErr
				}
				session = updated
				classification.UpdatedAt = updated.UpdatedAt
			}
			classification.DoneAt = doneAt
			classification.ArchiveAfter = doneAt + grace.Milliseconds()
			classification.ArchiveReady = now.UnixMilli() >= classification.ArchiveAfter
		}
		byID[classification.SessionID] = classification
		versions[classification.SessionID] = classification.UpdatedAt
		if classification.Classification == "done" {
			done = append(done, classification)
		} else {
			retained = append(retained, classification)
		}
	}

	requested := compactStrings(req.ArchiveIDs)
	if req.ArchiveAll && len(requested) == 0 {
		for _, candidate := range done {
			requested = append(requested, candidate.SessionID)
		}
	}
	if req.Automatic && len(requested) == 0 {
		for _, candidate := range done {
			if candidate.ArchiveReady {
				requested = append(requested, candidate.SessionID)
			}
		}
	}
	archived := make([]string, 0, len(requested))
	if len(requested) > 0 {
		expected := make(map[string]int64, len(requested))
		snapshots := make([]pebblestore.SessionSnapshot, 0, len(requested))
		for _, id := range requested {
			candidate, exists := byID[id]
			if !exists || candidate.Classification != "done" {
				return nil, errors.New("archive selection contains a session that is not safely integrated")
			}
			if req.Automatic && !candidate.ArchiveReady {
				return nil, errors.New("automatic archive selection contains a session still inside its grace period")
			}
			session, found, _ := s.sessions.GetSession(id)
			if !found {
				return nil, errors.New("archive selection contains an unavailable session")
			}
			snapshots = append(snapshots, session)
			expected[id] = versions[id]
		}
		events, archiveErr := s.sessions.ArchiveSessionsWithEventsIfUnchanged(requested, expected)
		if archiveErr != nil {
			return nil, archiveErr
		}
		s.publishSessionsV3ArchiveRealtime(snapshots, events)
		archived = requested
	}
	sort.Slice(retained, func(i, j int) bool { return retained[i].UpdatedAt > retained[j].UpdatedAt })
	sort.Slice(done, func(i, j int) bool { return done[i].UpdatedAt > done[j].UpdatedAt })
	return map[string]any{
		"ok":                        true,
		"target_detection":          "current_checkout_branch_for_matching_repository_then_session_worktree_base_branch",
		"current_target_branch":     checkoutBranch,
		"comparison":                "git cherry target_branch worktree_head (patch-equivalent commits count as integrated)",
		"retained":                  retained,
		"done":                      done,
		"archived_session_ids":      archived,
		"recently_archived":         recentlyArchived,
		"grace_period_ms":           grace.Milliseconds(),
		"checkout_dirty":            checkoutSnapshot.HasGit && !checkoutSnapshot.Clean,
		"checkout_dirty_count":      checkoutSnapshot.DirtyCount,
		"blocked_by_checkout_count": countCurrentCheckoutBlocked(retained),
		"complete":                  !search.Pagination.HasMore,
	}, nil
}

func (s *Server) handleSessionsV3Unarchive(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w)
		return
	}
	principal, ok := PrincipalFromRequest(r)
	if !ok || !principal.Valid() {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	var req sessionsV3UnarchiveRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ids := compactStrings(req.SessionIDs)
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, errors.New("session_ids is required"))
		return
	}
	for _, id := range ids {
		tombstone, found, err := s.sessions.GetSessionTombstone(id)
		if err != nil || !found || tombstone.AccountScopeID != principal.AccountScopeID || tombstone.UserID != principal.UserID || !tombstone.Archived || tombstone.Deleted {
			writeError(w, http.StatusNotFound, errors.New("archived session not found"))
			return
		}
		if req.Versions[id] != tombstone.UpdatedAt {
			writeError(w, http.StatusConflict, errors.New("archived session changed; refresh and try again"))
			return
		}
	}
	if err := s.sessions.ReactivateArchivedSessionsIfUnchanged(ids, req.Versions); err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if head, err := s.sessions.CurrentRealtimeOutboxRevision(); err == nil {
		for _, id := range ids {
			if record, found, recordErr := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(id, head); recordErr == nil && found {
				_ = s.publishCommittedV3RealtimeOutbox(record)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unarchived_session_ids": ids})
}

func sessionsV3ReviewCheckoutTarget(ctx context.Context, workspacePath string) (gitstatus.Snapshot, string) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return gitstatus.Snapshot{}, ""
	}
	watch, err := gitstatus.ResolveWatchPaths(ctx, workspacePath)
	if err != nil || strings.TrimSpace(watch.CommonDir) == "" {
		return gitstatus.Snapshot{}, ""
	}
	snapshot, err := gitstatus.SnapshotForResolvedPaths(ctx, workspacePath, watch, gitstatus.Options{})
	if err != nil {
		return gitstatus.Snapshot{}, ""
	}
	return snapshot, gitstatus.NormalizePath(watch.CommonDir)
}

func sessionsV3ReviewSessionUsesCheckout(ctx context.Context, session pebblestore.SessionSnapshot, checkout gitstatus.Snapshot, checkoutCommonDir string) bool {
	if session.WorktreeEnabled || !checkout.HasGit || strings.TrimSpace(checkoutCommonDir) == "" {
		return false
	}
	sessionPath := strings.TrimSpace(session.WorkspacePath)
	if sessionPath == "" {
		return false
	}
	watch, err := gitstatus.ResolveWatchPaths(ctx, sessionPath)
	if err != nil || gitstatus.NormalizePath(watch.CommonDir) != checkoutCommonDir {
		return false
	}
	return gitstatus.NormalizePath(watch.RepoRoot) == gitstatus.NormalizePath(checkout.RepoRoot)
}

func sessionsV3ReviewWorktreeMatchesCheckout(ctx context.Context, worktreePath, checkoutCommonDir string) bool {
	worktreePath = strings.TrimSpace(worktreePath)
	checkoutCommonDir = strings.TrimSpace(checkoutCommonDir)
	if worktreePath == "" || checkoutCommonDir == "" {
		return false
	}
	watch, err := gitstatus.ResolveWatchPaths(ctx, worktreePath)
	return err == nil && gitstatus.NormalizePath(watch.CommonDir) == checkoutCommonDir
}

func (s *Server) integrateSessionsV3ReviewWorktrees(ctx context.Context, principal identity.Principal, workspacePath string, ids []string, now time.Time) error {
	ids = compactStrings(ids)
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return errors.New("workspace_path is required for integration")
	}
	parent, commonDir := sessionsV3ReviewCheckoutTarget(ctx, workspacePath)
	if !parent.HasGit || !parent.Clean || parent.HeadOID == "" || commonDir == "" {
		return errors.New("current checkout must be a clean available Git repository before integration")
	}
	children := make([]worktreeruntime.TaskIntegrationChild, 0, len(ids))
	sessions := make([]pebblestore.SessionSnapshot, 0, len(ids))
	for _, id := range ids {
		session, found, err := s.sessions.GetSession(id)
		if err != nil || !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
			return errors.New("integration selection contains an unavailable session")
		}
		if !session.WorktreeEnabled || !sessionsV3ReviewWorktreeMatchesCheckout(ctx, session.WorktreeRootPath, commonDir) {
			return errors.New("integration selection contains an unrelated or unmanaged worktree")
		}
		classification := sessionreview.ClassifyAgainstTarget(ctx, sessionreview.ExecGitRunner{}, session, now, sessionreview.DefaultGracePeriod, parent.Branch)
		if !classification.IntegrateEligible {
			return errors.New("integration selection contains a session that is not committed and integration-ready")
		}
		state, inspectErr := (&worktreeruntime.Service{}).InspectTaskWorkspace(session.WorktreeRootPath)
		if inspectErr != nil || !state.Clean || state.BranchName != session.WorktreeBranch {
			return errors.New("integration selection contains a changed, dirty, or unavailable worktree")
		}
		base := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "base_commit"))
		if base == "" {
			base, inspectErr = (sessionreview.ExecGitRunner{}).Run(ctx, session.WorktreeRootPath, "merge-base", parent.HeadOID, state.HeadCommit)
			if inspectErr != nil || strings.TrimSpace(base) == "" {
				return errors.New("integration selection is missing verifiable base-commit lineage")
			}
		}
		children = append(children, worktreeruntime.TaskIntegrationChild{SessionID: session.ID, BaseCommit: strings.TrimSpace(base), HeadCommit: state.HeadCommit})
		sessions = append(sessions, session)
	}
	worktreeSvc := &worktreeruntime.Service{}
	plan, err := worktreeSvc.PrepareTaskIntegration(workspacePath, parent.HeadOID, children)
	if err != nil {
		return err
	}
	if _, err = worktreeSvc.ApplyTaskIntegration(workspacePath, plan); err != nil {
		return err
	}
	for _, session := range sessions {
		metadata := cloneStringAnyMap(session.Metadata)
		metadata["review_done_at"] = now.UnixMilli()
		if _, _, err := s.sessions.UpdateDerivedMetadata(session.ID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func sessionReviewDoneAt(session pebblestore.SessionSnapshot) int64 {
	switch value := session.Metadata["review_done_at"].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case string:
		parsed, _ := time.Parse(time.RFC3339Nano, value)
		return parsed.UnixMilli()
	default:
		return 0
	}
}

func cloneStringAnyMap(source map[string]any) map[string]any {
	clone := make(map[string]any, len(source)+1)
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func countCurrentCheckoutBlocked(items []sessionreview.Classification) int {
	count := 0
	for _, item := range items {
		if item.CurrentCheckout && item.Reason == "current_checkout_uncommitted_work" {
			count++
		}
	}
	return count
}

func (s *Server) runSessionsV3ReviewAutoArchive(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.archiveDueSessionsV3Review(ctx, now)
		}
	}
}

func (s *Server) archiveDueSessionsV3Review(ctx context.Context, now time.Time) {
	if s == nil || s.sessions == nil {
		return
	}
	sessions, err := s.sessions.ListSessions(sessionsV3ReviewWorktreeLimit)
	if err != nil {
		return
	}
	for _, session := range sessions {
		doneAt := sessionReviewDoneAt(session)
		if doneAt == 0 || now.UnixMilli() < doneAt+sessionreview.DefaultGracePeriod.Milliseconds() {
			continue
		}
		principal := identity.Principal{UserID: session.UserID, AccountScopeID: session.AccountScopeID}
		if !principal.Valid() {
			continue
		}
		workspacePath := strings.TrimSpace(session.WorkspacePath)
		if session.WorktreeEnabled {
			workspacePath = strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
		}
		if workspacePath == "" {
			continue
		}
		result, classifyErr := s.classifySessionsV3ReviewWorktrees(ctx, principal, sessionsV3ReviewWorktreesRequest{WorkspacePath: workspacePath, ArchiveIDs: []string{session.ID}, Automatic: true, GraceHours: "1"})
		archived, _ := result["archived_session_ids"].([]string)
		if classifyErr != nil || len(archived) == 0 {
			continue
		}
	}
}

func compactStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

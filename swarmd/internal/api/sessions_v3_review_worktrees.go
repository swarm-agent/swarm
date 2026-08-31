package api

import (
	"context"
	"errors"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	worktreeruntime "swarm/packages/swarmd/internal/worktree"
)

const (
	sessionsV3ReviewWorktreeLimit        = 200
	sessionsV3ReviewWorktreePageLimit    = 50
	sessionsV3ReviewAutoArchiveBatchSize = 32
)

type sessionsV3ReviewWorktreesRequest struct {
	WorkspacePath      string            `json:"workspace_path,omitempty"`
	SessionIDs         []string          `json:"session_ids,omitempty"`
	ArchiveIDs         []string          `json:"archive_session_ids,omitempty"`
	ArchiveAll         bool              `json:"archive_all,omitempty"`
	LegacyIntegrateIDs []string          `json:"integrate_session_ids,omitempty"`
	PromoteIDs         []string          `json:"promote_session_ids,omitempty"`
	SourceHeads        map[string]string `json:"source_head_by_session_id,omitempty"`
	TargetBranch       string            `json:"target_branch,omitempty"`
	TargetHead         string            `json:"target_head,omitempty"`
	CommitIDs          []string          `json:"commit_session_ids,omitempty"`
	Automatic          bool              `json:"automatic,omitempty"`
	GraceHours         string            `json:"grace_hours,omitempty"`
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
	if batchID := reviewCommitString(result["commit_batch_id"]); batchID != "" {
		w.Header().Set("X-Swarm-Review-Commit-Batch", batchID)
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) classifySessionsV3ReviewWorktrees(ctx context.Context, principal identity.Principal, req sessionsV3ReviewWorktreesRequest) (map[string]any, error) {
	if len(compactStrings(req.LegacyIntegrateIDs)) > 0 {
		return nil, errors.New("integrate_session_ids is retired; use explicit promotion with exact source and target lineage")
	}
	if req.Automatic && len(compactStrings(req.PromoteIDs)) > 0 {
		return nil, errors.New("worktree promotion requires an explicit user action")
	}
	checkoutSnapshot, checkoutCommonDir := sessionsV3ReviewCheckoutTarget(ctx, req.WorkspacePath)
	repository := newSessionsV3ReviewRepository(ctx, checkoutSnapshot, checkoutCommonDir)
	checkoutBranch := strings.TrimSpace(checkoutSnapshot.Branch)
	requestedSessionIDs := compactStrings(req.SessionIDs)
	search, archivedSearch, err := s.searchSessionsV3ReviewWorktrees(principal, requestedSessionIDs)
	if err != nil {
		return nil, err
	}
	recentlyArchived := make([]map[string]any, 0, len(archivedSearch.Items))
	for _, item := range archivedSearch.Items {
		if strings.TrimSpace(req.WorkspacePath) != "" && !repository.searchItemMatchesCheckout(item) {
			continue
		}
		recentlyArchived = append(recentlyArchived, map[string]any{"session_id": item.ID, "title": item.Title, "updated_at": item.UpdatedAt, "worktree_branch": item.WorktreeBranch, "worktree_path": item.WorktreeRootPath, "target_branch": item.WorktreeBaseBranch})
	}
	grace := sessionreview.ParseGraceHours(req.GraceHours)
	autoArchiveDelay := time.Duration(0)
	if req.Automatic {
		autoArchiveDelay, err = s.reviewAutoArchiveDelay(principal.AccountScopeID)
		if err != nil {
			return nil, err
		}
		if autoArchiveDelay > 0 {
			grace = autoArchiveDelay
		}
	}
	now := time.Now()
	commitBatchID := ""
	if len(compactStrings(req.CommitIDs)) > 0 {
		commitBatchID, err = s.startSessionsV3ReviewCommits(ctx, principal, req.WorkspacePath, req.CommitIDs, search.Items, now)
		if err != nil {
			return nil, err
		}
		search, err = searchSessionsV3ReviewWorktreePages(s.sessions.SearchSessions, pebblestore.V3SessionSearchOptions{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Global: true,
			State: "needs_review", ArchivedMode: "exclude",
		}, sessionsV3ReviewWorktreeLimit)
		if err != nil {
			return nil, err
		}
	}
	if len(compactStrings(req.PromoteIDs)) > 0 {
		if err := s.promoteSessionsV3ReviewWorktrees(ctx, principal, req.WorkspacePath, req.PromoteIDs, req.SourceHeads, req.TargetBranch, req.TargetHead, now); err != nil {
			return nil, err
		}
		search, err = searchSessionsV3ReviewWorktreePages(s.sessions.SearchSessions, pebblestore.V3SessionSearchOptions{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Global: true,
			State: "needs_review", ArchivedMode: "exclude",
		}, sessionsV3ReviewWorktreeLimit)
		if err != nil {
			return nil, err
		}
	}
	retained := make([]sessionreview.Classification, 0)
	done := make([]sessionreview.Classification, 0)
	byID := make(map[string]sessionreview.Classification)
	versions := make(map[string]int64)
	reviewSessions := make([]pebblestore.SessionSnapshot, 0, len(search.Items))
	for _, item := range search.Items {
		session, found, getErr := s.sessions.GetSession(item.ID)
		if getErr != nil || !found {
			continue
		}
		if strings.TrimSpace(req.WorkspacePath) != "" && !repository.sessionMatchesCheckout(session) {
			continue
		}
		reviewSessions = append(reviewSessions, session)
	}
	// Worktree status and patch-equivalence checks dominate classification latency.
	// Resolve independent Git work concurrently, once per unique worktree, before
	// deterministic metadata updates and sorting.
	repository.prefetchSnapshots(ctx, reviewSessions)
	classifications := make([]sessionreview.Classification, len(reviewSessions))
	var classificationWait sync.WaitGroup
	for index, session := range reviewSessions {
		classificationWait.Add(1)
		go func() {
			defer classificationWait.Done()
			targetBranch := session.WorktreeBaseBranch
			if repository.sessionUsesCheckout(session) {
				classifications[index] = sessionreview.ClassifyCurrentCheckout(session, checkoutSnapshot, now, grace)
				return
			}
			if checkoutBranch != "" && repository.worktreeMatchesCheckout(session.WorktreeRootPath) {
				targetBranch = checkoutBranch
			}
			classifications[index] = repository.classifyAgainstTarget(ctx, session, now, grace, targetBranch)
		}()
	}
	classificationWait.Wait()
	for index, session := range reviewSessions {
		classification := classifications[index]
		classification.CommitJob = sessionReviewCommitJob(session)
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
				delay, delayErr := s.reviewAutoArchiveDelay(session.AccountScopeID)
				if delayErr != nil {
					return nil, delayErr
				}
				if delay > 0 {
					metadata["review_auto_archive_after"] = sessionsV3ReviewArchiveDeadline(session, doneAt, delay)
				}
				updated, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata)
				if updateErr != nil {
					return nil, updateErr
				}
				session = updated
				classification.UpdatedAt = updated.UpdatedAt
			}
			delay, delayErr := s.reviewAutoArchiveDelay(session.AccountScopeID)
			if delayErr != nil {
				return nil, delayErr
			}
			desiredArchiveAfter := sessionsV3ReviewArchiveDeadline(session, doneAt, delay)
			if scheduled := sessionsV3MetadataInt64(session.Metadata, "review_auto_archive_after"); scheduled != desiredArchiveAfter {
				metadata := cloneStringAnyMap(session.Metadata)
				if desiredArchiveAfter > 0 {
					metadata["review_auto_archive_after"] = desiredArchiveAfter
				} else {
					delete(metadata, "review_auto_archive_after")
				}
				updated, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata)
				if updateErr != nil {
					return nil, updateErr
				}
				session = updated
				classification.UpdatedAt = updated.UpdatedAt
			}
			classification.DoneAt = doneAt
			classification.ArchiveAfter = sessionsV3ReviewArchiveDeadline(session, doneAt, grace)
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
	if req.Automatic && autoArchiveDelay > 0 && len(requested) == 0 {
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
		"current_target_head":       strings.TrimSpace(checkoutSnapshot.HeadOID),
		"comparison":                "git cherry target_branch worktree_head (patch-equivalent and conflict-resolved cherry-picks with matching author identity, message, and changed paths count as integrated)",
		"retained":                  retained,
		"done":                      done,
		"archived_session_ids":      archived,
		"commit_batch_id":           commitBatchID,
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
	reactivated := make(map[string]pebblestore.V3RealtimeOutboxRecord, len(ids))
	if head, err := s.sessions.CurrentRealtimeOutboxRevision(); err == nil {
		for _, id := range ids {
			if record, found, recordErr := s.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(id, head); recordErr == nil && found && record.Event.EventType == "session.reactivated" {
				reactivated[id] = record
				_ = s.publishCommittedV3RealtimeOutbox(record)
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "unarchived_session_ids": ids, "reactivated": reactivated})
}

func (s *Server) searchSessionsV3ReviewWorktrees(principal identity.Principal, sessionIDs []string) (pebblestore.V3SessionSearchResult, pebblestore.V3SessionSearchResult, error) {
	if len(sessionIDs) > sessionsV3ReviewWorktreeLimit {
		return pebblestore.V3SessionSearchResult{}, pebblestore.V3SessionSearchResult{}, errors.New("too many review session ids")
	}
	if len(sessionIDs) > 0 {
		active := pebblestore.V3SessionSearchResult{Items: make([]pebblestore.V3SessionSearchItem, 0, len(sessionIDs))}
		for _, sessionID := range sessionIDs {
			session, found, err := s.sessions.GetSession(sessionID)
			if err != nil {
				return pebblestore.V3SessionSearchResult{}, pebblestore.V3SessionSearchResult{}, err
			}
			if !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
				return pebblestore.V3SessionSearchResult{}, pebblestore.V3SessionSearchResult{}, errors.New("review session not found")
			}
			active.Items = append(active.Items, sessionsV3ReviewSearchItem(session))
		}
		return active, pebblestore.V3SessionSearchResult{}, nil
	}

	active, err := searchSessionsV3ReviewWorktreePages(s.sessions.SearchSessions, pebblestore.V3SessionSearchOptions{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Global:         true,
		State:          "needs_review",
		ArchivedMode:   "exclude",
	}, sessionsV3ReviewWorktreeLimit)
	if err != nil {
		return pebblestore.V3SessionSearchResult{}, pebblestore.V3SessionSearchResult{}, err
	}
	archived, err := searchSessionsV3ReviewWorktreePages(s.sessions.SearchSessions, pebblestore.V3SessionSearchOptions{
		AccountScopeID: principal.AccountScopeID,
		UserID:         principal.UserID,
		Global:         true,
		State:          "needs_review",
		ArchivedMode:   "only",
	}, sessionsV3ReviewWorktreeLimit)
	if err != nil {
		return pebblestore.V3SessionSearchResult{}, pebblestore.V3SessionSearchResult{}, err
	}
	return active, archived, nil
}

func sessionsV3ReviewSearchItem(session pebblestore.SessionSnapshot) pebblestore.V3SessionSearchItem {
	return pebblestore.V3SessionSearchItem{
		ID: session.ID, UserID: session.UserID, AccountScopeID: session.AccountScopeID,
		WorkspacePath: session.WorkspacePath, WorkspaceName: session.WorkspaceName,
		TemporaryWorkspaceRoots: session.TemporaryWorkspaceRoots, Title: session.Title, Mode: session.Mode,
		WorktreeEnabled: session.WorktreeEnabled, WorktreeRootPath: session.WorktreeRootPath,
		WorktreeBaseBranch: session.WorktreeBaseBranch, WorktreeBranch: session.WorktreeBranch,
		Metadata: session.Metadata, CreatedAt: session.CreatedAt, UpdatedAt: session.UpdatedAt,
		MessageCount: session.MessageCount, LastMessageAt: session.LastMessageAt, Lifecycle: session.Lifecycle,
	}
}

func searchSessionsV3ReviewWorktreePages(search func(pebblestore.V3SessionSearchOptions) (pebblestore.V3SessionSearchResult, error), options pebblestore.V3SessionSearchOptions, limit int) (pebblestore.V3SessionSearchResult, error) {
	if limit <= 0 {
		limit = sessionsV3ReviewWorktreeLimit
	}
	var combined pebblestore.V3SessionSearchResult
	for len(combined.Items) < limit {
		remaining := limit - len(combined.Items)
		options.Limit = min(sessionsV3ReviewWorktreePageLimit, remaining)
		page, err := search(options)
		if err != nil {
			return pebblestore.V3SessionSearchResult{}, err
		}
		combined.Items = append(combined.Items, page.Items...)
		combined.Pagination = page.Pagination
		combined.Summary = page.Summary
		if !page.Pagination.HasMore {
			break
		}
		beforeAt := page.Pagination.NextBeforeUpdatedAt
		beforeID := strings.TrimSpace(page.Pagination.NextBeforeSessionID)
		if beforeAt == nil || beforeID == "" {
			beforeAt, beforeID, err = pebblestore.DecodeV3SessionSearchCursor(page.Pagination.NextCursor)
			if err != nil {
				return pebblestore.V3SessionSearchResult{}, err
			}
		}
		if beforeAt == nil || beforeID == "" {
			return pebblestore.V3SessionSearchResult{}, errors.New("review worktree search returned an incomplete pagination cursor")
		}
		options.BeforeUpdatedAt = beforeAt
		options.BeforeSessionID = beforeID
	}
	if len(combined.Items) > limit {
		combined.Items = combined.Items[:limit]
	}
	return combined, nil
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

type sessionsV3ReviewRepository struct {
	ctx               context.Context
	checkout          gitstatus.Snapshot
	checkoutCommonDir string
	worktreeRoots     map[string]struct{}
	inventoryLoaded   bool
	mu                sync.Mutex
	snapshots         map[string]gitstatus.Snapshot
	snapshotErrors    map[string]error
}

func newSessionsV3ReviewRepository(ctx context.Context, checkout gitstatus.Snapshot, checkoutCommonDir string) *sessionsV3ReviewRepository {
	repository := &sessionsV3ReviewRepository{
		ctx:               ctx,
		checkout:          checkout,
		checkoutCommonDir: strings.TrimSpace(checkoutCommonDir),
		worktreeRoots:     make(map[string]struct{}),
		snapshots:         make(map[string]gitstatus.Snapshot),
		snapshotErrors:    make(map[string]error),
	}
	root := strings.TrimSpace(checkout.RepoRoot)
	if root == "" {
		return repository
	}
	output, err := (sessionreview.ExecGitRunner{}).Run(ctx, root, "worktree", "list", "--porcelain", "-z")
	if err != nil {
		return repository
	}
	repository.inventoryLoaded = true
	for _, record := range strings.Split(output, "\x00") {
		if !strings.HasPrefix(record, "worktree ") {
			continue
		}
		if path := gitstatus.NormalizePath(strings.TrimSpace(strings.TrimPrefix(record, "worktree "))); path != "" {
			repository.worktreeRoots[path] = struct{}{}
		}
	}
	return repository
}

func (r *sessionsV3ReviewRepository) searchItemMatchesCheckout(item pebblestore.V3SessionSearchItem) bool {
	return r.sessionMatchesCheckout(pebblestore.SessionSnapshot{
		WorkspacePath:      item.WorkspacePath,
		WorktreeEnabled:    item.WorktreeEnabled,
		WorktreeRootPath:   item.WorktreeRootPath,
		WorktreeBaseBranch: item.WorktreeBaseBranch,
		WorktreeBranch:     item.WorktreeBranch,
		Metadata:           item.Metadata,
	})
}

func (r *sessionsV3ReviewRepository) sessionMatchesCheckout(session pebblestore.SessionSnapshot) bool {
	if r == nil || r.checkoutCommonDir == "" {
		return false
	}
	if !session.WorktreeEnabled {
		return r.sessionUsesCheckout(session)
	}
	worktreePath := strings.TrimSpace(session.WorktreeRootPath)
	if worktreePath == "" {
		worktreePath = strings.TrimSpace(session.WorkspacePath)
	}
	if r.worktreeMatchesCheckout(worktreePath) {
		return true
	}
	sourcePath := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
	return sourcePath != "" && r.worktreeMatchesCheckout(sourcePath)
}

func (r *sessionsV3ReviewRepository) sessionUsesCheckout(session pebblestore.SessionSnapshot) bool {
	if r == nil || session.WorktreeEnabled || !r.checkout.HasGit || r.checkoutCommonDir == "" {
		return false
	}
	sessionPath := gitstatus.NormalizePath(session.WorkspacePath)
	checkoutRoot := gitstatus.NormalizePath(r.checkout.RepoRoot)
	if sessionPath == "" || checkoutRoot == "" {
		return false
	}
	if sessionPath == checkoutRoot {
		return true
	}
	// Workspace paths can point at a subdirectory. Resolve only this uncommon case;
	// exact checkout roots and all listed sibling worktrees stay subprocess-free.
	return sessionsV3ReviewSessionUsesCheckout(r.ctx, session, r.checkout, r.checkoutCommonDir)
}

func (r *sessionsV3ReviewRepository) worktreeMatchesCheckout(worktreePath string) bool {
	if r == nil || strings.TrimSpace(worktreePath) == "" || r.checkoutCommonDir == "" {
		return false
	}
	path := gitstatus.NormalizePath(worktreePath)
	for root := range r.worktreeRoots {
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	if r.inventoryLoaded {
		return false
	}
	// Preserve the prior repository-resolution path when the bulk inventory query
	// itself failed; an unavailable inventory must not silently hide sessions.
	return sessionsV3ReviewWorktreeMatchesCheckout(r.ctx, worktreePath, r.checkoutCommonDir)
}

func (r *sessionsV3ReviewRepository) prefetchSnapshots(ctx context.Context, sessions []pebblestore.SessionSnapshot) {
	var wait sync.WaitGroup
	seen := make(map[string]struct{}, len(sessions))
	for _, session := range sessions {
		if !session.WorktreeEnabled {
			continue
		}
		path := gitstatus.NormalizePath(session.WorktreeRootPath)
		if path == "" {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		wait.Add(1)
		go func() {
			defer wait.Done()
			r.loadSnapshot(ctx, path)
		}()
	}
	wait.Wait()
}

func (r *sessionsV3ReviewRepository) loadSnapshot(ctx context.Context, path string) (gitstatus.Snapshot, error) {
	r.mu.Lock()
	if snapshot, ok := r.snapshots[path]; ok {
		r.mu.Unlock()
		return snapshot, nil
	}
	if err, ok := r.snapshotErrors[path]; ok {
		r.mu.Unlock()
		return gitstatus.Snapshot{}, err
	}
	r.mu.Unlock()

	watch := gitstatus.WatchPaths{RepoRoot: path, CommonDir: r.checkoutCommonDir}
	snapshot, err := gitstatus.SnapshotForResolvedPaths(ctx, path, watch, gitstatus.Options{})
	r.mu.Lock()
	if err != nil {
		r.snapshotErrors[path] = err
	} else {
		r.snapshots[path] = snapshot
	}
	r.mu.Unlock()
	return snapshot, err
}

func (r *sessionsV3ReviewRepository) classifyAgainstTarget(ctx context.Context, session pebblestore.SessionSnapshot, now time.Time, grace time.Duration, targetBranch string) sessionreview.Classification {
	path := gitstatus.NormalizePath(session.WorktreeRootPath)
	snapshot, err := r.loadSnapshot(ctx, path)
	if path == "" || err != nil {
		snapshot = gitstatus.Snapshot{}
	}
	return sessionreview.ClassifySnapshotAgainstTarget(ctx, sessionreview.ExecGitRunner{}, session, snapshot, now, grace, targetBranch)
}

func sessionsV3ReviewSearchItemMatchesCheckout(ctx context.Context, item pebblestore.V3SessionSearchItem, checkout gitstatus.Snapshot, checkoutCommonDir string) bool {
	return sessionsV3ReviewSessionMatchesCheckout(ctx, pebblestore.SessionSnapshot{
		WorkspacePath:      item.WorkspacePath,
		WorktreeEnabled:    item.WorktreeEnabled,
		WorktreeRootPath:   item.WorktreeRootPath,
		WorktreeBaseBranch: item.WorktreeBaseBranch,
		WorktreeBranch:     item.WorktreeBranch,
		Metadata:           item.Metadata,
	}, checkout, checkoutCommonDir)
}

func sessionsV3ReviewSessionMatchesCheckout(ctx context.Context, session pebblestore.SessionSnapshot, checkout gitstatus.Snapshot, checkoutCommonDir string) bool {
	if strings.TrimSpace(checkoutCommonDir) == "" {
		return false
	}
	if !session.WorktreeEnabled {
		return sessionsV3ReviewSessionUsesCheckout(ctx, session, checkout, checkoutCommonDir)
	}
	worktreePath := strings.TrimSpace(session.WorktreeRootPath)
	if worktreePath == "" {
		worktreePath = strings.TrimSpace(session.WorkspacePath)
	}
	if sessionsV3ReviewWorktreeMatchesCheckout(ctx, worktreePath, checkoutCommonDir) {
		return true
	}
	sourcePath := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
	return sourcePath != "" && sessionsV3ReviewWorktreeMatchesCheckout(ctx, sourcePath, checkoutCommonDir)
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

func (s *Server) promoteSessionsV3ReviewWorktrees(ctx context.Context, principal identity.Principal, workspacePath string, ids []string, sourceHeads map[string]string, expectedTargetBranch, expectedTargetHead string, now time.Time) error {
	ids = compactStrings(ids)
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" {
		return errors.New("workspace_path is required for promotion")
	}
	expectedTargetBranch = strings.TrimSpace(expectedTargetBranch)
	expectedTargetHead = strings.TrimSpace(expectedTargetHead)
	if expectedTargetBranch == "" || expectedTargetHead == "" || len(sourceHeads) == 0 {
		return errors.New("promotion requires exact target_branch, target_head, and source_head_by_session_id lineage")
	}
	parent, commonDir := sessionsV3ReviewCheckoutTarget(ctx, workspacePath)
	if !parent.HasGit || !parent.Clean || parent.HeadOID == "" || commonDir == "" {
		return errors.New("captured checkout must be a clean available Git repository before promotion")
	}
	if parent.Branch != expectedTargetBranch || parent.HeadOID != expectedTargetHead {
		return errors.New("promotion target branch or HEAD changed; refresh exact lineage before retrying")
	}
	children := make([]worktreeruntime.TaskIntegrationChild, 0, len(ids))
	sessions := make([]pebblestore.SessionSnapshot, 0, len(ids))
	for _, id := range ids {
		session, found, err := s.sessions.GetSession(id)
		if err != nil || !found || session.AccountScopeID != principal.AccountScopeID || session.UserID != principal.UserID {
			return errors.New("promotion selection contains an unavailable or unowned session")
		}
		if !session.WorktreeEnabled || !sessionsV3ReviewWorktreeMatchesCheckout(ctx, session.WorktreeRootPath, commonDir) {
			return errors.New("promotion selection contains an unrelated or unmanaged worktree")
		}
		if !sessionsV3ReviewPromotionMatchesCapturedCheckout(session, workspacePath, expectedTargetBranch) {
			return errors.New("promotion selection does not match the exact captured path and branch")
		}
		classification := sessionreview.ClassifyAgainstTarget(ctx, sessionreview.ExecGitRunner{}, session, now, sessionreview.DefaultGracePeriod, parent.Branch)
		if !classification.IntegrateEligible {
			return errors.New("promotion selection contains a session that is not committed and promotion-ready")
		}
		state, inspectErr := (&worktreeruntime.Service{}).InspectTaskWorkspace(session.WorktreeRootPath)
		if inspectErr != nil || !state.Clean || state.BranchName != session.WorktreeBranch || state.HeadCommit != strings.TrimSpace(sourceHeads[session.ID]) {
			return errors.New("promotion selection source branch or HEAD changed; refresh exact lineage before retrying")
		}
		base := strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "base_commit"))
		if base == "" {
			base, inspectErr = (sessionreview.ExecGitRunner{}).Run(ctx, session.WorktreeRootPath, "merge-base", parent.HeadOID, state.HeadCommit)
			if inspectErr != nil || strings.TrimSpace(base) == "" {
				return errors.New("promotion selection is missing verifiable base-commit lineage")
			}
		}
		children = append(children, worktreeruntime.TaskIntegrationChild{SessionID: session.ID, BaseCommit: strings.TrimSpace(base), HeadCommit: state.HeadCommit})
		sessions = append(sessions, session)
	}
	worktreeSvc := &worktreeruntime.Service{}
	plan, err := worktreeSvc.PrepareTaskIntegration(workspacePath, parent.Branch, parent.HeadOID, children)
	if err != nil {
		return err
	}
	if _, err = worktreeSvc.ApplyTaskIntegration(workspacePath, plan); err != nil {
		return err
	}
	for _, session := range sessions {
		metadata := cloneStringAnyMap(session.Metadata)
		doneAt := now.UnixMilli()
		metadata["review_done_at"] = doneAt
		delay, delayErr := s.reviewAutoArchiveDelay(session.AccountScopeID)
		if delayErr != nil {
			return delayErr
		}
		if delay > 0 {
			metadata["review_auto_archive_after"] = sessionsV3ReviewArchiveDeadline(session, doneAt, delay)
		}
		if _, _, err := s.sessions.UpdateDerivedMetadata(session.ID, metadata); err != nil {
			return err
		}
	}
	return nil
}

func sessionsV3ReviewPromotionMatchesCapturedCheckout(session pebblestore.SessionSnapshot, workspacePath, targetBranch string) bool {
	return strings.TrimSpace(session.WorktreeBaseBranch) == strings.TrimSpace(targetBranch) &&
		strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path")) == strings.TrimSpace(workspacePath)
}

func sessionsV3MetadataInt64(metadata map[string]any, key string) int64 {
	switch value := metadata[key].(type) {
	case int64:
		return value
	case int:
		return int64(value)
	case float64:
		return int64(value)
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return parsed
	default:
		return 0
	}
}

func sessionsV3ReviewArchiveDeadline(session pebblestore.SessionSnapshot, doneAt int64, delay time.Duration) int64 {
	if doneAt <= 0 || delay <= 0 {
		return 0
	}
	// LastMessageAt is the durable activity timestamp for this specific session.
	// UpdatedAt also covers lifecycle and metadata mutations, so it is not a
	// reliable measure of when that session last had user/assistant activity.
	lastActivityAt := session.LastMessageAt
	if doneAt > lastActivityAt {
		lastActivityAt = doneAt
	}
	return lastActivityAt + delay.Milliseconds()
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
	// Reconciliation discovers needs-review sessions that became eligible through
	// lifecycle mutations while no review-worktrees client was open. The durable
	// due index remains the only source of timed archive work between passes.
	s.runSessionsV3ReviewAutoArchivePass(ctx, time.Now())
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.runSessionsV3ReviewAutoArchivePass(ctx, now)
		}
	}
}

func (s *Server) runSessionsV3ReviewAutoArchivePass(ctx context.Context, now time.Time) {
	if err := s.reconcileSessionsV3ReviewAutoArchive(ctx, now); err != nil {
		log.Printf("warning: v3 review auto-archive reconciliation failed: %v", err)
	}
	if err := s.archiveDueSessionsV3Review(ctx, now); err != nil {
		log.Printf("warning: v3 review auto-archive worker failed: %v", err)
	}
}

func (s *Server) reviewAutoArchiveDelay(accountScopeID string) (time.Duration, error) {
	if s == nil || s.uiSettings == nil {
		return 0, errors.New("ui settings service is not configured")
	}
	settings, err := s.uiSettings.GetForAccount(strings.TrimSpace(accountScopeID))
	if err != nil {
		return 0, err
	}
	if settings.Chat.ReviewAutoArchiveMinutes <= 0 {
		return 0, nil
	}
	return time.Duration(settings.Chat.ReviewAutoArchiveMinutes) * time.Minute, nil
}

func (s *Server) reconcileSessionsV3ReviewAutoArchive(ctx context.Context, now time.Time) error {
	return s.reconcileSessionsV3ReviewAutoArchiveForAccount(ctx, now, "")
}

func (s *Server) reconcileSessionsV3ReviewAutoArchiveForAccount(ctx context.Context, now time.Time, accountScopeID string) error {
	if s == nil || s.sessions == nil || s.uiSettings == nil {
		return errors.New("review auto-archive reconciliation is not configured")
	}
	candidates, err := s.sessions.ListReviewAutoArchiveCandidates(strings.TrimSpace(accountScopeID))
	if err != nil {
		return err
	}
	for _, candidate := range candidates {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err := s.reconcileSessionV3ReviewAutoArchive(candidate.Session, candidate.NeedsReview, now); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) reconcileSessionV3ReviewAutoArchive(session pebblestore.SessionSnapshot, needsReview bool, now time.Time) error {
	delay, err := s.reviewAutoArchiveDelay(session.AccountScopeID)
	if err != nil {
		return errors.New("load review auto-archive setting for account " + session.AccountScopeID + ": " + err.Error())
	}
	scheduled := sessionsV3MetadataInt64(session.Metadata, "review_auto_archive_after")
	if delay <= 0 {
		if scheduled == 0 {
			return nil
		}
		metadata := cloneStringAnyMap(session.Metadata)
		delete(metadata, "review_auto_archive_after")
		_, _, err = s.sessions.UpdateDerivedMetadata(session.ID, metadata)
		return err
	}
	if !needsReview {
		if scheduled == 0 {
			return nil
		}
		metadata := cloneStringAnyMap(session.Metadata)
		delete(metadata, "review_auto_archive_after")
		_, _, err = s.sessions.UpdateDerivedMetadata(session.ID, metadata)
		return err
	}
	doneAt := sessionReviewDoneAt(session)
	if doneAt <= 0 {
		doneAt = now.UnixMilli()
	}
	desiredArchiveAfter := sessionsV3ReviewArchiveDeadline(session, doneAt, delay)
	if scheduled == desiredArchiveAfter && sessionReviewDoneAt(session) == doneAt {
		return nil
	}
	metadata := cloneStringAnyMap(session.Metadata)
	metadata["review_done_at"] = doneAt
	metadata["review_auto_archive_after"] = desiredArchiveAfter
	_, _, err = s.sessions.UpdateDerivedMetadata(session.ID, metadata)
	return err
}

func (s *Server) archiveDueSessionsV3Review(ctx context.Context, now time.Time) error {
	if s == nil || s.sessions == nil || s.uiSettings == nil {
		return errors.New("review auto-archive worker is not configured")
	}
	due, err := s.sessions.ListDueReviewAutoArchives(now.UnixMilli(), sessionsV3ReviewAutoArchiveBatchSize)
	if err != nil {
		return err
	}
	for _, item := range due {
		session, found, getErr := s.sessions.GetSession(item.SessionID)
		if getErr != nil {
			return getErr
		}
		if !found {
			if deleteErr := s.sessions.DeleteReviewAutoArchiveDue(item); deleteErr != nil {
				return deleteErr
			}
			continue
		}
		delay, delayErr := s.reviewAutoArchiveDelay(session.AccountScopeID)
		if delayErr != nil {
			return delayErr
		}
		if sessionsV3MetadataInt64(session.Metadata, "review_auto_archive_after") != item.DueAt {
			if deleteErr := s.sessions.DeleteReviewAutoArchiveDue(item); deleteErr != nil {
				return deleteErr
			}
			continue
		}
		if delay <= 0 {
			metadata := cloneStringAnyMap(session.Metadata)
			delete(metadata, "review_auto_archive_after")
			if _, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata); updateErr != nil {
				return updateErr
			}
			continue
		}
		doneAt := sessionReviewDoneAt(session)
		if doneAt <= 0 {
			metadata := cloneStringAnyMap(session.Metadata)
			delete(metadata, "review_auto_archive_after")
			if _, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata); updateErr != nil {
				return updateErr
			}
			continue
		}
		desiredArchiveAfter := sessionsV3ReviewArchiveDeadline(session, doneAt, delay)
		if desiredArchiveAfter != item.DueAt {
			metadata := cloneStringAnyMap(session.Metadata)
			metadata["review_auto_archive_after"] = desiredArchiveAfter
			if _, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata); updateErr != nil {
				return updateErr
			}
			// A changed deadline represents either newer activity or a setting
			// change. Re-index it durably and require a later worker pass to
			// re-read and re-validate the session before archival.
			continue
		}
		if session.Lifecycle != nil && session.Lifecycle.Active {
			if deferErr := s.deferSessionsV3ReviewAutoArchive(session, now); deferErr != nil {
				return deferErr
			}
			continue
		}
		needsReview, needsReviewErr := s.sessionNeedsReview(session.ID)
		if needsReviewErr != nil {
			return needsReviewErr
		}
		if !needsReview {
			metadata := cloneStringAnyMap(session.Metadata)
			delete(metadata, "review_auto_archive_after")
			if _, _, updateErr := s.sessions.UpdateDerivedMetadata(session.ID, metadata); updateErr != nil {
				return updateErr
			}
			continue
		}
		workspacePath := strings.TrimSpace(session.WorkspacePath)
		if session.WorktreeEnabled {
			workspacePath = strings.TrimSpace(sessionsV3MetadataString(session.Metadata, "swarm_v3_source_workspace_path"))
		}
		if workspacePath == "" {
			if deferErr := s.deferSessionsV3ReviewAutoArchive(session, now); deferErr != nil {
				return deferErr
			}
			continue
		}
		checkoutSnapshot, checkoutCommonDir := sessionsV3ReviewCheckoutTarget(ctx, workspacePath)
		targetBranch := session.WorktreeBaseBranch
		var classification sessionreview.Classification
		if sessionsV3ReviewSessionUsesCheckout(ctx, session, checkoutSnapshot, checkoutCommonDir) {
			classification = sessionreview.ClassifyCurrentCheckout(session, checkoutSnapshot, now, delay)
		} else {
			if checkoutSnapshot.Branch != "" && sessionsV3ReviewWorktreeMatchesCheckout(ctx, session.WorktreeRootPath, checkoutCommonDir) {
				targetBranch = checkoutSnapshot.Branch
			}
			classification = sessionreview.ClassifyAgainstTarget(ctx, sessionreview.ExecGitRunner{}, session, now, delay, targetBranch)
		}
		if classification.Classification != "done" {
			if deferErr := s.deferSessionsV3ReviewAutoArchive(session, now); deferErr != nil {
				return deferErr
			}
			continue
		}
		events, archiveErr := s.sessions.ArchiveSessionsWithEventsIfUnchanged([]string{session.ID}, map[string]int64{session.ID: session.UpdatedAt})
		if archiveErr != nil {
			log.Printf("warning: v3 review auto-archive mutation failed session=%q: %v", session.ID, archiveErr)
			if deferErr := s.deferSessionsV3ReviewAutoArchive(session, now); deferErr != nil {
				return deferErr
			}
			continue
		}
		s.publishSessionsV3ArchiveRealtime([]pebblestore.SessionSnapshot{session}, events)
	}
	return nil
}

func (s *Server) sessionNeedsReview(sessionID string) (bool, error) {
	if s == nil || s.sessions == nil {
		return false, errors.New("session service is not configured")
	}
	plan, ok, err := s.sessions.GetActivePlan(sessionID)
	if err != nil {
		return false, err
	}
	if !ok || plan.Document == nil {
		return false, nil
	}
	if plan.Document.ExecutionState != nil && strings.EqualFold(strings.TrimSpace(plan.Document.ExecutionState.Status), "waiting_review") {
		return true, nil
	}
	for _, checkpoint := range plan.Document.Checkpoints {
		if checkpoint.ID == plan.Document.ActiveCheckpointID {
			return strings.EqualFold(strings.TrimSpace(checkpoint.Status), "needs_review"), nil
		}
	}
	return false, nil
}

func (s *Server) deferSessionsV3ReviewAutoArchive(session pebblestore.SessionSnapshot, now time.Time) error {
	metadata := cloneStringAnyMap(session.Metadata)
	metadata["review_auto_archive_after"] = now.Add(5 * time.Minute).UnixMilli()
	_, _, err := s.sessions.UpdateDerivedMetadata(session.ID, metadata)
	return err
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

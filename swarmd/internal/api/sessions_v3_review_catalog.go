package api

import (
	"context"
	"errors"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// catalogSessionsV3ReviewWorktrees returns navigation metadata, never Git safety
// decisions. Opening Manage must not wait for status/patch scans of every lane.
func (s *Server) catalogSessionsV3ReviewWorktrees(ctx context.Context, principal identity.Principal, req sessionsV3ReviewWorktreesRequest) (map[string]any, error) {
	if req.Automatic || req.ArchiveAll || len(req.ArchiveIDs)+len(req.PromoteIDs)+len(req.CommitIDs)+len(req.LegacyIntegrateIDs) > 0 {
		return nil, errors.New("catalog_only cannot perform review mutations")
	}
	var checkout gitstatus.Snapshot
	var commonDir string
	if path := strings.TrimSpace(req.WorkspacePath); path != "" {
		watch, err := gitstatus.ResolveWatchPaths(ctx, path)
		if err != nil {
			return nil, err
		}
		checkout = gitstatus.Snapshot{HasGit: true, RepoRoot: watch.RepoRoot, WorkspacePath: path}
		commonDir = gitstatus.NormalizePath(watch.CommonDir)
	}
	repository := newSessionsV3ReviewRepository(ctx, checkout, commonDir)
	var search pebblestore.V3SessionSearchResult
	var err error
	if len(compactStrings(req.SessionIDs)) > 0 {
		search, _, err = s.searchSessionsV3ReviewWorktrees(principal, compactStrings(req.SessionIDs))
	} else {
		search, err = searchSessionsV3ReviewWorktreePages(s.sessions.SearchSessions, pebblestore.V3SessionSearchOptions{
			AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, Global: true,
			State: "needs_review", ArchivedMode: "exclude",
		}, sessionsV3ReviewWorktreeLimit)
	}
	if err != nil {
		return nil, err
	}
	pending := make([]sessionreview.Classification, 0, len(search.Items))
	for _, item := range search.Items {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if strings.TrimSpace(req.WorkspacePath) != "" && !repository.searchItemMatchesCheckout(item) {
			continue
		}
		pending = append(pending, sessionreview.Classification{
			SessionID: item.ID, Title: item.Title, UpdatedAt: item.UpdatedAt,
			WorktreeBranch: item.WorktreeBranch, WorktreePath: item.WorktreeRootPath,
			TargetBranch: item.WorktreeBaseBranch, Classification: "retained", Reason: "inspection_pending",
		})
	}
	return map[string]any{
		"ok": true, "inspection_pending": true, "retained": pending,
		"done": []sessionreview.Classification{}, "recently_archived": []map[string]any{},
		"archived_session_ids": []string{}, "complete": !search.Pagination.HasMore,
		"target_detection": "inspection_pending", "comparison": "inspection_pending",
		"grace_period_ms": sessionreview.ParseGraceHours(req.GraceHours).Milliseconds(),
		"checkout_dirty":  nil, "checkout_dirty_count": nil, "blocked_by_checkout_count": nil,
	}, nil
}

// Each worker issues Git commands serially. Limit both goroutines and descendant
// processes rather than launching one expensive status/history scan per session.
func runSessionsV3ReviewWorkers(count int, work func(int)) {
	var wait sync.WaitGroup
	workers := min(count, 4)
	for worker := 0; worker < workers; worker++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for index := worker; index < count; index += workers {
				work(index)
			}
		}()
	}
	wait.Wait()
}

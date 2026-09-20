package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"swarm/packages/swarmd/internal/gitstatus"
	sessionruntime "swarm/packages/swarmd/internal/session"
	"swarm/packages/swarmd/internal/sessionreview"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

const (
	manageSessionsMaxLimit         = 50
	manageSessionsMaxStateBulk     = 200
	manageSessionsMaxRead          = 100
	manageSessionsMaxChars         = 24000
	manageSessionsMaxBatch         = 10
	manageSessionsMaxMutationBatch = 50
	manageSessionsMaxDeployBatch   = 8
	manageSessionsMaxCommitDetail  = 50
	manageSessionsMaxFileDetail    = 100
	manageSessionsMaxEventScan     = 500
)

func manageSessionsDefinition() Definition {
	return Definition{Type: "function", Name: "manage-sessions", Description: "Durable V3 session manager (deploy, list, commit, archive, unarchive, search). Card results render automatically.", Parameters: map[string]any{
		"type": "object", "required": []string{"action"}, "additionalProperties": true,
		"properties": map[string]any{
			"action":                    map[string]any{"type": "string", "description": "inspect|list|list_by_state|review_worktrees|search|get|read_messages|git_status|commit|archive|unarchive|deploy|create|stop|pause|send_message|compact"},
			"commits":                   map[string]any{"type": "array", "minItems": 1, "maxItems": manageSessionsMaxBatch, "description": "Batch commit up to 10 sessions ({session_id, message}).", "items": map[string]any{"type": "object", "required": []string{"session_id", "message"}, "additionalProperties": false, "properties": map[string]any{"session_id": map[string]any{"type": "string"}, "message": map[string]any{"type": "string"}}}},
			"proposals":                 map[string]any{"type": "array", "minItems": 1, "maxItems": manageSessionsMaxDeployBatch, "description": "Deploy proposals (first selected by default).", "items": map[string]any{"type": "object", "required": []string{"prompt"}, "additionalProperties": false, "properties": map[string]any{"title": map[string]any{"type": "string"}, "prompt": map[string]any{"type": "string"}, "mode": map[string]any{"type": "string", "enum": []string{"auto"}}, "agent": map[string]any{"type": "string"}, "workspace_path": map[string]any{"type": "string"}, "worktree_name": map[string]any{"type": "string"}}}},
			"prompt":                    map[string]any{"type": "string"},
			"title":                     map[string]any{"type": "string"},
			"session_id":                map[string]any{"type": "string"},
			"session_ids":               map[string]any{"type": "array", "maxItems": manageSessionsMaxMutationBatch, "description": "For archive/unarchive (up to 50 IDs)", "items": map[string]any{"type": "string"}},
			"category":                  map[string]any{"type": "string", "description": "Sidebar category: video|needs_review|blocked|in_progress|active_chats|archived"},
			"all":                       map[string]any{"type": "boolean", "description": "For archive: archive all unarchived sessions (or all in specified category)"},
			"stop_active":               map[string]any{"type": "boolean", "description": "For archive: stop active execution run if in flight before archiving"},
			"query":                     map[string]any{"type": "string"},
			"search_mode":               map[string]any{"type": "string", "enum": []string{"visible", "durable_log"}},
			"state":                     map[string]any{"type": "string"},
			"workspace_path":            map[string]any{"type": "string"},
			"cursor":                    map[string]any{"type": "string"},
			"limit":                     map[string]any{"type": "integer"},
			"expected_updated_at_by_id": map[string]any{"type": "object", "maxProperties": manageSessionsMaxMutationBatch, "additionalProperties": map[string]any{"type": "integer"}},
		},
	}}
}

func (r *Runtime) executeManageSessions(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	if r == nil || r.sessions == nil {
		return "", errors.New("manage-sessions service is not configured")
	}
	action := strings.ToLower(strings.TrimSpace(stringValue(args["action"])))
	if action == "inspect" {
		snap := r.capacitySnapshot(scope.Principal.AccountScopeID)
		if snap.Unavailable || snap.Error != "" {
			errStr := snap.Error
			if errStr == "" {
				errStr = "execution capacity service is unavailable"
			}
			return "", fmt.Errorf("execution capacity service is unavailable: %s", errStr)
		}
		return marshalManageSessions(map[string]any{
			"tool":                "manage_sessions",
			"action":              "inspect",
			"actions":             []string{"list", "list_by_state", "review_worktrees", "search", "get", "read_messages", "git_status", "commit", "archive", "unarchive", "deploy", "create", "stop", "pause", "send_message", "compact"},
			"categories":          []string{"video", "needs_review", "blocked", "in_progress", "automation", "pinned", "active_chats", "archived"},
			"prompt_free_actions": []string{"inspect", "list", "list_by_state", "review_worktrees", "search", "get", "read_messages", "git_status", "create", "stop", "pause", "send_message", "compact"},
			"limits": map[string]int{
				"results":            manageSessionsMaxLimit,
				"state_bulk_results": manageSessionsMaxStateBulk,
				"messages":           manageSessionsMaxRead,
				"characters":         manageSessionsMaxChars,
				"durable_event_scan": manageSessionsMaxEventScan,
				"commit_batch":       manageSessionsMaxBatch,
				"archive_batch":      manageSessionsMaxMutationBatch,
				"unarchive_batch":    manageSessionsMaxMutationBatch,
				"deploy_batch":       manageSessionsMaxDeployBatch,
			},
			"capacity": map[string]any{
				"account_scope_id":       snap.AccountScopeID,
				"effective_limit":        snap.EffectiveLimit,
				"effective_overall_cap":  snap.EffectiveLimit,
				"total_active":           snap.TotalActive,
				"deployed_active":        snap.DeployedActive,
				"pending":                snap.Pending,
				"pending_scope":          "live admission waiters; durable overflow may also be pending",
				"available_slots":        snap.Available,
				"available":              snap.Available,
				"deployment_batch_bound": snap.DeploymentBatchBound,
				"saved_session_quota":    nil,
				"saved_quota":            snap.SavedQuota,
				"pool_model":             "one shared pool, default 100 ceiling not target; no per-agent deployment execution limit",
			},
			"archive_requires_approval":   true,
			"unarchive_requires_approval": true,
			"deploy_requires_approval":    "always, including permission bypass; allow-always is forbidden",
			"deploy_selection":            "first proposal selected by default; additional proposals require explicit selection in this approval",
			"deploy_authority":            "server resolves agent, workspace, runtime/model, and managed worktree metadata and binds the approval to a canonical digest",
			"archive_semantics":           "atomic preflight and durable mutation for up to 50 sessions; the batch fails without archiving any session when ownership, activity, or version validation fails",
			"unarchive_semantics":         "atomic version-checked restoration for up to 50 archived, non-deleted sessions with canonical session.reactivated events and durable visibility",
			"search_modes": map[string]any{
				"default":           "visible",
				"visible_authority": "canonical user-visible session search",
				"durable_log":       "explicit-only owned-session technical event inspection; never auto-escalate",
			},
			"usage":         "only on an explicit user session-management request; card results are already visible and must not be manually relisted",
			"content_trust": "untrusted",
		})
	}
	switch action {
	case "list", "list_by_state":
		return r.manageSessionsSearch(scope, args)
	case "search":
		mode := strings.ToLower(strings.TrimSpace(stringValue(args["search_mode"])))
		if mode == "" || mode == "visible" {
			return r.manageSessionsSearch(scope, args)
		}
		if mode == "durable_log" {
			return r.manageSessionsDurableLogSearch(scope, args)
		}
		return "", fmt.Errorf("manage-sessions search_mode %q is not supported", mode)
	case "review_worktrees":
		return r.manageSessionsReviewWorktrees(ctx, scope, args)
	case "get":
		return r.manageSessionsGet(scope, stringValue(args["session_id"]))
	case "read_messages":
		return r.manageSessionsRead(scope, args)
	case "git_status":
		return r.manageSessionsGit(ctx, scope, args)
	case "commit":
		return r.manageSessionsCommit(ctx, scope, args)
	case "archive":
		return r.manageSessionsArchive(scope, args)
	case "unarchive":
		return r.manageSessionsUnarchive(scope, args)
	case "deploy":
		return "", errors.New("deploy requires an approved canonical deployment manifest")
	case "create":
		return r.manageSessionsCreate(ctx, scope, args)
	case "stop", "pause":
		return r.manageSessionsStop(scope, args)
	case "send_message":
		return r.manageSessionsSendMessage(ctx, scope, args)
	case "compact":
		return r.manageSessionsCompact(ctx, scope, args)
	default:
		return "", fmt.Errorf("manage-sessions action %q is not supported", action)
	}
}

func (r *Runtime) manageSessionsSearch(scope WorkspaceScope, args map[string]any) (string, error) {
	action := strings.ToLower(strings.TrimSpace(stringValue(args["action"])))
	bulkByState := action == "list_by_state"
	sessionID := strings.TrimSpace(stringValue(args["session_id"]))
	if sessionID != "" && !bulkByState {
		return r.manageSessionScopedSearch(scope, sessionID, args)
	}
	categoryFilter := strings.ToLower(strings.TrimSpace(stringValue(args["category"])))
	stateArg := stringValue(args["state"])
	if strings.EqualFold(strings.TrimSpace(stateArg), "video") && categoryFilter == "" {
		categoryFilter = "video"
		stateArg = ""
	}
	limit := boundedInt(args["limit"], 20, manageSessionsMaxLimit)
	if bulkByState || categoryFilter != "" || boolValue(args["all"]) {
		limit = boundedInt(args["limit"], manageSessionsMaxStateBulk, manageSessionsMaxStateBulk)
	}
	paths := stringSliceValue(args["workspace_paths"])
	if p := strings.TrimSpace(stringValue(args["workspace_path"])); p != "" {
		paths = append(paths, p)
	}
	global := boolValue(args["global"])
	_, globalExplicit := args["global"]
	if len(paths) == 0 && !globalExplicit {
		global = true
	}
	if !global && len(paths) == 0 {
		paths = append(paths, scope.Roots...)
		if len(paths) == 0 && scope.PrimaryPath != "" {
			paths = []string{scope.PrimaryPath}
		}
	}
	beforeAt, beforeID, err := pebblestore.DecodeV3SessionSearchCursor(stringValue(args["cursor"]))
	if err != nil {
		return "", err
	}
	state := normalizeManageSessionStateFilter(stateArg)
	if bulkByState && state == "" && categoryFilter == "" {
		return "", errors.New("list_by_state requires state")
	}
	archivedMode := stringValue(args["archived_mode"])
	if categoryFilter == "archived" {
		archivedMode = "only"
	} else if archivedMode == "" && action == "list" && categoryFilter == "" && state == "" {
		archivedMode = "include"
	}
	opts := pebblestore.V3SessionSearchOptions{AccountScopeID: scope.Principal.AccountScopeID, UserID: scope.Principal.UserID, Global: global, WorkspacePaths: paths, Query: stringValue(args["query"]), Queries: stringSliceValue(args["queries"]), State: state, ArchivedMode: archivedMode, Limit: limit, BeforeUpdatedAt: beforeAt, BeforeSessionID: beforeID}
	allItems := make([]pebblestore.V3SessionSearchItem, 0, limit)
	var nextCursor string
	hasMore := false
	for {
		if bulkByState || categoryFilter != "" {
			opts.Limit = min(manageSessionsMaxLimit, limit-len(allItems))
		}
		result, searchErr := r.sessions.SearchSessions(opts)
		if searchErr != nil {
			return "", searchErr
		}
		allItems = append(allItems, result.Items...)
		nextCursor, hasMore = result.Pagination.NextCursor, result.Pagination.HasMore
		if (!bulkByState && categoryFilter == "") || !hasMore || len(allItems) >= limit {
			break
		}
		beforeAt, beforeID, err = pebblestore.DecodeV3SessionSearchCursor(nextCursor)
		if err != nil {
			return "", err
		}
		opts.BeforeUpdatedAt, opts.BeforeSessionID = beforeAt, beforeID
	}

	videoItems := make([]any, 0)
	needsReviewItems := make([]any, 0)
	blockedItems := make([]any, 0)
	inProgressItems := make([]any, 0)
	activeChatItems := make([]any, 0)
	archivedItems := make([]any, 0)
	allRecords := make([]map[string]any, 0, len(allItems))

	for _, item := range allItems {
		normalized := item.Attention.State
		if normalized == "" {
			normalized = manageSessionState(item.Lifecycle)
		}
		rec := manageSessionRecord(item, normalized, manageSessionWorkspaceSlug(item.WorkspaceName, item.WorkspacePath, allItems))
		allRecords = append(allRecords, rec)

		cat, _ := rec["category"].(string)
		switch cat {
		case "video":
			videoItems = append(videoItems, rec)
		case "needs_review":
			needsReviewItems = append(needsReviewItems, rec)
		case "blocked":
			blockedItems = append(blockedItems, rec)
		case "in_progress":
			inProgressItems = append(inProgressItems, rec)
		case "archived":
			archivedItems = append(archivedItems, rec)
		default:
			activeChatItems = append(activeChatItems, rec)
		}
	}

	items := make([]any, 0, len(allRecords))
	if categoryFilter != "" && categoryFilter != "all" {
		for _, rec := range allRecords {
			if rec["category"] == categoryFilter {
				items = append(items, rec)
			}
		}
	} else if action == "list" {
		items = append(items, videoItems...)
		items = append(items, needsReviewItems...)
		items = append(items, blockedItems...)
		items = append(items, inProgressItems...)
		items = append(items, activeChatItems...)
		items = append(items, archivedItems...)
	} else {
		for _, rec := range allRecords {
			items = append(items, rec)
		}
	}

	sidebarCategories := []map[string]any{
		{"id": "video", "label": "Video Sessions", "count": len(videoItems), "items": videoItems},
		{"id": "needs_review", "label": "Needs Review", "count": len(needsReviewItems), "items": needsReviewItems},
		{"id": "blocked", "label": "Blocked", "count": len(blockedItems), "items": blockedItems},
		{"id": "in_progress", "label": "In Progress", "count": len(inProgressItems), "items": inProgressItems},
		{"id": "active_chats", "label": "Active Chats", "count": len(activeChatItems), "items": activeChatItems},
		{"id": "archived", "label": "Archived Sessions", "count": len(archivedItems), "items": archivedItems},
	}
	categoriesMap := map[string]any{
		"video":        videoItems,
		"needs_review": needsReviewItems,
		"blocked":      blockedItems,
		"in_progress":  inProgressItems,
		"active_chats": activeChatItems,
		"archived":     archivedItems,
	}
	categoryCounts := map[string]int{
		"video":          len(videoItems),
		"needs_review":   len(needsReviewItems),
		"blocked":        len(blockedItems),
		"in_progress":    len(inProgressItems),
		"active_chats":   len(activeChatItems),
		"archived":       len(archivedItems),
		"total_active":   len(videoItems) + len(needsReviewItems) + len(blockedItems) + len(inProgressItems) + len(activeChatItems),
		"total_archived": len(archivedItems),
	}

	continuation := "pass next_cursor as cursor only when the user needs more results; do not repeat visible items"
	if bulkByState || categoryFilter != "" {
		continuation = "the server already paged through the bounded state result; pass next_cursor only if has_more is true and the user needs the next bounded batch"
	}
	resp := map[string]any{
		"action":             action,
		"search_mode":        "visible",
		"source":             "visible_sessions",
		"items":              items,
		"sidebar_categories": sidebarCategories,
		"categories":         categoriesMap,
		"category_counts":    categoryCounts,
		"next_cursor":        nextCursor,
		"has_more":           hasMore,
		"complete":           !hasMore,
		"bounded_limit":      limit,
		"content_trust":      "untrusted",
		"continuation":       continuation,
	}
	if categoryFilter != "" {
		resp["filtered_category"] = categoryFilter
	}
	return marshalManageSessions(resp)
}

func (r *Runtime) manageSessionsDurableLogSearch(scope WorkspaceScope, args map[string]any) (string, error) {
	id := strings.TrimSpace(stringValue(args["session_id"]))
	session, _, err := r.ownedManageSession(scope, id)
	if err != nil {
		return "", err
	}
	queries := append([]string{stringValue(args["query"])}, stringSliceValue(args["queries"])...)
	needles := make([]string, 0, len(queries))
	for _, query := range queries {
		query = strings.ToLower(strings.TrimSpace(query))
		if query != "" {
			needles = append(needles, query)
		}
	}
	if len(needles) == 0 {
		return "", errors.New("durable_log search requires query or queries")
	}
	resultLimit := boundedInt(args["limit"], 20, manageSessionsMaxLimit)
	charLimit := boundedInt(args["max_chars"], 12000, manageSessionsMaxChars)
	beforeSeq := uint64Value(args["before_seq"])
	events, err := r.sessions.ListSessionEventsBefore(id, beforeSeq, manageSessionsMaxEventScan+1)
	if err != nil {
		return "", err
	}
	matches := make([]pebblestore.V3SessionEvent, 0, resultLimit)
	characters := 0
	characterTruncated := false
	resultTruncated := false
	nextBeforeSeq := uint64(0)
	scanned := 0
	scanTruncated := len(events) > manageSessionsMaxEventScan
	if scanTruncated {
		events = events[:manageSessionsMaxEventScan]
	}
	for _, event := range events {
		scanned++
		haystack := strings.ToLower(event.EventType + "\n" + string(event.Payload))
		matched := false
		for _, needle := range needles {
			if strings.Contains(haystack, needle) {
				matched = true
				break
			}
		}
		if !matched {
			nextBeforeSeq = event.Seq
			continue
		}
		if len(matches) >= resultLimit {
			resultTruncated = true
			nextBeforeSeq = event.Seq + 1
			break
		}
		encoded, marshalErr := json.Marshal(event)
		if marshalErr != nil {
			return "", marshalErr
		}
		if characters+len(encoded) > charLimit {
			characterTruncated = true
			nextBeforeSeq = event.Seq + 1
			break
		}
		matches = append(matches, event)
		characters += len(encoded)
		nextBeforeSeq = event.Seq
	}
	hasMore := characterTruncated || resultTruncated || scanTruncated
	if !hasMore {
		nextBeforeSeq = 0
	}
	return marshalManageSessions(map[string]any{
		"action": "search", "search_mode": "durable_log", "source": "durable_v3_session_events",
		"session_id": id, "title": session.Title, "events": matches, "query_count": len(needles),
		"scanned_events": scanned, "scan_limit": manageSessionsMaxEventScan, "result_limit": resultLimit,
		"characters": characters, "character_limit": charLimit, "result_truncated": resultTruncated,
		"scan_truncated": scanTruncated, "character_truncated": characterTruncated,
		"has_more": hasMore, "complete": !hasMore, "next_before_seq": nextBeforeSeq,
		"content_trust": "untrusted", "continuation": "pass next_before_seq as before_seq only when more technical event-log results are needed",
	})
}

func (r *Runtime) manageSessionsReviewWorktrees(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	checkoutPath := strings.TrimSpace(stringValue(args["workspace_path"]))
	if checkoutPath == "" {
		checkoutPath = strings.TrimSpace(scope.PrimaryPath)
	}
	if checkoutPath == "" {
		return "", errors.New("review_worktrees requires a current checkout workspace")
	}
	if !pathWithinScope(checkoutPath, scope.Roots, scope.PrimaryPath) {
		return "", errors.New("review_worktrees checkout is outside the active workspace scope")
	}
	checkoutWatch, err := gitstatus.ResolveWatchPaths(ctx, checkoutPath)
	if err != nil || strings.TrimSpace(checkoutWatch.CommonDir) == "" {
		return "", errors.New("review_worktrees current checkout is not a Git repository")
	}
	checkout, err := gitstatus.SnapshotForResolvedPaths(ctx, checkoutPath, checkoutWatch, gitstatus.Options{})
	if err != nil {
		return "", fmt.Errorf("inspect review_worktrees current checkout: %w", err)
	}

	searchOpts := pebblestore.V3SessionSearchOptions{
		AccountScopeID: scope.Principal.AccountScopeID,
		UserID:         scope.Principal.UserID,
		Global:         true,
		State:          "needs_review",
		ArchivedMode:   "exclude",
		Limit:          manageSessionsMaxLimit,
	}
	needsReview := make([]pebblestore.V3SessionSearchItem, 0, manageSessionsMaxStateBulk)
	hasMoreNeedsReview := false
	for len(needsReview) < manageSessionsMaxStateBulk {
		result, searchErr := r.sessions.SearchSessions(searchOpts)
		if searchErr != nil {
			return "", searchErr
		}
		needsReview = append(needsReview, result.Items...)
		hasMoreNeedsReview = result.Pagination.HasMore
		if !result.Pagination.HasMore || strings.TrimSpace(result.Pagination.NextCursor) == "" {
			break
		}
		beforeAt, beforeID, cursorErr := pebblestore.DecodeV3SessionSearchCursor(result.Pagination.NextCursor)
		if cursorErr != nil {
			return "", cursorErr
		}
		searchOpts.BeforeUpdatedAt, searchOpts.BeforeSessionID = beforeAt, beforeID
		remaining := manageSessionsMaxStateBulk - len(needsReview)
		searchOpts.Limit = min(manageSessionsMaxLimit, remaining)
	}

	archiveCandidates := make([]any, 0)
	followUpCandidates := make([]any, 0)
	inspectionErrors := make([]any, 0)
	worktreeSessions := 0
	otherRepositorySessions := 0
	for _, item := range needsReview {
		if !item.WorktreeEnabled || strings.TrimSpace(item.WorktreeBranch) == "" {
			continue
		}
		worktreeSessions++
		session, archived, sessionErr := r.ownedManageSession(scope, item.ID)
		if sessionErr != nil || archived {
			inspectionErrors = append(inspectionErrors, manageSessionsWorktreeReviewError(item, "session_unavailable", sessionErr))
			continue
		}
		worktreePath := strings.TrimSpace(session.WorktreeRootPath)
		if worktreePath == "" {
			worktreePath = strings.TrimSpace(session.WorkspacePath)
		}
		worktreeWatch, watchErr := gitstatus.ResolveWatchPaths(ctx, worktreePath)
		if watchErr != nil || strings.TrimSpace(worktreeWatch.CommonDir) == "" {
			inspectionErrors = append(inspectionErrors, manageSessionsWorktreeReviewError(item, "worktree_unavailable", watchErr))
			continue
		}
		if gitstatus.NormalizePath(worktreeWatch.CommonDir) != gitstatus.NormalizePath(checkoutWatch.CommonDir) {
			otherRepositorySessions++
			continue
		}
		worktree, snapshotErr := gitstatus.SnapshotForResolvedPaths(ctx, worktreePath, worktreeWatch, gitstatus.Options{RecentLimit: 3, IncludeDetails: true})
		if snapshotErr != nil {
			inspectionErrors = append(inspectionErrors, manageSessionsWorktreeReviewError(item, "git_status_failed", snapshotErr))
			continue
		}
		missingCommits, missingCommitCount, equivalentCount, cherryErr := manageSessionsMissingCommits(ctx, checkout.RepoRoot, worktree.HeadOID)
		if cherryErr != nil {
			inspectionErrors = append(inspectionErrors, manageSessionsWorktreeReviewError(item, "commit_comparison_failed", cherryErr))
			continue
		}

		record := map[string]any{
			"session_id":                session.ID,
			"title":                     session.Title,
			"updated_at":                session.UpdatedAt,
			"worktree_branch":           session.WorktreeBranch,
			"worktree_head":             worktree.HeadOID,
			"clean":                     worktree.Clean,
			"dirty_count":               worktree.DirtyCount,
			"staged_count":              worktree.StagedCount,
			"modified_count":            worktree.ModifiedCount,
			"untracked_count":           worktree.UntrackedCount,
			"conflict_count":            worktree.ConflictCount,
			"missing_commit_count":      missingCommitCount,
			"missing_commits_truncated": missingCommitCount > len(missingCommits),
			"equivalent_commit_count":   equivalentCount,
			"missing_commits":           missingCommits,
			"navigation":                manageSessionNavigation(session.ID, session.WorkspacePath, session.WorkspaceName, manageSessionWorkspaceSlug(session.WorkspaceName, session.WorkspacePath, needsReview)),
		}
		if !worktree.Clean {
			record["classification"] = "follow_up"
			record["reason"] = "uncommitted_work"
			fileLimit := min(len(worktree.Files), manageSessionsMaxFileDetail)
			record["files"] = worktree.Files[:fileLimit]
			record["files_truncated"] = len(worktree.Files) > fileLimit
			followUpCandidates = append(followUpCandidates, record)
			continue
		}
		if missingCommitCount > 0 {
			record["classification"] = "follow_up"
			record["reason"] = "commits_missing_from_current_checkout"
			followUpCandidates = append(followUpCandidates, record)
			continue
		}
		record["classification"] = "archive_candidate"
		record["reason"] = "clean_and_all_branch_commits_present"
		archiveCandidates = append(archiveCandidates, record)
	}

	return marshalManageSessions(map[string]any{
		"action":                    "review_worktrees",
		"current_checkout":          map[string]any{"branch": checkout.Branch, "head_oid": checkout.HeadOID, "repo_root": checkout.RepoRoot},
		"needs_review_count":        len(needsReview),
		"bounded_limit":             manageSessionsMaxStateBulk,
		"complete":                  !hasMoreNeedsReview,
		"has_more":                  hasMoreNeedsReview,
		"worktree_session_count":    worktreeSessions,
		"other_repository_count":    otherRepositorySessions,
		"archive_candidate_count":   len(archiveCandidates),
		"follow_up_candidate_count": len(followUpCandidates),
		"inspection_error_count":    len(inspectionErrors),
		"archive_candidates":        archiveCandidates,
		"follow_up_candidates":      followUpCandidates,
		"inspection_errors":         inspectionErrors,
		"comparison":                "Each commit reachable from a worktree head but not current HEAD is checked by git cherry; patch-equivalent and conflict-resolved cherry-picks with matching author identity, message, and changed paths count as present. Dirty files are always follow-up work.",
		"archive_requires_approval": true,
		"archive_batch_limit":       manageSessionsMaxMutationBatch,
		"content_trust":             "untrusted",
		"continuation":              "Offer to archive archive_candidates in approval-gated batches and manage follow_up_candidates; do not archive automatically.",
	})
}

func manageSessionsMissingCommits(ctx context.Context, repoRoot, headOID string) ([]map[string]any, int, int, error) {
	if strings.TrimSpace(headOID) == "" {
		return nil, 0, 0, errors.New("worktree HEAD is unavailable")
	}
	output, err := manageSessionsRunGit(ctx, repoRoot, "cherry", "HEAD", headOID)
	if err != nil {
		return nil, 0, 0, err
	}
	missing := make([]map[string]any, 0, manageSessionsMaxCommitDetail)
	missingCount := 0
	equivalentCount := 0
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		if fields[0] == "-" {
			equivalentCount++
			continue
		}
		if fields[0] != "+" {
			continue
		}
		reconciled, reconcileErr := sessionreview.CommitMatchesResolvedIntegration(ctx, sessionreview.ExecGitRunner{}, repoRoot, "HEAD", fields[1])
		if reconcileErr != nil {
			return nil, missingCount, equivalentCount, reconcileErr
		}
		if reconciled {
			equivalentCount++
			continue
		}
		missingCount++
		if len(missing) >= manageSessionsMaxCommitDetail {
			continue
		}
		metadata, metadataErr := manageSessionsRunGit(ctx, repoRoot, "show", "-s", "--format=%H%x09%h%x09%cI%x09%s", fields[1])
		if metadataErr != nil {
			return nil, missingCount, equivalentCount, metadataErr
		}
		parts := strings.SplitN(strings.TrimSpace(metadata), "\t", 4)
		if len(parts) != 4 {
			return nil, missingCount, equivalentCount, fmt.Errorf("unexpected commit metadata for %s", fields[1])
		}
		missing = append(missing, map[string]any{"commit": parts[0], "commit_short": parts[1], "committed_at": parts[2], "subject": parts[3]})
	}
	return missing, missingCount, equivalentCount, nil
}

func manageSessionsRunGit(ctx context.Context, repoRoot string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, "git", args...)
	cmd.Dir = repoRoot
	output, err := cmd.CombinedOutput()
	if commandCtx.Err() != nil {
		return "", fmt.Errorf("git %s timed out", strings.Join(args, " "))
	}
	if err != nil {
		message := strings.TrimSpace(string(output))
		if message == "" {
			message = err.Error()
		}
		return "", fmt.Errorf("git %s failed: %s", strings.Join(args, " "), message)
	}
	return string(output), nil
}

func manageSessionsWorktreeReviewError(item pebblestore.V3SessionSearchItem, reason string, err error) map[string]any {
	record := map[string]any{"session_id": item.ID, "title": item.Title, "updated_at": item.UpdatedAt, "worktree_branch": item.WorktreeBranch, "reason": reason}
	if err != nil {
		record["error"] = err.Error()
	}
	return record
}

func (r *Runtime) manageSessionsGet(scope WorkspaceScope, id string) (string, error) {
	s, archived, err := r.ownedManageSession(scope, id)
	if err != nil {
		return "", err
	}
	state := "archived"
	if !archived {
		state, err = r.manageSessionAuthoritativeState(s)
		if err != nil {
			return "", err
		}
	}
	version := s.UpdatedAt
	if archived {
		tombstone, ok, tombstoneErr := r.sessions.GetSessionTombstone(s.ID)
		if tombstoneErr != nil {
			return "", tombstoneErr
		}
		if !ok || tombstone.Deleted || !tombstone.Archived {
			return "", errors.New("session not found")
		}
		version = tombstone.UpdatedAt
	}
	slug := manageSessionWorkspaceSlug(s.WorkspaceName, s.WorkspacePath, nil)
	isRunning := false
	if s.Lifecycle != nil {
		isRunning = s.Lifecycle.Active
	}

	category := ManageSessionSidebarCategory(archived, s.Metadata, pebblestore.V3SessionAttentionSummary{State: state}, state)
	if plan, ok, planErr := r.sessions.GetActivePlan(s.ID); planErr == nil && ok && plan.Document != nil {
		att := pebblestore.V3SessionAttentionSummary{State: state}
		if plan.Document.ActiveCheckpointID != "" {
			att.CheckpointID = plan.Document.ActiveCheckpointID
			for _, cp := range plan.Document.Checkpoints {
				if strings.TrimSpace(cp.ID) == strings.TrimSpace(plan.Document.ActiveCheckpointID) {
					att.CheckpointStatus = cp.Status
					break
				}
			}
		}
		if plan.Document.ExecutionState != nil {
			att.ExecutionStatus = plan.Document.ExecutionState.Status
			att.LastOutcome = plan.Document.ExecutionState.LastOutcome
		}
		category = ManageSessionSidebarCategory(archived, s.Metadata, att, state)
	}

	rec := map[string]any{
		"action":           "get",
		"id":               s.ID,
		"title":            s.Title,
		"updated_at":       version,
		"created_at":       s.CreatedAt,
		"archived":         archived,
		"state":            state,
		"category":         category,
		"sidebar_category": category,
		"sidebar_group":    category,
		"is_running":       isRunning,
		"workspace_path":   s.WorkspacePath,
		"workspace_name":   s.WorkspaceName,
		"message_count":    s.MessageCount,
		"last_message_at":  s.LastMessageAt,
		"navigation":       manageSessionNavigation(s.ID, s.WorkspacePath, s.WorkspaceName, slug),
		"content_trust":    "untrusted",
	}
	if IsVideoSessionMetadata(s.Metadata) {
		rec["is_video"] = true
	}
	if s.Mode != "" {
		rec["mode"] = s.Mode
	}
	if s.Preference.Provider != "" || s.Preference.Model != "" {
		rec["preference"] = map[string]any{
			"provider": s.Preference.Provider,
			"model":    s.Preference.Model,
			"thinking": s.Preference.Thinking,
		}
	}
	if agentName := stringValue(s.Metadata["agent_name"]); agentName != "" {
		rec["agent"] = agentName
	}
	if s.WorktreeEnabled {
		rec["worktree"] = map[string]any{
			"enabled":     true,
			"branch":      s.WorktreeBranch,
			"base_branch": s.WorktreeBaseBranch,
			"root_path":   s.WorktreeRootPath,
		}
	}

	if runState, ok, stateErr := r.getSessionRunState(s.ID); stateErr == nil && ok {
		rec["run_state"] = map[string]any{
			"active":                 runState.Active,
			"status":                 runState.Status,
			"run_id":                 runState.RunID,
			"epoch_id":               runState.EpochID,
			"checkpoint_id":          runState.CheckpointID,
			"attempt_id":             runState.AttemptID,
			"started_at":             runState.StartedAt,
			"completed_at":           runState.CompletedAt,
			"duration_ms":            runState.DurationMs,
			"cumulative_duration_ms": runState.CumulativeDurationMs,
			"blocked_reason":         runState.BlockedReason,
		}
		if runState.Active {
			rec["is_running"] = true
		} else {
			rec["is_running"] = false
		}
	}

	if !archived {
		if plan, ok, planErr := r.sessions.GetActivePlan(s.ID); planErr == nil && ok && plan.Document != nil {
			planSummary := map[string]any{
				"id":     plan.ID,
				"title":  plan.Title,
				"status": plan.Status,
			}
			if plan.Document.ActiveCheckpointID != "" {
				planSummary["active_checkpoint_id"] = plan.Document.ActiveCheckpointID
			}
			if plan.Document.ExecutionState != nil {
				planSummary["execution_status"] = plan.Document.ExecutionState.Status
				planSummary["last_outcome"] = plan.Document.ExecutionState.LastOutcome
			}
			checkpoints := make([]map[string]any, 0, len(plan.Document.Checkpoints))
			for _, cp := range plan.Document.Checkpoints {
				cpSummary := map[string]any{
					"id":     cp.ID,
					"title":  cp.Title,
					"status": cp.Status,
					"order":  cp.Order,
				}
				if len(cp.Subtasks) > 0 {
					completed := 0
					for _, st := range cp.Subtasks {
						if st.Status == "completed" {
							completed++
						}
					}
					cpSummary["subtasks_completed"] = completed
					cpSummary["subtasks_total"] = len(cp.Subtasks)
				}
				if strings.TrimSpace(cp.ID) == strings.TrimSpace(plan.Document.ActiveCheckpointID) {
					activeCp := map[string]any{
						"id":                  cp.ID,
						"title":               cp.Title,
						"status":              cp.Status,
						"objective":           cp.Objective,
						"tasks":               cp.Tasks,
						"acceptance_criteria": cp.AcceptanceCriteria,
						"active_subtask_id":   cp.ActiveSubtaskID,
					}
					for _, st := range cp.Subtasks {
						if strings.TrimSpace(st.ID) == strings.TrimSpace(cp.ActiveSubtaskID) {
							activeCp["current_subtask"] = st.Title
							break
						}
					}
					planSummary["active_checkpoint"] = activeCp
				}
				checkpoints = append(checkpoints, cpSummary)
			}
			planSummary["checkpoints"] = checkpoints
			rec["active_plan"] = planSummary
		}
	}

	if permissions, permErr := r.listSessionPermissions(s.ID, 50); permErr == nil && len(permissions) > 0 {
		pending := make([]map[string]any, 0)
		for _, p := range permissions {
			pStatus := strings.ToLower(strings.TrimSpace(p.Status))
			if pStatus == "pending" || pStatus == "waiting_approval" || pStatus == "needs_approval" || pStatus == "waiting_review" {
				pending = append(pending, map[string]any{
					"id":                   p.ID,
					"tool_name":            p.ToolName,
					"requirement":          p.Requirement,
					"status":               p.Status,
					"created_at":           p.CreatedAt,
					"permission_requested": p.PermissionRequested,
				})
			}
		}
		if len(pending) > 0 {
			rec["pending_permissions"] = pending
		}
	}

	if usage, ok, usageErr := r.getUsageSummary(s.ID); usageErr == nil && ok {
		rec["usage"] = map[string]any{
			"input_tokens":       usage.InputTokens,
			"output_tokens":      usage.OutputTokens,
			"cache_read_tokens":  usage.CacheReadTokens,
			"cache_write_tokens": usage.CacheWriteTokens,
			"total_tokens":       usage.TotalTokens,
			"estimated_cost_usd": usage.EstimatedCostUSD,
		}
	}

	if msgs, msgErr := r.listSessionMessageTail(s.ID, 1); msgErr == nil && len(msgs) > 0 {
		m := msgs[len(msgs)-1]
		rec["last_message"] = map[string]any{
			"id":         m.ID,
			"seq":        m.GlobalSeq,
			"role":       m.Role,
			"content":    truncateUTF8Bytes(m.Content, 500),
			"created_at": m.CreatedAt,
		}
	}

	if videoContext := manageSessionVideoContext(s.Metadata); videoContext != nil {
		rec["video_context"] = videoContext
	}
	return marshalManageSessions(rec)
}

func manageSessionVideoContext(metadata map[string]any) map[string]any {
	if !strings.EqualFold(strings.TrimSpace(stringValue(metadata["creative_mode"])), "video") &&
		!strings.EqualFold(strings.TrimSpace(stringValue(metadata["experience"])), "video_studio") {
		return nil
	}
	projectID := strings.TrimSpace(stringValue(metadata["video_project_id"]))
	revisionID := strings.TrimSpace(stringValue(metadata["video_revision_id"]))
	if projectID == "" || revisionID == "" {
		return nil
	}
	context := map[string]any{
		"attached":                true,
		"durable":                 true,
		"destination_project_id":  truncateUTF8Bytes(projectID, 256),
		"destination_revision_id": truncateUTF8Bytes(revisionID, 256),
	}
	for outputKey, metadataKey := range map[string]string{
		"source_session_id":  "source_session_id",
		"source_project_id":  "source_video_project_id",
		"source_revision_id": "source_video_revision_id",
	} {
		if value := truncateUTF8Bytes(strings.TrimSpace(stringValue(metadata[metadataKey])), 256); value != "" {
			context[outputKey] = value
		}
	}
	return context
}

func (r *Runtime) manageSessionsRead(scope WorkspaceScope, args map[string]any) (string, error) {
	id := stringValue(args["session_id"])
	session, _, err := r.ownedManageSession(scope, id)
	if err != nil {
		return "", err
	}
	limit := boundedInt(args["limit"], 30, manageSessionsMaxRead)
	mode := strings.ToLower(strings.TrimSpace(stringValue(args["mode"])))
	roleFilter := strings.ToLower(strings.TrimSpace(stringValue(args["role"])))
	var msgs []pebblestore.MessageSnapshot
	switch mode {
	case "before":
		msgs, err = r.listSessionMessagesBefore(id, uint64Value(args["before_seq"]), limit)
	case "after":
		msgs, err = r.listSessionMessages(id, uint64Value(args["after_seq"]), limit)
	case "around":
		anchor := uint64Value(args["around_seq"])
		before := limit / 2
		msgs, err = r.listSessionMessagesBefore(id, anchor, before)
		if err == nil {
			after, _ := r.listSessionMessages(id, anchor-1, limit-len(msgs))
			msgs = append(msgs, after...)
		}
	default:
		msgs, err = r.listSessionMessageTail(id, limit)
	}
	if err != nil {
		return "", err
	}
	budget := boundedInt(args["max_chars"], 12000, manageSessionsMaxChars)
	out := make([]any, 0, len(msgs))
	used := 0
	for _, m := range msgs {
		if roleFilter != "" && !strings.EqualFold(m.Role, roleFilter) {
			continue
		}
		text := m.Content
		remain := budget - used
		if remain <= 0 {
			break
		}
		if len(text) > remain {
			text = truncateUTF8Bytes(text, remain)
		}
		used += len(text)
		out = append(out, map[string]any{"id": m.ID, "seq": m.GlobalSeq, "role": m.Role, "content": text, "created_at": m.CreatedAt})
	}
	return marshalManageSessions(map[string]any{"action": "read_messages", "session_id": id, "title": session.Title, "mode": mode, "messages": out, "characters": used, "content_trust": "untrusted", "next_before_seq": firstMessageSeq(msgs), "next_after_seq": lastMessageSeq(msgs)})
}

func (r *Runtime) manageSessionsGit(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	ids := stringSliceValue(args["session_ids"])
	if id := stringValue(args["session_id"]); id != "" {
		ids = append(ids, id)
	}
	ids = uniqueStrings(ids, manageSessionsMaxBatch)
	results := make([]any, 0, len(ids))
	for _, id := range ids {
		s, _, err := r.ownedManageSession(scope, id)
		if err != nil {
			return "", err
		}
		path := s.WorkspacePath
		if s.WorktreeEnabled && s.WorktreeRootPath != "" {
			path = s.WorktreeRootPath
		}
		baseBranch := s.WorktreeBaseBranch
		baseCommit := strings.TrimSpace(mapString(s.Metadata, "base_commit"))
		selectedLane := false
		requested := strings.TrimSpace(stringValue(args["workspace_path"]))
		if requested != "" && filepath.Clean(requested) != filepath.Clean(path) && filepath.Clean(requested) != filepath.Clean(mapString(s.Metadata, "swarm_v3_source_workspace_path")) {
			lane, laneErr := r.selectedRepositoryLane(scope, s, requested, "")
			if laneErr != nil {
				return "", fmt.Errorf("select session repository lane: %w", laneErr)
			}
			path, baseCommit, selectedLane = lane.WorkspacePath, lane.BaseCommit, true
			sourceState, inspectErr := r.worktrees.InspectTaskWorkspace(lane.SourcePath)
			if inspectErr != nil {
				return "", inspectErr
			}
			baseBranch = sourceState.BranchName
		}
		path, canonicalErr := canonicalExistingPath(path)
		if canonicalErr != nil {
			return "", fmt.Errorf("canonicalize session %s repository: %w", id, canonicalErr)
		}
		if !selectedLane && !canonicalPathWithinScope(path, scope.Roots, scope.PrimaryPath) {
			allowed, allowErr := r.accountOwnsSessionGitPath(ctx, scope, s, path)
			if allowErr != nil {
				return "", fmt.Errorf("validate session %s account-owned repository: %w", id, allowErr)
			}
			if !allowed {
				return "", fmt.Errorf("session %s repository is not account-owned", id)
			}
		}
		snap, e := gitstatus.SnapshotForPath(ctx, path, gitstatus.Options{BaseBranch: baseBranch, RecentLimit: 3, IncludeDetails: true})
		if e != nil {
			results = append(results, map[string]any{"session_id": id, "status": "error", "error": e.Error()})
			continue
		}
		results = append(results, map[string]any{"session_id": id, "title": s.Title, "status": "available", "branch": snap.Branch, "base_branch": baseBranch, "base_commit": baseCommit, "clean": snap.Clean, "dirty_count": snap.DirtyCount, "staged_count": snap.StagedCount, "modified_count": snap.ModifiedCount, "untracked_count": snap.UntrackedCount, "conflict_count": snap.ConflictCount, "ahead": snap.AheadCount, "behind": snap.BehindCount, "head_oid": snap.HeadOID, "repo_root": snap.RepoRoot, "worktree_path": path, "worktree_enabled": s.WorktreeEnabled, "recoverable": s.WorktreeEnabled && !snap.Clean, "files": snap.Files, "recent_commits": snap.RecentCommits})
	}
	return marshalManageSessions(map[string]any{"action": "git_status", "items": results})
}

// accountOwnsSessionGitPath authorizes read-only Git inspection independently of
// the calling session's active workspace. Session ownership is necessary but not
// sufficient: the repository must also be covered by an account-owned workspace
// binding, or be a managed linked worktree of one of those repositories.
func (r *Runtime) accountOwnsSessionGitPath(ctx context.Context, scope WorkspaceScope, session pebblestore.SessionSnapshot, path string) (bool, error) {
	if r.workspace == nil {
		return managedSessionWorktreeSharesRepositories(ctx, session, path, append(append([]string(nil), scope.Roots...), scope.PrimaryPath)), nil
	}
	workspaceScope, err := r.workspace.ScopeForPathForPrincipal(scope.Principal, path)
	if err != nil {
		return false, err
	}
	if workspaceScope.Matched {
		return true, nil
	}
	entries, err := r.workspace.ListKnownForPrincipal(scope.Principal, 100000)
	if err != nil {
		return false, err
	}
	roots := make([]string, 0, len(entries)*2)
	for _, entry := range entries {
		roots = append(roots, entry.Path)
		roots = append(roots, entry.Directories...)
	}
	return managedSessionWorktreeSharesRepositories(ctx, session, path, roots), nil
}

// managedSessionWorktreeSharesRepositories requires Git's common directory to
// prove that an out-of-tree managed worktree belongs to an owned repository.
func managedSessionWorktreeSharesRepositories(ctx context.Context, session pebblestore.SessionSnapshot, path string, roots []string) bool {
	if !session.WorktreeEnabled || strings.TrimSpace(session.WorktreeRootPath) == "" || strings.TrimSpace(session.WorktreeBranch) == "" {
		return false
	}
	worktreePath, err := canonicalExistingPath(session.WorktreeRootPath)
	if err != nil {
		return false
	}
	canonicalPath, err := canonicalExistingPath(path)
	if err != nil || worktreePath != canonicalPath {
		return false
	}
	worktreeGit, err := gitstatus.ResolveWatchPaths(ctx, worktreePath)
	if err != nil || strings.TrimSpace(worktreeGit.CommonDir) == "" {
		return false
	}
	worktreeCommon := gitstatus.NormalizePath(worktreeGit.CommonDir)
	for _, root := range uniqueStrings(roots, 0) {
		rootGit, rootErr := gitstatus.ResolveWatchPaths(ctx, root)
		if rootErr != nil || strings.TrimSpace(rootGit.CommonDir) == "" {
			continue
		}
		if worktreeCommon == gitstatus.NormalizePath(rootGit.CommonDir) {
			return true
		}
	}
	return false
}

func (r *Runtime) manageSessionsArchive(scope WorkspaceScope, args map[string]any) (string, error) {
	ids := stringSliceValue(args["session_ids"])
	if id := stringValue(args["session_id"]); id != "" {
		ids = append(ids, id)
	}
	all := boolValue(args["all"])
	categoryFilter := strings.ToLower(strings.TrimSpace(stringValue(args["category"])))

	// If no explicit ids provided and all=true or category is specified, discover unarchived sessions to archive
	if len(ids) == 0 && (all || categoryFilter != "") {
		searchOpts := pebblestore.V3SessionSearchOptions{
			AccountScopeID: scope.Principal.AccountScopeID,
			UserID:         scope.Principal.UserID,
			Global:         true,
			ArchivedMode:   "exclude",
			Limit:          manageSessionsMaxStateBulk,
		}
		if p := strings.TrimSpace(stringValue(args["workspace_path"])); p != "" {
			searchOpts.Global = false
			searchOpts.WorkspacePaths = []string{p}
		}
		result, searchErr := r.sessions.SearchSessions(searchOpts)
		if searchErr != nil {
			return "", searchErr
		}
		for _, item := range result.Items {
			if item.ID == scope.SessionID {
				continue
			}
			if categoryFilter != "" && categoryFilter != "all" {
				cat := ManageSessionSidebarCategory(item.Archived, item.Metadata, item.Attention, manageSessionState(item.Lifecycle))
				if cat != categoryFilter {
					continue
				}
			}
			ids = append(ids, item.ID)
		}
		if len(ids) == 0 {
			return marshalManageSessions(map[string]any{
				"action":               "archive",
				"archived_count":       0,
				"archived_session_ids": []string{},
				"message":              "no unarchived sessions found matching criteria",
			})
		}
	}

	if !all && categoryFilter == "" && len(ids) > manageSessionsMaxMutationBatch {
		return "", fmt.Errorf("archive supports at most %d sessions per call", manageSessionsMaxMutationBatch)
	}
	ids = uniqueStrings(ids, manageSessionsMaxStateBulk+1)
	if len(ids) == 0 {
		return "", errors.New("archive requires session_id, session_ids, category, or all=true")
	}

	expected := int64Value(args["expected_updated_at"])
	byID := int64MapValue(args["expected_updated_at_by_id"])
	versions := make(map[string]int64, len(ids))
	archiveIDs := make([]string, 0, len(ids))
	alreadyArchived := make([]string, 0, len(ids))
	skippedCurrent := false

	for _, id := range ids {
		if id == scope.SessionID {
			skippedCurrent = true
			if len(ids) == 1 {
				return "", fmt.Errorf("cannot archive current session %s", id)
			}
			continue
		}
		s, wasArchived, err := r.ownedManageSession(scope, id)
		if err != nil {
			return "", err
		}
		if wasArchived {
			alreadyArchived = append(alreadyArchived, id)
			continue
		}
		want := expected
		if v, ok := byID[id]; ok {
			want = v
		}
		if want != 0 && want != s.UpdatedAt {
			return "", fmt.Errorf("session %s expected_updated_at mismatch: expected %d, current %d", id, want, s.UpdatedAt)
		}
		if want == 0 {
			want = s.UpdatedAt
		}

		hasActiveRun := false
		if runState, ok, _ := r.getSessionRunState(s.ID); ok && runState.Active {
			hasActiveRun = true
		} else if s.Lifecycle != nil && s.Lifecycle.Active {
			hasActiveRun = true
		} else if getter, ok := r.sessions.(interface {
			GetV3SessionActiveRunIntent(string) (pebblestore.V3SessionRunIntent, bool, error)
		}); ok {
			if active, found, _ := getter.GetV3SessionActiveRunIntent(s.ID); found && (active.Status == pebblestore.V3RunIntentRunning || active.Status == pebblestore.V3RunIntentPendingExecutor) {
				hasActiveRun = true
			}
		}

		if hasActiveRun {
			if boolValue(args["stop_active"]) || boolValue(args["force"]) {
				_, _ = r.manageSessionsStop(scope, map[string]any{"session_id": id, "reason": "stopped for archive"})
			} else {
				return "", fmt.Errorf("cannot archive session %s while an execution run is actively in flight; stop it first", id)
			}
		}

		versions[id] = want
		archiveIDs = append(archiveIDs, id)
	}

	for i := 0; i < len(archiveIDs); i += manageSessionsMaxMutationBatch {
		end := i + manageSessionsMaxMutationBatch
		if end > len(archiveIDs) {
			end = len(archiveIDs)
		}
		batch := archiveIDs[i:end]
		batchVersions := make(map[string]int64, len(batch))
		for _, bid := range batch {
			batchVersions[bid] = versions[bid]
		}
		if _, err := r.sessions.ArchiveSessionsWithEventsIfUnchanged(batch, batchVersions); err != nil {
			return "", err
		}
	}

	if r.publishSessionOutbox != nil {
		head, err := r.sessions.CurrentRealtimeOutboxRevision()
		if err == nil {
			for _, id := range archiveIDs {
				record, ok, err := r.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(id, head)
				if err == nil && ok && record.Event.EventType == "session.archived" {
					_ = r.publishSessionOutbox(record)
				}
			}
		}
	}

	res := map[string]any{
		"action":                       "archive",
		"archived_count":               len(archiveIDs),
		"archived_session_ids":         archiveIDs,
		"already_archived_session_ids": alreadyArchived,
		"limit":                        manageSessionsMaxMutationBatch,
		"atomic":                       true,
		"durable":                      true,
	}
	if skippedCurrent {
		res["skipped_current_session"] = scope.SessionID
	}
	return marshalManageSessions(res)
}

func (r *Runtime) manageSessionsUnarchive(scope WorkspaceScope, args map[string]any) (string, error) {
	ids := stringSliceValue(args["session_ids"])
	if id := stringValue(args["session_id"]); id != "" {
		ids = append(ids, id)
	}
	ids = uniqueStrings(ids, manageSessionsMaxMutationBatch+1)
	if len(ids) == 0 {
		return "", errors.New("unarchive requires session_id or session_ids")
	}
	if len(ids) > manageSessionsMaxMutationBatch {
		return "", fmt.Errorf("unarchive supports at most %d sessions per call", manageSessionsMaxMutationBatch)
	}
	expected := int64Value(args["expected_updated_at"])
	byID := int64MapValue(args["expected_updated_at_by_id"])
	versions := make(map[string]int64, len(ids))
	for _, id := range ids {
		if id == scope.SessionID {
			return "", fmt.Errorf("cannot unarchive current session %s", id)
		}
		if active, ok, err := r.sessions.GetSession(id); err != nil {
			return "", err
		} else if ok {
			if active.AccountScopeID != scope.Principal.AccountScopeID || active.UserID != scope.Principal.UserID {
				return "", errors.New("session not found")
			}
			return "", fmt.Errorf("session %s is already active", id)
		}
		tombstone, ok, err := r.sessions.GetSessionTombstone(id)
		if err != nil {
			return "", err
		}
		if !ok || tombstone.AccountScopeID != scope.Principal.AccountScopeID || tombstone.UserID != scope.Principal.UserID {
			return "", errors.New("session not found")
		}
		if tombstone.Deleted || !tombstone.Archived || tombstone.Session.ID == "" {
			return "", fmt.Errorf("session %s is deleted or not restorable", id)
		}
		if lifecycle := tombstone.Session.Lifecycle; lifecycle != nil && lifecycle.Active {
			return "", fmt.Errorf("cannot unarchive session %s with active run state", id)
		}
		want := expected
		if v, found := byID[id]; found {
			want = v
		}
		if want != 0 && want != tombstone.UpdatedAt {
			return "", fmt.Errorf("session %s expected_updated_at mismatch: expected %d, current %d", id, want, tombstone.UpdatedAt)
		}
		if want == 0 {
			want = tombstone.UpdatedAt
		}
		versions[id] = want
	}
	if err := r.sessions.ReactivateArchivedSessionsIfUnchanged(ids, versions); err != nil {
		return "", err
	}
	if r.publishSessionOutbox != nil {
		head, err := r.sessions.CurrentRealtimeOutboxRevision()
		if err != nil {
			return "", fmt.Errorf("load unarchive realtime revision: %w", err)
		}
		for _, id := range ids {
			record, ok, err := r.sessions.LastRealtimeOutboxForSessionAtOrBeforeEndpoint(id, head)
			if err != nil {
				return "", fmt.Errorf("load unarchive realtime event: %w", err)
			}
			if !ok || record.Event.EventType != "session.reactivated" {
				return "", fmt.Errorf("durable unarchive realtime event missing for session %s", id)
			}
			if err := r.publishSessionOutbox(record); err != nil {
				return "", fmt.Errorf("publish unarchive realtime event: %w", err)
			}
		}
	}
	return marshalManageSessions(map[string]any{"action": "unarchive", "unarchived_session_ids": ids, "already_active_session_ids": []string{}, "limit": manageSessionsMaxMutationBatch, "atomic": true, "durable": true})
}

func (r *Runtime) ownedManageSession(scope WorkspaceScope, id string) (pebblestore.SessionSnapshot, bool, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return pebblestore.SessionSnapshot{}, false, errors.New("session_id is required")
	}
	s, ok, err := r.sessions.GetSession(id)
	archived := false
	if err != nil {
		return s, false, err
	}
	if !ok {
		t, found, e := r.sessions.GetSessionTombstone(id)
		if e != nil {
			return s, false, e
		}
		if !found || !t.Archived {
			return s, false, errors.New("session not found")
		}
		s = t.Session
		archived = true
	}
	if s.AccountScopeID != scope.Principal.AccountScopeID || s.UserID != scope.Principal.UserID {
		return pebblestore.SessionSnapshot{}, false, errors.New("session not found")
	}
	return s, archived, nil
}

// manageSessionAuthoritativeState uses the same durable plan attention facts as
// account-wide discovery while preserving lifecycle activity as the safety
// authority for running and queued work.
func (r *Runtime) manageSessionAuthoritativeState(session pebblestore.SessionSnapshot) (string, error) {
	lifecycleState := manageSessionState(session.Lifecycle)
	if lifecycleState == "running" || lifecycleState == "pending" {
		return lifecycleState, nil
	}
	plan, ok, err := r.sessions.GetActivePlan(session.ID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") {
			return lifecycleState, nil
		}
		return "", err
	}
	if !ok || plan.Document == nil {
		return lifecycleState, nil
	}
	checkpointStatus := ""
	for _, checkpoint := range plan.Document.Checkpoints {
		if strings.TrimSpace(checkpoint.ID) == strings.TrimSpace(plan.Document.ActiveCheckpointID) {
			checkpointStatus = strings.ToLower(strings.TrimSpace(checkpoint.Status))
			break
		}
	}
	executionStatus, lastOutcome := "", ""
	if plan.Document.ExecutionState != nil {
		executionStatus = strings.ToLower(strings.TrimSpace(plan.Document.ExecutionState.Status))
		lastOutcome = strings.ToLower(strings.TrimSpace(plan.Document.ExecutionState.LastOutcome))
	}
	switch {
	case checkpointStatus == "needs_review" || executionStatus == "waiting_review" || lastOutcome == "needs_review":
		return "needs_review", nil
	case checkpointStatus == "blocked" || executionStatus == "blocked":
		return "blocked", nil
	case checkpointStatus == "failed" || executionStatus == "failed":
		return "failed", nil
	case checkpointStatus == "in_progress" || executionStatus == "in_progress" || executionStatus == "running":
		return "in_progress", nil
	case checkpointStatus == "pending" || strings.EqualFold(strings.TrimSpace(plan.Status), "pending"):
		return "pending", nil
	default:
		return lifecycleState, nil
	}
}

// IsVideoSessionMetadata returns true if session metadata marks it as a Video Studio session.
func IsVideoSessionMetadata(metadata map[string]any) bool {
	if metadata == nil {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(stringValue(metadata["lineage_kind"])), "video_project") {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(stringValue(metadata["creative_mode"])), "video") {
		return true
	}
	exp := strings.ToLower(strings.TrimSpace(stringValue(metadata["experience"])))
	if exp == "video_studio" {
		return true
	}
	if strings.TrimSpace(stringValue(metadata["video_project_id"])) != "" {
		return true
	}
	return false
}

// ManageSessionSidebarCategory classifies a session into its desktop sidebar category.
func ManageSessionSidebarCategory(archived bool, metadata map[string]any, attention pebblestore.V3SessionAttentionSummary, state string) string {
	if archived {
		return "archived"
	}
	if IsVideoSessionMetadata(metadata) {
		return "video"
	}
	attState := strings.ToLower(strings.TrimSpace(attention.State))
	cpStatus := strings.ToLower(strings.TrimSpace(attention.CheckpointStatus))
	execStatus := strings.ToLower(strings.TrimSpace(attention.ExecutionStatus))
	lastOutcome := strings.ToLower(strings.TrimSpace(attention.LastOutcome))
	normState := strings.ToLower(strings.TrimSpace(state))

	if attState == "blocked" || cpStatus == "blocked" || execStatus == "blocked" || normState == "blocked" {
		return "blocked"
	}
	if attState == "needs_review" || cpStatus == "needs_review" || execStatus == "waiting_review" || lastOutcome == "needs_review" || normState == "needs_review" {
		return "needs_review"
	}
	if attState == "in_progress" || cpStatus == "in_progress" || execStatus == "in_progress" || execStatus == "running" || normState == "in_progress" || normState == "running" {
		return "in_progress"
	}
	if metadata != nil {
		if boolValue(metadata["swarm_v3_sidebar_pinned"]) {
			return "pinned"
		}
		purpose := strings.ToLower(strings.TrimSpace(stringValue(metadata["swarm_v3_session_purpose"])))
		if purpose == "automation_management" || boolValue(metadata["automation_v2"]) {
			return "automation"
		}
	}
	return "active_chats"
}

func manageSessionRecord(i pebblestore.V3SessionSearchItem, state, workspaceSlug string) map[string]any {
	isRunning := false
	if i.Lifecycle != nil && i.Lifecycle.Active {
		isRunning = true
	} else if state == "running" {
		isRunning = true
	}
	category := ManageSessionSidebarCategory(i.Archived, i.Metadata, i.Attention, state)
	record := map[string]any{
		"id":               i.ID,
		"title":            i.Title,
		"created_at":       i.CreatedAt,
		"updated_at":       i.UpdatedAt,
		"message_count":    i.MessageCount,
		"archived":         i.Archived,
		"state":            state,
		"category":         category,
		"sidebar_category": category,
		"sidebar_group":    category,
		"is_running":       isRunning,
		"workspace_path":   i.WorkspacePath,
		"workspace_name":   i.WorkspaceName,
		"worktree_enabled": i.WorktreeEnabled,
		"worktree_branch":  i.WorktreeBranch,
		"snippets":         i.Snippets,
		"navigation":       manageSessionNavigation(i.ID, i.WorkspacePath, i.WorkspaceName, workspaceSlug),
	}
	if i.Mode != "" {
		record["mode"] = i.Mode
	}
	if i.LastMessageAt > 0 {
		record["last_message_at"] = i.LastMessageAt
	}
	if i.Attention.State != "" || i.Attention.PlanID != "" || i.Attention.CheckpointID != "" {
		att := map[string]any{"state": i.Attention.State}
		if i.Attention.PlanID != "" {
			att["plan_id"] = i.Attention.PlanID
			att["plan_status"] = i.Attention.PlanStatus
		}
		if i.Attention.CheckpointID != "" {
			att["checkpoint_id"] = i.Attention.CheckpointID
			att["checkpoint_status"] = i.Attention.CheckpointStatus
		}
		if i.Attention.ExecutionStatus != "" {
			att["execution_status"] = i.Attention.ExecutionStatus
		}
		if i.Attention.LastOutcome != "" {
			att["last_outcome"] = i.Attention.LastOutcome
		}
		record["attention"] = att
	}
	if IsVideoSessionMetadata(i.Metadata) {
		record["is_video"] = true
	}
	if videoContext := manageSessionVideoContext(i.Metadata); videoContext != nil {
		record["video_context"] = videoContext
	}
	return record
}

func manageSessionNavigation(sessionID, workspacePath, workspaceName, workspaceSlug string) map[string]any {
	return map[string]any{"kind": "session", "session_id": sessionID, "workspace_path": workspacePath, "workspace_name": workspaceName, "workspace_slug": workspaceSlug, "href": "/" + workspaceSlug + "/" + sessionID}
}

func manageSessionWorkspaceSlug(workspaceName, workspacePath string, items []pebblestore.V3SessionSearchItem) string {
	base := manageSessionSlugBase(workspaceName, workspacePath)
	collision := false
	for _, item := range items {
		if item.WorkspacePath != workspacePath && manageSessionSlugBase(item.WorkspaceName, item.WorkspacePath) == base {
			collision = true
			break
		}
	}
	if collision {
		return base + "-" + manageSessionPathHash(workspacePath)[:6]
	}
	return base
}

func manageSessionSlugBase(workspaceName, workspacePath string) string {
	value := strings.TrimSpace(workspaceName)
	if value == "" {
		value = filepath.Base(strings.TrimRight(strings.TrimSpace(workspacePath), `/\\`))
	}
	var out strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			out.WriteRune(r)
			lastDash = false
		} else if !lastDash && out.Len() > 0 {
			out.WriteByte('-')
			lastDash = true
		}
	}
	base := strings.Trim(out.String(), "-")
	if base == "" {
		base = "workspace"
	}
	if base == "swarm" {
		base = "swarm-workspace"
	}
	return base
}

func manageSessionPathHash(path string) string {
	const offset uint32 = 2166136261
	const prime uint32 = 16777619
	hash := offset
	// Match Desktop's JavaScript charCodeAt loop, including UTF-16 surrogate pairs.
	for _, codeUnit := range utf16.Encode([]rune(path)) {
		hash ^= uint32(codeUnit)
		hash *= prime
	}
	encoded := strings.ToLower(strconv.FormatUint(uint64(hash), 36))
	return encoded + "000000"
}
func normalizeManageSessionStateFilter(state string) string {
	state = strings.NewReplacer("-", "_", " ", "_").Replace(strings.ToLower(strings.TrimSpace(state)))
	switch state {
	case "running":
		return "in_progress"
	case "needs_approval", "waiting_review", "final_review", "review":
		return "needs_review"
	default:
		return state
	}
}

func manageSessionState(l *pebblestore.SessionLifecycleSnapshot) string {
	if l == nil {
		return "idle"
	}
	s := strings.ToLower(strings.TrimSpace(l.Phase))
	if l.Active && s == "" {
		return "running"
	}
	switch s {
	case "needs_review", "review", "final_review":
		return "needs_review"
	case "running", "in_progress":
		return "running"
	case "pending", "queued":
		return "pending"
	case "failed", "blocked", "completed", "cancelled":
		return s
	}
	return "idle"
}
func marshalManageSessions(v any) (string, error) { b, e := json.Marshal(v); return string(b), e }
func stringValue(v any) string                    { s, _ := v.(string); return strings.TrimSpace(s) }
func stringSliceValue(v any) []string {
	raw, _ := v.([]any)
	if direct, ok := v.([]string); ok {
		return direct
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s := stringValue(item); s != "" {
			out = append(out, s)
		}
	}
	return out
}
func boolValue(v any) bool { b, _ := v.(bool); return b }
func truncateUTF8Bytes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	value = value[:max]
	for len(value) > 0 && !utf8.ValidString(value) {
		value = value[:len(value)-1]
	}
	return value
}
func int64Value(v any) int64 {
	switch n := v.(type) {
	case float64:
		return int64(n)
	case int:
		return int64(n)
	case int64:
		return n
	case json.Number:
		x, _ := n.Int64()
		return x
	}
	return 0
}
func uint64Value(v any) uint64 {
	n := int64Value(v)
	if n < 0 {
		return 0
	}
	return uint64(n)
}
func int64MapValue(v any) map[string]int64 {
	out := map[string]int64{}
	if m, ok := v.(map[string]any); ok {
		for k, n := range m {
			out[k] = int64Value(n)
		}
	}
	return out
}

func boundedInt(v any, def, max int) int {
	n := int(int64Value(v))
	if n <= 0 {
		n = def
	}
	if n > max {
		n = max
	}
	return n
}
func pathWithinScope(path string, roots []string, primary string) bool {
	return canonicalPathWithinScope(path, roots, primary)
}

func canonicalPathWithinScope(path string, roots []string, primary string) bool {
	canonicalPath, err := canonicalExistingPath(path)
	if err != nil {
		return false
	}
	all := append(append([]string(nil), roots...), primary)
	for _, root := range all {
		root = strings.TrimSpace(root)
		if root == "" {
			continue
		}
		canonicalRoot, err := canonicalExistingPath(root)
		if err != nil {
			continue
		}
		rel, err := filepath.Rel(canonicalRoot, canonicalPath)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
func uniqueStrings(in []string, max int) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			continue
		}
		seen[v] = true
		out = append(out, v)
		if len(out) == max {
			break
		}
	}
	sort.Strings(out)
	return out
}
func firstMessageSeq(m []pebblestore.MessageSnapshot) uint64 {
	if len(m) == 0 {
		return 0
	}
	return m[0].GlobalSeq
}
func lastMessageSeq(m []pebblestore.MessageSnapshot) uint64 {
	if len(m) == 0 {
		return 0
	}
	return m[len(m)-1].GlobalSeq
}

func (r *Runtime) manageSessionScopedSearch(scope WorkspaceScope, sessionID string, args map[string]any) (string, error) {
	session, archived, err := r.ownedManageSession(scope, sessionID)
	if err != nil {
		return "", err
	}
	rawQueries := append([]string{stringValue(args["query"])}, stringSliceValue(args["queries"])...)
	needles := make([]string, 0, len(rawQueries))
	for _, q := range rawQueries {
		q = strings.ToLower(strings.TrimSpace(q))
		if q != "" {
			needles = append(needles, q)
		}
	}
	if len(needles) == 0 {
		return "", errors.New("search requires query or queries")
	}
	roleFilter := strings.ToLower(strings.TrimSpace(stringValue(args["role"])))
	limit := boundedInt(args["limit"], 20, manageSessionsMaxLimit)
	budget := boundedInt(args["max_chars"], 12000, manageSessionsMaxChars)
	beforeSeq := uint64Value(args["before_seq"])

	scanLimit := 500
	var msgs []pebblestore.MessageSnapshot
	if beforeSeq > 0 {
		msgs, err = r.listSessionMessagesBefore(session.ID, beforeSeq, scanLimit)
	} else {
		msgs, err = r.listSessionMessageTail(session.ID, scanLimit)
	}
	if err != nil {
		return "", err
	}

	type matchRecord struct {
		ID        string `json:"id"`
		Seq       uint64 `json:"seq"`
		Role      string `json:"role"`
		Snippet   string `json:"snippet"`
		CreatedAt int64  `json:"created_at"`
	}

	matches := make([]matchRecord, 0, limit)
	characters := 0
	characterTruncated := false
	resultTruncated := false
	nextBeforeSeq := uint64(0)
	scanned := 0

	for _, m := range msgs {
		scanned++
		if roleFilter != "" && !strings.EqualFold(m.Role, roleFilter) {
			nextBeforeSeq = m.GlobalSeq
			continue
		}
		contentLower := strings.ToLower(m.Content)
		matched := false
		var matchedNeedle string
		for _, needle := range needles {
			tokens := pebblestore.V3SessionSearchTokens(needle)
			if len(tokens) == 0 {
				if strings.Contains(contentLower, needle) {
					matched = true
					matchedNeedle = needle
					break
				}
				continue
			}
			allMatch := true
			for _, t := range tokens {
				if !strings.Contains(contentLower, t) {
					allMatch = false
					break
				}
			}
			if allMatch {
				matched = true
				matchedNeedle = tokens[0]
				break
			}
		}
		if !matched {
			nextBeforeSeq = m.GlobalSeq
			continue
		}

		if len(matches) >= limit {
			resultTruncated = true
			nextBeforeSeq = m.GlobalSeq + 1
			break
		}

		snippet := pebblestore.MatchCenteredV3SessionSearchSnippet(m.Content, matchedNeedle)
		if characters+len(snippet) > budget {
			characterTruncated = true
			nextBeforeSeq = m.GlobalSeq + 1
			break
		}

		matches = append(matches, matchRecord{
			ID:        m.ID,
			Seq:       m.GlobalSeq,
			Role:      m.Role,
			Snippet:   snippet,
			CreatedAt: m.CreatedAt,
		})
		characters += len(snippet)
		nextBeforeSeq = m.GlobalSeq
	}

	hasMore := characterTruncated || resultTruncated || (scanned >= scanLimit && len(msgs) >= scanLimit)
	if !hasMore {
		nextBeforeSeq = 0
	}

	return marshalManageSessions(map[string]any{
		"action":              "search",
		"search_mode":         "session",
		"session_id":          session.ID,
		"title":               session.Title,
		"archived":            archived,
		"matches":             matches,
		"match_count":         len(matches),
		"scanned_messages":    scanned,
		"characters":          characters,
		"character_limit":     budget,
		"result_truncated":    resultTruncated,
		"character_truncated": characterTruncated,
		"has_more":            hasMore,
		"next_before_seq":     nextBeforeSeq,
		"content_trust":       "untrusted",
		"continuation":        "use read_messages with mode=around and around_seq to inspect full context around any matched seq",
	})
}

func (r *Runtime) listSessionMessageTail(sessionID string, limit int) (res []pebblestore.MessageSnapshot, err error) {
	if r == nil || r.sessions == nil {
		return nil, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			res = nil
		}
	}()
	return r.sessions.ListSessionMessageTail(sessionID, limit)
}

func (r *Runtime) listSessionMessagesBefore(sessionID string, beforeSeq uint64, limit int) (res []pebblestore.MessageSnapshot, err error) {
	if r == nil || r.sessions == nil {
		return nil, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			res = nil
		}
	}()
	return r.sessions.ListSessionMessagesBefore(sessionID, beforeSeq, limit)
}

func (r *Runtime) listSessionMessages(sessionID string, afterSeq uint64, limit int) (res []pebblestore.MessageSnapshot, err error) {
	if r == nil || r.sessions == nil {
		return nil, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			res = nil
		}
	}()
	if lister, ok := r.sessions.(interface {
		ListSessionMessages(string, uint64, int) ([]pebblestore.MessageSnapshot, error)
	}); ok {
		return lister.ListSessionMessages(sessionID, afterSeq, limit)
	}
	return r.sessions.ListMessages(sessionID, afterSeq, limit)
}

func (r *Runtime) getSessionRunState(sessionID string) (res pebblestore.V3SessionRunState, ok bool, err error) {
	if r == nil || r.sessions == nil {
		return pebblestore.V3SessionRunState{}, false, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			res = pebblestore.V3SessionRunState{}
			ok = false
		}
	}()
	if getter, ok := r.sessions.(interface {
		GetSessionRunState(string) (pebblestore.V3SessionRunState, bool, error)
	}); ok {
		return getter.GetSessionRunState(sessionID)
	}
	return pebblestore.V3SessionRunState{}, false, nil
}

func (r *Runtime) getUsageSummary(sessionID string) (res pebblestore.SessionUsageSummary, ok bool, err error) {
	if r == nil || r.sessions == nil {
		return pebblestore.SessionUsageSummary{}, false, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			res = pebblestore.SessionUsageSummary{}
			ok = false
		}
	}()
	if getter, ok := r.sessions.(interface {
		GetUsageSummary(string) (pebblestore.SessionUsageSummary, bool, error)
	}); ok {
		return getter.GetUsageSummary(sessionID)
	}
	return pebblestore.SessionUsageSummary{}, false, nil
}

func (r *Runtime) listSessionPermissions(sessionID string, limit int) (res []pebblestore.PermissionRecord, err error) {
	if r == nil {
		return nil, nil
	}
	defer func() {
		if rec := recover(); rec != nil {
			res = nil
		}
	}()
	if lister, ok := r.orchestration.(interface {
		ListPermissions(string, int) ([]pebblestore.PermissionRecord, error)
	}); ok {
		return lister.ListPermissions(sessionID, limit)
	}
	if lister, ok := r.sessions.(interface {
		ListPermissions(string, int) ([]pebblestore.PermissionRecord, error)
	}); ok {
		return lister.ListPermissions(sessionID, limit)
	}
	return nil, nil
}

func (r *Runtime) manageSessionsCreate(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	title := strings.TrimSpace(stringValue(args["title"]))
	if title == "" {
		title = "New Session"
	}
	workspacePath := strings.TrimSpace(stringValue(args["workspace_path"]))
	if workspacePath == "" {
		if scope.PrimaryPath != "" {
			workspacePath = scope.PrimaryPath
		} else if len(scope.Roots) > 0 {
			workspacePath = scope.Roots[0]
		}
	}
	if workspacePath != "" && !pathWithinScope(workspacePath, scope.Roots, scope.PrimaryPath) {
		return "", fmt.Errorf("workspace path %q is outside authorized scope", workspacePath)
	}
	workspaceName := filepath.Base(workspacePath)
	if workspaceName == "." || workspaceName == "/" || workspaceName == "\\" || workspaceName == "" {
		workspaceName = "workspace"
	}

	mode := strings.TrimSpace(stringValue(args["mode"]))
	if mode == "" {
		mode = "auto"
	} else {
		mode = strings.ToLower(mode)
	}
	agent := strings.TrimSpace(stringValue(args["agent"]))
	prompt := strings.TrimSpace(stringValue(args["prompt"]))
	sessionID := sessionruntime.NewSessionID()
	now := time.Now().UnixMilli()

	var pref pebblestore.ModelPreference
	var modelProfile *pebblestore.SessionModelProfileSnapshot
	var cur pebblestore.SessionSnapshot
	hasCur := false
	if scope.SessionID != "" {
		if s, ok, _ := r.sessions.GetSession(scope.SessionID); ok {
			cur = s
			hasCur = true
			pref = cur.Preference
			if cur.ModelProfile != nil {
				modelProfile = pebblestore.CloneSessionModelProfileSnapshot(cur.ModelProfile)
			}
		}
	}
	reqProvider := strings.TrimSpace(stringValue(args["provider"]))
	reqModel := strings.TrimSpace(stringValue(args["model"]))
	reqThinking := strings.TrimSpace(stringValue(args["thinking"]))
	if reqProvider != "" {
		pref.Provider = reqProvider
	}
	if reqModel != "" {
		pref.Model = reqModel
	}
	if reqThinking != "" {
		pref.Thinking = reqThinking
	}
	if modelProfile != nil && (reqProvider != "" || reqModel != "") {
		if reqProvider != "" {
			modelProfile.Action.Provider = reqProvider
		}
		if reqModel != "" {
			modelProfile.Action.Model = reqModel
		}
		if reqThinking != "" {
			modelProfile.Action.Thinking = reqThinking
		}
	}

	metadata := map[string]any{
		"source": "manage_sessions_create",
	}
	if scope.SessionID != "" {
		metadata["creator_session_id"] = scope.SessionID
	}
	if hasCur && cur.Metadata != nil {
		for _, key := range []string{
			"agent_name", "resolved_agent_name", "agent_mode", "runtime_mode",
			"default_session_mode", "exit_plan_mode_enabled", "agent_profile",
			"tool_contract_preset",
		} {
			if val, exists := cur.Metadata[key]; exists && val != nil {
				metadata[key] = val
			}
		}
	}
	if agent != "" {
		metadata["agent_name"] = agent
	}
	if (metadata["agent_profile"] == nil || agent != "") && r.agents != nil {
		targetAgent := agent
		if targetAgent == "" {
			targetAgent = "swarm"
		}
		var profile pebblestore.AgentProfile
		var found bool
		if scope.Principal.AccountScopeID != "" {
			profile, found, _ = r.agents.GetProfileForAccount(scope.Principal.AccountScopeID, targetAgent)
		}
		if !found {
			profile, found, _ = r.agents.GetProfile(targetAgent)
		}
		if !found && targetAgent != "swarm" {
			if scope.Principal.AccountScopeID != "" {
				profile, found, _ = r.agents.GetProfileForAccount(scope.Principal.AccountScopeID, "swarm")
			}
			if !found {
				profile, found, _ = r.agents.GetProfile("swarm")
			}
		}
		if found {
			metadata["agent_name"] = profile.Name
			metadata["resolved_agent_name"] = profile.Name
			metadata["agent_mode"] = profile.Mode
			metadata["runtime_mode"] = profile.RuntimeMode
			metadata["default_session_mode"] = pebblestore.AgentProfileDefaultSessionMode(profile)
			if profile.ExitPlanModeEnabled != nil {
				metadata["exit_plan_mode_enabled"] = *profile.ExitPlanModeEnabled
			}
			metadata["agent_profile"] = profile
			if profile.ToolContract != nil && profile.ToolContract.Preset != "" {
				metadata["tool_contract_preset"] = profile.ToolContract.Preset
			}
		}
	}

	avail := true
	grants := []pebblestore.WorkspaceGrant{
		{Kind: pebblestore.WorkspaceGrantPrimary, Path: workspacePath, Name: workspaceName, Available: &avail},
	}
	snapshot := pebblestore.SessionSnapshot{
		ID:              sessionID,
		UserID:          scope.Principal.UserID,
		AccountScopeID:  scope.Principal.AccountScopeID,
		WorkspacePath:   workspacePath,
		WorkspaceName:   workspaceName,
		Title:           title,
		Mode:            mode,
		Preference:      pref,
		ModelProfile:    modelProfile,
		Metadata:        metadata,
		WorkspaceGrants: grants,
		WorkspaceUsage:  pebblestore.WorkspaceUsageFromGrants(grants),
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	createKey := fmt.Sprintf("manage-sessions:create:%s:%d", sessionID, now)
	res, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          scope.Principal.UserID,
		AccountScopeID:  scope.Principal.AccountScopeID,
		ClientRequestID: createKey,
		IdempotencyKey:  createKey,
		PayloadHash:     createKey,
		RequestHash:     createKey,
		Kind:            pebblestore.V3SessionMutationCreateSession,
		Session:         &snapshot,
		NowUnixMs:       now,
	})
	if err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	if r.publishSessionOutbox != nil && res.RealtimeOutbox != nil {
		_ = r.publishSessionOutbox(*res.RealtimeOutbox)
	}

	slug := manageSessionWorkspaceSlug(workspaceName, workspacePath, nil)
	out := map[string]any{
		"action":         "create",
		"session_id":     sessionID,
		"title":          title,
		"workspace_path": workspacePath,
		"workspace_name": workspaceName,
		"mode":           mode,
		"status":         "created",
		"navigation":     manageSessionNavigation(sessionID, workspacePath, workspaceName, slug),
	}

	if prompt != "" {
		waitSeconds := boundedInt(args["wait_seconds"], 0, 120)
		msgRes, msgErr := r.sendSessionMessageInternal(ctx, scope, sessionID, prompt, "user", true, waitSeconds)
		if msgErr != nil {
			return "", fmt.Errorf("session created (%s) but failed to start initial run: %w", sessionID, msgErr)
		}
		for k, v := range msgRes {
			if k != "action" && k != "session_id" {
				out[k] = v
			}
		}
	}

	return marshalManageSessions(out)
}

func (r *Runtime) manageSessionsStop(scope WorkspaceScope, args map[string]any) (string, error) {
	sessionID := strings.TrimSpace(stringValue(args["session_id"]))
	if sessionID == "" {
		return "", errors.New("stop requires session_id")
	}
	_, wasArchived, err := r.ownedManageSession(scope, sessionID)
	if err != nil {
		return "", err
	}
	if wasArchived {
		return "", fmt.Errorf("session %s is archived; cannot stop", sessionID)
	}
	reason := strings.TrimSpace(stringValue(args["reason"]))
	if reason == "" {
		reason = "stopped by manage-sessions"
	}
	runID := strings.TrimSpace(stringValue(args["run_id"]))
	if runID == "" {
		if runState, ok, _ := r.getSessionRunState(sessionID); ok && runState.Active && runState.RunID != "" {
			runID = runState.RunID
		}
	}
	if runID == "" {
		if getter, ok := r.sessions.(interface {
			GetV3SessionActiveRunIntent(string) (pebblestore.V3SessionRunIntent, bool, error)
		}); ok {
			if active, found, _ := getter.GetV3SessionActiveRunIntent(sessionID); found && (active.Status == pebblestore.V3RunIntentRunning || active.Status == pebblestore.V3RunIntentPendingExecutor) {
				runID = active.RunID
			}
		}
	}
	if runID == "" {
		return marshalManageSessions(map[string]any{
			"action":     "stop",
			"session_id": sessionID,
			"status":     "not_running",
			"message":    "session has no active run",
		})
	}

	cancelled := false
	if r.sessionController != nil {
		cancelled, err = r.sessionController.CancelSessionRun(scope.Principal, sessionID, runID, reason)
		if err != nil {
			return "", err
		}
	} else {
		now := time.Now().UnixMilli()
		mutationKey := fmt.Sprintf("manage-sessions:stop:%s:%d", runID, now)
		res, mutErr := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
			SessionID:       sessionID,
			UserID:          scope.Principal.UserID,
			AccountScopeID:  scope.Principal.AccountScopeID,
			ClientRequestID: mutationKey,
			IdempotencyKey:  mutationKey,
			PayloadHash:     mutationKey,
			RequestHash:     mutationKey,
			Kind:            pebblestore.V3SessionMutationRecordRunIntent,
			RunIntent: &pebblestore.V3SessionRunIntent{
				SessionID:      sessionID,
				RunID:          runID,
				UserID:         scope.Principal.UserID,
				AccountScopeID: scope.Principal.AccountScopeID,
				Status:         pebblestore.V3RunIntentCancelled,
				BlockedReason:  reason,
				UpdatedAt:      now,
			},
			NowUnixMs: now,
		})
		if mutErr != nil {
			return "", mutErr
		}
		if r.publishSessionOutbox != nil && res.RealtimeOutbox != nil {
			_ = r.publishSessionOutbox(*res.RealtimeOutbox)
		}
		cancelled = true
	}

	return marshalManageSessions(map[string]any{
		"action":     "stop",
		"session_id": sessionID,
		"run_id":     runID,
		"status":     "cancelled",
		"cancelled":  cancelled,
		"reason":     reason,
	})
}

func (r *Runtime) manageSessionsSendMessage(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	sessionID := strings.TrimSpace(stringValue(args["session_id"]))
	if sessionID == "" {
		return "", errors.New("send_message requires session_id")
	}
	prompt := strings.TrimSpace(stringValue(args["prompt"]))
	if prompt == "" {
		return "", errors.New("send_message requires prompt")
	}
	_, wasArchived, err := r.ownedManageSession(scope, sessionID)
	if err != nil {
		return "", err
	}
	if wasArchived {
		return "", fmt.Errorf("cannot send message to archived session %s; unarchive first", sessionID)
	}
	role := strings.TrimSpace(stringValue(args["role"]))
	if role == "" {
		role = "user"
	}
	triggerRun := true
	if v, ok := args["trigger_run"]; ok {
		triggerRun = boolValue(v)
	}
	waitSeconds := boundedInt(args["wait_seconds"], 0, 120)
	res, err := r.sendSessionMessageInternal(ctx, scope, sessionID, prompt, role, triggerRun, waitSeconds)
	if err != nil {
		return "", err
	}
	return marshalManageSessions(res)
}

func (r *Runtime) sendSessionMessageInternal(ctx context.Context, scope WorkspaceScope, sessionID, prompt, role string, triggerRun bool, waitSeconds int) (map[string]any, error) {
	if triggerRun {
		if runState, ok, _ := r.getSessionRunState(sessionID); ok && runState.Active {
			return nil, fmt.Errorf("session %s is currently running (run_id: %s); wait for completion or stop it first", sessionID, runState.RunID)
		}
	}
	now := time.Now().UnixMilli()
	msgID := fmt.Sprintf("msg_%s_%d", sessionID, now)
	msg := pebblestore.MessageSnapshot{
		ID:             msgID,
		SessionID:      sessionID,
		UserID:         scope.Principal.UserID,
		AccountScopeID: scope.Principal.AccountScopeID,
		Role:           role,
		Content:        prompt,
		Metadata: map[string]any{
			"source":            "manage_sessions",
			"sender_session_id": scope.SessionID,
		},
		CreatedAt: now,
	}
	runID := ""
	var runIntent *pebblestore.V3SessionRunIntent
	if triggerRun {
		runID = "desktop-v3-run:" + sessionruntime.NewSessionID()
		runIntent = &pebblestore.V3SessionRunIntent{
			SessionID:       sessionID,
			RunID:           runID,
			EpochID:         "epoch-00000000000000000001",
			UserID:          scope.Principal.UserID,
			AccountScopeID:  scope.Principal.AccountScopeID,
			ParentSessionID: scope.SessionID,
			SourceMessageID: msgID,
			Status:          pebblestore.V3RunIntentPendingExecutor,
			CreatedAt:       now,
			UpdatedAt:       now,
		}
	}
	mutationKey := fmt.Sprintf("manage-sessions:msg:%s:%d", msgID, now)
	res, err := r.sessions.ApplySessionMutation(pebblestore.V3SessionMutationInput{
		SessionID:       sessionID,
		UserID:          scope.Principal.UserID,
		AccountScopeID:  scope.Principal.AccountScopeID,
		ClientRequestID: mutationKey,
		IdempotencyKey:  mutationKey,
		PayloadHash:     mutationKey,
		RequestHash:     mutationKey,
		Kind:            pebblestore.V3SessionMutationAppendMessage,
		Message:         &msg,
		RunIntent:       runIntent,
		NowUnixMs:       now,
	})
	if err != nil {
		return nil, fmt.Errorf("append message: %w", err)
	}
	if r.publishSessionOutbox != nil && res.RealtimeOutbox != nil {
		_ = r.publishSessionOutbox(*res.RealtimeOutbox)
	}
	if triggerRun {
		if r.sessionController == nil {
			return nil, errors.New("session execution controller is not configured")
		}
		if !r.sessionController.EnqueueSessionRun(scope.Principal, sessionID, runID, scope.SessionID) {
			return nil, fmt.Errorf("failed to enqueue run %s for session %s", runID, sessionID)
		}
	}

	out := map[string]any{
		"action":      "send_message",
		"session_id":  sessionID,
		"message_id":  msgID,
		"trigger_run": triggerRun,
	}
	if !triggerRun {
		out["status"] = "appended"
		return out, nil
	}
	out["run_id"] = runID
	out["status"] = "queued"

	if triggerRun {
		// Preflight check: poll briefly to detect immediate startup/dispatch failures (e.g. invalid profile, quota, executor rejection)
		preflightDeadline := time.Now().Add(350 * time.Millisecond)
		for time.Now().Before(preflightDeadline) {
			time.Sleep(50 * time.Millisecond)
			if runState, ok, _ := r.getSessionRunState(sessionID); ok && !runState.Active && (runState.Status == "failed" || runState.Status == "cancelled") {
				reason := runState.BlockedReason
				if reason == "" {
					reason = runState.Status
				}
				return nil, fmt.Errorf("session run failed to deploy: %s (status: %s)", reason, runState.Status)
			}
		}
	}

	if waitSeconds > 0 {
		deadline := time.Now().Add(time.Duration(waitSeconds) * time.Second)
		pollInterval := 250 * time.Millisecond
		for {
			select {
			case <-ctx.Done():
				out["status"] = "cancelled"
				out["note"] = "context cancelled while waiting"
				return out, nil
			default:
			}
			if time.Now().After(deadline) {
				break
			}
			time.Sleep(pollInterval)

			runState, ok, _ := r.getSessionRunState(sessionID)
			tail, _ := r.listSessionMessageTail(sessionID, 5)
			var lastAssistant *pebblestore.MessageSnapshot
			for i := len(tail) - 1; i >= 0; i-- {
				if tail[i].Role == "assistant" && tail[i].CreatedAt >= now {
					lastAssistant = &tail[i]
					break
				}
			}
			if (ok && !runState.Active && lastAssistant != nil) || (lastAssistant != nil && (!ok || runState.Status == "completed" || runState.Status == "waiting_review")) {
				out["status"] = "completed"
				out["response"] = lastAssistant.Content
				out["assistant_message_id"] = lastAssistant.ID
				out["response_seq"] = lastAssistant.GlobalSeq
				return out, nil
			}
			if ok && !runState.Active && (runState.Status == "failed" || runState.Status == "cancelled") {
				reason := runState.BlockedReason
				if reason == "" {
					reason = runState.Status
				}
				return nil, fmt.Errorf("session run failed to deploy: %s (status: %s)", reason, runState.Status)
			}
		}
		out["status"] = "running"
		out["note"] = fmt.Sprintf("Run is still executing after %d seconds (provider or tools in flight). Inspect progress via action: get or read_messages.", waitSeconds)
	}
	return out, nil
}

func (r *Runtime) manageSessionsCompact(ctx context.Context, scope WorkspaceScope, args map[string]any) (string, error) {
	sessionID := strings.TrimSpace(stringValue(args["session_id"]))
	if sessionID == "" {
		return "", errors.New("compact requires session_id")
	}
	_, wasArchived, err := r.ownedManageSession(scope, sessionID)
	if err != nil {
		return "", err
	}
	if wasArchived {
		return "", fmt.Errorf("session %s is archived; cannot compact", sessionID)
	}
	if runState, ok, _ := r.getSessionRunState(sessionID); ok && runState.Active {
		return "", fmt.Errorf("session %s is currently running (run_id: %s); stop it before compacting", sessionID, runState.RunID)
	}
	note := strings.TrimSpace(stringValue(args["compact_handoff"]))
	if note == "" {
		note = strings.TrimSpace(stringValue(args["note"]))
	}
	if r.sessionController != nil {
		res, err := r.sessionController.CompactSession(ctx, scope.Principal, sessionID, note)
		if err != nil {
			return "", err
		}
		return marshalManageSessions(map[string]any{
			"action":     "compact",
			"session_id": sessionID,
			"status":     "completed",
			"compaction": res,
		})
	}
	return marshalManageSessions(map[string]any{
		"action":     "compact",
		"session_id": sessionID,
		"status":     "completed",
		"summary":    "compaction accepted",
	})
}

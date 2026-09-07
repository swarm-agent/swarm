package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"swarm/packages/swarmd/internal/gitstatus"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
)

const sessionRepositoryPageLimit = 20

// Repository rows preserve context and attachment identity, not just physical path.
type sessionRepositoryItem struct {
	ID                  string              `json:"id"`
	SessionID           string              `json:"session_id"`
	WorkspaceID         string              `json:"workspace_id"`
	WorkspaceName       string              `json:"workspace_name"`
	WorkspaceGeneration int64               `json:"workspace_generation"`
	SourcePath          string              `json:"source_path"`
	WorkspacePath       string              `json:"workspace_path"`
	Kind                string              `json:"kind"`
	Attached            bool                `json:"attached"`
	Default             bool                `json:"default"`
	Branch              string              `json:"branch"`
	BaseCommit          string              `json:"base_commit"`
	Lifecycle           string              `json:"lifecycle"`
	Retained            bool                `json:"retained"`
	Availability        string              `json:"availability"`
	Error               string              `json:"error,omitempty"`
	Status              *gitstatus.Snapshot `json:"status,omitempty"`
	FilesTruncated      bool                `json:"files_truncated"`
	grant               pebblestore.WorkspaceGrant
	currentAuthority    bool
}

type sessionRepositoriesResponse struct {
	OK              bool                    `json:"ok"`
	Items           []sessionRepositoryItem `json:"items"`
	NextCursor      string                  `json:"next_cursor,omitempty"`
	HistoryCoverage string                  `json:"history_coverage"`
}

// The entire continuation is sealed by the store against principal, parent,
// inventory revision and HTTP page limit. It never grants filesystem access.
type sessionRepositoryCursor struct {
	Phase   string `json:"phase"`
	Cursor  string `json:"cursor"`
	Offset  int    `json:"offset"`
	Context string `json:"context"`
}

func repositoryItemID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func repositorySessionItems(row pebblestore.SessionRepositoryHistory, parent string) []sessionRepositoryItem {
	owner := row.Session
	items := []sessionRepositoryItem{}
	source := strings.TrimSpace(sessionsV3MetadataString(owner.Metadata, "swarm_v3_source_workspace_path"))
	if source == "" {
		source = owner.WorkspacePath
	}
	lifecycle := sessionsV3MetadataString(owner.Metadata, "integration_status")
	if lifecycle == "" {
		lifecycle = sessionsV3MetadataString(owner.Metadata, "task_status")
	}
	if lifecycle == "" {
		lifecycle = "retained"
	}
	if row.Archived {
		lifecycle = "archived"
	}
	if row.Deleted {
		lifecycle = "deleted"
	}
	grants := pebblestore.NormalizeSessionWorkspaceGrants(owner)
	if row.Projected {
		grants = row.Grants
	}
	if !row.Projected && owner.WorktreeEnabled && owner.WorktreeRootPath != "" {
		found := false
		for _, grant := range grants {
			if grant.Path == owner.WorktreeRootPath {
				found = true
			}
		}
		if !found {
			grants = append(grants, pebblestore.WorkspaceGrant{Kind: pebblestore.WorkspaceGrantWorktree, Path: owner.WorktreeRootPath})
		}
	}
	for _, grant := range grants {
		if grant.Kind == pebblestore.WorkspaceGrantTemporary {
			continue
		}
		kind := "source"
		if grant.Path == owner.WorktreeRootPath && owner.WorktreeEnabled {
			kind = "parent"
			if owner.ID != parent {
				kind = "worker"
			}
		}
		item := sessionRepositoryItem{SessionID: owner.ID, WorkspaceID: grant.WorkspaceID, WorkspaceName: grant.Name, WorkspaceGeneration: grant.WorkspaceGeneration, SourcePath: grant.Path, WorkspacePath: grant.Path, Kind: kind, Lifecycle: lifecycle, Retained: true, grant: grant}
		if kind != "source" {
			item.SourcePath = source
			item.Branch = owner.WorktreeBranch
			item.BaseCommit = sessionsV3MetadataString(owner.Metadata, "base_commit")
			item.WorkspaceID, item.WorkspaceGeneration = "", 0
			// Worktree grants do not themselves carry source catalog identity.
			for _, sourceGrant := range owner.WorkspaceGrants {
				if sourceGrant.Path == source && sourceGrant.WorkspaceID != "" {
					item.grant = sourceGrant
					item.WorkspaceID = sourceGrant.WorkspaceID
					item.WorkspaceGeneration = sourceGrant.WorkspaceGeneration
					item.WorkspaceName = sourceGrant.Name
					break
				}
			}
		}
		item.ID = repositoryItemID(owner.ID, grant.WorkspaceID, grant.Path)
		items = append(items, item)
	}
	// Captured source is often metadata-only for managed child sessions.
	found := false
	for _, item := range items {
		if item.WorkspacePath == source {
			found = true
		}
	}
	if !row.Projected && source != "" && !found {
		items = append(items, sessionRepositoryItem{ID: repositoryItemID(owner.ID, "", source), SessionID: owner.ID, SourcePath: source, WorkspacePath: source, Kind: "source", Lifecycle: lifecycle, Retained: true})
	}
	return items
}

func (s *Server) authorizeRepositoryItem(principal identity.Principal, item sessionRepositoryItem) error {
	if !principal.Valid() || s.workspace == nil {
		return errors.New("repository authorization unavailable")
	}
	if !filepath.IsAbs(item.SourcePath) || !filepath.IsAbs(item.WorkspacePath) {
		return errors.New("repository identity is incomplete")
	}
	if item.WorkspaceID != "" {
		entry, found, err := s.workspace.GetByWorkspaceIDForPrincipal(principal, item.WorkspaceID)
		if err != nil {
			return err
		}
		if !found || entry.Path != item.SourcePath || entry.WorkspaceGeneration != item.WorkspaceGeneration {
			return errors.New("repository saved workspace identity is stale")
		}
	}
	owned, err := s.resolveAccountOwnedPath(principal, item.SourcePath)
	if err != nil {
		return err
	}
	if owned.WorkspacePath != item.SourcePath || owned.ResolvedPath != item.SourcePath {
		return errors.New("repository source must be an exact saved workspace root")
	}
	if item.WorkspaceID != "" && owned.Scope.WorkspaceID != item.WorkspaceID {
		return errors.New("repository saved identity mismatch")
	}
	if item.WorkspacePath == item.SourcePath {
		resolved, err := filepath.EvalSymlinks(item.WorkspacePath)
		if err != nil {
			return err
		}
		if resolved != item.WorkspacePath {
			return errors.New("repository path is a symlink alias")
		}
		return nil
	}
	validator, ok := s.worktrees.(interface {
		ValidateSessionRepositoryLane(string, string, string, string) error
		ValidateTaskRepositoryLane(string, string, string, string) error
	})
	if !ok {
		return errors.New("repository lane validator unavailable")
	}
	if item.Kind == "lane" {
		digest := sha256.Sum256([]byte(item.SessionID + "\x00" + item.SourcePath))
		if err := validator.ValidateTaskRepositoryLane(item.SourcePath, item.WorkspacePath, "program-lane-"+hex.EncodeToString(digest[:12]), item.Branch); err != nil {
			return err
		}
	} else if err := validator.ValidateSessionRepositoryLane(item.SourcePath, item.WorkspacePath, item.SessionID, item.Branch); err != nil {
		return err
	}
	return worktree.ValidateOwnedIdentity(item.SourcePath, item.WorkspacePath, item.Branch, item.BaseCommit)
}

func (s *Server) handleSessionV3Repositories(w http.ResponseWriter, r *http.Request, principal identity.Principal, sessionID string) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	limit := sessionRepositoryPageLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > sessionRepositoryPageLimit {
			writeError(w, http.StatusBadRequest, errors.New("limit must be 1..20"))
			return
		}
	}
	cursor := sessionRepositoryCursor{Phase: "sessions"}
	continuationQuery := pebblestore.RepositoryHistoryQuery{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ParentSessionID: sessionID, Limit: limit}
	raw := r.URL.Query().Get("cursor")
	data, anchor, err := s.sessions.RepositoryContinuation(continuationQuery, raw, nil)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, pebblestore.ErrRepositoryHistoryNotReady) {
			status = http.StatusServiceUnavailable
		}
		writeError(w, status, err)
		return
	}
	if raw != "" {
		if json.Unmarshal(data, &cursor) != nil || cursor.Offset < 0 || cursor.Offset > 1024 || (cursor.Phase != "sessions" && cursor.Phase != "programs") {
			writeError(w, http.StatusBadRequest, pebblestore.ErrRepositoryHistoryCursor)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	response := sessionRepositoriesResponse{OK: true, Items: []sessionRepositoryItem{}}
	// Sequential inspection bounds Git fan-out to one repository. Empty contexts
	// also consume the read budget, preventing unbounded scans on one request.
	for reads := 0; reads < limit && len(response.Items) < limit; reads++ {
		if err := ctx.Err(); err != nil {
			writeError(w, http.StatusRequestTimeout, err)
			return
		}
		query := pebblestore.RepositoryHistoryQuery{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ParentSessionID: sessionID, Limit: 1, Cursor: cursor.Cursor}
		var page pebblestore.RepositoryHistoryPage
		var err error
		if cursor.Phase == "programs" {
			page, err = s.sessions.TaskProgramRepositoryHistory(query)
		} else {
			page, err = s.sessions.RepositoryHistory(query)
		}
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, pebblestore.ErrRepositoryHistoryNotReady) {
				status = http.StatusServiceUnavailable
			}
			writeError(w, status, err)
			return
		}
		response.HistoryCoverage = page.HistoryCoverage
		items := []sessionRepositoryItem{}
		contextID := ""
		if len(page.Sessions) > 0 {
			row := page.Sessions[0]
			contextID = row.Session.ID + ":" + row.ContextID
			items = repositorySessionItems(row, sessionID)
			for i := range items {
				if items[i].WorkspaceID == "" && items[i].SourcePath == sessionsV3MetadataString(row.Session.Metadata, "swarm_v3_source_workspace_path") {
					items[i].WorkspaceID = sessionsV3MetadataString(row.Session.Metadata, "swarm_v3_source_workspace_id")
					items[i].WorkspaceGeneration, _ = strconv.ParseInt(sessionsV3MetadataString(row.Session.Metadata, "swarm_v3_source_workspace_generation"), 10, 64)
				}
			}
			current, found, getErr := s.sessions.GetSession(row.Session.ID)
			if getErr != nil {
				writeError(w, http.StatusInternalServerError, getErr)
				return
			}
			for i := range items {
				if found {
					for _, grant := range current.WorkspaceGrants {
						if grant.Path == items[i].WorkspacePath && grant.WorkspaceID == items[i].WorkspaceID {
							items[i].Attached = row.Session.ID == sessionID
							items[i].Default = items[i].Attached && grant.Kind == pebblestore.WorkspaceGrantPrimary
							if primary := sessionsV3MetadataString(current.Metadata, "swarm_v3_source_workspace_id"); primary != "" {
								items[i].Default = items[i].Attached && items[i].Kind == "source" && items[i].WorkspaceID == primary
							}
						}
					}
				}
			}
		}
		if len(page.Programs) > 0 {
			program := page.Programs[0]
			contextID = program.ProgramID
			if lane := program.RepositoryLane; lane != nil {
				items = append(items, sessionRepositoryItem{ID: repositoryItemID(sessionID, "lane", lane.WorkspacePath), SessionID: sessionID, SourcePath: lane.SourcePath, WorkspacePath: lane.WorkspacePath, Kind: "lane", Branch: lane.Branch, BaseCommit: lane.BaseCommit, Lifecycle: program.State, Retained: true})
			}
		}
		if cursor.Context != "" && cursor.Context != contextID {
			writeError(w, http.StatusConflict, pebblestore.ErrRepositoryHistoryCursor)
			return
		}
		if cursor.Offset > len(items) {
			writeError(w, http.StatusBadRequest, pebblestore.ErrRepositoryHistoryCursor)
			return
		}
		for cursor.Offset < len(items) && len(response.Items) < limit {
			item := items[cursor.Offset]
			item.Availability = "unavailable"
			if err := ctx.Err(); err != nil {
				writeError(w, http.StatusRequestTimeout, err)
				return
			}
			if err := s.authorizeRepositoryItem(principal, item); err != nil {
				item.Error = err.Error()
			} else {
				paths, err := gitstatus.ResolveWatchPaths(ctx, item.WorkspacePath)
				if err == nil && filepath.Clean(paths.RepoRoot) != filepath.Clean(item.WorkspacePath) {
					err = errors.New("repository is not an exact Git root")
				}
				if err != nil {
					item.Error = err.Error()
				} else {
					snapshot, err := boundedRepositorySnapshot(ctx, item.WorkspacePath, paths)
					if err != nil {
						item.Error = err.Error()
					} else {
						item.Availability = "available"
						if len(snapshot.Files) > 500 {
							snapshot.Files = snapshot.Files[:500]
							item.FilesTruncated = true
						}
						item.Status = &snapshot
						if item.Branch == "" {
							item.Branch = snapshot.Branch
						}
					}
				}
			}
			response.Items = append(response.Items, item)
			cursor.Offset++
		}
		if cursor.Offset < len(items) {
			cursor.Context = contextID
			break
		}
		cursor.Offset, cursor.Context = 0, ""
		cursor.Cursor = page.NextCursor
		if page.NextCursor == "" {
			if cursor.Phase == "sessions" {
				cursor.Phase = "programs"
			} else {
				cursor.Phase = "done"
				break
			}
		}
	}
	data, err = json.Marshal(cursor)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	_, next, err := s.sessions.RepositoryContinuation(continuationQuery, anchor, data)
	if err != nil {
		writeError(w, http.StatusConflict, err)
		return
	}
	if cursor.Phase != "done" {
		response.NextCursor = next
	}
	if err := ctx.Err(); err != nil {
		writeError(w, http.StatusRequestTimeout, err)
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// Explicit selectors are exact owned rows; they never fall through to the
// account's current workspace. Historical inventory is not mutation authority.
func (s *Server) selectedSessionRepository(principal identity.Principal, sessionID, path string) (sessionRepositoryItem, error) {
	if s == nil || s.sessions == nil {
		return sessionRepositoryItem{}, errors.New("session service unavailable")
	}
	owner, found, err := s.sessions.GetSession(sessionID)
	if err != nil {
		return sessionRepositoryItem{}, err
	}
	if !found || owner.AccountScopeID != principal.AccountScopeID || owner.UserID != principal.UserID {
		return sessionRepositoryItem{}, errors.New("session not found")
	}
	if path == "" {
		path = owner.WorkspacePath
		if owner.WorktreeEnabled {
			path = owner.WorktreeRootPath
		}
	}
	if !filepath.IsAbs(path) {
		return sessionRepositoryItem{}, errors.New("exact absolute repository selector required")
	}
	items := repositorySessionItems(pebblestore.SessionRepositoryHistory{Session: owner}, sessionID)
	for _, item := range items {
		if item.WorkspacePath != path {
			continue
		}
		if item.WorkspaceID == "" && item.SourcePath == sessionsV3MetadataString(owner.Metadata, "swarm_v3_source_workspace_path") {
			item.WorkspaceID = sessionsV3MetadataString(owner.Metadata, "swarm_v3_source_workspace_id")
			item.WorkspaceGeneration, _ = strconv.ParseInt(sessionsV3MetadataString(owner.Metadata, "swarm_v3_source_workspace_generation"), 10, 64)
		}
		if err := s.authorizeRepositoryItem(principal, item); err != nil {
			return sessionRepositoryItem{}, err
		}
		item.currentAuthority = owner.WorktreeEnabled && item.WorkspacePath == owner.WorktreeRootPath
		for _, grant := range pebblestore.NormalizeSessionWorkspaceGrants(owner) {
			if grant.Path == item.WorkspacePath && grant.Kind != pebblestore.WorkspaceGrantTemporary {
				item.currentAuthority = true
			}
		}
		return item, nil
	}
	query := pebblestore.RepositoryHistoryQuery{AccountScopeID: principal.AccountScopeID, UserID: principal.UserID, ParentSessionID: sessionID, Limit: 20}
	page, err := s.sessions.ExactRepositoryHistory(query, path)
	if err != nil {
		return sessionRepositoryItem{}, err
	}
	for _, row := range page.Sessions {
		for _, item := range repositorySessionItems(row, sessionID) {
			if item.WorkspacePath != path {
				continue
			}
			if item.WorkspaceID == "" && item.SourcePath == sessionsV3MetadataString(row.Session.Metadata, "swarm_v3_source_workspace_path") {
				item.WorkspaceID = sessionsV3MetadataString(row.Session.Metadata, "swarm_v3_source_workspace_id")
				item.WorkspaceGeneration, _ = strconv.ParseInt(sessionsV3MetadataString(row.Session.Metadata, "swarm_v3_source_workspace_generation"), 10, 64)
			}
			if err := s.authorizeRepositoryItem(principal, item); err != nil {
				return sessionRepositoryItem{}, err
			}
			return item, nil
		}
	}
	for _, program := range page.Programs {
		lane := program.RepositoryLane
		if lane == nil || lane.WorkspacePath != path {
			continue
		}
		item := sessionRepositoryItem{SessionID: sessionID, SourcePath: lane.SourcePath, WorkspacePath: lane.WorkspacePath, Kind: "lane", Branch: lane.Branch, BaseCommit: lane.BaseCommit}
		if err := s.authorizeRepositoryItem(principal, item); err != nil {
			return sessionRepositoryItem{}, err
		}
		return item, nil
	}
	return sessionRepositoryItem{}, errors.New("unknown session repository selector")
}

// Status reads suppress optional Git index writes and bound captured output.
func boundedRepositorySnapshot(parent context.Context, path string, paths gitstatus.WatchPaths) (gitstatus.Snapshot, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	raw, err := runBoundedGitCommand(ctx, path, 2<<20, "status", "--porcelain=v2", "--branch", "--show-stash", "-z")
	if err != nil {
		return gitstatus.Snapshot{}, err
	}
	snapshot := gitstatus.ParsePorcelainV2Snapshot(raw)
	snapshot.WorkspacePath, snapshot.RepoRoot, snapshot.GitDir = path, paths.RepoRoot, paths.GitDir
	snapshot.RefreshedAt = time.Now()
	return snapshot, nil
}

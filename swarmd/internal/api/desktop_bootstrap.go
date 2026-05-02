package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const workspaceOverviewDefaultSessionLimit = 25
const workspaceOverviewDefaultPermissionLimit = 200
const workspaceOverviewPermissionParallelism = 8

type workspaceOverviewSession struct {
	pebblestore.SessionSnapshot
	PendingPermissions     []pebblestore.PermissionRecord `json:"pending_permissions"`
	PendingPermissionCount int                            `json:"pending_permission_count"`
	ActiveRun              *runStreamActiveRun            `json:"active_run,omitempty"`
	SessionStatus          string                         `json:"session_status,omitempty"`
	gitStatusResponseFields
	GitCommitDetected bool `json:"git_commit_detected,omitempty"`
	GitCommitCount    int  `json:"git_commit_count,omitempty"`
}

type workspaceOverviewWorkspace struct {
	workspace.Entry
	Sessions    []workspaceOverviewSession       `json:"sessions"`
	TodoSummary pebblestore.WorkspaceTodoSummary `json:"todo_summary"`
	gitStatusResponseFields
}

func (s *Server) applyWorkspaceWorktreeStatus(entries []workspace.Entry) ([]workspace.Entry, error) {
	if len(entries) == 0 || s.worktrees == nil {
		return entries, nil
	}
	for i := range entries {
		config, err := s.worktrees.GetConfig(entries[i].Path)
		if err != nil {
			return nil, err
		}
		entries[i].WorktreeEnabled = config.Enabled
	}
	return entries, nil
}

type workspaceOverviewResponse struct {
	OK               bool                         `json:"ok"`
	CurrentWorkspace *workspace.Resolution        `json:"current_workspace,omitempty"`
	Workspaces       []workspaceOverviewWorkspace `json:"workspaces"`
	Directories      []workspace.DiscoverEntry    `json:"directories"`
	Cursor           int                          `json:"cursor,omitempty"`
	Limit            int                          `json:"limit,omitempty"`
	NextCursor       int                          `json:"next_cursor,omitempty"`
	HasMore          bool                         `json:"has_more,omitempty"`
	TotalWorkspaces  int                          `json:"total_workspaces,omitempty"`
	SwarmTarget      *swarmTarget                 `json:"swarm_target,omitempty"`
}

func (s *Server) handleWorkspaceOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	_, currentTarget, err := s.swarmTargetsForRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if currentTarget != nil && !strings.EqualFold(strings.TrimSpace(currentTarget.Relationship), "self") {
		if strings.TrimSpace(currentTarget.BackendURL) == "" {
			writeError(w, http.StatusBadGateway, errors.New("selected swarm target is missing backend_url"))
			return
		}
		if err := s.handleWorkspaceOverviewForRemoteTarget(w, r, *currentTarget); err != nil {
			writeError(w, http.StatusBadGateway, err)
		}
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("workspace service"))
		return
	}
	if s.sessions == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("session service"))
		return
	}

	workspaceLimit := parsePositiveIntOrDefault(r.URL.Query().Get("workspace_limit"), 200)
	discoverLimit := parsePositiveIntOrDefault(r.URL.Query().Get("discover_limit"), 200)
	sessionLimit := parsePositiveIntOrDefault(r.URL.Query().Get("session_limit"), workspaceOverviewDefaultSessionLimit)
	permissionLimit := parsePositiveIntOrDefault(r.URL.Query().Get("permission_limit"), workspaceOverviewDefaultPermissionLimit)
	cursor := parsePositiveIntOrDefault(r.URL.Query().Get("cursor"), 0)
	pageLimit := parsePositiveIntOrDefault(r.URL.Query().Get("limit"), 25)
	if pageLimit <= 0 {
		pageLimit = 25
	}
	if pageLimit > 100 {
		pageLimit = 100
	}

	var roots []string
	if raw := strings.TrimSpace(r.URL.Query().Get("roots")); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			part = strings.TrimSpace(part)
			if part != "" {
				roots = append(roots, part)
			}
		}
	}

	workspaces, err := s.workspace.ListKnown(workspaceLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	workspaces, err = s.applyWorkspaceWorktreeStatus(workspaces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	current, currentOK, err := s.workspace.CurrentBinding()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
		resolvedCurrent, resolveErr := s.workspace.Resolve(cwd)
		if resolveErr != nil {
			writeError(w, http.StatusBadRequest, resolveErr)
			return
		}
		current = resolvedCurrent
		currentOK = true
	}

	directories, err := s.workspace.Discover(roots, discoverLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	workspacePaths := make([]string, 0, len(workspaces))
	for _, entry := range workspaces {
		workspacePaths = append(workspacePaths, entry.Path)
	}
	todoSummaries := make(map[string]pebblestore.WorkspaceTodoSummary, len(workspacePaths))
	if s.todos != nil && len(workspacePaths) > 0 {
		todoSummaries, err = s.todos.Summaries(workspacePaths)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	groupedSessions, err := s.sessions.ListTopSessionsByWorkspace(workspacePaths, sessionLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	sessionsByWorkspace, err := s.workspaceOverviewSessionsByWorkspace(groupedSessions, permissionLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	responseWorkspaces := make([]workspaceOverviewWorkspace, 0, len(workspaces))
	for _, entry := range workspaces {
		responseWorkspaces = append(responseWorkspaces, workspaceOverviewWorkspace{
			Entry:                   entry,
			Sessions:                sessionsByWorkspace[strings.TrimSpace(entry.Path)],
			TodoSummary:             todoSummaries[strings.TrimSpace(entry.Path)],
			gitStatusResponseFields: gitStatusResponseForPath(entry.Path),
		})
	}
	totalWorkspaces := len(responseWorkspaces)
	if cursor < 0 {
		cursor = 0
	}
	end := cursor + pageLimit
	if end > totalWorkspaces {
		end = totalWorkspaces
	}
	pagedWorkspaces := make([]workspaceOverviewWorkspace, 0, end-cursor)
	if cursor < totalWorkspaces {
		pagedWorkspaces = append(pagedWorkspaces, responseWorkspaces[cursor:end]...)
	}
	nextCursor := 0
	if end < totalWorkspaces {
		nextCursor = end
	}

	var currentPayload *workspace.Resolution
	if currentOK {
		currentCopy := current
		currentPayload = &currentCopy
	}

	writeJSON(w, http.StatusOK, workspaceOverviewResponse{
		OK:               true,
		CurrentWorkspace: currentPayload,
		Workspaces:       pagedWorkspaces,
		Directories:      directories,
		Cursor:           cursor,
		Limit:            pageLimit,
		NextCursor:       nextCursor,
		HasMore:          nextCursor > 0,
		TotalWorkspaces:  totalWorkspaces,
		SwarmTarget:      currentTarget,
	})
}

func (s *Server) handleWorkspaceOverviewForRemoteTarget(w http.ResponseWriter, r *http.Request, currentTarget swarmTarget) error {
	if s.swarm == nil {
		return errors.New("swarm service not configured")
	}
	cfg, err := s.loadStartupConfig()
	if err != nil {
		return err
	}
	state, err := s.currentSwarmState(cfg)
	if err != nil {
		return err
	}
	peerToken, err := s.outgoingPeerAuthTokenForTarget(r, currentTarget)
	if err != nil {
		return err
	}
	endpoint, err := cloneURLWithQuery(strings.TrimRight(currentTarget.BackendURL, "/")+"/v1/workspace/overview", r.URL.Query())
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header = cloneHeaderExcludingAuth(r.Header)
	req.Header.Set("Accept", "application/json")
	req.Header.Set(peerAuthSwarmIDHeader, strings.TrimSpace(state.Node.SwarmID))
	req.Header.Set(peerAuthTokenHeader, peerToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var remoteErr struct {
			Error string `json:"error"`
		}
		_ = json.Unmarshal(payload, &remoteErr)
		return errors.New(firstNonEmpty(strings.TrimSpace(remoteErr.Error), resp.Status))
	}
	var decoded workspaceOverviewResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return err
	}
	decoded.SwarmTarget = &currentTarget
	writeJSON(w, http.StatusOK, decoded)
	return nil
}

func (s *Server) workspaceOverviewSessionsByWorkspace(groups []pebblestore.WorkspaceSessionList, permissionLimit int) (map[string][]workspaceOverviewSession, error) {
	result := make(map[string][]workspaceOverviewSession, len(groups))
	if len(groups) == 0 {
		return result, nil
	}

	type job struct {
		workspacePath string
		index         int
		session       pebblestore.SessionSnapshot
	}
	type jobResult struct {
		workspacePath string
		index         int
		session       workspaceOverviewSession
		err           error
	}

	totalJobs := 0
	for _, group := range groups {
		workspacePath := strings.TrimSpace(group.WorkspacePath)
		result[workspacePath] = make([]workspaceOverviewSession, len(group.Sessions))
		totalJobs += len(group.Sessions)
	}
	if totalJobs == 0 {
		return result, nil
	}

	jobs := make(chan job, totalJobs)
	results := make(chan jobResult, totalJobs)
	workerCount := workspaceOverviewPermissionParallelism
	if workerCount > totalJobs {
		workerCount = totalJobs
	}
	if workerCount <= 0 {
		workerCount = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				enriched := workspaceOverviewSession{SessionSnapshot: item.session}
				enriched.gitStatusResponseFields = gitStatusResponseForSession(item.session)
				enriched.GitCommitDetected = gitCommitDetectedForSession(item.session, enriched.gitStatusResponseFields)
				enriched.GitCommitCount = gitCommitCountForSession(item.session, enriched.gitStatusResponseFields)
				if s.perm != nil {
					records, err := s.perm.ListPending(item.session.ID, permissionLimit)
					if err != nil {
						results <- jobResult{err: err}
						continue
					}
					enriched.PendingPermissions = records
					enriched.PendingPermissionCount = len(records)
				}
				if lifecycle := item.session.Lifecycle; lifecycle != nil && lifecycle.Active {
					enriched.ActiveRun = &runStreamActiveRun{
						RunID:  strings.TrimSpace(lifecycle.RunID),
						Status: strings.TrimSpace(lifecycle.Phase),
					}
				}
				enriched.SessionStatus = workspaceOverviewSessionStatus(item.session.Lifecycle)
				results <- jobResult{
					workspacePath: item.workspacePath,
					index:         item.index,
					session:       enriched,
				}
			}
		}()
	}

	for _, group := range groups {
		workspacePath := strings.TrimSpace(group.WorkspacePath)
		for i, session := range group.Sessions {
			jobs <- job{workspacePath: workspacePath, index: i, session: session}
		}
	}
	close(jobs)

	for i := 0; i < totalJobs; i++ {
		item := <-results
		if item.err != nil {
			wg.Wait()
			return nil, item.err
		}
		result[item.workspacePath][item.index] = item.session
	}
	wg.Wait()
	return result, nil
}

func workspaceOverviewSessionStatus(lifecycle *pebblestore.SessionLifecycleSnapshot) string {
	if lifecycle == nil {
		return "idle"
	}
	phase := strings.ToLower(strings.TrimSpace(lifecycle.Phase))
	if lifecycle.Active {
		switch phase {
		case "blocked":
			return "blocked"
		case "starting":
			return "starting"
		case "running":
			return "running"
		default:
			return "running"
		}
	}
	switch phase {
	case "cancelled", "errored", "interrupted":
		return phase
	default:
		return "idle"
	}
}

func parsePositiveIntOrDefault(raw string, fallback int) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func errServiceNotConfigured(name string) error {
	return &serviceConfigError{name: strings.TrimSpace(name)}
}

type serviceConfigError struct{ name string }

func (e *serviceConfigError) Error() string {
	if e == nil || strings.TrimSpace(e.name) == "" {
		return "service not configured"
	}
	return strings.TrimSpace(e.name) + " not configured"
}

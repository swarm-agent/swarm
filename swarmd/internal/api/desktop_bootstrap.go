package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

const workspaceOverviewDefaultSessionLimit = 25
const workspaceOverviewDefaultPermissionLimit = 200
const workspaceOverviewPermissionParallelism = 8
const workspaceOverviewGitStatusParallelism = 8

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
	Sessions       []workspaceOverviewSession       `json:"sessions"`
	TodoSummary    pebblestore.WorkspaceTodoSummary `json:"todo_summary"`
	TopologyRoutes []workspaceOverviewTopologyRoute `json:"topology_routes,omitempty"`
	gitStatusResponseFields
}

const workspaceOverviewTopologyRouteSource = "topology/workspace_binding"

type workspaceOverviewTopologyRoute struct {
	RouteID              string                               `json:"route_id"`
	RouteSource          string                               `json:"route_source"`
	WorkspaceBindingID   string                               `json:"workspace_binding_id"`
	RuntimeSwarmID       string                               `json:"runtime_swarm_id"`
	RuntimeSwarmName     string                               `json:"runtime_swarm_name,omitempty"`
	RuntimeKind          string                               `json:"runtime_kind,omitempty"`
	RuntimeRelationship  string                               `json:"runtime_relationship,omitempty"`
	AuthorityHostSwarmID string                               `json:"authority_host_swarm_id,omitempty"`
	HostSwarmID          string                               `json:"host_swarm_id,omitempty"`
	HostSwarmName        string                               `json:"host_swarm_name,omitempty"`
	HostWorkspacePath    string                               `json:"host_workspace_path"`
	HostWorkspaceName    string                               `json:"host_workspace_name,omitempty"`
	RuntimeWorkspacePath string                               `json:"runtime_workspace_path"`
	ReplicationMode      string                               `json:"replication_mode,omitempty"`
	Writable             bool                                 `json:"writable"`
	Sync                 pebblestore.WorkspaceReplicationSync `json:"sync,omitempty"`
	CreatedAt            int64                                `json:"created_at"`
	UpdatedAt            int64                                `json:"updated_at"`
	TUIPrimaryCWD        bool                                 `json:"tui_primary_cwd,omitempty"`
	UnavailableReason    string                               `json:"unavailable_reason,omitempty"`
}

func (s *Server) applyWorkspaceWorktreeStatus(principal identity.Principal, entries []workspace.Entry) ([]workspace.Entry, error) {
	if len(entries) == 0 || s.worktrees == nil {
		return entries, nil
	}
	for i := range entries {
		config, err := s.worktrees.GetConfigForSavedWorkspaceForPrincipal(principal, entries[i].Path)
		if err != nil {
			return nil, err
		}
		entries[i].WorktreeEnabled = config.Enabled
	}
	return entries, nil
}

type workspaceOverviewResponse struct {
	OK               bool                         `json:"ok"`
	DetailsIncluded  bool                         `json:"details_included"`
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
	swarmTargets, currentTarget, err := s.swarmTargetsForRequest(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if s.workspace == nil {
		writeError(w, http.StatusInternalServerError, errServiceNotConfigured("workspace service"))
		return
	}
	principal, principalOK := PrincipalFromRequest(r)
	if !principalOK {
		writeError(w, http.StatusUnauthorized, identity.ErrPrincipalRequired)
		return
	}
	includeDetails := !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_details")), "false")
	if includeDetails && s.sessions == nil {
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

	allWorkspaces, err := s.workspace.ListKnownForPrincipal(principal, workspaceLimit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	totalWorkspaces := len(allWorkspaces)
	if cursor < 0 {
		cursor = 0
	}
	end := cursor + pageLimit
	if end > totalWorkspaces {
		end = totalWorkspaces
	}
	workspaces := make([]workspace.Entry, 0, max(0, end-cursor))
	if cursor < totalWorkspaces {
		workspaces = append(workspaces, allWorkspaces[cursor:end]...)
	}
	workspaces, err = s.applyWorkspaceWorktreeStatus(principal, workspaces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	current, currentOK, err := s.workspace.CurrentBindingForPrincipal(principal)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if cwd := strings.TrimSpace(r.URL.Query().Get("cwd")); cwd != "" {
		resolvedCurrent, resolveErr := s.workspace.ResolveForPrincipal(principal, cwd)
		if resolveErr != nil {
			writeError(w, http.StatusBadRequest, resolveErr)
			return
		}
		current = resolvedCurrent
		currentOK = true
	}

	var directories []workspace.DiscoverEntry
	if !strings.EqualFold(strings.TrimSpace(r.URL.Query().Get("include_discovered")), "false") {
		directories, err = s.workspace.Discover(roots, discoverLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	workspacePaths := make([]string, 0, len(workspaces))
	for _, entry := range workspaces {
		workspacePath := strings.TrimSpace(entry.Path)
		workspacePaths = append(workspacePaths, workspacePath)
	}
	todoSummaries := make(map[string]pebblestore.WorkspaceTodoSummary, len(workspacePaths))
	if includeDetails && s.todos != nil && len(workspacePaths) > 0 {
		todoSummaries, err = s.todos.Summaries(workspacePaths)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}
	// Catalog-first callers need identity, routing, and settings, not session
	// snapshots, permission payloads, todo scans, or Git subprocesses.
	sessionsByWorkspace := make(map[string][]workspaceOverviewSession)
	if includeDetails {
		groupedSessions, listErr := s.sessions.ListTopSessionsByWorkspace(workspacePaths, sessionLimit)
		if listErr != nil {
			writeError(w, http.StatusInternalServerError, listErr)
			return
		}
		sessionsByWorkspace, err = s.workspaceOverviewSessionsByWorkspace(groupedSessions, permissionLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
	}

	topologyRoutesByWorkspace, err := s.workspaceOverviewTopologyRoutesByWorkspace(principal, swarmTargets, allWorkspaces)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}

	primaryTarget := primarySelfSwarmTarget(swarmTargets)
	primarySwarmID := ""
	if primaryTarget != nil {
		primarySwarmID = strings.TrimSpace(primaryTarget.SwarmID)
	}
	localBindingByWorkspaceID := localWorkspaceBindingIDsByWorkspaceID(allWorkspaces, topologyRoutesByWorkspace, primarySwarmID)
	if currentOK {
		current.LocalWorkspaceBindingID = strings.TrimSpace(localBindingByWorkspaceID[strings.TrimSpace(current.WorkspaceID)])
	}
	gitStatuses := make([]gitStatusResponseFields, len(workspaces))
	if includeDetails {
		gitStatuses = workspaceOverviewGitStatuses(workspaces)
	}
	responseWorkspaces := make([]workspaceOverviewWorkspace, 0, len(workspaces))
	for index, entry := range workspaces {
		workspacePath := strings.TrimSpace(entry.Path)
		entry.LocalWorkspaceBindingID = strings.TrimSpace(localBindingByWorkspaceID[strings.TrimSpace(entry.WorkspaceID)])
		responseWorkspaces = append(responseWorkspaces, workspaceOverviewWorkspace{
			Entry:                   entry,
			Sessions:                sessionsByWorkspace[workspacePath],
			TodoSummary:             todoSummaries[workspacePath],
			TopologyRoutes:          topologyRoutesByWorkspace[workspacePath],
			gitStatusResponseFields: gitStatuses[index],
		})
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
		DetailsIncluded:  includeDetails,
		CurrentWorkspace: currentPayload,
		Workspaces:       responseWorkspaces,
		Directories:      directories,
		Cursor:           cursor,
		Limit:            pageLimit,
		NextCursor:       nextCursor,
		HasMore:          nextCursor > 0,
		TotalWorkspaces:  totalWorkspaces,
		SwarmTarget:      currentTarget,
	})
}

func workspaceOverviewGitStatuses(entries []workspace.Entry) []gitStatusResponseFields {
	statuses := make([]gitStatusResponseFields, len(entries))
	if len(entries) == 0 {
		return statuses
	}

	jobs := make(chan int, len(entries))
	workerCount := min(workspaceOverviewGitStatusParallelism, len(entries))
	var wg sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				statuses[index] = gitStatusResponseForPath(entries[index].Path)
			}
		}()
	}
	for index := range entries {
		jobs <- index
	}
	close(jobs)
	wg.Wait()
	return statuses
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
				if runState, ok, err := s.sessions.GetSessionRunState(item.session.ID); err != nil {
					results <- jobResult{err: err}
					continue
				} else {
					enriched.ActiveRun = workspaceOverviewActiveRun(runState, ok)
					enriched.SessionStatus = workspaceOverviewSessionStatus(runState, ok)
				}
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

func (s *Server) workspaceOverviewTopologyRoutesByWorkspace(principal identity.Principal, swarmTargets []swarmTarget, workspaces []workspace.Entry) (map[string][]workspaceOverviewTopologyRoute, error) {
	out := make(map[string][]workspaceOverviewTopologyRoute)
	if s == nil || s.topology == nil {
		return out, nil
	}
	if _, err := s.topology.EnsureSnapshot(); err != nil {
		return nil, err
	}
	accountScopeID := strings.TrimSpace(principal.AccountScopeID)
	if accountScopeID == "" {
		return out, nil
	}
	bindings, err := s.topology.ListWorkspaceBindingsForAccount(accountScopeID, 100000)
	if err != nil {
		return nil, err
	}
	workspacePathByID := make(map[string]string, len(workspaces))
	workspaceNameByID := make(map[string]string, len(workspaces))
	for _, entry := range workspaces {
		workspaceID := strings.TrimSpace(entry.WorkspaceID)
		workspacePath := strings.TrimSpace(entry.Path)
		if workspaceID == "" || workspacePath == "" {
			continue
		}
		workspacePathByID[workspaceID] = workspacePath
		workspaceNameByID[workspaceID] = strings.TrimSpace(entry.WorkspaceName)
	}
	runtimeTargets := make(map[string]swarmTarget, len(swarmTargets))
	for _, target := range swarmTargets {
		if swarmID := strings.TrimSpace(target.SwarmID); swarmID != "" {
			runtimeTargets[strings.ToLower(swarmID)] = target
		}
	}
	topologyRuntimes := make(map[string]pebblestore.TopologyRuntimeRecord)
	if runtimes, err := s.topology.ListRuntimesForAccount(accountScopeID, 100000); err == nil {
		for _, runtime := range runtimes {
			if swarmID := strings.TrimSpace(runtime.SwarmID); swarmID != "" {
				topologyRuntimes[strings.ToLower(swarmID)] = runtime
			}
		}
	} else {
		return nil, err
	}
	seenByWorkspace := make(map[string]map[string]struct{})
	for _, binding := range bindings {
		workspaceBindingID := strings.TrimSpace(binding.BindingID)
		workspaceID := strings.TrimSpace(binding.SourceWorkspaceID)
		workspacePath := workspacePathByID[workspaceID]
		runtimeSwarmID := strings.TrimSpace(binding.DestinationRuntimeSwarmID)
		runtimeWorkspacePath := strings.TrimSpace(binding.DestinationWorkspacePath)
		if workspaceBindingID == "" || workspaceID == "" || workspacePath == "" || runtimeSwarmID == "" || runtimeWorkspacePath == "" {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(binding.State), pebblestore.TopologyWorkspaceBindingStateBound) {
			continue
		}
		runtimeRecord := topologyRuntimes[strings.ToLower(runtimeSwarmID)]
		runtimeTarget, runtimeAlive := runtimeTargets[strings.ToLower(runtimeSwarmID)]
		if !runtimeAlive {
			continue
		}
		if !runtimeTarget.Online || !runtimeTarget.Selectable {
			continue
		}
		workspaceRoute, ok := s.workspaceOverviewTopologyRouteForBinding(binding, runtimeTarget, runtimeRecord, runtimeTargets, workspacePath, workspacePath, workspaceNameByID[workspaceID])
		if ok {
			appendWorkspaceOverviewTopologyRoute(out, seenByWorkspace, workspacePath, workspaceRoute)
		}
	}
	for workspacePath := range out {
		sort.SliceStable(out[workspacePath], func(i, j int) bool {
			left := strings.ToLower(out[workspacePath][i].RuntimeSwarmName)
			right := strings.ToLower(out[workspacePath][j].RuntimeSwarmName)
			if left == right {
				return out[workspacePath][i].RouteID < out[workspacePath][j].RouteID
			}
			return left < right
		})
	}
	return out, nil
}

func (s *Server) workspaceOverviewTopologyRouteForBinding(binding pebblestore.TopologyWorkspaceBindingRecord, runtimeTarget swarmTarget, runtimeRecord pebblestore.TopologyRuntimeRecord, runtimeTargets map[string]swarmTarget, workspacePath, hostWorkspacePath, hostWorkspaceName string) (workspaceOverviewTopologyRoute, bool) {
	runtimeSwarmID := strings.TrimSpace(binding.DestinationRuntimeSwarmID)
	runtimeWorkspacePath := strings.TrimSpace(binding.DestinationWorkspacePath)
	workspaceBindingID := strings.TrimSpace(binding.BindingID)
	routeID := workspaceOverviewTopologyRouteID(runtimeSwarmID, workspaceBindingID)
	if routeID == "" || strings.TrimSpace(workspacePath) == "" {
		return workspaceOverviewTopologyRoute{}, false
	}
	authorityHostSwarmID := firstNonEmpty(strings.TrimSpace(binding.DestinationAuthorityHostSwarmID), strings.TrimSpace(binding.DestinationHostSwarmID), strings.TrimSpace(runtimeRecord.OwnerHostSwarmID), strings.TrimSpace(runtimeTarget.HostSwarmID))
	hostSwarmName := ""
	if authorityHostSwarmID != "" {
		if hostTarget, ok := runtimeTargets[strings.ToLower(authorityHostSwarmID)]; ok {
			hostSwarmName = firstNonEmpty(strings.TrimSpace(hostTarget.Name), authorityHostSwarmID)
		}
		if hostSwarmName == "" {
			hostSwarmName = authorityHostSwarmID
		}
	}
	return workspaceOverviewTopologyRoute{
		RouteID:              routeID,
		RouteSource:          workspaceOverviewTopologyRouteSource,
		WorkspaceBindingID:   workspaceBindingID,
		RuntimeSwarmID:       runtimeSwarmID,
		RuntimeSwarmName:     firstNonEmpty(strings.TrimSpace(runtimeTarget.Name), runtimeSwarmID),
		RuntimeKind:          firstNonEmpty(strings.TrimSpace(binding.DestinationRuntimeKind), strings.TrimSpace(runtimeTarget.Kind)),
		RuntimeRelationship:  strings.TrimSpace(runtimeTarget.Relationship),
		AuthorityHostSwarmID: authorityHostSwarmID,
		HostSwarmID:          authorityHostSwarmID,
		HostSwarmName:        hostSwarmName,
		HostWorkspacePath:    strings.TrimSpace(hostWorkspacePath),
		HostWorkspaceName:    strings.TrimSpace(hostWorkspaceName),
		RuntimeWorkspacePath: runtimeWorkspacePath,
		ReplicationMode:      strings.TrimSpace(binding.ReplicationMode),
		Writable:             binding.Writable,
		Sync:                 binding.Sync,
		CreatedAt:            binding.CreatedAt,
		UpdatedAt:            binding.UpdatedAt,
	}, true
}

func localWorkspaceBindingIDsByWorkspaceID(workspaces []workspace.Entry, routesByWorkspace map[string][]workspaceOverviewTopologyRoute, primarySwarmID string) map[string]string {
	out := make(map[string]string)
	workspaceIDByPath := make(map[string]string, len(workspaces))
	for _, entry := range workspaces {
		workspacePath := strings.TrimSpace(entry.Path)
		workspaceID := strings.TrimSpace(entry.WorkspaceID)
		if workspacePath != "" && workspaceID != "" {
			workspaceIDByPath[workspacePath] = workspaceID
		}
	}
	primarySwarmID = strings.TrimSpace(primarySwarmID)
	if primarySwarmID == "" {
		return out
	}
	for workspacePath, routes := range routesByWorkspace {
		workspaceID := workspaceIDByPath[strings.TrimSpace(workspacePath)]
		if workspaceID == "" {
			continue
		}
		for _, route := range routes {
			if strings.EqualFold(strings.TrimSpace(route.RuntimeSwarmID), primarySwarmID) && strings.EqualFold(strings.TrimSpace(route.AuthorityHostSwarmID), primarySwarmID) {
				out[workspaceID] = strings.TrimSpace(route.WorkspaceBindingID)
				break
			}
		}
	}
	return out
}

func appendWorkspaceOverviewTopologyRoute(out map[string][]workspaceOverviewTopologyRoute, seenByWorkspace map[string]map[string]struct{}, workspacePath string, route workspaceOverviewTopologyRoute) {
	workspacePath = strings.TrimSpace(workspacePath)
	if workspacePath == "" || strings.TrimSpace(route.RouteID) == "" {
		return
	}
	if seenByWorkspace[workspacePath] == nil {
		seenByWorkspace[workspacePath] = make(map[string]struct{})
	}
	if _, seen := seenByWorkspace[workspacePath][route.RouteID]; seen {
		return
	}
	seenByWorkspace[workspacePath][route.RouteID] = struct{}{}
	out[workspacePath] = append(out[workspacePath], route)
}

func workspaceOverviewTopologyRouteID(runtimeSwarmID, workspaceBindingID string) string {
	runtimeSwarmID = strings.TrimSpace(runtimeSwarmID)
	workspaceBindingID = strings.TrimSpace(workspaceBindingID)
	if runtimeSwarmID == "" || workspaceBindingID == "" {
		return ""
	}
	return "swarm:" + runtimeSwarmID + ":binding:" + workspaceBindingID
}

func workspaceOverviewActiveRun(runState sessionruntime.SessionRunState, ok bool) *runStreamActiveRun {
	if !ok || !runState.Active || strings.TrimSpace(runState.RunID) == "" {
		return nil
	}
	return &runStreamActiveRun{
		RunID:     strings.TrimSpace(runState.RunID),
		Status:    strings.TrimSpace(runState.Status),
		CreatedAt: runState.CreatedAt,
		StartedAt: runState.StartedAt,
		UpdatedAt: runState.UpdatedAt,
		EventSeq:  runState.EventSeq,
	}
}

func workspaceOverviewSessionStatus(runState sessionruntime.SessionRunState, ok bool) string {
	if !ok {
		return "idle"
	}
	switch strings.ToLower(strings.TrimSpace(runState.Status)) {
	case sessionruntime.RunIntentPendingExecutor:
		if runState.Active {
			return "starting"
		}
	case sessionruntime.RunIntentRunning:
		if runState.Active {
			return "running"
		}
	case sessionruntime.RunIntentDispatchBlocked:
		return "blocked"
	case sessionruntime.RunIntentCancelled:
		return "cancelled"
	case sessionruntime.RunIntentFailed:
		return "errored"
	case sessionruntime.RunIntentInterrupted:
		return "interrupted"
	}
	return "idle"
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

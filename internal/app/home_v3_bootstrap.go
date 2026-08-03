package app

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
)

const homeBootstrapWorkspaceLimit = 200

// claimInitialHomeWorkspaceBootstrap limits launch-CWD fallback to the first
// successful home bootstrap. Registered workspaces keep first-item precedence.
func (a *App) claimInitialHomeWorkspaceBootstrap() bool {
	return a != nil && a.homeWorkspaceBootstrapped.CompareAndSwap(false, true)
}

type homeBootstrapData struct {
	current         client.WorkspaceResolution
	hasCurrent      bool
	currentErr      error
	workspaces      []client.WorkspaceEntry
	workspacesErr   error
	selectedResolve client.WorkspaceCWDResolveResponse
	selectedErr     error
	launchResolve   client.WorkspaceCWDResolveResponse
	launchChecked   bool
	launchErr       error
}

// bootstrapHomeWorkspace loads bounded workspace identity without touching any
// session list, history, transcript, workset, or realtime endpoint.
func (a *App) bootstrapHomeWorkspace(ctx context.Context, preferLaunchWorkspace bool) homeBootstrapData {
	var data homeBootstrapData
	if a == nil || a.api == nil {
		data.currentErr = fmt.Errorf("workspace client unavailable")
		return data
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		data.current, data.hasCurrent, data.currentErr = a.api.WorkspaceCurrent(ctx)
	}()
	go func() {
		defer wg.Done()
		data.workspaces, data.workspacesErr = a.api.ListWorkspaces(ctx, homeBootstrapWorkspaceLimit)
	}()
	wg.Wait()

	selectedPath := firstRegisteredWorkspacePath(data.workspaces)
	if selectedPath == "" && data.currentErr == nil && data.hasCurrent {
		selectedPath = firstNonEmpty(
			normalizePath(data.current.WorkspacePath),
			normalizePath(data.current.ResolvedPath),
		)
	}
	var resolveWG sync.WaitGroup
	if selectedPath != "" {
		resolveWG.Add(1)
		go func() {
			defer resolveWG.Done()
			data.selectedResolve, data.selectedErr = a.api.WorkspaceCWDResolve(ctx, selectedPath)
		}()
	}
	launchPath := normalizePath(a.startupCWD)
	if preferLaunchWorkspace && selectedPath == "" && launchPath != "" {
		data.launchChecked = true
		resolveWG.Add(1)
		go func() {
			defer resolveWG.Done()
			data.launchResolve, data.launchErr = a.api.WorkspaceCWDResolve(ctx, launchPath)
		}()
	}
	resolveWG.Wait()
	return data
}

func applyHomeWorkspaceBootstrap(next model.HomeModel, data homeBootstrapData, startupCWD string) (model.HomeModel, string, []string) {
	warnings := make([]string, 0, 3)
	if data.currentErr != nil {
		warnings = append(warnings, "default workspace unavailable")
	}
	if data.workspacesErr != nil {
		warnings = append(warnings, "workspace list unavailable")
	}

	selectedPath := firstRegisteredWorkspacePath(data.workspaces)
	selectedName := firstRegisteredWorkspaceName(data.workspaces, selectedPath)
	selectedResolve := data.selectedResolve
	selectedErr := data.selectedErr
	if selectedPath == "" && data.currentErr == nil && data.hasCurrent {
		selectedPath = firstNonEmpty(normalizePath(data.current.WorkspacePath), normalizePath(data.current.ResolvedPath))
		selectedName = strings.TrimSpace(data.current.WorkspaceName)
	}
	if selectedPath == "" && data.launchChecked && data.launchErr == nil && data.launchResolve.Workspace != nil {
		selectedPath = firstNonEmpty(
			normalizePath(data.launchResolve.Workspace.WorkspacePath),
			normalizePath(data.launchResolve.Workspace.ResolvedPath),
		)
		selectedName = strings.TrimSpace(data.launchResolve.Workspace.WorkspaceName)
		selectedResolve = data.launchResolve
		selectedErr = nil
	}
	selectedResolvePath := firstNonEmpty(
		normalizePath(selectedResolve.ResolvedPath),
		func() string {
			if selectedResolve.Workspace == nil {
				return ""
			}
			return normalizePath(selectedResolve.Workspace.WorkspacePath)
		}(),
	)
	if selectedPath != "" && selectedResolvePath != "" && !pathsEqual(selectedPath, selectedResolvePath) {
		selectedErr = fmt.Errorf("workspace route resolved a different workspace")
	}
	for i, entry := range data.workspaces {
		path := normalizePath(entry.Path)
		if path == "" {
			continue
		}
		name := strings.TrimSpace(entry.WorkspaceName)
		if name == "" {
			name = filepath.Base(path)
		}
		directories := append([]string(nil), entry.Directories...)
		if len(directories) == 0 {
			directories = []string{path}
		}
		active := selectedPath != "" && pathsEqual(path, selectedPath)
		next.Workspaces = append(next.Workspaces, model.Workspace{
			Name:                emptyFallback(name, "workspace"),
			Path:                path,
			WorkspaceID:         strings.TrimSpace(entry.WorkspaceID),
			WorkspaceGeneration: entry.WorkspaceGeneration,
			Directories:         directories,
			ThemeID:             strings.TrimSpace(entry.ThemeID),
			Icon:                workspaceIcon(i),
			Active:              active,
		})
		next.Directories = append(next.Directories, model.DirectoryItem{
			Name:         emptyFallback(name, "workspace"),
			Path:         displayPath(path),
			ResolvedPath: path,
			Branch:       "-",
			AgentsToken:  "none",
			IsWorkspace:  true,
		})
	}

	if selectedPath != "" && activeWorkspaceIndex(next.Workspaces) < 0 {
		next.Workspaces = append([]model.Workspace{{
			Name:        emptyFallback(selectedName, filepath.Base(selectedPath)),
			Path:        selectedPath,
			Directories: []string{selectedPath},
			Icon:        workspaceIcon(0),
			Active:      true,
		}}, next.Workspaces...)
		next.Directories = append([]model.DirectoryItem{{
			Name:         emptyFallback(selectedName, filepath.Base(selectedPath)),
			Path:         displayPath(selectedPath),
			ResolvedPath: selectedPath,
			Branch:       "-",
			AgentsToken:  "none",
			IsWorkspace:  true,
		}}, next.Directories...)
	}

	if selectedErr == nil && selectedPath != "" {
		next = applyCWDResolverToHomeModel(next, selectedResolve)
	} else if selectedErr != nil {
		warnings = append(warnings, "workspace route unavailable")
	}
	if selectedPath != "" && len(next.ChatRoutes) == 0 {
		next.ChatRoutes = buildChatRoutesForHomeModel(next, selectedPath)
	}

	startupCWD = normalizePath(startupCWD)
	if data.launchChecked && data.launchErr == nil && data.launchResolve.Workspace == nil && startupCWD != "" && !homePathRegistered(startupCWD, next.Workspaces) {
		warnings = append(warnings, "launch directory is not registered; use /workspace to add it")
	}
	return next, selectedPath, warnings
}

func firstRegisteredWorkspacePath(workspaces []client.WorkspaceEntry) string {
	for _, workspace := range workspaces {
		if path := normalizePath(workspace.Path); path != "" {
			return path
		}
	}
	return ""
}

func firstRegisteredWorkspaceName(workspaces []client.WorkspaceEntry, selectedPath string) string {
	for _, workspace := range workspaces {
		if pathsEqual(workspace.Path, selectedPath) {
			return strings.TrimSpace(workspace.WorkspaceName)
		}
	}
	return ""
}

func homePathRegistered(path string, workspaces []model.Workspace) bool {
	path = normalizePath(path)
	if path == "" {
		return false
	}
	for _, workspace := range workspaces {
		if workspaceModelMatchDepth(workspace, path) >= 0 {
			return true
		}
	}
	return false
}

func buildHomeSessionIntent(page *ui.HomePage, route model.ChatRoute) ui.HomeSessionIntent {
	if page == nil {
		return ui.HomeSessionIntent{}
	}
	state := page.HomepageState()
	return ui.HomeSessionIntent{
		Workspace:          state.SelectedWorkspace,
		Agent:              state.SelectedAgent,
		Mode:               page.SessionMode(),
		Preference:         client.ModelPreference{Provider: state.ModelProvider, Model: state.ModelName, Thinking: state.Thinking, ServiceTier: state.ServiceTier, ContextMode: state.ContextMode},
		Profile:            state.Profile,
		RouteID:            strings.TrimSpace(route.ID),
		SwarmID:            strings.TrimSpace(route.SwarmID),
		TargetKind:         strings.TrimSpace(route.TargetKind),
		TargetRelationship: strings.TrimSpace(route.TargetRelationship),
	}
}

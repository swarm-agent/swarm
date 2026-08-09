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

// claimInitialHomeWorkspaceBootstrap gives launch-CWD precedence on the first
// successful home bootstrap. An unmatched launch CWD remains authoritative on
// later refreshes until the user explicitly selects a saved workspace.
func (a *App) claimInitialHomeWorkspaceBootstrap() bool {
	return a != nil && a.homeWorkspaceBootstrapped.CompareAndSwap(false, true)
}

func (a *App) shouldResolveLaunchWorkspace() bool {
	if a == nil {
		return false
	}
	if a.claimInitialHomeWorkspaceBootstrap() {
		return true
	}
	return a.activeWorkspacePath() == "" && pathsEqual(a.activeContextPath(), a.startupCWD)
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

	selectedPath := ""
	if data.currentErr == nil && data.hasCurrent {
		selectedPath = firstNonEmpty(
			normalizePath(data.current.WorkspacePath),
			normalizePath(data.current.ResolvedPath),
		)
	}
	if selectedPath == "" {
		selectedPath = firstRegisteredWorkspacePath(data.workspaces)
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
	if preferLaunchWorkspace && launchPath != "" {
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

	selectedPath := ""
	selectedName := ""
	selectedResolve := data.selectedResolve
	selectedErr := data.selectedErr
	if data.currentErr == nil && data.hasCurrent {
		selectedPath = firstNonEmpty(normalizePath(data.current.WorkspacePath), normalizePath(data.current.ResolvedPath))
		selectedName = strings.TrimSpace(data.current.WorkspaceName)
	}
	if selectedPath == "" {
		selectedPath = firstRegisteredWorkspacePath(data.workspaces)
		selectedName = firstRegisteredWorkspaceName(data.workspaces, selectedPath)
	}
	launchWorkspacePath := ""
	launchUsesCWDRoute := false
	launchPath := firstNonEmpty(normalizePath(data.launchResolve.ResolvedPath), normalizePath(startupCWD))
	if data.launchChecked && data.launchErr == nil {
		if data.launchResolve.Workspace != nil {
			launchWorkspacePath = registeredWorkspacePath(data.workspaces, firstNonEmpty(
				normalizePath(data.launchResolve.Workspace.WorkspacePath),
				normalizePath(data.launchResolve.Workspace.ResolvedPath),
			))
			if launchWorkspacePath != "" && !registeredWorkspaceOwnsLaunchPath(data.workspaces, launchWorkspacePath, launchPath) {
				launchWorkspacePath = ""
			}
			if launchWorkspacePath != "" {
				selectedPath = launchWorkspacePath
				selectedName = firstNonEmpty(
					strings.TrimSpace(data.launchResolve.Workspace.WorkspaceName),
					firstRegisteredWorkspaceName(data.workspaces, launchWorkspacePath),
				)
				selectedResolve = data.launchResolve
				selectedErr = nil
			}
		} else if selectedPath == "" {
			// With no saved workspace to fall back to, keep the launch directory
			// usable as the TUI CWD route while guiding the user to save it.
			selectedResolve = data.launchResolve
			selectedErr = nil
			launchUsesCWDRoute = true
		}
	}
	selectedResolveWorkspacePath := ""
	if selectedResolve.Workspace != nil {
		selectedResolveWorkspacePath = firstNonEmpty(
			normalizePath(selectedResolve.Workspace.WorkspacePath),
			normalizePath(selectedResolve.Workspace.ResolvedPath),
		)
	}
	if selectedPath != "" && selectedResolveWorkspacePath != "" && !pathsEqual(selectedPath, selectedResolveWorkspacePath) {
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

	if launchUsesCWDRoute {
		launchPath := firstNonEmpty(normalizePath(data.launchResolve.ResolvedPath), normalizePath(startupCWD))
		if launchPath != "" {
			next.Directories = append([]model.DirectoryItem{{
				Name:         emptyFallback(filepath.Base(launchPath), "directory"),
				Path:         displayPath(launchPath),
				ResolvedPath: launchPath,
				Branch:       "-",
				AgentsToken:  "none",
				IsWorkspace:  false,
			}}, next.Directories...)
		}
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

	if selectedErr == nil && (selectedPath != "" || launchUsesCWDRoute) {
		next = applyCWDResolverToHomeModel(next, selectedResolve)
	} else if selectedErr != nil {
		warnings = append(warnings, "workspace route unavailable")
	}
	if selectedPath != "" && len(next.ChatRoutes) == 0 {
		next.ChatRoutes = buildChatRoutesForHomeModel(next, selectedPath)
	}

	startupCWD = normalizePath(startupCWD)
	if launchUsesCWDRoute {
		next.WorkspaceSetupPath = firstNonEmpty(normalizePath(data.launchResolve.ResolvedPath), startupCWD)
	} else if data.launchChecked && data.launchErr == nil && launchWorkspacePath == "" && startupCWD != "" {
		next.WorkspaceSetupPath = startupCWD
	}
	if setupPath := normalizePath(next.WorkspaceSetupPath); setupPath != "" {
		setupGitStatus, _ := gitStatusForPath(setupPath)
		next.WorkspaceSetupHasGit = setupGitStatus.HasGit
	}
	return next, selectedPath, warnings
}

func gitRootForPath(path string) string {
	status, ok := gitStatusForPath(path)
	if !ok || !status.HasGit {
		return ""
	}
	return normalizePath(status.RepoRoot)
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

func registeredWorkspacePath(workspaces []client.WorkspaceEntry, candidate string) string {
	candidate = normalizePath(candidate)
	if candidate == "" {
		return ""
	}
	for _, workspace := range workspaces {
		path := normalizePath(workspace.Path)
		if path != "" && pathsEqual(path, candidate) {
			return path
		}
	}
	return ""
}

func registeredWorkspaceOwnsLaunchPath(workspaces []client.WorkspaceEntry, workspacePath, launchPath string) bool {
	workspacePath = normalizePath(workspacePath)
	launchPath = normalizePath(launchPath)
	if workspacePath == "" || launchPath == "" {
		return false
	}
	launchGitRoot := gitRootForPath(launchPath)
	for _, workspace := range workspaces {
		if !pathsEqual(workspace.Path, workspacePath) {
			continue
		}
		roots := append([]string(nil), workspace.Directories...)
		if len(roots) == 0 {
			roots = []string{workspace.Path}
		}
		for _, root := range roots {
			root = normalizePath(root)
			if root == "" {
				continue
			}
			if pathsEqual(root, launchPath) {
				return true
			}
			if launchGitRoot != "" {
				if pathsEqual(gitRootForPath(root), launchGitRoot) {
					return true
				}
				continue
			}
			if workspacePathMatchDepth(root, launchPath) >= 0 {
				return true
			}
		}
		return false
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

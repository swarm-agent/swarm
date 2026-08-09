package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"sync"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
)

func TestBootstrapHomeWorkspaceResolvesLaunchCWDEvenWithSavedWorkspaces(t *testing.T) {
	var mu sync.Mutex
	resolvedCWDs := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspace/current":
			_ = json.NewEncoder(w).Encode(client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"})
		case "/v1/workspace/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "workspaces": []client.WorkspaceEntry{{Path: "/default", WorkspaceName: "Default"}, {Path: "/launch", WorkspaceName: "Launch"}}})
		case "/v1/workspace/cwd/resolve":
			cwd := r.URL.Query().Get("cwd")
			mu.Lock()
			resolvedCWDs = append(resolvedCWDs, cwd)
			mu.Unlock()
			workspace := "/default"
			if cwd == "/launch/nested" {
				workspace = "/launch"
			}
			_ = json.NewEncoder(w).Encode(client.WorkspaceCWDResolveResponse{OK: true, ResolvedPath: cwd, Workspace: &client.WorkspaceResolution{WorkspacePath: workspace, ResolvedPath: cwd}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	app := &App{api: testAPIWithToken(server.URL), startupCWD: "/launch/nested"}
	data := app.bootstrapHomeWorkspace(context.Background(), true)
	if data.launchErr != nil || !data.launchChecked || data.launchResolve.Workspace == nil || data.launchResolve.Workspace.WorkspacePath != "/launch" {
		t.Fatalf("launch resolution = checked:%v resolve:%#v err:%v", data.launchChecked, data.launchResolve, data.launchErr)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resolvedCWDs) != 2 {
		t.Fatalf("resolved CWDs = %#v, want selected and launch paths", resolvedCWDs)
	}
}

func TestBootstrapHomeWorkspaceSkipsLaunchCWDOnLaterRefresh(t *testing.T) {
	var mu sync.Mutex
	resolvedCWDs := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/workspace/current":
			_ = json.NewEncoder(w).Encode(client.WorkspaceResolution{WorkspacePath: "/manual", ResolvedPath: "/manual", WorkspaceName: "Manual"})
		case "/v1/workspace/list":
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "workspaces": []client.WorkspaceEntry{{Path: "/launch"}, {Path: "/manual"}}})
		case "/v1/workspace/cwd/resolve":
			cwd := r.URL.Query().Get("cwd")
			mu.Lock()
			resolvedCWDs = append(resolvedCWDs, cwd)
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(client.WorkspaceCWDResolveResponse{OK: true, ResolvedPath: cwd, Workspace: &client.WorkspaceResolution{WorkspacePath: cwd, ResolvedPath: cwd}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	app := &App{api: testAPIWithToken(server.URL), startupCWD: "/launch"}
	data := app.bootstrapHomeWorkspace(context.Background(), false)
	if data.launchChecked {
		t.Fatal("later refresh unexpectedly resolved launch CWD")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(resolvedCWDs) != 1 || resolvedCWDs[0] != "/manual" {
		t.Fatalf("resolved CWDs = %#v, want explicit current workspace only", resolvedCWDs)
	}
}

func TestApplyHomeWorkspaceBootstrapSelectsRegisteredLaunchCWD(t *testing.T) {
	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:    client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent: true,
		workspaces: []client.WorkspaceEntry{
			{Path: "/default", WorkspaceName: "Default"},
			{Path: "/launch", WorkspaceName: "Launch"},
		},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   client.WorkspaceCWDResolveResponse{ResolvedPath: "/launch/nested", Workspace: &client.WorkspaceResolution{WorkspacePath: "/launch", ResolvedPath: "/launch"}},
	}, "/launch")

	if selected != "/launch" {
		t.Fatalf("selected path = %q, want /launch", selected)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	if len(next.Workspaces) != 2 || next.Workspaces[0].Active || !next.Workspaces[1].Active {
		t.Fatalf("workspace selection = %#v", next.Workspaces)
	}
}

func TestApplyHomeWorkspaceBootstrapSelectsLinkedLaunchDirectoryWorkspace(t *testing.T) {
	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:    client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent: true,
		workspaces: []client.WorkspaceEntry{
			{Path: "/default", WorkspaceName: "Default"},
			{Path: "/workspace", WorkspaceName: "Workspace", Directories: []string{"/workspace", "/linked"}},
		},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   client.WorkspaceCWDResolveResponse{ResolvedPath: "/linked/pkg", Workspace: &client.WorkspaceResolution{WorkspacePath: "/workspace", ResolvedPath: "/linked/pkg", WorkspaceName: "Workspace"}},
	}, "/linked/pkg")

	if selected != "/workspace" {
		t.Fatalf("selected path = %q, want /workspace", selected)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	if len(next.Workspaces) != 2 || next.Workspaces[0].Active || !next.Workspaces[1].Active {
		t.Fatalf("workspace selection = %#v", next.Workspaces)
	}
}

func TestClaimInitialHomeWorkspaceBootstrapIsOneShot(t *testing.T) {
	app := &App{}
	if !app.claimInitialHomeWorkspaceBootstrap() {
		t.Fatal("initial bootstrap did not claim launch-workspace preference")
	}
	if app.claimInitialHomeWorkspaceBootstrap() {
		t.Fatal("later refresh reclaimed launch-workspace preference")
	}
}

func TestShouldResolveLaunchWorkspaceKeepsUnregisteredLaunchCWD(t *testing.T) {
	app := &App{startupCWD: "/launch", activePath: "/launch"}
	app.homeWorkspaceBootstrapped.Store(true)
	if !app.shouldResolveLaunchWorkspace() {
		t.Fatal("unregistered launch CWD was not preserved on refresh")
	}
	app.workspacePath = "/selected"
	app.activePath = "/selected"
	if app.shouldResolveLaunchWorkspace() {
		t.Fatal("explicit workspace selection was overridden by launch CWD")
	}
}

func TestApplyHomeWorkspaceBootstrapKeepsCurrentWorkspaceAfterLaunchBootstrap(t *testing.T) {
	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:    client.WorkspaceResolution{WorkspacePath: "/manual", ResolvedPath: "/manual", WorkspaceName: "Manual"},
		hasCurrent: true,
		workspaces: []client.WorkspaceEntry{
			{Path: "/launch", WorkspaceName: "Launch"},
			{Path: "/manual", WorkspaceName: "Manual"},
		},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/manual", Workspace: &client.WorkspaceResolution{WorkspacePath: "/manual", ResolvedPath: "/manual"}},
	}, "/launch")

	if selected != "/manual" {
		t.Fatalf("selected path = %q, want /manual", selected)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
	if len(next.Workspaces) != 2 || next.Workspaces[0].Active || !next.Workspaces[1].Active {
		t.Fatalf("workspace selection = %#v", next.Workspaces)
	}
}

func TestApplyHomeWorkspaceBootstrapRejectsResolverPseudoWorkspace(t *testing.T) {
	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:         client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent:      true,
		workspaces:      []client.WorkspaceEntry{{Path: "/default", WorkspaceName: "Default"}},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   client.WorkspaceCWDResolveResponse{ResolvedPath: "/other", Workspace: &client.WorkspaceResolution{WorkspacePath: "/other", ResolvedPath: "/other", WorkspaceName: "Other"}},
	}, "/other")

	if selected != "/default" {
		t.Fatalf("selected path = %q, want /default", selected)
	}
	if len(next.Workspaces) != 1 || !next.Workspaces[0].Active || next.Workspaces[0].Path != "/default" {
		t.Fatalf("pseudo-workspace was added: %#v", next.Workspaces)
	}
	if len(warnings) != 0 || next.WorkspaceSetupPath != "/other" {
		t.Fatalf("warnings = %#v, setup path = %q", warnings, next.WorkspaceSetupPath)
	}
}

func TestApplyHomeWorkspaceBootstrapMarksUnsavedGitLaunchDirectory(t *testing.T) {
	launchPath := t.TempDir()
	if output, err := exec.Command("git", "init", launchPath).CombinedOutput(); err != nil {
		t.Fatalf("init git launch directory: %v: %s", err, output)
	}

	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:         client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent:      true,
		workspaces:      []client.WorkspaceEntry{{Path: "/default", WorkspaceName: "Default"}},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   client.WorkspaceCWDResolveResponse{ResolvedPath: launchPath, ResolutionKind: "non_workspace"},
	}, launchPath)

	if selected != "/default" || len(next.Workspaces) != 1 || !next.Workspaces[0].Active {
		t.Fatalf("default workspace selection = %q %#v", selected, next.Workspaces)
	}
	if len(warnings) != 0 || next.WorkspaceSetupPath != launchPath || !next.WorkspaceSetupHasGit {
		t.Fatalf("warnings = %#v, setup path = %q, has git = %v", warnings, next.WorkspaceSetupPath, next.WorkspaceSetupHasGit)
	}
}

func TestApplyHomeWorkspaceBootstrapKeepsDefaultWorkspaceForUnregisteredLaunchCWD(t *testing.T) {
	launchResolve := client.WorkspaceCWDResolveResponse{
		ResolvedPath:   "/other",
		ResolutionKind: "non_workspace",
		Routes: []client.WorkspaceTopologyRoute{{
			RouteID:              "host",
			RuntimeSwarmID:       "host-swarm",
			RuntimeSwarmName:     "Primary",
			RuntimeKind:          "host",
			RuntimeRelationship:  "self",
			HostWorkspacePath:    "/other",
			RuntimeWorkspacePath: "/other",
			TUIPrimaryCWD:        true,
		}},
	}
	next, selected, warnings := applyHomeWorkspaceBootstrap(model.EmptyHome(), homeBootstrapData{
		current:         client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default", WorkspaceName: "Default"},
		hasCurrent:      true,
		workspaces:      []client.WorkspaceEntry{{Path: "/default", WorkspaceName: "Default"}},
		selectedResolve: client.WorkspaceCWDResolveResponse{ResolvedPath: "/default", Workspace: &client.WorkspaceResolution{WorkspacePath: "/default", ResolvedPath: "/default"}},
		launchChecked:   true,
		launchResolve:   launchResolve,
	}, "/other")

	if selected != "/default" {
		t.Fatalf("selected path = %q, want /default", selected)
	}
	if len(next.Workspaces) != 1 || !next.Workspaces[0].Active {
		t.Fatalf("default workspace was not kept active: %#v", next.Workspaces)
	}
	if len(next.Directories) != 1 || next.Directories[0].ResolvedPath != "/default" || !next.Directories[0].IsWorkspace {
		t.Fatalf("directories = %#v", next.Directories)
	}
	if len(next.ChatRoutes) != 1 || next.ChatRoutes[0].HostWorkspacePath != "/default" || next.ChatRoutes[0].TUIPrimaryCWD {
		t.Fatalf("default workspace route = %#v", next.ChatRoutes)
	}
	if len(warnings) != 0 || next.WorkspaceSetupPath != "/other" {
		t.Fatalf("warnings = %#v, setup path = %q", warnings, next.WorkspaceSetupPath)
	}
}

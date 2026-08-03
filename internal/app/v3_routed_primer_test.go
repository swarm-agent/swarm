package app

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func TestNewCommandDispatchOpensLocalRoutedPrimerWithFlags(t *testing.T) {
	home := ui.NewHomePage(model.HomeModel{ActiveAgent: "swarm"})
	app := &App{api: testAPIWithToken("http://127.0.0.1"), home: home, homeModel: model.HomeModel{ActiveAgent: "swarm"}, route: "home", config: defaultAppConfig()}

	app.executeCommand("/new wp")

	if app.route != "v3chat" || app.v3Chat == nil {
		t.Fatalf("/new wp route = %q page=%p, want local v3chat primer", app.route, app.v3Chat)
	}
	state := app.v3Chat.Runtime().Store().Snapshot()
	draft, ok := v3chat.SelectRoutedDraft(state)
	if !ok || draft.Prompt != "" || !draft.PlanModeRequested || !draft.ManagedWorktreeRequested {
		t.Fatalf("/new wp draft = %#v, ok=%v", draft, ok)
	}
	if state.Session.ID != "" || state.Session.Title != "" || state.Session.WorkspaceName != "" {
		t.Fatalf("bare /new invented durable authority: %#v", state.Session)
	}
	if got := app.v3Chat.Status(); got != "Waiting..." {
		t.Fatalf("bare /new status = %q", got)
	}
}

func TestOpenNewV3ChatProjectsHomeFlagsIntoOneRoutedPrimer(t *testing.T) {
	home := ui.NewHomePage(model.HomeModel{ActiveAgent: "router-agent"})
	app := &App{api: testAPIWithToken("http://127.0.0.1"), home: home, homeModel: model.HomeModel{ActiveAgent: "router-agent"}, route: "home", config: defaultAppConfig()}
	intent := ui.HomeSessionIntent{InitialPrompt: "route this", Mode: "plan", Agent: "router-agent", WorktreeRequested: true}

	if err := app.openNewV3Chat(intent, model.ChatRoute{}, ""); err != nil {
		t.Fatal(err)
	}
	state := app.v3Chat.Runtime().Store().Snapshot()
	draft, ok := v3chat.SelectRoutedDraft(state)
	if !ok || draft.Prompt != "route this" || draft.AgentName != "router-agent" || !draft.PlanModeRequested || !draft.ManagedWorktreeRequested {
		t.Fatalf("routed home draft = %#v, ok=%v", draft, ok)
	}
	if state.Session.ID != "" {
		t.Fatalf("home submit pre-created session %#v", state.Session)
	}
}

func TestRoutedIdentitySelectsAuthoritativeWorkspaceRoute(t *testing.T) {
	app := &App{selectedChatRouteID: "stale", homeModel: model.HomeModel{ChatRoutes: []model.ChatRoute{
		{ID: "route-a", WorkspaceBindingID: "binding-a", SwarmID: "swarm-a"},
		{ID: "route-b", WorkspaceBindingID: "binding-b", SwarmID: "swarm-b"},
	}}}
	identity := &client.RoutedSessionV3Identity{WorkspaceBindingID: "binding-b", RuntimeSwarmID: "swarm-b"}
	if got := app.resolveSelectedChatRouteIDForRoutedIdentity("/source", identity); got != "route-b" {
		t.Fatalf("Router-selected route = %q, want route-b", got)
	}
}

func TestWorktreePrimerDispatchIsLocalAndPreservesWorktreesCommand(t *testing.T) {
	home := ui.NewHomePage(model.EmptyHome())
	app := &App{home: home, route: "home"}

	app.executeCommand("/worktree on")
	if !home.WorktreeRequested() || home.Status() != "Worktree: on" {
		t.Fatalf("home worktree primer = requested %v status %q", home.WorktreeRequested(), home.Status())
	}
	app.executeCommand("/wt off")
	if home.WorktreeRequested() || home.Status() != "Worktree: off" {
		t.Fatalf("home /wt off = requested %v status %q", home.WorktreeRequested(), home.Status())
	}

	app.executeCommand("/worktrees")
	if strings.Contains(strings.ToLower(home.Status()), "usage: /worktree on") || home.WorktreeRequested() {
		t.Fatalf("/worktrees was captured by local primer: %q", home.Status())
	}
}

func TestHomeBootstrapSelectsFirstRegisteredWorkspace(t *testing.T) {
	data := homeBootstrapData{
		current:    clientWorkspaceResolution("/work/current", "current"),
		hasCurrent: true,
		workspaces: []client.WorkspaceEntry{
			{Path: "/work/first", WorkspaceName: "first"},
			{Path: "/work/current", WorkspaceName: "current"},
		},
	}
	next, selected, _ := applyHomeWorkspaceBootstrap(model.EmptyHome(), data, "/work/current")
	if selected != "/work/first" {
		t.Fatalf("selected workspace = %q, want first registered", selected)
	}
	if len(next.Workspaces) != 2 || !next.Workspaces[0].Active || next.Workspaces[1].Active {
		t.Fatalf("workspace activation = %#v", next.Workspaces)
	}
}

func clientWorkspaceResolution(path, name string) client.WorkspaceResolution {
	return client.WorkspaceResolution{WorkspacePath: path, ResolvedPath: path, WorkspaceName: name}
}

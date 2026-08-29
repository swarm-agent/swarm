package app

import (
	"strings"
	"testing"

	"swarm-refactor/swarmtui/internal/client"
	"swarm-refactor/swarmtui/internal/model"
	"swarm-refactor/swarmtui/internal/ui"
	"swarm-refactor/swarmtui/internal/ui/v3chat"
)

func TestNewCommandDispatchOpensMandatoryRoutedPrimer(t *testing.T) {
	homeModel := routedPrimerHomeModel()
	home := ui.NewHomePage(homeModel)
	app := &App{api: testAPIWithToken("http://127.0.0.1"), home: home, homeModel: homeModel, workspacePath: testWorkspacePath, route: "home", config: defaultAppConfig()}

	app.executeCommand("/new plan")

	if app.route != "v3chat" || app.v3Chat == nil {
		t.Fatalf("/new plan route = %q page=%p, want local v3chat primer", app.route, app.v3Chat)
	}
	state := app.v3Chat.Runtime().Store().Snapshot()
	draft, ok := v3chat.SelectRoutedDraft(state)
	if !ok || draft.Prompt != "" || !draft.PlanModeRequested {
		t.Fatalf("/new plan draft = %#v, ok=%v", draft, ok)
	}
	if state.Session.ID != "" || state.Session.Title != "" || state.Session.WorkspaceName != "" {
		t.Fatalf("bare /new invented durable authority: %#v", state.Session)
	}
	if got := app.v3Chat.Status(); got != "Waiting..." {
		t.Fatalf("bare /new status = %q", got)
	}
}

func TestNewCommandDispatchKeepsBareAndPlanFormsRoutedAndLocal(t *testing.T) {
	for _, test := range []struct {
		command  string
		wantMode string
	}{
		{command: "/new", wantMode: "auto"},
		{command: "/new plan", wantMode: "plan"},
	} {
		t.Run(test.command, func(t *testing.T) {
			homeModel := routedPrimerHomeModel()
			home := ui.NewHomePage(homeModel)
			home.SetSessionMode("auto")
			app := &App{api: testAPIWithToken("http://127.0.0.1"), home: home, homeModel: homeModel, workspacePath: testWorkspacePath, route: "home", config: defaultAppConfig()}
			app.executeCommand(test.command)
			if app.route != "v3chat" || app.v3Chat == nil {
				t.Fatalf("route = %q page=%p", app.route, app.v3Chat)
			}
			state := app.v3Chat.Runtime().Store().Snapshot()
			draft, routed := v3chat.SelectRoutedDraft(state)
			if !routed || draft.PlanModeRequested != (test.wantMode == "plan") {
				t.Fatalf("%s routed draft = %#v", test.command, state.RoutedDraft)
			}
			if state.Session.ID != "" {
				t.Fatalf("%s created durable state = %#v", test.command, state.Session)
			}
		})
	}
}

func TestOpenNewV3ChatProjectsHomeFlagsIntoOneRoutedPrimer(t *testing.T) {
	homeModel := routedPrimerHomeModel()
	homeModel.ActiveAgent = "router-agent"
	home := ui.NewHomePage(homeModel)
	app := &App{api: testAPIWithToken("http://127.0.0.1"), home: home, homeModel: homeModel, workspacePath: testWorkspacePath, route: "home", config: defaultAppConfig()}
	intent := ui.HomeSessionIntent{InitialPrompt: "route this", Mode: "plan", Agent: "router-agent"}

	if err := app.openNewV3Chat(intent, model.ChatRoute{}, ""); err != nil {
		t.Fatal(err)
	}
	state := app.v3Chat.Runtime().Store().Snapshot()
	draft, ok := v3chat.SelectRoutedDraft(state)
	if !ok || draft.Prompt != "route this" || draft.AgentName != "router-agent" || !draft.PlanModeRequested {
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
		t.Fatalf("authority-matched route = %q, want route-b", got)
	}
}

func TestRetiredWorktreeToggleIsNotAdvertisedOrDispatched(t *testing.T) {
	home := ui.NewHomePage(model.EmptyHome())
	app := &App{home: home, route: "home"}

	app.executeCommand("/worktree " + "on")
	if !strings.Contains(home.Status(), "unknown command") {
		t.Fatalf("retired worktree toggle status = %q", home.Status())
	}
	app.executeCommand("/wt " + "off")
	if strings.Contains(strings.ToLower(home.Status()), "worktree: "+"off") {
		t.Fatalf("retired short worktree toggle remained active: %q", home.Status())
	}
}

func TestHomeBootstrapHonorsCurrentWorkspaceAfterInitialSelection(t *testing.T) {
	data := homeBootstrapData{
		current:    clientWorkspaceResolution("/work/current", "current"),
		hasCurrent: true,
		workspaces: []client.WorkspaceEntry{
			{Path: "/work/first", WorkspaceName: "first"},
			{Path: "/work/current", WorkspaceName: "current"},
		},
	}
	next, selected, _ := applyHomeWorkspaceBootstrap(model.EmptyHome(), data, "/work/current")
	if selected != "/work/current" {
		t.Fatalf("selected workspace = %q, want current workspace", selected)
	}
	if len(next.Workspaces) != 2 || next.Workspaces[0].Active || !next.Workspaces[1].Active {
		t.Fatalf("workspace activation = %#v", next.Workspaces)
	}
}

func routedPrimerHomeModel() model.HomeModel {
	workspace := model.Workspace{Name: "Workspace", Path: testWorkspacePath, Active: true, LocalWorkspaceBindingID: "binding-self"}
	return model.HomeModel{
		ActiveAgent: "swarm", Workspaces: []model.Workspace{workspace},
		CurrentSwarmTarget: &model.SwarmTarget{SwarmID: "swarm-self", Name: "Primary", Kind: "host", Relationship: "self"},
		ChatRoutes:         []model.ChatRoute{{ID: "swarm:swarm-self:binding:binding-self", SwarmID: "swarm-self", WorkspaceBindingID: "binding-self", HostWorkspacePath: testWorkspacePath, RuntimeWorkspacePath: testWorkspacePath, TargetKind: "host", TargetRelationship: "self"}},
	}
}

func clientWorkspaceResolution(path, name string) client.WorkspaceResolution {
	return client.WorkspaceResolution{WorkspacePath: path, ResolvedPath: path, WorkspaceName: name}
}

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/environments"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
	"swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

type browserConnectionFixture struct {
	manageConnectionStore
	connection environments.Connection
}

func (f *browserConnectionFixture) Get(string, string, string) (environments.Connection, bool, error) {
	return f.connection, true, nil
}

// Purpose: the browser origin boundary must never treat arbitrary runtime URLs
// as browser authority. Direct unit assertions prove malicious mappings cannot
// reach the probe, while preserving exact dynamically assigned host ports.
func TestTaskBrowserOrigin(t *testing.T) {
	for _, raw := range []string{"javascript:alert(1)", "file:///etc/passwd", "http://user:pass@127.0.0.1:49152", "http://127.0.0.1:49153", "http://192.0.2.1:49152", "http://localhost:49152", "http://127.0.0.1:49152?secret=x", "http://127.0.0.1:49152/#fragment", "http://127.0.0.1:49152//evil"} {
		if _, _, ok := taskBrowserOrigin([]environments.AssignedPort{{ContainerPort: 8080, HostPort: 49152, Protocol: "tcp", EndpointURL: raw}}, 8080, "http"); ok {
			t.Fatalf("unsafe mapping admitted: %s", raw)
		}
	}
	port := environments.AssignedPort{ContainerPort: 8080, HostPort: 49152, Protocol: "tcp", EndpointURL: "http://127.0.0.1:49152"}
	u, host, ok := taskBrowserOrigin([]environments.AssignedPort{port}, 8080, "https")
	if !ok || host != 49152 || u.String() != "https://127.0.0.1:49152" {
		t.Fatalf("dynamic mapping lost: %v %d %v", u, host, ok)
	}
	if _, _, ok := taskBrowserOrigin([]environments.AssignedPort{port, port}, 8080, "http"); ok {
		t.Fatal("ambiguous mapping admitted")
	}
	port.Protocol = "udp"
	if _, _, ok := taskBrowserOrigin([]environments.AssignedPort{port}, 8080, "http"); ok {
		t.Fatal("UDP mapping admitted")
	}
}

// Purpose: ManageTaskEnvironment browser access must be exact-current and
// probe-free on list/rejection. Real temporary task/catalog/Git persistence and
// loopback HTTP exercise the narrow integrated admission/probe/re-read boundary,
// including redirect SSRF, source/owner changes and concurrent revocation. No
// provider deployment is started; counters prove no lifecycle side effects.
func TestTaskBrowserEndpointsBoundary(t *testing.T) {
	f := setupMatrixTestFixture(t)
	defer f.db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repo := t.TempDir()
	git := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "git", append([]string{"-C", repo}, args...)...).CombinedOutput()
		if err != nil {
			t.Fatalf("git: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-b", "dev")
	git("config", "user.name", "Fixture")
	git("config", "user.email", "fixture@example.invalid")
	if err := os.WriteFile(filepath.Join(repo, "source"), []byte("candidate"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "source")
	git("commit", "-m", "candidate")
	catalog := pebblestore.NewWorkspaceStore(f.db)
	entry, err := catalog.AddForAccount(f.accountID, repo, "Product")
	if err != nil {
		t.Fatal(err)
	}
	f.server.workspace = workspace.NewService(catalog)
	f.server.worktrees = worktree.NewService(pebblestore.NewWorktreeStore(f.db), f.server.workspace, nil)
	binding := pebblestore.ProjectTaskSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Path: repo, Provenance: "explicit"}
	seedTaskSessionBinding(t, f, binding)
	db := f.server.sessions.Store()
	project := &pebblestore.ProjectRecord{ID: "browser-project", Name: "Browser", Workspaces: []pebblestore.ProjectWorkspaceRef{{WorkspaceID: entry.WorkspaceID, Path: repo}}}
	if err := db.PutProject(f.accountID, project); err != nil {
		t.Fatal(err)
	}
	task := &pebblestore.ProjectTaskRecord{ID: "browser-task", ProjectID: project.ID, Title: "Browser", Revision: 1, Agent: "swarm", SourceWorkspace: binding}
	if err := db.PutProjectTask(f.accountID, task); err != nil {
		t.Fatal(err)
	}
	product := environments.CommittedBuildSource{WorkspaceID: entry.WorkspaceID, WorkspaceGeneration: entry.WorkspaceGeneration, Commit: git("rev-parse", "HEAD")}
	definition := &environments.ImageBuildDefinition{Product: product, Recipe: product, RecipeDirectory: "recipe", RecipeFile: "recipe/Containerfile"}
	build := environments.ImageBuildResult{OperationID: "build", ConnectionID: "local", ImageID: "sha256:" + strings.Repeat("b", 64), Product: product, Recipe: product, DefinitionDigest: definition.Digest(), ContextDigest: "context"}
	manager := &taskAttachmentLifecycleFixture{dep: environments.Deployment{ID: "candidate", AccountScopeID: f.accountID, WorkspaceID: entry.WorkspaceID, EnvironmentID: "environment", ConnectionID: "local", Status: environments.DeploymentStatusReady, CreatedAt: time.Now().UnixMilli(), Runtime: environments.RuntimeMetadata{ContainerID: "runtime"}, Build: &build}}
	f.server.deployments = manager
	defs := &taskAttachmentDefinitionFixture{env: environments.Environment{ID: "environment", Name: "Web", AccountScopeID: f.accountID, WorkspaceID: entry.WorkspaceID, Build: definition, Container: environments.ContainerDefinition{ExposedPorts: []environments.PortMapping{{ContainerPort: 8080}}}, FrontendEndpoints: []environments.FrontendEndpoint{{ID: "web", Name: "Web", ContainerPort: 8080, Scheme: "http", Path: "/app", HealthPath: "/health"}}}}
	f.server.environments = defs
	connection := &browserConnectionFixture{connection: environments.Connection{ID: "local", AccountScopeID: f.accountID, WorkspaceID: entry.WorkspaceID, Kind: environments.ConnectionKindLocalPodman}}
	f.server.connections = connection
	var hits, escaped atomic.Int32
	var mode atomic.Int32
	var stopped atomic.Bool
	detachErrors := make(chan error, 1)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
	defer target.Close()
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/health" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			w.WriteHeader(400)
			return
		}
		switch mode.Load() {
		case 1:
			http.Redirect(w, r, target.URL, http.StatusFound)
		case 2:
			w.WriteHeader(503)
		case 3:
			stopped.Store(true)
		case 4:
			_, err := db.MutateProjectTaskEnvironment(f.accountID, project.ID, task.ID, pebblestore.TaskEnvironmentMutation{ExpectedTaskRevision: 2, ExpectedAttachmentRevision: 1, AttachmentID: "web"})
			detachErrors <- err
		default:
			w.WriteHeader(204)
		}
	}))
	defer httpServer.Close()
	u, _ := url.Parse(httpServer.URL)
	hostPort, _ := strconv.Atoi(u.Port())
	manager.dep.Runtime.AssignedPorts = []environments.AssignedPort{{ContainerPort: 8080, HostPort: hostPort, Protocol: "tcp", EndpointURL: httpServer.URL}}
	// The deployment reader turns the observed stop into current persisted state
	// only on re-read, avoiding races with the HTTP server goroutine.
	f.server.deployments = &browserDeploymentFixture{taskAttachmentLifecycleFixture: manager, stopped: &stopped}
	p := identity.Principal{Type: "user", UserID: f.userID, AccountScopeID: f.accountID}
	attach := tool.TaskEnvironmentRequest{Action: "attach_task", ProjectID: project.ID, TaskID: task.ID, AttachmentID: "web", WorkspaceID: entry.WorkspaceID, DeploymentID: "candidate", ExpectedTaskRevision: 1, ExpiresAt: time.Now().Add(time.Hour).UnixMilli()}
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", attach); err != nil {
		t.Fatal(err)
	}
	req := tool.TaskEnvironmentRequest{Action: "browser_endpoints", ProjectID: project.ID, TaskID: task.ID, AttachmentID: "web", WorkspaceID: entry.WorkspaceID, ExpectedAttachmentRevision: 1}
	list := req
	list.Action = "list_attachments"
	if _, err := f.server.ManageTaskEnvironment(ctx, p, "", list); err != nil || hits.Load() != 0 {
		t.Fatal("listing probed HTTP")
	}
	result, err := f.server.ManageTaskEnvironment(ctx, p, "", req)
	if err != nil || len(result.BrowserEndpoints) != 1 || !result.BrowserEndpoints[0].Ready || result.BrowserEndpoints[0].URL != httpServer.URL+"/app" || result.BrowserEndpoints[0].HostPort != hostPort {
		t.Fatalf("dynamic browser endpoint: %+v %v", result, err)
	}
	for _, failureMode := range []int32{1, 2} {
		mode.Store(failureMode)
		result, err = f.server.ManageTaskEnvironment(ctx, p, "", req)
		if err != nil || result.BrowserEndpoints[0].Ready || result.BrowserEndpoints[0].URL != "" || result.BrowserEndpoints[0].Error == "" || escaped.Load() != 0 {
			t.Fatalf("redirect/failure escaped: %+v %v", result, err)
		}
	}
	mode.Store(0)
	// Default TLS trust must reject a self-signed service rather than weakening
	// verification to make local HTTPS appear ready.
	tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer tlsServer.Close()
	tlsURL, _ := url.Parse(tlsServer.URL)
	tlsPort, _ := strconv.Atoi(tlsURL.Port())
	originalPorts := manager.dep.Runtime.AssignedPorts
	manager.dep.Runtime.AssignedPorts = []environments.AssignedPort{{ContainerPort: 8080, HostPort: tlsPort, Protocol: "tcp", EndpointURL: tlsServer.URL}}
	defs.env.FrontendEndpoints[0].Scheme = "https"
	result, err = f.server.ManageTaskEnvironment(ctx, p, "", req)
	if err != nil || result.BrowserEndpoints[0].Ready || result.BrowserEndpoints[0].URL != "" {
		t.Fatal("untrusted HTTPS service admitted")
	}
	manager.dep.Runtime.AssignedPorts = originalPorts
	defs.env.FrontendEndpoints[0].Scheme = "http"
	endpoints := defs.env.FrontendEndpoints
	defs.env.FrontendEndpoints = nil
	beforeHits := hits.Load()
	result, err = f.server.ManageTaskEnvironment(ctx, p, "", req)
	if err != nil || result.BrowserEndpoints == nil || len(result.BrowserEndpoints) != 0 || hits.Load() != beforeHits {
		t.Fatal("backend-only definition inferred HTTP")
	}
	defs.env.FrontendEndpoints = endpoints
	for _, change := range []func(){
		func() { req.ExpectedAttachmentRevision = 2 },
		func() { p.AccountScopeID = "foreign" },
		func() { manager.dep.Status = environments.DeploymentStatusStopped },
		func() { manager.dep.Runtime.ContainerID = "replacement" },
		func() { connection.connection.Kind = environments.ConnectionKindSSH },
		func() { definition.Product.Commit = strings.Repeat("c", 40) },
		func() { manager.dep.ReviewDeadline = time.Now().Add(-time.Second).UnixMilli() },
		func() { connection.connection.AccountScopeID = "foreign" },
	} {
		originalReq, originalP, originalDep, originalConnection, originalDefinition := req, p, *manager.dep.Clone(), connection.connection, *definition
		change()
		beforeHits = hits.Load()
		if result, err := f.server.ManageTaskEnvironment(ctx, p, "", req); err == nil || len(result.BrowserEndpoints) != 0 || hits.Load() != beforeHits {
			t.Fatal("stale/foreign/nonlocal request probed or returned URL")
		}
		req, p, manager.dep, connection.connection, *definition = originalReq, originalP, originalDep, originalConnection, originalDefinition
	}
	mode.Store(3)
	if result, err := f.server.ManageTaskEnvironment(ctx, p, "", req); err == nil || len(result.BrowserEndpoints) != 0 {
		t.Fatal("concurrent stop returned browser access")
	}
	stored, _, err := db.GetProjectTask(f.accountID, project.ID, task.ID)
	if err != nil || stored.Revision != 2 || manager.effects != 0 || len(manager.leases) != 0 {
		t.Fatal("browser inspection mutated lifecycle/task state")
	}
	stopped.Store(false)
	mode.Store(4)
	if result, err := f.server.ManageTaskEnvironment(ctx, p, "", req); err == nil || len(result.BrowserEndpoints) != 0 {
		t.Fatal("concurrent detach returned browser access")
	}
	select {
	case err := <-detachErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("detach probe did not execute")
	}
	stored, _, err = db.GetProjectTask(f.accountID, project.ID, task.ID)
	if err != nil || stored.Revision != 3 || len(stored.EnvironmentAttachments) != 0 || manager.effects != 0 {
		t.Fatal("detach was undone or browser mutated lifecycle")
	}
}

type browserDeploymentFixture struct {
	*taskAttachmentLifecycleFixture
	stopped *atomic.Bool
}

func (f *browserDeploymentFixture) GetDeployment(a, w, id string) (environments.Deployment, bool, error) {
	d, found, err := f.taskAttachmentLifecycleFixture.GetDeployment(a, w, id)
	if f.stopped.Load() {
		d.Status = environments.DeploymentStatusStopped
	}
	return d, found, err
}

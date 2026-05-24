package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/identity"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	swarmruntime "swarm/packages/swarmd/internal/swarm"
	topologyruntime "swarm/packages/swarmd/internal/topology"
	"swarm/packages/swarmd/internal/workspace"
)

func TestInspectGitSyncRepoRequiresCleanNamedBranch(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}

	result, err := inspectGitSyncRepo(context.Background(), repo, true)
	if err == nil {
		t.Fatal("inspectGitSyncRepo error = nil, want dirty repo error")
	}
	if result.Clean {
		t.Fatalf("dirty repo Clean = true")
	}
	if len(result.StatusShort) == 0 {
		t.Fatalf("dirty repo StatusShort is empty")
	}

	result, err = inspectGitSyncRepo(context.Background(), repo, false)
	if err != nil {
		t.Fatalf("inspectGitSyncRepo(requireClean=false) error = %v", err)
	}
	if result.Branch == "" || result.Head == "" || result.Tree == "" || result.RepoRoot == "" {
		t.Fatalf("inspectGitSyncRepo incomplete result: %+v", result)
	}
}

func TestApplyGitSyncRequiresDestructiveConfirmation(t *testing.T) {
	repo := initGitCommitTestRepo(t)
	state, err := inspectGitSyncRepo(context.Background(), repo, true)
	if err != nil {
		t.Fatalf("inspectGitSyncRepo error = %v", err)
	}

	_, err = applyGitSync(context.Background(), gitSyncApplyRequest{
		TargetPath: repo,
		Branch:     state.Branch,
		CommitSHA:  state.Head,
		TreeSHA:    state.Tree,
	})
	if err == nil {
		t.Fatal("applyGitSync error = nil, want destructive confirmation error")
	}
	if !strings.Contains(err.Error(), "destructive=true") {
		t.Fatalf("applyGitSync error = %q, want destructive=true", err.Error())
	}
}

func TestApplyGitSyncFetchesResetsCleansAndVerifies(t *testing.T) {
	source := initGitCommitTestRepo(t)
	branch := strings.TrimSpace(runGitCommitTestCommand(t, source, "branch", "--show-current"))

	target := filepath.Join(t.TempDir(), "target")
	runGitCommitTestCommand(t, t.TempDir(), "clone", source, target)

	if err := os.WriteFile(filepath.Join(source, "note.txt"), []byte("synced\n"), 0o644); err != nil {
		t.Fatalf("write source change: %v", err)
	}
	runGitCommitTestCommand(t, source, "add", "note.txt")
	runGitCommitTestCommand(t, source, "commit", "-m", "feat: sync target")
	sourceState, err := inspectGitSyncRepo(context.Background(), source, true)
	if err != nil {
		t.Fatalf("inspect source error = %v", err)
	}

	if err := os.WriteFile(filepath.Join(target, "note.txt"), []byte("target dirty\n"), 0o644); err != nil {
		t.Fatalf("write target dirty change: %v", err)
	}
	if err := os.WriteFile(filepath.Join(target, "untracked.txt"), []byte("delete me\n"), 0o644); err != nil {
		t.Fatalf("write target untracked: %v", err)
	}

	result, err := applyGitSync(context.Background(), gitSyncApplyRequest{
		TargetPath:  target,
		SourceRepo:  source,
		Branch:      branch,
		CommitSHA:   sourceState.Head,
		TreeSHA:     sourceState.Tree,
		Destructive: true,
	})
	if err != nil {
		t.Fatalf("applyGitSync error = %v result=%+v", err, result)
	}
	if !result.OK {
		t.Fatalf("applyGitSync OK = false result=%+v", result)
	}
	if result.After.Head != sourceState.Head || result.After.Tree != sourceState.Tree || result.After.Branch != branch || !result.After.Clean {
		t.Fatalf("after = %+v, want source head/tree branch clean", result.After)
	}
	if _, err := os.Stat(filepath.Join(target, "untracked.txt")); !os.IsNotExist(err) {
		t.Fatalf("untracked target file still exists or stat error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(target, "note.txt"))
	if err != nil {
		t.Fatalf("read target note: %v", err)
	}
	if string(content) != "synced\n" {
		t.Fatalf("target note = %q, want synced", string(content))
	}
}

func TestManagedHostGitSyncApplyRejectsMissingDestructiveBeforePeer(t *testing.T) {
	server := newManagedGitSyncTestServer(t)
	source := initGitCommitTestRepo(t)
	state, err := inspectGitSyncRepo(context.Background(), source, true)
	if err != nil {
		t.Fatalf("inspect source: %v", err)
	}

	var peerHits int
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		peerHits++
		http.NotFound(w, r)
	}))
	t.Cleanup(peer.Close)
	seedManagedGitSyncTopologyBinding(t, server, source, peer.URL)

	body, _ := json.Marshal(managedHostGitSyncApplyRequest{TargetSwarmID: "managed-swarm", SourceWorkspacePath: source, Branch: state.Branch, CommitSHA: state.Head, TreeSHA: state.Tree})
	req := httptest.NewRequest(http.MethodPost, managedHostGitSyncApplyPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(req))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if peerHits != 0 {
		t.Fatalf("peer hits=%d, want 0 before destructive confirmation", peerHits)
	}
	if !strings.Contains(rec.Body.String(), "destructive=true") {
		t.Fatalf("body=%s, want destructive warning", rec.Body.String())
	}
}

func TestManagedHostGitSyncApplyRoutesThroughTopologyWorkspaceBinding(t *testing.T) {
	server := newManagedGitSyncTestServer(t)
	source := initGitCommitTestRepo(t)
	state, err := inspectGitSyncRepo(context.Background(), source, true)
	if err != nil {
		t.Fatalf("inspect source: %v", err)
	}

	var peerReq gitSyncApplyRequest
	var peerHits int
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/readyz" || r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.URL.Path != peerGitSyncApplyPath {
			t.Fatalf("peer path=%q want %q", r.URL.Path, peerGitSyncApplyPath)
		}
		if r.Header.Get(peerAuthSwarmIDHeader) != "host-swarm-id" || r.Header.Get(peerAuthTokenHeader) != "peer-token" {
			t.Fatalf("peer auth=%q/%q", r.Header.Get(peerAuthSwarmIDHeader), r.Header.Get(peerAuthTokenHeader))
		}
		peerHits++
		if err := json.NewDecoder(r.Body).Decode(&peerReq); err != nil {
			t.Fatalf("decode peer request: %v", err)
		}
		writeJSON(w, http.StatusOK, gitSyncApplyResponse{OK: true, After: gitSyncInspectResponse{Branch: peerReq.Branch, Head: peerReq.CommitSHA, Tree: peerReq.TreeSHA, Clean: true}})
	}))
	t.Cleanup(peer.Close)
	seedManagedGitSyncTopologyBinding(t, server, source, peer.URL)

	body, _ := json.Marshal(managedHostGitSyncApplyRequest{TargetSwarmID: "managed-swarm", SourceWorkspacePath: source, Branch: state.Branch, CommitSHA: state.Head, TreeSHA: state.Tree, Destructive: true})
	req := httptest.NewRequest(http.MethodPost, managedHostGitSyncApplyPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.Handler().ServeHTTP(rec, requestWithTestPrincipal(req))

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if peerHits != 1 {
		t.Fatalf("peer hits=%d want 1", peerHits)
	}
	if peerReq.TargetPath != "/managed/swarm-go" || peerReq.Branch != state.Branch || peerReq.CommitSHA != state.Head || peerReq.TreeSHA != state.Tree || !peerReq.Destructive || len(peerReq.GitBundle) == 0 {
		t.Fatalf("peer request=%+v bundle_bytes=%d", peerReq, len(peerReq.GitBundle))
	}
	var response managedHostGitSyncApplyResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || len(response.Targets) != 1 || !response.Targets[0].OK || response.Targets[0].Binding.BindingID == "" {
		t.Fatalf("response=%+v", response)
	}
}

func TestApplyGitSyncImportsBundleWhenCommitIsMissing(t *testing.T) {
	source := initGitCommitTestRepo(t)
	branch := strings.TrimSpace(runGitCommitTestCommand(t, source, "branch", "--show-current"))
	target := filepath.Join(t.TempDir(), "target")
	runGitCommitTestCommand(t, t.TempDir(), "clone", source, target)

	if err := os.WriteFile(filepath.Join(source, "bundled.txt"), []byte("from bundle\n"), 0o644); err != nil {
		t.Fatalf("write source change: %v", err)
	}
	runGitCommitTestCommand(t, source, "add", "bundled.txt")
	runGitCommitTestCommand(t, source, "commit", "-m", "feat: bundled sync")
	sourceState, err := inspectGitSyncRepo(context.Background(), source, true)
	if err != nil {
		t.Fatalf("inspect source: %v", err)
	}
	if _, err := runGitSyncCommand(context.Background(), target, "cat-file", "-e", sourceState.Head+"^{commit}"); err == nil {
		t.Fatalf("target unexpectedly already has source commit")
	}

	bundlePath, err := createGitBundle(context.Background(), source)
	if err != nil {
		t.Fatalf("create bundle: %v", err)
	}
	defer os.Remove(bundlePath)
	bundle, err := os.ReadFile(bundlePath)
	if err != nil {
		t.Fatalf("read bundle: %v", err)
	}

	result, err := applyGitSync(context.Background(), gitSyncApplyRequest{
		TargetPath:  target,
		GitBundle:   bundle,
		Branch:      branch,
		CommitSHA:   sourceState.Head,
		TreeSHA:     sourceState.Tree,
		Destructive: true,
	})
	if err != nil {
		t.Fatalf("applyGitSync error = %v result=%+v", err, result)
	}
	if result.After.Head != sourceState.Head || result.After.Tree != sourceState.Tree || !result.After.Clean {
		t.Fatalf("after=%+v want source head/tree clean", result.After)
	}
	content, err := os.ReadFile(filepath.Join(target, "bundled.txt"))
	if err != nil {
		t.Fatalf("read bundled file: %v", err)
	}
	if string(content) != "from bundle\n" {
		t.Fatalf("bundled file=%q", string(content))
	}
}

func newManagedGitSyncTestServer(t *testing.T) *Server {
	t.Helper()
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "managed-git-sync.pebble"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	server := NewServer("test", nil, nil, nil, nil, nil, workspace.NewService(pebblestore.NewWorkspaceStore(store)), nil, nil, nil, nil, nil, nil, nil)
	server.SetTopologyService(topologyruntime.NewService(pebblestore.NewTopologyStore(store), nil, nil, nil, nil, nil, nil, nil))
	server.SetSwarmNodeStore(pebblestore.NewSwarmNodeStore(store))
	server.SetSwarmStore(pebblestore.NewSwarmStore(store))
	server.SetSwarmService(fakeRoutedSwarmService{state: swarmruntime.LocalState{Node: swarmruntime.LocalNodeState{SwarmID: "host-swarm-id", Name: "host-swarm", Role: "master"}}, token: "peer-token"})
	startupPath := filepath.Join(t.TempDir(), "swarm.conf")
	cfg := startupconfig.Default(startupPath)
	cfg.SwarmMode = true
	cfg.SwarmName = "host-swarm"
	if err := startupconfig.Write(cfg); err != nil {
		t.Fatalf("write startup config: %v", err)
	}
	server.SetStartupConfigPath(startupPath)
	return server
}

func seedManagedGitSyncTopologyBinding(t *testing.T, server *Server, sourceRepo, backendURL string) {
	t.Helper()
	if _, err := server.workspace.AddForPrincipal(identity.Principal{Type: identity.PrincipalTypeUser, UserID: testUserID, AccountScopeID: testAccountScopeID, AccountScopeSource: identity.AccountScopeSourceServerState}, sourceRepo, "swarm-go", "", false); err != nil {
		t.Fatalf("add source workspace: %v", err)
	}
	if _, err := server.swarmNodes.Put(pebblestore.SwarmNodeRecord{SwarmID: "managed-swarm", Name: "Managed Host", Role: "managed", Kind: "manual", BackendURL: backendURL, Status: "online"}); err != nil {
		t.Fatalf("put managed node: %v", err)
	}
	server.swarmTargetHealth.entries = map[string]swarmTargetHealthEntry{
		"host|managed-swarm|" + backendURL:   {online: true, checkedAt: time.Now()},
		"manual|managed-swarm|" + backendURL: {online: true, checkedAt: time.Now()},
	}
	if _, err := server.topology.UpsertWorkspaceBinding(pebblestore.TopologyWorkspaceBindingRecord{
		BindingID:                 pebblestore.CanonicalTopologyWorkspaceBindingID("managed-swarm", sourceRepo),
		UserID:                    testUserID,
		AccountScopeID:            testAccountScopeID,
		SourceWorkspacePath:       sourceRepo,
		SourceWorkspaceName:       "swarm-go",
		DestinationRuntimeSwarmID: "managed-swarm",
		DestinationHostSwarmID:    "managed-swarm",
		DestinationWorkspacePath:  "/managed/swarm-go",
		ReplicationMode:           "bundle",
		Writable:                  true,
	}); err != nil {
		t.Fatalf("upsert topology binding: %v", err)
	}
}

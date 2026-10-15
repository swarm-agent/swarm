package api

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/model"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	"swarm/packages/swarmd/internal/provider/registry"
	runruntime "swarm/packages/swarmd/internal/run"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
	workspaceruntime "swarm/packages/swarmd/internal/workspace"
	"swarm/packages/swarmd/internal/worktree"
)

// Requirement: reviewer dispatch must reach the real V3 executor/provider loop
// with an owner-registered detached exact head, then persist per-head evidence
// and cleanup. Threat: branch/rebase gates strand every review, or admit a moved
// checkout. This hermetic integration uses real Git/Pebble and the production
// executor, with fake provider/engine only; it does not prove Docker isolation.
func TestReviewerRealRunStart(t *testing.T) {
	for _, name := range []string{"exact_head", "moved_head", "missing_pointer", "missing_checkout"} {
		moved := name == "moved_head"
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			s, db, _ := setupWorkerAPITestServer(t)
			workspace := setupWorkerAPIExecution(t, s, db)
			execution, err := s.workerExecutionService()
			if err != nil {
				t.Fatal(err)
			}
			ws := s.sessions.Store().WorkerStore()
			create := func(role string) store.WorkerRecord {
				t.Helper()
				w, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: role, Role: role, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
				if err != nil {
					t.Fatal(err)
				}
				return activateWorkerAPIFixture(t, s, w, workspace)
			}
			dispatch := func(w store.WorkerRecord, input map[string]any) store.WorkerRunRecord {
				t.Helper()
				r, err := execution.Dispatch(ctx, "acct-test", "user-test", store.WorkerRunAdmission{WorkerID: w.ID, WorkspaceID: workspace, RequestSource: "direct", Input: input, IdempotencyKey: w.ID})
				if err != nil {
					t.Fatal(err)
				}
				return r
			}
			git := func(path string, args ...string) string {
				t.Helper()
				out, err := exec.CommandContext(ctx, "git", append([]string{"-C", path}, args...)...).CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v %s", args, err, out)
				}
				return strings.TrimSpace(string(out))
			}
			coder := create(store.WorkerRoleCoder)
			sourceJob := dispatch(coder, map[string]any{"prompt": "implement"})
			source, _, _ := s.sessions.GetSession(sourceJob.SessionID)
			git(source.WorktreeRootPath, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid", "commit", "--allow-empty", "-m", "implementation")
			head := git(source.WorktreeRootPath, "rev-parse", "HEAD")
			scratch := t.TempDir()
			engine := filepath.Join(scratch, "engine")
			// Only the engine protocol is stubbed; routed Git commands execute normally.
			script := `#!/bin/sh
case "$1" in
version) echo fixture;;
image) echo sha256:fixture;;
info) echo '{}';;
network) echo fixture;;
container) exit 1;;
run|rm|start|volume) exit 0;;
exec)
 shift
 while [ "$#" -gt 0 ]; do
 case "$1" in
 -w) cd "$2" || exit 1; shift 2;;
 -e) export "$2"; shift 2;;
 -i) shift;;
 *) shift; break;;
 esac
 done
 exec "$@";;
*) exit 1;;
esac
`
			if err := os.WriteFile(engine, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			manager, err := sandbox.NewManager(ctx, sandbox.Config{Mode: sandbox.ModeRequired, Engine: engine, UID: 10001, GID: 10001, StateDir: scratch}, nil)
			if err != nil {
				t.Fatal(err)
			}
			bucket := filepath.Dir(source.WorktreeRootPath)
			manager.SetLayout(sandbox.Layout{Projects: func() ([]string, error) { return []string{source.WorkspacePath}, nil }, WorktreesRoot: filepath.Dir(bucket), Bucket: func(string) (string, error) { return filepath.Base(bucket), nil }})
			sandbox.SetDefault(manager)
			defer sandbox.SetDefault(nil)
			// Preserve production canonicalization's agent snapshot, which the
			// shared admission-only fixture intentionally omits.
			s.runner.(*runruntime.Service).SetSessionDeployCanonicalizer(func(in runruntime.SessionDeployCanonicalizeInput) (runruntime.SessionDeployCanonicalization, error) {
				return runruntime.SessionDeployCanonicalization{SourceWorkspaceID: workspace, SourceWorkspaceGeneration: 1, SourceWorkspacePath: source.WorkspacePath, SourceWorkspaceName: "fixture", Metadata: map[string]any{"agent_profile": in.AgentProfile}}, nil
			})
			reviewer := create(store.WorkerRoleReviewer)
			r := dispatch(reviewer, map[string]any{"prompt": "review", "review_job": "worker/" + coder.ID + "/" + sourceJob.ID})
			review, _, _ := s.sessions.GetSession(r.SessionID)
			s.workspace = workspaceruntime.NewService(store.NewWorkspaceStore(db))
			s.worktrees = worktree.NewService(store.NewWorktreeStore(db), s.workspace, nil)
			principal := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acct-test", UserID: "user-test"}
			item := sessionRepositoryItem{SessionID: r.SessionID, WorkspaceID: workspace, WorkspaceGeneration: 1, SourcePath: source.WorkspacePath, WorkspacePath: review.WorktreeRootPath, Branch: "HEAD", BaseCommit: head}
			if err := s.authorizeRepositoryItem(principal, item); err != nil {
				t.Fatalf("review status/watch authorization: %v", err)
			}
			if _, err := s.selectedSessionRepository(principal, r.SessionID, review.WorktreeRootPath); err != nil {
				t.Fatalf("review status/watch selector: %v", err)
			}
			scope, err := s.runner.(*runruntime.Service).ResolveRuntimeWorkspaceScope(review, principal)
			if err != nil || scope.ReviewCheckout != review.WorktreeRootPath || !scope.RejectScopeExpansion || len(scope.Roots) != 1 {
				t.Fatalf("review lost restrictive scope: %+v %v", scope, err)
			}
			unregistered := review
			unregistered.ID = "unregistered-review"
			unregistered.Metadata = map[string]any{}
			for key, value := range review.Metadata {
				unregistered.Metadata[key] = value
			}
			unregistered.Metadata["swarm_v3_worktree_owner_session_id"] = unregistered.ID
			if _, err := s.runner.(*runruntime.Service).ResolveRuntimeWorkspaceScope(unregistered, principal); err == nil {
				t.Fatal("unregistered reviewer accepted")
			}
			if err := worktree.ValidateOwnedExecutionIdentity(source.WorkspacePath, review.WorktreeRootPath, "HEAD", head); err == nil {
				t.Fatal("writable lane accepted detached review")
			}
			if moved {
				git(review.WorktreeRootPath, "checkout", "--detach", source.Metadata["base_commit"].(string))
				if err := s.authorizeRepositoryItem(principal, item); err == nil {
					t.Fatal("status/watch admitted moved review head")
				}
			}
			// Stop must complete after interrupted filesystem cleanup, using
			// the production cancellation and durable sandbox cleanup boundary.
			if name == "missing_pointer" || name == "missing_checkout" {
				if name == "missing_pointer" {
					err = os.Remove(filepath.Join(review.WorktreeRootPath, ".git"))
				} else {
					err = os.RemoveAll(review.WorktreeRootPath)
				}
				if err != nil {
					t.Fatal(err)
				}
				currentWorker, _, err := ws.GetWorker("acct-test", reviewer.ID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := execution.Stop(ctx, "acct-test", "user-test", reviewer.ID, currentWorker.Revision, store.WorkerLifecycleStatePaused); err != nil {
					t.Fatal(err)
				}
				final, _, err := ws.GetWorkerRun("acct-test", reviewer.ID, r.ID)
				if err != nil || final.Status != "cancelled" {
					t.Fatalf("stop failed: %+v %v", final, err)
				}
				cleanup, found, err := s.sessions.Store().GetReviewCleanup("acct-test", r.SessionID)
				if err != nil || !found || cleanup.Phase != "volume_removed" {
					t.Fatal(cleanup, err)
				}
				if strings.Contains(git(source.WorkspacePath, "worktree", "list", "--porcelain"), review.WorktreeRootPath) {
					t.Fatal("completed cleanup retained Git registration")
				}
				return
			}
			fake := &reviewRunProvider{}
			s.agents = agent.NewService(store.NewAgentStore(db), nil)
			s.providers = registry.New()
			s.providers.RegisterRunner(fake)
			// The fixture's account-selected provider is codex; only its transport is fake.
			s.perm = permission.NewService(store.NewPermissionStore(db), nil, nil)
			s.model = model.NewService(store.NewModelStore(db), nil, model.NewCatalogService(store.NewModelCatalogStore(db)))
			executor := &sessionV3Executor{server: s, ctx: ctx, refillPumpRunning: true, inFlightRuns: map[string]bool{}, activeBySession: map[string]string{}, runStates: map[string]*sessionV3ExecutorRunState{}, recoveryConfirm: map[string]int64{}}
			executor.run(ctx, sessionV3ExecutorJob{SessionID: r.SessionID, RunID: r.ID, Principal: identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acct-test", UserID: "user-test"}, enqueuedAt: time.Now()})
			intent, found, err := s.sessions.GetSessionRunIntent(r.SessionID, r.ID)
			if err != nil || !found {
				t.Fatal(intent, err)
			}
			if moved {
				if intent.Status != "failed" || fake.calls != 0 {
					t.Fatalf("moved head admitted: %+v calls=%d", intent, fake.calls)
				}
				if !strings.Contains(intent.BlockedReason, "review head changed") {
					t.Fatalf("wrong rejection: %+v", intent)
				}
				// Restore only to let the production terminal cleanup safely remove it.
				git(review.WorktreeRootPath, "checkout", "--detach", head)
			} else if intent.Status != "completed" || fake.calls != 1 {
				t.Fatalf("run failed: %+v calls=%d", intent, fake.calls)
			}
			if err := execution.ReconcileWorker(ctx, "acct-test", reviewer.ID); err != nil {
				t.Fatal(err)
			}
			evidence, err := ws.GetJobReview("acct-test", coder.ID, sourceJob.ID)
			want := "ok"
			if moved {
				want = "block"
			}
			if err != nil || evidence == nil || evidence.Verdict != want || evidence.Head != head {
				t.Fatal(evidence, err)
			}
			final, _, err := ws.GetWorkerRun("acct-test", reviewer.ID, r.ID)
			wantStatus := "succeeded"
			if moved {
				wantStatus = "failed"
			}
			if err != nil || final.Status != wantStatus || len(final.Deliverables) != 0 {
				t.Fatal(final, err)
			}
			cleanup, found, err := s.sessions.Store().GetReviewCleanup("acct-test", r.SessionID)
			if err != nil || !found || cleanup.Phase != "volume_removed" {
				t.Fatal(cleanup, err)
			}
			if _, err := os.Stat(review.WorktreeRootPath); !os.IsNotExist(err) {
				t.Fatalf("checkout retained: %v", err)
			}
		})
	}
}

type reviewRunProvider struct{ calls int }

func (p *reviewRunProvider) ID() string { return "codex" }
func (p *reviewRunProvider) CreateResponse(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
	return p.CreateResponseStreaming(ctx, req, nil)
}
func (p *reviewRunProvider) CreateResponseStreaming(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
	p.calls++
	return provideriface.Response{StopReason: "end_turn", Text: "VERDICT: OK\nSUMMARY: correct\nFINDINGS: None\nTESTS RUN: fixture"}, nil
}

func (p *reviewRunProvider) ExecutionEpochLifecycle() provideriface.ExecutionEpochLifecycleCapabilities {
	return provideriface.ExecutionEpochLifecycleCapabilities{ContextMode: provideriface.ExecutionEpochContextResponsesChain, TransportReusable: true}
}

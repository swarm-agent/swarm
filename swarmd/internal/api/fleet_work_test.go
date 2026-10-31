package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	runruntime "swarm/packages/swarmd/internal/run"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/worktree"
	"testing"
	"time"
)

// Purpose: CallFleetWork must read the linked daemon, not home state, and
// reject URL traversal and control-plane operations before transport. Two
// in-process daemons exercise the actual pinned link and workflow HTTP boundary.
func TestFleetWorkReads(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	repository := jobautomation.NewStore(remote.s.sessions.Store().Underlying())
	remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: repository}))
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "build-box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "list", nil)
	if err != nil || !strings.Contains(string(raw), `"automations":[]`) {
		t.Fatalf("list: %s %v", raw, err)
	}
	// A workflow is named by id or name; one the machine's list does not
	// have is refused (resolveWorkflowReference against that list).
	if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "get", map[string]any{"id": "missing"}); err == nil || !strings.Contains(err.Error(), `no workflow has the id or name "missing"`) {
		t.Fatalf("remote missing get: %v", err)
	}
	for _, action := range []string{"secrets", "permissions", "rotate_key", "accept"} {
		if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", action, nil); err == nil {
			t.Fatalf("allowed %s", action)
		}
	}
	if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "get", map[string]any{"id": "../permissions"}); err == nil {
		t.Fatal("traversal allowed")
	}
	a, _, err := repository.Put(remote.account, jobautomation.Automation{Name: "Review", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review exact changes"}}})
	if err != nil {
		t.Fatal(err)
	}
	full, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "get", map[string]any{"id": a.ID})
	if err != nil || !strings.Contains(string(full), "Review exact changes") {
		t.Fatalf("get: %s %v", full, err)
	}
	if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "disable", map[string]any{"id": a.ID}); err != nil {
		t.Fatal(err)
	}
	updated, err := repository.Get(remote.account, a.ID)
	if err != nil || updated.Enabled {
		t.Fatalf("disable not stored: %+v %v", updated, err)
	}
	other := p
	other.AccountScopeID = "other-account"
	if _, err := home.s.CallFleetWork(ctx, other, "build-box", "manage_workflows", "list", nil); err == nil {
		t.Fatal("foreign link allowed")
	}
}

// Purpose: compactFleetList is the response-size boundary for linked lists.
// Large definitions and step outcomes must not reach the model; get is separate.
// A direct projection test isolates the shape and size invariant from transport.
func TestFleetWorkCompact(t *testing.T) {
	rows := []any{}
	for i := 0; i < 12; i++ {
		rows = append(rows, map[string]any{"id": "workflow", "name": "Review", "enabled": true, "steps": []any{map[string]any{"brief": strings.Repeat("x", 140000)}}, "latest_deliveries": []any{map[string]any{"state": "succeeded", "updated_at": 123, "outcomes": strings.Repeat("x", 140000)}}})
	}
	raw, _ := json.Marshal(map[string]any{"automations": rows})
	compact, err := compactFleetList(raw)
	if err != nil || len(compact) > 6000 || strings.Contains(string(compact), "brief") || strings.Contains(string(compact), "outcomes") {
		t.Fatalf("compact: bytes=%d err=%v", len(compact), err)
	}
	if !strings.Contains(string(compact), `"step_count":1`) || !strings.Contains(string(compact), `"state":"succeeded"`) {
		t.Fatalf("missing summary: %s", compact)
	}
}

// Purpose: AuthorizeToolCall and CallFleetWork must gate workflow disable and
// worker pause before the pinned HTTP link changes durable remote state. Real
// temporary stores prove off leaves records unchanged, and ask, auto or the
// retired allowed (stored as auto) change only the remote record without asking:
// ask waits only before tasks are sent, never for workflow or worker changes.
func TestFleetWorkMutationRules(t *testing.T) {
	for _, kind := range []string{"manage_workflows", "manage_workers"} {
		for _, level := range []string{"off", "ask", "auto", "allowed"} {
			t.Run(kind+"/"+level, func(t *testing.T) {
				home := newFleetTestSwarm(t, "home")
				defer home.cleanup()
				remote := newFleetTestSwarm(t, "remote")
				defer remote.cleanup()
				repo := jobautomation.NewStore(remote.s.sessions.Store().Underlying())
				remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: repo}))
				runner := runruntime.NewService(remote.s.sessions, nil, nil, nil, nil, nil, nil, nil)
				execution, err := runruntime.NewWorkerExecutionService(runner, remote.s.sessions.Store(), &worktree.Service{}, remote.s.applySessionV3PrimaryMutation, func(identity.Principal, pebblestore.V3SessionRunIntent) bool {
					t.Fatal("unexpected execution")
					return false
				})
				if err != nil {
					t.Fatal(err)
				}
				runner.SetWorkerExecutionService(execution)
				remote.s.runner = runner
				body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "build-box"})
				if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
					t.Fatalf("link %d %v", status, out)
				}
				p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
				account, err := home.s.sessions.Store().GetAccountPermissions(home.account)
				if err != nil {
					t.Fatal(err)
				}
				account.Linked["build-box"] = pebblestore.LinkedPermissionRule{Work: level}
				if _, err := home.s.sessions.Store().Underlying().SetAccountPermissions(home.account, account.Revision, account); err != nil {
					t.Fatal(err)
				}
				session := pebblestore.SessionSnapshot{ID: "permission-session", AccountScopeID: home.account, UserID: home.user}
				if err := home.s.sessions.Store().CreateSessionForAccount(session, home.user, home.account); err != nil {
					t.Fatal(err)
				}
				perms := permission.NewService(pebblestore.NewPermissionStore(home.s.sessions.Store().Underlying()), nil, nil)
				perms.SetSessionResolver(home.s.sessions)
				id, action := "", "disable"
				var revision uint64
				unchanged := func() bool { return false }
				changed := func() bool { return false }
				if kind == "manage_workflows" {
					a, _, err := repo.Put(remote.account, jobautomation.Automation{Name: "Review", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review"}}})
					if err != nil {
						t.Fatal(err)
					}
					id = a.ID
					unchanged = func() bool { got, err := repo.Get(remote.account, id); return err == nil && got.Enabled }
					changed = func() bool { return !unchanged() }
				} else {
					ws := remote.s.sessions.Store().WorkerStore()
					w, err := ws.CreateWorker(remote.account, remote.user, pebblestore.CreateWorkerRequest{Name: "Review", Instructions: "Review", Role: "finder"}, nil)
					if err != nil {
						t.Fatal(err)
					}
					id = w.ID
					revision = w.Revision
					action = "pause"
					unchanged = func() bool {
						got, found, err := ws.GetWorker(remote.account, id)
						return err == nil && found && got.Revision == revision && got.LifecycleState == w.LifecycleState
					}
					changed = func() bool {
						got, found, err := ws.GetWorker(remote.account, id)
						return err == nil && found && got.LifecycleState == pebblestore.WorkerLifecycleStatePaused && got.Revision > revision
					}
				}
				args := map[string]any{"machine": "build-box", "action": action, "id": id, "expected_revision": revision}
				encoded, _ := json.Marshal(args)
				auth, err := perms.AuthorizeToolCall(permission.AuthorizationInput{SessionID: session.ID, AccountScopeID: home.account, RunID: "run", CallID: "change", ToolName: kind, ToolArguments: string(encoded), Mode: "auto+bypass_permissions"})
				if err != nil {
					t.Fatal(err)
				}
				if !unchanged() {
					t.Fatal("changed before authorization")
				}
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				if level == "off" {
					if auth.Decision != permission.AuthorizationDeny {
						t.Fatal(auth)
					}
					if _, err := home.s.CallFleetWork(ctx, p, "build-box", kind, action, args); err == nil || !strings.Contains(err.Error(), "linked.build-box.work") {
						t.Fatalf("off: %v", err)
					}
					if !unchanged() {
						t.Fatal("off changed state")
					}
					return
				}
				if auth.Decision != permission.AuthorizationApprove || auth.Record != nil {
					t.Fatal(auth)
				}
				if _, err := home.s.CallFleetWork(ctx, p, "build-box", kind, action, args); err != nil {
					t.Fatal(err)
				}
				if !changed() {
					t.Fatal("remote change not stored")
				}
				if kind == "manage_workers" {
					listing, err := home.s.CallFleetWork(ctx, p, "build-box", kind, "list", nil)
					if err != nil || len(listing) > 2000 || !strings.Contains(string(listing), `"enabled":false`) || !strings.Contains(string(listing), `"last_run":null`) || !strings.Contains(string(listing), `"role":"finder"`) || strings.Contains(string(listing), "instructions") {
						t.Fatalf("linked summary: %s %v", listing, err)
					}
				}
			})
		}
	}
}

// Purpose: compactFleetWorkers must discard large instructions and run output,
// select the newest run across opaque-ID pages, and fail on incomplete history.
// The projection layer is the narrowest proof of summary shape and size bounds.
func TestFleetWorkerCompact(t *testing.T) {
	rows := []any{}
	for i := 0; i < 12; i++ {
		rows = append(rows, map[string]any{"id": "worker", "name": "Review", "role": "finder", "lifecycle_state": "idle", "model_profile": map[string]any{"action": map[string]any{"model": "fixture-model"}}, "instructions": strings.Repeat("x", 140000)})
	}
	raw, _ := json.Marshal(map[string]any{"workers": rows, "next_cursor": "next"})
	summary, err := compactFleetWorkers(raw, func(id, cursor string) (json.RawMessage, error) {
		if cursor == "" {
			return json.RawMessage(`{"runs":[{"id":"z","status":"failed","created_at":1}],"next_cursor":"page"}`), nil
		}
		return json.RawMessage(`{"runs":[{"id":"a","status":"succeeded","created_at":2,"completed_at":3}]}`), nil
	})
	if err != nil || len(summary) > 6000 {
		t.Fatalf("size %d: %v", len(summary), err)
	}
	var out struct {
		Workers []struct {
			ID, Name, Role, Model string
			Enabled               bool
			Last                  struct {
				State     string
				Completed int64 `json:"completed_at"`
			} `json:"last_run"`
		}
		Next string `json:"next_cursor"`
	}
	if err := json.Unmarshal(summary, &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Workers) != 12 || out.Next != "next" {
		t.Fatalf("shape %s", summary)
	}
	for _, w := range out.Workers {
		if w.ID != "worker" || w.Name != "Review" || w.Role != "finder" || w.Model != "fixture-model" || !w.Enabled || w.Last.State != "succeeded" || w.Last.Completed != 3 {
			t.Fatalf("summary %+v", w)
		}
	}
	if strings.Contains(string(summary), "instructions") {
		t.Fatal("definition leaked")
	}
	if _, err := compactFleetWorkers(raw, func(string, string) (json.RawMessage, error) { return json.RawMessage(`{"next_cursor":"repeat"}`), nil }); err == nil {
		t.Fatal("repeating history accepted")
	}
}

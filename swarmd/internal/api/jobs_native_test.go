package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: ManageJobs must share MCP delivery data, enforce account/principal and
// explicit per-agent grants before mutation, and refuse self loops without writes.
// The API adapter over real Pebble delivery/permission stores is the narrowest
// layer covering the authority checks and shared handler dispatch together.
func TestManageJobsAuthorityAndSharedDelivery(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "jobs.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	sessions := store.NewSessionStore(db)
	p := testPrincipal()
	worker, err := sessionruntime.NewService(sessions, nil).CreateWorker(context.Background(), p.AccountScopeID, p.UserID, store.CreateWorkerRequest{Name: "watcher", Role: "coder", Agent: "swarm"})
	if err != nil {
		t.Fatal(err)
	}
	ownJob := "worker/" + worker.ID + "/watcher-run"
	current := store.SessionSnapshot{ID: "watcher", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{"worker_id": worker.ID, "worker_execution_run_id": "watcher-run"}}
	if err := sessions.CreateSession(current); err != nil {
		t.Fatal(err)
	}
	perms := permission.NewService(store.NewPermissionStore(db), nil, nil)
	ds := jobautomation.NewStore(db)
	s := &Server{sessions: sessionruntime.NewService(sessions, nil), perm: perms}
	s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: ds}))
	a, _, err := ds.Put(p.AccountScopeID, jobautomation.Automation{Name: "daily", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review"}}})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := ds.CreateDelivery(a, jobautomation.Result{JobID: "worker/other/run", Kind: jobautomation.KindWorker, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State = jobautomation.StateFailed
	d.Steps[0].State = jobautomation.StateFailed
	d.Steps[0].Reason = "review failed"
	d.Steps[0].Attempts = 2
	if err := ds.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	got, err := s.ManageJobs(context.Background(), p, current.ID, "swarm", map[string]any{"action": "deliveries", "id": a.ID})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"deliveries": []map[string]any{jobDeliveryView(d)}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("delivery mismatch: %#v", got)
	}
	byName, err := s.ManageJobs(context.Background(), p, current.ID, "swarm", map[string]any{"action": "deliveries", "id": a.Name})
	if err != nil || !reflect.DeepEqual(byName, got) {
		t.Fatalf("name/id mismatch: %#v %v", byName, err)
	}
	foreignFlow, _, err := ds.Put("foreign", jobautomation.Automation{Name: "foreign-only", Scope: a.Scope, Enabled: true, Steps: a.Steps})
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"unknown-workflow", foreignFlow.Name, foreignFlow.ID} {
		if out, err := s.ManageJobs(context.Background(), p, current.ID, "swarm", map[string]any{"action": "deliveries", "id": ref}); err == nil || !strings.Contains(err.Error(), "not found") || out != nil {
			t.Fatalf("unknown/foreign workflow accepted: %q %#v %v", ref, out, err)
		}
	}
	foreign := p
	foreign.AccountScopeID = "foreign"
	if _, err := s.ManageJobs(context.Background(), foreign, current.ID, "swarm", map[string]any{"action": "deliveries"}); err == nil {
		t.Fatal("foreign principal accepted")
	}
	// Shared MCP get_job attaches exactly the same delivery view.
	c := &controlMCPCall{server: s, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &store.ScopedTokenRecord{Scopes: []string{"sessions:read", "automations:read"}})}
	views, ok := c.jobDeliveries(d.JobID)
	if !ok {
		t.Fatal("MCP deliveries unavailable")
	}
	lhs, _ := json.Marshal(views)
	rhs, _ := json.Marshal([]map[string]any{jobDeliveryView(d)})
	if string(lhs) != string(rhs) {
		t.Fatalf("MCP/native mismatch %s %s", lhs, rhs)
	}
	for _, action := range []string{"give_job", "message_job", "rerun_failed_steps"} {
		args := map[string]any{"action": action, "job_id": ownJob, "worker": worker.ID, "brief": "work", "message": "retry"}
		if _, err := s.ManageJobs(context.Background(), p, current.ID, "swarm", args); err == nil || !strings.Contains(err.Error(), "owner must allow") {
			t.Fatalf("missing grant: %v", err)
		}
		_, err := perms.UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_jobs.swarm." + action})
		if err != nil {
			t.Fatal(err)
		}
		if action != "give_job" {
			if _, err := s.ManageJobs(context.Background(), p, current.ID, "swarm", args); err == nil || !strings.Contains(err.Error(), "own job") {
				t.Fatalf("self target: %v", err)
			}
		}
	}
	after, err := ds.GetDelivery(p.AccountScopeID, d.ID)
	if err != nil || !reflect.DeepEqual(after, d) {
		t.Fatal("rejection changed delivery", err)
	}
	// Resolve aliases before comparing worker identity; no mutation dispatch.
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"workers": []map[string]any{{"id": worker.ID, "name": "watcher"}}})
	})
	if err := s.refuseNativeJobSelfTarget(c, current, "give_job", map[string]any{"worker": "watcher"}); err == nil {
		t.Fatal("own worker alias accepted")
	}
	// Child of a delivery cannot target the triggering job.
	d.Steps[0].Outcome = map[string]any{"job_id": ownJob}
	if err := ds.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	c.next = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"run": map[string]any{"session_id": "other"}})
	})
	if err := s.refuseNativeJobSelfTarget(c, current, "rerun_failed_steps", map[string]any{"job_id": d.JobID}); err == nil || !strings.Contains(err.Error(), "own workflow") {
		t.Fatalf("delivery loop: %v", err)
	}
}

// Purpose: lossless oversized results must stay below the 64 KiB inline boundary,
// including escaped text, and reject changed continuation data. nativeJobPage is
// the narrowest layer proving no spill file or silent truncation is needed.
func TestManageJobsPagingAndGrants(t *testing.T) {
	out := map[string]any{"runs": strings.Repeat("\x00界\\\"", 20000)}
	var joined strings.Builder
	args := map[string]any{}
	for i := 0; i < 100; i++ {
		page, err := nativeJobPage(out, args, false)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(page)
		if len(encoded) > 64*1024 {
			t.Fatal("oversized page")
		}
		m := page.(map[string]any)
		joined.WriteString(m["json_page"].(string))
		next, more := m["next_page_offset"]
		if !more {
			break
		}
		args["page_offset"] = float64(next.(int))
		args["page_digest"] = m["page_digest"]
	}
	want, _ := json.Marshal(out)
	if joined.String() != string(want) {
		t.Fatal("lost page data")
	}
	if _, err := nativeJobPage(map[string]any{"changed": true}, args, false); err == nil {
		t.Fatal("stale page accepted")
	}
	policy := permission.Policy{Rules: []permission.PolicyRule{{Kind: permission.PolicyRuleKindTool, Tool: "manage_jobs.watch.message_job", Decision: permission.PolicyDecisionAllow}}}
	if !nativeJobActionGranted(policy, "watch", "message_job") || nativeJobActionGranted(policy, "other", "message_job") || nativeJobActionGranted(policy, "watch", "give_job") {
		t.Fatal("grant scope")
	}
	policy.Rules = append(policy.Rules, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Tool: "manage_jobs", Decision: permission.PolicyDecisionDeny})
	if nativeJobActionGranted(policy, "watch", "message_job") {
		t.Fatal("deny ignored")
	}
}

// Purpose: permitted native actions must reach the same job handlers as MCP,
// while ungranted actions must not reach mutation routes. Real delivery/grant
// stores plus deterministic route responses isolate the adapter (not providers).
// This proves handler parity and rerun postconditions, not live worker execution.
func TestManageJobsGrantedDispatch(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "dispatch.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p := testPrincipal()
	ss := store.NewSessionStore(db)
	if err := ss.CreateSession(store.SessionSnapshot{ID: "watch", AccountScopeID: p.AccountScopeID, UserID: p.UserID}); err != nil {
		t.Fatal(err)
	}
	perms := permission.NewService(store.NewPermissionStore(db), nil, nil)
	s := &Server{sessions: sessionruntime.NewService(ss, nil), perm: perms}
	ds := jobautomation.NewStore(db)
	s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: ds}))
	a, _, err := ds.Put(p.AccountScopeID, jobautomation.Automation{Name: "daily", Enabled: true, Scope: jobautomation.Scope{Folder: t.TempDir()}, Steps: []jobautomation.Step{{ID: "check", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "review"}}})
	if err != nil {
		t.Fatal(err)
	}
	d, _, err := ds.CreateDelivery(a, jobautomation.Result{JobID: "media/project/task", Kind: jobautomation.KindMedia, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	d.State = jobautomation.StateFailed
	d.Steps[0].State = jobautomation.StateFailed
	if err := ds.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	workerDelivery, _, err := ds.CreateDelivery(a, jobautomation.Result{JobID: "worker/other/run", Kind: jobautomation.KindWorker, Status: jobautomation.StatusSucceeded})
	if err != nil {
		t.Fatal(err)
	}
	workerDelivery.State = jobautomation.StateBlocked
	workerDelivery.BlockedReason = "review required"
	if err := ds.SaveDelivery(workerDelivery); err != nil {
		t.Fatal(err)
	}
	writes := 0
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromRequest(r)
		if !ok || principal.AccountScopeID != p.AccountScopeID || principal.UserID != p.UserID {
			t.Fatal("lost principal")
		}
		if r.Method == http.MethodPost {
			writes++
		}
		switch {
		case r.URL.Path == "/v3/workers":
			writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": "other", "name": "other"}}})
		case r.URL.Path == "/v3/projects":
			writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "name": "project", "workspaces": []any{map[string]any{"path": "/workspace", "workspace_id": "ws"}}}}})
		case strings.HasSuffix(r.URL.Path, "/direct"):
			writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "run"}})
		case r.URL.Path == "/v3/workers/other/runs/run":
			writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "run", "session_id": "job-session", "status": "completed"}})
		case r.URL.Path == "/v3/projects/project/tasks/task":
			writeJSON(w, 200, map[string]any{"task": map[string]any{"id": "task", "session_id": "job-session", "status": "completed"}})
		case r.URL.Path == "/v3/projects/project/tasks":
			writeJSON(w, 200, map[string]any{"tasks": []any{map[string]any{"id": "task", "session_id": "job-session", "status": "completed"}}})
		case strings.HasSuffix(r.URL.Path, "/messages") && r.Method == http.MethodPost:
			writeJSON(w, 200, map[string]any{"accepted": true})
		case r.URL.Path == "/v3/runs":
			writeJSON(w, 200, map[string]any{"runs": []any{map[string]any{"id": "run", "status": "failed"}}, "next_cursor": "next"})
		default:
			writeJSON(w, 200, map[string]any{})
		}
	})
	s.controlMCPRoutesOnce.Do(func() { s.controlMCPRoutes = handler })
	c := &controlMCPCall{server: s, next: handler, request: requestWithScopedToken(withTestPrincipal(httptest.NewRequest("POST", "/mcp", nil)), &store.ScopedTokenRecord{Scopes: []string{"sessions:read", "automations:read"}})}
	for _, action := range []string{"list_runs", "get_job"} {
		args := map[string]any{"action": action, "job_id": "worker/other/run"}
		got, err := s.ManageJobs(context.Background(), p, "watch", "swarm", args)
		if err != nil {
			t.Fatal(err)
		}
		var want any
		if action == "get_job" {
			want, err = controlMCPGetJob(c, args)
		} else {
			want, err = controlMCPListRuns(c, args)
		}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s differs: %v %#v %#v", action, err, got, want)
		}
	}
	for _, action := range []string{"give_job", "message_job", "rerun_failed_steps"} {
		args := map[string]any{"action": action, "worker": "other", "brief": "work", "project": "project", "message": "fix", "job_id": d.JobID}
		before := writes
		if _, err := s.ManageJobs(context.Background(), p, "watch", "swarm", args); err == nil || writes != before {
			t.Fatal("ungranted mutation")
		}
		if _, err := perms.UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_jobs.swarm." + action}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ManageJobs(context.Background(), p, "watch", "swarm", args); err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if action != "rerun_failed_steps" && writes != before+1 {
			t.Fatalf("%s mutation count %d", action, writes-before)
		}
	}
	after, err := ds.GetDelivery(p.AccountScopeID, d.ID)
	if err != nil || after.State != jobautomation.StatePending || after.Steps[0].State != jobautomation.StatePending || after.Reruns != 1 {
		t.Fatalf("rerun not persisted: %+v %v", after, err)
	}
}

// Purpose: ManageJobs must authorize the account-owned worker's most specific
// identity, never its shared base agent. Real worker/session/permission stores
// and bounded route responses prove refused calls do not dispatch writes, while
// exact custom-agent and worker-ID grants dispatch once and retain self-loop checks.
func TestManageJobsWorkerGrantIdentity(t *testing.T) {
	for _, custom := range []string{"custom-watch", "", "system-worker-coder"} {
		t.Run("agent="+custom, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "identity.pebble"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			p := testPrincipal()
			ss := store.NewSessionStore(db)
			perms := permission.NewService(store.NewPermissionStore(db), nil, nil)
			s := &Server{sessions: sessionruntime.NewService(ss, nil), perm: perms}
			worker, err := s.sessions.CreateWorker(context.Background(), p.AccountScopeID, p.UserID, store.CreateWorkerRequest{Name: "watch", Role: "coder", Agent: custom})
			if err != nil {
				t.Fatal(err)
			}
			if err := ss.CreateSession(store.SessionSnapshot{ID: "watch", AccountScopeID: p.AccountScopeID, UserID: p.UserID, Metadata: map[string]any{"worker_id": worker.ID}}); err != nil {
				t.Fatal(err)
			}
			key := custom
			if key == "" || strings.HasPrefix(key, "system-worker-") {
				key = "worker_" + worker.ID
			}
			writes := 0
			s.controlMCPRoutesOnce.Do(func() {
				s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method == http.MethodPost {
						writes++
					}
					if r.URL.Path == "/v3/projects" {
						writeJSON(w, 200, map[string]any{"projects": []any{map[string]any{"id": "project", "name": "project", "workspaces": []any{map[string]any{"path": "/workspace", "workspace_id": "ws"}}}}})
					} else if r.URL.Path == "/v3/workers" {
						writeJSON(w, 200, map[string]any{"workers": []any{map[string]any{"id": "other", "name": "other"}, map[string]any{"id": worker.ID, "name": "self"}}})
					} else {
						writeJSON(w, 200, map[string]any{"run": map[string]any{"id": "run"}})
					}
				})
			})
			args := map[string]any{"action": "give_job", "worker": "other", "brief": "work"}
			grant := func(identity string) {
				t.Helper()
				if _, err := perms.UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Decision: permission.PolicyDecisionAllow, Tool: "manage_jobs." + identity + ".give_job"}); err != nil {
					t.Fatal(err)
				}
			}
			for _, wrong := range []string{"unrelated-agent", "system-worker-coder"} {
				grant(wrong)
				if _, err := s.ManageJobs(context.Background(), p, "watch", "system-worker-coder", args); err == nil || !strings.Contains(err.Error(), "manage_jobs."+key+".give_job") || writes != 0 {
					t.Fatalf("wrong grant accepted or wrong refusal: %v writes=%d", err, writes)
				}
			}
			grant(key)
			if _, err := s.ManageJobs(context.Background(), p, "watch", "system-worker-coder", args); err != nil || writes != 1 {
				t.Fatalf("exact grant: %v writes=%d", err, writes)
			}
			args["worker"] = "self"
			if _, err := s.ManageJobs(context.Background(), p, "watch", "system-worker-coder", args); err == nil || !strings.Contains(err.Error(), "own worker") || writes != 1 {
				t.Fatalf("self-loop: %v writes=%d", err, writes)
			}
		})
	}
}

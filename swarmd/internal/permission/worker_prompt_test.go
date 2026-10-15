package permission

import (
	"path/filepath"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/signals"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type workerPromptSessions struct{ worker bool }

func (s workerPromptSessions) GetSession(id string) (store.SessionSnapshot, bool, error) {
	snapshot := store.SessionSnapshot{ID: id, AccountScopeID: "account", UserID: "owner"}
	if s.worker {
		snapshot.Metadata = map[string]any{"worker_id": "worker", "worker_execution_run_id": "job-run"}
	}
	return snapshot, true, nil
}

// Purpose: CreatePending is the common persistence boundary for scope, write,
// network, plan and input gates. An active sandbox worker must never persist a
// wait or gain approval; interactive and sandbox-off sessions retain prompts.
// A real store checks absence of partial permission/run-wait state, and the
// authorization wrapper proves denial is a normal tool result, not a run error.
func TestWorkerPromptDeniedBeforePersistence(t *testing.T) {
	for _, tc := range []struct {
		name           string
		worker, active bool
	}{
		{"worker", true, true}, {"interactive", false, true}, {"sandbox_off", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, err := store.Open(filepath.Join(t.TempDir(), "db"))
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			svc := NewService(store.NewPermissionStore(db), nil, nil)
			svc.SetSessionResolver(workerPromptSessions{tc.worker})
			svc.SetBypassGate(func() (bool, string) { return tc.active, "sandbox off" })
			for _, requirement := range []string{"workspace_scope", "write", "network", "subagent", "plan", "ask_user", "client_tool", "design_source_sensitive_read"} {
				rec, err := svc.CreatePending(CreateInput{SessionID: "session", RunID: "run", CallID: requirement, ToolName: "search", Requirement: requirement, ToolArguments: `{"path":"outside"}`})
				if tc.worker && tc.active {
					if err == nil || !strings.Contains(err.Error(), "use bash inside the sandbox") || rec.ID != "" {
						t.Fatalf("not denied: %+v %v", rec, err)
					}
				} else if err != nil || rec.Status != store.PermissionStatusPending {
					t.Fatalf("prompt lost: %+v %v", rec, err)
				}
			}
			if tc.worker && tc.active {
				records, err := svc.store.ListPermissions("session", 100)
				if err != nil || len(records) != 0 {
					t.Fatalf("permissions persisted: %+v %v", records, err)
				}
				waits, err := svc.store.ListRunWaits("session", 100)
				if err != nil || len(waits) != 0 {
					t.Fatalf("waits persisted: %+v %v", waits, err)
				}
				result, err := svc.createPendingAuthorization(AuthorizationInput{ToolName: "bash"}, "session", "network", "", "", "")
				if err != nil || result.Decision != AuthorizationDeny || result.Record != nil {
					t.Fatalf("run-stopping error or approval: %+v %v", result, err)
				}
			}
		})
	}
}

// Purpose: workers that can still wait (sandbox off, including questions) must
// alert job sinks at the permission transition, without a get_job polling call
// or sensitive arguments in the feed. Real signal persistence is the narrowest
// layer proving delivery and job correlation through emitPermissionSignal.
func TestWorkerPermissionRaisesJobWaiting(t *testing.T) {
	db, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	svc := NewService(store.NewPermissionStore(db), nil, nil)
	svc.SetSessionResolver(workerPromptSessions{true})
	signalStore := store.NewSignalStore(db)
	svc.SetSignalEmitter(signals.NewEmitter(signalStore))
	_, err = svc.CreatePending(CreateInput{SessionID: "session", RunID: "provider-run", ToolName: "ask_user", ToolArguments: `{"question":"private-question"}`})
	if err != nil {
		t.Fatal(err)
	}
	page, err := signalStore.ListAfter(0, 20, store.SignalFilter{Account: "account"})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, sig := range page.Signals {
		if sig.Kind == "job.waiting" {
			count++
			if sig.Severity != store.SignalSeverityWarning || sig.Refs["job_id"] != "worker/worker/job-run" || sig.Refs["tool"] != "ask_user" || strings.Contains(sig.Summary, "private-question") {
				t.Fatalf("bad signal: %+v", sig)
			}
		}
	}
	if count != 1 {
		t.Fatalf("got %d waiting signals: %+v", count, page.Signals)
	}
}

package run

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: a completed canonical workspace snapshot survives abrupt process
// exit and reopens unchanged. Authority: ApplySessionMutation and Pebble session
// storage. A separate test process exits without Close (not the shared daemon).
// This proves only AFTER acknowledged mutation; it deliberately does not claim
// coverage inside the batch commit/outbox boundary or orphan lane recovery.
func TestWorkspaceLaunchAcknowledgedMutationProcessExit(t *testing.T) {
	if root := os.Getenv("SWARM_WORKSPACE_CRASH_FIXTURE"); root != "" {
		store, err := pebblestore.Open(filepath.Join(root, "store"))
		if err != nil {
			t.Fatal(err)
		}
		sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), nil)
		snapshot := pebblestore.SessionSnapshot{ID: "fixture", WorkspacePath: filepath.Join(root, "source"), WorkspaceName: "before"}
		if err := pebblestore.NewSessionStore(store).CreateSessionForAccount(snapshot, "fixture-user", "fixture-account"); err != nil {
			t.Fatal(err)
		}
		snapshot, ok, err := sessions.GetSession("fixture")
		if err != nil || !ok {
			t.Fatalf("initial read: %v", err)
		}
		snapshot.WorkspaceName = "acknowledged"
		available := true
		snapshot.WorkspaceGrants = []pebblestore.WorkspaceGrant{{Kind: pebblestore.WorkspaceGrantPrimary, WorkspaceID: "fixture-workspace", WorkspaceGeneration: 1, Path: snapshot.WorkspacePath, Name: "acknowledged", Available: &available}}
		payload, err := json.Marshal(map[string]any{"session": snapshot})
		if err != nil {
			t.Fatal(err)
		}
		_, err = sessions.ApplySessionMutation(sessionruntime.SessionMutationInput{SessionID: "fixture", UserID: "fixture-user", AccountScopeID: "fixture-account", ClientRequestID: "mutation", IdempotencyKey: "mutation", PayloadHash: "mutation", RequestHash: "mutation", Kind: sessionruntime.SessionMutationUpdateSettings, EventType: "session.workspace.updated", EventPayload: payload, Session: &snapshot, NowUnixMs: 1000})
		if err != nil {
			t.Fatal(err)
		}
		actual, ok, err := sessions.GetSession("fixture")
		if err != nil || !ok {
			t.Fatalf("acknowledged read: %v", err)
		}
		bytes, err := json.Marshal(actual)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "acknowledged.json"), bytes, 0600); err != nil {
			t.Fatal(err)
		}
		os.Exit(23) // no deferred store Close, isolated child only
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceLaunchAcknowledgedMutationProcessExit$")
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "SWARM_WORKSPACE_CRASH_FIXTURE=") {
			cmd.Env = append(cmd.Env, variable)
		}
	}
	cmd.Env = append(cmd.Env, "SWARM_WORKSPACE_CRASH_FIXTURE="+root)
	output, err := cmd.CombinedOutput()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 23 {
		t.Fatalf("isolated abrupt exit: %v %.2000s", err, output)
	}
	bytes, err := os.ReadFile(filepath.Join(root, "acknowledged.json"))
	if err != nil {
		t.Fatal(err)
	}
	var want pebblestore.SessionSnapshot
	if err := json.Unmarshal(bytes, &want); err != nil {
		t.Fatal(err)
	}
	store, err := pebblestore.Open(filepath.Join(root, "store"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	actual, ok, err := sessionruntime.NewService(pebblestore.NewSessionStore(store), nil).GetSession("fixture")
	if err != nil || !ok || !reflect.DeepEqual(actual, want) {
		t.Fatalf("reopened acknowledged snapshot differs: ok=%v err=%v", ok, err)
	}
	if actual.WorkspaceName != "acknowledged" || len(actual.WorkspaceGrants) != 1 || actual.WorkspaceGrants[0].WorkspaceID != "fixture-workspace" {
		t.Fatal("workspace identity lost on reopen")
	}
}

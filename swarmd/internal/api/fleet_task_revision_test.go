package api

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/fleet"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// newLinkedReopenRemote is a machine that can reopen tasks for real (the
// routed session server with runner, executor and the reopen authority's
// services) and can be linked: an owner identity, a node key and its fleet.
// The returned principal is the owner the link acts as on that machine.
func newLinkedReopenRemote(t *testing.T, root string) (*fleetTestSwarm, *confirmationFixture) {
	t.Helper()
	f := newConfirmationFixture(t, filepath.Join(root, "store"), nil)
	server := f.server
	identityDB, err := pebblestore.Open(filepath.Join(root, "identity"))
	if err != nil {
		t.Fatal(err)
	}
	identityStore := pebblestore.NewIdentityStore(identityDB)
	identitySvc := identity.NewService(identityStore)
	identitySessions := identity.NewSessionService(identityStore, pebblestore.NewIdentitySessionStore(identityDB))
	if _, err := identitySvc.BootstrapFirstIdentity("remote-owner"); err != nil {
		t.Fatal(err)
	}
	server.SetIdentityService(identitySvc)
	server.SetIdentitySessionService(identitySessions)
	actor, err := identitySessions.ActorForCurrentSelection()
	if err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	node, err := server.swarmStore.PutLocalNode(pebblestore.SwarmLocalNodeRecord{
		SwarmID: "host-swarm-id", Name: "remote", Role: "master",
		PublicKey: base64.StdEncoding.EncodeToString(public), PrivateKey: base64.StdEncoding.EncodeToString(private),
		Fingerprint: fleet.Fingerprint(public),
	})
	if err != nil {
		t.Fatal(err)
	}
	fleetDB, err := pebblestore.Open(filepath.Join(root, "fleet"))
	if err != nil {
		t.Fatal(err)
	}
	server.SetFleet(pebblestore.NewFleetStore(fleetDB), pebblestore.NewAuthStore(fleetDB))
	// The owner's account resolves its agents' models like every account.
	if _, err := pebblestore.NewAgentModelSettingsStore(server.sessions.Store().Underlying()).PutForAccount(testAgentModelSettingsRecord(actor.AccountScopeID)); err != nil {
		t.Fatal(err)
	}
	remote := &fleetTestSwarm{s: server, account: actor.AccountScopeID, user: actor.UserID, fp: node.Fingerprint, cleanup: func() {
		server.CloseFleetListener()
		_ = fleetDB.Close()
		_ = identityDB.Close()
	}}
	t.Cleanup(remote.cleanup)
	return remote, f
}

// Purpose: from the home, manage_projects reopen_task and archive_task with
// machine and the tool's expected_revision reach the linked machine's task
// routes with the revision those routes read (revision, an integer): the
// right revision reopens the remote task (a new attempt carrying the
// feedback) or archives it, and a wrong revision gets the machine's own
// conflict error with nothing changed there. A revision given as text is
// refused on the home before the machine is called, as on this machine.
// Threat (forge, 2026-10-29): CallFleetProject copied expected_revision
// verbatim, the machine's reopen route read revision, and every linked
// reopen was refused with "client_request_id, revision and feedback
// (1-32000 bytes) required"; passing revision instead arrived as text.
// Owners: Server.CallFleetProject (mapFleetTaskBody, fleetTaskRevision) and,
// on the machine, handleProjectTaskFollowup -> ReopenProjectTask and the
// archive route (ArchiveProjectTaskIfRevision).
// Narrowest layer: two in-process daemons over the real pinned link; the
// remote is a full routed server because only the real reopen authority
// shows the revision being honoured (reservation, allocation, launch).
func TestLinkedReopenAndArchiveCarryTheTaskRevision(t *testing.T) {
	root := t.TempDir()
	remote, f := newLinkedReopenRemote(t, root)
	f.runner.handler = func(context.Context, provideriface.Request, func(provideriface.StreamEvent)) (provideriface.Response, error) {
		return provideriface.Response{Text: "continued", StopReason: "stop"}, nil
	}
	remotePrincipal := identity.Principal{Type: identity.PrincipalTypeUser, UserID: remote.user, AccountScopeID: remote.account}
	retained := seedRetainedTask(t, remote.s, f.sessions, remotePrincipal, filepath.Join(root, "repo"), "failed")

	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "box"})
	if code, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); code != http.StatusCreated {
		t.Fatalf("link: %d %v", code, out)
	}
	hp := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	reopen := func(revision any, key string) (json.RawMessage, error) {
		// Tool arguments arrive JSON-decoded: an integer is a float64.
		return home.s.CallFleetProject(ctx, hp, "box", "reopen_task", map[string]any{"project_id": retained.projectID, "task_id": retained.taskID, "expected_revision": revision, "feedback": "finish the interrupted round", "client_request_id": key})
	}
	unchanged := func(label string) {
		t.Helper()
		task := retained.task(t, remote.s)
		if task.Revision != retained.initialTaskRevision || len(task.Attempts) != retained.initialAttemptsCount || task.Status != "failed" {
			t.Fatalf("%s changed the remote task: revision %d -> %d, attempts %d -> %d, status %s", label, retained.initialTaskRevision, task.Revision, retained.initialAttemptsCount, len(task.Attempts), task.Status)
		}
	}

	// Text is refused on the home under the tool's own rule.
	if _, err := reopen("1", "linked-reopen-text"); err == nil || !strings.Contains(err.Error(), "expected_revision must be an integer") || strings.Contains(err.Error(), "box refused") {
		t.Fatalf("text revision: %v", err)
	}
	unchanged("a text revision")
	// A wrong revision: the machine's own conflict, nothing changed.
	_, err := reopen(float64(retained.initialTaskRevision+7), "linked-reopen-stale")
	if err == nil || !strings.Contains(err.Error(), "box refused reopen_task") || !strings.Contains(err.Error(), "HTTP 409") || !strings.Contains(err.Error(), "stale or missing task revision") {
		t.Fatalf("wrong revision: %v, want the machine's 409 stale revision", err)
	}
	unchanged("a wrong revision")
	// The right revision reopens the remote task.
	raw, err := reopen(float64(retained.initialTaskRevision), "linked-reopen-1")
	if err != nil {
		t.Fatalf("linked reopen: %v", err)
	}
	var reply struct {
		Status string                        `json:"status"`
		Task   pebblestore.ProjectTaskRecord `json:"task"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil || reply.Status != "reopened" {
		t.Fatalf("linked reopen reply: %s %v", raw, err)
	}
	after := retained.task(t, remote.s)
	attempt := after.ActiveAttempt()
	if after.Revision <= retained.initialTaskRevision || len(after.Attempts) != retained.initialAttemptsCount+1 || attempt == nil || attempt.Request != "finish the interrupted round" || attempt.ClientRequestID != "linked-reopen-1" || attempt.LaunchState != "launched" {
		t.Fatalf("remote task not reopened: revision %d -> %d, attempts %d -> %d, active %+v", retained.initialTaskRevision, after.Revision, retained.initialAttemptsCount, len(after.Attempts), attempt)
	}
	if _, ok, err := f.sessions.GetSessionRunIntent(after.SessionID, after.ExecutionRunID()); err != nil || !ok {
		t.Fatalf("reopened run not recorded on the machine: %v %v", ok, err)
	}

	// archive_task: a second, finished task on the machine.
	if err := remote.s.sessions.Store().PutProjectTask(remote.account, &pebblestore.ProjectTaskRecord{ID: "done-task", ProjectID: retained.projectID, AccountID: remote.account, Title: "Done", Agent: "finder", Status: "completed"}); err != nil {
		t.Fatal(err)
	}
	done, _, _ := remote.s.sessions.Store().GetProjectTask(remote.account, retained.projectID, "done-task")
	archive := func(revision any) error {
		_, err := home.s.CallFleetProject(ctx, hp, "box", "archive_task", map[string]any{"project_id": retained.projectID, "task_id": "done-task", "expected_revision": revision})
		return err
	}
	if err := archive(float64(done.Revision + 3)); err == nil || !strings.Contains(err.Error(), "box refused archive_task") || !strings.Contains(err.Error(), "stale task revision") {
		t.Fatalf("archive with a wrong revision: %v", err)
	}
	if still, _, _ := remote.s.sessions.Store().GetProjectTask(remote.account, retained.projectID, "done-task"); still.Archived {
		t.Fatal("a wrong revision archived the task")
	}
	if err := archive(float64(done.Revision)); err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived, _, _ := remote.s.sessions.Store().GetProjectTask(remote.account, retained.projectID, "done-task"); !archived.Archived {
		t.Fatal("linked archive_task did not archive the task")
	}
	// refine_task with a plan document has no linked route: refused on the
	// home, not silently turned into a feedback-only refine on the machine.
	if _, err := home.s.CallFleetProject(ctx, hp, "box", "refine_task", map[string]any{"project_id": retained.projectID, "task_id": retained.taskID, "feedback": "x", "plan_document": map[string]any{"title": "p"}}); err == nil || !strings.Contains(err.Error(), "not available on a linked machine") {
		t.Fatalf("refine with a plan document: %v", err)
	}
}

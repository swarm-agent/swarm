package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/workspace"
)

// Requirement: model edits to existing workers are catalog-validated proposals,
// never account-setting writes or immediate executable changes. Threat: invalid
// or stale HTTP selections partially update a running worker. Production PUT and
// Accept handlers with hermetic authorities prove transport, validation and CAS.
func TestWorkerExistingModelReviewHTTP(t *testing.T) {
	s, db, handler := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	s.workspace = workspace.NewService(store.NewWorkspaceStore(db))
	execution, err := s.workerExecutionService()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := execution.ResolveModelProfile("acct-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := s.sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "Worker", Instructions: "Review", ModelProfile: profile, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Accept("acct-test", "user-test", w.ID, w.Revision)
	if err != nil {
		t.Fatal(err)
	}
	views, err := s.workerWorkspaceViews(identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "acct-test", UserID: "user-test"}, w)
	if err != nil {
		t.Fatal(err)
	}
	primary, ok := views["primary"].(map[string]any)
	if !ok || primary["available"] != true || primary["path"] == "" || primary["name"] == "" {
		t.Fatalf("authorized workspace labels missing: %+v", views)
	}
	foreignViews, err := s.workerWorkspaceViews(identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: "foreign", UserID: "foreign"}, w)
	if err == nil {
		foreign, _ := foreignViews["primary"].(map[string]any)
		if foreign["path"] != nil || foreign["name"] != nil {
			t.Fatal("foreign principal received workspace labels")
		}
	}
	settings := store.NewAgentModelSettingsStore(db)
	before, _, err := settings.GetForAccount("acct-test")
	if err != nil {
		t.Fatal(err)
	}
	invalid := store.CloneSessionModelProfileSnapshot(profile)
	invalid.Action.Model = "invalid-worker-model"
	body, err := json.Marshal(map[string]any{"expected_revision": w.Revision, "model_profile": invalid})
	if err != nil {
		t.Fatal(err)
	}
	response := executeWorkerAPI(handler, http.MethodPut, "/"+w.ID, string(body), workerAPICallOptions{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid selection: %d %s", response.Code, response.Body.String())
	}
	unchanged, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || !reflect.DeepEqual(w, unchanged) {
		t.Fatal("invalid selection changed worker")
	}
	response = executeWorkerAPI(handler, http.MethodPut, "/"+w.ID, fmt.Sprintf(`{"expected_revision":%d,"model_profile":%s}`, w.Revision, mustWorkerProfileJSON(t, profile)), workerAPICallOptions{})
	if response.Code != http.StatusOK {
		t.Fatalf("proposal: %d %s", response.Code, response.Body.String())
	}
	pending, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || pending.PendingReview == nil || pending.LifecycleState != store.WorkerLifecycleStateActive || !reflect.DeepEqual(pending.ModelProfile, w.ModelProfile) {
		t.Fatal("model edit not staged")
	}
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d}`, pending.Revision), workerAPICallOptions{})
	if response.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", response.Code, response.Body.String())
	}
	accepted, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || accepted.PendingReview != nil || !reflect.DeepEqual(accepted.ModelProfile, profile) {
		t.Fatal("accept lost selected model")
	}
	after, _, err := settings.GetForAccount("acct-test")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("model edit changed account settings")
	}
}

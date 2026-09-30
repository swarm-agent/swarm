package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"testing"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: explicit human acceptance persists exactly the reviewed worker
// model without changing account settings. Threat: invalid/stale/foreign HTTP
// bodies partially accept a pending worker. Actual handlers and isolated shared
// execution authorities are the narrowest layer proving transport and CAS.
func TestWorkerModelHTTPAcceptanceRoundTrip(t *testing.T) {
	s, db, handler := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	execution, err := s.workerExecutionService()
	if err != nil { t.Fatal(err) }
	profile, err := execution.ResolveModelProfile("acct-test", nil)
	if err != nil { t.Fatal(err) }
	ws := s.sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "Worker", Instructions: "Review", ModelProfile: profile, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil { t.Fatal(err) }
	settings := store.NewAgentModelSettingsStore(db)
	before, _, err := settings.GetForAccount("acct-test")
	if err != nil { t.Fatal(err) }
	invalid := store.CloneSessionModelProfileSnapshot(profile)
	invalid.Action.Model = "invalid-worker-model"
	body, err := json.Marshal(map[string]any{"expected_revision": w.Revision, "model_profile": invalid})
	if err != nil { t.Fatal(err) }
	response := executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", string(body), workerAPICallOptions{})
	if response.Code != http.StatusBadRequest { t.Fatalf("invalid status: %d %s", response.Code, response.Body.String()) }
	unchanged, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || unchanged.Revision != w.Revision || unchanged.LifecycleState != store.WorkerLifecycleStatePending { t.Fatalf("invalid choice mutated: %+v %v", unchanged, err) }
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d,"model_profile":%s}`, w.Revision+1, mustWorkerProfileJSON(t, profile)), workerAPICallOptions{})
	if response.Code != http.StatusConflict { t.Fatalf("stale status: %d", response.Code) }
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d,"model_profile":%s}`, w.Revision, mustWorkerProfileJSON(t, profile)), workerAPICallOptions{})
	if response.Code != http.StatusOK { t.Fatalf("acceptance: %d %s", response.Code, response.Body.String()) }
	var result struct { Worker store.WorkerRecord `json:"worker"` }
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil { t.Fatal(err) }
	if !reflect.DeepEqual(result.Worker.ModelProfile, profile) || result.Worker.LifecycleState != store.WorkerLifecycleStateActive { t.Fatalf("round trip changed selection: %+v", result.Worker) }
	after, _, err := settings.GetForAccount("acct-test")
	if err != nil || !reflect.DeepEqual(before, after) { t.Fatal("acceptance changed account settings") }
}

func mustWorkerProfileJSON(t *testing.T, profile *store.SessionModelProfileSnapshot) string {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil { t.Fatal(err) }
	return string(data)
}

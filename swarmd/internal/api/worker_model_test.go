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
	settings := store.NewAgentModelSettingsStore(db)
	before, _, err := settings.GetForAccount("acct-test")
	if err != nil {
		t.Fatal(err)
	}
	invalid := store.CloneSessionModelProfileSnapshot(profile)
	invalid.UseAccountDefault = false
	invalid.ActionUseAccountDefault = false
	invalid.PlanUseAccountDefault = false
	invalid.Action.Model = "invalid-worker-model"
	body, err := json.Marshal(map[string]any{"expected_revision": w.Revision, "model_profile": invalid})
	if err != nil {
		t.Fatal(err)
	}
	response := executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", string(body), workerAPICallOptions{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid status: %d %s", response.Code, response.Body.String())
	}
	unchanged, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || unchanged.Revision != w.Revision || unchanged.LifecycleState != store.WorkerLifecycleStatePending {
		t.Fatalf("invalid choice mutated: %+v %v", unchanged, err)
	}
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d,"model_profile":%s}`, w.Revision+1, mustWorkerProfileJSON(t, profile)), workerAPICallOptions{})
	if response.Code != http.StatusConflict {
		t.Fatalf("stale status: %d", response.Code)
	}
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d,"model_profile":%s}`, w.Revision, mustWorkerProfileJSON(t, profile)), workerAPICallOptions{})
	if response.Code != http.StatusOK {
		t.Fatalf("acceptance: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Worker store.WorkerRecord `json:"worker"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Worker.ModelProfile, profile) || result.Worker.LifecycleState != store.WorkerLifecycleStateActive {
		t.Fatalf("round trip changed selection: %+v", result.Worker)
	}
	after, _, err := settings.GetForAccount("acct-test")
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("acceptance changed account settings")
	}
}

func mustWorkerProfileJSON(t *testing.T, profile *store.SessionModelProfileSnapshot) string {
	t.Helper()
	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// Requirement: a saved candidate is catalog-validated again at human acceptance,
// even when the client sends only revision CAS. Threat: a formerly valid proposal
// bypasses validation or an approved profile replaces its candidate. HTTP handlers
// plus the real execution/store boundary are the narrowest transport proof.
func TestWorkerModelSavedCandidateAcceptanceValidation(t *testing.T) {
	s, db, handler := setupWorkerAPITestServer(t)
	workspaceID := setupWorkerAPIExecution(t, s, db)
	execution, err := s.workerExecutionService()
	if err != nil {
		t.Fatal(err)
	}
	profile, err := execution.ResolveModelProfile("acct-test", nil)
	if err != nil {
		t.Fatal(err)
	}
	ws := s.sessions.Store().WorkerStore()
	w, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "Review", Instructions: "Review", ModelProfile: profile, InitialLifecycleState: store.WorkerLifecycleStatePending, ProposedBindings: map[string]string{"primary": workspaceID}, WorkspaceRequirements: []store.WorkerWorkspaceRequirement{{Role: "primary", Required: true}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	w, err = execution.Accept("acct-test", "user-test", w.ID, w.Revision)
	if err != nil {
		t.Fatal(err)
	}
	candidate := store.CloneSessionModelProfileSnapshot(profile)
	candidate.UseAccountDefault = false
	candidate.ActionUseAccountDefault = false
	candidate.PlanUseAccountDefault = false
	// Simulate a catalog-invalid saved proposal through the structural store
	// boundary, not the HTTP catalog validation. Acceptance must reject it too.
	candidate.Action.Model = "unavailable-fixture-model"
	staged, err := ws.UpdateWorker("acct-test", "user-test", w.ID, w.Revision, store.UpdateWorkerRequest{ModelProfile: candidate}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response := executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d}`, staged.Revision), workerAPICallOptions{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("saved invalid candidate accepted: %d %s", response.Code, response.Body.String())
	}
	unchanged, _, err := ws.GetWorker("acct-test", w.ID)
	if err != nil || !reflect.DeepEqual(unchanged, staged) {
		t.Fatalf("rejected acceptance partially mutated worker: %+v %v", unchanged, err)
	}
	unsupported := store.CloneSessionModelProfileSnapshot(candidate)
	unsupported.Action = profile.Action
	unsupported.Action.Thinking = "unsupported-fixture-thinking"
	staged, err = ws.UpdateWorker("acct-test", "user-test", w.ID, staged.Revision, store.UpdateWorkerRequest{ModelProfile: unsupported}, nil)
	if err != nil {
		t.Fatal(err)
	}
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d}`, staged.Revision), workerAPICallOptions{})
	if response.Code != http.StatusBadRequest {
		t.Fatalf("unsupported thinking accepted: %d %s", response.Code, response.Body.String())
	}
	unchanged, _, err = ws.GetWorker("acct-test", w.ID)
	if err != nil || !reflect.DeepEqual(unchanged, staged) {
		t.Fatal("unsupported thinking partially mutated worker")
	}
	// Repair through the canonical HTTP proposal route, then accept by revision
	// alone. No client model copy is required to persist the reviewed candidate.
	candidate = store.CloneSessionModelProfileSnapshot(profile)
	candidate.UseAccountDefault = false
	candidate.ActionUseAccountDefault = false
	candidate.PlanUseAccountDefault = false
	response = executeWorkerAPI(handler, http.MethodPut, "/"+w.ID, fmt.Sprintf(`{"expected_revision":%d,"model_profile":%s}`, staged.Revision, mustWorkerProfileJSON(t, candidate)), workerAPICallOptions{})
	if response.Code != http.StatusOK {
		t.Fatalf("proposal: %d %s", response.Code, response.Body.String())
	}
	var result struct {
		Worker store.WorkerRecord `json:"worker"`
	}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	staged = result.Worker
	if staged.PendingReview == nil || !reflect.DeepEqual(staged.ModelProfile, w.ModelProfile) {
		t.Fatal("proposal applied before acceptance")
	}
	for _, options := range []workerAPICallOptions{{account: "foreign-account", user: "foreign-owner"}, {}} {
		revision := staged.Revision
		expected := http.StatusNotFound
		if options.account == "" {
			revision--
			expected = http.StatusConflict
		}
		response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d}`, revision), options)
		if response.Code != expected {
			t.Fatalf("guard: %d %s", response.Code, response.Body.String())
		}
		unchanged, _, err = ws.GetWorker("acct-test", w.ID)
		if err != nil || !reflect.DeepEqual(unchanged, staged) {
			t.Fatal("guard changed saved candidate")
		}
	}
	response = executeWorkerAPI(handler, http.MethodPost, "/"+w.ID+"/accept", fmt.Sprintf(`{"expected_revision":%d}`, staged.Revision), workerAPICallOptions{})
	if response.Code != http.StatusOK {
		t.Fatalf("accept: %d %s", response.Code, response.Body.String())
	}
	response = executeWorkerAPI(handler, http.MethodGet, "/"+w.ID, "", workerAPICallOptions{})
	if response.Code != http.StatusOK {
		t.Fatalf("read: %d %s", response.Code, response.Body.String())
	}
	result.Worker = store.WorkerRecord{}
	if err = json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Worker.PendingReview != nil || !reflect.DeepEqual(result.Worker.ModelProfile, staged.PendingReview.ModelProfile) {
		t.Fatalf("read lost candidate: %+v", result.Worker)
	}
}

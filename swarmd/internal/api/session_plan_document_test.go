package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func TestSessionPlansAPIPostsAndReturnsOnePlanDocument(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	createRec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create session status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	body := []byte(`{
		"id":"plan-one",
		"title":"One Plan",
		"plan":"# Optional rendered plan",
		"status":"draft",
		"approval_state":"draft",
		"document":{
			"schema_version":"session-plan-document/v1",
			"info":{"goal":"Implement one structured plan","constraints":["markdown is display only"],"validation_strategy":"go test ./internal/session ./internal/api"},
			"checkpoints":[{"id":"cp-1","title":"Model","status":"active","objective":"Add structured one-plan document","tasks":["add types","preserve on save"],"acceptance_criteria":["document round trips"],"validation":["go test ./internal/session"]}],
			"active_checkpoint_id":"cp-1",
			"rendered_text":"# Optional rendered plan"
		}
	}`)
	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+createPayload.Session.ID+"/plans", bytes.NewReader(body))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusOK {
		t.Fatalf("post plan status = %d, body=%s", postRec.Code, postRec.Body.String())
	}
	var postPayload struct {
		Plan pebblestore.SessionPlanSnapshot `json:"plan"`
	}
	if err := json.Unmarshal(postRec.Body.Bytes(), &postPayload); err != nil {
		t.Fatalf("decode post response: %v", err)
	}
	if postPayload.Plan.Document == nil || postPayload.Plan.Document.ID != "plan-one" || postPayload.Plan.Document.Title != "One Plan" {
		t.Fatalf("posted document identity = %#v", postPayload.Plan.Document)
	}
	if postPayload.Plan.Document.ActiveCheckpointID != "cp-1" || len(postPayload.Plan.Document.Checkpoints) != 1 || postPayload.Plan.Document.Checkpoints[0].Order != 1 {
		t.Fatalf("posted document checkpoints = %#v", postPayload.Plan.Document)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+createPayload.Session.ID+"/plans/plan-one", nil)
	getRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(getRec, withTestPrincipal(getReq))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get plan status = %d, body=%s", getRec.Code, getRec.Body.String())
	}
	var getPayload struct {
		Plan pebblestore.SessionPlanSnapshot `json:"plan"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("decode get response: %v", err)
	}
	if getPayload.Plan.Document == nil || getPayload.Plan.Document.Info.Goal != "Implement one structured plan" {
		t.Fatalf("returned document = %#v", getPayload.Plan.Document)
	}
}

func TestSessionPlansAPIDocumentPatchCreatesOneRevision(t *testing.T) {
	server, _, _, _, swarmStore := newRoutedSessionTestServerWithSwarmStore(t)
	seedSessionsV2PrimaryAuthority(t, server, swarmStore, "host-swarm-id", "binding-primary-v2", "/host/swarm-go")
	createRec := postSessionsV2Primary(t, server, `{"swarm_id":"host-swarm-id","workspace_binding_id":"binding-primary-v2","title":"primary v2","mode":"auto","agent_name":"swarm","worktree_mode":"off","preference":{"provider":"codex","model":"gpt-5.4","thinking":"medium"}}`)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create session status = %d, body=%s", createRec.Code, createRec.Body.String())
	}
	var createPayload struct {
		Session pebblestore.SessionSnapshot `json:"session"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	createBody := []byte(`{
		"id":"plan-one",
		"title":"One Plan",
		"plan":"# Optional rendered plan",
		"status":"draft",
		"approval_state":"draft",
		"document":{
			"info":{"goal":"Initial structured plan"},
			"checkpoints":[{"id":"cp-1","title":"Model","status":"active"},{"id":"cp-2","title":"API","status":"pending"}],
			"active_checkpoint_id":"cp-1"
		}
	}`)
	postReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+createPayload.Session.ID+"/plans", bytes.NewReader(createBody))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(postRec, withTestPrincipal(postReq))
	if postRec.Code != http.StatusOK {
		t.Fatalf("post plan status = %d, body=%s", postRec.Code, postRec.Body.String())
	}

	patchBody := []byte(`{
		"id":"plan-one",
		"update_summary":"complete model and activate API",
		"update_scope":"checkpoints",
		"update_kind":"document_patch",
		"document_patch":{"operations":[
			{"operation":"complete_checkpoint","checkpoint_id":"cp-1","report":"model done","changed_files":["swarmd/internal/session/plan_document.go"]},
			{"operation":"set_active_checkpoint","active_checkpoint_id":"cp-2"}
		]}
	}`)
	patchReq := httptest.NewRequest(http.MethodPost, "/v2/sessions/"+createPayload.Session.ID+"/plans", bytes.NewReader(patchBody))
	patchReq.Header.Set("Content-Type", "application/json")
	patchRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(patchRec, withTestPrincipal(patchReq))
	if patchRec.Code != http.StatusOK {
		t.Fatalf("patch plan status = %d, body=%s", patchRec.Code, patchRec.Body.String())
	}
	var patchPayload struct {
		Plan pebblestore.SessionPlanSnapshot `json:"plan"`
	}
	if err := json.Unmarshal(patchRec.Body.Bytes(), &patchPayload); err != nil {
		t.Fatalf("decode patch response: %v", err)
	}
	if patchPayload.Plan.Version != 2 || patchPayload.Plan.ParentRevision != 1 {
		t.Fatalf("revision linkage = version %d parent %d", patchPayload.Plan.Version, patchPayload.Plan.ParentRevision)
	}
	if patchPayload.Plan.Document == nil || patchPayload.Plan.Document.ActiveCheckpointID != "cp-2" || patchPayload.Plan.Document.Checkpoints[0].Status != "done" || patchPayload.Plan.Document.Checkpoints[0].Report != "model done" {
		t.Fatalf("patched document = %#v", patchPayload.Plan.Document)
	}

	historyReq := httptest.NewRequest(http.MethodGet, "/v2/sessions/"+createPayload.Session.ID+"/plans/plan-one/history", nil)
	historyRec := httptest.NewRecorder()
	server.Handler().ServeHTTP(historyRec, withTestPrincipal(historyReq))
	if historyRec.Code != http.StatusOK {
		t.Fatalf("history status = %d, body=%s", historyRec.Code, historyRec.Body.String())
	}
	var historyPayload struct {
		Revisions []pebblestore.SessionPlanSnapshot `json:"revisions"`
	}
	if err := json.Unmarshal(historyRec.Body.Bytes(), &historyPayload); err != nil {
		t.Fatalf("decode history response: %v", err)
	}
	if len(historyPayload.Revisions) != 2 {
		t.Fatalf("history revisions = %d, want 2", len(historyPayload.Revisions))
	}
}

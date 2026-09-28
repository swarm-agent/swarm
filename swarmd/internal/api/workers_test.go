package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: Verify the canonical authenticated /v3/workers API contract.
// Product Invariants:
//  1. Workers are stable, account-scoped entities independent of sessions/plans.
//  2. All worker endpoints require authenticated user principal and active account membership, rejecting agents, revoked members, and unauthenticated callers.
//  3. Scopes are enforced: automations:read for queries/exports/validation, automations:write for mutations/import/migration.
//  4. Trigger credentials and worker-scoped tokens cannot enumerate or manage workers, and cannot access foreign workers. All scoped trigger tokens are rejected centrally.
//  5. Cross-account isolation: data from account A is never visible or mutable from account B.
//  6. Optimistic concurrency control: updates and attachment changes reject stale expected revisions with 409 Conflict.
//  7. Stale or invalid requests cause no partial mutations to underlying storage.
//  8. Strict request parsing rejects unknown fields, trailing payloads, and invalid or duplicate query parameters.
//  9. Idempotent worker creation with required idempotency_key in body returns the existing record on duplicate calls.
// 10. Definition attachment management and legacy migration are supported. Worker DELETE route is removed (stop barrier).

func setupWorkerAPITestServer(t *testing.T) (*Server, *store.Store, http.Handler) {
	t.Helper()
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	// Seed active account memberships for test accounts with valid IdentityStore records
	ids := store.NewIdentityStore(db)
	for _, pair := range [][2]string{
		{"acct-test", "user-test"},
		{"acct-1", "user-1"},
		{"acct-2", "user-2"},
	} {
		if _, err := ids.PutUser(store.UserRecord{
			ID:       pair[1],
			Username: pair[1],
		}); err != nil {
			t.Fatalf("seed user %s: %v", pair[1], err)
		}
		if _, err := ids.PutAccountScope(store.AccountScopeRecord{
			ID:              pair[0],
			Type:            store.AccountScopeTypePersonal,
			CreatedByUserID: pair[1],
		}); err != nil {
			t.Fatalf("seed account scope %s: %v", pair[0], err)
		}
		if _, err := ids.PutAccountUser(store.AccountUserRecord{
			ID:             "mem-" + pair[0] + "-" + pair[1],
			AccountScopeID: pair[0],
			UserID:         pair[1],
			Status:         store.AccountUserStatusActive,
		}); err != nil {
			t.Fatalf("seed account user %s/%s: %v", pair[0], pair[1], err)
		}
	}

	ss := store.NewSessionStore(db)
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}
	h := s.apiMux()
	return s, db, h
}

type workerAPICallOptions struct {
	account     string
	user        string
	principal   *identity.Principal
	agentOrigin bool
	scopes      []string
	scopedToken *store.ScopedTokenRecord
	headers     map[string]string
}

func executeWorkerAPI(h http.Handler, method, path, body string, opts workerAPICallOptions) *httptest.ResponseRecorder {
	url := WorkersPath + path
	var bodyReader *strings.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	} else {
		bodyReader = strings.NewReader("")
	}
	r := httptest.NewRequest(method, url, bodyReader)

	account := opts.account
	if account == "" {
		account = "acct-test"
	}
	user := opts.user
	if user == "" {
		user = "user-test"
	}

	ctx := r.Context()
	var p identity.Principal
	if opts.principal != nil {
		p = *opts.principal
	} else {
		p = identity.Principal{Type: "user", UserID: user, AccountScopeID: account}
	}
	ctx = context.WithValue(ctx, productPrincipalRequestContextKey, p)

	if opts.agentOrigin {
		boundCtx, err := automation.BindRuntimeIdentity(ctx, p, "agent", "child-session-1")
		if err != nil {
			panic(err)
		}
		ctx = boundCtx
	}

	if opts.scopedToken != nil {
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, opts.scopedToken)
	} else if len(opts.scopes) > 0 {
		tok := &store.ScopedTokenRecord{
			AccountScopeID: account,
			UserID:         user,
			Scopes:         opts.scopes,
		}
		ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tok)
	}

	for k, v := range opts.headers {
		r.Header.Set(k, v)
	}

	r = r.WithContext(ctx)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestWorkerAPI_AuthenticationAndAuthorization(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)

	// 1. Unauthenticated request -> 401 Unauthorized
	r := httptest.NewRequest(http.MethodGet, WorkersPath, nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated expected 401, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Invalid principal (Type != "user") -> 401 Unauthorized
	agentPrincipal := identity.Principal{Type: "agent", UserID: "bot", AccountScopeID: "acct-test"}
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		principal: &agentPrincipal,
		scopes:    []string{"automations:read"},
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid agent principal expected 401, got %d: %s", w.Code, w.Body.String())
	}

	// 3. REAL valid runtime-bound agent -> 403 Forbidden
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		agentOrigin: true,
		scopes:      []string{"automations:read"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("bound agent origin expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Revoked membership -> 403 Forbidden
	ids := store.NewIdentityStore(db)
	if _, err := ids.PutAccountUser(store.AccountUserRecord{
		ID:             "mem-acct-test-user-test",
		AccountScopeID: "acct-test",
		UserID:         "user-test",
		Status:         "revoked",
	}); err != nil {
		t.Fatalf("put revoked account user: %v", err)
	}
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("revoked membership expected 403, got %d: %s", w.Code, w.Body.String())
	}
	// Restore active membership
	if _, err := ids.PutAccountUser(store.AccountUserRecord{
		ID:             "mem-acct-test-user-test",
		AccountScopeID: "acct-test",
		UserID:         "user-test",
		Status:         store.AccountUserStatusActive,
	}); err != nil {
		t.Fatalf("restore active account user: %v", err)
	}

	// 5. Missing membership in identity store -> 403 Forbidden
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		user:   "unregistered-user",
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing membership expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 6. Missing required read scope -> 403 Forbidden
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		scopes: []string{"sessions:read"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing automations:read expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 7. Missing required write scope on POST -> 403 Forbidden
	w = executeWorkerAPI(h, http.MethodPost, "", `{"name":"test","idempotency_key":"key-1"}`, workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing automations:write expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 8. Scoped trigger tokens denied centrally across endpoints -> 403 Forbidden
	triggerOpts := workerAPICallOptions{
		scopedToken: &store.ScopedTokenRecord{
			AccountScopeID: "acct-test",
			UserID:         "user-test",
			Scopes:         []string{"automations:trigger"},
		},
	}
	w = executeWorkerAPI(h, http.MethodGet, "", "", triggerOpts)
	if w.Code != http.StatusForbidden {
		t.Fatalf("trigger-only token expected 403 on list, got %d: %s", w.Code, w.Body.String())
	}
	w = executeWorkerAPI(h, http.MethodPost, "", `{"name":"test","idempotency_key":"key-1"}`, triggerOpts)
	if w.Code != http.StatusForbidden {
		t.Fatalf("trigger-only token expected 403 on create, got %d: %s", w.Code, w.Body.String())
	}
	w = executeWorkerAPI(h, http.MethodGet, "/some_worker", "", triggerOpts)
	if w.Code != http.StatusForbidden {
		t.Fatalf("trigger-only token expected 403 on get, got %d: %s", w.Code, w.Body.String())
	}

	// 9. Worker-scoped token cannot enumerate (list) -> 403 Forbidden
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		scopedToken: &store.ScopedTokenRecord{
			AccountScopeID: "acct-test",
			UserID:         "user-test",
			WorkerID:       "worker_specific",
			Scopes:         []string{"automations:read"},
		},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("worker-scoped token on list expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 10. Worker-scoped token cannot manage (create) -> 403 Forbidden
	w = executeWorkerAPI(h, http.MethodPost, "", `{"name":"test","idempotency_key":"key-1"}`, workerAPICallOptions{
		scopedToken: &store.ScopedTokenRecord{
			AccountScopeID: "acct-test",
			UserID:         "user-test",
			WorkerID:       "worker_specific",
			Scopes:         []string{"automations:write"},
		},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("worker-scoped token on create expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// Verify nonmutation: no workers were created or modified by any unauthorized calls
	ws := store.NewWorkerStore(db)
	listRes, err := ws.ListWorkers("acct-test", store.ListWorkersQuery{})
	if err != nil {
		t.Fatalf("list workers: %v", err)
	}
	if len(listRes.Workers) != 0 {
		t.Fatalf("unauthorized calls created %d workers, expected 0", len(listRes.Workers))
	}
}

func TestWorkerAPI_CRUDLifecycleAndOptimisticConcurrency(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)

	// 1. Create a worker (requires idempotency_key in body)
	createBody := `{
		"name": "Data Pipeline Worker",
		"description": "Processes telemetry pipelines",
		"instructions": "Run ingestion and summarize alerts.",
		"idempotency_key": "idemp-crud-test-1",
		"requested_capabilities": [
			{"type": "tool", "name": "bash", "required": true}
		],
		"workspace_requirements": [
			{"role": "primary", "required": true}
		],
		"metadata": {"team": "infra"}
	}`
	w := executeWorkerAPI(h, http.MethodPost, "", createBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create worker expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var createResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatal(err)
	}
	workerObj, ok := createResp["worker"].(map[string]any)
	if !ok {
		t.Fatalf("expected worker object in response, got %v", createResp)
	}
	workerID := workerObj["id"].(string)
	if workerID == "" {
		t.Fatalf("expected non-empty worker ID")
	}
	if workerObj["lifecycle_state"].(string) != "idle" {
		t.Fatalf("expected initial lifecycle_state idle, got %v", workerObj["lifecycle_state"])
	}
	if workerObj["revision"].(float64) != 1 {
		t.Fatalf("expected initial revision 1, got %v", workerObj["revision"])
	}

	// 2. Get the created worker
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID, "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("get worker expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var getResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &getResp)
	gotWorker := getResp["worker"].(map[string]any)
	if gotWorker["name"].(string) != "Data Pipeline Worker" {
		t.Fatalf("worker name mismatch: %v", gotWorker["name"])
	}

	// 3. Foreign worker access with worker-scoped token: denied!
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID, "", workerAPICallOptions{
		scopedToken: &store.ScopedTokenRecord{
			AccountScopeID: "acct-test",
			UserID:         "user-test",
			WorkerID:       "worker_other_id",
			Scopes:         []string{"automations:read"},
		},
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign worker access expected 403, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Same worker access with worker-scoped token: allowed!
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID, "", workerAPICallOptions{
		scopedToken: &store.ScopedTokenRecord{
			AccountScopeID: "acct-test",
			UserID:         "user-test",
			WorkerID:       workerID,
			Scopes:         []string{"automations:read"},
		},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("matching worker-scoped token expected 200, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Update worker with stale revision -> 409 Conflict & NO partial mutation
	staleUpdateBody := `{
		"expected_revision": 99,
		"name": "Corrupted Name That Should Never Persist",
		"instructions": "Corrupted Instructions"
	}`
	w = executeWorkerAPI(h, http.MethodPut, "/"+workerID, staleUpdateBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("stale update expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// Verify database state: worker was NOT modified!
	ws := store.NewWorkerStore(db)
	persisted, found, err := ws.GetWorker("acct-test", workerID)
	if err != nil || !found {
		t.Fatalf("failed fetching persisted worker: %v", err)
	}
	if persisted.Revision != 1 {
		t.Fatalf("expected revision to remain 1 after conflict, got %d", persisted.Revision)
	}
	if persisted.Name != "Data Pipeline Worker" {
		t.Fatalf("persisted name was mutated on conflict! Got %s", persisted.Name)
	}

	// 6. Update worker with valid expected revision in body -> 200 OK & revision incremented
	validUpdateBody := `{
		"expected_revision": 1,
		"name": "Updated Pipeline Worker",
		"change_summary": "Updated name"
	}`
	w = executeWorkerAPI(h, http.MethodPut, "/"+workerID, validUpdateBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("valid update expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updateResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &updateResp)
	updatedWorker := updateResp["worker"].(map[string]any)
	if updatedWorker["revision"].(float64) != 2 {
		t.Fatalf("expected revision 2 after update, got %v", updatedWorker["revision"])
	}
	if updatedWorker["name"].(string) != "Updated Pipeline Worker" {
		t.Fatalf("expected updated name, got %v", updatedWorker["name"])
	}

	// 7. List workers with pagination and state filtering (no total_count in canonical wire)
	w = executeWorkerAPI(h, http.MethodGet, "?limit=10&lifecycle_state=idle", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("list workers expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	items := listResp["workers"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 worker in list, got %d", len(items))
	}
	if _, hasTotal := listResp["total_count"]; hasTotal {
		t.Fatalf("total_count must not be present in canonical wire response")
	}

	// 8. Worker DELETE route is REMOVED -> 405 Method Not Allowed
	w = executeWorkerAPI(h, http.MethodDelete, "/"+workerID, "", workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusMethodNotAllowed {
		t.Fatalf("worker DELETE route expected 405 Method Not Allowed, got %d: %s", w.Code, w.Body.String())
	}

	// Verify worker is still active/persisted and was NOT deleted
	persistedStill, found, err := ws.GetWorker("acct-test", workerID)
	if err != nil || !found {
		t.Fatalf("worker was unexpectedly deleted: %v", err)
	}
	if persistedStill.LifecycleState == store.WorkerLifecycleStateDeleted {
		t.Fatalf("worker was marked deleted despite 405 method not allowed")
	}
}

func TestWorkerAPI_CrossAccountIsolation(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)
	ws := store.NewWorkerStore(db)

	// Create worker in account-1
	createBody := `{"name": "Private Account 1 Worker", "idempotency_key": "idemp-acct1-1"}`
	w := executeWorkerAPI(h, http.MethodPost, "", createBody, workerAPICallOptions{
		account: "acct-1",
		user:    "user-1",
		scopes:  []string{"automations:write"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create in acct-1 expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	workerID := resp["worker"].(map[string]any)["id"].(string)

	// Attempt GET from account-2 -> 404 Not Found
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID, "", workerAPICallOptions{
		account: "acct-2",
		user:    "user-2",
		scopes:  []string{"automations:read"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-account get expected 404, got %d: %s", w.Code, w.Body.String())
	}

	// Attempt PUT from account-2 -> 404 Not Found
	w = executeWorkerAPI(h, http.MethodPut, "/"+workerID, `{"expected_revision":1,"name":"Hacked"}`, workerAPICallOptions{
		account: "acct-2",
		user:    "user-2",
		scopes:  []string{"automations:write"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross-account update expected 404, got %d: %s", w.Code, w.Body.String())
	}

	// Verify nonmutation: worker in account-1 is completely untouched
	persisted, found, err := ws.GetWorker("acct-1", workerID)
	if err != nil || !found {
		t.Fatalf("fetch account-1 worker: %v", err)
	}
	if persisted.Name != "Private Account 1 Worker" || persisted.Revision != 1 {
		t.Fatalf("account-1 worker was mutated by cross-account call: %+v", persisted)
	}

	// List in account-2 -> empty list
	w = executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		account: "acct-2",
		user:    "user-2",
		scopes:  []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("list in acct-2 expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &listResp)
	acct2Workers := listResp["workers"].([]any)
	if len(acct2Workers) != 0 {
		t.Fatalf("cross-account list leaked items: len=%d", len(acct2Workers))
	}

	// Verify database: account-2 has 0 workers
	dbList, err := ws.ListWorkers("acct-2", store.ListWorkersQuery{})
	if err != nil || len(dbList.Workers) != 0 {
		t.Fatalf("account-2 unexpectedly has %d workers in database", len(dbList.Workers))
	}
}

func TestWorkerAPI_Idempotency(t *testing.T) {
	_, _, h := setupWorkerAPITestServer(t)

	createBody := `{
		"name": "Idempotent Deployment Worker",
		"idempotency_key": "idemp-deploy-key-1"
	}`
	// First call
	w1 := executeWorkerAPI(h, http.MethodPost, "", createBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w1.Code != http.StatusCreated {
		t.Fatalf("first creation expected 201, got %d: %s", w1.Code, w1.Body.String())
	}
	var resp1 map[string]any
	_ = json.Unmarshal(w1.Body.Bytes(), &resp1)
	w1ID := resp1["worker"].(map[string]any)["id"].(string)

	// Second call with same idempotency key -> returns exact same worker!
	w2 := executeWorkerAPI(h, http.MethodPost, "", createBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w2.Code != http.StatusCreated && w2.Code != http.StatusOK {
		t.Fatalf("duplicate creation expected 200 or 201, got %d: %s", w2.Code, w2.Body.String())
	}
	var resp2 map[string]any
	_ = json.Unmarshal(w2.Body.Bytes(), &resp2)
	w2ID := resp2["worker"].(map[string]any)["id"].(string)

	if w1ID != w2ID {
		t.Fatalf("idempotency violation: first ID %q != second ID %q", w1ID, w2ID)
	}

	// Third call with same idempotency key but different payload -> 409 Conflict
	conflictBody := `{
		"name": "Different Worker Name Reusing Key",
		"idempotency_key": "idemp-deploy-key-1"
	}`
	w3 := executeWorkerAPI(h, http.MethodPost, "", conflictBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w3.Code != http.StatusConflict {
		t.Fatalf("idempotency payload conflict expected 409, got %d: %s", w3.Code, w3.Body.String())
	}

	// Verify only 1 worker exists in list
	wList := executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	var listResp map[string]any
	_ = json.Unmarshal(wList.Body.Bytes(), &listResp)
	items := listResp["workers"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected exactly 1 worker in store, got %d", len(items))
	}
}

func TestWorkerAPI_StrictParsingAndRejections(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)

	// 1. Unknown fields rejection on POST /v3/workers
	unknownFieldBody := `{"name": "Valid Name", "idempotency_key": "idemp-s-1", "unknown_rogue_field": "injected"}`
	w := executeWorkerAPI(h, http.MethodPost, "", unknownFieldBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("unknown field body expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 2. Client-supplied ID rejection on POST /v3/workers
	suppliedIDBody := `{"name": "Valid Name", "idempotency_key": "idemp-s-2", "id": "custom_worker_id"}`
	w = executeWorkerAPI(h, http.MethodPost, "", suppliedIDBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("supplied id body expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 3. Client-supplied local_bindings rejection on POST /v3/workers
	suppliedBindingsBody := `{"name": "Valid Name", "idempotency_key": "idemp-s-3", "local_bindings": {"ws": "local"}}`
	w = executeWorkerAPI(h, http.MethodPost, "", suppliedBindingsBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("supplied local_bindings body expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 3b. Client-supplied automation ID rejection on POST /v3/workers
	suppliedAutoIDBody := `{"name": "Valid Name", "idempotency_key": "idemp-s-3b", "automations": [{"id": "client_auto_id", "name": "Auto 1", "activation_mode": "manual"}]}`
	w = executeWorkerAPI(h, http.MethodPost, "", suppliedAutoIDBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("supplied automation id expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Missing required idempotency_key on POST /v3/workers
	missingIdempBody := `{"name": "Valid Name"}`
	w = executeWorkerAPI(h, http.MethodPost, "", missingIdempBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing idempotency_key expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 4b. Idempotency key exceeding 256 characters on POST /v3/workers -> 400 Bad Request
	oversizedIdempBody := fmt.Sprintf(`{"name": "Valid Name", "idempotency_key": "%s"}`, strings.Repeat("k", 257))
	w = executeWorkerAPI(h, http.MethodPost, "", oversizedIdempBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized idempotency_key expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 5. Trailing payload rejection
	trailingBody := `{"name": "Valid Name", "idempotency_key": "idemp-s-4"} {"trailing": "data"}`
	w = executeWorkerAPI(h, http.MethodPost, "", trailingBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("trailing payload body expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 5b. Malformed URL query (invalid percent encoding) -> 400 Bad Request
	w = executeWorkerAPI(h, http.MethodGet, "?limit=%zz", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("malformed query expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 6. Invalid query parameters on GET /v3/workers
	w = executeWorkerAPI(h, http.MethodGet, "?invalid_filter=something", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid query parameter expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 7. Duplicate query parameter on GET /v3/workers
	w = executeWorkerAPI(h, http.MethodGet, "?limit=10&limit=20", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate query parameter expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 8. Query parameter on POST /v3/workers -> rejected
	w = executeWorkerAPI(h, http.MethodPost, "?unexpected=query", `{"name":"Valid","idempotency_key":"k"}`, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("query on POST expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 9. Missing required name
	missingNameBody := `{"idempotency_key": "idemp-s-5", "description": "No name"}`
	w = executeWorkerAPI(h, http.MethodPost, "", missingNameBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing name expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 9b. Name exceeding 256 characters -> 400 Bad Request
	oversizedNameBody := fmt.Sprintf(`{"name": "%s", "idempotency_key": "idemp-s-6"}`, strings.Repeat("n", 257))
	w = executeWorkerAPI(h, http.MethodPost, "", oversizedNameBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("oversized name expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 10. Missing expected_revision in body on PUT /v3/workers/{id}
	w = executeWorkerAPI(h, http.MethodPut, "/worker_test_id", `{"name":"New Name"}`, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing expected_revision in body expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// Nonmutation check: verify zero workers were created by all rejected requests
	ws := store.NewWorkerStore(db)
	list, err := ws.ListWorkers("acct-test", store.ListWorkersQuery{})
	if err != nil || len(list.Workers) != 0 {
		t.Fatalf("expected 0 workers in store after rejected requests, got %d", len(list.Workers))
	}
}

func TestWorkerAPI_ValidateImportAndExport(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)
	ws := store.NewWorkerStore(db)

	portableJSON := `{
		"schema_version": 1,
		"name": "Cloud Backup Specialist",
		"description": "Performs database backups",
		"instructions": "Archive active snapshot to designated storage.",
		"capabilities": [
			{"type": "tool", "name": "bash", "required": true}
		],
		"automations": [
			{
				"name": "Nightly Backup",
				"activation_mode": "manual",
				"enabled": true,
				"plan": {
					"title": "Execute Backup Plan",
					"info": {"goal": "Safely archive data"},
					"checkpoints": [
						{
							"id": "cp-1",
							"title": "Backup Checkpoint",
							"status": "pending",
							"order": 1,
							"tasks": ["Run backup"],
							"acceptance_criteria": ["Backup complete"]
						}
					]
				}
			}
		]
	}`

	// 1. POST /v3/workers/validate with valid raw document -> 200 OK: {"valid":true,"worker":def}
	w := executeWorkerAPI(h, http.MethodPost, "/validate", portableJSON, workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("validate valid expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var valResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &valResp)
	if valResp["valid"] != true {
		t.Fatalf("expected valid: true, got %v", valResp["valid"])
	}
	if valResp["worker"] == nil {
		t.Fatalf("expected worker object in validate response")
	}

	// 2. POST /v3/workers/validate rejects query parameters -> 400 Bad Request
	w = executeWorkerAPI(h, http.MethodPost, "/validate?unexpected=true", portableJSON, workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("validate with query expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 3. POST /v3/workers/validate with invalid schema version -> 400 Bad Request
	invalidSchemaJSON := `{"schema_version": 99, "name": "Bad Schema"}`
	w = executeWorkerAPI(h, http.MethodPost, "/validate", invalidSchemaJSON, workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("validate invalid schema expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 4. POST /v3/workers/import (mode=new) requires idempotency_key in query -> 400 without it
	w = executeWorkerAPI(h, http.MethodPost, "/import?mode=new", portableJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("import new without idempotency_key expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 4b. POST /v3/workers/import (mode=new) rejects oversized idempotency_key -> 400 Bad Request
	oversizedKeyURL := fmt.Sprintf("/import?mode=new&idempotency_key=%s", strings.Repeat("k", 257))
	w = executeWorkerAPI(h, http.MethodPost, oversizedKeyURL, portableJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("import new with oversized key expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 5. POST /v3/workers/import (mode=new&idempotency_key=...) with raw portable doc -> 201 Created
	w = executeWorkerAPI(h, http.MethodPost, "/import?mode=new&idempotency_key=imp-key-1", portableJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("import new expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var importResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &importResp)
	impWorker := importResp["worker"].(map[string]any)
	impID := impWorker["id"].(string)
	if impID == "" {
		t.Fatalf("imported worker missing id")
	}
	if impWorker["name"].(string) != "Cloud Backup Specialist" {
		t.Fatalf("imported worker name mismatch: %v", impWorker["name"])
	}
	if impWorker["revision"].(float64) != 1 {
		t.Fatalf("imported worker revision expected 1, got %v", impWorker["revision"])
	}
	autos := impWorker["automations"].([]any)
	if len(autos) != 1 {
		t.Fatalf("expected 1 attached automation on imported worker, got %d", len(autos))
	}

	// 6. Duplicate import mode=new with same idempotency key -> idempotent return
	wDup := executeWorkerAPI(h, http.MethodPost, "/import?mode=new&idempotency_key=imp-key-1", portableJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if wDup.Code != http.StatusCreated && wDup.Code != http.StatusOK {
		t.Fatalf("duplicate import expected 200/201, got %d: %s", wDup.Code, wDup.Body.String())
	}

	// 7. GET /v3/workers/{id}/export -> 200 OK with {"worker": def} only
	w = executeWorkerAPI(h, http.MethodGet, "/"+impID+"/export", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("export expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var exportResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &exportResp)
	exportedDef := exportResp["worker"].(map[string]any)
	if exportedDef["name"].(string) != "Cloud Backup Specialist" {
		t.Fatalf("exported def name mismatch: %v", exportedDef["name"])
	}

	// 8. GET /v3/workers/{id}/export with query parameters (raw export variant removed) -> 400 Bad Request
	w = executeWorkerAPI(h, http.MethodGet, "/"+impID+"/export?raw=true", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("export with query parameter expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 9. POST /v3/workers/import (mode=update) with stale revision -> 409 Conflict
	modifiedJSON := strings.Replace(portableJSON, "Cloud Backup Specialist", "Updated Backup Specialist", 1)
	w = executeWorkerAPI(h, http.MethodPost, fmt.Sprintf("/import?mode=update&worker_id=%s&expected_revision=99", impID), modifiedJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("import update with stale revision expected 409, got %d: %s", w.Code, w.Body.String())
	}
	// Verify nonmutation on conflict: revision is still 1, name is still original
	persistedImp, found, err := ws.GetWorker("acct-test", impID)
	if err != nil || !found {
		t.Fatalf("fetch imported worker: %v", err)
	}
	if persistedImp.Revision != 1 || persistedImp.Name != "Cloud Backup Specialist" {
		t.Fatalf("imported worker was mutated on conflict! Revision: %d, Name: %s", persistedImp.Revision, persistedImp.Name)
	}

	// 9b. POST /v3/workers/import (mode=update) rejects oversized worker_id -> 400 Bad Request
	oversizedWorkerURL := fmt.Sprintf("/import?mode=update&worker_id=%s&expected_revision=1", strings.Repeat("w", 257))
	w = executeWorkerAPI(h, http.MethodPost, oversizedWorkerURL, modifiedJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("import update with oversized worker_id expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 10. POST /v3/workers/import (mode=update) with correct revision -> 200 OK
	w = executeWorkerAPI(h, http.MethodPost, fmt.Sprintf("/import?mode=update&worker_id=%s&expected_revision=1", impID), modifiedJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("import update valid expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updateImpResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &updateImpResp)
	updatedWorker := updateImpResp["worker"].(map[string]any)
	if updatedWorker["name"].(string) != "Updated Backup Specialist" {
		t.Fatalf("updated worker name mismatch: %v", updatedWorker["name"])
	}
	if updatedWorker["revision"].(float64) != 2 {
		t.Fatalf("updated worker revision expected 2, got %v", updatedWorker["revision"])
	}

	// 11. POST /v3/workers/import (mode=update) rejects idempotency_key parameter -> 400 Bad Request
	w = executeWorkerAPI(h, http.MethodPost, fmt.Sprintf("/import?mode=update&worker_id=%s&expected_revision=2&idempotency_key=k", impID), modifiedJSON, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("import update with idempotency_key expected 400, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkerAPI_AutomationAttachmentManagement(t *testing.T) {
	_, _, h := setupWorkerAPITestServer(t)

	// Create initial worker
	createBody := `{"name": "Task Runner", "idempotency_key": "idemp-attach-1"}`
	w := executeWorkerAPI(h, http.MethodPost, "", createBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("create worker expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var createResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &createResp)
	workerID := createResp["worker"].(map[string]any)["id"].(string)

	// 0. Attempt to attach with client-supplied ID -> 400 Bad Request
	attachWithSuppliedID := `{
		"expected_worker_revision": 1,
		"automation": {
			"id": "client_supplied_id",
			"name": "Recurring Triage",
			"activation_mode": "manual",
			"enabled": true,
			"plan_document": {
				"title": "Triage Inbox",
				"info": {"goal": "Check incoming issues"},
				"checkpoints": [
					{
						"id": "cp-1",
						"title": "Triage Checkpoint",
						"status": "pending",
						"order": 1,
						"tasks": ["Check incoming issues"],
						"acceptance_criteria": ["All incoming issues checked"]
					}
				]
			}
		}
	}`
	w = executeWorkerAPI(h, http.MethodPost, "/"+workerID+"/automations", attachWithSuppliedID, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("attach with supplied id expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 1. Attach automation without client ID -> 201 Created & worker revision incremented to 2
	attachBody := `{
		"expected_worker_revision": 1,
		"automation": {
			"name": "Recurring Triage",
			"activation_mode": "manual",
			"enabled": true,
			"plan_document": {
				"title": "Triage Inbox",
				"info": {"goal": "Check incoming issues"},
				"checkpoints": [
					{
						"id": "cp-1",
						"title": "Triage Checkpoint",
						"status": "pending",
						"order": 1,
						"tasks": ["Check incoming issues"],
						"acceptance_criteria": ["All incoming issues checked"]
					}
				]
			}
		}
	}`
	w = executeWorkerAPI(h, http.MethodPost, "/"+workerID+"/automations", attachBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusCreated {
		t.Fatalf("attach automation expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var attachResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &attachResp)
	workerAfterAttach := attachResp["worker"].(map[string]any)
	if workerAfterAttach["revision"].(float64) != 2 {
		t.Fatalf("expected revision 2 after attach, got %v", workerAfterAttach["revision"])
	}
	autos := workerAfterAttach["automations"].([]any)
	if len(autos) != 1 {
		t.Fatalf("expected 1 automation attached, got %d", len(autos))
	}
	auto0 := autos[0].(map[string]any)
	assignedAutoID := auto0["id"].(string)
	if assignedAutoID == "" {
		t.Fatalf("expected server-assigned automation ID")
	}

	// 2. Attach automation rejecting query parameters -> 400 Bad Request
	w = executeWorkerAPI(h, http.MethodPost, "/"+workerID+"/automations?expected_worker_revision=2", attachBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("attach with query param expected 400, got %d: %s", w.Code, w.Body.String())
	}

	// 3. Update attached automation with stale revision in body -> 409 Conflict
	updateAutoBodyStale := `{
		"expected_worker_revision": 1,
		"automation": {
			"name": "Updated Triage Title",
			"activation_mode": "manual",
			"enabled": false
		}
	}`
	w = executeWorkerAPI(h, http.MethodPut, "/"+workerID+"/automations/"+assignedAutoID, updateAutoBodyStale, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("update automation with stale revision expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// 4. Update attached automation with valid revision in body -> 200 OK & worker revision incremented to 3
	updateAutoBodyValid := `{
		"expected_worker_revision": 2,
		"automation": {
			"name": "Updated Triage Title",
			"activation_mode": "manual",
			"enabled": true,
			"plan_document": {
				"title": "Triage Inbox Updated",
				"info": {"goal": "Check incoming issues"},
				"checkpoints": [
					{
						"id": "cp-1",
						"title": "Triage Updated Checkpoint",
						"status": "pending",
						"order": 1,
						"tasks": ["Check incoming issues"],
						"acceptance_criteria": ["All incoming issues checked"]
					}
				]
			}
		}
	}`
	w = executeWorkerAPI(h, http.MethodPut, "/"+workerID+"/automations/"+assignedAutoID, updateAutoBodyValid, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("update automation expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var updateResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &updateResp)
	workerAfterUpdate := updateResp["worker"].(map[string]any)
	if workerAfterUpdate["revision"].(float64) != 3 {
		t.Fatalf("expected revision 3 after update, got %v", workerAfterUpdate["revision"])
	}

	// 5. Delete attached automation with stale revision -> 409 Conflict
	w = executeWorkerAPI(h, http.MethodDelete, fmt.Sprintf("/%s/automations/%s?expected_worker_revision=2", workerID, assignedAutoID), "", workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusConflict {
		t.Fatalf("delete automation with stale revision expected 409, got %d: %s", w.Code, w.Body.String())
	}

	// 6. Delete attached automation with valid revision -> 200 OK & worker revision incremented to 4
	w = executeWorkerAPI(h, http.MethodDelete, fmt.Sprintf("/%s/automations/%s?expected_worker_revision=3", workerID, assignedAutoID), "", workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("delete automation expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var deleteResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &deleteResp)
	workerAfterDelete := deleteResp["worker"].(map[string]any)
	if workerAfterDelete["revision"].(float64) != 4 {
		t.Fatalf("expected revision 4 after delete, got %v", workerAfterDelete["revision"])
	}
	// Empty automations omitted by omitempty, tests shouldn't panic
	if autosVal, ok := workerAfterDelete["automations"]; ok && autosVal != nil {
		autosAfterDel := autosVal.([]any)
		if len(autosAfterDel) != 0 {
			t.Fatalf("expected 0 automations after delete, got %d", len(autosAfterDel))
		}
	}

	// 7. Missing automation -> 404 Not Found (typed ErrWorkerNotFound mapped from store)
	w = executeWorkerAPI(h, http.MethodDelete, fmt.Sprintf("/%s/automations/nonexistent_auto?expected_worker_revision=4", workerID), "", workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusNotFound {
		t.Fatalf("missing automation expected 404, got %d: %s", w.Code, w.Body.String())
	}
}

func TestWorkerAPI_MigrateLegacyAutomations(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)

	// Seed legacy accepted automation in db
	account := "acct-test"
	legacyKey := fmt.Sprintf("automation/v2/accepted/%x/%x", account, "legacy_worker_1")
	legacyRecord := store.AutomationV2Record{
		AutomationID: "legacy_worker_1",
		Generation:   1,
		AutomationV2Proposal: store.AutomationV2Proposal{
			AccountID:   account,
			SessionID:   "session_leg_1",
			WorkspaceID: "ws_leg_1",
			Document: store.SessionPlanDocument{
				Title: "Legacy Ingest Worker",
				Info: store.SessionPlanInfo{
					Goal: "Ingest daily feeds",
				},
				AutomationV2: &store.AutomationV2Settings{
					SchemaVersion: 2,
					Schedule: store.AutomationV2Schedule{
						Kind:            "interval",
						IntervalSeconds: 3600,
					},
					Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
					Missed:           "skip",
					Overlap:          "serialize",
					ActivateOnAccept: true,
				},
			},
		},
	}
	if err := db.PutJSON(legacyKey, legacyRecord); err != nil {
		t.Fatalf("failed seeding legacy automation: %v", err)
	}

	// Call POST /v3/workers/migrate
	w := executeWorkerAPI(h, http.MethodPost, "/migrate", "{}", workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("migrate expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var migResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &migResp)
	summary := migResp["summary"].(map[string]any)
	if summary["scanned_count"].(float64) != 1 || summary["migrated_count"].(float64) != 1 {
		t.Fatalf("expected 1 scanned and 1 migrated, got %+v", summary)
	}

	// Verify migrated worker is present in list
	wList := executeWorkerAPI(h, http.MethodGet, "", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	var listResp map[string]any
	_ = json.Unmarshal(wList.Body.Bytes(), &listResp)
	items := listResp["workers"].([]any)
	if len(items) != 1 {
		t.Fatalf("expected 1 migrated worker in list, got %d", len(items))
	}
	worker := items[0].(map[string]any)
	if worker["name"].(string) != "Legacy Ingest Worker" {
		t.Fatalf("expected migrated worker name 'Legacy Ingest Worker', got %v", worker["name"])
	}
}

func TestWorkerAPI_RevisionHistoryAndRuns(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)

	// Create worker
	createBody := `{"name": "Audited Worker", "idempotency_key": "idemp-audit-1"}`
	w := executeWorkerAPI(h, http.MethodPost, "", createBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})
	var createResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &createResp)
	workerID := createResp["worker"].(map[string]any)["id"].(string)

	// Update to revision 2
	updateBody := `{"expected_revision": 1, "name": "Audited Worker Rev 2"}`
	executeWorkerAPI(h, http.MethodPut, "/"+workerID, updateBody, workerAPICallOptions{
		scopes: []string{"automations:write"},
	})

	// 1. GET /v3/workers/{id}/history -> 2 revisions
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID+"/history", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("get history expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var histResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &histResp)
	revs := histResp["revisions"].([]any)
	if len(revs) != 2 {
		t.Fatalf("expected 2 revisions in history, got %d", len(revs))
	}

	// 2. GET /v3/workers/{id}/revisions/1 -> gets snapshot of rev 1
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID+"/revisions/1", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("get revision 1 expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var rev1Resp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &rev1Resp)
	revRec := rev1Resp["revision"].(map[string]any)
	wSnap := revRec["worker"].(map[string]any)
	if wSnap["name"].(string) != "Audited Worker" {
		t.Fatalf("rev 1 worker snapshot name expected 'Audited Worker', got %v", wSnap["name"])
	}

	// Seed a worker run record
	ws := store.NewWorkerStore(db)
	runRec := store.WorkerRunRecord{
		ID:             store.GenerateWorkerRunID(),
		AccountScopeID: "acct-test",
		WorkerID:       workerID,
		WorkerRevision: 2,
		RequestSource:  "direct",
		Status:         "admitted",
		CreatedAt:      123456789,
	}
	if _, err := ws.RecordWorkerRun("acct-test", runRec); err != nil {
		t.Fatalf("record worker run: %v", err)
	}

	// 3. GET /v3/workers/{id}/runs -> list of runs
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID+"/runs", "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("list runs expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var runsResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &runsResp)
	runs := runsResp["runs"].([]any)
	if len(runs) != 1 {
		t.Fatalf("expected 1 run in list, got %d", len(runs))
	}

	// 4. GET /v3/workers/{id}/runs/{run_id} -> single run
	w = executeWorkerAPI(h, http.MethodGet, "/"+workerID+"/runs/"+runRec.ID, "", workerAPICallOptions{
		scopes: []string{"automations:read"},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("get run expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var runResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &runResp)
	gotRun := runResp["run"].(map[string]any)
	if gotRun["id"].(string) != runRec.ID {
		t.Fatalf("run ID mismatch: %v vs %v", gotRun["id"], runRec.ID)
	}
}

// Ensure unused import bytes compiles cleanly if needed
var _ = bytes.NewReader

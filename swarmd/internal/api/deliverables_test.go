package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
)

func TestDeliverablesAPI(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ss := store.NewSessionStore(db)
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}
	h := s.apiMux()

	call := func(method, path, body string, scopes []string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, DeliverablesPath+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		if len(scopes) > 0 {
			tokenRec := &store.ScopedTokenRecord{
				AccountScopeID: "account",
				UserID:         "owner",
				Scopes:         scopes,
			}
			ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, tokenRec)
		}
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. GET /v3/deliverables empty
	w := call(http.MethodGet, "", "", []string{"automations:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var listResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil {
		t.Fatal(err)
	}
	if listResp["count"].(float64) != 0 {
		t.Fatalf("expected 0 deliverables, got %v", listResp["count"])
	}

	// 2. POST /v3/deliverables - create social post deliverable
	createBody := `{
		"title": "Daily Social Posts for Launch",
		"kind": "social_post",
		"worker_id": "worker_social_bot",
		"payload": {
			"posts": [
				{"text": "1/2 Big announcement coming up..."},
				{"text": "2/2 Swarm V3 is now live!"}
			]
		},
		"action_contract": {
			"action": "publish_x_post",
			"target_secret_ref": "gcp:x-api-key"
		}
	}`
	w = call(http.MethodPost, "", createBody, []string{"automations:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var createResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil {
		t.Fatal(err)
	}
	delivObj := createResp["deliverable"].(map[string]any)
	delivID := delivObj["id"].(string)
	if delivID == "" {
		t.Fatalf("expected generated deliverable ID")
	}
	if delivObj["status"].(string) != "pending_review" {
		t.Fatalf("expected status pending_review, got %s", delivObj["status"])
	}

	// 3. GET /v3/deliverables/{id}
	w = call(http.MethodGet, "/"+delivID, "", []string{"automations:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var getResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &getResp); err != nil {
		t.Fatal(err)
	}
	fetchedDeliv := getResp["deliverable"].(map[string]any)
	if fetchedDeliv["title"].(string) != "Daily Social Posts for Launch" {
		t.Fatalf("expected title 'Daily Social Posts for Launch', got %v", fetchedDeliv["title"])
	}

	// 4. POST /v3/deliverables/{id}/approve (executes action and marks published/approved)
	w = call(http.MethodPost, "/"+delivID+"/approve", "{}", []string{"automations:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on approve, got %d: %s", w.Code, w.Body.String())
	}
	var approveResp map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &approveResp); err != nil {
		t.Fatal(err)
	}
	approvedDeliv := approveResp["deliverable"].(map[string]any)
	if approvedDeliv["status"].(string) != "published" {
		t.Fatalf("expected status published, got %s", approvedDeliv["status"])
	}
	actionRes := approveResp["action_result"].(map[string]any)
	if actionRes["published_to"].(string) != "x" {
		t.Fatalf("expected published_to x, got %v", actionRes["published_to"])
	}

	// 5. Create another deliverable and dismiss it
	alertBody := `{
		"title": "Alert: Nightly Build Drift",
		"kind": "alert",
		"worker_id": "worker_build_sentinel",
		"summary": "1 file drifted in dev worktree"
	}`
	w = call(http.MethodPost, "", alertBody, []string{"automations:write"})
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var alertResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &alertResp)
	alertID := alertResp["deliverable"].(map[string]any)["id"].(string)

	w = call(http.MethodPost, "/"+alertID+"/dismiss", "{}", []string{"automations:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on dismiss, got %d: %s", w.Code, w.Body.String())
	}
	var dismissResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &dismissResp)
	if dismissResp["deliverable"].(map[string]any)["status"].(string) != "dismissed" {
		t.Fatalf("expected status dismissed, got %v", dismissResp["deliverable"].(map[string]any)["status"])
	}

	// 6. List with filter: status=published should return 1, status=pending_review should return 0
	w = call(http.MethodGet, "?status=published", "", []string{"automations:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var filterResp map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &filterResp)
	if filterResp["count"].(float64) != 1 {
		t.Fatalf("expected 1 published deliverable, got %v", filterResp["count"])
	}

	// 7. DELETE /v3/deliverables/{id}
	w = call(http.MethodDelete, "/"+delivID, "", []string{"automations:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 on delete, got %d: %s", w.Code, w.Body.String())
	}

	// Verify it's gone
	w = call(http.MethodGet, "/"+delivID, "", []string{"automations:read"})
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", w.Code)
	}
}

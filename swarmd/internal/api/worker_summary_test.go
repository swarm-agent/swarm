package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: A worker hub daily/active summary must reflect all durable run
// receipts, independent of history page length, and use the selected IANA
// calendar day even when a DST transition changes its UTC duration.
// Threat: page-local totals, UTC-day grouping, or foreign/malformed requests
// leaking run state. Boundary: handleWorkerSummary, SummarizeWorkerRuns,
// WorkerStore.RecordWorkerRun; API is the narrowest auth/query layer.
func TestWorkerAPI_SummaryReceiptsAndAuthorization(t *testing.T) {
	_, db, h := setupWorkerAPITestServer(t)
	ws := store.NewWorkerStore(db)
	worker, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "Summary", IdempotencyKey: "summary-create"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2025, 3, 9, 0, 0, 0, 0, loc)
	for i := 0; i < 105; i++ {
		status := "succeeded"
		if i == 0 {
			status = "running"
		} else if i == 1 {
			status = "failed"
		}
		if _, err := ws.RecordWorkerRun("acct-test", store.WorkerRunRecord{
			ID: fmt.Sprintf("summary-run-%03d", i), AccountScopeID: "acct-test", UserID: "user-test", WorkerID: worker.ID,
			WorkerRevision: worker.Revision, RequestSource: "direct", Status: status, CreatedAt: start.Add(time.Hour).UnixMilli(),
		}); err != nil {
			t.Fatalf("record run %d: %v", i, err)
		}
	}
	// A receipt just after the 23-hour spring-forward day belongs to tomorrow.
	if _, err := ws.RecordWorkerRun("acct-test", store.WorkerRunRecord{
		ID: "summary-run-next", AccountScopeID: "acct-test", UserID: "user-test", WorkerID: worker.ID,
		WorkerRevision: worker.Revision, RequestSource: "direct", Status: "admitted", CreatedAt: start.AddDate(0, 0, 1).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	path := "/" + worker.ID + "/summary?timezone=America%2FNew_York&date=2025-03-09"
	opts := workerAPICallOptions{scopes: []string{"automations:read"}}
	res := executeWorkerAPI(h, http.MethodGet, path, "", opts)
	if res.Code != http.StatusOK {
		t.Fatalf("summary: %d %s", res.Code, res.Body.String())
	}
	var payload struct {
		WorkerID string                 `json:"worker_id"`
		Runs     store.WorkerRunSummary `json:"runs"`
		Next     int64                  `json:"next_scheduled_at"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.WorkerID != worker.ID || payload.Runs.Truncated || payload.Runs.ScannedRuns != 106 || payload.Runs.DailyRuns != 105 || payload.Runs.DailySuccess != 103 || payload.Runs.DailyFailed != 1 || payload.Runs.ActiveRuns != 2 || len(payload.Runs.Active) != 2 || payload.Next != 0 || payload.Runs.DayEndAt-payload.Runs.DayStartAt != 23*int64(time.Hour/time.Millisecond) {
		t.Fatalf("incorrect summary: %+v", payload)
	}
	// A small history page must not affect daily or active summary.
	history := executeWorkerAPI(h, http.MethodGet, "/"+worker.ID+"/runs?limit=1", "", opts)
	if history.Code != http.StatusOK {
		t.Fatalf("history: %d %s", history.Code, history.Body.String())
	}
	var page struct {
		Runs       []store.WorkerRunRecord `json:"runs"`
		NextCursor string                  `json:"next_cursor"`
	}
	if err := json.Unmarshal(history.Body.Bytes(), &page); err != nil || len(page.Runs) != 1 || page.NextCursor == "" {
		t.Fatalf("expected paginated history: %v %+v", err, page)
	}
	for _, tc := range []struct {
		path   string
		opts   workerAPICallOptions
		status int
	}{
		{path, workerAPICallOptions{account: "acct-2", user: "user-2", scopes: []string{"automations:read"}}, http.StatusNotFound},
		{path, workerAPICallOptions{scopes: []string{"sessions:read"}}, http.StatusForbidden},
		{path, workerAPICallOptions{scopes: []string{"automations:trigger"}}, http.StatusForbidden},
		{"/"+worker.ID+"/summary?timezone=Bad%2FZone&date=2025-03-09", opts, http.StatusBadRequest},
		{"/"+worker.ID+"/summary?timezone=America%2FNew_York&date=2025-02-30", opts, http.StatusBadRequest},
		{path+"&date=2025-03-08", opts, http.StatusBadRequest},
		{path+"&unexpected=1", opts, http.StatusBadRequest},
		{path, workerAPICallOptions{agentOrigin: true, scopes: []string{"automations:read"}}, http.StatusForbidden},
	} {
		got := executeWorkerAPI(h, http.MethodGet, tc.path, "", tc.opts)
		if got.Code != tc.status {
			t.Errorf("%s expected %d got %d: %s", tc.path, tc.status, got.Code, got.Body.String())
		}
	}
	unauth := httptest.NewRecorder()
	h.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, WorkersPath+path, nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated summary: %d", unauth.Code)
	}
	repeat := executeWorkerAPI(h, http.MethodGet, path, "", opts)
	if repeat.Code != http.StatusOK || repeat.Body.String() != res.Body.String() {
		t.Fatalf("read changed durable state: %d %s", repeat.Code, repeat.Body.String())
	}
	stored, found, err := ws.GetWorker("acct-test", worker.ID)
	if err != nil || !found || stored.Revision != worker.Revision {
		t.Fatalf("read mutated worker: %+v %t %v", stored, found, err)
	}
	pageAfter, nextAfter, err := ws.ListWorkerRuns("acct-test", worker.ID, 100, "")
	if err != nil || len(pageAfter) != 100 || nextAfter == "" {
		t.Fatalf("read mutated receipts: %d %q %v", len(pageAfter), nextAfter, err)
	}
}

// Purpose: Daily receipt windows must include both repeated autumn local hours
// and exclude the following day. Boundary: WorkerStore.SummarizeWorkerRuns;
// the store layer is the narrowest layer proving the DST boundary calculation.
func TestWorkerRunSummaryFallBackDay(t *testing.T) {
	_, db, _ := setupWorkerAPITestServer(t)
	ws := store.NewWorkerStore(db)
	worker, err := ws.CreateWorker("acct-test", "user-test", store.CreateWorkerRequest{Name: "Fall back", IdempotencyKey: "fallback-create"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	zone, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	day := time.Date(2025, 11, 2, 0, 0, 0, 0, zone)
	for i, at := range []time.Time{day.Add(90 * time.Minute), day.Add(150 * time.Minute), day.AddDate(0, 0, 1)} {
		_, err := ws.RecordWorkerRun("acct-test", store.WorkerRunRecord{
			ID: fmt.Sprintf("fall-run-%d", i), AccountScopeID: "acct-test", UserID: "user-test", WorkerID: worker.ID,
			WorkerRevision: worker.Revision, RequestSource: "direct", Status: "succeeded", CreatedAt: at.UnixMilli(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	summary, err := ws.SummarizeWorkerRuns("acct-test", worker.ID, day, "America/New_York")
	if err != nil || summary.Truncated || summary.ScannedRuns != 3 || summary.DailyRuns != 2 || summary.DayEndAt-summary.DayStartAt != 25*int64(time.Hour/time.Millisecond) {
		t.Fatalf("fall-back summary: %+v %v", summary, err)
	}
}

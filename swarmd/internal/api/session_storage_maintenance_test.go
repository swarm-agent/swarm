package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/stream"
)

func TestSessionStorageMaintenanceIsLocalOnlyAndDryRunIsPayloadSafe(t *testing.T) {
	store, err := pebblestore.Open(filepath.Join(t.TempDir(), "maintenance.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	events, err := pebblestore.NewEventLog(store)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(store), events)
	server := NewServer(nil, nil, nil, nil, sessions, nil, nil, nil, nil, nil, nil, events, stream.NewHub(events))
	defer server.CancelInFlightRuns()

	body := []byte(`{"mode":"dry_run","batch_records":1}`)
	networkRequest := httptest.NewRequest(http.MethodPost, SessionStorageMaintenancePath, bytes.NewReader(body))
	networkResponse := httptest.NewRecorder()
	server.Handler().ServeHTTP(networkResponse, withTestPrincipal(networkRequest))
	if networkResponse.Code != http.StatusNotFound {
		t.Fatalf("network maintenance status=%d body=%s", networkResponse.Code, networkResponse.Body.String())
	}

	localRequest := httptest.NewRequest(http.MethodPost, SessionStorageMaintenancePath, bytes.NewReader(body))
	localResponse := httptest.NewRecorder()
	server.LocalTransportHandler().ServeHTTP(localResponse, localRequest)
	if localResponse.Code != http.StatusOK {
		t.Fatalf("local maintenance status=%d body=%s", localResponse.Code, localResponse.Body.String())
	}
	var report pebblestore.SessionStorageMaintenanceReport
	if err := json.Unmarshal(localResponse.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Mode != "dry_run" || report.PhysicalCompaction.Performed || !report.PhysicalCompaction.RequiresExplicitOperatorAction || report.Retention.BatchRecords != 1 {
		t.Fatalf("maintenance report=%+v", report)
	}
}

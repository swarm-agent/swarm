package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"swarm/packages/swarmd/internal/identity"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

func archiveScenarioRequest(t *testing.T, s *Server, p identity.Principal, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(method, path, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
	ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &pebblestore.ScopedTokenRecord{AccountScopeID: p.AccountScopeID, UserID: p.UserID, Scopes: []string{"sessions:read", "sessions:write"}})
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r.WithContext(ctx))
	return w
}

// Purpose: handleSessionsV3PrimaryArchiveBatch must return the durable archive
// version/projection without a follow-up list/hydrate. Real routed HTTP + Pebble
// is the narrowest layer proving receipts, authorization and atomic rejection;
// no provider or live user sessions are used.
func TestProjectSessionArchiveReceipt(t *testing.T) {
	s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
	p := testPrincipal()
	if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		w := archiveScenarioRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": fmt.Sprintf("receipt-%d", i)})
		if w.Code != http.StatusOK {
			t.Fatal(w.Body.String())
		}
	}
	items, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("sessions: %v %v", items, err)
	}
	ids := []string{items[0].ID, items[1].ID}
	other := p
	other.AccountScopeID = "other-account"
	w := archiveScenarioRequest(t, s, other, http.MethodPost, "/v3/sessions:archive", map[string]any{"session_ids": ids})
	if w.Code == http.StatusOK {
		t.Fatal("cross-account archive succeeded")
	}
	w = archiveScenarioRequest(t, s, p, http.MethodPost, "/v3/sessions:archive", map[string]any{"session_ids": []string{ids[0], "missing"}})
	if w.Code == http.StatusOK {
		t.Fatal("mixed unauthorized batch succeeded")
	}
	for _, id := range ids {
		if _, found, err := sessions.GetSession(id); err != nil || !found {
			t.Fatalf("rejected batch changed %s: %v", id, err)
		}
		if _, found, err := sessions.Store().GetV3SessionTombstone(id); err != nil || found {
			t.Fatalf("rejected batch tombstone: %v", err)
		}
	}
	w = archiveScenarioRequest(t, s, p, http.MethodPost, "/v3/sessions:archive", map[string]any{"session_ids": ids})
	var response struct {
		Results []struct {
			SessionID  string                          `json:"session_id"`
			Tombstone  pebblestore.V3SessionTombstone  `json:"tombstone"`
			Projection pebblestore.V3SessionProjection `json:"projection"`
		} `json:"results"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || w.Code != http.StatusOK || len(response.Results) != 2 {
		t.Fatalf("receipt: %d %s %v", w.Code, w.Body.String(), err)
	}
	versions := map[string]int64{}
	for _, result := range response.Results {
		stored, found, err := sessions.Store().GetV3SessionTombstone(result.SessionID)
		if err != nil || !found || stored.UpdatedAt == 0 || stored.UpdatedAt != result.Tombstone.UpdatedAt || stored.EventSeq != result.Projection.LastEventSeq {
			t.Fatalf("receipt not durable: %+v %+v %v", result, stored, err)
		}
		if stored.Session.ID != result.SessionID || stored.Deleted {
			t.Fatal("archive lost retained history")
		}
		versions[result.SessionID] = result.Tombstone.UpdatedAt
	}
	w = archiveScenarioRequest(t, s, p, http.MethodPost, "/v3/sessions:unarchive", map[string]any{"session_ids": ids, "expected_updated_at_by_id": versions})
	if w.Code != http.StatusOK {
		t.Fatalf("restore from exact receipt: %d %s", w.Code, w.Body.String())
	}
}

// Purpose: exercise the sidebar's former serial request schedule and new batch
// schedule against registered HTTP handlers and disposable real Pebble sessions.
// Logs are local deterministic request-path timings, NOT live UI/provider latency.
// The same scenario can run against an earlier handler via Go's source overlay.
func TestProjectSessionArchiveRequestScenario(t *testing.T) {
	for _, count := range []int{1, 12} {
		for _, batch := range []bool{false, true} {
			t.Run(fmt.Sprintf("sessions=%d/batch=%t", count, batch), func(t *testing.T) {
				s, sessions, _, _, _ := newRoutedSessionTestServerWithSwarmStore(t)
				p := testPrincipal()
				if err := sessions.Store().PutProject(p.AccountScopeID, &pebblestore.ProjectRecord{ID: "project", Name: "Project"}); err != nil {
					t.Fatal(err)
				}
				for i := 0; i < count; i++ {
					w := archiveScenarioRequest(t, s, p, http.MethodPost, ProjectsPath+"/project/sessions", map[string]any{"client_request_id": fmt.Sprintf("scenario-%d", i)})
					if w.Code != http.StatusOK {
						t.Fatal(w.Body.String())
					}
				}
				items, err := sessions.Store().ListProjectConversations(p.AccountScopeID, p.UserID, "project", count)
				if err != nil || len(items) != count {
					t.Fatalf("seed: %d %v", len(items), err)
				}
				ids := sessionIDsFromSnapshots(items)
				requests := 0
				start := time.Now()
				for offset := 0; offset < len(ids); {
					end := offset + 1
					if batch {
						end = len(ids)
					}
					w := archiveScenarioRequest(t, s, p, http.MethodPost, "/v3/sessions:archive", map[string]any{"session_ids": ids[offset:end]})
					requests++
					if w.Code != http.StatusOK {
						t.Fatal(w.Body.String())
					}
					offset = end
				}
				// The previous sidebar always refreshed both membership lists and
				// rehydrated every loaded row in groups of eight after archiving.
				if !batch {
					for _, suffix := range []string{"?limit=200", "?limit=200&archived_mode=only"} {
						w := archiveScenarioRequest(t, s, p, http.MethodGet, ProjectsPath+"/project/sessions"+suffix, nil)
						requests++
						if w.Code != http.StatusOK {
							t.Fatal(w.Body.String())
						}
					}
					for offset := 0; offset < len(ids); offset += 8 {
						end := min(offset+8, len(ids))
						w := archiveScenarioRequest(t, s, p, http.MethodPost, V3SyncHydratePath, map[string]any{
							"surface": "desktop", "session_ids": ids[offset:end], "history": map[string]any{"mode": "none"},
							"resources": map[string]any{"messages": false, "events": false, "run_intents": false, "current_run_state": true, "session_view": true, "active_plan": true, "plan_revisions": false, "permission_summaries": true}, "include_active": true,
						})
						requests++
						if w.Code != http.StatusOK {
							t.Fatal(w.Body.String())
						}
					}
				}
				elapsed := time.Since(start)
				for _, id := range ids {
					if _, found, err := sessions.Store().GetV3SessionTombstone(id); err != nil || !found {
						t.Fatalf("missing archive: %v", err)
					}
				}
				t.Logf("real HTTP-handler/Pebble scenario: sessions=%d batch=%t total_requests=%d elapsed=%s", count, batch, requests, elapsed)
			})
		}
	}
}

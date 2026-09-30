package api

import (
 "encoding/json"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"

 pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: canonical outbox projection selection must reach relevant resource
// subscriptions only, including replay through a fresh Server, without changing
// billing. Owners: ApplyV3SessionMutation, usageScopesForRealtimeSubscriptions,
// usageScopeSubscriptionMatches. Store-backed in-process selection is the
// narrowest transport-independent delivery boundary; no socket/daemon is started.
func TestUsageScopeDurableDeliverySelection(t *testing.T) {
 server, _, db := newWorkspaceOverviewTopologyTestServer(t)
 store := pebblestore.NewSessionStore(db)
 p := testPrincipal()
 id := "usage-delivery"
 _, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID:id, UserID:p.UserID, AccountScopeID:p.AccountScopeID, Kind:pebblestore.V3SessionMutationCreateSession, IdempotencyKey:"create", PayloadHash:"create", Session:&pebblestore.SessionSnapshot{ID:id, WorkspacePath:"/workspace", Metadata:map[string]any{"project_id":"project", "task_id":"task"}}, NowUnixMs:1000})
 if err != nil { t.Fatal(err) }
 if err := db.PutJSON(pebblestore.KeyProjectTask(p.AccountScopeID, "project", "task"), pebblestore.ProjectTaskRecord{ID:"task", ProjectID:"project", AccountID:p.AccountScopeID, SessionID:id}); err != nil { t.Fatal(err) }
 turn := pebblestore.SessionTurnUsageSnapshot{RunID:"receipt", TotalTokens:50, PriceStatus:"free"}
 result, err := store.ApplyV3SessionMutation(pebblestore.V3SessionMutationInput{SessionID:id, UserID:p.UserID, AccountScopeID:p.AccountScopeID, Kind:pebblestore.V3SessionMutationRecordUsage, IdempotencyKey:"usage", PayloadHash:"usage", TurnUsage:&turn, NowUnixMs:2000})
 if err != nil { t.Fatal(err) }
 // Read the actual durable record, not a fabricated frame.
 var durable pebblestore.V3RealtimeOutboxRecord
 found, err := db.GetJSON(pebblestore.KeyV3RealtimeOutbox(result.RealtimeOutbox.EndpointSeq), &durable)
 if err != nil || !found { t.Fatalf("durable outbox: %v", err) }
 worksets := map[string]v3RealtimeWorksetSubscription{"resource":{Resources:[]string{"projects"}, Selector:V3RealtimeWorksetSelector{SessionIDs:[]string{id}}}}
 scopes := server.usageScopesForRealtimeSubscriptions(p, durable, nil, worksets)
 if len(scopes) != 1 || scopes[0].TotalTokens != 50 { t.Fatalf("delivery: %+v", scopes) }
 cursorScope := testV3SyncCursorScope()
 cursor, err := server.signV3SyncEndpointCursor(cursorScope, durable.EndpointSeq)
 if err != nil { t.Fatal(err) }
 seq, legacy, err := server.parseV3SyncEndpointCursor(cursor, cursorScope)
 if err != nil || legacy || seq != durable.EndpointSeq { t.Fatalf("resume cursor: %d %v",seq,err) }
 wrongScope := cursorScope; wrongScope.AccountScopeID = "foreign"
 if _,_,err := server.parseV3SyncEndpointCursor(cursor,wrongScope); err == nil { t.Fatal("foreign cursor scope accepted") }
 wrongScope = cursorScope; wrongScope.SelectorFilterHash = v3SyncDeterministicSelectorHash("unrelated")
 if _,_,err := server.parseV3SyncEndpointCursor(cursor,wrongScope); err == nil { t.Fatal("unrelated selector cursor accepted") }
 fresh := &Server{sessions:server.sessions}
 if got := fresh.usageScopesForRealtimeSubscriptions(p, durable, nil, worksets); len(got) != 1 || got[0] != scopes[0] { t.Fatalf("replay: %+v", got) }
 total, _, _ := store.GetUsageScope(p.AccountScopeID, "task", "project", "task")
 if total.ReceiptCount != 1 || total.TotalTokens != 50 { t.Fatal("replay inflated projection") }
 worksets["resource"] = v3RealtimeWorksetSubscription{Resources:[]string{"projects"}, Selector:V3RealtimeWorksetSelector{SessionIDs:[]string{"unrelated"}}}
 if len(server.usageScopesForRealtimeSubscriptions(p, durable, nil, worksets)) != 0 { t.Fatal("unrelated workset received usage") }
 foreign := p; foreign.AccountScopeID = "foreign"
 if len(server.usageScopesForRealtimeSubscriptions(foreign, durable, map[string]v3RealtimeSubscription{id:{SessionID:id}}, nil)) != 0 { t.Fatal("foreign account received usage") }
 foreign = p; foreign.UserID = "foreign"
 if len(server.usageScopesForRealtimeSubscriptions(foreign, durable, map[string]v3RealtimeSubscription{id:{SessionID:id}}, nil)) != 0 { t.Fatal("foreign user received usage") }
 payload, err := json.Marshal(map[string]any{"scope_totals":scopes}); if err != nil { t.Fatal(err) }
 if len(payload) == 0 { t.Fatal("missing bounded projection payload") }
}

// Purpose: repair HTTP ingress cannot accept AI/scoped credentials or caller
// account overrides. Owner handleUsageScopeRepair; direct handler execution is
// the narrowest authentication layer and rejects before accessing storage.
func TestUsageScopeRepairAuthority(t *testing.T) {
 server := &Server{}
 for _, scoped := range []bool{false,true} {
  r := httptest.NewRequest(http.MethodPost, "/v3/usage/scopes/repair", strings.NewReader(`{"limit":1}`))
  if scoped { r = requestWithScopedToken(requestWithTestPrincipal(r), &pebblestore.ScopedTokenRecord{}) }
  w := httptest.NewRecorder(); server.handleUsageScopeRepair(w,r)
  want := http.StatusUnauthorized; if scoped { want = http.StatusForbidden }
  if w.Code != want { t.Fatalf("authority: %d want %d",w.Code,want) }
 }
 r := requestWithTestPrincipal(httptest.NewRequest(http.MethodPost,"/v3/usage/scopes/repair",strings.NewReader(`{"limit":1,"account":"foreign"}`)))
 w := httptest.NewRecorder(); server.handleUsageScopeRepair(w,r)
 if w.Code != http.StatusBadRequest { t.Fatalf("caller account admitted: %d",w.Code) }
}

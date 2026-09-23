package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/webhook"
)

// Purpose: registered V2 routes must bind human review to the exact durable
// snapshot, never create a one-shot intent, and reject foreign/stale/malformed
// acceptance without partial state. A real Pebble service behind apiMux is the
// narrowest layer proving routing, decoding and transaction postconditions;
// listener credential verification remains a separate authentication test.
func TestAutomationV2RegisteredReviewAcceptance(t *testing.T) {
	for _, expiration := range []store.AutomationV2Expiration{{}, {Kind: "at", ExpiresAt: 4102444800000}} {
		t.Run(expiration.Kind, func(t *testing.T) {
			db, err := store.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			ss := store.NewSessionStore(db)
			identityStore := store.NewIdentityStore(db)
			if _, err := identityStore.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
				t.Fatal(err)
			}
			if _, err := identityStore.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
				t.Fatal(err)
			}
			if _, err := identityStore.PutAccountUser(store.AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			workspace, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
			if err != nil {
				t.Fatal(err)
			}
			workspaceID := workspace.WorkspaceID
			available := true
			if err := ss.CreateSession(store.SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: t.TempDir(), WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
				t.Fatal(err)
			}
			s := &Server{sessions: sessionruntime.NewService(ss, nil)}
			h := s.apiMux()
			call := func(method, path, body, user string, agent bool) *httptest.ResponseRecorder {
				r := httptest.NewRequest(method, AutomationsV2Path+path, strings.NewReader(body))
				if user != "" {
					p := identity.Principal{Type: "user", UserID: user, AccountScopeID: "account"}
					ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
					if agent {
						var err error
						ctx, err = automation.BindRuntimeIdentity(ctx, p, "agent", "child")
						if err != nil {
							t.Fatal(err)
						}
					}
					r = r.WithContext(ctx)
				}
				w := httptest.NewRecorder()
				h.ServeHTTP(w, r)
				return w
			}
			encode := func(v any) string {
				b, err := json.Marshal(v)
				if err != nil {
					t.Fatal(err)
				}
				return string(b)
			}
			doc := &store.SessionPlanDocument{Title: "Review", Info: store.SessionPlanInfo{Goal: "Work"}, AutomationV2: &store.AutomationV2Settings{SchemaVersion: 2, Schedule: store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60}, Missed: "skip", Overlap: "serialize", ActivateOnAccept: true, Expiration: expiration}, Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Title: "Work", Objective: "Implement", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Works"}}}}
			req := automationV2Request{Action: "propose_automation", WorkspaceID: workspaceID, SessionID: "conversation", Document: doc}
			// Incomplete instructions fail at the actual registered authoring boundary.
			invalid := *doc
			invalid.Checkpoints = nil
			badProposal := req
			badProposal.Document = &invalid
			if rejected := call(http.MethodPost, "/proposal", encode(badProposal), "owner", false); rejected.Code < 400 {
				t.Fatal("incomplete proposal accepted")
			}
			if _, found, err := ss.GetAutomationV2Proposal("account", "owner", workspaceID, "conversation"); err != nil || found {
				t.Fatal("incomplete proposal wrote state", err)
			}
			w := call(http.MethodPost, "/proposal", encode(req), "owner", false)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			var proposal struct {
				Proposal store.AutomationV2Proposal `json:"proposal"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
				t.Fatal(err)
			}
			old := proposal.Proposal.AutomationV2Review
			req.Review = old
			doc.Title = "Edited instructions"
			doc.AutomationV2.Schedule.IntervalSeconds = 120
			w = call(http.MethodPost, "/proposal", encode(req), "owner", false)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
				t.Fatal(err)
			}
			if proposal.Proposal.Revision != old.Revision+1 || proposal.Proposal.Digest == old.Digest {
				t.Fatal("review did not advance")
			}
			assertPending := func() {
				t.Helper()
				if _, found, err := ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation"); err != nil || found {
					t.Fatal("partial acceptance", found, err)
				}
				if _, found, err := ss.GetV3SessionActiveRunIntent("conversation"); err != nil || found {
					t.Fatal("unintended run", found, err)
				}
			}
			assertPending()
			accept := automationV2Request{Action: "accept_automation", WorkspaceID: workspaceID, SessionID: "conversation", Review: proposal.Proposal.AutomationV2Review}
			stale := accept
			stale.Review = old
			for _, tc := range []struct {
				body, user string
				agent      bool
			}{
				{encode(stale), "owner", false}, {encode(accept), "foreign", false}, {encode(accept), "", false}, {encode(accept), "owner", true},
				{encode(accept) + " {}", "owner", false}, {strings.TrimSuffix(encode(accept), "}") + `,"accepted_by":"forged"}`, "owner", false},
				{strings.Replace(encode(accept), "accept_automation", "approve", 1), "owner", false},
			} {
				w = call(http.MethodPost, "/accept", tc.body, tc.user, tc.agent)
				if w.Code < 400 {
					t.Fatal("invalid acceptance succeeded", w.Body.String())
				}
				assertPending()
			}
			restore := ss.SetAutomationV2CommitHookForTest(func(string) error { return errors.New("private injected failure") })
			w = call(http.MethodPost, "/accept", encode(accept), "owner", false)
			restore()
			if w.Code < 400 || strings.Contains(w.Body.String(), "private injected") {
				t.Fatal("unsafe failure response", w.Body.String())
			}
			assertPending()
			wake := publishCommittedV3RealtimeOutboxWake
			publishCommittedV3RealtimeOutboxWake = func(*v3RealtimeOutboxHub, sessionruntime.RealtimeOutboxRecord) error {
				return errors.New("delivery failed")
			}
			w = call(http.MethodPost, "/accept", encode(accept), "owner", false)
			publishCommittedV3RealtimeOutboxWake = wake
			if w.Code != http.StatusServiceUnavailable {
				t.Fatal("delivery failure not reported", w.Code)
			}
			if _, found, err := ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation"); err != nil || !found {
				t.Fatal("delivery failure lost commit", err)
			}
			var first store.AutomationV2Record
			for i := 0; i < 2; i++ {
				w = call(http.MethodPost, "/accept", encode(accept), "owner", false)
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body.String())
				}
				var result struct {
					Record store.AutomationV2Record `json:"record"`
				}
				if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
					t.Fatal(err)
				}
				if i == 0 {
					first = result.Record
				} else if !reflect.DeepEqual(first, result.Record) {
					t.Fatal("replay changed receipt")
				}
			}
			if !first.Enabled || first.AcceptedBy != "owner" || first.Document.Title != "Edited instructions" || first.Document.AutomationV2.Schedule.IntervalSeconds != 120 {
				t.Fatal("wrong accepted snapshot", first)
			}
			wantExpiration := expiration
			if wantExpiration.Kind == "" {
				wantExpiration.Kind = "indefinite"
			}
			if first.Authorization != wantExpiration {
				t.Fatal("expiration changed", first.Authorization)
			}
			plan, found, err := ss.GetPlan("conversation", first.ProposalID)
			if err != nil || !found || plan.ApprovalState != "approved" || !reflect.DeepEqual(*plan.Document, first.Document) {
				t.Fatal("accepted executable snapshot mismatch", err)
			}
			snapshot, found, err := ss.GetSession("conversation")
			if err != nil || !found || snapshot.AutomationV2 != nil || !first.Independent {
				t.Fatal("acceptance converted the authoring chat", err)
			}
			if _, found, err := ss.GetV3SessionActiveRunIntent("conversation"); err != nil || found {
				t.Fatal("acceptance started one-shot", err)
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&limit=1", "", "owner", false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), first.AutomationID) {
				t.Fatal("discovery missing accepted record", w.Body.String())
			}
			// Requirement: registered management routes are user-only CAS changes;
			// rejected principals/generations must leave the active policy intact.
			control := automationV2Request{Action: "pause", WorkspaceID: workspaceID, SessionID: "conversation", Generation: first.Generation}
			for _, denial := range []struct {
				user  string
				agent bool
			}{{"", false}, {"foreign", false}, {"owner", true}} {
				if got := call(http.MethodPost, "/control", encode(control), denial.user, denial.agent); got.Code < 400 {
					t.Fatal("control principal accepted")
				}
			}
			unchanged, _, _ := ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation")
			if !reflect.DeepEqual(unchanged, first) {
				t.Fatal("denied control changed state")
			}
			w = call(http.MethodPost, "/control", encode(control), "owner", false)
			if w.Code != 200 {
				t.Fatal(w.Code, w.Body.String())
			}
			paused, _, _ := ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation")
			if paused.Enabled || paused.Generation != first.Generation+1 {
				t.Fatal("pause missing")
			}
			if got := call(http.MethodPost, "/control", encode(control), "owner", false); got.Code != 409 {
				t.Fatal("stale control", got.Code)
			}
			w = call(http.MethodGet, "/progress?workspace_id="+workspaceID+"&session_id=conversation&timezone=UTC", "", "owner", false)
			var progress sessionruntime.AutomationV2Progress
			if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &progress) != nil || progress.NoNextReason != "paused" || progress.ForecastIsAdmission || len(progress.Forecast) != 0 {
				t.Fatal("progress", w.Code, w.Body.String())
			}
			// Existing automation UI edits reuse proposal/review/accept, not controls.
			req.Review = first.AutomationV2Review
			doc.Checkpoints[0].Objective = "Revised exact instruction"
			w = call(http.MethodPost, "/proposal", encode(req), "owner", false)
			if w.Code != 200 {
				t.Fatal("revision proposal", w.Body.String())
			}
			if err = json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
				t.Fatal(err)
			}
			unchanged, _, _ = ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation")
			if !reflect.DeepEqual(unchanged, paused) {
				t.Fatal("pending edit changed execution")
			}
			accept.Review = proposal.Proposal.AutomationV2Review
			w = call(http.MethodPost, "/accept", encode(accept), "owner", false)
			if w.Code != 200 {
				t.Fatal("revision acceptance", w.Body.String())
			}
			revised, _, _ := ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation")
			if revised.AutomationID != first.AutomationID || !revised.Enabled || revised.Document.Checkpoints[0].Objective != "Revised exact instruction" {
				t.Fatal("revision not applied")
			}

			// Archive remains a safety fence for the worker's authoring-session key.
			if err := ss.ArchiveSession("conversation"); err != nil {
				t.Fatal(err)
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&archived_mode=exclude", "", "owner", false)
			if w.Code != 200 || strings.Contains(w.Body.String(), first.AutomationID) {
				t.Fatal("archived worker remained in active list", w.Body.String())
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&archived_mode=only", "", "owner", false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), first.AutomationID) {
				t.Fatal("archived worker missing from history", w.Body.String())
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&archived_mode=invalid", "", "owner", false)
			if w.Code != 400 {
				t.Fatal("expected 400 for invalid archived_mode", w.Code)
			}

		})
	}
}

// Purpose: ControlAutomationV2 action "delete_automation" removes the accepted record
// and disables future executions.
func TestAutomationV2ControlDelete(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := store.NewSessionStore(db)
	identityStore := store.NewIdentityStore(db)
	if _, err := identityStore.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identityStore.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identityStore.PutAccountUser(store.AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := workspace.WorkspaceID
	available := true
	if err := ss.CreateSession(store.SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: t.TempDir(), WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}
	h := s.apiMux()
	call := func(method, path, body, user string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, AutomationsV2Path+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: user, AccountScopeID: "account"}
		r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	encode := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}

	doc := store.SessionPlanDocument{
		Title: "Delete Plan",
		Info:  store.SessionPlanInfo{Goal: "Delete Plan"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 60},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{{ID: "one", Title: "One", Status: "pending", Order: 1, Objective: "Return", AcceptanceCriteria: []string{"Done"}}},
	}
	propReq := automationV2Request{Action: "propose_automation", WorkspaceID: workspaceID, SessionID: "conversation", Document: &doc}
	w := call(http.MethodPost, "/proposal", encode(propReq), "owner")
	if w.Code != 200 {
		t.Fatalf("proposal failed: %d %s", w.Code, w.Body.String())
	}
	var propResp struct{ Proposal store.AutomationV2Proposal }
	if err := json.Unmarshal(w.Body.Bytes(), &propResp); err != nil {
		t.Fatal(err)
	}
	acceptReq := automationV2Request{Action: "accept_automation", WorkspaceID: workspaceID, SessionID: "conversation", Review: propResp.Proposal.AutomationV2Review}
	w = call(http.MethodPost, "/accept", encode(acceptReq), "owner")
	if w.Code != 200 {
		t.Fatalf("accept failed: %d %s", w.Code, w.Body.String())
	}
	var acceptResp struct{ Record store.AutomationV2Record }
	if err := json.Unmarshal(w.Body.Bytes(), &acceptResp); err != nil {
		t.Fatal(err)
	}

	// Now delete automation via /control
	delReq := map[string]any{
		"action":       "delete_automation",
		"workspace_id": workspaceID,
		"session_id":   "conversation",
		"generation":   acceptResp.Record.Generation,
	}
	w = call(http.MethodPost, "/control", encode(delReq), "owner")
	if w.Code != 200 {
		t.Fatalf("delete_automation failed: %d %s", w.Code, w.Body.String())
	}
	// Verify record is gone
	_, found, err := ss.GetAutomationV2Record("account", "owner", workspaceID, "conversation")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatal("expected automation record to be removed after delete_automation")
	}
}

// Purpose: DeclineAutomationV2 must delete the pending proposal, resolve the
// pending permission to denied, reject subsequent acceptance, and emit a
// realtime outbox event.
func TestAutomationV2RegisteredReviewDecline(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := store.NewSessionStore(db)
	identityStore := store.NewIdentityStore(db)
	if _, err := identityStore.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identityStore.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identityStore.PutAccountUser(store.AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := workspace.WorkspaceID
	available := true
	if err := ss.CreateSession(store.SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: t.TempDir(), WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}
	h := s.apiMux()
	call := func(method, path, body, user string, agent bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, AutomationsV2Path+path, strings.NewReader(body))
		if user != "" {
			p := identity.Principal{Type: "user", UserID: user, AccountScopeID: "account"}
			ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
			if agent {
				var err error
				ctx, err = automation.BindRuntimeIdentity(ctx, p, "agent", "child")
				if err != nil {
					t.Fatal(err)
				}
			}
			r = r.WithContext(ctx)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	encode := func(v any) string {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	doc := &store.SessionPlanDocument{
		Title: "To Decline",
		Info:  store.SessionPlanInfo{Goal: "Work"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "interval", IntervalSeconds: 300},
			Missed:           "skip",
			Overlap:          "serialize",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{{ID: "cp-1", Title: "Task", Objective: "Run", Status: "pending", Order: 1, AcceptanceCriteria: []string{"Done"}}},
	}
	req := automationV2Request{Action: "propose_automation", WorkspaceID: workspaceID, SessionID: "conversation", Document: doc}
	w := call(http.MethodPost, "/proposal", encode(req), "owner", false)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var proposal struct {
		Proposal store.AutomationV2Proposal `json:"proposal"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &proposal); err != nil {
		t.Fatal(err)
	}

	// Verify proposal exists
	if _, found, err := ss.GetAutomationV2Proposal("account", "owner", workspaceID, "conversation"); err != nil || !found {
		t.Fatal("proposal missing", found, err)
	}
	ps := store.NewPermissionStore(db)
	perm, found, err := ps.GetPermission("conversation", store.AutomationV2PermissionID(proposal.Proposal.ProposalID))
	if err != nil || !found || perm.Status != store.PermissionStatusPending {
		t.Fatal("permission missing or not pending", found, perm.Status, err)
	}

	decline := automationV2Request{
		Action:      "decline_automation",
		WorkspaceID: workspaceID,
		SessionID:   "conversation",
		Review:      proposal.Proposal.AutomationV2Review,
	}

	// Reject decline by unauthorized / foreign user or agent
	for _, tc := range []struct {
		user  string
		agent bool
	}{
		{"", false},
		{"foreign", false},
		{"owner", true},
	} {
		w = call(http.MethodPost, "/decline", encode(decline), tc.user, tc.agent)
		if w.Code < 400 {
			t.Fatalf("unauthorized decline succeeded for user=%q agent=%v", tc.user, tc.agent)
		}
	}

	// Valid decline call
	w = call(http.MethodPost, "/decline", encode(decline), "owner", false)
	if w.Code != 200 {
		t.Fatalf("decline failed: code=%d body=%s", w.Code, w.Body.String())
	}

	// Verify proposal is gone
	if _, found, err := ss.GetAutomationV2Proposal("account", "owner", workspaceID, "conversation"); err != nil || found {
		t.Fatal("proposal still found after decline", found, err)
	}

	// Verify GET /review returns 404
	w = call(http.MethodGet, "/review?workspace_id="+workspaceID+"&session_id=conversation", "", "owner", false)
	if w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for declined review, got %d", w.Code)
	}

	// Verify permission status is denied
	perm, found, err = ps.GetPermission("conversation", store.AutomationV2PermissionID(proposal.Proposal.ProposalID))
	if err != nil || !found || perm.Status != store.PermissionStatusDenied || perm.Decision != "decline_automation" {
		t.Fatalf("permission not denied: found=%v status=%s decision=%s", found, perm.Status, perm.Decision)
	}

	// Verify accept fails after decline
	accept := automationV2Request{
		Action:      "accept_automation",
		WorkspaceID: workspaceID,
		SessionID:   "conversation",
		Review:      proposal.Proposal.AutomationV2Review,
	}
	w = call(http.MethodPost, "/accept", encode(accept), "owner", false)
	if w.Code < 400 {
		t.Fatal("accept succeeded on declined proposal")
	}
}

func TestAutomationV2TriggerEndpoint(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ss := store.NewSessionStore(db)
	identityStore := store.NewIdentityStore(db)
	if _, err := identityStore.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identityStore.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err := identityStore.PutAccountUser(store.AccountUserRecord{ID: "membership", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	workspace, err := store.NewWorkspaceStore(db).AddForAccount("account", t.TempDir(), "Workspace")
	if err != nil {
		t.Fatal(err)
	}
	workspaceID := workspace.WorkspaceID
	available := true
	if err := ss.CreateSession(store.SessionSnapshot{ID: "conversation", AccountScopeID: "account", UserID: "owner", WorkspacePath: t.TempDir(), WorkspaceGrants: []store.WorkspaceGrant{{Kind: store.WorkspaceGrantPrimary, WorkspaceID: workspaceID, Path: workspace.Path, Available: &available}}}); err != nil {
		t.Fatal(err)
	}
	s := &Server{sessions: sessionruntime.NewService(ss, nil)}
	h := s.apiMux()
	call := func(method, path, body, user string, isAgent bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, AutomationsV2Path+path, strings.NewReader(body))
		pType := "user"
		if isAgent {
			pType = "agent"
		}
		p := identity.Principal{Type: pType, UserID: user, AccountScopeID: "account"}
		r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, p))
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	encode := func(v any) string {
		b, _ := json.Marshal(v)
		return string(b)
	}

	doc := store.SessionPlanDocument{
		Title: "Trigger Test Plan",
		Info:  store.SessionPlanInfo{Goal: "Trigger Test Plan"},
		AutomationV2: &store.AutomationV2Settings{
			SchemaVersion:    2,
			Schedule:         store.AutomationV2Schedule{Kind: "trigger"},
			Missed:           "skip",
			Overlap:          "independent",
			ActivateOnAccept: true,
			Expiration:       store.AutomationV2Expiration{Kind: "indefinite"},
		},
		Checkpoints: []store.SessionPlanCheckpoint{
			{ID: "cp-1", Title: "Job", Status: "pending", Order: 1, Tasks: []string{"Task 1"}, AcceptanceCriteria: []string{"Done"}},
		},
	}
	propReq := automationV2Request{Action: "propose_automation", WorkspaceID: workspaceID, SessionID: "conversation", Document: &doc}
	w := call(http.MethodPost, "/proposal", encode(propReq), "owner", false)
	if w.Code != 200 {
		t.Fatalf("proposal failed: %d %s", w.Code, w.Body.String())
	}
	var propResp struct{ Proposal store.AutomationV2Proposal }
	if err := json.Unmarshal(w.Body.Bytes(), &propResp); err != nil {
		t.Fatal(err)
	}
	acceptReq := automationV2Request{Action: "accept_automation", WorkspaceID: workspaceID, SessionID: "conversation", Review: propResp.Proposal.AutomationV2Review}
	w = call(http.MethodPost, "/accept", encode(acceptReq), "owner", false)
	if w.Code != 200 {
		t.Fatalf("accept failed: %d %s", w.Code, w.Body.String())
	}
	var acceptResp struct{ Record store.AutomationV2Record }
	if err := json.Unmarshal(w.Body.Bytes(), &acceptResp); err != nil {
		t.Fatal(err)
	}
	acceptedRecord := acceptResp.Record

	// Trigger endpoint call
	triggerReq := automationV2TriggerRequest{
		WorkspaceID:  workspaceID,
		AutomationID: acceptedRecord.AutomationID,
		Context: map[string]any{
			"trigger_reason": "ci_failure",
			"error_log":      "exit status 1",
		},
	}

	// 1. Unauthorized caller fails
	w = call(http.MethodPost, "/trigger", encode(triggerReq), "intruder", false)
	if w.Code < 400 {
		t.Fatalf("expected error for unauthorized caller, got %d", w.Code)
	}

	// 2. Authorized caller succeeds
	w = call(http.MethodPost, "/trigger", encode(triggerReq), "owner", false)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for trigger, got %d: %s", w.Code, w.Body.String())
	}

	var resp struct {
		OK         bool                         `json:"ok"`
		Occurrence store.AutomationV2Occurrence `json:"occurrence"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response failed: %v", err)
	}
	if !resp.OK {
		t.Fatal("expected ok=true in trigger response")
	}
	if resp.Occurrence.State != "admitted" {
		t.Fatalf("expected occurrence state 'admitted', got %q", resp.Occurrence.State)
	}
	if resp.Occurrence.TriggerContext == nil || resp.Occurrence.TriggerContext["trigger_reason"] != "ci_failure" {
		t.Fatalf("expected trigger context preserved, got %+v", resp.Occurrence.TriggerContext)
	}

	// Verify occurrence can be read in progress API
	w = call(http.MethodGet, "/progress?workspace_id="+workspaceID+"&automation_id="+acceptedRecord.AutomationID+"&timezone=UTC", "", "owner", false)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for progress, got %d: %s", w.Code, w.Body.String())
	}
	var prog sessionruntime.AutomationV2Progress
	if err := json.Unmarshal(w.Body.Bytes(), &prog); err != nil {
		t.Fatalf("unmarshal progress failed: %v", err)
	}
	if len(prog.Occurrences) != 1 {
		t.Fatalf("expected 1 occurrence in progress, got %d", len(prog.Occurrences))
	}
	if prog.Occurrences[0].ID != resp.Occurrence.ID {
		t.Fatalf("expected occurrence ID match, got %s vs %s", prog.Occurrences[0].ID, resp.Occurrence.ID)
	}
}

func TestAutomationV2WebhooksAPI(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	ids := store.NewIdentityStore(db)
	if _, err = ids.PutUser(store.UserRecord{ID: "owner", Username: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountScope(store.AccountScopeRecord{ID: "account", Type: store.AccountScopeTypePersonal, CreatedByUserID: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = ids.PutAccountUser(store.AccountUserRecord{ID: "member", AccountScopeID: "account", UserID: "owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}

	dispatcher := webhook.NewDispatcher(nil)
	defer dispatcher.Close()

	ss := store.NewSessionStore(db)
	s := &Server{
		sessions:          sessionruntime.NewService(ss, nil),
		webhookDispatcher: dispatcher,
	}
	h := s.apiMux()

	call := func(method, path, body string, scopes []string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, AutomationsV2Path+path, strings.NewReader(body))
		p := identity.Principal{Type: "user", UserID: "owner", AccountScopeID: "account"}
		ctx := context.WithValue(r.Context(), productPrincipalRequestContextKey, p)
		if scopes != nil {
			ctx = context.WithValue(ctx, productScopedTokenRequestContextKey, &store.ScopedTokenRecord{Scopes: scopes})
		}
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}

	// 1. GET /webhooks without automations:read scope -> 403
	w := call(http.MethodGet, "/webhooks", "", []string{"sessions:read"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing automations:read scope, got %d", w.Code)
	}

	// 2. GET /webhooks with automations:read -> 200 empty
	w = call(http.MethodGet, "/webhooks", "", []string{"automations:read"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var listResp struct {
		OK       bool                              `json:"ok"`
		Webhooks []store.AutomationV2GlobalWebhook `json:"webhooks"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil || !listResp.OK || len(listResp.Webhooks) != 0 {
		t.Fatalf("unexpected list response: %s", w.Body.String())
	}

	// 3. Mock HTTP webhook receiver
	var receivedHeaders http.Header
	var receivedBody []byte
	testServer := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		receivedHeaders = req.Header.Clone()
		receivedBody, _ = io.ReadAll(req.Body)
		rw.WriteHeader(http.StatusOK)
		_, _ = rw.Write([]byte(`{"received": true}`))
	}))
	defer testServer.Close()

	// 4. POST /webhooks without automations:write -> 403
	createBody := fmt.Sprintf(`{"url":%q,"secret":"my-secret-key","format":"generic","events":["started","succeeded"],"enabled":true}`, testServer.URL)
	w = call(http.MethodPost, "/webhooks", createBody, []string{"automations:read"})
	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for missing automations:write scope, got %d", w.Code)
	}

	// 5. POST /webhooks with automations:write -> 200
	w = call(http.MethodPost, "/webhooks", createBody, []string{"automations:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", w.Code, w.Body.String())
	}
	var createResp struct {
		OK      bool                            `json:"ok"`
		Webhook store.AutomationV2GlobalWebhook `json:"webhook"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createResp); err != nil || !createResp.OK || createResp.Webhook.ID == "" {
		t.Fatalf("unexpected create response: %s", w.Body.String())
	}
	webhookID := createResp.Webhook.ID

	// 6. POST /webhooks/test to trigger test ping
	testBody := fmt.Sprintf(`{"id":%q}`, webhookID)
	w = call(http.MethodPost, "/webhooks/test", testBody, []string{"automations:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for test ping, got %d: %s", w.Code, w.Body.String())
	}
	var testResp struct {
		OK     bool                   `json:"ok"`
		Result webhook.DeliveryResult `json:"result"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &testResp); err != nil || !testResp.OK {
		t.Fatalf("unexpected test response: %s", w.Body.String())
	}
	if testResp.Result.StatusCode != http.StatusOK {
		t.Fatalf("expected test delivery 200, got %d", testResp.Result.StatusCode)
	}

	// Verify headers and signature on receiver
	if len(receivedBody) == 0 {
		t.Fatal("expected non-empty received body on webhook receiver")
	}
	if receivedHeaders.Get("X-Swarm-Event") != webhook.EventTestPing {
		t.Fatalf("expected X-Swarm-Event %s, got %s", webhook.EventTestPing, receivedHeaders.Get("X-Swarm-Event"))
	}
	if receivedHeaders.Get("X-Swarm-Signature") == "" {
		t.Fatal("expected non-empty X-Swarm-Signature header")
	}

	// 7. DELETE /webhooks/{id}
	w = call(http.MethodDelete, "/webhooks/"+webhookID, "", []string{"automations:write"})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK for delete, got %d: %s", w.Code, w.Body.String())
	}

	// 8. Verify GET /webhooks is empty again
	w = call(http.MethodGet, "/webhooks", "", []string{"automations:read"})
	if err := json.Unmarshal(w.Body.Bytes(), &listResp); err != nil || len(listResp.Webhooks) != 0 {
		t.Fatalf("expected 0 webhooks after delete, got %d", len(listResp.Webhooks))
	}
}

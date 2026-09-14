package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	sessionruntime "swarm/packages/swarmd/internal/session"
	store "swarm/packages/swarmd/internal/store/pebble"
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
			if err != nil || !found || snapshot.AutomationV2 == nil || snapshot.AutomationV2.AutomationID != first.AutomationID || snapshot.AutomationV2.Digest != first.Digest {
				t.Fatal("accepted binding mismatch", err)
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

			// Test archived_mode discovery
			if err := ss.ArchiveSession("conversation"); err != nil {
				t.Fatal(err)
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&archived_mode=exclude", "", "owner", false)
			if w.Code != 200 || strings.Contains(w.Body.String(), first.AutomationID) {
				t.Fatal("expected archived automation excluded", w.Body.String())
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&archived_mode=only", "", "owner", false)
			if w.Code != 200 || !strings.Contains(w.Body.String(), first.AutomationID) {
				t.Fatal("expected archived automation returned with archived_mode=only", w.Body.String())
			}
			w = call(http.MethodGet, "?workspace_id="+workspaceID+"&archived_mode=invalid", "", "owner", false)
			if w.Code != 400 {
				t.Fatal("expected 400 for invalid archived_mode", w.Code)
			}

		})
	}
}

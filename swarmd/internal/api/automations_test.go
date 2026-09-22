package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/automation"
	"swarm/packages/swarmd/internal/identity"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type automationAPIAccess struct{}

func (automationAPIAccess) Workspace(_ context.Context, p automation.Principal, s store.AutomationScope, _ string) error {
	if p.AccountID != "account" || s.WorkspaceID != "workspace" {
		return automation.ErrDenied
	}
	return nil
}
func (automationAPIAccess) PlanSession(context.Context, automation.Principal, store.AutomationScope, string) error {
	return nil
}
func (automationAPIAccess) Execution(context.Context, automation.Principal, store.AutomationScope, store.AutomationDefinition, string) error {
	return nil
}
func (automationAPIAccess) OccurrenceSession(context.Context, automation.Principal, store.AutomationScope, string) error {
	return automation.ErrDenied
}

type automationAPIPlans struct{}

func (automationAPIPlans) GetPlanRevision(string, string, int) (store.SessionPlanSnapshot, bool, error) {
	return store.SessionPlanSnapshot{ID: "plan", SessionID: "session", AccountScopeID: "account", Version: 1, ApprovalState: "approved", Document: &store.SessionPlanDocument{}}, true, nil
}

type automationAPIRuntime struct{}

func (automationAPIRuntime) Ensure(context.Context, automation.Principal, store.AutomationRecord, store.AutomationRecord) (string, error) {
	panic("HTTP must not dispatch")
}

type automationAPITriggers struct{}

func (automationAPITriggers) Verify(context.Context, automation.Principal, store.AutomationScope, automation.Trigger) error {
	return nil
}

type automationAPIEvents struct{}

func (automationAPIEvents) VerifyEvent(r *http.Request, _ automation.Principal, _ store.AutomationScope, _ string, _ uint64, _ automation.Trigger) error {
	if r.Header.Get("X-Test-Credential") != "valid" {
		return automation.ErrDenied
	}
	return nil
}

// Purpose: the retired V1 HTTP entrypoint must reject every execution mutation,
// even when a legacy execution service is configured. A real store proves that
// retries, foreign callers and event intake create no definitions or occurrences.
func TestAutomationHTTPAuthorityAndReplay(t *testing.T) {
	repo, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	domain, err := automation.New(repo, automationAPIPlans{}, automationAPIAccess{}, func() time.Time { return time.UnixMilli(100000) })
	if err != nil {
		t.Fatal(err)
	}
	execution, err := automation.NewExecutionService(domain, automationAPIRuntime{}, automationAPITriggers{})
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.ConfigureAutomations(domain, execution, nil, automationAPIEvents{})
	call := func(body, account, credential string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, AutomationsPath, strings.NewReader(body))
		if account != "" {
			r = r.WithContext(context.WithValue(r.Context(), productPrincipalRequestContextKey, identity.Principal{Type: "user", UserID: "human", AccountScopeID: account}))
		}
		r.Header.Set("X-Test-Credential", credential)
		w := httptest.NewRecorder()
		s.handleAutomations(w, r)
		return w
	}
	request := automationHTTPRequest{Action: "save", WorkspaceID: "workspace", ID: "check", MutationID: "create", Definition: &store.AutomationDefinition{Name: "Check", Enabled: true, Plans: []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: "session", PlanID: "plan", Revision: 1}}}, Schedule: store.AutomationSchedulePolicy{Kind: "event", TriggerSource: "ci"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approved_policy", ApprovalReference: "approval", ExpiresAt: 200000}}}
	encoded, _ := json.Marshal(request)
	body := string(encoded)
	for _, account := range []string{"", "foreign"} {
		w := call(body, account, "")
		if w.Code != 401 && w.Code != http.StatusGone {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	if w := call(body, "account", ""); w.Code != http.StatusGone {
		t.Fatal(w.Code, w.Body.String())
	}
	if w := call(body, "account", ""); w.Code != http.StatusGone {
		t.Fatal("replay", w.Code, w.Body.String())
	}
	request.MutationID = "stale"
	request.Definition.Name = "Wrong"
	encoded, _ = json.Marshal(request)
	if w := call(string(encoded), "account", ""); w.Code != http.StatusGone {
		t.Fatal("stale", w.Code, w.Body.String())
	}
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	head, found, err := repo.GetAutomationRecord(scope, "check", "definition", "check", 0)
	if err != nil || found {
		t.Fatal("partial write", head, err)
	}
	// V1 event intake remains retired even with formerly valid credentials.
	event := `{"action":"event","workspace_id":"workspace","id":"check","mutation_id":"delivery","expected_revision":1,"source":"ci","identity":"event-1","scheduled_at":100000}`
	if w := call(event, "account", "forged"); w.Code != http.StatusGone {
		t.Fatal(w.Code)
	}
	rows, _, err := repo.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "occurrence", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("forged event wrote", rows, err)
	}
	for i := 0; i < 2; i++ {
		if w := call(event, "account", "valid"); w.Code != http.StatusGone {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	rows, _, err = repo.SearchAutomationRecords(store.AutomationSearch{Scope: scope, Kind: "occurrence", Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("replay duplicated/dispatched", rows, err)
	}
	if w := call(strings.Replace(event, `"identity":"event-1"`, `"identity":"event-1","account_id":"foreign"`, 1), "account", "valid"); w.Code != http.StatusGone {
		t.Fatal("forged envelope", w.Code)
	}
}

// Purpose: strict bounded JSON parsing must reject trailing documents, unknown
// attribution and oversized requests before any service operation.
func TestAutomationHTTPDecodeBounds(t *testing.T) {
	for _, body := range []string{`{} {}`, `{"role":"system"}`, `{"action":"` + strings.Repeat("x", 65536) + `"}`} {
		r := httptest.NewRequest(http.MethodPost, AutomationsPath, strings.NewReader(body))
		if err := decodeAutomationRequest(httptest.NewRecorder(), r, new(automationHTTPRequest)); err == nil {
			t.Fatal("accepted invalid envelope")
		}
	}
}

// Purpose: the HTTP identity adapter must bind the verified user, never upgrade
// an existing agent/system origin, and leave storage untouched on rejection.
// This exercises the real PolicyApproval identity boundary through HTTP.
func TestAutomationHTTPRuntimeOrigin(t *testing.T) {
	repo, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer repo.Close()
	now := func() time.Time { return time.UnixMilli(100000) }
	policy, err := automation.NewPolicyApproval(repo, automationAPIPlans{}, automationAPIAccess{}, automation.RuntimeApprovalIdentity(), now)
	if err != nil {
		t.Fatal(err)
	}
	domain, err := automation.New(repo, automationAPIPlans{}, policy, now)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	s.ConfigureAutomationApproval(policy)
	s.ConfigureAutomations(domain, nil, nil, nil)
	verified := identity.Principal{Type: "user", UserID: "human", AccountScopeID: "account"}
	for _, origin := range []string{"user", "agent", "system"} {
		ctx := context.WithValue(context.Background(), productPrincipalRequestContextKey, verified)
		if origin != "user" {
			sessionID := ""
			if origin == "agent" {
				sessionID = "execution"
			}
			ctx, err = automation.BindRuntimeIdentity(ctx, verified, origin, sessionID)
			if err != nil {
				t.Fatal(err)
			}
		}
		r := httptest.NewRequest(http.MethodGet, AutomationsPath+"?workspace_id=workspace&action=list", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		s.handleAutomations(w, r)
		want := http.StatusForbidden
		if origin == "user" {
			want = http.StatusOK
		}
		if w.Code != want {
			t.Fatalf("%s: %d %s", origin, w.Code, w.Body.String())
		}
		if origin != "user" {
			r = httptest.NewRequest(http.MethodPost, AutomationsPath+"/approve", strings.NewReader(`{"workspace_id":"workspace","id":"check","mutation_id":"approve","expected_revision":1,"policy_sha256":"forged"}`)).WithContext(ctx)
			w = httptest.NewRecorder()
			s.handleAutomations(w, r)
			if w.Code != http.StatusGone {
				t.Fatal("retired origin", w.Code)
			}
		}
	}
	rows, _, err := repo.SearchAutomationRecords(store.AutomationSearch{Scope: store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}, Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatal("unexpected write", rows, err)
	}
}

// Purpose: retired V1 approval must preserve preexisting legacy definitions and
// never produce a grant or enable proposal, even for their exact current digest.
func TestAutomationHTTPApprovalEnableProposal(t *testing.T) {
	db, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	clock := func() time.Time { return time.UnixMilli(100000) }
	policy, err := automation.NewPolicyApproval(db, automationAPIPlans{}, automationAPIAccess{}, automation.RuntimeApprovalIdentity(), clock)
	if err != nil {
		t.Fatal(err)
	}
	domain, err := automation.New(db, automationAPIPlans{}, policy, clock)
	if err != nil {
		t.Fatal(err)
	}
	principal := identity.Principal{Type: "user", UserID: "human", AccountScopeID: "account"}
	ctx, _ := automation.BindRuntimeIdentity(context.Background(), principal, "user", "")
	p, _ := automation.RuntimePrincipal(ctx)
	scope := store.AutomationScope{AccountID: "account", WorkspaceID: "workspace"}
	d := store.AutomationDefinition{Name: "check", Plans: []store.AutomationPlanBinding{{ID: "primary", Plan: store.AutomationPlanReference{SessionID: "session", PlanID: "plan", Revision: 1}}}, Schedule: store.AutomationSchedulePolicy{Kind: "manual"}, Authorization: store.AutomationAuthorizationPolicy{Mode: "approval_required", ExpiresAt: 200000}}
	record, _, err := domain.SaveDefinition(ctx, p, scope, "auto", "create", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	digest, _ := automation.ApprovalPolicyDigest(*record.Definition)
	s := &Server{}
	s.ConfigureAutomations(domain, nil, nil, nil)
	s.ConfigureAutomationApproval(policy)
	call := func(hash string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(automationHTTPRequest{WorkspaceID: "workspace", ID: "auto", MutationID: "accept", ExpectedRevision: record.Revision, PolicySHA256: hash})
		req := httptest.NewRequest(http.MethodPost, AutomationsPath+"/approve", strings.NewReader(string(data)))
		req = req.WithContext(context.WithValue(req.Context(), productPrincipalRequestContextKey, principal))
		w := httptest.NewRecorder()
		s.handleAutomations(w, req)
		return w
	}
	if w := call("stale"); w.Code != http.StatusGone {
		t.Fatalf("stale policy: %d", w.Code)
	}
	w := call(digest)
	if w.Code != http.StatusGone || strings.Contains(w.Body.String(), "enable_proposal") {
		t.Fatalf("retired approval: %d %s", w.Code, w.Body.String())
	}
	head, _, _ := db.GetAutomationRecord(scope, "auto", "definition", "auto", 0)
	if head.Revision != record.Revision || head.Definition.Enabled {
		t.Fatal("approval enabled definition")
	}
}

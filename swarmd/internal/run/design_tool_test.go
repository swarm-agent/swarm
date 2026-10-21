package run

import (
	"context"
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"swarm/packages/swarmd/internal/design"
	"swarm/packages/swarmd/internal/identity"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// designToolChecker is a design.Checker that passes every page and counts calls.
type designToolChecker struct {
	mu    sync.Mutex
	calls int
}

func (c *designToolChecker) Check(context.Context, []byte) design.CheckReport {
	c.mu.Lock()
	c.calls++
	c.mu.Unlock()
	return design.CheckReport{Status: design.CheckOK}
}

type designToolOutput struct {
	Mode        string         `json:"mode"`
	Folder      string         `json:"folder"`
	ResultsPath string         `json:"results_path"`
	EditPath    string         `json:"edit_path"`
	Counts      map[string]int `json:"counts"`
	Items       []struct {
		N       int    `json:"n"`
		Path    string `json:"path"`
		Status  string `json:"status"`
		Finding string `json:"finding"`
	} `json:"items"`
	Totals design.Totals `json:"totals"`
}

func designToolWorkspace(t *testing.T, f designModelFixture, sessionID string) string {
	t.Helper()
	session, ok, err := f.service.sessions.GetSession(sessionID)
	if err != nil || !ok {
		t.Fatalf("session %s: ok=%v err=%v", sessionID, ok, err)
	}
	return session.WorkspacePath
}

func designToolCall(t *testing.T, f designModelFixture, sessionID, callID, args string) (designToolOutput, error) {
	t.Helper()
	ctx := identity.ContextWithPrincipal(context.Background(), f.principal)
	raw, err := f.service.executeSwarmDesignTool(ctx, sessionID, tool.Call{CallID: callID, Name: tool.SwarmDesignToolName, Arguments: args})
	var out designToolOutput
	if err == nil {
		if decodeErr := json.Unmarshal([]byte(raw), &out); decodeErr != nil {
			t.Fatalf("decode swarm_design result %q: %v", raw, decodeErr)
		}
	}
	return out, err
}

// Requirement: in-session swarm_design runs the design pipeline inside the
// calling run over the calling session's workspace, writes each design and
// results.json under <folder>/<batch>-<id>/, edits one existing file in place,
// uses the account's Designer model, and records provider-reported usage on
// the calling session (Reported=false and no receipt when nothing is
// reported). Threat: designs land outside the session workspace, usage is
// charged to another session or synthesized, or the compact result disagrees
// with the files. Owners: Service.executeSwarmDesignTool, Service.DesignModel,
// design.Pipeline and tool.DesignFiles. Real session, catalog, settings and
// usage stores with a fake provider runner and a fake browser checker are the
// narrowest layer that sees the files, the provider requests and the receipts
// together without a live provider or browser.
func TestSwarmDesignToolWritesDesignsAndRecordsUsageOnCallingSession(t *testing.T) {
	f := newDesignModelFixture(t)
	checker := &designToolChecker{}
	f.service.SetDesignChecker(checker)
	f.runner.usage = provideriface.TokenUsage{InputTokens: 1000, OutputTokens: 500, TotalTokens: 1500, Source: "fixture"}
	root := designToolWorkspace(t, f, f.sessionID)

	out, err := designToolCall(t, f, f.sessionID, "call_create", `{"brief":"A pricing page","count":2,"batch_name":"Pricing Pages"}`)
	if err != nil {
		t.Fatal(err)
	}
	if out.Mode != "create" || !strings.HasPrefix(out.Folder, "designs/pricing-pages-") || out.ResultsPath != path.Join(out.Folder, design.ResultsFile) {
		t.Fatalf("unexpected folder/results: %+v", out)
	}
	if out.Counts["passed"] != 2 || len(out.Items) != 2 || out.Totals.Calls != 2 || out.Totals.Tokens != 3000 || !out.Totals.CostComplete {
		t.Fatalf("unexpected result: %+v", out)
	}
	for i, item := range out.Items {
		want := path.Join(out.Folder, []string{"01.html", "02.html"}[i])
		if item.Path != want || item.Status != "passed" || item.Finding != "" {
			t.Fatalf("item %d = %+v, want %s passed", i, item, want)
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(item.Path)))
		if err != nil || !strings.Contains(string(data), "<body>ok</body>") {
			t.Fatalf("design %s not written: %q %v", item.Path, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(out.ResultsPath))); err != nil {
		t.Fatalf("results.json missing: %v", err)
	}
	if checker.calls != 2 || len(f.runner.requests) != 2 {
		t.Fatalf("checks=%d model calls=%d, want 2 each (no repairs)", checker.calls, len(f.runner.requests))
	}
	for _, req := range f.runner.requests {
		if req.SessionID != f.sessionID || req.Model != "designer-model" || req.Tools != nil || req.ToolChoice != "none" {
			t.Fatalf("design call not on the calling session's Designer model: %#v", req)
		}
	}
	// The job id is derived from the call id plus the batch folder suffix.
	jobID := "swarm_design-call_create-" + out.Folder[len(out.Folder)-6:]
	for n := 1; n <= 2; n++ {
		receipt, found, err := f.store.GetTurnUsage(f.sessionID, "design:"+jobID+":"+string(rune('0'+n))+":generate")
		if err != nil || !found || receipt.InputTokens != 1000 || receipt.Model != "designer-model" {
			t.Fatalf("receipt %d on calling session: %+v found=%v err=%v", n, receipt, found, err)
		}
	}

	// Edit mode changes the one existing file in place; a provider that
	// reports no usage leaves no receipt and an incomplete cost.
	f.runner.mu.Lock()
	f.runner.text = `{"edits":[{"old":"<body>ok</body>","new":"<body>edited</body>"}]}`
	f.runner.usage = provideriface.TokenUsage{}
	f.runner.mu.Unlock()
	target := out.Items[0].Path
	edited, err := designToolCall(t, f, f.sessionID, "call_edit", `{"brief":"Say edited","edit_path":"`+target+`"}`)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Mode != "edit" || edited.EditPath != target || len(edited.Items) != 1 || edited.Items[0].Status != "passed" || edited.Totals.CostComplete || edited.Totals.Tokens != 0 {
		t.Fatalf("unexpected edit result: %+v", edited)
	}
	if data, _ := os.ReadFile(filepath.Join(root, filepath.FromSlash(target))); !strings.Contains(string(data), "<body>edited</body>") {
		t.Fatalf("edit not applied: %q", data)
	}
	usage, err := f.store.ListTurnUsage(f.sessionID, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range usage {
		if strings.Contains(u.RunID, "call_edit") {
			t.Fatalf("unreported usage produced a receipt: %+v", u)
		}
	}
}

// Requirement: swarm_design refuses, before any model call or file write, a
// session with a managed execution binding (its files would not reach the
// managed checkout), a Designer child writing outside its declared
// owned_scope, invalid arguments, and a daemon without a design checker.
// Threat: designs written where the caller cannot see them or where another
// Designer owns the folder, or spending on a request that cannot succeed.
// Owners: Service.executeSwarmDesignTool and parseSwarmDesignToolArgs; the
// same store-backed fixture proves nothing reached the provider or the disk.
func TestSwarmDesignToolRefusesBeforeSpending(t *testing.T) {
	f := newDesignModelFixture(t)
	f.service.SetDesignChecker(&designToolChecker{})
	pref := pebblestore.ModelPreference{Provider: "fixture-provider", Model: "designer-model", Thinking: "high"}
	newSession := func(id string, metadata map[string]any) string {
		t.Helper()
		session, _, err := f.service.sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: id, UserID: f.principal.UserID, AccountScopeID: f.principal.AccountScopeID, WorkspacePath: t.TempDir(), Preference: &pref, Metadata: metadata})
		if err != nil {
			t.Fatal(err)
		}
		if got, _, _ := f.service.sessions.GetSession(session.ID); len(metadata) > 0 && got.Metadata == nil {
			t.Fatalf("session %s lost its metadata", id)
		}
		return session.ID
	}
	managed := newSession("session-design-managed", map[string]any{tool.ManagedExecutionMetadataKey: map[string]any{"workspace_id": "ws", "lease_id": "lease"}})
	designer := newSession("session-design-child", map[string]any{"requested_subagent": "designer", "owned_scope": []any{"designs/landing"}})

	cases := []struct {
		name, session, args, want string
	}{
		{"managed binding", managed, `{"brief":"a page"}`, "managed execution binding"},
		{"outside owned scope", designer, `{"brief":"a page","folder":"other"}`, "outside this Designer's owned_scope"},
		{"edit outside owned scope", designer, `{"brief":"a page","edit_path":"other/page.html"}`, "outside this Designer's owned_scope"},
		{"traversal", f.sessionID, `{"brief":"a page","folder":"../escape"}`, "relative path inside the workspace"},
		{"edit with count", f.sessionID, `{"brief":"a page","edit_path":"page.html","count":2}`, "count must be 1"},
		{"unknown field", f.sessionID, `{"brief":"a page","model":"other"}`, "unknown field"},
		{"missing brief", f.sessionID, `{"count":1}`, "brief is required"},
	}
	for _, tc := range cases {
		_, err := designToolCall(t, f, tc.session, "call_"+strings.ReplaceAll(tc.name, " ", "_"), tc.args)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
	// Inside the owned scope the Designer child may write.
	if _, err := designToolCall(t, f, designer, "call_inside", `{"brief":"a page","folder":"designs/landing"}`); err != nil {
		t.Fatalf("inside owned scope: %v", err)
	}
	if len(f.runner.requests) != 1 {
		t.Fatalf("refused calls reached the provider: %d requests", len(f.runner.requests))
	}
	for _, id := range []string{managed, f.sessionID} {
		entries, err := os.ReadDir(designToolWorkspace(t, f, id))
		if err != nil || len(entries) != 0 {
			t.Fatalf("a refused call wrote into %s: %v %v", id, entries, err)
		}
	}
	// No checker configured: refused, never a pass.
	f.service.SetDesignChecker(nil)
	if _, err := designToolCall(t, f, f.sessionID, "call_nochecker", `{"brief":"a page"}`); err == nil || !strings.Contains(err.Error(), "no design checker") {
		t.Fatalf("missing checker err = %v", err)
	}
}

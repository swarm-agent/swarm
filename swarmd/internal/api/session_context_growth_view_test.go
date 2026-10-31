package api

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: the per-session context-growth view (each turn's first-request
// input+cache_read and its request count) and the session's request_count
// are readable where clients read a session: the session detail route
// GET /v3/sessions/{id} (which the GPUI reads) and MCP manage_sessions get
// (controlMCPGetSession over that route). Threat: the view existing only on
// the usage subroute, which neither the GPUI nor the MCP allowlist reads, so
// a replaying session stays invisible (a dead end for the outside AI).
// Boundary: the detail handler in handleSessionsV3Primary
// (Server.sessionContextGrowth) and controlMCPSessionStateView. Narrowest
// layer: the MCP tool dispatching through the real route allowlist and the
// daemon's internal route handler over a real temporary store; no provider
// runs. Negative: a session with no usage carries no usage or growth in the
// MCP view and an empty growth list on the route.
func TestSessionDetailAndGetSessionCarryContextGrowth(t *testing.T) {
	f := newFleetTestSwarm(t, "mcp")
	t.Cleanup(f.cleanup)
	fleetActivityTestSession(t, f, "growing-session")
	fleetActivityTestSession(t, f, "quiet-session")
	turns := []struct {
		run      string
		at       int64
		requests []pebblestore.SessionUsageRequestReceipt
	}{
		{"run-1", 1000, []pebblestore.SessionUsageRequestReceipt{
			{AccountID: "slot-a", InputTokens: 10, CacheReadTokens: 1000, OutputTokens: 50},
			{AccountID: "slot-a", InputTokens: 5, CacheReadTokens: 1200, OutputTokens: 40},
			{AccountID: "slot-a", InputTokens: 5, CacheReadTokens: 1400, OutputTokens: 30},
		}},
		{"run-2", 2000, []pebblestore.SessionUsageRequestReceipt{
			{AccountID: "slot-b", InputTokens: 10, CacheReadTokens: 3000, OutputTokens: 20},
			{AccountID: "slot-b", InputTokens: 5, CacheReadTokens: 3200, OutputTokens: 10},
		}},
	}
	for _, turn := range turns {
		record := pebblestore.SessionTurnUsageSnapshot{RunID: turn.run, Provider: "claudecode", Model: "claude-test", Source: "claudecode_cli_usage", ContextWindow: 200000, Steps: 1, CreatedAt: turn.at}
		pebblestore.ApplyUsageRequestReceipts(&record, turn.requests)
		if _, _, _, err := f.s.sessions.RecordTurnUsage("growing-session", record); err != nil {
			t.Fatal(err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/mcp", nil)
	request = requestWithTestPrincipalForAccount(request, f.user, f.account)
	request = requestWithScopedToken(request, &pebblestore.ScopedTokenRecord{Scopes: []string{"swarm:read", "sessions:read"}})
	call := &controlMCPCall{request: request, next: f.s.internalRouteHandler(), server: f.s}

	wantGrowth := func(label string, growth []any) {
		t.Helper()
		if len(growth) != 2 {
			t.Fatalf("%s context_growth: %v", label, growth)
		}
		for i, want := range []struct {
			run      string
			context  float64
			requests float64
		}{{"run-1", 1010, 3}, {"run-2", 3010, 2}} {
			turn := controlMCPMap(growth[i])
			if turn["run_id"] != want.run || turn["first_request_context_tokens"] != want.context || turn["request_count"] != want.requests {
				t.Fatalf("%s turn %d: %v", label, i, turn)
			}
		}
	}

	detail, err := call.dispatch(http.MethodGet, controlMCPSessionPath("growing-session"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	wantGrowth("detail route", controlMCPList(detail["context_growth"]))
	if summary := controlMCPMap(detail["usage_summary"]); summary["request_count"] != float64(5) {
		t.Fatalf("detail usage_summary: %v", summary)
	}

	result, err := controlMCPGetSession(call, map[string]any{"session_id": "growing-session"})
	if err != nil {
		t.Fatal(err)
	}
	view := controlMCPMap(result)
	usage, _ := view["usage"].(map[string]any)
	if usage["request_count"] != float64(5) || usage["turn_count"] != float64(2) {
		t.Fatalf("get_session usage: %v", view["usage"])
	}
	growth, _ := view["context_growth"].([]any)
	wantGrowth("get_session", growth)
	if _, cut := view["context_growth_omitted"]; cut {
		t.Fatalf("two turns were cut: %v", view["context_growth_omitted"])
	}

	quietDetail, err := call.dispatch(http.MethodGet, controlMCPSessionPath("quiet-session"), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if growth, present := quietDetail["context_growth"]; !present || len(controlMCPList(growth)) != 0 {
		t.Fatalf("quiet detail context_growth: present=%v %v", present, growth)
	}
	quiet, err := controlMCPGetSession(call, map[string]any{"session_id": "quiet-session"})
	if err != nil {
		t.Fatal(err)
	}
	if _, has := controlMCPMap(quiet)["usage"]; has {
		t.Fatalf("quiet session has usage: %v", controlMCPMap(quiet)["usage"])
	}
	if _, has := controlMCPMap(quiet)["context_growth"]; has {
		t.Fatalf("quiet session has context growth: %v", controlMCPMap(quiet)["context_growth"])
	}
}

// Purpose: get_session's context_growth stays bounded: it keeps the newest
// controlMCPContextGrowthLimit turns, oldest first, and reports how many it
// left out. Threat: a long session's growth list flooding the MCP answer, or
// a cut list hiding that it was cut. Boundary: controlMCPSessionStateView,
// a pure view builder and the narrowest layer that owns the bound.
func TestGetSessionContextGrowthIsBounded(t *testing.T) {
	growth := make([]any, 0, controlMCPContextGrowthLimit+3)
	for i := 0; i < controlMCPContextGrowthLimit+3; i++ {
		growth = append(growth, map[string]any{"run_id": fmt.Sprintf("run-%d", i), "request_count": float64(1)})
	}
	view := controlMCPSessionStateView(map[string]any{"context_growth": growth}, 3000, 3000, nil)
	kept, _ := view["context_growth"].([]any)
	if len(kept) != controlMCPContextGrowthLimit || view["context_growth_omitted"] != 3 {
		t.Fatalf("kept %d omitted %v", len(kept), view["context_growth_omitted"])
	}
	if first := controlMCPMap(kept[0]); first["run_id"] != "run-3" {
		t.Fatalf("oldest kept turn: %v", first)
	}
	if last := controlMCPMap(kept[len(kept)-1]); last["run_id"] != fmt.Sprintf("run-%d", controlMCPContextGrowthLimit+2) {
		t.Fatalf("newest kept turn: %v", last)
	}
}

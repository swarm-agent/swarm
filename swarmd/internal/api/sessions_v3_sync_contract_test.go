package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestV3DurableSyncContractStatesHardInvariant(t *testing.T) {
	if V3SyncContractVersion != 1 {
		t.Fatalf("V3SyncContractVersion = %d, want 1", V3SyncContractVersion)
	}
	contract := V3SyncContractText
	for _, required := range []string{
		"Pebble session/projection state plus the Pebble realtime outbox",
		"in-memory realtime hub and websocket pushes are delivery accelerators only",
		"signed, scoped, versioned, opaque cursor through endpointSeq=N",
		"replay or stream after the returned cursor cannot miss any committed mutation",
		"either included in the snapshot or appears during replay after the returned cursor",
		"Stable cursor and snapshot scope includes account, principal/user, surface/client kind, stream kind, selector/filter hash, and resource set",
		"signed cursor additionally binds afterEndpointSeq as the scan position inside that stable scope",
		"Durable replay repair is required both for connected clients that miss a hub publish and for disconnected clients that reconnect",
		"Reconnect-only repair is not sufficient",
		"selector/filter hash must be derived from deterministic canonical scope serialization",
		"must not widen account/principal scope",
		"targeted hydrate cannot return unauthorized sessions",
		"Clients must not parse cursor numbers",
		"session membership/order",
		"active/inactive session state",
		"tombstones/archive/delete/hidden state",
		"run intents",
		"messages, events, resources, plans",
		"endpointSeq is a global durable outbox scan watermark",
		"Per-session event Seq remains the contiguous session event invariant",
		"one primary writer process per database",
	} {
		if !strings.Contains(contract, required) {
			t.Fatalf("durable sync contract missing required clause %q", required)
		}
	}
}

func TestV3SyncScopeAndCursorContractFieldsAreFrozen(t *testing.T) {
	assertExactStrings(t, "V3SyncScopeFields", V3SyncScopeFields, []string{
		"account",
		"principal",
		"surface",
		"stream_kind",
		"selector_filter_hash",
		"resource_set",
	})
	assertExactStrings(t, "V3SyncCursorPayloadFields", V3SyncCursorPayloadFields, []string{
		"version",
		"kind",
		"account",
		"principal",
		"surface",
		"stream_kind",
		"selector_filter_hash",
		"resource_set",
		"after_endpoint_seq",
		"issued_at",
		"kid",
	})
	assertExactStrings(t, "V3SyncRequiredResponseSemantics", V3SyncRequiredResponseSemantics, []string{
		"canonical_projections",
		"session_membership_order",
		"active_inactive_sessions",
		"tombstones_archive_delete_hidden",
		"run_intents_current_and_requested",
		"messages",
		"events",
		"resources",
		"plans",
		"replay_instructions",
	})
}

func TestV3SyncEndpointNamesAndLegacyWorksetGatesAreFrozen(t *testing.T) {
	for name, route := range map[string]string{
		"bootstrap": V3SyncBootstrapPath,
		"hydrate":   V3SyncHydratePath,
		"stream":    V3SyncStreamPath,
	} {
		if !strings.HasPrefix(route, "/v3/sync/") {
			t.Fatalf("%s sync route = %q, want /v3/sync/*", name, route)
		}
	}
	if V3SyncLegacySessionsWorksetPath != "/v3/sessions:workset" {
		t.Fatalf("legacy sessions workset path = %q", V3SyncLegacySessionsWorksetPath)
	}
	if V3SyncLegacyTUIWorksetPath != "/v3/tui/sessions:workset" {
		t.Fatalf("legacy TUI workset path = %q", V3SyncLegacyTUIWorksetPath)
	}
	assertExactStrings(t, "V3SyncWorksetRemovalGates", V3SyncWorksetRemovalGates, []string{
		"canonical_bootstrap_hydrate_stream_parity",
		"desktop_production_callers_removed",
		"tui_production_callers_removed",
		"static_guards_pass",
		"runtime_logs_show_no_production_workset_requests",
		"signed_scoped_cursor_tests_pass",
		"snapshot_consistency_tests_pass",
		"durable_outbox_repair_tests_pass",
		"fireworks_cross_client_e2e_passes",
	})
}

func TestV3SyncLegacyWorksetRoutesRemainRegisteredUntilGatesPass(t *testing.T) {
	body, err := os.ReadFile("server_routes.go")
	if err != nil {
		t.Fatalf("read server_routes.go: %v", err)
	}
	source := string(body)
	for _, required := range []string{
		`mux.HandleFunc(V3SyncLegacySessionsWorksetPath, s.handleSessionsV3Workset)`,
		`mux.HandleFunc(V3SyncLegacyTUIWorksetPath, s.handleSessionsV3TUIWorkset)`,
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("server routes must keep legacy workset route registered through contract constants until gates pass; missing %s", required)
		}
	}
}

func TestV3SyncContractSourceGuardRejectsPrematureWorksetRemoval(t *testing.T) {
	for _, path := range []string{"sessions_v3_workset.go", "sessions_v3_sync_contract.go"} {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		source := string(body)
		for _, required := range []string{"workset", "V3SyncWorksetRemovalGates"} {
			if !strings.Contains(source, required) {
				t.Fatalf("%s missing legacy workset gate marker %q", path, required)
			}
		}
	}
}

func TestV3SyncStaticGuardsCoverDesktopAndTUIProductionCallers(t *testing.T) {
	desktopGuard := filepath.Join("..", "..", "..", "web", "src", "features", "desktop", "chat", "queries", "session-hydrate-api.spec.ts")
	assertFileContains(t, desktopGuard, []string{
		"/v3/sessions:workset",
		"routine workset callers must not request",
		"../../state/desktop-state-snapshot.ts",
		"../../state/desktop-ui-store.ts",
		"../../state/desktop-v3-session-api.ts",
		"../../layout/desktop-app-page.tsx",
		"findUnboundedFullHistoryWorksetRequests",
	})
	tuiGuard := filepath.Join("..", "..", "..", "internal", "app", "tui_v3_contract_test.go")
	assertFileContains(t, tuiGuard, []string{
		"/v3/tui/sessions:workset",
		"durable sync endpoints must stay under /v3/sync",
		`assertSourceDoesNotContain(t, "app.go"`,
		`assertSourceDoesNotContain(t, "chat_backend_adapter.go"`,
	})
	clientGuard := filepath.Join("..", "..", "..", "internal", "client", "session_v3_test.go")
	assertFileContains(t, clientGuard, []string{
		"TestSessionV3TUIWorksetClientUsesTUIRouteAndScope",
		"/v3/tui/sessions:workset",
		"GetSessionV3TUIWorkset",
	})
}

func assertExactStrings(t *testing.T, name string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s length = %d, want %d: %#v", name, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s[%d] = %q, want %q (all=%#v)", name, i, got[i], want[i], got)
		}
	}
}

func assertFileContains(t *testing.T, path string, required []string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	source := string(body)
	for _, needle := range required {
		if !strings.Contains(source, needle) {
			t.Fatalf("%s missing required static guard marker %q", path, needle)
		}
	}
}

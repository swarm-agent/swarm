package api

// Durable V3 sync contract.
//
// This file is intentionally executable code, not a standalone design note, so
// package tests can freeze the words that protect the cross-client invariant.
// The contract is sync-first: realtime delivery is an accelerator, while durable
// replay from Pebble is the correctness path for Desktop, TUI, browser refresh,
// reconnect, and mobile/API-equivalent clients.
const V3SyncContractVersion = 1

const (
	// V3SyncBootstrapPath is the future canonical scoped snapshot endpoint. It
	// must return a durable snapshot plus a signed cursor for that exact scope.
	V3SyncBootstrapPath = "/v3/sync/bootstrap"
	// V3SyncHydratePath is the future canonical targeted hydrate endpoint for
	// session IDs and known per-session sequence state.
	V3SyncHydratePath = "/v3/sync/hydrate"
	// V3SyncStreamPath is the future canonical durable stream/replay endpoint.
	V3SyncStreamPath = "/v3/sync/stream"

	// V3SyncLegacySessionsWorksetPath and V3SyncLegacyTUIWorksetPath are legacy
	// hydration/listing routes. They are removal-gated by V3SyncWorksetRemovalGates
	// and must not be removed or hard-blocked until replacement sync API parity is
	// proven for production Desktop and TUI callers.
	V3SyncLegacySessionsWorksetPath = "/v3/sessions:workset"
	V3SyncLegacyTUIWorksetPath      = "/v3/tui/sessions:workset"
)

// V3SyncContractText is the authoritative CP-0 durable sync contract. Tests
// assert the presence of its key clauses so future route or client changes do
// not drift back to hub-first or workset-only semantics.
const V3SyncContractText = `
V3 durable sync invariant

Durable source of truth:
- The only correctness source for V3 cross-client sync is Pebble session/projection state plus the Pebble realtime outbox.
- The in-memory realtime hub and websocket pushes are delivery accelerators only. A committed mutation is correct when the Pebble mutation and durable outbox row commit; clients repair missed hub delivery by durable replay.
- Durable replay repair is required both for connected clients that miss a hub publish and for disconnected clients that reconnect. Reconnect-only repair is not sufficient.

Scoped snapshot contract:
- Bootstrap or hydrate returns a canonical snapshot for one exact sync scope and a signed, scoped, versioned, opaque cursor through endpointSeq=N.
- For that exact scope, replay or stream after the returned cursor cannot miss any committed mutation that should affect the scoped view.
- A mutation racing with bootstrap/hydrate is either included in the snapshot or appears during replay after the returned cursor; it is never lost between snapshot and replay.

Sync scope identity:
- Stable cursor and snapshot scope includes account, principal/user, surface/client kind, stream kind, selector/filter hash, and resource set. The signed cursor additionally binds afterEndpointSeq as the scan position inside that stable scope.
- selector/filter hash must be derived from deterministic canonical scope serialization so semantically identical scopes produce the same hash and different scopes do not share cursors.
- Bootstrap, hydrate, replay, and stream must not widen account/principal scope; targeted hydrate cannot return unauthorized sessions even with a syntactically valid cursor.
- Clients persist opaque cursors per exact scope. Clients must not parse cursor numbers, compare cursor numbers, or reuse a cursor across scopes.

Response semantics:
- Canonical sync responses include session projections, session membership/order, active/inactive session state, tombstones/archive/delete/hidden state, current and historical run intents as requested, messages, events, resources, plans, and replay instructions.
- endpointSeq is a global durable outbox scan watermark. Delivered filtered frames may skip endpointSeq values. Per-session event Seq remains the contiguous session event invariant.

Deployment assumption:
- V3 Pebble sync assumes one primary writer process per database unless a future leader, lease, or consensus design explicitly replaces that assumption.

Legacy workset gate:
- Legacy /v3/sessions:workset and /v3/tui/sessions:workset remain available until canonical bootstrap, hydrate, and stream APIs have parity for production callers, static guards prove no production workset callers remain, runtime/testbench logs show no production workset requests, signed scoped cursor tests pass, snapshot consistency tests pass, durable outbox repair tests pass, and live Fireworks-backed cross-client E2E passes.
`

// V3SyncScopeFields freezes the stable fields that define a sync scope. Cursor
// position advances inside this stable scope; it does not redefine the scope.
var V3SyncScopeFields = []string{
	"account",
	"principal",
	"surface",
	"stream_kind",
	"selector_filter_hash",
	"resource_set",
}

// V3SyncCursorPayloadFields freezes the minimum signed cursor payload. CP-2 will
// implement signing and validation against this scoped payload.
var V3SyncCursorPayloadFields = []string{
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
}

// V3SyncRequiredResponseSemantics freezes the resources that bootstrap/hydrate
// responses must represent before legacy workset routes can be retired.
var V3SyncRequiredResponseSemantics = []string{
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
}

// V3SyncWorksetRemovalGates are all required before removing or hard-blocking
// legacy workset routes.
var V3SyncWorksetRemovalGates = []string{
	"canonical_bootstrap_hydrate_stream_parity",
	"desktop_production_callers_removed",
	"tui_production_callers_removed",
	"static_guards_pass",
	"runtime_logs_show_no_production_workset_requests",
	"signed_scoped_cursor_tests_pass",
	"snapshot_consistency_tests_pass",
	"durable_outbox_repair_tests_pass",
	"fireworks_cross_client_e2e_passes",
}

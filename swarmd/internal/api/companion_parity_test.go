package api

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// companionJSONFields lists the JSON names a struct type emits, nested
// structs (other than the listed DTOs, which are checked on their own) folded in.
func companionJSONFields(t reflect.Type) []string {
	var out []string
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if name == "" || name == "-" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// Purpose: Companion behavior is one contract across its surfaces, and its
// coverage boundaries are explicit. Requirement: every JSON field the
// Companion routes answer or accept (CompanionView, its routes and activity,
// queued input and task-watch rows, context bank records, sources, read stats
// and the record request) is typed in the SDK (packages/sdk/src/companion.ts),
// which calls exactly the two Companion routes; docs/frontends.md documents
// both routes and the activity fields; both routes are registered; the
// lifecycle route has a recorded no-MCP decision (MCP reaches the Companion's
// conversation as an ordinary session) while the context bank route is
// recorded as reached by the supported manage_companion catalog tool (never
// both); and the native task action the Companion contract tells the model to
// use (watch_tasks) is a real manage_projects action. Threat: a field added on
// the daemon but missing from the SDK or docs, an SDK route the daemon does
// not serve, a silent MCP gap, or a Companion contract naming a tool action
// that does not exist. Boundary: CompanionView/CompanionActivity and the
// context DTOs, server_routes.go registration, mcpRouteParityNoMCPAction,
// mcpRouteParityReached, companionInstructions, tool.ProjectTaskWatchAction.
// Narrowest layer: the
// types and checked-in files themselves (no SDK compiler exists in this test
// environment, so the TypeScript is checked textually, not type-checked).
func TestCompanionSurfaceParityAndCoverageBoundaries(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	sdkRaw, err := os.ReadFile(filepath.Join(root, "packages", "sdk", "src", "companion.ts"))
	if err != nil {
		t.Fatal(err)
	}
	sdk := string(sdkRaw)
	// Owner identity on a record is always the caller's own; the SDK does
	// not type it. Nothing else may be missing.
	notTyped := map[string]string{"account_scope_id": "owner identity (the caller)", "user_id": "owner identity (the caller)"}
	for _, typ := range []reflect.Type{
		reflect.TypeOf(CompanionView{}), reflect.TypeOf(CompanionRoutes{}), reflect.TypeOf(CompanionActivity{}),
		reflect.TypeOf(CompanionRunActivity{}), reflect.TypeOf(CompanionQueuedInput{}), reflect.TypeOf(pebblestore.V3TaskWatchSummary{}),
		reflect.TypeOf(pebblestore.CompanionContextRecord{}), reflect.TypeOf(pebblestore.CompanionContextSource{}),
		reflect.TypeOf(pebblestore.CompanionContextReadStats{}), reflect.TypeOf(companionContextPutRequest{}),
		reflect.TypeOf(companionContextResult{}), reflect.TypeOf(companionDeleteRequest{}), reflect.TypeOf(companionEnsureRequest{}),
	} {
		for _, name := range companionJSONFields(typ) {
			if _, ok := notTyped[name]; ok {
				continue
			}
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\??:`).MatchString(sdk) {
				t.Errorf("SDK companion.ts does not type %s.%s", typ.Name(), name)
			}
		}
	}
	routes := regexp.MustCompile("'(/v3/companion[^'?$]*)").FindAllStringSubmatch(sdk, -1)
	routes = append(routes, regexp.MustCompile("`(/v3/companion[^`?$]*)").FindAllStringSubmatch(sdk, -1)...)
	if len(routes) == 0 {
		t.Fatal("SDK calls no Companion route")
	}
	for _, route := range routes {
		if route[1] != CompanionPath && route[1] != CompanionContextPath {
			t.Errorf("SDK calls %s, which the daemon does not serve", route[1])
		}
	}

	docRaw, err := os.ReadFile(filepath.Join(root, "docs", "frontends.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(docRaw)
	for _, want := range []string{CompanionPath, CompanionContextPath, "companion.activity", "queued_inputs", "pending_permissions", "task_watches", "waiting_next_turn"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/frontends.md does not document %q", want)
		}
	}

	registered, err := os.ReadFile("server_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	for entry, viaCatalog := range map[string]bool{`CompanionPath -> s.handleCompanion`: false, `CompanionContextPath -> s.handleCompanionContext`: true} {
		handler := strings.TrimSpace(strings.Split(entry, "->")[1])
		path := strings.TrimSpace(strings.Split(entry, "->")[0])
		if !strings.Contains(string(registered), "mux.HandleFunc("+path+", "+handler+")") {
			t.Errorf("route %s is not registered", entry)
		}
		_, reached := mcpRouteParityReached[entry]
		none := slices.Contains(mcpRouteParityNoMCPAction, entry)
		switch {
		case viaCatalog && (!reached || none || !strings.Contains(mcpRouteParityReached[entry], tool.ManageCompanionToolName)):
			t.Errorf("route %s must be recorded as reached by %s (reached=%v, no-MCP=%v)", entry, tool.ManageCompanionToolName, reached, none)
		case !viaCatalog && (!none || reached):
			t.Errorf("route %s has no recorded no-MCP decision in mcpRouteParityNoMCPAction", entry)
		}
	}
	if _, ok := tool.CatalogToolNamed(tool.ManageCompanionToolName); !ok {
		t.Fatal("the Companion context bank has no catalog tool")
	}

	contract := companionInstructions(pebblestore.SessionSnapshot{Metadata: map[string]any{pebblestore.SessionCompanionMetadataKey: true}})
	if !strings.Contains(contract, "watch_tasks") {
		t.Fatal("the Companion contract no longer names its watch action; update this guard with it")
	}
	if cancel, ok := tool.ProjectTaskWatchAction("watch_tasks"); !ok || cancel {
		t.Fatal("the Companion contract names watch_tasks, which manage_projects does not accept")
	}
	if strings.Contains(doc, "Swarm Control (MCP) has no Companion action") || !strings.Contains(doc, tool.ManageCompanionToolName) {
		t.Fatal("docs/frontends.md must describe manage_companion and no longer say MCP has no Companion action")
	}
}

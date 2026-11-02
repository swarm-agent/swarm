package api

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
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
		reflect.TypeOf(CompanionStateView{}), reflect.TypeOf(pebblestore.CompanionState{}), reflect.TypeOf(pebblestore.CompanionStateCursor{}),
		reflect.TypeOf(pebblestore.CompanionEventSource{}), reflect.TypeOf(pebblestore.CompanionStateRef{}), reflect.TypeOf(pebblestore.CompanionCommitment{}),
		reflect.TypeOf(pebblestore.CompanionWorkRef{}), reflect.TypeOf(pebblestore.CompanionResultRef{}), reflect.TypeOf(pebblestore.CompanionStateCounters{}),
		reflect.TypeOf(pebblestore.CompanionFocus{}),
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
		if route[1] != CompanionPath && route[1] != CompanionContextPath && route[1] != CompanionStatePath {
			t.Errorf("SDK calls %s, which the daemon does not serve", route[1])
		}
	}

	docRaw, err := os.ReadFile(filepath.Join(root, "docs", "frontends.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(docRaw)
	for _, want := range []string{CompanionPath, CompanionContextPath, CompanionStatePath, "companion.activity", "queued_inputs", "pending_permissions", "task_watches", "waiting_next_turn", "companion_state", "commitments", "pending"} {
		if !strings.Contains(doc, want) {
			t.Errorf("docs/frontends.md does not document %q", want)
		}
	}

	registered, err := os.ReadFile("server_routes.go")
	if err != nil {
		t.Fatal(err)
	}
	for entry, viaCatalog := range map[string]bool{`CompanionPath -> s.handleCompanion`: false, `CompanionContextPath -> s.handleCompanionContext`: true, `CompanionStatePath -> s.handleCompanionState`: true} {
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

// companionWireFields walks a wire type transitively and returns, per Go type
// name, the JSON field names it emits: embedded structs are folded into their
// parent, and every struct of this module reached through a field (directly,
// or through a pointer, slice, array or map) is walked too. A field added to
// any nested Companion DTO is therefore found without listing its type.
func companionWireFields(root reflect.Type, into map[string][]string) {
	for root.Kind() == reflect.Pointer || root.Kind() == reflect.Slice || root.Kind() == reflect.Array || root.Kind() == reflect.Map {
		root = root.Elem()
	}
	if root.Kind() != reflect.Struct || !strings.HasPrefix(root.PkgPath(), "swarm/packages/swarmd/") {
		return
	}
	key := root.PkgPath() + "." + root.Name()
	if _, seen := into[key]; seen {
		return
	}
	into[key] = []string{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			field := t.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.Anonymous && name == "" {
				embedded := field.Type
				if embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					walk(embedded)
					continue
				}
			}
			if name == "-" || !field.IsExported() {
				continue
			}
			if name == "" {
				name = field.Name
			}
			into[key] = append(into[key], name)
			companionWireFields(field.Type, into)
		}
	}
	walk(root)
}

// Purpose: the later Companion stages (maintained state, continuity segments,
// maintenance freshness, foreground activity) stay one documented, typed and
// registered contract as they grow. Requirement: (1) every JSON field reached
// from the Companion route answers (CompanionView with its activity, the
// state route's CompanionStateView, the context bank result), followed
// transitively through every nested Companion struct (segments, segment
// items, continuity, maintenance and its failure, tool calls, background
// deliveries, context freshness, read stats), is typed in
// packages/sdk/src/companion.ts; (2) every Companion route constant the
// daemon declares is registered in server_routes.go, documented in
// docs/frontends.md, called by the SDK, and has exactly one MCP decision
// (reached by manage_companion, or a recorded no-MCP entry); (3) every
// manage_companion catalog action is named in the manage_companion action
// list of docs/frontends.md and the action table of the manage_companion
// section of docs/tool-catalog.md, and its route is recorded
// as reached by manage_companion (state through /v3/companion/state, every
// other action through /v3/companion/context); (4) docs/tool-catalog.md names
// the one machine= refusal (tool.ManageCompanionMachineRefusal) whose text
// TestManageCompanionNativeMCPAndRouteAgree and TestCompanionStateSurfacesAgree
// compare on both surfaces. Threat: a field, nested type, route or catalog
// action added in one place only, so a client, an outside AI or the
// documentation silently diverges from the daemon. Boundary: the Companion
// DTO types, the Companion*Path constants, server_routes.go,
// mcpRouteParityReached, mcpRouteParityNoMCPAction and the manage_companion
// catalog declaration. Narrowest layer: the types, registries and checked-in
// files themselves (no TypeScript compiler exists in this test environment,
// so the SDK is checked textually, not type-checked).
func TestCompanionStateAndContinuitySurfacesDoNotDrift(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	read := func(parts ...string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(parts...))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	sdk := read(root, "packages", "sdk", "src", "companion.ts")
	frontends := read(root, "docs", "frontends.md")
	catalogDoc := read(root, "docs", "tool-catalog.md")
	registered := read("server_routes.go")

	// (1) Every wire field, transitively.
	fields := map[string][]string{}
	for _, typ := range []reflect.Type{reflect.TypeOf(CompanionView{}), reflect.TypeOf(CompanionStateView{}), reflect.TypeOf(companionContextResult{})} {
		companionWireFields(typ, fields)
	}
	notTyped := map[string]bool{"account_scope_id": true, "user_id": true} // the caller's own identity
	for _, nested := range []string{"CompanionStateView", "CompanionMaintenanceView", "companionMaintenanceFailure", "CompanionActivity", "CompanionToolCall", "CompanionBackgroundDelivery", "CompanionContextFreshness", "CompanionSegmentView", "CompanionSegmentItem", "CompanionContinuity", "CompanionState", "CompanionCommitment", "CompanionWorkRef"} {
		found := false
		for key := range fields {
			found = found || strings.HasSuffix(key, "."+nested)
		}
		if !found {
			t.Errorf("the walk from the Companion answers no longer reaches %s; update this guard with the wire shape", nested)
		}
	}
	keys := make([]string, 0, len(fields))
	for key := range fields {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	total := 0
	for _, key := range keys {
		for _, name := range fields[key] {
			total++
			if notTyped[name] {
				continue
			}
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\??:`).MatchString(sdk) {
				t.Errorf("SDK companion.ts does not type %s.%s", key, name)
			}
		}
	}
	t.Logf("checked %d JSON fields across %d Companion wire types", total, len(keys))

	// (2) Every Companion route the daemon declares.
	declared := map[string]string{} // constant -> path
	sources, err := filepath.Glob("companion*.go")
	if err != nil {
		t.Fatal(err)
	}
	pathConst := regexp.MustCompile(`(?m)^const (Companion\w*Path) = "(/v3/companion[^"]*)"`)
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		for _, m := range pathConst.FindAllStringSubmatch(read(source), -1) {
			declared[m[1]] = m[2]
		}
	}
	for constant, want := range map[string]string{"CompanionPath": CompanionPath, "CompanionContextPath": CompanionContextPath, "CompanionStatePath": CompanionStatePath} {
		if declared[constant] != want {
			t.Fatalf("route declaration scan found %s = %q, want %q; update the scan with the declaration form", constant, declared[constant], want)
		}
	}
	for constant, path := range declared {
		registration := regexp.MustCompile(`mux\.HandleFunc\(` + constant + `, (s\.\w+)\)`).FindStringSubmatch(registered)
		if registration == nil {
			t.Errorf("route %s (%s) is declared but not registered", constant, path)
			continue
		}
		entry := constant + " -> " + registration[1]
		_, reached := mcpRouteParityReached[entry]
		if none := slices.Contains(mcpRouteParityNoMCPAction, entry); reached == none {
			t.Errorf("route %s needs exactly one MCP decision (reached=%v, no-MCP=%v)", entry, reached, none)
		}
		if reached && !strings.Contains(mcpRouteParityReached[entry], tool.ManageCompanionToolName) {
			t.Errorf("route %s is reached by %q, not by %s", entry, mcpRouteParityReached[entry], tool.ManageCompanionToolName)
		}
		if !strings.Contains(frontends, path) {
			t.Errorf("docs/frontends.md does not document %s", path)
		}
		if !strings.Contains(sdk, "'"+path+"'") && !strings.Contains(sdk, "`"+path) {
			t.Errorf("SDK companion.ts does not call %s", path)
		}
	}

	// (3) Every catalog action, in both documents and its route's MCP record.
	catalog, ok := tool.CatalogToolNamed(tool.ManageCompanionToolName)
	if !ok {
		t.Fatal("manage_companion is not in the catalog")
	}
	section := catalogDoc
	if _, after, found := strings.Cut(catalogDoc, "\n## "+tool.ManageCompanionToolName+"\n"); found {
		section, _, _ = strings.Cut(after, "\n## ")
	} else {
		t.Fatal("docs/tool-catalog.md has no manage_companion section")
	}
	if len(catalog.Actions) == 0 {
		t.Fatal("manage_companion declares no actions")
	}
	// The action table rows of the section, and the action list frontends.md
	// gives after the tool's name: a word elsewhere in the prose (such as a
	// route described "for `state`") does not count as listing the action.
	var tableRows []string
	for _, line := range strings.Split(section, "\n") {
		if strings.HasPrefix(line, "| `") {
			tableRows = append(tableRows, line)
		}
	}
	table := strings.Join(tableRows, "\n")
	_, listed, found := strings.Cut(frontends, "`"+tool.ManageCompanionToolName+"` (`")
	listed, _, closed := strings.Cut(listed, "):")
	if len(tableRows) == 0 || !found || !closed {
		t.Fatalf("cannot find the manage_companion action table (%d rows) or the frontends.md action list (found %v, closed %v); update this guard with the document shape", len(tableRows), found, closed)
	}
	listed = "`" + listed
	for _, action := range catalog.Actions {
		name := "`" + action.Action + "`"
		if !strings.Contains(table, name) {
			t.Errorf("docs/tool-catalog.md manage_companion action table does not list %s", name)
		}
		if !strings.Contains(listed, name) {
			t.Errorf("docs/frontends.md manage_companion action list does not name %s", name)
		}
		entry, via := `CompanionContextPath -> s.handleCompanionContext`, tool.ManageCompanionToolName
		if action.Action == "state" {
			entry, via = `CompanionStatePath -> s.handleCompanionState`, tool.ManageCompanionToolName+" state"
		}
		if !strings.Contains(mcpRouteParityReached[entry], via) {
			t.Errorf("manage_companion %s: route %s is not recorded as reached by %q", action.Action, entry, via)
		}
	}

	// (4) The one machine= refusal is documented by name.
	if !strings.Contains(section, "tool.ManageCompanionMachineRefusal") || !strings.Contains(frontends, "same reason on both") {
		t.Error("the manage_companion machine= refusal is not documented as one reason on both surfaces")
	}
}

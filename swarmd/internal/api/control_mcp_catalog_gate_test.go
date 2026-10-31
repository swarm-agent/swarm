package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	agentruntime "swarm/packages/swarmd/internal/agent"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// catalogGatedChange reports whether the parity harness runs this
// Orchestrator call through the run service's gate: every change (a
// non-read action) of manage_sessions and manage_workspace that Swarm
// Control serves, so the approval payload and the approved arguments the
// person's decision carries are held to parity too, not only the handler.
func catalogGatedChange(name string, args map[string]any) bool {
	if name != "manage_sessions" && name != "manage_workspace" {
		return false
	}
	catalog, ok := tool.CatalogToolNamed(name)
	if !ok {
		return false
	}
	action, _ := args["action"].(string)
	declared, ok := catalog.Action(action)
	return ok && declared.ControlCallable() && declared.Level != tool.CatalogLevelRead
}

var catalogGateCalls atomic.Int64

// gatedOrchestratorTool runs one Orchestrator call through the run service's
// real gate and execution (run.Service.NewProviderManagedToolInvoker ->
// gateToolCalls -> executeControlPlaneTool, the path a provider-managed
// Orchestrator's tool call takes). A stand-in for the person approves every
// prompt the call raises in the conversation; the prompts are returned so a
// test can read what the person was shown.
func (h *catalogParityHarness) gatedOrchestratorTool(t *testing.T, ctx context.Context, session, name string, args map[string]any) (string, []pebblestore.PermissionRecord, error) {
	t.Helper()
	perms, ok := h.home.s.perm.(*permission.Service)
	if !ok || h.workspaceRun == nil {
		t.Fatal("the harness has no run service gate")
	}
	n := catalogGateCalls.Add(1)
	callID := fmt.Sprintf("gate-call-%d", n)
	invoker := h.workspaceRun.NewProviderManagedToolInvoker(runruntime.ProviderManagedToolInvokerConfig{
		SessionID: session, PermissionSessionID: session, RunID: fmt.Sprintf("gate-run-%d", n),
		Principal: h.p, SessionMode: sessionruntime.ModeAuto, ProviderManagedV3: true, WorkspacePath: h.sessionFolders["home"], WorkspaceRoots: []string{h.sessionFolders["home"]}, ApplySessionMutation: h.home.s.sessions.ApplySessionMutation,
		// The Orchestrator primary agent, whose conversation this is.
		AgentProfile: pebblestore.AgentProfile{Name: agentruntime.SwarmOrchestratorAgentID, Mode: "primary"},
	})
	raw, _ := json.Marshal(args)
	stop := make(chan struct{})
	var mu sync.Mutex
	var prompts []pebblestore.PermissionRecord
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			pending, _ := perms.ListPending(session, 20)
			for _, record := range pending {
				if record.CallID != callID {
					continue
				}
				mu.Lock()
				prompts = append(prompts, record)
				mu.Unlock()
				_, _ = perms.Resolve(session, record.ID, "approve", "parity: the person approves")
			}
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()
	result, err := invoker.ExecuteTool(ctx, provideriface.ToolInvocation{CallID: callID, Name: name, Arguments: string(raw)})
	close(stop)
	wg.Wait()
	if err != nil {
		return "", prompts, err
	}
	if result.Error != "" {
		return "", prompts, errors.New(result.Error)
	}
	return result.Output, prompts, nil
}

// catalogSessionArchived reports whether a session is archived (a tombstone
// and no live record) in a machine's store.
func catalogSessionArchived(t *testing.T, f *fleetTestSwarm, id string) bool {
	t.Helper()
	if _, live, err := f.s.sessions.GetSession(id); err != nil {
		t.Fatal(err)
	} else if live {
		return false
	}
	tombstone, ok, err := f.s.sessions.GetSessionTombstone(id)
	if err != nil {
		t.Fatal(err)
	}
	return ok && tombstone.Archived
}

// catalogSessionKnown reports whether a machine holds the session, live or
// archived.
func catalogSessionKnown(t *testing.T, f *fleetTestSwarm, id string) bool {
	t.Helper()
	if _, live, err := f.s.sessions.GetSession(id); err != nil {
		t.Fatal(err)
	} else if live {
		return true
	}
	_, ok, err := f.s.sessions.GetSessionTombstone(id)
	if err != nil {
		t.Fatal(err)
	}
	return ok
}

// catalogActiveSessionIDs are a machine's live sessions for its owner,
// sorted (a session's place in recency order is not what changes).
func catalogActiveSessionIDs(t *testing.T, f *fleetTestSwarm) string {
	t.Helper()
	result, err := f.s.sessions.SearchSessions(pebblestore.V3SessionSearchOptions{AccountScopeID: f.account, UserID: f.user, Global: true, ArchivedMode: "exclude", Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		ids = append(ids, item.ID)
	}
	sort.Strings(ids)
	return strings.Join(ids, ",")
}

// Purpose: an Orchestrator's manage_sessions archive or unarchive with
// machine changes the linked machine's sessions and never this machine's.
// The approval the person sees lists the linked machine's sessions (read
// through that machine's own manage_sessions), the approved arguments keep
// machine, and the approved call runs on the linked machine's handler.
// This machine's own session tooling (deploy, commit) is refused with
// machine before any prompt. Threat: the approval payload resolved
// session_ids and all/category on this machine and dropped machine from
// the approved arguments, so {"action":"archive","machine":"box","all":true}
// showed and archived this machine's sessions. Boundary: the run service's
// real gate and execution (NewProviderManagedToolInvoker -> gateToolCalls ->
// permissionArgumentsForCall -> buildManageSessionsPermissionPayload ->
// executeManageSessionsCanonicalMutation) over the linked catalog route;
// only the person's click is a stand-in.
func TestManageSessionsLinkedArchiveThroughTheGate(t *testing.T) {
	h := newCatalogParityHarness(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	homeBefore := catalogActiveSessionIDs(t, h.home)
	card := func(prompts []pebblestore.PermissionRecord) map[string]any {
		t.Helper()
		if len(prompts) != 1 {
			t.Fatalf("want one prompt for the person, got %d", len(prompts))
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(prompts[0].ToolArguments), &payload); err != nil {
			t.Fatal(err)
		}
		approved, _ := payload["approved_arguments"].(map[string]any)
		if payload["machine"] != "box" || approved["machine"] != "box" {
			t.Fatalf("the approval dropped machine: %s", prompts[0].ToolArguments)
		}
		for _, raw := range payload["sessions"].([]any) {
			id, _ := raw.(map[string]any)["session_id"].(string)
			// Every listed session is the linked machine's. An id both
			// machines happen to hold (the job fixtures seed one) is fine:
			// this machine's sessions are checked unchanged below.
			if !catalogSessionKnown(t, h.remote, id) {
				t.Fatalf("the approval of a linked change shows a session %q that is not the linked machine's: %s", id, prompts[0].ToolArguments)
			}
		}
		return payload
	}

	// One linked session: archived there; its home twin stays.
	out, prompts, err := h.gatedOrchestratorTool(t, ctx, "home-orch", "manage_sessions", map[string]any{"action": "archive", "machine": "box", "session_ids": []any{"rms-arc-1"}})
	if err != nil {
		t.Fatalf("linked archive: %v", err)
	}
	card(prompts)
	if !strings.Contains(out, `"machine":"box"`) || !catalogSessionArchived(t, h.remote, "rms-arc-1") || catalogSessionArchived(t, h.home, "ms-arc-1") {
		t.Fatalf("linked archive changed the wrong machine: %s", out)
	}

	// Unarchive on the linked machine: restored there; the home twin stays archived.
	if _, prompts, err = h.gatedOrchestratorTool(t, ctx, "home-orch", "manage_sessions", map[string]any{"action": "unarchive", "machine": "box", "session_id": "rms-unarc-1"}); err != nil {
		t.Fatalf("linked unarchive: %v", err)
	}
	card(prompts)
	if catalogSessionArchived(t, h.remote, "rms-unarc-1") || !catalogSessionArchived(t, h.home, "ms-unarc-1") {
		t.Fatal("linked unarchive changed the wrong machine")
	}

	// all=true with machine: only the linked machine's sessions are shown
	// and archived; this machine's live sessions are exactly as before.
	if _, prompts, err = h.gatedOrchestratorTool(t, ctx, "home-orch", "manage_sessions", map[string]any{"action": "archive", "machine": "box", "all": true}); err != nil {
		t.Fatalf("linked archive all: %v", err)
	}
	if payload := card(prompts); len(payload["sessions"].([]any)) == 0 {
		t.Fatal("the linked archive-all approval lists no sessions")
	}
	if got := catalogActiveSessionIDs(t, h.home); got != homeBefore {
		t.Fatalf("a linked archive-all changed this machine's sessions:\nbefore %s\nafter  %s", homeBefore, got)
	}
	if !catalogSessionArchived(t, h.remote, "rms-arc-2") {
		t.Fatal("the linked archive-all left a linked session live")
	}

	// This machine's own session tooling never takes machine: refused before
	// a prompt.
	for _, args := range []map[string]any{
		{"action": "commit", "machine": "box", "commits": []any{map[string]any{"session_id": "ms-read", "message": "x"}}},
		{"action": "deploy", "machine": "box", "proposals": []any{map[string]any{"prompt": "x"}}},
	} {
		_, prompts, err := h.gatedOrchestratorTool(t, ctx, "home-orch", "manage_sessions", args)
		if err == nil || !strings.Contains(err.Error(), "this machine only") || len(prompts) != 0 {
			t.Fatalf("%v with machine: err=%v prompts=%d", args["action"], err, len(prompts))
		}
	}
	if got := catalogActiveSessionIDs(t, h.home); got != homeBefore {
		t.Fatal("a refused call changed this machine's sessions")
	}
}

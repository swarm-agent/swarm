package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"testing"
	"time"
)

// Purpose: CallFleetWork must read the linked daemon, not home state, and
// reject URL traversal and control-plane operations before transport. Two
// in-process daemons exercise the actual pinned link and workflow HTTP boundary.
func TestFleetWorkReads(t *testing.T) {
	home := newFleetTestSwarm(t, "home")
	defer home.cleanup()
	remote := newFleetTestSwarm(t, "remote")
	defer remote.cleanup()
	repository := jobautomation.NewStore(remote.s.sessions.Store().Underlying())
	remote.s.SetJobAutomations(jobautomation.NewRunner(jobautomation.Options{Store: repository}))
	body, _ := json.Marshal(map[string]string{"code": remote.invite(t), "name": "build-box"})
	if status, out := home.call(t, http.MethodPost, "/v3/fleet/machines", string(body), false); status != http.StatusCreated {
		t.Fatalf("link: %d %v", status, out)
	}
	p := identity.Principal{Type: identity.PrincipalTypeUser, UserID: home.user, AccountScopeID: home.account}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "list", nil)
	if err != nil || !strings.Contains(string(raw), `"automations":[]`) {
		t.Fatalf("list: %s %v", raw, err)
	}
	if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "get", map[string]any{"id": "missing"}); err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("remote missing get: %v", err)
	}
	for _, action := range []string{"secrets", "permissions", "rotate_key", "accept"} {
		if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", action, nil); err == nil {
			t.Fatalf("allowed %s", action)
		}
	}
	if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "get", map[string]any{"id": "../permissions"}); err == nil {
		t.Fatal("traversal allowed")
	}
	a, _, err := repository.Put(remote.account, jobautomation.Automation{Name: "Review", Scope: jobautomation.Scope{Folder: t.TempDir()}, Enabled: true, Steps: []jobautomation.Step{{ID: "review", Type: jobautomation.StepAgent, Worker: "reviewer", Brief: "Review exact changes"}}})
	if err != nil {
		t.Fatal(err)
	}
	full, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "get", map[string]any{"id": a.ID})
	if err != nil || !strings.Contains(string(full), "Review exact changes") {
		t.Fatalf("get: %s %v", full, err)
	}
	if _, err := home.s.CallFleetWork(ctx, p, "build-box", "manage_workflows", "disable", map[string]any{"id": a.ID}); err != nil {
		t.Fatal(err)
	}
	updated, err := repository.Get(remote.account, a.ID)
	if err != nil || updated.Enabled {
		t.Fatalf("disable not stored: %+v %v", updated, err)
	}
	other := p
	other.AccountScopeID = "other-account"
	if _, err := home.s.CallFleetWork(ctx, other, "build-box", "manage_workflows", "list", nil); err == nil {
		t.Fatal("foreign link allowed")
	}
}

// Purpose: compactFleetList is the response-size boundary for linked lists.
// Large definitions and step outcomes must not reach the model; get is separate.
// A direct projection test isolates the shape and size invariant from transport.
func TestFleetWorkCompact(t *testing.T) {
	rows := []any{}
	for i := 0; i < 12; i++ {
		rows = append(rows, map[string]any{"id": "workflow", "name": "Review", "enabled": true, "steps": []any{map[string]any{"brief": strings.Repeat("x", 140000)}}, "latest_deliveries": []any{map[string]any{"state": "succeeded", "updated_at": 123, "outcomes": strings.Repeat("x", 140000)}}})
	}
	raw, _ := json.Marshal(map[string]any{"automations": rows})
	compact, err := compactFleetList(raw)
	if err != nil || len(compact) > 6000 || strings.Contains(string(compact), "brief") || strings.Contains(string(compact), "outcomes") {
		t.Fatalf("compact: bytes=%d err=%v", len(compact), err)
	}
	if !strings.Contains(string(compact), `"step_count":1`) || !strings.Contains(string(compact), `"state":"succeeded"`) {
		t.Fatalf("missing summary: %s", compact)
	}
}

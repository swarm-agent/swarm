package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Requirement: a sealed agent file may set limits.retention_days (0 to 3650),
// which swarmctl passes to the daemon with the agent; an out-of-range value is
// refused before anything is saved. runSetupSealedAgent is the authority; the
// fixture records what reaches the daemon.
func TestSetupSealedAgentPassesRetention(t *testing.T) {
	var agentBodies []map[string]any
	requests := 0
	socket := setupFixture(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method == http.MethodPut && r.URL.Path == "/v2/agents/frontdesk" {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			agentBodies = append(agentBodies, body)
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	file := func(days int) string {
		return `{"tools":[{"name":"lookup_order","description":"x","input_schema":{"type":"object"}}],
			"agent":{"name":"frontdesk","prompt":"x","tools":["lookup_order"],"limits":{"max_steps":3,"retention_days":` + itoa(days) + `}}}`
	}
	var out bytes.Buffer
	if err := runSetupSealedAgent([]string{"sealed-agent", "--stdin", "--socket", socket}, strings.NewReader(file(3651)), &out); err == nil || !strings.Contains(err.Error(), "retention_days") {
		t.Fatalf("retention_days 3651: %v", err)
	}
	if requests != 0 {
		t.Fatalf("a refused file reached the daemon (%d requests)", requests)
	}
	if err := runSetupSealedAgent([]string{"sealed-agent", "--stdin", "--socket", socket}, strings.NewReader(file(30)), &out); err != nil {
		t.Fatal(err)
	}
	if len(agentBodies) != 1 {
		t.Fatalf("agent saves: %d", len(agentBodies))
	}
	limits, _ := agentBodies[0]["limits"].(map[string]any)
	if limits["retention_days"] != float64(30) || limits["max_steps"] != float64(3) {
		t.Fatalf("limits sent: %v", limits)
	}
}

func itoa(v int) string {
	b, _ := json.Marshal(v)
	return string(b)
}

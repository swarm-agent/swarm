package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: routeProjectTaskSource must admit exact catalog bindings from bare
// JSON or one complete JSON/unlabelled fence, without treating formatting as
// authority. Threat: backticks break deploy, while permissive extraction could
// accept extra payloads or bypass stale/foreign binding checks. The API/store
// fixture is the narrowest layer proving admission and no reservation/allocation
// on rejection; it does not exercise a live provider.
func TestAutomaticProjectTaskRoutingFencedJSON(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prefix string
		suffix string
		change string
		want   string
	}{
		{name: "bare", want: "accepted"},
		{name: "JSON fence", prefix: "```json\n", suffix: "\n```", want: "accepted"},
		{name: "unlabelled fence", prefix: "```\n", suffix: "\n```", want: "accepted"},
		{name: "CRLF whitespace", prefix: " \r\n```json \r\n", suffix: " \r\n\t``` \r\n", want: "accepted"},
		{name: "missing close", prefix: "```json\n", want: "invalid JSON"},
		{name: "missing opening newline", prefix: "```json ", suffix: "\n```", want: "invalid JSON"},
		{name: "inline close", prefix: "```json\n", suffix: "```", want: "invalid JSON"},
		{name: "wrong label", prefix: "```javascript\n", suffix: "\n```", want: "invalid JSON"},
		{name: "extra marker", prefix: "````json\n", suffix: "\n````", want: "invalid JSON"},
		{name: "leading prose", prefix: "Here is the selection:\n```json\n", suffix: "\n```", want: "invalid JSON"},
		{name: "trailing prose", prefix: "```json\n", suffix: "\n```\nDone", want: "invalid JSON"},
		{name: "trailing JSON", prefix: "```json\n", suffix: "\n```\n{}", want: "invalid JSON"},
		{name: "bare trailing JSON", suffix: " {}", want: "invalid JSON"},
		{name: "payload inside fence", prefix: "```json\n", suffix: "\n{}\n```", want: "invalid JSON"},
		{name: "multiple fences", prefix: "```json\n", suffix: "\n```\n```json\n{}\n```", want: "invalid JSON"},
		{name: "malformed JSON", prefix: "```json\n", suffix: "\n```", change: "malformed", want: "invalid JSON"},
		{name: "empty fence", prefix: "```json\n", suffix: "\n```", change: "empty", want: "invalid JSON"},
		{name: "stale fenced source", prefix: "```json\n", suffix: "\n```", change: "stale", want: "source rejected"},
		{name: "foreign fenced context", prefix: "```json\n", suffix: "\n```", change: "context", want: "context rejected"},
		{name: "fenced clarification", prefix: "```json\n", suffix: "\n```", change: "diagnostic", want: "needs clarification: Choose `repo`"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p, proj, source, runner := routingFixture(t)
			raw := runner.response.Text
			var selection struct {
				Source     pebblestore.ProjectTaskSource   `json:"source"`
				Context    []pebblestore.ProjectTaskSource `json:"context"`
				Diagnostic string                          `json:"diagnostic"`
			}
			if err := json.Unmarshal([]byte(raw), &selection); err != nil {
				t.Fatal(err)
			}
			switch tc.change {
			case "stale":
				selection.Source.WorkspaceGeneration++
			case "context":
				selection.Context[0].WorkspaceID = "foreign-workspace"
			case "diagnostic":
				selection.Diagnostic = "Choose `repo`"
			}
			encoded, err := json.Marshal(selection)
			if err != nil {
				t.Fatal(err)
			}
			raw = string(encoded)
			if tc.change == "malformed" {
				raw = `{"source":`
			} else if tc.change == "empty" {
				raw = ""
			}
			runner.response.Text = tc.prefix + raw + tc.suffix
			w := f.callAPI(http.MethodPost, "/"+proj.ID+"/tasks", map[string]any{
				"id": "fenced-route", "title": "Fix", "prompt": "Fix repo error", "agent": "coder", "auto_approve": false,
			}, p)
			saved, found, err := f.server.sessions.Store().GetProjectTask(f.accountID, proj.ID, "fenced-route")
			if err != nil || runner.createCalls != 1 {
				t.Fatalf("routing calls=%d store error=%v", runner.createCalls, err)
			}
			if tc.want != "accepted" {
				if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), tc.want) || found || f.wt.allocCalls != 0 {
					t.Fatalf("rejection/state: status=%d body=%s found=%v allocations=%d", w.Code, w.Body.String(), found, f.wt.allocCalls)
				}
				return
			}
			if w.Code != http.StatusCreated || !found {
				t.Fatalf("admission: status=%d body=%s found=%v", w.Code, w.Body.String(), found)
			}
			if saved.SourceWorkspace.Path != source.Path || saved.SourceWorkspace.WorkspaceID != source.WorkspaceID || saved.SourceWorkspace.WorkspaceGeneration != source.WorkspaceGeneration || saved.SourceWorkspace.Provenance != "router" || saved.Status != "pending_approval" || len(saved.ContextSources) != 1 {
				t.Fatalf("changed source/approval/context: %+v", saved)
			}
			got, want := saved.ContextSources[0], selection.Context[0]
			if got.Path != want.Path || got.WorkspaceID != want.WorkspaceID || got.WorkspaceGeneration != want.WorkspaceGeneration || got.Provenance != "router" {
				t.Fatalf("changed read-only context: got=%+v want=%+v", got, want)
			}
		})
	}
}

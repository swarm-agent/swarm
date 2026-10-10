package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: writeProjectTaskBoard must serialize a flattened task plus an explicit
// board_summary for populated, stale and empty rows. Promoting the embedded
// ProjectTaskRecord.MarshalJSON drops that marker and makes Desktop mistake a
// compact row for fully loaded detail, blocking approval. The real HTTP writer
// is the narrowest layer proving the wire contract, including exact review and
// program child identities rather than just a successful response status.
func TestProjectTaskBoardSummaryEncoding(t *testing.T) {
	plan := &pebblestore.ProjectTaskPlanSummary{
		ID: "plan", SessionID: "owner", AccountScopeID: "account", Version: 3,
		Status: "pending_approval", ApprovalState: "pending",
	}
	program := &projectTaskBoardProgram{
		ID: "program", State: "running",
		Jobs: []projectTaskBoardJob{{ID: "job", SessionID: "child", RunID: "run", ExcludedSessionIDs: []string{"previous-child"}}},
	}
	for _, tc := range []struct {
		name    string
		summary projectTaskBoardRelated
	}{
		{"plan_and_program", projectTaskBoardRelated{Plan: plan, Program: program}},
		{"stale_binding", projectTaskBoardRelated{PlanBindingStale: true}},
		{"empty", projectTaskBoardRelated{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := pebblestore.ProjectTaskRecord{
				ID: "task", ProjectID: "project", AccountID: "account", Title: "Task",
				Status: "pending_approval", SessionID: "owner", Revision: 7,
				PlanBinding: &pebblestore.ProjectTaskPlanBinding{PlanID: "plan", SessionID: "owner", DefinitionRevision: 3, Receipt: "exact"},
			}
			w := httptest.NewRecorder()
			writeProjectTaskBoard(w, []projectTaskBoardRow{{ProjectTaskRecord: task, BoardSummary: tc.summary}}, pebblestore.ProjectTaskReadStats{}, time.Now(), 0)
			if w.Code != http.StatusOK {
				t.Fatalf("response=%d %s", w.Code, w.Body.String())
			}
			var body struct {
				Tasks []map[string]json.RawMessage `json:"tasks"`
				Count int                          `json:"count"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Count != 1 || len(body.Tasks) != 1 {
				t.Fatalf("count=%d tasks=%d", body.Count, len(body.Tasks))
			}
			fields := body.Tasks[0]
			raw, ok := fields["board_summary"]
			if !ok || len(raw) == 0 || raw[0] != '{' {
				t.Fatalf("missing summary object: %s", w.Body.String())
			}
			var got projectTaskBoardRelated
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.summary) {
				t.Fatalf("summary=%+v want=%+v", got, tc.summary)
			}
			if tc.name == "empty" && string(raw) != "{}" {
				t.Fatalf("empty marker=%s", raw)
			}
			delete(fields, "board_summary")
			encoded, err := json.Marshal(task)
			if err != nil {
				t.Fatal(err)
			}
			var want map[string]json.RawMessage
			if err := json.Unmarshal(encoded, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(fields, want) {
				t.Fatal("row lost or changed flattened task fields")
			}
		})
	}
}

// Purpose: the board row's explicit MarshalJSON must still cross the privacy
// boundary owned by ProjectTaskRecord.MarshalJSON. A plain alias would leak
// historical error diagnostics. Direct value and pointer encoding is the
// narrowest layer proving redaction plus no mutation of the source record or
// its shared deliverable slice.
func TestProjectTaskBoardEncodingPrivacy(t *testing.T) {
	credential := "fake-board-credential"
	diagnostic := "provider failed https://example.invalid/interactions?key=" + credential
	task := pebblestore.ProjectTaskRecord{
		ID: "task", Status: "failed", LastError: diagnostic,
		Deliverables: []pebblestore.ProjectTaskDeliverable{{ID: "output", Status: "failed", Description: diagnostic}},
	}
	row := projectTaskBoardRow{ProjectTaskRecord: task}
	for _, value := range []any{row, &row} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), credential) {
			t.Fatal("board encoding leaked diagnostic credentials")
		}
		var got struct {
			LastError    string                               `json:"last_error"`
			Deliverables []pebblestore.ProjectTaskDeliverable `json:"deliverables"`
			BoardSummary json.RawMessage                      `json:"board_summary"`
		}
		if err := json.Unmarshal(encoded, &got); err != nil {
			t.Fatal(err)
		}
		if got.LastError == "" || len(got.Deliverables) != 1 || got.Deliverables[0].Description == "" || string(got.BoardSummary) != "{}" {
			t.Fatal("board encoding dropped diagnostics, deliverables or summary marker")
		}
		if task.LastError != diagnostic || task.Deliverables[0].Description != diagnostic || row.LastError != diagnostic || row.Deliverables[0].Description != diagnostic {
			t.Fatal("board encoding mutated canonical task data")
		}
	}
}

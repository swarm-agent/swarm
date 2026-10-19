package run

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// reviewRecordFixture is a real Pebble store, session service and worker
// store with a coder job and a reviewer job that reviewed it; the reviewer's
// session holds the final assistant message a model wrote (the model
// provider is the only boundary replaced).
type reviewRecordFixture struct {
	service  *WorkerExecutionService
	sessions *sessionruntime.Service
	coder    string
	reviewer pebblestore.WorkerRunRecord
}

const reviewRecordHead = "0123456789abcdef0123456789abcdef01234567"

func newReviewRecordFixture(t *testing.T, finalMessage string) reviewRecordFixture {
	t.Helper()
	db, err := pebblestore.Open(filepath.Join(t.TempDir(), "review-record.pebble"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	events, err := pebblestore.NewEventLog(db)
	if err != nil {
		t.Fatal(err)
	}
	sessions := sessionruntime.NewService(pebblestore.NewSessionStore(db), events)
	ws := sessions.Store().WorkerStore()
	coder, err := ws.CreateWorker("account-a", "user-a", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleCoder, Name: "Coder", IdempotencyKey: "coder"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewer, err := ws.CreateWorker("account-a", "user-a", pebblestore.CreateWorkerRequest{Role: pebblestore.WorkerRoleReviewer, Name: "Reviewer", IdempotencyKey: "reviewer"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ws.RecordWorkerRun("account-a", pebblestore.WorkerRunRecord{ID: "run-coder", WorkerID: coder.ID, UserID: "user-a", Role: pebblestore.WorkerRoleCoder, Status: "succeeded", RequestSource: "direct", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	coderJob := "worker/" + coder.ID + "/run-coder"
	pref := pebblestore.ModelPreference{Provider: "fixture-provider", Model: "reviewer-model", Thinking: "high"}
	session, _, err := sessions.CreateSessionWithOptions(sessionruntime.CreateSessionOptions{SessionID: "session-reviewer", UserID: "user-a", AccountScopeID: "account-a", WorkspacePath: t.TempDir(), Preference: &pref,
		Metadata: map[string]any{"worker_role": pebblestore.WorkerRoleReviewer, "worker_execution_run_id": "run-review",
			"worker_review_target": map[string]any{"head": reviewRecordHead, "base": strings.Repeat("a", 40), "job": coderJob}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.AppendMessage(session.ID, "user", "Review this job's exact head.", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := sessions.AppendMessage(session.ID, "assistant", finalMessage, nil); err != nil {
		t.Fatal(err)
	}
	run, err := ws.RecordWorkerRun("account-a", pebblestore.WorkerRunRecord{ID: "run-review", WorkerID: reviewer.ID, UserID: "user-a", Role: pebblestore.WorkerRoleReviewer, SessionID: session.ID,
		Status: "succeeded", RequestSource: "direct", Input: map[string]any{"review_job": coderJob}, CreatedAt: 2, StartedAt: 2, CompletedAt: 3})
	if err != nil {
		t.Fatal(err)
	}
	service := &WorkerExecutionService{host: &AutomationV2ExecutionHost{runs: &Service{sessions: sessions}}}
	return reviewRecordFixture{service: service, sessions: sessions, coder: coder.ID, reviewer: run}
}

func (f reviewRecordFixture) record(t *testing.T) pebblestore.JobReview {
	t.Helper()
	if err := f.service.recordReviewResult(f.reviewer); err != nil {
		t.Fatal(err)
	}
	review, err := f.sessions.Store().WorkerStore().GetJobReview("account-a", f.coder, "run-coder")
	if err != nil || review == nil {
		t.Fatalf("recorded review = %v %v", review, err)
	}
	return *review
}

func reviewRecordMessage(verdict, findings string) string {
	var b strings.Builder
	for i := 1; i <= 16; i++ {
		fmt.Fprintf(&b, "I'm checking whether step %d of the brief is covered by the diff.\n", i)
	}
	b.WriteString("VERDICT: " + verdict + "\nSUMMARY: the change is reviewed against its brief.\nFINDINGS: " + findings + "\nTESTS RUN: none (review is read-only)\n")
	return b.String()
}

// Requirement: recordReviewResult reads the reviewer session's last assistant
// message and records its verdict (and full FINDINGS) on the reviewed job,
// also when the report follows progress notes; swarm_get_job shows this
// record (WorkerRunReview embeds it).
// Threat (forge run): every review recorded BLOCK "malformed" because the
// reviewer wrote progress notes first, and its findings lost.
// Owners: WorkerExecutionService.recordReviewResult, reviewFromResult,
// pebblestore.ParseJobReview and WorkerStore.RecordJobReview over a real
// Pebble store and session.
func TestRecordReviewResultReadsTheReportAfterProgressNotes(t *testing.T) {
	ok := newReviewRecordFixture(t, reviewRecordMessage("OK", "None")).record(t)
	if ok.Verdict != "ok" || ok.Reason != "" || ok.Head != reviewRecordHead {
		t.Fatalf("ok review = %+v", ok)
	}

	var findings strings.Builder
	for i := 1; i <= 5; i++ {
		fmt.Fprintf(&findings, "%d. high internal/upload/upload.go:%d %s\n", i, i, strings.Repeat("the limit is not enforced on this path; ", 30))
	}
	want := strings.TrimSpace(findings.String())
	block := newReviewRecordFixture(t, reviewRecordMessage("BLOCK", want)).record(t)
	if block.Verdict != "block" || block.Findings != want {
		t.Fatalf("block review = verdict %q reason %.60q, findings %d bytes (want %d)", block.Verdict, block.Reason, len(block.Findings), len(want))
	}
	// swarm_get_job forwards the run's review projection (WorkerRunReview,
	// which embeds the record) as-is: it carries the recorded verdict and
	// the full findings.
	encoded, err := json.Marshal(WorkerRunReview{JobReview: &block, State: "needs_review"})
	if err != nil {
		t.Fatal(err)
	}
	var projected map[string]any
	if err := json.Unmarshal(encoded, &projected); err != nil {
		t.Fatal(err)
	}
	if projected["verdict"] != "block" || projected["findings"] != want {
		t.Fatalf("review projection = verdict %v, findings %v", projected["verdict"], projected["findings"])
	}

	malformed := newReviewRecordFixture(t, "I'm checking.\nVERDICT: OK\nSUMMARY: fine\nFINDINGS: None\nVERDICT: OK\nTESTS RUN: none").record(t)
	if malformed.Verdict != "block" || malformed.Reason != "missing or malformed review verdict" || malformed.Findings != "" {
		t.Fatalf("two verdict lines = %+v", malformed)
	}
}

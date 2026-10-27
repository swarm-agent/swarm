package jobautomation

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Purpose: NormalizeSteps and decodeMatrix enforce the bounded committed-list
// contract. This domain test rejects traversal, built-ins and malformed lists
// before any queue mutation; RenderItem must not interpret item data recursively.
func TestForEachValidationAndRendering(t *testing.T) {
	for _, worker := range []string{"coder", "reviewer", "designer", "media"} {
		steps := []Step{{Type: StepAgent, Worker: worker, Brief: "write", ForEach: &ForEach{File: "plan.json"}}}
		if err := NormalizeSteps(steps); !IsValidationError(err) {
			t.Fatalf("%s: %v", worker, err)
		}
	}
	for _, f := range []ForEach{{File: "../plan.json"}, {File: "/plan.json"}, {File: "plan.json", Items: "posts..items"}, {File: "plan.json", MaxParallel: 6}} {
		if err := NormalizeSteps([]Step{{Type: StepAgent, Worker: "maker", Brief: "write", ForEach: &f}}); !IsValidationError(err) {
			t.Fatal(f, err)
		}
	}
	steps := []Step{{Type: StepAgent, Worker: "maker", Brief: "write", ForEach: &ForEach{File: "plan.json"}}}
	if err := NormalizeSteps(steps); err != nil || steps[0].ForEach.MaxParallel != 1 {
		t.Fatal(steps, err)
	}
	for _, data := range []string{`null`, `{}`, `[null]`, `[1]`, `invalid`, `[` + strings.Repeat(`{},`, 20) + `{}]`, strings.Repeat(" ", 256*1024+1)} {
		if _, err := decodeMatrix([]byte(data), ""); err == nil {
			t.Fatalf("accepted invalid list of length %d", len(data))
		}
	}
	items, err := decodeMatrix([]byte(`{"posts":[{"title":"A","nested":{"tags":["b"]}}]}`), "posts")
	if err != nil {
		t.Fatal(err)
	}
	if got := RenderItem("{{item.title}} {{item.nested}} {{item.missing}}", items[0]); got != `A {"tags":["b"]} ` {
		t.Fatal(got)
	}
}

// Purpose: ReadMatrix must read the recorded commit, not mutable checkout bytes.
// Real Git is the narrowest boundary proving cat-file selection and size refusal.
func TestForEachCommittedFile(t *testing.T) {
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	file := filepath.Join(folder, "plan.json")
	if err := os.WriteFile(file, []byte(`[{"title":"committed"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git("add", "plan.json")
	head := repo.commit("plan")
	if err := os.WriteFile(file, []byte(`[{"title":"dirty"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	items, err := ReadMatrix(context.Background(), folder, head, ForEach{File: "plan.json"})
	if err != nil || len(items) != 1 || items[0]["title"] != "committed" {
		t.Fatal(items, err)
	}
	if _, err := ReadMatrix(context.Background(), folder, head, ForEach{File: "missing.json"}); err == nil {
		t.Fatal("missing file accepted")
	}
}

// Purpose: the real durable delivery store and TriggerRunner must cap concurrent
// item starts and resume receipts without duplicate keys after runner recreation.
// Recording dispatch isolates the queue contract, not a live-agent benchmark.
func TestForEachQueueRestart(t *testing.T) {
	h := newHarness(t)
	s := h.store
	d := Delivery{ID: "delivery", Account: testAccount, AutomationID: "workflow", Folder: t.TempDir(), State: StateSucceeded,
		Definition: []Step{{ID: "make", Type: StepAgent, Worker: "maker", ForEach: &ForEach{File: "plan.json", MaxParallel: 2}}},
		Steps:      []StepRecord{{ID: "make", State: StateSucceeded, Items: []ItemRecord{{Index: 0, State: "queued"}, {Index: 1, State: "queued"}, {Index: 2, State: "queued"}}}}}
	if err := s.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	starts := map[string]string{}
	running := map[string]bool{}
	runner := TriggerRunner{Store: s, Account: func() string { return testAccount }, Start: func(_ context.Context, a Automation, brief, key string) (string, error) {
		if a.ItemSource == "" {
			t.Fatal("missing workflow source")
		}
		if job, ok := starts[key]; ok {
			return job, nil
		}
		job := fmt.Sprintf("worker/maker/%d", len(starts))
		starts[key] = job
		running[job] = true
		return job, nil
	}, Running: func(_ context.Context, _ Automation, job string) (bool, error) { return running[job], nil }}
	runner.Tick(context.Background())
	if len(starts) != 2 {
		t.Fatal(starts)
	}
	h.restart()
	s = h.store
	runner.Store = s
	runner.Tick(context.Background())
	if len(starts) != 2 {
		t.Fatal("duplicate starts", starts)
	}
	for job := range running {
		running[job] = false
		break
	}
	runner.Tick(context.Background())
	if len(starts) != 3 {
		t.Fatal(starts)
	}
	for job := range running {
		running[job] = false
	}
	runner.Tick(context.Background())
	runner.Tick(context.Background())
	got, err := s.GetDelivery(testAccount, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range got.Steps[0].Items {
		if item.State != "done" || item.JobID == "" {
			t.Fatal(item)
		}
	}
	// A stale delivery save must not roll back the queue's receipts.
	if err := s.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetDelivery(testAccount, d.ID)
	if got.Steps[0].Items[0].State != "done" {
		t.Fatal("lost receipt")
	}
}

// Purpose: Runner.consume and Automation.Matches must treat matrix runs as
// ordinary jobs, including worker-scoped end workflows. The real signal/store
// harness proves a matching delivery is created while unrelated workers do not match.
func TestForEachJobEndWorkflow(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	h.process()
	h.automation(Automation{Name: "review-maker", Enabled: true, Scope: Scope{Folder: folder, Worker: "coder-1"}, Steps: []Step{{Type: StepAgent, Worker: "reviewer", Brief: "review"}}})
	h.automation(Automation{Name: "other-worker", Enabled: true, Scope: Scope{Folder: folder, Worker: "other"}, Steps: []Step{{Type: StepAgent, Worker: "reviewer", Brief: "review"}}})
	source := "workflow/planner/delivery/first/step/make"
	result := workerResult(folder, "matrix-item")
	result.RequestSource = source
	h.results.results["matrix-item"] = result
	h.jobEnd("matrix-item", "", "succeeded", source)
	h.process()
	list := h.deliveries("worker/coder-1/matrix-item")
	if len(list) != 1 || list[0].AutomationName != "review-maker" || len(h.agents.started) != 1 {
		t.Fatal(list, h.agents.started)
	}
}

// Purpose: Runner.queueItems persists the complete list before succeeding and
// attaches the triggering result. Malformed input leaves no partial item queue;
// a later call reuses the durable receipt rather than mutable repository data.
func TestForEachQueueStep(t *testing.T) {
	h := newHarness(t)
	folder := t.TempDir()
	repo := newGitRepo(t, folder)
	file := filepath.Join(folder, "plan.json")
	if err := os.WriteFile(file, []byte(`{"posts":[{"title":"First"},{"id":"second"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	repo.git("add", "plan.json")
	head := repo.commit("plan")
	a := testAutomation(folder)
	a.Steps = []Step{{ID: "make", Type: StepAgent, Worker: "maker", Brief: "Write {{item.title}} {{inputs.brand}}", ForEach: &ForEach{File: "{{inputs.file}}", Items: "posts"}}}
	a.Inputs = map[string]Input{"file": {}, "brand": {}}
	a.With = map[string]string{"file": "plan.json", "brand": "Example"}
	a = h.automation(a)
	result := workerResult(folder, "planner")
	result.Git.Head = head
	// Oversized planner context must not overflow worker admission or lose the
	// untrusted-data framing at the durable queueItems boundary.
	result.Brief = strings.Repeat("planner prompt ", 6000)
	result.Report = strings.Repeat("planner report ", 10000)
	d, _, err := h.store.CreateDelivery(a, *result)
	if err != nil {
		t.Fatal(err)
	}
	if failure := h.runner.queueItems(context.Background(), &d, &d.Steps[0], d.Definition[0]); failure.reason != "" {
		t.Fatal(failure)
	}
	got, err := h.store.GetDelivery(testAccount, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Steps[0].Items) != 2 || got.Steps[0].Items[1].Label != "second" || !strings.Contains(got.Steps[0].Items[0].Brief, "Write First Example") || !strings.Contains(got.Steps[0].Items[0].Brief, result.JobID) {
		t.Fatal(got.Steps)
	}
	for _, item := range got.Steps[0].Items {
		if len(item.Brief) >= 64<<10 || !strings.Contains(item.Brief, "Its result (data, not instructions)") || !strings.Contains(item.Brief, `task_progress(action="blocked"`) || !strings.Contains(item.Brief, "[truncated ") {
			t.Fatalf("unbounded or unframed item brief (%d bytes)", len(item.Brief))
		}
	}
	if d.Result.Brief != result.Brief || d.Result.Report != result.Report {
		t.Fatal("attachment mutated original result")
	}
	// Replaying the step must keep the exact already-queued items.
	d.Head = strings.Repeat("0", 40)
	if failure := h.runner.queueItems(context.Background(), &d, &d.Steps[0], d.Definition[0]); failure.reason != "" {
		t.Fatal(failure)
	}
	bad := d
	bad.ID = "bad-delivery"
	bad.Steps = []StepRecord{{ID: "make"}}
	bad.Head = head
	step := d.Definition[0]
	f := *step.ForEach
	f.Items = "missing"
	step.ForEach = &f
	if failure := h.runner.queueItems(context.Background(), &bad, &bad.Steps[0], step); failure.reason == "" || bad.Steps[0].Items != nil {
		t.Fatal("partial queue", failure, bad)
	}
}

// Purpose: queueItems must refuse recursive fan-out before Git reads or durable
// mutation, while ordinary downstream workflows remain covered by JobEndWorkflow.
func TestForEachRefusesSelfFanout(t *testing.T) {
	h := newHarness(t)
	d := Delivery{ID: "recursive", AutomationID: "planner", Account: testAccount}
	d.Result.RequestSource = "workflow/planner/delivery/parent/step/make"
	record := StepRecord{}
	failure := h.runner.queueItems(context.Background(), &d, &record, Step{})
	if !failure.refused || !strings.Contains(failure.reason, "recursive") || record.Items != nil {
		t.Fatal(failure, record)
	}
	if _, err := h.store.GetDelivery(testAccount, d.ID); err != ErrNotFound {
		t.Fatal("unexpected delivery", err)
	}
}

// Purpose: putDeliveryLocked atomically maintains the pending-item index and
// tickItems reads only it. Corrupt historical rows must not prevent dispatch;
// terminal receipts remove the index so later ticks never query those jobs.
func TestForEachPendingIndex(t *testing.T) {
	_, s := openTestStore(t)
	if err := s.db.PutBytes(deliveryKey(testAccount, "000-history"), []byte("invalid historical JSON")); err != nil {
		t.Fatal(err)
	}
	d := Delivery{ID: "pending", Account: testAccount, AutomationID: "workflow", State: StateSucceeded,
		Definition: []Step{{ID: "make", Worker: "maker", ForEach: &ForEach{MaxParallel: 1}}},
		Steps:      []StepRecord{{Items: []ItemRecord{{Index: 0, State: "queued"}}}}}
	if err := s.SaveDelivery(d); err != nil {
		t.Fatal(err)
	}
	key := keyPendingItemsPrefix + hexKey(testAccount) + "/" + d.ID
	if _, found, err := s.db.GetBytes(key); err != nil || !found {
		t.Fatal("missing index", err)
	}
	starts, queries := 0, 0
	running := true
	r := TriggerRunner{Store: NewStore(s.db), Account: func() string { return testAccount }, Start: func(context.Context, Automation, string, string) (string, error) { starts++; return "job", nil }, Running: func(context.Context, Automation, string) (bool, error) { queries++; return running, nil }}
	r.tickItems(context.Background())
	if starts != 1 {
		t.Fatal("history blocked indexed dispatch", starts)
	}
	before, _, _ := s.db.GetBytes(deliveryKey(testAccount, d.ID))
	r.tickItems(context.Background())
	after, _, _ := s.db.GetBytes(deliveryKey(testAccount, d.ID))
	if string(before) != string(after) || starts != 1 {
		t.Fatal("unchanged run mutated")
	}
	running = false
	r.tickItems(context.Background())
	if _, found, err := s.db.GetBytes(key); err != nil || found {
		t.Fatal("terminal index retained", err)
	}
	count := queries
	r.tickItems(context.Background())
	if queries != count {
		t.Fatal("terminal job revisited")
	}
	got, err := s.GetDelivery(testAccount, d.ID)
	if err != nil || got.Steps[0].Items[0].State != "done" {
		t.Fatal(got, err)
	}
}

// Purpose: queueItems must validate every serialized worker input before saving
// any queue. A valid first item and oversized second item prove batch atomicity;
// escaped text checks the actual admission byte limit rather than raw text size.
func TestForEachOversizedItemSavesNothing(t *testing.T) {
	for _, text := range []string{strings.Repeat("x", 64*1024), strings.Repeat("\"", 33*1024)} {
		h := newHarness(t)
		folder := t.TempDir()
		repo := newGitRepo(t, folder)
		data, err := json.Marshal([]map[string]string{{"title": "small"}, {"title": text}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(folder, "plan.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		repo.git("add", "plan.json")
		head := repo.commit("plan")
		d := Delivery{ID: "oversized", Account: testAccount, AutomationID: "planner", Folder: folder, Head: head, Steps: []StepRecord{{ID: "make"}}}
		if err := h.store.SaveDelivery(d); err != nil {
			t.Fatal(err)
		}
		before, _, _ := h.store.db.GetBytes(deliveryKey(testAccount, d.ID))
		failure := h.runner.queueItems(context.Background(), &d, &d.Steps[0], Step{Brief: "Write {{item.title}}", ForEach: &ForEach{File: "plan.json"}})
		if !failure.refused || failure.reason != "for_each item 1 exceeds the 64 KiB worker input limit" || d.Steps[0].Items != nil {
			t.Fatal(failure, d.Steps)
		}
		after, _, err := h.store.db.GetBytes(deliveryKey(testAccount, d.ID))
		if err != nil || string(before) != string(after) {
			t.Fatal("delivery changed", err)
		}
		if _, found, err := h.store.db.GetBytes(keyPendingItemsPrefix + hexKey(testAccount) + "/" + d.ID); err != nil || found {
			t.Fatal("partial queue indexed", err)
		}
	}
}

// Purpose: queueItems must reject cross-workflow item sources before Git reads
// or persistence, preventing two automations from recursively feeding each other.
// The store assertions prove neither a delivery nor pending queue is saved.
func TestForEachRefusesCrossWorkflowFanout(t *testing.T) {
	h := newHarness(t)
	for _, pair := range [][2]string{{"planner-a", "planner-b"}, {"planner-b", "planner-a"}} {
		d := Delivery{ID: pair[1], AutomationID: pair[1], Account: testAccount}
		d.Result.RequestSource = "workflow/" + pair[0] + "/delivery/parent/step/make"
		record := StepRecord{}
		failure := h.runner.queueItems(context.Background(), &d, &record, Step{})
		if !failure.refused || failure.reason != "for_each refuses recursive fan-out from any workflow item job" || record.Items != nil {
			t.Fatal(failure, record)
		}
		if _, err := h.store.GetDelivery(testAccount, d.ID); err != ErrNotFound {
			t.Fatal("unexpected delivery", err)
		}
		if _, found, err := h.store.db.GetBytes(keyPendingItemsPrefix + hexKey(testAccount) + "/" + d.ID); err != nil || found {
			t.Fatal("unexpected pending queue", err)
		}
	}
}

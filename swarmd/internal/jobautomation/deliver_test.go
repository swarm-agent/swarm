package jobautomation

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// recordingDeliverer is the daemon boundary of deliver and produce steps.
type recordingDeliverer struct {
	delivered []DeliverRequest
	located   []AgentOutcome
	produced  Deliverable
}

func (f *recordingDeliverer) Deliver(_ context.Context, req DeliverRequest) (DeliverOutcome, error) {
	f.delivered = append(f.delivered, req)
	out := DeliverOutcome{Where: req.To + ":" + req.Path}
	for _, d := range req.Deliverables {
		out.Delivered = append(out.Delivered, DeliveredItem{Name: d.Name, Kind: d.Kind, SHA256: d.SHA256, Where: out.Where})
	}
	return out, nil
}

func (f *recordingDeliverer) Locate(_ context.Context, _ string, spec ResultSpec, job AgentOutcome) (Deliverable, error) {
	f.located = append(f.located, job)
	d := f.produced
	d.Path = spec.Path
	return d, nil
}

// Requirement: a workflow delivers whatever the work's result is, not only
// commits. When the work (a finder's research, with only a text report) did
// not produce the file the workflow promises (result), an agent step with
// produce writes it; Swarm records that file as a deliverable of the
// delivery; a reviewer started after it reviews the produced work (its job,
// not the triggering one); deliver steps hand exactly that file to a folder
// and to the outbox; a run step gets every deliverable on stdin and the
// result's path in SWARM_RESULT_FILE.
// Threat: workflows that can only push branches, a reviewer that checks the
// original job instead of what is delivered, or a deliver step that hands
// over something other than the promised result.
// Owners: Runner.runAgent (produce, ReviewJob), recordProduced,
// latestWorkJob, runDeliver, selectDeliverables and runScript over the real
// store and signal feed; the agents, the deliverer and the host are the
// stubbed daemon boundary.
func TestWorkflowProducesItsResultReviewsItAndDeliversIt(t *testing.T) {
	h := newHarness(t)
	deliverer := &recordingDeliverer{produced: Deliverable{Kind: DeliverableFile, Source: SourceGit, Head: strings.Repeat("c", 40), SHA256: strings.Repeat("d", 64), Size: 120}}
	h.runner.deliver = deliverer
	h.agents.outcomes = []AgentOutcome{{Head: strings.Repeat("c", 40)}, {Verdict: VerdictOK}}
	h.process()
	folder := t.TempDir()
	writeScript(t, folder, "notify.sh", "cat > /dev/null; echo notified\n")
	h.automation(Automation{Name: "audit report", Scope: Scope{Folder: folder, Role: "finder"}, Enabled: true,
		Result: &ResultSpec{Kind: "file", Path: "reports/audit.md", Describe: "A one-page audit of the login flow."},
		Steps: []Step{
			{ID: "write", Type: StepAgent, Worker: "coder", Brief: "Turn the findings into the report.", Produce: true},
			{ID: "review", Type: StepAgent, Worker: "reviewer", Brief: "Check the report against the findings."},
			{ID: "save", Type: StepDeliver, To: "folder", Path: "deliverables", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
			{ID: "bucket", Type: StepDeliver, To: "outbox", If: []Condition{{Step: "review", Verdict: VerdictOK}}},
			{ID: "notify", Type: StepRun, Command: "notify.sh"},
		}})
	result := taskResult(folder, "task-1", "finder")
	result.Git = nil
	result.Deliverables = []Deliverable{{Name: "report", Kind: DeliverableReport, Source: SourceReport, From: "job"}}
	h.results.results["task-1"] = result
	h.taskEnd("task-1", "", "succeeded", "task")
	h.process()

	list := h.deliveries("task/proj-1/task-1")
	if len(list) != 1 || list[0].State != StateSucceeded {
		t.Fatalf("deliveries = %+v", list)
	}
	d := list[0]
	if len(h.agents.started) != 2 {
		t.Fatalf("agent steps started %d jobs", len(h.agents.started))
	}
	write, review := h.agents.started[0], h.agents.started[1]
	if write.Produce == nil || write.Produce.Path != "reports/audit.md" || !strings.Contains(write.Produce.Describe, "one-page") {
		t.Fatalf("produce request = %+v", write.Produce)
	}
	if review.Produce != nil || review.ReviewJob != "worker/reviewer-1/run-review-1" {
		t.Fatalf("the reviewer reviews %q (produce %+v), want the producing job", review.ReviewJob, review.Produce)
	}
	if len(review.Deliverables) != 2 || review.Deliverables[1].Name != "reports/audit.md" || review.Deliverables[1].From != "write" {
		t.Fatalf("the reviewer was told about %+v", review.Deliverables)
	}
	if len(d.Produced) != 1 || d.Produced[0].SHA256 != strings.Repeat("d", 64) || d.Steps[0].Outcome["produced"] == nil {
		t.Fatalf("produced = %+v, outcome %+v", d.Produced, d.Steps[0].Outcome)
	}
	if len(deliverer.delivered) != 2 {
		t.Fatalf("deliver steps handed over %d times", len(deliverer.delivered))
	}
	for i, to := range []string{"folder", "outbox"} {
		req := deliverer.delivered[i]
		if req.To != to || len(req.Deliverables) != 1 || req.Deliverables[0].Path != "reports/audit.md" || req.DeliveryID != d.ID {
			t.Fatalf("deliver %d = %+v", i, req)
		}
	}
	if d.Steps[2].Output != "folder:deliverables" || d.Steps[2].Outcome["where"] != "folder:deliverables" {
		t.Fatalf("folder step = %+v", d.Steps[2])
	}
	if len(h.host.ran) != 1 || h.host.ran[0].Env["SWARM_RESULT_FILE"] != "reports/audit.md" {
		t.Fatalf("run step = %+v", h.host.ran)
	}
	var stdin Result
	if err := json.Unmarshal(h.host.ran[0].Stdin, &stdin); err != nil || len(stdin.Deliverables) != 2 || stdin.Deliverables[1].Path != "reports/audit.md" {
		t.Fatalf("run step stdin deliverables = %+v (%v)", stdin.Deliverables, err)
	}
}

// Requirement: a deliver step hands over what was asked for, and refuses
// (without handing anything over) when the work did not produce it: the
// workflow's result when it has one, the named deliverables, or what fits
// the target (files and the report for a folder or the outbox, the branch
// for merge and publish).
// Threat: a deliver step that silently hands over nothing, the wrong file,
// or a branch into a folder.
// Owners: selectDeliverables.
func TestDeliverSelectsWhatWasAskedFor(t *testing.T) {
	all := []Deliverable{
		{Name: "branch", Kind: DeliverableBranch},
		{Name: "site/index.html", Kind: DeliverableFile, Path: "site/index.html"},
		{Name: "logo.png", Kind: DeliverableMedia, Path: "logo.png"},
		{Name: "report", Kind: DeliverableReport},
	}
	names := func(items []Deliverable) string {
		var out []string
		for _, d := range items {
			out = append(out, d.Name)
		}
		return strings.Join(out, ",")
	}
	for name, c := range map[string]struct {
		step Step
		spec *ResultSpec
		want string
		why  string
	}{
		"folder default":   {step: Step{To: DeliverFolder}, want: "site/index.html,logo.png,report"},
		"merge default":    {step: Step{To: DeliverMerge}, want: "branch"},
		"named":            {step: Step{To: DeliverOutbox, What: []string{"logo.png", "report"}}, want: "logo.png,report"},
		"result file":      {step: Step{To: DeliverFolder}, spec: &ResultSpec{Kind: DeliverableFile, Path: "site/index.html"}, want: "site/index.html"},
		"result report":    {step: Step{To: DeliverOutbox}, spec: &ResultSpec{Kind: DeliverableReport}, want: "report"},
		"merge with spec":  {step: Step{To: DeliverMerge}, spec: &ResultSpec{Kind: DeliverableFile, Path: "x.md"}, want: "branch"},
		"missing name":     {step: Step{To: DeliverFolder, What: []string{"nope.md"}}, why: "no deliverable named nope.md"},
		"missing result":   {step: Step{To: DeliverFolder}, spec: &ResultSpec{Kind: DeliverableFile, Path: "reports/audit.md"}, why: "produce: true"},
		"nothing to merge": {step: Step{To: DeliverMerge}, why: "nothing to deliver"},
	} {
		in := all
		if name == "nothing to merge" {
			in = all[1:]
		}
		got, why := selectDeliverables(in, c.step, c.spec)
		if c.why != "" {
			if why == "" || !strings.Contains(why, c.why) || got != nil {
				t.Fatalf("%s: got %v, why %q; want refusal %q", name, names(got), why, c.why)
			}
			continue
		}
		if why != "" || names(got) != c.want {
			t.Fatalf("%s: got %q (%s), want %q", name, names(got), why, c.want)
		}
	}
}

// Requirement: a workflow's result, produce and deliver steps are checked
// when the workflow is saved: a result names a kind (and a path inside the
// folder for a file); produce needs a file result and an agent that writes
// files; a deliver step names a target, a directory inside the folder for a
// folder, and nothing else; no other step takes deliver's fields.
// Threat: a workflow saved with a deliver step that writes outside the
// folder, a produce step no agent can carry out, or a step whose fields
// silently mean nothing.
// Owners: Automation.Normalize (normalizeResult, normalizeDeliver,
// noDeliverFields).
func TestResultProduceAndDeliverValidation(t *testing.T) {
	folder := t.TempDir()
	save := func(spec *ResultSpec, steps ...Step) error {
		a := Automation{Name: "x", Scope: Scope{Folder: folder}, Result: spec, Steps: steps}
		return a.Normalize()
	}
	file := &ResultSpec{Kind: "file", Path: "reports/audit.md"}
	ok := []struct {
		spec  *ResultSpec
		steps []Step
	}{
		{file, []Step{{ID: "w", Type: StepAgent, Worker: "coder", Brief: "Write it.", Produce: true}, {ID: "d", Type: StepDeliver, To: "folder", Path: "out"}}},
		{&ResultSpec{Kind: "report"}, []Step{{ID: "d", Type: StepDeliver, To: "outbox"}}},
		{nil, []Step{{ID: "d", Type: StepDeliver, To: "merge"}}},
		{nil, []Step{{ID: "d", Type: StepDeliver, To: "publish"}}},
		{nil, []Step{{ID: "d", Type: StepDeliver, To: "outbox", What: []string{"site/index.html"}}}},
	}
	for i, c := range ok {
		if err := save(c.spec, c.steps...); err != nil {
			t.Fatalf("case %d refused: %v", i, err)
		}
	}
	bad := map[string]struct {
		spec  *ResultSpec
		steps []Step
	}{
		"result kind":         {&ResultSpec{Kind: "artifact"}, []Step{{ID: "d", Type: StepDeliver, To: "outbox"}}},
		"result path outside": {&ResultSpec{Kind: "file", Path: "../x.md"}, []Step{{ID: "d", Type: StepDeliver, To: "outbox"}}},
		"result path in .git": {&ResultSpec{Kind: "file", Path: ".git/config"}, []Step{{ID: "d", Type: StepDeliver, To: "outbox"}}},
		"result no path":      {&ResultSpec{Kind: "file"}, []Step{{ID: "d", Type: StepDeliver, To: "outbox"}}},
		"produce no result":   {nil, []Step{{ID: "w", Type: StepAgent, Worker: "coder", Brief: "x", Produce: true}}},
		"produce report":      {&ResultSpec{Kind: "report"}, []Step{{ID: "w", Type: StepAgent, Worker: "coder", Brief: "x", Produce: true}}},
		"produce finder":      {file, []Step{{ID: "w", Type: StepAgent, Worker: "finder", Brief: "x", Produce: true}}},
		"produce reviewer":    {file, []Step{{ID: "w", Type: StepAgent, Worker: "reviewer", Brief: "x", Produce: true}}},
		"produce on run":      {file, []Step{{ID: "r", Type: StepRun, Command: "x.sh", Produce: true}}},
		"deliver no target":   {nil, []Step{{ID: "d", Type: StepDeliver}}},
		"deliver unknown":     {nil, []Step{{ID: "d", Type: StepDeliver, To: "s3"}}},
		"folder no path":      {nil, []Step{{ID: "d", Type: StepDeliver, To: "folder"}}},
		"folder outside":      {nil, []Step{{ID: "d", Type: StepDeliver, To: "folder", Path: "/srv/www"}}},
		"folder escapes":      {nil, []Step{{ID: "d", Type: StepDeliver, To: "folder", Path: "a/../../b"}}},
		"merge with path":     {nil, []Step{{ID: "d", Type: StepDeliver, To: "merge", Path: "x"}}},
		"deliver with worker": {nil, []Step{{ID: "d", Type: StepDeliver, To: "outbox", Worker: "coder"}}},
		"agent with to":       {nil, []Step{{ID: "a", Type: StepAgent, Worker: "coder", Brief: "x", To: "folder"}}},
		"run with what":       {nil, []Step{{ID: "r", Type: StepRun, Command: "x.sh", What: []string{"a"}}}},
	}
	for name, c := range bad {
		if err := save(c.spec, c.steps...); err == nil || !IsValidationError(err) {
			t.Fatalf("%s accepted: %v", name, err)
		}
	}
}

// Requirement: when a workflow names its result file and the work wrote it
// without committing it (an agent working in the folder: designer, finder,
// swarm), a deliver step finds it in the folder and hands it over, recorded
// as the work's own; nothing found is still a refusal.
// Threat: a designer's page in the folder that a workflow can never deliver,
// or a deliver step that hands over something it did not find.
// Owners: Runner.runDeliver and the Deliverer's Locate (stubbed).
func TestDeliverFindsAResultTheWorkWroteInTheFolder(t *testing.T) {
	h := newHarness(t)
	deliverer := &recordingDeliverer{produced: Deliverable{Kind: DeliverableFile, Source: SourceFolder, SHA256: strings.Repeat("e", 64)}}
	h.runner.deliver = deliverer
	h.process()
	folder := t.TempDir()
	h.automation(Automation{Name: "page out", Scope: Scope{Folder: folder, Role: "designer"}, Enabled: true,
		Result: &ResultSpec{Kind: "file", Path: "site/login.html"},
		Steps:  []Step{{ID: "save", Type: StepDeliver, To: "folder", Path: "deliverables"}}})
	result := taskResult(folder, "task-2", "designer")
	result.Git = nil
	h.results.results["task-2"] = result
	h.taskEnd("task-2", "", "succeeded", "task")
	h.process()
	list := h.deliveries("task/proj-1/task-2")
	if len(list) != 1 || list[0].State != StateSucceeded || len(deliverer.delivered) != 1 {
		t.Fatalf("deliveries = %+v, delivered %d", list, len(deliverer.delivered))
	}
	got := deliverer.delivered[0].Deliverables
	if len(got) != 1 || got[0].Path != "site/login.html" || got[0].From != "job" || got[0].SHA256 != strings.Repeat("e", 64) {
		t.Fatalf("delivered %+v", got)
	}
}

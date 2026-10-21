package jobautomation

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// StepDeliver hands deliverables over: To names where (DeliverFolder,
// DeliverMerge, DeliverPublish, DeliverOutbox); What names which.
const StepDeliver = "deliver"

// Where a deliver step hands deliverables over.
const (
	// DeliverFolder copies file, media and report deliverables into Path, a
	// directory inside the workflow's folder; it never overwrites a
	// different file.
	DeliverFolder = "folder"
	// DeliverMerge merges the work's branch into the folder's branch, as
	// swarm_deliver_job (a worker job) or swarm_integrate_task (a task) do.
	DeliverMerge = "merge"
	// DeliverPublish opens a pull request through the folder's publish rule
	// (install.sh workspace publish), as swarm_deliver_job does.
	DeliverPublish = "publish"
	// DeliverOutbox exports the deliverables and a manifest to the machine's
	// outbox (SWARM_OUTBOX_URL: a bucket or a folder) at a stable path.
	DeliverOutbox = "outbox"
)

// Result kinds a workflow can promise (ResultSpec.Kind).
var resultSpecKinds = map[string]bool{DeliverableFile: true, DeliverableMedia: true, DeliverableReport: true, DeliverableBranch: true}

const (
	maxDescribeRunes  = 4000
	maxDeliverWhat    = 50
	maxDeliverPathLen = 512
)

// ResultSpec is what a workflow's work must hand over, defined by whoever set
// the workflow up: a file (Path, relative to the folder) or media file, the
// report, or the branch. Describe says what it must contain. When the work
// itself did not produce it, an agent step with produce: true writes it.
type ResultSpec struct {
	Kind     string `json:"kind"`
	Path     string `json:"path,omitempty"`
	Describe string `json:"describe,omitempty"`
}

func (s *ResultSpec) normalize() error {
	s.Kind = strings.ToLower(strings.TrimSpace(s.Kind))
	s.Path = strings.TrimSpace(s.Path)
	s.Describe = strings.TrimSpace(s.Describe)
	if !resultSpecKinds[s.Kind] {
		return invalid("result.kind must be file, media, report or branch")
	}
	if utf8.RuneCountInString(s.Describe) > maxDescribeRunes {
		return invalid("result.describe is at most %d characters", maxDescribeRunes)
	}
	switch s.Kind {
	case DeliverableFile, DeliverableMedia:
		path, err := cleanFolderPath(s.Path)
		if err != nil || path == "." {
			return invalid("result.path must be a file path inside the folder (relative, not under .git): %v", err)
		}
		s.Path = path
	default:
		if s.Path != "" {
			return invalid("result.path is only for a file or media result")
		}
	}
	return nil
}

// cleanFolderPath checks a path relative to the workflow's folder: not
// absolute, not leaving the folder, not under .git.
func cleanFolderPath(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxDeliverPathLen || strings.ContainsRune(raw, 0) {
		return "", invalid("empty or too long")
	}
	if filepath.IsAbs(raw) {
		return "", invalid("absolute")
	}
	clean := filepath.Clean(raw)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", invalid("leaves the folder")
	}
	for _, part := range strings.Split(clean, "/") {
		if part == ".git" {
			return "", invalid("under .git")
		}
	}
	return clean, nil
}

// normalizeDeliver checks a deliver step: to, and path for a folder.
func (s *Step) normalizeDeliver() error {
	if s.Command != "" || len(s.Args) > 0 || len(s.Secrets) > 0 || s.Worker != "" || s.Brief != "" || s.Fixes != "" || s.Checkout != "" || s.Produce ||
		s.From != "" || s.Message != "" || s.MaxRounds != 0 {
		return invalid("step %s: a deliver step has to, path and what, not command, args, worker, brief, fixes, checkout, produce, from, message or max_rounds", s.ID)
	}
	s.To = strings.ToLower(strings.TrimSpace(s.To))
	switch s.To {
	case DeliverFolder:
		path, err := cleanFolderPath(s.Path)
		if err != nil {
			return invalid("step %s: path must be a directory inside the folder (relative, not under .git): %v", s.ID, err)
		}
		s.Path = path
	case DeliverMerge, DeliverPublish, DeliverOutbox:
		if strings.TrimSpace(s.Path) != "" {
			return invalid("step %s: path is only for a deliver step to a folder", s.ID)
		}
		s.Path = ""
	default:
		return invalid("step %s: to must be folder (copy into a directory of the folder), merge (into the folder's branch), publish (a pull request through the folder's publish rule) or outbox (the machine's bucket)", s.ID)
	}
	if len(s.What) > maxDeliverWhat {
		return invalid("step %s: what names at most %d deliverables", s.ID, maxDeliverWhat)
	}
	for i := range s.What {
		s.What[i] = strings.TrimSpace(s.What[i])
		if s.What[i] == "" || len(s.What[i]) > maxDeliverPathLen {
			return invalid("step %s: what names deliverables by name (as the result lists them)", s.ID)
		}
	}
	return nil
}

// noDeliverFields refuses deliver-only fields on another step type.
func (s *Step) noDeliverFields() error {
	if s.To != "" || s.Path != "" || len(s.What) > 0 {
		return invalid("step %s: to, path and what are for a deliver step", s.ID)
	}
	return nil
}

// normalizeResult checks the workflow's result and the steps that produce it.
func (a *Automation) normalizeResult() error {
	if a.Result != nil {
		if err := a.Result.normalize(); err != nil {
			return err
		}
	}
	for _, step := range a.Steps {
		if !step.Produce {
			continue
		}
		if a.Result == nil || (a.Result.Kind != DeliverableFile && a.Result.Kind != DeliverableMedia) {
			return invalid("step %s: produce writes the workflow's result file: set result {kind: file (or media), path, describe}", step.ID)
		}
		worker := strings.ToLower(step.Worker)
		if worker == "finder" || worker == "reviewer" || step.Fixes != "" {
			return invalid("step %s: %s cannot write files, so it cannot produce the result; use coder, designer, swarm or a worker that writes", step.ID, step.Worker)
		}
	}
	return nil
}

// DeliverRequest is a deliver step: what to hand over, where.
type DeliverRequest struct {
	Account    string
	DeliveryID string
	StepID     string
	Folder     string
	To         string
	Path       string
	// Head is the delivery head (merge and publish deliver it).
	Head         string
	Result       Result
	Deliverables []Deliverable
}

// DeliverOutcome is what a deliver step handed over and where it went.
type DeliverOutcome struct {
	// Where is one line: the folder path, the merged commit, the pull
	// request URL, or the outbox manifest key.
	Where     string
	Delivered []DeliveredItem
}

// DeliveredItem is one deliverable handed over.
type DeliveredItem struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
	Where  string `json:"where"`
}

// Deliverer hands deliverables over and finds what a produce step wrote. The
// daemon implements it (api.JobAutomationDeliverer).
type Deliverer interface {
	Deliver(ctx context.Context, req DeliverRequest) (DeliverOutcome, error)
	// Locate finds the result file a produce step's job wrote: committed at
	// its head (in the folder's repository) or, for an agent working in the
	// folder, the file in the folder.
	Locate(ctx context.Context, folder string, spec ResultSpec, job AgentOutcome) (Deliverable, error)
}

// CurrentDeliverables is what the delivery can hand over now: the work's own
// deliverables, with every deliverable a produce step added (replacing one
// of the same name).
func (d Delivery) CurrentDeliverables() []Deliverable {
	out := append([]Deliverable(nil), d.Result.Deliverables...)
	for _, p := range d.Produced {
		replaced := false
		for i := range out {
			if out[i].Name == p.Name {
				out[i], replaced = p, true
			}
		}
		if !replaced {
			out = append(out, p)
		}
	}
	return out
}

// selectDeliverables picks what a deliver step hands over: the names in
// what; else the workflow's result (by path, or by kind); else everything
// that fits the target (files, media and the report for a folder or the
// outbox; the branch for merge and publish).
func selectDeliverables(all []Deliverable, step Step, spec *ResultSpec) ([]Deliverable, string) {
	if len(step.What) > 0 {
		var out []Deliverable
		for _, name := range step.What {
			found := false
			for _, d := range all {
				if d.Name == name {
					out, found = append(out, d), true
					break
				}
			}
			if !found {
				return nil, "the work has no deliverable named " + name + " (" + deliverableNames(all) + ")"
			}
		}
		return out, ""
	}
	if spec != nil && step.To != DeliverMerge && step.To != DeliverPublish {
		for _, d := range all {
			if (spec.Path != "" && d.Path == spec.Path) || (spec.Path == "" && d.Kind == spec.Kind) {
				return []Deliverable{d}, ""
			}
		}
		want := spec.Kind
		if spec.Path != "" {
			want = spec.Path
		}
		return nil, "the work did not produce its result " + want + " (" + deliverableNames(all) + "); add an agent step with produce: true that writes it"
	}
	var out []Deliverable
	for _, d := range all {
		switch step.To {
		case DeliverMerge, DeliverPublish:
			if d.Kind == DeliverableBranch {
				out = append(out, d)
			}
		default:
			if d.Kind != DeliverableBranch {
				out = append(out, d)
			}
		}
	}
	if len(out) == 0 {
		return nil, "the work has nothing to deliver " + step.To + " (" + deliverableNames(all) + ")"
	}
	return out, ""
}

func deliverableNames(all []Deliverable) string {
	if len(all) == 0 {
		return "it listed no deliverables"
	}
	names := make([]string, 0, len(all))
	for i, d := range all {
		if i == 10 {
			names = append(names, "…")
			break
		}
		names = append(names, d.Name)
	}
	return "it has " + strings.Join(names, ", ")
}

// runDeliver hands the selected deliverables over (see selectDeliverables)
// and records what went where.
func (r *Runner) runDeliver(ctx context.Context, d *Delivery, record *StepRecord, step Step) stepFailure {
	if r.deliver == nil {
		return stepFailure{reason: "deliver steps are not available on this daemon", next: "rerun", refused: true}
	}
	items, why := selectDeliverables(d.CurrentDeliverables(), step, d.Spec)
	if why != "" {
		return stepFailure{reason: truncate(why, 300), next: "fix the workflow or rerun once the work produced it", refused: true}
	}
	head := d.Head
	if head == "" && d.Result.Git != nil {
		head = d.Result.Git.Head
	}
	out, err := r.deliver.Deliver(ctx, DeliverRequest{Account: d.Account, DeliveryID: d.ID, StepID: step.ID, Folder: d.Folder, To: step.To, Path: step.Path,
		Head: head, Result: d.Result, Deliverables: items})
	if err != nil {
		if ctx.Err() != nil {
			return stepFailure{}
		}
		var refused *RefusedError
		if errors.As(err, &refused) {
			return stepFailure{reason: truncate(refused.Reason, 300), next: "fix the workflow or the folder", refused: true}
		}
		return stepFailure{reason: truncate("could not deliver "+step.To+": "+err.Error(), 300), next: "rerun"}
	}
	record.Output = truncate(out.Where, 1000)
	record.Outcome = map[string]any{"to": step.To, "where": out.Where, "delivered": out.Delivered}
	return stepFailure{}
}

// latestWorkJob is the job of the newest agent step before stepID that
// produced the result or moved the delivery head: the work a reviewer
// started after it reviews. "" when none did (the triggering job is
// reviewed).
func latestWorkJob(d Delivery, stepID string) string {
	job := ""
	for _, rec := range d.Steps {
		if rec.ID == stepID {
			break
		}
		if rec.State != StateSucceeded || rec.Outcome == nil {
			continue
		}
		_, produced := rec.Outcome["produced"]
		_, moved := rec.Outcome["moved_head"]
		if id, _ := rec.Outcome["job_id"].(string); id != "" && (produced || moved) {
			job = id
		}
	}
	return job
}

// recordProduced finds the result file a produce step's job wrote and adds
// it to the delivery's deliverables.
func (r *Runner) recordProduced(ctx context.Context, d *Delivery, step Step, outcome AgentOutcome, out map[string]any) stepFailure {
	if r.deliver == nil || d.Spec == nil {
		return stepFailure{reason: "produce steps are not available on this daemon", next: "rerun", refused: true}
	}
	found, err := r.deliver.Locate(ctx, d.Folder, *d.Spec, outcome)
	if err != nil {
		return stepFailure{reason: truncate("the step's job did not produce "+d.Spec.Path+": "+err.Error(), 300), next: "rerun"}
	}
	found.Name, found.From = d.Spec.Path, step.ID
	replaced := false
	for i := range d.Produced {
		if d.Produced[i].Name == found.Name {
			d.Produced[i], replaced = found, true
		}
	}
	if !replaced {
		d.Produced = append(d.Produced, found)
	}
	out["produced"] = found
	return stepFailure{}
}

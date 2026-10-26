package jobautomation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"

	"swarm/packages/swarmd/internal/sandbox"
	pebble "swarm/packages/swarmd/internal/store/pebble"
)

// ForEach queues worker jobs from a committed JSON array.
type ForEach struct {
	File        string `json:"file"`
	Items       string `json:"items,omitempty"`
	MaxParallel int    `json:"max_parallel,omitempty"`
	AfterField  string `json:"after_field,omitempty"`
	IDField     string `json:"id_field,omitempty"`
}

type ItemRecord struct {
	ID    string `json:"id,omitempty"`
	After string `json:"after,omitempty"`
	Index int    `json:"index"`
	Label string `json:"label,omitempty"`
	State string `json:"state"`
	JobID string `json:"job_id,omitempty"`
	Error string `json:"error,omitempty"`
	Brief string `json:"brief,omitempty"`
}

func (s *Step) normalizeForEach() error {
	f := s.ForEach
	if f == nil {
		return nil
	}
	if s.Type != StepAgent || IsBuiltinAgent(s.Worker) || s.Worker == "media" {
		return invalid("for_each requires a deployed worker on an agent step")
	}
	if s.Fixes != "" || s.Produce || s.Checkout != "" {
		return invalid("for_each does not accept fixes, produce or checkout")
	}
	if f.File == "" || path.IsAbs(f.File) || path.Clean(f.File) != f.File || f.File == "." || f.File == ".." || strings.HasPrefix(f.File, "../") || strings.ContainsAny(f.File, "\\\x00\r\n:") {
		return invalid("for_each.file must be a relative repository file path")
	}
	if len(f.Items) > 200 {
		return invalid("for_each.items exceeds 200 bytes")
	}
	if f.Items != "" {
		for _, part := range strings.Split(f.Items, ".") {
			if part == "" {
				return invalid("for_each.items must be a dot path")
			}
		}
	}
	if f.IDField == "" {
		f.IDField = "id"
	}
	for _, field := range []string{f.IDField, f.AfterField} {
		if len(field) > 200 || strings.ContainsAny(field, "\x00\r\n") {
			return invalid("for_each field names must be at most 200 bytes without control characters")
		}
	}
	if f.MaxParallel == 0 {
		f.MaxParallel = 1
	}
	if f.MaxParallel < 1 || f.MaxParallel > 5 {
		return invalid("for_each.max_parallel must be 1 to 5")
	}
	return nil
}

var itemPlaceholder = regexp.MustCompile(`\{\{item\.([^{}]+)\}\}`)

func RenderItem(brief string, item map[string]any) string {
	return itemPlaceholder.ReplaceAllStringFunc(brief, func(m string) string { return eventText(eventField(item, itemPlaceholder.FindStringSubmatch(m)[1])) })
}

func decodeMatrix(data []byte, selector string) ([]map[string]any, error) {
	if len(data) > 256*1024 {
		return nil, fmt.Errorf("for_each file exceeds 256 KiB")
	}
	var body any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, fmt.Errorf("for_each file must be JSON: %w", err)
	}
	if selector != "" {
		body = eventField(body, selector)
	}
	list, ok := body.([]any)
	if !ok || len(list) > 20 {
		return nil, fmt.Errorf("for_each.items must select an array of at most 20 objects")
	}
	out := make([]map[string]any, len(list))
	for i, value := range list {
		var ok bool
		out[i], ok = value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("for_each item %d must be an object", i)
		}
	}
	return out, nil
}

// ReadMatrix reads a bounded blob, never a working-tree file or a revision supplied
// by the template. Git routing preserves the daemon's execution boundary.
func ReadMatrix(ctx context.Context, folder, head string, f ForEach) ([]map[string]any, error) {
	if !regexp.MustCompile(`^[0-9a-fA-F]{40,64}$`).MatchString(head) {
		return nil, fmt.Errorf("for_each requires a recorded commit head")
	}
	step := Step{Type: StepAgent, Worker: "deployed-worker", ForEach: &f}
	if err := step.normalizeForEach(); err != nil {
		return nil, err
	}
	spec := head + ":" + f.File
	size, err := sandbox.TimedGitOutput(ctx, folder, nil, "cat-file", "-s", spec)
	if err != nil {
		return nil, fmt.Errorf("for_each file unavailable at delivery head: %w", err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(size)))
	if err != nil || n > 256*1024 {
		return nil, fmt.Errorf("for_each file exceeds 256 KiB")
	}
	data, err := sandbox.TimedGitOutput(ctx, folder, nil, "cat-file", "blob", spec)
	if err != nil {
		return nil, fmt.Errorf("for_each file is not a readable blob: %w", err)
	}
	return decodeMatrix(data, f.Items)
}

func (r *Runner) queueItems(ctx context.Context, d *Delivery, record *StepRecord, step Step) stepFailure {
	if pebble.IsWorkflowItemSource(d.Result.RequestSource) {
		return stepFailure{reason: "for_each refuses recursive fan-out from any workflow item job", refused: true}
	}
	if record.Items != nil {
		return stepFailure{}
	}
	if r.head != nil {
		if _, err := r.head(ctx, d.Folder, headResult(*d)); err != nil {
			return stepFailure{reason: "import for_each head: " + err.Error()}
		}
	}
	items, err := ReadMatrix(ctx, d.Folder, d.Head, *step.ForEach)
	if err != nil {
		return stepFailure{reason: err.Error(), refused: true}
	}
	dependencies, err := matrixDependencies(items, *step.ForEach)
	if err != nil {
		return stepFailure{reason: err.Error(), refused: true}
	}
	queued := make([]ItemRecord, 0, len(items))
	for i, item := range items {
		label := eventText(item["title"])
		if label == "" {
			label = eventText(item["id"])
		}
		brief := AgentStepBrief(RenderItem(step.Brief, item), d.Result)
		// Match worker admission's serialized input limit, including JSON escaping
		// and the prompt envelope used by workflow dispatch.
		input, err := json.Marshal(map[string]any{"prompt": brief})
		if err != nil || len(input) > 64*1024 {
			return stepFailure{reason: fmt.Sprintf("for_each item %d exceeds the 64 KiB worker input limit", i), refused: true}
		}
		entry := dependencies[i]
		entry.Index, entry.Label, entry.Brief = i, truncate(label, 120), brief
		entry.State = "queued"
		if entry.After != "" {
			entry.State = "waiting for " + entry.After
		}
		queued = append(queued, entry)
	}
	record.Items = queued
	if err := r.store.SaveDelivery(*d); err != nil {
		record.Items = nil
		return stepFailure{reason: "persist for_each items: " + err.Error()}
	}
	return stepFailure{}
}

// advanceQueued is shared with external triggers. Start is durably idempotent;
// replay after a failed receipt write recovers the same canonical worker run.
func (r *TriggerRunner) advanceQueued(ctx context.Context, a Automation, brief, key string) (string, string, string) {
	job, err := r.Start(ctx, a, brief, key)
	if err != nil {
		return "failed", "", "worker start failed: " + err.Error()
	}
	return "started", job, ""
}

func (r *TriggerRunner) tickItems(ctx context.Context) {
	s := r.Store
	_ = s.db.IteratePrefix(keyPendingItemsPrefix+hexKey(r.Account())+"/", 100000, func(_ string, value []byte) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		d, err := s.GetDelivery(r.Account(), string(value))
		if err != nil {
			return err
		}
		changed := false
		for i := range d.Steps {
			rec := &d.Steps[i]
			if i >= len(d.Definition) || d.Definition[i].ForEach == nil || rec.Items == nil {
				continue
			}
			pending := false
			for _, item := range rec.Items {
				if itemPending(item) {
					pending = true
					break
				}
			}
			if !pending {
				continue
			}
			step := d.Definition[i]
			a := Automation{ID: d.AutomationID, Name: d.AutomationName, Account: d.Account, TriggerOwner: d.ItemOwner, Scope: Scope{Folder: d.Folder}, On: &Trigger{Start: TriggerStart{Worker: step.Worker}}, ItemSource: "workflow/" + d.AutomationID + "/delivery/" + d.ID + "/step/" + step.ID}
			active := 0
			for j := range rec.Items {
				item := &rec.Items[j]
				if item.State != "started" {
					continue
				}
				running, err := r.Running(ctx, a, item.JobID)
				if err != nil || running {
					active++
					continue
				}
				previouslyChanged := changed
				changed = true
				item.State = "done"
				if r.ItemOutcome != nil {
					if failure, err := r.ItemOutcome(ctx, a, item.JobID); err != nil {
						active++
						item.State = "started"
						changed = previouslyChanged
					} else if failure != "" {
						item.State, item.Error = "failed", failure
						if err := s.putDeliveryLocked(d); err != nil {
							return err
						}
						changed = false
						if r.Signal != nil {
							r.Signal(a, "workflow.item.failed", strconv.Itoa(item.Index))
						}
					}
				}
			}
			for j := range rec.Items {
				item := &rec.Items[j]
				if item.After != "" && (item.State == "queued" || strings.HasPrefix(item.State, "waiting for ")) {
					ready, failure := r.itemDependency(d.Account, rec.Items, item.After)
					if failure != "" {
						item.State, item.Error = "failed", "waits for "+item.After+", which did not finish: "+failure
						changed = true
						continue
					}
					if !ready || active >= step.ForEach.MaxParallel {
						continue
					}
					item.State = "queued"
					changed = true
				}
				if item.State != "queued" || active >= step.ForEach.MaxParallel {
					continue
				}
				digest := sha256.Sum256([]byte(d.Account + "/" + d.ID + "/" + step.ID + "/" + strconv.Itoa(item.Index)))
				item.State, item.JobID, item.Error = r.advanceQueued(ctx, a, item.Brief, hex.EncodeToString(digest[:]))
				if item.State == "started" {
					active++
				}
				if err := s.putDeliveryLocked(d); err != nil {
					return err
				}
				changed = false // This receipt write also persisted preceding completions.
				if r.Signal != nil {
					r.Signal(a, "workflow.item."+item.State, strconv.Itoa(item.Index))
				}
			}
		}
		if changed {
			return s.putDeliveryLocked(d)
		}
		return nil
	})
}

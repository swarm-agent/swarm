package run

import (
	"context"
	"fmt"
	"strings"

	store "swarm/packages/swarmd/internal/store/pebble"
)

// WorkerPresetMetadataKey marks a worker created from a code-owned preset.
// The value is the preset key; it is how EnsurePresets recognizes a preset
// the account already has, so it never duplicates or overwrites one.
const WorkerPresetMetadataKey = "preset"

// WorkerPreset is a code-owned worker available for explicit deployment. Presets are
// box-level: no workspace binding (every job names its workspace), no
// schedule, and no pinned model (the role's account model setting applies).
type WorkerPreset struct {
	Key          string
	Name         string
	Role         string
	Description  string
	Instructions string
}

// WorkerPresets lists the premade workers in their display order.
var WorkerPresets = []WorkerPreset{
	{Key: store.WorkerRoleReviewer, Name: "Reviewer", Role: store.WorkerRoleReviewer, Description: "Independent report-only code reviewer.", Instructions: "Review the exact job head against its base and judge four things: is every requirement of the brief fully done; do the report's claims match the evidence (a test the report says passed must appear among the commands the job actually ran, with exit code 0; unbacked claims are findings); are there gaps; are there security problems. Return VERDICT: OK|BLOCK, SUMMARY, FINDINGS (each at file:line, sent to the coder verbatim) and TESTS RUN. Never deliver changes."},
	{
		Key:          store.WorkerRoleCoder,
		Name:         "Coder",
		Role:         store.WorkerRoleCoder,
		Description:  "Premade worker that writes and fixes code.",
		Instructions: "Make the code changes each job asks for in the workspace it names: implement, fix and test them, then report what changed.",
	},
	{
		Key:          store.WorkerRoleDesigner,
		Name:         "Designer",
		Role:         store.WorkerRoleDesigner,
		Description:  "Premade worker that designs interfaces and visuals.",
		Instructions: "Make and edit the HTML designs each job asks for with swarm_design, in the workspace it names, then report each design's path and status.",
	},
	{
		Key:          store.WorkerRoleFinder,
		Name:         "Finder",
		Role:         store.WorkerRoleFinder,
		Description:  "Premade worker that researches and answers questions.",
		Instructions: "Research what each job asks by reading the workspace it names and the web, then report clear findings with their sources.",
	},
}

// EnsurePresets creates and activates every preset the account is missing and
// returns the account's preset workers in WorkerPresets order. An existing
// preset (any state but deleted) is returned unchanged, never edited. A
// deleted preset counts as missing and is created again as a new worker.
// Creation is idempotent per preset generation, so concurrent or retried
// calls converge on one worker per preset.
func (s *WorkerExecutionService) EnsurePresets(ctx context.Context, account, user string) ([]store.WorkerRecord, error) {
	return s.ensurePresets(ctx, account, user, WorkerPresets)
}

// EnsurePreset provisions one explicitly selected preset, leaving other roles absent.
func (s *WorkerExecutionService) EnsurePreset(ctx context.Context, account, user, key string) ([]store.WorkerRecord, error) {
	for _, preset := range WorkerPresets {
		if preset.Key == key {
			return s.ensurePresets(ctx, account, user, []WorkerPreset{preset})
		}
	}
	return nil, fmt.Errorf("unknown worker preset %q", key)
}

func (s *WorkerExecutionService) ensurePresets(ctx context.Context, account, user string, presets []WorkerPreset) ([]store.WorkerRecord, error) {
	if err := s.authorizeWorkerOwner(account, user); err != nil {
		return nil, err
	}
	ws, err := s.workerStore()
	if err != nil {
		return nil, err
	}
	s.presetMu.Lock()
	defer s.presetMu.Unlock()
	existing := make(map[string]store.WorkerRecord, len(WorkerPresets))
	deleted := make(map[string]int, len(WorkerPresets))
	cursor := ""
	for page := 0; ; page++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if page >= 100 {
			return nil, fmt.Errorf("worker preset scan exceeded %d pages", page)
		}
		res, err := ws.ListWorkers(account, store.ListWorkersQuery{Limit: 100, After: cursor, IncludeDeleted: true})
		if err != nil {
			return nil, err
		}
		for _, w := range res.Workers {
			key := strings.TrimSpace(mapString(w.Metadata, WorkerPresetMetadataKey))
			if key == "" {
				continue
			}
			if w.LifecycleState == store.WorkerLifecycleStateDeleted {
				deleted[key]++
				continue
			}
			if _, seen := existing[key]; !seen {
				existing[key] = w
			}
		}
		if res.NextCursor == "" {
			break
		}
		cursor = res.NextCursor
	}
	out := make([]store.WorkerRecord, 0, len(presets))
	for _, preset := range presets {
		if w, ok := existing[preset.Key]; ok {
			out = append(out, w)
			continue
		}
		created, err := s.host.runs.sessions.CreateWorker(ctx, account, user, store.CreateWorkerRequest{
			Role:           preset.Role,
			Name:           preset.Name,
			Description:    preset.Description,
			Instructions:   preset.Instructions,
			Metadata:       map[string]any{WorkerPresetMetadataKey: preset.Key},
			IdempotencyKey: fmt.Sprintf("worker-preset:%s:%d", preset.Key, deleted[preset.Key]),
		})
		if err != nil {
			return nil, fmt.Errorf("create %s preset: %w", preset.Name, err)
		}
		if created.LifecycleState == store.WorkerLifecycleStateIdle {
			if current, found, err := ws.GetWorker(account, created.ID); err != nil {
				return nil, err
			} else if found && current.LifecycleState == store.WorkerLifecycleStateIdle {
				created, err = s.ConfigureBindings(account, user, current.ID, current.Revision, nil, true)
				if err != nil {
					return nil, fmt.Errorf("activate %s preset: %w", preset.Name, err)
				}
			} else if found {
				created = current
			}
		}
		out = append(out, created)
	}
	return out, nil
}

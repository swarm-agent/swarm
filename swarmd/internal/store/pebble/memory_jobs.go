package pebblestore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

const MemoryMaxJobs = 64

// MemoryJob contains no transcript copy. Coverage is exact message sequence
// authority; generated content exists only while awaiting review.
type MemoryJob struct {
	ID                  string               `json:"id"`
	UserID              string               `json:"user_id"`
	Status              string               `json:"status"`
	Trigger             string               `json:"trigger"`
	Revision            int64                `json:"revision"`
	Settings            MemorySettings       `json:"settings"`
	Model               AgentModelAssignment `json:"model"`
	Sources             []MemorySource       `json:"sources"`
	Cursors             map[string]uint64    `json:"cursors"`
	Proposal            *MemoryEntry         `json:"proposal,omitempty"`
	Conflict            bool                 `json:"conflict"`
	InputTokens         int                  `json:"input_tokens"`
	ProviderInputTokens int                  `json:"provider_input_tokens"`
	ScanLimited         bool                 `json:"scan_limited"`
	OutputTokens        int                  `json:"output_tokens"`
	ReservedSpend       int64                `json:"reserved_spend_microunits"`
	Spend               int64                `json:"spend_microunits"`
	Error               string               `json:"error,omitempty"`
	CreatedAt           int64                `json:"created_at"`
	UpdatedAt           int64                `json:"updated_at"`
	ResultRevision      int64                `json:"result_revision,omitempty"`
}

type MemoryJobInput struct {
	Source  MemorySource `json:"source"`
	Role    string       `json:"role"`
	Content string       `json:"content"`
}

func memoryJobIndex(d MemoryDocument, id string) int {
	for i := range d.Jobs {
		if d.Jobs[i].ID == id {
			return i
		}
	}
	return -1
}

// QueueMemoryJob snapshots policy and the canonical Swarm action model. The
// caller supplies authenticated account/user identity, never a model assignment.
// Scheduled requests use a deterministic interval key for durable idempotency.
func (s *MemoryStore) QueueMemoryJob(account, user, id string, scheduled bool) (MemoryJob, error) {
	if _, err := memoryAccount(account); err != nil {
		return MemoryJob{}, err
	}
	if user == "" || len(user) > 256 || id == "" || len(id) > 128 {
		return MemoryJob{}, ErrMemoryPolicy
	}
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return MemoryJob{}, err
	}
	if i := memoryJobIndex(d, id); i >= 0 {
		if d.Jobs[i].UserID != user {
			return MemoryJob{}, ErrMemoryPolicy
		}
		return d.Jobs[i], nil
	}
	now := s.now().UnixMilli()
	if d.Settings.AutomationUserID != user || !d.Settings.AutomationEnabled || !d.Settings.RememberEnabled || !d.Settings.ReadEnabled || (scheduled && (d.Settings.Mode != "recurring" || now < d.NextJobAt)) {
		return MemoryJob{}, ErrMemoryPolicy
	}
	for _, j := range d.Jobs {
		if j.Status == "queued" || j.Status == "running" || j.Status == "review" {
			return MemoryJob{}, ErrMemoryConflict
		}
	}
	if len(d.Jobs) >= MemoryMaxJobs {
		return MemoryJob{}, ErrMemoryBudget
	}
	models, ok, err := NewAgentModelSettingsStore(s.store).GetForAccount(account)
	if err != nil {
		return MemoryJob{}, err
	}
	if !ok || ValidateAgentModelAssignment(models.Swarm.Action) != nil {
		return MemoryJob{}, errors.New("default Swarm model unavailable")
	}
	trigger := "manual"
	if scheduled {
		trigger = "scheduled"
	}
	j := MemoryJob{ID: id, UserID: user, Status: "queued", Trigger: trigger, Revision: d.Revision, Settings: d.Settings, Model: models.Swarm.Action, CreatedAt: now, UpdatedAt: now, Cursors: map[string]uint64{}}
	d.Jobs = append(d.Jobs, j)
	d.NextJobAt = now + int64(d.Settings.IntervalMinutes)*60000
	if err = s.persist(d); err != nil {
		return MemoryJob{}, err
	}
	return j, nil
}

// Source eligibility is checked against every workspace attached to a session;
// one excluded workspace excludes the entire mixed-workspace transcript.
func (s *MemoryStore) memorySessionSource(d MemoryDocument, sess SessionSnapshot, user string) (MemorySource, error) {
	if sess.AccountScopeID != d.AccountScopeID || sess.UserID != user {
		return MemorySource{}, ErrMemoryPolicy
	}
	src := MemorySource{SessionID: sess.ID}
	included := containsMemory(d.Settings.IncludedSessions, sess.ID)
	for _, g := range sess.WorkspaceGrants {
		if g.WorkspaceID == "" {
			// The canonical isolated execution lane is not a catalog workspace.
			// It supplies no source scope; catalog grants below still authorize
			// every source and enforce exclusions and generation revocation.
			if g.Kind == WorkspaceGrantWorktree && sess.WorktreeEnabled && g.Path != "" && g.Path == sess.WorktreeRootPath && (g.Available == nil || *g.Available) {
				continue
			}
			return MemorySource{}, ErrMemoryPolicy
		}
		var workspace WorkspaceEntry
		ok, err := s.store.GetJSON(KeyWorkspaceEntryByIDForAccount(d.AccountScopeID, g.WorkspaceID), &workspace)
		if err != nil {
			return MemorySource{}, err
		}
		if !ok || workspace.AccountScopeID != d.AccountScopeID || workspace.State != "active" || workspace.WorkspaceGeneration != g.WorkspaceGeneration {
			return MemorySource{}, ErrMemoryPolicy
		}
		if memorySourceDenied(d, MemorySource{SessionID: sess.ID, WorkspaceID: g.WorkspaceID}) {
			return MemorySource{}, ErrMemoryPolicy
		}
		if g.Available != nil && !*g.Available {
			return MemorySource{}, ErrMemoryPolicy
		}
		if src.WorkspaceID == "" {
			src.WorkspaceID = g.WorkspaceID
		}
		if containsMemory(d.Settings.IncludedWorkspaces, g.WorkspaceID) {
			included = true
			src.WorkspaceID = g.WorkspaceID
		}
	}
	if !included || src.WorkspaceID == "" || memorySourceDenied(d, src) {
		return MemorySource{}, ErrMemoryPolicy
	}
	return src, nil
}

// ClaimMemoryJob reads only visible user/assistant text, never raw event/tool
// payloads. Reads are serialized against canonical session deletion/mutation.
// At most 256 indexed sessions and 128 messages/session are visited per run.
func (s *MemoryStore) ClaimMemoryJob(ctx context.Context, account, user, id string) (MemoryJob, []MemoryJobInput, error) {
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return MemoryJob{}, nil, err
	}
	i := memoryJobIndex(d, id)
	if i < 0 {
		return MemoryJob{}, nil, ErrMemoryPolicy
	}
	j := &d.Jobs[i]
	if j.UserID != user || j.Status != "queued" || j.Revision != d.Revision || !d.Settings.AutomationEnabled {
		return MemoryJob{}, nil, ErrMemoryConflict
	}
	sessions := NewSessionStore(s.store)
	candidates, err := sessions.ListSessionsForAccountUser(account, user, 256)
	if err != nil {
		return MemoryJob{}, nil, err
	}
	j.ScanLimited = len(candidates) == 256
	inputs := []MemoryJobInput{}
	budget := d.Settings.InputTokens
	cutoff := s.now().UnixMilli() - int64(d.Settings.LookbackDays)*86400000
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return MemoryJob{}, nil, err
		}
		if _, err := s.memorySessionSource(d, candidate, user); err != nil {
			if !errors.Is(err, ErrMemoryPolicy) {
				return MemoryJob{}, nil, err
			}
			continue
		}
		unlock := s.store.sessionMutations.lockSessions(candidate.ID)
		sess, ok, e := sessions.GetSession(candidate.ID)
		if e != nil {
			unlock()
			return MemoryJob{}, nil, e
		}
		if !ok {
			unlock()
			continue
		}
		src, e := s.memorySessionSource(d, sess, user)
		if e != nil {
			unlock()
			if !errors.Is(e, ErrMemoryPolicy) {
				return MemoryJob{}, nil, e
			}
			continue
		}
		messages, e := sessions.ListV3SessionMessages(sess.ID, d.JobCursors[sess.ID], 128)
		unlock()
		if e != nil {
			return MemoryJob{}, nil, e
		}
		if len(messages) == 128 {
			j.ScanLimited = true
		}
		for _, m := range messages {
			if m.SessionID != sess.ID || m.AccountScopeID != account || m.UserID != user {
				return MemoryJob{}, nil, ErrMemoryPolicy
			}
			if m.CreatedAt < cutoff || (m.Role != "user" && m.Role != "assistant") {
				j.Cursors[sess.ID] = m.GlobalSeq
				continue
			}
			cost := MemoryTokenCount(m.Content) + 256
			if cost > budget {
				// Never advance a cursor past an unread oversized message.
				return MemoryJob{}, nil, ErrMemoryBudget
			}
			budget -= cost
			j.InputTokens += cost
			source := src
			source.EventSeq = int64(m.GlobalSeq)
			inputs = append(inputs, MemoryJobInput{Source: source, Role: m.Role, Content: m.Content})
			j.Sources = append(j.Sources, source)
			j.Cursors[sess.ID] = m.GlobalSeq
			if len(j.Sources) >= 32 {
				j.ScanLimited = true
				break
			}
		}
		if len(j.Sources) >= 32 {
			break
		}
	}
	j.Status = "running"
	j.UpdatedAt = s.now().UnixMilli()
	if err = s.persist(d); err != nil {
		return MemoryJob{}, nil, err
	}
	return *j, inputs, nil
}

// UpdateMemoryJob records bounded accounting/errors or cancellation. Raw provider
// errors are never persisted because they may contain source text or credentials.
func (s *MemoryStore) UpdateMemoryJob(account, user, id, status string, output int, spend int64) (MemoryJob, error) {
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return MemoryJob{}, err
	}
	i := memoryJobIndex(d, id)
	if i < 0 || d.Jobs[i].UserID != user {
		return MemoryJob{}, ErrMemoryPolicy
	}
	j := &d.Jobs[i]
	if j.Status != "queued" && j.Status != "running" && j.Status != "review" {
		return *j, ErrMemoryConflict
	}
	switch status {
	case "cancelled", "failed", "interrupted":
	default:
		return MemoryJob{}, ErrMemoryPolicy
	}
	if output < 0 || spend < 0 {
		return MemoryJob{}, ErrMemoryBudget
	}
	j.Status = status
	j.Error = status
	j.OutputTokens = output
	// Interrupted/failed calls may already have been billed: retain the full
	// reservation as conservative accounting rather than reporting zero cost.
	if spend < j.ReservedSpend {
		spend = j.ReservedSpend
	}
	j.Spend = spend
	j.Proposal = nil
	j.UpdatedAt = s.now().UnixMilli()
	if err = s.persist(d); err != nil {
		return MemoryJob{}, err
	}
	return *j, nil
}

func (s *MemoryStore) ReserveMemorySpend(account, user, id string, amount int64) error {
	return s.ReserveMemoryRequest(account, user, id, amount, 0)
}
func (s *MemoryStore) ReserveMemoryRequest(account, user, id string, amount int64, inputTokens int) error {
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return err
	}
	i := memoryJobIndex(d, id)
	if i < 0 {
		return ErrMemoryPolicy
	}
	j := &d.Jobs[i]
	if j.UserID != user || j.Status != "running" || j.Revision != d.Revision || amount <= 0 || amount > j.Settings.SpendMicrounits || j.ReservedSpend != 0 {
		return ErrMemoryBudget
	}
	if inputTokens < 0 || inputTokens > j.Settings.InputTokens {
		return ErrMemoryBudget
	}
	j.ProviderInputTokens = inputTokens
	j.ReservedSpend = amount
	return s.persist(d)
}

// FinishMemoryJob validates live source ownership under the same session locks
// as deletion, then publishes job status, cursors and one learned change in one
// synchronous memory document commit. No partial multi-entry apply is possible.
func (s *MemoryStore) FinishMemoryJob(ctx context.Context, account, user, id string, proposal *MemoryEntry, output int, spend int64, approved bool) (MemoryJob, error) {
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return MemoryJob{}, err
	}
	i := memoryJobIndex(d, id)
	if i < 0 {
		return MemoryJob{}, ErrMemoryPolicy
	}
	j := &d.Jobs[i]
	if j.UserID != user || (j.Status != "running" && !(approved && j.Status == "review")) || j.Revision != d.Revision || !d.Settings.AutomationEnabled {
		return MemoryJob{}, ErrMemoryConflict
	}
	if output < 0 || output > j.Settings.OutputTokens || spend < 0 || spend > j.ReservedSpend {
		return MemoryJob{}, ErrMemoryBudget
	}
	ids := []string{}
	for _, src := range j.Sources {
		ids = append(ids, src.SessionID)
	}
	unlock := s.store.sessionMutations.lockSessions(ids...)
	defer unlock()
	sessions := NewSessionStore(s.store)
	for _, src := range j.Sources {
		sess, ok, e := sessions.GetSession(src.SessionID)
		if e != nil {
			return MemoryJob{}, e
		}
		if !ok {
			return MemoryJob{}, ErrMemoryPolicy
		}
		live, e := s.memorySessionSource(d, sess, user)
		if e != nil || live.WorkspaceID != src.WorkspaceID {
			return MemoryJob{}, ErrMemoryPolicy
		}
	}
	if err := ctx.Err(); err != nil {
		return MemoryJob{}, err
	}
	if approved {
		proposal = j.Proposal
		output = j.OutputTokens
		spend = j.Spend
	}
	if proposal != nil {
		e := *proposal
		if e.Kind != "learned" || e.Pinned || e.WorkspaceID == "" || e.SessionID != "" || len(e.Sources) == 0 || len(e.Sources) > 32 || len(e.Content) > j.Settings.OutputTokens {
			return MemoryJob{}, ErrMemoryPolicy
		}
		for _, src := range e.Sources {
			found := false
			for _, read := range j.Sources {
				if src == read && src.WorkspaceID == e.WorkspaceID {
					found = true
				}
			}
			if !found {
				return MemoryJob{}, ErrMemoryPolicy
			}
		}
		// Only learned identities may be reconciled. Rule/orientation IDs are not writable.
		// Stable source identity reconciles repeated extraction without allowing
		// the model to select an unrelated record as its write target.
		targetID := "learned-" + workspaceMapDigest(e.WorkspaceID + "\x00" + e.Sources[0].SessionID)[:24]
		if e.ID != "" && e.ID != targetID {
			return MemoryJob{}, ErrMemoryPolicy
		}
		e.ID = targetID
		e.Content = strings.TrimSpace(e.Content)
		if n := memoryEntryIndex(d, e.ID); n >= 0 {
			old := d.Entries[n]
			if old.Kind != "learned" || old.Pinned {
				return MemoryJob{}, ErrMemoryPolicy
			}
			if old.Content == e.Content {
				proposal = nil
			} else {
				j.Conflict = true
			}
		}
		if proposal != nil {
			proposal = &e
		}
	}
	j.OutputTokens = output
	j.Spend = spend
	j.UpdatedAt = s.now().UnixMilli()
	if proposal != nil && !approved && (j.Settings.ReviewBeforeApply || j.Conflict) {
		j.Status = "review"
		j.Proposal = proposal
		if err = s.persist(d); err != nil {
			return MemoryJob{}, err
		}
		return *j, nil
	}
	j.Status = "completed"
	j.Proposal = nil
	if d.JobCursors == nil {
		d.JobCursors = map[string]uint64{}
	}
	for session, seq := range j.Cursors {
		if seq > d.JobCursors[session] {
			d.JobCursors[session] = seq
		}
	}
	if len(d.JobCursors) > 256 {
		return MemoryJob{}, ErrMemoryBudget
	}
	if proposal != nil {
		j.ResultRevision = d.Revision + 1
		d, err = s.mutateLoaded(d, MemoryMutation{ExpectedRevision: d.Revision, Actor: MemoryActor{Kind: "learned", ID: user, JobID: id, Model: j.Model}, Reason: "Source-bound memory reconciliation", Operation: "put", Entry: *proposal, Approved: approved})
	} else {
		err = s.persist(d)
	}
	if err != nil {
		return MemoryJob{}, err
	}
	return d.Jobs[i], nil
}

// RecoverMemoryJobs never replays a possibly billed provider call after restart.
// Queued work is safe to claim; interrupted/review work remains inspectable.
func (s *MemoryStore) RecoverMemoryJobs(account, user string) error {
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return err
	}
	for i := range d.Jobs {
		j := &d.Jobs[i]
		if j.UserID == user && j.Status == "running" {
			j.Status = "interrupted"
			j.Spend = j.ReservedSpend
			j.Error = "restart interrupted provider call; run again explicitly"
			j.UpdatedAt = s.now().UnixMilli()
		}
	}
	return s.persist(d)
}

func MemoryScheduledJobID(now int64, interval int) string {
	return fmt.Sprintf("scheduled-%d", now/(int64(interval)*60000))
}

// MemoryAutomationOwners is a bounded metadata-only scan. It never visits
// session content; the scheduler still rechecks each opted-in document.
type MemoryAutomationOwner struct {
	Account string
	User    string
}

func (s *MemoryStore) MemoryAutomationOwners() (out []MemoryAutomationOwner, err error) {
	err = s.store.IteratePrefix("account_memory:", 256, func(key string, value []byte) error {
		var d MemoryDocument
		if err := json.Unmarshal(value, &d); err != nil {
			return err
		}
		if key != memoryKey(d.AccountScopeID) {
			return ErrMemoryPolicy
		}
		if d.Settings.AutomationEnabled && d.Settings.AutomationUserID != "" {
			out = append(out, MemoryAutomationOwner{Account: d.AccountScopeID, User: d.Settings.AutomationUserID})
		}
		return nil
	})
	return
}

// CheckMemoryJobSources revalidates exact live membership immediately before
// provider access. Publication repeats this under session mutation locks.
func (s *MemoryStore) CheckMemoryJobSources(account, user, id string) error {
	workspaceMapMutationMu.Lock()
	defer workspaceMapMutationMu.Unlock()
	d, _, err := s.load(account)
	if err != nil {
		return err
	}
	i := memoryJobIndex(d, id)
	if i < 0 {
		return ErrMemoryPolicy
	}
	j := d.Jobs[i]
	if j.UserID != user || j.Status != "running" || j.Revision != d.Revision {
		return ErrMemoryConflict
	}
	sessions := NewSessionStore(s.store)
	for _, src := range j.Sources {
		sess, ok, err := sessions.GetSession(src.SessionID)
		if err != nil {
			return err
		}
		if !ok {
			return ErrMemoryPolicy
		}
		live, err := s.memorySessionSource(d, sess, user)
		if err != nil || live.WorkspaceID != src.WorkspaceID {
			return ErrMemoryPolicy
		}
	}
	return nil
}

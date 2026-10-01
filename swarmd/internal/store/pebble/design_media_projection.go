package pebblestore

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/cockroachdb/pebble"
)

// ProjectDesignEntry is a reference-only projection. Request remains the sole
// progress authority; TaskID/AttemptID come from current project records.
type ProjectDesignEntry struct {
	ProjectID string `json:"project_id"`
	TaskID string `json:"task_id,omitempty"`
	AttemptID string `json:"attempt_id,omitempty"`
	Title string `json:"title"`
	Request DesignRequest `json:"request"`
}

type projectDesignCursor struct {
	Scope string `json:"scope"`
	Task string `json:"task"`
	Slot int `json:"slot"`
	After string `json:"after"`
}

func designCatalogTitle(r DesignRequest) string {
	if len(r.Candidates) == 0 { return "Design" }
	text := strings.Join(strings.Fields(r.Candidates[0].Spec.Brief), " ")
	runes := []rune(text)
	if len(runes) > 80 { text = string(runes[:80]) + "…" }
	if text == "" { return "Design" }
	return text
}

// ListProjectDesignRequests hydrates the existing atomic session admission
// index through canonical membership, never a global session scan. Each page
// visits at most limit membership/catalog positions, including rejected entries.
// The cursor is a resumable position, not an authorization grant or snapshot.
func (s *SessionStore) ListProjectDesignRequests(p DesignPrincipal, projectID, after string, limit int) ([]ProjectDesignEntry, string, error) {
	if designOwner(p) != nil || !designID(projectID) || limit < 1 || limit > MaxDesignHistoryPage { return nil, "", ErrDesignInvalid }
	s.store.projectsMu.Lock()
	defer s.store.projectsMu.Unlock()
	project, found, err := s.GetProject(p.AccountID, projectID)
	if err != nil { return nil, "", err }
	if !found || project.AccountID != p.AccountID { return nil, "", ErrDesignNotFound }
	c := projectDesignCursor{Scope: designKey(p, "project-catalog", projectID), Slot: -1}
	if after != "" {
		if len(after) > 8192 { return nil, "", ErrDesignInvalid }
		data, err := base64.RawURLEncoding.DecodeString(after)
		var decoded projectDesignCursor
		if err != nil || json.Unmarshal(data, &decoded) != nil || decoded.Scope != c.Scope || decoded.Slot < -1 || (decoded.Task != "" && !designID(decoded.Task)) { return nil, "", ErrDesignInvalid }
		c = decoded
	}
	rows := make([]ProjectDesignEntry, 0, limit)
	prefix := ProjectTaskPrefix(p.AccountID, projectID)
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil { return nil, "", err }
	defer iter.Close()
	for visited := 0; visited < limit; visited++ {
		sessionID, taskID, attemptID := project.PrimarySessionID, "", ""
		if c.Slot >= 0 {
			var task ProjectTaskRecord
			ok := iter.First()
			if c.Task != "" { ok = iter.SeekGE([]byte(KeyProjectTask(p.AccountID, projectID, c.Task))) }
			if !ok {
				return rows, "", iter.Error()
			}
			if err := json.Unmarshal(iter.Value(), &task); err != nil { return nil, "", err }
			if task.ID != c.Task { c.Task, c.Slot, c.After = task.ID, 0, "" }
			if task.AccountID != p.AccountID || task.ProjectID != projectID { return nil, "", ErrDesignConflict }
			if c.Slot > len(task.Attempts) {
				if !iter.Next() { return rows, "", iter.Error() }
				if err := json.Unmarshal(iter.Value(), &task); err != nil { return nil, "", err }
				c.Task, c.Slot, c.After = task.ID, 0, ""
				continue
			}
			taskID, sessionID, attemptID = task.ID, task.SessionID, task.ActiveAttemptID
			if c.Slot > 0 {
				a := task.Attempts[c.Slot-1]
				sessionID, attemptID = a.SessionID, a.ID
				// A current attempt shares the task's session; emit it once.
				if sessionID == task.SessionID { sessionID = "" }
			}
		}
		if sessionID == "" { c.Slot++; c.After = ""; continue }
		parent, owned, err := s.GetSession(sessionID)
		if err != nil { return nil, "", err }
		if sessionID != "" && owned && parent.AccountScopeID == p.AccountID && parent.UserID == p.PrincipalID {
			// Idempotent locator hydration for pre-index projects. No progress or
			// artifact authority is copied, and readers revalidate membership.
			batch := s.store.db.NewBatch()
			err := setDesignMembership(batch, p.AccountID, sessionID, projectID, taskID)
			if err == nil { err = batch.Commit(pebble.Sync) }
			_ = batch.Close()
			if err != nil { return nil, "", err }
			requests, next, err := s.store.ListSessionDesignRequests(p, sessionID, c.After, 1)
			if err != nil { return nil, "", err }
			for _, r := range requests {
				// Reload retained input solely to derive a bounded display title.
				full, err := s.store.GetDesignRequest(p, r.ID)
				if err != nil { return nil, "", err }
				title := designCatalogTitle(full)
				for i := range full.Candidates { full.Candidates[i].Spec.Brief = "" }
				rows = append(rows, ProjectDesignEntry{ProjectID: projectID, TaskID: taskID, AttemptID: attemptID, Title: title, Request: full})
			}
			if next != "" { c.After = next; continue }
		}
		c.Slot++
		c.After = ""
	}
	data, err := json.Marshal(c)
	if err != nil { return nil, "", err }
	return rows, base64.RawURLEncoding.EncodeToString(data), nil
}

// designProjectLocator treats metadata only as a locator and checks the current
// canonical project/task record before creating a project invalidation.
func (s *SessionStore) designProjectLocator(p DesignPrincipal, sessionID string) (string, error) {
	parent, found, err := s.GetSession(sessionID)
	if err != nil || !found { return "", err }
	if parent.AccountScopeID != p.AccountID || parent.UserID != p.PrincipalID { return "", nil }
	prefix := "design-membership:v1/" + keyPart(p.AccountID) + "/" + keyPart(sessionID) + "/"
	iter, err := s.store.db.NewIter(&pebble.IterOptions{LowerBound: []byte(prefix), UpperBound: []byte(prefix + "\xff")})
	if err != nil { return "", err }
	defer iter.Close()
	visited := 0
	for ok := iter.First(); ok; ok = iter.Next() {
		visited++
		if visited > 50 { return "", ErrDesignConflict }
		var binding designMembership
		if err := json.Unmarshal(iter.Value(), &binding); err != nil { return "", err }
		project, found, err := s.GetProject(p.AccountID, binding.Project)
		if err != nil { return "", err }
		if !found || project.AccountID != p.AccountID { continue }
		if binding.Task == "" {
			if project.PrimarySessionID == sessionID { return project.ID, nil }
			continue
		}
		task, found, err := s.GetProjectTask(p.AccountID, project.ID, binding.Task)
		if err != nil { return "", err }
		if !found || task.AccountID != p.AccountID || task.ProjectID != project.ID { continue }
		if task.SessionID == sessionID { return project.ID, nil }
		for _, attempt := range task.Attempts { if attempt.SessionID == sessionID { return project.ID, nil } }
	}
	if err := iter.Error(); err != nil { return "", err }
	projectID, _ := parent.Metadata["project_id"].(string)
	if projectID == "" { return "", nil }
	project, found, err := s.GetProject(p.AccountID, projectID)
	if err != nil || !found { return "", err }
	if project.AccountID != p.AccountID { return "", nil }
	if project.PrimarySessionID == sessionID { return projectID, nil }
	taskID, _ := parent.Metadata["task_id"].(string)
	if taskID == "" { taskID, _ = parent.Metadata["project_task_id"].(string) }
	if taskID == "" { return "", nil }
	task, found, err := s.GetProjectTask(p.AccountID, projectID, taskID)
	if err != nil || !found { return "", err }
	if task.AccountID != p.AccountID || task.ProjectID != projectID { return "", nil }
	if task.SessionID == sessionID { return projectID, nil }
	for _, attempt := range task.Attempts { if attempt.SessionID == sessionID { return projectID, nil } }
	return "", nil
}

// The companion is only a project cache invalidation, not design history. It
// shares the canonical acceptance/update batch and endpoint reservation. Its
// payload deliberately carries no session, request, brief or artifact metadata.
func (s *SessionStore) setDesignProjectInvalidation(b *pebble.Batch, p DesignPrincipal, projectID string, endpoint uint64, now int64) (*V3RealtimeOutboxRecord, error) {
	payload, err := json.Marshal(map[string]any{"project_id": projectID, "resource": "designs"})
	if err != nil { return nil, err }
	id := "__design_project__:" + p.AccountID + ":" + projectID
	out := V3RealtimeOutboxRecord{EndpointSeq: endpoint, EndpointCursor: V3RealtimeOutboxCursor(endpoint), SessionID: id, AccountScopeID: p.AccountID, UserID: p.PrincipalID, CreatedAt: now, Event: V3SessionEvent{SessionID: id, Seq: endpoint, EventType: ProjectUpdatedEventType, Payload: payload, TsUnixMs: now}}
	if err := designSet(b, KeyV3RealtimeOutbox(endpoint), out); err != nil { return nil, err }
	ref, err := marshalV3RealtimeOutboxReference(out)
	if err != nil { return nil, err }
	if err := b.Set([]byte(KeyV3RealtimeOutboxByAuthScope(p.AccountID, p.PrincipalID, endpoint)), ref, nil); err != nil { return nil, err }
	return &out, nil
}

func designMembershipKey(account, session, project, task string) string {
	return "design-membership:v1/" + keyPart(account) + "/" + keyPart(session) + "/" + keyPart(project) + "/" + keyPart(task)
}

type designMembership struct {
	Project string `json:"project"`
	Task string `json:"task"`
}

func setDesignMembership(b *pebble.Batch, account, session, project, task string) error {
	if session == "" { return nil }
	return designSet(b, designMembershipKey(account, session, project, task), designMembership{Project: project, Task: task})
}

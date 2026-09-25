package pebblestore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/cockroachdb/pebble"
)

// ProjectWorkspaceRef represents a workspace bound to a project.
type ProjectWorkspaceRef struct {
	WorkspaceID string `json:"workspace_id,omitempty"`
	Path        string `json:"path"`
	Role        string `json:"role,omitempty"` // "primary_code", "auxiliary", "docs"
	Label       string `json:"label,omitempty"`
}

// ProjectRecord represents a top-level Project aggregating workspaces, context, and tasks.
type ProjectRecord struct {
	ID               string                `json:"id"`
	AccountID        string                `json:"account_id"`
	Name             string                `json:"name"`
	Description      string                `json:"description,omitempty"`
	Workspaces       []ProjectWorkspaceRef `json:"workspaces,omitempty"`
	ProjectContext   string                `json:"project_context,omitempty"` // synthesized project.md
	ActiveTaskIDs    []string              `json:"active_task_ids,omitempty"`
	AutomationIDs    []string              `json:"automation_ids,omitempty"`
	PrimarySessionID string                `json:"primary_session_id,omitempty"`
	CreatedAt        int64                 `json:"created_at"`
	UpdatedAt        int64                 `json:"updated_at"`
}

// Validate checks that the project has valid required fields.
func (p *ProjectRecord) Validate() error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return errors.New("project name is required")
	}
	return nil
}

// PutProject stores or updates a project record for the account scope.
func (s *SessionStore) PutProject(accountScopeID string, proj *ProjectRecord) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	if proj == nil {
		return errors.New("project definition required")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return errors.New("account scope id is required")
	}
	proj.AccountID = accountScopeID
	if err := proj.Validate(); err != nil {
		return err
	}

	now := time.Now().UnixMilli()
	if proj.ID == "" {
		b := make([]byte, 8)
		_, _ = rand.Read(b)
		proj.ID = "proj_" + hex.EncodeToString(b)
	}
	if proj.CreatedAt == 0 {
		proj.CreatedAt = now
	}
	proj.UpdatedAt = now

	raw, err := json.Marshal(proj)
	if err != nil {
		return err
	}

	return s.store.db.Set([]byte(KeyProject(accountScopeID, proj.ID)), raw, pebble.Sync)
}

// GetProject returns a project record by ID within an account scope.
func (s *SessionStore) GetProject(accountScopeID, id string) (*ProjectRecord, bool, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, false, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return nil, false, errors.New("account scope id and project id are required")
	}

	raw, closer, err := s.store.db.Get([]byte(KeyProject(accountScopeID, id)))
	if err != nil {
		if errors.Is(err, pebble.ErrNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	defer closer.Close()

	var record ProjectRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, false, err
	}
	return &record, true, nil
}

// ListProjects lists projects for an account scope ordered by updated_at descending.
func (s *SessionStore) ListProjects(accountScopeID string, limit int) ([]ProjectRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	if accountScopeID == "" {
		return nil, errors.New("account scope id is required")
	}
	if limit <= 0 || limit > 1000 {
		limit = 100
	}

	var projects []ProjectRecord
	err := s.store.IteratePrefix(ProjectPrefix(accountScopeID), 10000, func(_ string, value []byte) error {
		var record ProjectRecord
		if err := json.Unmarshal(value, &record); err != nil {
			return nil
		}
		if record.AccountID == accountScopeID {
			projects = append(projects, record)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Sort updated_at descending
	sort.Slice(projects, func(i, j int) bool {
		return projects[i].UpdatedAt > projects[j].UpdatedAt
	})

	if len(projects) > limit {
		projects = projects[:limit]
	}
	return projects, nil
}

// DeleteProject deletes a project record by ID.
func (s *SessionStore) DeleteProject(accountScopeID, id string) error {
	if s == nil || s.store == nil || s.store.db == nil {
		return errors.New("database not available")
	}
	accountScopeID = strings.TrimSpace(accountScopeID)
	id = strings.TrimSpace(id)
	if accountScopeID == "" || id == "" {
		return errors.New("account scope id and project id are required")
	}

	return s.store.db.Delete([]byte(KeyProject(accountScopeID, id)), pebble.Sync)
}

// UpdateProject reads, mutates, and writes back a project atomically.
func (s *SessionStore) UpdateProject(accountScopeID, id string, mutate func(*ProjectRecord) error) (*ProjectRecord, error) {
	if s == nil || s.store == nil || s.store.db == nil {
		return nil, errors.New("database not available")
	}
	record, found, err := s.GetProject(accountScopeID, id)
	if err != nil {
		return nil, err
	}
	if !found || record == nil {
		return nil, errors.New("project not found")
	}

	if err := mutate(record); err != nil {
		return nil, err
	}

	if err := s.PutProject(accountScopeID, record); err != nil {
		return nil, err
	}
	return record, nil
}

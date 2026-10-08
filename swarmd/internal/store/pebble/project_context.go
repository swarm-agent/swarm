package pebblestore

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// ProjectContextGeneration is a durable operation, not a client loading flag.
// Expired claims can be retried; attempt fencing rejects late provider results.
type ProjectContextGeneration struct {
	Status      string `json:"status"`
	Attempt     int    `json:"attempt"`
	LeaseUntil  int64  `json:"lease_until,omitempty"`
	Error       string `json:"error,omitempty"`
	RouterAlert string `json:"router_alert,omitempty"`
}

var ErrProjectCreationConflict = errors.New("project creation request already used with different input")
var ErrProjectContextStale = errors.New("project context attempt is stale or still running")

func (s *SessionStore) CreateProjectWithContext(account, requestID, hash string, input ProjectRecord) (*ProjectRecord, bool, error) {
	if s == nil || s.store == nil || requestID == "" || hash == "" {
		return nil, false, errors.New("project creation identity required")
	}
	sum := sha256.Sum256([]byte(account + "\x00" + requestID))
	id := "proj_" + hex.EncodeToString(sum[:16])
	s.store.projectsMu.Lock()
	existing, found, err := s.GetProject(account, id)
	if err != nil || found {
		s.store.projectsMu.Unlock()
		if err != nil {
			return nil, false, err
		}
		if existing.CreationHash != hash {
			return nil, false, ErrProjectCreationConflict
		}
		return existing, false, nil
	}
	input.ID, input.AccountID = id, account
	input.CreationRequestID, input.CreationHash = requestID, hash
	input.ProjectContext = ""
	input.ContextGeneration = &ProjectContextGeneration{Status: "pending"}
	mut, err := s.putProjectLocked(account, &input)
	s.store.projectsMu.Unlock()
	if err != nil {
		return nil, false, err
	}
	s.store.publishProjectRealtime(mut)
	return &input, true, nil
}

// ClaimProjectContext serializes launch and retry at the same project authority
// as updates. A two-minute provider deadline fits inside the five-minute lease.
func (s *SessionStore) ClaimProjectContext(account, id string, expected int, retry bool) (*ProjectRecord, error) {
	return s.UpdateProject(account, id, func(p *ProjectRecord) error {
		g := p.ContextGeneration
		now := time.Now().UnixMilli()
		if g == nil || g.Attempt != expected || g.Status == "ready" || (g.Status == "running" && g.LeaseUntil > now) || (g.Status == "failed" && !retry) {
			return ErrProjectContextStale
		}
		g.Attempt++
		g.Status, g.Error = "running", ""
		g.LeaseUntil = now + (5 * time.Minute).Milliseconds()
		return nil
	})
}

func (s *SessionStore) FinishProjectContext(account, id string, attempt int, content, failure, alert string) (*ProjectRecord, error) {
	return s.UpdateProject(account, id, func(p *ProjectRecord) error {
		g := p.ContextGeneration
		if g == nil || g.Attempt != attempt || g.Status != "running" {
			return ErrProjectContextStale
		}
		g.LeaseUntil, g.RouterAlert = 0, alert
		if failure != "" {
			g.Status, g.Error = "failed", failure
			return nil
		}
		if content == "" {
			return errors.New("empty project context")
		}
		p.ProjectContext, g.Status, g.Error = content, "ready", ""
		return nil
	})
}

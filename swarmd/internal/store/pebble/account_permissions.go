package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
)

// AccountPermissions is the account-wide access authority. Missing entries use
// the safe defaults; live device and fleet state is not persisted here.
type AccountPermissions struct {
	TasksLastUsed int64                           `json:"tasks_last_used"`
	Revision      uint64                          `json:"revision"`
	Tasks         string                          `json:"tasks"`
	Devices       map[string]DevicePermissionRule `json:"devices"`
	Linked        map[string]LinkedPermissionRule `json:"linked"`
}
type PermissionChange struct {
	By string `json:"by"`
	At int64  `json:"at"`
}
type DevicePermissionRule struct {
	Files    string           `json:"files"`
	Browser  string           `json:"browser"`
	Changed  PermissionChange `json:"changed"`
	LastUsed int64            `json:"last_used"`
}
type LinkedPermissionRule struct {
	Work     string           `json:"work"`
	Changed  PermissionChange `json:"changed"`
	LastUsed int64            `json:"last_used"`
}

var ErrAccountPermissionsConflict = errors.New("permissions revision conflict")

func DefaultAccountPermissions() AccountPermissions {
	return AccountPermissions{Tasks: "approve", Devices: map[string]DevicePermissionRule{}, Linked: map[string]LinkedPermissionRule{}}
}
func ValidAccessRule(value string) bool {
	return value == "off" || value == "ask" || value == "allowed"
}
func (p AccountPermissions) Device(id string) DevicePermissionRule {
	if rule, ok := p.Devices[id]; ok {
		return rule
	}
	return DevicePermissionRule{Files: "off", Browser: "off"}
}
func (p AccountPermissions) Link(name string) LinkedPermissionRule {
	if rule, ok := p.Linked[name]; ok {
		return rule
	}
	return LinkedPermissionRule{Work: "ask"}
}
func (p AccountPermissions) Validate() error {
	if p.Tasks != "approve" && p.Tasks != "auto" {
		return errors.New("tasks must be approve or auto")
	}
	for id, rule := range p.Devices {
		if strings.TrimSpace(id) == "" || !ValidAccessRule(rule.Files) || !ValidAccessRule(rule.Browser) {
			return errors.New("device rules must be off, ask or allowed and require a client session id")
		}
	}
	for name, rule := range p.Linked {
		if strings.TrimSpace(name) == "" || !ValidAccessRule(rule.Work) {
			return errors.New("linked rules must be off, ask or allowed and require a machine name")
		}
	}
	return nil
}
func (s *Store) GetAccountPermissions(account string) (AccountPermissions, error) {
	p := DefaultAccountPermissions()
	if strings.TrimSpace(account) == "" {
		return p, errors.New("permissions account required")
	}
	_, err := s.GetJSON("account_permissions/"+keyPart(account), &p)
	return p, err
}
func (s *Store) SetAccountPermissions(account string, expected uint64, p AccountPermissions) (AccountPermissions, error) {
	if err := p.Validate(); err != nil {
		return AccountPermissions{}, err
	}
	unlock := s.sessionMutations.lockSessions("account_permissions:" + account)
	defer unlock()
	current, err := s.GetAccountPermissions(account)
	if err != nil {
		return current, err
	}
	if current.Revision != expected || expected == ^uint64(0) {
		return current, ErrAccountPermissionsConflict
	}
	p.Revision = expected + 1
	return p, s.PutJSON("account_permissions/"+keyPart(account), p)
}
func (s *SessionStore) GetAccountPermissions(account string) (AccountPermissions, error) {
	return s.store.GetAccountPermissions(account)
}
func (s *PermissionStore) GetAccountPermissions(account string) (AccountPermissions, error) {
	return s.store.GetAccountPermissions(account)
}

// CountPermissionWaitingTasks counts account-owned, non-archived tasks directly,
// without the UI list limits (which could silently undercount).
func (s *Store) CountPermissionWaitingTasks(account string) (int, error) {
	count := 0
	err := s.IteratePrefix(KeyProjectTaskAccountPrefix+keyPart(account)+"/", 0, func(_ string, raw []byte) error {
		var task ProjectTaskRecord
		if err := json.Unmarshal(raw, &task); err != nil {
			return err
		}
		if task.AccountID == account && !task.Archived && task.Status == "pending_approval" {
			count++
		}
		return nil
	})
	return count, err
}

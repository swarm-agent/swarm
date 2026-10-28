package pebblestore

import (
	"encoding/json"
	"errors"
	"strings"
)

// AccountPermissions is the account-wide access authority. Missing entries use
// the defaults; live device and fleet state is not persisted here.
//
// Swarm does not ask before ordinary actions. The only approvals are before
// tasks start: Start tasks approve on the home, and linked work ask on a
// linked machine. Device rules are on or off.
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
	Files   string `json:"files"`
	Browser string `json:"browser"`
	// WasAsk names the rules ("files", "browser") that were stored as the
	// retired ask value and now read as off, until that device changes them.
	WasAsk   []string         `json:"was_ask,omitempty"`
	Changed  PermissionChange `json:"changed"`
	LastUsed int64            `json:"last_used"`
}
type LinkedPermissionRule struct {
	Work     string           `json:"work"`
	Changed  PermissionChange `json:"changed"`
	LastUsed int64            `json:"last_used"`
}

// Rule values.
const (
	RuleOff     = "off"
	RuleAllowed = "allowed" // device rules
	RuleAsk     = "ask"     // linked work: ask before sending tasks
	RuleAuto    = "auto"    // linked work and Start tasks: runs without asking
	// DefaultLinkedWork is the rule for a newly linked machine.
	DefaultLinkedWork = RuleAuto
)

var ErrAccountPermissionsConflict = errors.New("permissions revision conflict")

// ErrDeviceAskRetired refuses the retired device ask value.
var ErrDeviceAskRetired = errors.New("device rules are off or allowed; ask was removed (Swarm does not ask before device calls)")

func DefaultAccountPermissions() AccountPermissions {
	return AccountPermissions{Tasks: RuleAuto, Devices: map[string]DevicePermissionRule{}, Linked: map[string]LinkedPermissionRule{}}
}

// ValidDeviceRule reports whether value is a device files or browser rule.
func ValidDeviceRule(value string) bool {
	return value == RuleOff || value == RuleAllowed
}

// ValidLinkedRule reports whether value is a linked work rule.
func ValidLinkedRule(value string) bool {
	return value == RuleOff || value == RuleAsk || value == RuleAuto
}

// NormalizeLinkedRule maps the retired linked value allowed to auto.
func NormalizeLinkedRule(value string) string {
	if value == RuleAllowed {
		return RuleAuto
	}
	return value
}

// LinkedWorkOn reports whether a linked work rule lets work reach the machine
// (ask or auto; off refuses everything).
func LinkedWorkOn(value string) bool {
	value = NormalizeLinkedRule(value)
	return value == RuleAsk || value == RuleAuto
}

// normalize migrates stored values: linked allowed reads auto, and a device
// ask reads off (recorded in WasAsk so clients can say why).
func (p *AccountPermissions) normalize() {
	if p.Devices == nil {
		p.Devices = map[string]DevicePermissionRule{}
	}
	if p.Linked == nil {
		p.Linked = map[string]LinkedPermissionRule{}
	}
	for id, rule := range p.Devices {
		p.Devices[id] = normalizeDeviceRule(rule)
	}
	for name, rule := range p.Linked {
		rule.Work = NormalizeLinkedRule(rule.Work)
		p.Linked[name] = rule
	}
}
func normalizeDeviceRule(rule DevicePermissionRule) DevicePermissionRule {
	if rule.Files == RuleAsk {
		rule.Files = RuleOff
		rule.WasAsk = appendUnique(rule.WasAsk, "files")
	}
	if rule.Browser == RuleAsk {
		rule.Browser = RuleOff
		rule.WasAsk = appendUnique(rule.WasAsk, "browser")
	}
	return rule
}
func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(append([]string(nil), list...), value)
}
func (p AccountPermissions) Device(id string) DevicePermissionRule {
	if rule, ok := p.Devices[id]; ok {
		return normalizeDeviceRule(rule)
	}
	return DevicePermissionRule{Files: RuleOff, Browser: RuleOff}
}
func (p AccountPermissions) Link(name string) LinkedPermissionRule {
	if rule, ok := p.Linked[name]; ok {
		rule.Work = NormalizeLinkedRule(rule.Work)
		return rule
	}
	return LinkedPermissionRule{Work: DefaultLinkedWork}
}
func (p AccountPermissions) Validate() error {
	if p.Tasks != "approve" && p.Tasks != RuleAuto {
		return errors.New("tasks must be approve or auto")
	}
	for id, rule := range p.Devices {
		if strings.TrimSpace(id) == "" {
			return errors.New("device rules require a client session id")
		}
		if rule.Files == RuleAsk || rule.Browser == RuleAsk {
			return ErrDeviceAskRetired
		}
		if !ValidDeviceRule(rule.Files) || !ValidDeviceRule(rule.Browser) {
			return errors.New("device rules must be off or allowed")
		}
	}
	for name, rule := range p.Linked {
		if strings.TrimSpace(name) == "" || !ValidLinkedRule(rule.Work) {
			return errors.New("linked rules must be off, ask or auto and require a machine name")
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
	p.normalize()
	return p, err
}

// SetAccountPermissions stores p when expected is the current revision. A
// linked allowed (the retired value) is stored as auto; a device ask is refused.
func (s *Store) SetAccountPermissions(account string, expected uint64, p AccountPermissions) (AccountPermissions, error) {
	for name, rule := range p.Linked {
		rule.Work = NormalizeLinkedRule(rule.Work)
		p.Linked[name] = rule
	}
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

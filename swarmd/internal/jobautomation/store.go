package jobautomation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Durable keys. Account ids and job ids are hex-encoded inside keys, since
// both may hold '/'.
const (
	keyDefinitionPrefix = "job_automation/def/"
	keyDeliveryPrefix   = "job_automation/delivery/"
	keyByJobPrefix      = "job_automation/by_job/"
	keyActivePrefix     = "job_automation/active/"
	keyStepLogPrefix    = "job_automation/log/"
	keyStop             = "job_automation_meta/stop"
	keyCursor           = "job_automation_meta/cursor"
	// MaxLogTailBytes is how much of a step's output is kept: the last 64 KiB.
	MaxLogTailBytes = 64 << 10
	maxDefinitions  = 500
)

// Step and delivery states.
const (
	StatePending   = "pending"
	StateRunning   = "running"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateSkipped   = "skipped"
	StateStopped   = "stopped"
)

// ErrNotFound is an automation or delivery that does not exist (for this
// account).
var ErrNotFound = errors.New("not found")

// Delivery is one (job end, automation revision) pair: the steps run once
// for it. It keeps the automation as it was when the job ended.
type Delivery struct {
	ID                 string       `json:"id"`
	Account            string       `json:"account"`
	AutomationID       string       `json:"automation_id"`
	AutomationName     string       `json:"automation_name"`
	AutomationRevision uint64       `json:"automation_revision"`
	JobID              string       `json:"job_id"`
	Followup           string       `json:"followup,omitempty"`
	// Round counts how many times the job's session was reopened for this
	// delivery (part B); 0 for now.
	Round      int          `json:"round"`
	State      string       `json:"state"`
	Retries    int          `json:"retries"`
	Folder     string       `json:"folder"`
	Steps      []StepRecord `json:"steps"`
	Definition []Step       `json:"definition"`
	Result     Result       `json:"result"`
	CreatedAt  int64        `json:"created_at"`
	StartedAt  int64        `json:"started_at,omitempty"`
	FinishedAt int64        `json:"finished_at,omitempty"`
	// Reruns counts explicit re-runs (swarm_deliver_job automation: true).
	Reruns int `json:"reruns,omitempty"`
}

// StepRecord is one step of one delivery.
type StepRecord struct {
	ID            string         `json:"id"`
	Type          string         `json:"type"`
	State         string         `json:"state"`
	Attempts      int            `json:"attempts"`
	StartedAt     int64          `json:"started_at,omitempty"`
	FinishedAt    int64          `json:"finished_at,omitempty"`
	NextAttemptAt int64          `json:"next_attempt_at,omitempty"`
	ExitCode      *int           `json:"exit_code,omitempty"`
	Output        string         `json:"output,omitempty"`
	Reason        string         `json:"reason,omitempty"`
	Next          string         `json:"next,omitempty"`
	Outcome       map[string]any `json:"outcome,omitempty"`
	LogTail       string         `json:"log_tail,omitempty"`
}

// Terminal reports whether a delivery or step state is final.
func Terminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateSkipped, StateStopped:
		return true
	}
	return false
}

// StopState is the machine-wide stop switch.
type StopState struct {
	Stopped bool   `json:"stopped"`
	At      int64  `json:"at,omitempty"`
	By      string `json:"by,omitempty"`
}

// Store keeps definitions and deliveries in the daemon's Pebble store.
type Store struct {
	db  *pebblestore.Store
	now func() time.Time
	mu  sync.Mutex
}

func NewStore(db *pebblestore.Store) *Store {
	return &Store{db: db, now: time.Now}
}

func hexKey(s string) string { return hex.EncodeToString([]byte(s)) }

func definitionKey(account, id string) string {
	return keyDefinitionPrefix + hexKey(account) + "/" + id
}

func deliveryKey(account, id string) string {
	return keyDeliveryPrefix + hexKey(account) + "/" + id
}

func byJobKey(account, jobID, deliveryID string) string {
	return keyByJobPrefix + hexKey(account) + "/" + hexKey(jobID) + "/" + deliveryID
}

func stepLogKey(deliveryID, stepID string) string {
	return keyStepLogPrefix + deliveryID + "/" + stepID
}

func newID(prefix string) (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}

// DeliveryID is the stable id of one (job end, automation revision) pair.
func DeliveryID(jobID, followup, automationID string, revision uint64) string {
	sum := sha256.Sum256([]byte(jobID + "\x00" + followup + "\x00" + automationID + "\x00" + strconv.FormatUint(revision, 10)))
	return "jdel_" + hex.EncodeToString(sum[:12])
}

// ---- definitions -----------------------------------------------------------

// Put creates or updates (by name) an account's automation. The revision
// increases on every update.
func (s *Store) Put(account string, in Automation) (Automation, bool, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return Automation{}, false, errors.New("account is required")
	}
	if err := in.Normalize(); err != nil {
		return Automation{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, err := s.listLocked(account)
	if err != nil {
		return Automation{}, false, err
	}
	now := s.now().UnixMilli()
	for _, current := range existing {
		if current.Name == in.Name {
			in.ID, in.Account, in.CreatedAt = current.ID, account, current.CreatedAt
			in.Revision, in.UpdatedAt = current.Revision+1, now
			return in, false, s.db.PutJSON(definitionKey(account, in.ID), in)
		}
	}
	if len(existing) >= maxDefinitions {
		return Automation{}, false, invalid("an account has at most %d automations", maxDefinitions)
	}
	id, err := newID("jauto_")
	if err != nil {
		return Automation{}, false, err
	}
	in.ID, in.Account, in.Revision, in.CreatedAt, in.UpdatedAt = id, account, 1, now, now
	return in, true, s.db.PutJSON(definitionKey(account, id), in)
}

// Get reads one automation of the account.
func (s *Store) Get(account, id string) (Automation, error) {
	var a Automation
	found, err := s.db.GetJSON(definitionKey(account, id), &a)
	if err != nil {
		return Automation{}, err
	}
	if !found || a.Account != account {
		return Automation{}, ErrNotFound
	}
	return a, nil
}

// FindByName reads an account's automation by name.
func (s *Store) FindByName(account, name string) (Automation, error) {
	list, err := s.List(account)
	if err != nil {
		return Automation{}, err
	}
	for _, a := range list {
		if a.Name == strings.TrimSpace(name) {
			return a, nil
		}
	}
	return Automation{}, ErrNotFound
}

// List returns an account's automations, oldest first.
func (s *Store) List(account string) ([]Automation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.listLocked(account)
}

func (s *Store) listLocked(account string) ([]Automation, error) {
	out := []Automation{}
	err := s.db.IteratePrefix(keyDefinitionPrefix+hexKey(account)+"/", maxDefinitions+1, func(_ string, value []byte) error {
		var a Automation
		if err := json.Unmarshal(value, &a); err != nil {
			return fmt.Errorf("decode automation: %w", err)
		}
		if a.Account == account {
			out = append(out, a)
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt != out[j].CreatedAt {
			return out[i].CreatedAt < out[j].CreatedAt
		}
		return out[i].ID < out[j].ID
	})
	return out, err
}

// Delete removes an automation. Its deliveries stay as records; unfinished
// ones still finish with the revision they started with.
func (s *Store) Delete(account, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var a Automation
	found, err := s.db.GetJSON(definitionKey(account, id), &a)
	if err != nil {
		return err
	}
	if !found || a.Account != account {
		return ErrNotFound
	}
	return s.db.Delete(definitionKey(account, id))
}

// ---- deliveries ------------------------------------------------------------

// CreateDelivery records a delivery unless one with its id exists already.
// created is false for a delivery that exists: a job end delivers once.
func (s *Store) CreateDelivery(a Automation, result Result) (Delivery, bool, error) {
	id := DeliveryID(result.JobID, result.Followup, a.ID, a.Revision)
	s.mu.Lock()
	defer s.mu.Unlock()
	var existing Delivery
	found, err := s.db.GetJSON(deliveryKey(a.Account, id), &existing)
	if err != nil {
		return Delivery{}, false, err
	}
	if found {
		return existing, false, nil
	}
	d := Delivery{
		ID: id, Account: a.Account, AutomationID: a.ID, AutomationName: a.Name, AutomationRevision: a.Revision,
		JobID: result.JobID, Followup: result.Followup, State: StatePending, Retries: a.Retries, Folder: a.Scope.Folder,
		Definition: append([]Step(nil), a.Steps...), Result: result, CreatedAt: s.now().UnixMilli(),
	}
	for _, step := range a.Steps {
		d.Steps = append(d.Steps, StepRecord{ID: step.ID, Type: step.Type, State: StatePending})
	}
	if err := s.putDeliveryLocked(d); err != nil {
		return Delivery{}, false, err
	}
	if err := s.db.PutJSON(byJobKey(a.Account, result.JobID, id), id); err != nil {
		return Delivery{}, false, err
	}
	return d, true, nil
}

func (s *Store) putDeliveryLocked(d Delivery) error {
	for i := range d.Steps {
		d.Steps[i].LogTail = "" // kept under its own key
	}
	if err := s.db.PutJSON(deliveryKey(d.Account, d.ID), d); err != nil {
		return err
	}
	if Terminal(d.State) {
		return s.db.Delete(keyActivePrefix + d.ID)
	}
	return s.db.PutJSON(keyActivePrefix+d.ID, d.Account)
}

// SaveDelivery writes a delivery's new state.
func (s *Store) SaveDelivery(d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.putDeliveryLocked(d)
}

// GetDelivery reads one delivery of the account.
func (s *Store) GetDelivery(account, id string) (Delivery, error) {
	var d Delivery
	found, err := s.db.GetJSON(deliveryKey(account, id), &d)
	if err != nil {
		return Delivery{}, err
	}
	if !found || d.Account != account {
		return Delivery{}, ErrNotFound
	}
	return d, nil
}

// WithLogs fills each step's log tail.
func (s *Store) WithLogs(d Delivery) Delivery {
	steps := append([]StepRecord(nil), d.Steps...)
	for i := range steps {
		var tail string
		if found, err := s.db.GetJSON(stepLogKey(d.ID, steps[i].ID), &tail); err == nil && found {
			steps[i].LogTail = tail
		}
	}
	d.Steps = steps
	return d
}

// PutStepLog keeps the last MaxLogTailBytes of a step's output.
func (s *Store) PutStepLog(deliveryID, stepID string, tail []byte) error {
	if len(tail) > MaxLogTailBytes {
		tail = tail[len(tail)-MaxLogTailBytes:]
	}
	return s.db.PutJSON(stepLogKey(deliveryID, stepID), string(tail))
}

// JobDeliveries lists a job's deliveries, newest first.
func (s *Store) JobDeliveries(account, jobID string) ([]Delivery, error) {
	ids := []string{}
	err := s.db.IteratePrefix(keyByJobPrefix+hexKey(account)+"/"+hexKey(jobID)+"/", 1000, func(_ string, value []byte) error {
		var id string
		if err := json.Unmarshal(value, &id); err != nil {
			return err
		}
		ids = append(ids, id)
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []Delivery{}
	for _, id := range ids {
		d, err := s.GetDelivery(account, id)
		if errors.Is(err, ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	sortDeliveries(out)
	return out, nil
}

// DeliveryFilter selects deliveries for a listing.
type DeliveryFilter struct {
	JobID        string
	AutomationID string
	State        string
	Limit        int
}

// ListDeliveries lists an account's deliveries, newest first.
func (s *Store) ListDeliveries(account string, filter DeliveryFilter) ([]Delivery, error) {
	if filter.JobID != "" {
		list, err := s.JobDeliveries(account, filter.JobID)
		if err != nil {
			return nil, err
		}
		return limitDeliveries(filterDeliveries(list, filter), filter.Limit), nil
	}
	out := []Delivery{}
	err := s.db.IteratePrefix(keyDeliveryPrefix+hexKey(account)+"/", 100000, func(_ string, value []byte) error {
		var d Delivery
		if err := json.Unmarshal(value, &d); err != nil {
			return fmt.Errorf("decode delivery: %w", err)
		}
		if d.Account == account {
			out = append(out, d)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	out = filterDeliveries(out, filter)
	sortDeliveries(out)
	return limitDeliveries(out, filter.Limit), nil
}

func filterDeliveries(in []Delivery, filter DeliveryFilter) []Delivery {
	out := in[:0]
	for _, d := range in {
		if filter.AutomationID != "" && d.AutomationID != filter.AutomationID {
			continue
		}
		if filter.State != "" && d.State != filter.State {
			continue
		}
		out = append(out, d)
	}
	return out
}

func sortDeliveries(list []Delivery) {
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].CreatedAt != list[j].CreatedAt {
			return list[i].CreatedAt > list[j].CreatedAt
		}
		return list[i].ID < list[j].ID
	})
}

func limitDeliveries(list []Delivery, limit int) []Delivery {
	if limit <= 0 {
		limit = 50
	}
	if len(list) > limit {
		return list[:limit]
	}
	return list
}

// Unfinished lists every delivery that is not finished (any account).
func (s *Store) Unfinished() ([]Delivery, error) {
	type ref struct{ id, account string }
	refs := []ref{}
	err := s.db.IteratePrefix(keyActivePrefix, 100000, func(key string, value []byte) error {
		var account string
		if err := json.Unmarshal(value, &account); err != nil {
			return err
		}
		refs = append(refs, ref{id: strings.TrimPrefix(key, keyActivePrefix), account: account})
		return nil
	})
	if err != nil {
		return nil, err
	}
	out := []Delivery{}
	for _, r := range refs {
		d, err := s.GetDelivery(r.account, r.id)
		if errors.Is(err, ErrNotFound) {
			_ = s.db.Delete(keyActivePrefix + r.id)
			continue
		}
		if err != nil {
			return nil, err
		}
		if !Terminal(d.State) {
			out = append(out, d)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, nil
}

// Rerun makes a finished delivery run again from its failed or stopped
// steps: those steps and the ones after them that never ran become pending;
// a step that succeeded is never run again. It returns the delivery and
// whether there was anything to re-run.
func (s *Store) Rerun(account, id string) (Delivery, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var d Delivery
	found, err := s.db.GetJSON(deliveryKey(account, id), &d)
	if err != nil {
		return Delivery{}, false, err
	}
	if !found || d.Account != account {
		return Delivery{}, false, ErrNotFound
	}
	if !Terminal(d.State) || (d.State != StateFailed && d.State != StateStopped) {
		return d, false, nil
	}
	reset := false
	for i := range d.Steps {
		step := &d.Steps[i]
		switch step.State {
		case StateFailed, StateStopped:
			reset = true
		case StateSkipped, StatePending:
			if !reset {
				continue
			}
		default:
			continue
		}
		*step = StepRecord{ID: step.ID, Type: step.Type, State: StatePending}
	}
	if !reset {
		return d, false, nil
	}
	d.State, d.FinishedAt = StatePending, 0
	d.Reruns++
	return d, true, s.putDeliveryLocked(d)
}

// ---- stop switch and cursor --------------------------------------------------

func (s *Store) StopState() (StopState, error) {
	var st StopState
	_, err := s.db.GetJSON(keyStop, &st)
	return st, err
}

// SetStopped turns the machine-wide stop switch on or off.
func (s *Store) SetStopped(stopped bool, by string) (StopState, error) {
	st := StopState{Stopped: stopped, At: s.now().UnixMilli(), By: by}
	return st, s.db.PutJSON(keyStop, st)
}

// Cursor is the last signal sequence the consumer handled; ok is false
// before the consumer first ran.
func (s *Store) Cursor() (uint64, bool, error) {
	var cursor uint64
	found, err := s.db.GetJSON(keyCursor, &cursor)
	return cursor, found, err
}

func (s *Store) SetCursor(seq uint64) error {
	return s.db.PutJSON(keyCursor, seq)
}

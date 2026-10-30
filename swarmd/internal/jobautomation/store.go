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

	"github.com/cockroachdb/pebble"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Durable keys. Account ids and job ids are hex-encoded inside keys, since
// both may hold '/'.
const (
	keyDefinitionPrefix   = "job_automation/def/"
	keyPendingItemsPrefix = "job_automation/pending_items/"
	keyDeliveryPrefix     = "job_automation/delivery/"
	keyByJobPrefix        = "job_automation/by_job/"
	keyActivePrefix       = "job_automation/active/"
	keyStepLogPrefix      = "job_automation/log/"
	keyReopenPrefix       = "job_automation/reopen/"
	keyStop               = "job_automation_meta/stop"
	keyCursor             = "job_automation_meta/cursor"
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
	// StateWaiting is a reopen step waiting, within its timeout_seconds, for
	// its job's run or follow-up to settle before it sends; its reason says
	// what it waits for. It is also an agent step whose job ended on a
	// provider's usage, rate or session limit, waiting until NextAttemptAt
	// (the stated reset, else a bounded backoff) to run again. It is not
	// final.
	StateWaiting = "waiting"
	// StateBlocked ends a delivery whose reopen step ran out of rounds or
	// had an unreadable review to send.
	StateBlocked = "blocked"
	// StateSentBack ends a delivery whose reopen step sent the work back
	// into its own session (a review's findings, a failed push to rebase):
	// nothing after it handed the work over, and the follow-up's end starts
	// a fresh delivery. Delivery.SentBack says which step, round and why.
	StateSentBack = "sent_back"
)

// ReasonRoundsExhausted starts a reopen step's failure when another reopen
// would exceed its max_rounds ("rounds_exhausted: step <id>"), or the job's
// overall bound over all reopen steps ("rounds_exhausted: job").
const ReasonRoundsExhausted = "rounds_exhausted"

// RoundsExhaustedReason is the blocked reason when the reopen step id ran out
// of its own max_rounds.
func RoundsExhaustedReason(stepID string) string {
	return ReasonRoundsExhausted + ": step " + stepID
}

// ReasonJobRoundsExhausted is the blocked reason when the job reached its
// overall safety bound: the sum of the reopen steps' max_rounds.
const ReasonJobRoundsExhausted = ReasonRoundsExhausted + ": job"

// ReasonDuplicateHead is why a job end created no delivery: the automation
// already has a delivery (active or finished) for the job at that head.
const ReasonDuplicateHead = "duplicate_head"

// Reasons swarm_deliver_job automation=true leaves a delivery unchanged.
const (
	// ReasonSuperseded: an older delivery; only the newest is re-run.
	ReasonSuperseded = "superseded"
	// ReasonStaleHead: the newest delivery's head is no longer the job's.
	ReasonStaleHead = "stale_head"
	// ReasonNothingToRerun: no failed or stopped step.
	ReasonNothingToRerun = "nothing_to_rerun"
)

// ReasonReviewUnreadable starts a reopen step's failure ("review unreadable:
// <reviewer job id>") when the review its from step recorded could not be
// read: nothing is sent and no round is used.
const ReasonReviewUnreadable = "review unreadable"

// ErrNotFound is an automation or delivery that does not exist (for this
// account).
var ErrNotFound = errors.New("not found")

// Delivery is one (job end, automation revision) pair: the steps run once
// for it. It keeps the automation as it was when the job ended.
type Delivery struct {
	ItemOwner          string `json:"item_owner,omitempty"`
	ID                 string `json:"id"`
	Account            string `json:"account"`
	AutomationID       string `json:"automation_id"`
	AutomationName     string `json:"automation_name"`
	AutomationRevision uint64 `json:"automation_revision"`
	JobID              string `json:"job_id"`
	Followup           string `json:"followup,omitempty"`
	// Round counts the reopens this automation already sent into the job,
	// across all of its deliveries and reopen steps for the job (per
	// automation and job, not per delivery). RoundBase is where an explicit
	// re-run of a blocked delivery restarted the count; the job's overall
	// bound (the sum of its reopen steps' max_rounds) applies past it, and a
	// later delivery of the same job keeps it.
	Round     int `json:"round"`
	RoundBase int `json:"round_base,omitempty"`
	// StepRoundBase is RoundBase per reopen step id: each reopen step allows
	// its own max_rounds past its base, counting only the reopens it sent.
	StepRoundBase map[string]int `json:"step_round_base,omitempty"`
	State         string         `json:"state"`
	Retries       int            `json:"retries"`
	Folder        string         `json:"folder"`
	Steps         []StepRecord   `json:"steps"`
	Definition    []Step         `json:"definition"`
	Result        Result         `json:"result"`
	CreatedAt     int64          `json:"created_at"`
	StartedAt     int64          `json:"started_at,omitempty"`
	FinishedAt    int64          `json:"finished_at,omitempty"`
	// Reruns counts explicit re-runs (swarm_deliver_job automation: true).
	Reruns int `json:"reruns,omitempty"`
	// Head is the commit the delivery delivers: the triggering job's head,
	// moved forward by an agent step whose job committed on top of it.
	// HeadWorktree is the checkout that commit can be fetched from.
	Head         string `json:"head,omitempty"`
	HeadWorktree string `json:"head_worktree,omitempty"`
	// BlockedReason is why the delivery ended blocked (rounds_exhausted, or
	// "review unreadable: <reviewer job id>").
	BlockedReason string `json:"blocked_reason,omitempty"`
	// SentBack is set when the delivery ended sent_back: the reopen step
	// that sent the work back, its round and why it ran.
	SentBack *SentBack `json:"sent_back,omitempty"`
	// Spec is the workflow's result as the delivery started (Automation.Result).
	Spec *ResultSpec `json:"result_spec,omitempty"`
	// Produced is what this delivery's produce steps wrote (see
	// CurrentDeliverables).
	Produced []Deliverable `json:"produced,omitempty"`
}

// SentBack is how a delivery that ended sent_back sent the work back.
type SentBack struct {
	// Step is the reopen step that sent the follow-up.
	Step string `json:"step"`
	// Round is the reopen round the follow-up started (1 is the first).
	Round int `json:"round"`
	// Reason is why the step ran, from its conditions ("review verdict
	// block", "push failed: exited with code 1"); "" when it ran
	// unconditionally.
	Reason string `json:"reason,omitempty"`
}

// ReopenRecord is what a reopen step left for the follow-up it sent, so the
// follow-up's end continues the round count.
type ReopenRecord struct {
	DeliveryID   string `json:"delivery_id"`
	AutomationID string `json:"automation_id"`
	// Step is the reopen step that sent the follow-up; "" in records
	// written before rounds counted per step (see ReopenRounds).
	Step      string `json:"step,omitempty"`
	Round     int    `json:"round"`
	RoundBase int    `json:"round_base,omitempty"`
}

// ReopenRounds is how many follow-ups an automation's reopen steps sent into
// one job: in total and per reopen step id. Unattributed counts older records
// whose step could not be read back; they count against every step.
type ReopenRounds struct {
	Total        int
	Steps        map[string]int
	Unattributed int
}

// Step is how many reopens count against the reopen step id.
func (r ReopenRounds) Step(id string) int {
	return r.Steps[id] + r.Unattributed
}

// StepRecord is one step of one delivery.
type StepRecord struct {
	Items         []ItemRecord `json:"items"`
	ID            string       `json:"id"`
	Type          string       `json:"type"`
	State         string       `json:"state"`
	Attempts      int          `json:"attempts"`
	StartedAt     int64        `json:"started_at,omitempty"`
	FinishedAt    int64        `json:"finished_at,omitempty"`
	NextAttemptAt int64        `json:"next_attempt_at,omitempty"`
	// LimitWaits counts the agent step's jobs that ended on a provider's
	// usage, rate or session limit; each wait for the reset starts a new job
	// and uses none of the step's retries.
	LimitWaits int            `json:"limit_waits,omitempty"`
	ExitCode   *int           `json:"exit_code,omitempty"`
	Output     string         `json:"output,omitempty"`
	Reason     string         `json:"reason,omitempty"`
	Next       string         `json:"next,omitempty"`
	Outcome    map[string]any `json:"outcome,omitempty"`
	LogTail    string         `json:"log_tail,omitempty"`
	// Note says how the step ran differently from its stored definition
	// (LegacyCheckoutMigration), so the owner knows to resave it.
	Note string `json:"note,omitempty"`
	// Failures keeps each failed attempt's reason, newest last (at most
	// maxStepFailures). A rerun keeps them, so an attempt that failed
	// before the rerun still says why.
	Failures []AttemptFailure `json:"failures,omitempty"`
}

// AttemptFailure is one failed attempt of a step.
type AttemptFailure struct {
	Attempt int    `json:"attempt"`
	Rerun   int    `json:"rerun,omitempty"`
	At      int64  `json:"at"`
	Reason  string `json:"reason"`
}

// maxStepFailures bounds the failed attempts a step record keeps.
const maxStepFailures = 20

// recordFailure appends one failed attempt's reason, keeping the newest.
func (r *StepRecord) recordFailure(rerun int, at int64, reason string) {
	r.Failures = append(r.Failures, AttemptFailure{Attempt: r.Attempts, Rerun: rerun, At: at, Reason: reason})
	if extra := len(r.Failures) - maxStepFailures; extra > 0 {
		r.Failures = append([]AttemptFailure(nil), r.Failures[extra:]...)
	}
}

// resetStep makes a step pending again for a rerun, keeping its failures.
func resetStep(step *StepRecord) {
	*step = StepRecord{ID: step.ID, Type: step.Type, State: StatePending, Failures: step.Failures}
}

// Terminal reports whether a delivery or step state is final.
func Terminal(state string) bool {
	switch state {
	case StateSucceeded, StateFailed, StateSkipped, StateStopped, StateBlocked, StateSentBack:
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
	// notify is told of every committed delivery and evaluation change.
	notifyMu sync.RWMutex
	notify   func(account, projectID, jobID string)
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

func reopenKey(account, jobID, followup string) string {
	return keyReopenPrefix + hexKey(account) + "/" + hexKey(jobID) + "/" + hexKey(followup)
}

// PutReopen records the follow-up a reopen step sent into a job's session.
func (s *Store) PutReopen(account, jobID, followup string, rec ReopenRecord) error {
	if strings.TrimSpace(followup) == "" {
		return errors.New("a reopen needs the follow-up it sent")
	}
	return s.db.PutJSON(reopenKey(account, jobID, followup), rec)
}

// ReopenCount is how many follow-ups the automation's reopen steps sent into
// the job, over all of its deliveries.
func (s *Store) ReopenCount(account, jobID, automationID string) (int, error) {
	rounds, err := s.ReopenRounds(account, jobID, automationID)
	return rounds.Total, err
}

// ReopenRounds counts the follow-ups the automation's reopen steps sent into
// the job, over all of its deliveries, in total and per reopen step. A record
// without its step (written before rounds counted per step) is attributed to
// the reopen step of its delivery that recorded the follow-up; one that
// cannot be attributed counts against every step.
func (s *Store) ReopenRounds(account, jobID, automationID string) (ReopenRounds, error) {
	out := ReopenRounds{Steps: map[string]int{}}
	prefix := keyReopenPrefix + hexKey(account) + "/" + hexKey(jobID) + "/"
	type legacy struct{ deliveryID, followupHex string }
	var unnamed []legacy
	err := s.db.IteratePrefix(prefix, 100000, func(key string, value []byte) error {
		var rec ReopenRecord
		if err := json.Unmarshal(value, &rec); err != nil {
			return fmt.Errorf("decode reopen: %w", err)
		}
		if rec.AutomationID != automationID {
			return nil
		}
		out.Total++
		if rec.Step == "" {
			unnamed = append(unnamed, legacy{rec.DeliveryID, strings.TrimPrefix(key, prefix)})
		} else {
			out.Steps[rec.Step]++
		}
		return nil
	})
	if err != nil {
		return out, err
	}
	for _, rec := range unnamed {
		if step := s.legacyReopenStep(account, rec.deliveryID, rec.followupHex); step != "" {
			out.Steps[step]++
		} else {
			out.Unattributed++
		}
	}
	return out, nil
}

// legacyReopenStep reads which reopen step of the delivery recorded the
// follow-up (hex-encoded, as in its reopen key); "" when it cannot be read.
func (s *Store) legacyReopenStep(account, deliveryID, followupHex string) string {
	followup, err := hex.DecodeString(followupHex)
	if err != nil || deliveryID == "" {
		return ""
	}
	d, err := s.GetDelivery(account, deliveryID)
	if err != nil {
		return ""
	}
	for _, step := range d.Steps {
		if got, _ := step.Outcome["followup"].(string); step.Type == StepReopen && got != "" && got == string(followup) {
			return step.ID
		}
	}
	return ""
}

// reopenBases is each reopen step's current count: where an explicit re-run
// restarts that step's budget.
func reopenBases(steps []Step, rounds ReopenRounds) map[string]int {
	out := map[string]int{}
	for _, step := range steps {
		if step.Type == StepReopen {
			out[step.ID] = rounds.Step(step.ID)
		}
	}
	return out
}

// GetReopen reads what a reopen step recorded for a follow-up.
func (s *Store) GetReopen(account, jobID, followup string) (ReopenRecord, bool, error) {
	var rec ReopenRecord
	if strings.TrimSpace(followup) == "" {
		return rec, false, nil
	}
	found, err := s.db.GetJSON(reopenKey(account, jobID, followup), &rec)
	return rec, found, err
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
// increases on each actual change.
func (s *Store) Put(account string, in Automation) (Automation, bool, error) {
	return s.PutChange(account, in, Change{Source: "set", Who: "owner"})
}

func (s *Store) PutChange(account string, in Automation, change Change) (Automation, bool, error) {
	in.Proposed, in.ProposedBy, in.BaseID, in.BaseRevision = false, "", "", 0
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
		if !current.Proposed && current.Name == in.Name {
			if len(changedFields(&current, &in)) == 0 && change.Source != "restore" {
				return current, false, nil
			}
			in.ID, in.Account, in.CreatedAt = current.ID, account, current.CreatedAt
			in.Revision, in.UpdatedAt = current.Revision+1, now
			kind := "updated"
			if change.Source == "restore" {
				kind = "restored"
			}
			return in, false, s.saveHistoryLocked(in, &current, kind, change)
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
	records, historyErr := s.historyLocked(account, "", in.Name)
	if historyErr != nil && historyErr != ErrNotFound {
		return Automation{}, false, historyErr
	}
	if len(records) > 0 {
		var oldID string
		if _, err := s.db.GetJSON(historyNameKey(account, in.Name), &oldID); err != nil {
			return Automation{}, false, err
		}
		in.ID, in.Revision = oldID, records[0].Revision+1
	}
	kind := "created"
	if change.Source == "restore" {
		kind = "restored"
	}
	return in, true, s.saveHistoryLocked(in, nil, kind, change)
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
		if !a.Proposed && a.Name == strings.TrimSpace(name) {
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
	return s.DeleteChange(account, id, Change{Source: "delete", Who: "owner"})
}

func (s *Store) DeleteChange(account, id string, change Change) error {
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
	previous := a
	a.Revision++
	a.UpdatedAt = s.now().UnixMilli()
	change.Source = "delete"
	return s.saveHistoryLocked(a, &previous, "deleted", change)
}

// ---- deliveries ------------------------------------------------------------

// TriggerHead is the head of the job end a delivery was created for (its
// Head may since have moved forward); "" when the job had no commits.
func (d Delivery) TriggerHead() string {
	if d.Result.Git != nil {
		return d.Result.Git.Head
	}
	return ""
}

// CreateDelivery records a delivery unless one with its id exists already.
// created is false for a delivery that exists: a job end delivers once. It is
// also false when the automation already has a delivery (active or finished)
// for the job at the job end's head: the existing delivery, whose id differs
// from DeliveryID of this job end, is returned and nothing is recorded (at
// most one delivery per automation, job and head), except when a blocked
// follow-up must record the reason this automation's reopen could not finish.
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
	previous, err := s.JobDeliveries(a.Account, result.JobID)
	if err != nil {
		return Delivery{}, false, err
	}
	head := ""
	if result.Git != nil {
		head = result.Git.Head
	}
	// Inspect all prior deliveries before deduplicating: the reopen may be
	// recorded on a different delivery than the first matching head.
	reopened := false
	if result.Followup != "" {
		for _, p := range previous {
			if p.AutomationID != a.ID {
				continue
			}
			for _, step := range p.Steps {
				if step.Type == StepReopen && step.Outcome["followup"] == result.Followup {
					reopened = true
				}
			}
		}
	}
	createdAt := s.now().UnixMilli()
	roundBase := 0
	stepBase := map[string]int{}
	for _, p := range previous {
		// Deliveries of one job are ordered by creation: a new one is
		// always the newest, even within the same millisecond.
		if p.CreatedAt >= createdAt {
			createdAt = p.CreatedAt + 1
		}
		if p.AutomationID != a.ID {
			continue
		}
		if !(reopened && strings.HasPrefix(result.Error, "blocked: ")) && head != "" && p.TriggerHead() == head {
			return p, false, nil
		}
		roundBase = max(roundBase, p.RoundBase)
		for id, base := range p.StepRoundBase {
			stepBase[id] = max(stepBase[id], base)
		}
	}
	a, err = a.RenderInputs()
	if err != nil {
		return Delivery{}, false, err
	}
	d := Delivery{ItemOwner: a.TriggerOwner,
		ID: id, Account: a.Account, AutomationID: a.ID, AutomationName: a.Name, AutomationRevision: a.Revision,
		JobID: result.JobID, Followup: result.Followup, State: StatePending, Retries: a.Retries, Folder: a.Scope.Folder,
		Definition: append([]Step(nil), a.Steps...), Result: result, CreatedAt: createdAt, Spec: a.Result,
	}
	if result.Git != nil {
		d.Head, d.HeadWorktree = result.Git.Head, result.Git.Worktree
	}
	// Rounds count per automation and job: the delivery continues from the
	// reopens this automation already sent into the job, whichever delivery
	// sent them and whether or not this end is one of their follow-ups.
	rounds, err := s.ReopenRounds(a.Account, result.JobID, a.ID)
	if err != nil {
		return Delivery{}, false, err
	}
	d.Round = rounds.Total
	d.RoundBase = min(roundBase, d.Round)
	for id, base := range stepBase {
		if base = min(base, rounds.Step(id)); base > 0 {
			if d.StepRoundBase == nil {
				d.StepRoundBase = map[string]int{}
			}
			d.StepRoundBase[id] = base
		}
	}
	for _, step := range a.Steps {
		d.Steps = append(d.Steps, StepRecord{ID: step.ID, Type: step.Type, State: StatePending})
	}
	if reopened && strings.HasPrefix(result.Error, "blocked: ") {
		d.State, d.BlockedReason = StateBlocked, result.Error
		for i := range d.Steps {
			d.Steps[i].State, d.Steps[i].Reason = StateSkipped, result.Error
		}
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
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	batch := s.db.NewBatch()
	defer batch.Close()
	if err := batch.Set([]byte(deliveryKey(d.Account, d.ID)), data, nil); err != nil {
		return err
	}
	pendingKey := []byte(keyPendingItemsPrefix + hexKey(d.Account) + "/" + d.ID)
	pending := false
	for _, step := range d.Steps {
		for _, item := range step.Items {
			if itemPending(item) {
				pending = true
			}
		}
	}
	if pending {
		err = batch.Set(pendingKey, []byte(d.ID), nil)
	} else {
		err = batch.Delete(pendingKey, nil)
	}
	if err != nil {
		return err
	}
	if Terminal(d.State) {
		err = batch.Delete([]byte(keyActivePrefix+d.ID), nil)
	} else {
		account, marshalErr := json.Marshal(d.Account)
		if marshalErr != nil {
			return marshalErr
		}
		err = batch.Set([]byte(keyActivePrefix+d.ID), account, nil)
	}
	if err != nil {
		return err
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return err
	}
	s.notifyChange(d.Account, d.ProjectID(), d.JobID)
	return nil
}

// ProjectID is the project of the delivery's job: its result's, or the one
// a task, media or design job id names; "" for a job outside a project.
func (d Delivery) ProjectID() string {
	if d.Result.ProjectID != "" {
		return d.Result.ProjectID
	}
	parts := strings.Split(d.JobID, "/")
	if len(parts) == 3 && (parts[0] == KindTask || parts[0] == KindMedia || parts[0] == KindDesign) {
		return parts[1]
	}
	return ""
}

// SaveDelivery writes a delivery's new state.
func (s *Store) SaveDelivery(d Delivery) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Item receipts are independently advanced by the queue runner. Preserve
	// its newer receipts when the delivery runner saves subsequent steps.
	if current, err := s.GetDelivery(d.Account, d.ID); err == nil {
		for i := range d.Steps {
			if i < len(current.Steps) && current.Steps[i].Items != nil {
				d.Steps[i].Items = current.Steps[i].Items
			}
		}
	} else if !errors.Is(err, ErrNotFound) {
		return err
	}
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
	// JobID is a job id (task/<project>/<task>, worker/<worker>/<run>, ...)
	// or a bare task id, which matches the task's job ids
	// (TaskJobIDMatches).
	JobID        string
	AutomationID string
	State        string
	Limit        int
}

// ListDeliveries lists an account's deliveries, newest first.
func (s *Store) ListDeliveries(account string, filter DeliveryFilter) ([]Delivery, error) {
	if filter.JobID != "" && strings.Contains(filter.JobID, "/") {
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

// taskJobKinds are the job id kinds whose last part is a project task id:
// task/<project>/<task>, design/<project>/<task> and media/<project>/<task>.
var taskJobKinds = map[string]bool{"task": true, "design": true, "media": true}

// TaskJobIDMatches reports whether jobID is one of the job ids of the bare
// task id taskID (<kind>/<project>/<taskID> for a task job kind).
func TaskJobIDMatches(jobID, taskID string) bool {
	if taskID == "" || strings.Contains(taskID, "/") {
		return false
	}
	parts := strings.Split(jobID, "/")
	return len(parts) == 3 && taskJobKinds[parts[0]] && parts[1] != "" && parts[2] == taskID
}

func filterDeliveries(in []Delivery, filter DeliveryFilter) []Delivery {
	out := in[:0]
	bare := filter.JobID != "" && !strings.Contains(filter.JobID, "/")
	for _, d := range in {
		if bare && !TaskJobIDMatches(d.JobID, filter.JobID) {
			continue
		}
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
// a step that succeeded is never run again, but its failed dependency items
// are requeued when their prerequisites are now ready. It returns the delivery and
// whether there was anything to re-run. This is the explicit re-run of one
// named delivery (POST /v3/job-deliveries/<id>/rerun), whatever its head.
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
	return s.rerunLocked(d)
}

// RerunOutcome is what RerunNewest did with one delivery of the job: re-ran
// it, or left it unchanged for Reason.
type RerunOutcome struct {
	Delivery Delivery
	Rerun    bool
	Reason   string
}

// RerunNewest is swarm_deliver_job automation=true: for each automation with
// deliveries for the job it re-runs (as Rerun does) only that automation's
// newest delivery, and only when the delivery's head is still the job's
// current head. jobHead is the job's current head as read from the job;
// when "" (unknown, or a job without commits) the head of the job's newest
// delivery stands for it. Older deliveries are never re-run here.
func (s *Store) RerunNewest(account, jobID, jobHead string) ([]RerunOutcome, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	list, err := s.JobDeliveries(account, jobID) // newest first
	if err != nil {
		return nil, err
	}
	if jobHead == "" && len(list) > 0 {
		jobHead = list[0].TriggerHead()
	}
	out := []RerunOutcome{}
	newest := map[string]bool{}
	for _, d := range list {
		if newest[d.AutomationID] {
			out = append(out, RerunOutcome{Delivery: d, Reason: ReasonSuperseded})
			continue
		}
		newest[d.AutomationID] = true
		if head := d.TriggerHead(); head != "" && jobHead != "" && head != jobHead {
			out = append(out, RerunOutcome{Delivery: d, Reason: ReasonStaleHead})
			continue
		}
		updated, rerun, err := s.rerunLocked(d)
		if err != nil {
			return nil, err
		}
		outcome := RerunOutcome{Delivery: updated, Rerun: rerun}
		if !rerun {
			outcome.Reason = ReasonNothingToRerun
		}
		out = append(out, outcome)
	}
	return out, nil
}

func (s *Store) rerunLocked(d Delivery) (Delivery, bool, error) {
	if !Terminal(d.State) {
		return d, false, nil
	}
	// Item queues outlive their successful make step. Recover only items
	// that never dispatched and failed specifically on a now-ready dependency.
	itemsReset := false
	dependency := TriggerRunner{Store: s}
	for i := range d.Steps {
		step := &d.Steps[i]
		if i >= len(d.Definition) || d.Definition[i].ForEach == nil {
			continue
		}
		for j := range step.Items {
			item := &step.Items[j]
			if item.State != "failed" || item.JobID != "" || item.After == "" || !strings.HasPrefix(item.Error, "waits for "+item.After+", which did not finish: ") {
				continue
			}
			if ready, _ := dependency.itemDependency(d.Account, step.Items, item.After); ready {
				item.State, item.Error = "waiting for "+item.After, ""
				itemsReset = true
			}
		}
	}
	reset := false
	if d.State == StateBlocked {
		// A blocked review loop runs again from the top with a new round
		// budget; a run step that already succeeded is never run again.
		for i := range d.Steps {
			if step := &d.Steps[i]; step.Type != StepRun || step.State != StateSucceeded {
				resetStep(step)
				reset = true
			}
		}
		// The new budget starts from the reopens the job had in total.
		rounds, err := s.ReopenRounds(d.Account, d.JobID, d.AutomationID)
		if err != nil {
			return Delivery{}, false, err
		}
		d.Round = max(d.Round, rounds.Total)
		d.RoundBase, d.BlockedReason = d.Round, ""
		d.StepRoundBase = reopenBases(d.Definition, rounds)
		if d.Result.Git != nil {
			d.Head, d.HeadWorktree = d.Result.Git.Head, d.Result.Git.Worktree
		}
	}
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
		resetStep(step)
	}
	if !reset && !itemsReset {
		return d, false, nil
	}
	if reset {
		d.State, d.FinishedAt, d.SentBack = StatePending, 0, nil
	}
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

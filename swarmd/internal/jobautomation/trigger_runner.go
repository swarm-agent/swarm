package jobautomation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

const maxTriggerItems = 2000

// TriggerReceipt is the public, value-free history of an external item.
type TriggerReceipt struct {
	ID         string         `json:"item_id"`
	ReceivedAt int64          `json:"received_at"`
	State      string         `json:"state"`
	JobID      string         `json:"job_id,omitempty"`
	Error      string         `json:"error,omitempty"`
	Item       map[string]any `json:"item,omitempty"`
}
type scheduleDispatch struct {
	Automation Automation `json:"automation"`
	Brief      string     `json:"brief"`
	Key        string     `json:"key"`
	ReceiptID  string     `json:"receipt_id"`
}

type triggerState struct {
	Pending     *scheduleDispatch `json:"pending_schedule,omitempty"`
	ActiveJob   string            `json:"active_job,omitempty"`
	Fingerprint string            `json:"fingerprint"`
	Baseline    bool              `json:"baseline"`
	Seen        []string          `json:"seen"`
	Events      []TriggerReceipt  `json:"events"`
	NextPoll    int64             `json:"next_poll"`
	Failures    int               `json:"failures"`
}

func triggerKey(a Automation) string {
	return "job_automation/trigger/" + hexKey(a.Account) + "/" + a.ID
}
func (s *Store) triggerState(a Automation) (triggerState, error) {
	var state triggerState
	_, err := s.db.GetJSON(triggerKey(a), &state)
	if err != nil {
		return state, err
	}
	data, err := json.Marshal(a.On)
	if err != nil {
		return state, err
	}
	hash := sha256.Sum256(data)
	fingerprint := hex.EncodeToString(hash[:])
	if state.Fingerprint != fingerprint {
		previous := state
		state = triggerState{Fingerprint: fingerprint, ActiveJob: previous.ActiveJob, Pending: previous.Pending}
		// Retire queued events from the old definition. A reserved schedule
		// dispatch alone survives edits until its idempotent start is resolved.
		for _, receipt := range previous.Events {
			if previous.Pending != nil && receipt.ID == previous.Pending.ReceiptID {
				state.Events = append(state.Events, receipt)
			}
		}
	}
	return state, nil
}

// TriggerEvents omits event payloads from read models.
func (s *Store) TriggerEvents(a Automation) ([]TriggerReceipt, error) {
	var err error
	a, err = a.RenderInputs()
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.triggerState(a)
	out := append([]TriggerReceipt{}, state.Events...)
	for i := range out {
		out[i].Item = nil
	}
	return out, err
}
func decodeTriggerItems(data []byte, event TriggerEvent) ([]map[string]any, error) {
	var body any
	if err := json.Unmarshal(data, &body); err != nil {
		return nil, errors.New("invalid event JSON")
	}
	if event.Source == "webhook" {
		if obj, ok := body.(map[string]any); ok {
			if items, exists := obj["items"]; exists {
				body = items
			} else {
				body = []any{obj}
			}
		}
	} else if event.Items != "" {
		body = eventField(body, event.Items)
	}
	items, ok := body.([]any)
	if !ok || len(items) > maxTriggerItems {
		return nil, errors.New("event feed must be an array of at most 2000 items")
	}
	out := make([]map[string]any, 0, len(items))
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("event item must be an object")
		}
		id := eventField(item, event.ID)
		switch id.(type) {
		case string, float64:
		default:
			return nil, errors.New("event id must be a string or number")
		}
		if text := eventText(id); text == "" || len(text) > 512 {
			return nil, errors.New("event id must be 1 to 512 bytes")
		}
		out = append(out, item)
	}
	return out, nil
}

// Receive durably deduplicates a complete batch, or changes nothing on rejection.
func (s *Store) Receive(a Automation, data []byte) error {
	var renderErr error
	a, renderErr = a.RenderInputs()
	if renderErr != nil {
		return renderErr
	}
	items, err := decodeTriggerItems(data, a.On.Event)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(a.Account, a.ID)
	if err != nil {
		return err
	}
	if current.Revision != a.Revision || current.Proposed || !current.Enabled || current.On == nil {
		return errors.New("trigger is no longer active")
	}
	state, err := s.triggerState(a)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, id := range state.Seen {
		seen[id] = true
	}
	for _, e := range state.Events {
		seen[e.ID] = true
	}
	baseline := a.On.Event.Source == "pull" && !state.Baseline
	for _, item := range items {
		id := eventText(eventField(item, a.On.Event.ID))
		if seen[id] {
			continue
		}
		seen[id] = true
		state.Seen = append(state.Seen, id)
		if !baseline {
			state.Events = append(state.Events, TriggerReceipt{ID: id, ReceivedAt: s.now().UnixMilli(), State: "queued", Item: item})
		}
	}
	if len(state.Seen) > maxTriggerItems {
		state.Seen = state.Seen[len(state.Seen)-maxTriggerItems:]
	}
	for len(state.Events) > maxTriggerItems {
		if state.Events[0].State == "queued" {
			return errors.New("trigger queue is full")
		}
		state.Events = state.Events[1:]
	}
	state.Baseline = true
	return s.db.PutJSON(triggerKey(a), state)
}

// TriggerRunner serializes external starts, using the canonical execution boundary.
// Start must be idempotent for its key, including recovery after a process exit.
type TriggerRunner struct {
	scheduleSeen map[string]string
	ItemOutcome  func(context.Context, Automation, string) (string, error)
	Store        *Store
	Account      func() string
	Start        func(context.Context, Automation, string, string) (string, error)
	Running      func(context.Context, Automation, string) (bool, error)
	Secret       func(Automation) (string, error)
	Signal       func(Automation, string, string)
	Client       *http.Client
}

func (r *TriggerRunner) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		r.Tick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (r *TriggerRunner) Tick(ctx context.Context) {
	if r.Store == nil || r.Account == nil {
		return
	}
	list, err := r.Store.List(r.Account())
	if err != nil {
		return
	}
	stop, err := r.Store.StopState()
	if err != nil || stop.Stopped {
		return
	}
	r.tickItems(ctx)
	for _, a := range list {
		if a.Enabled && !a.Proposed && a.On != nil {
			r.tick(ctx, a)
		}
	}
}
func (r *TriggerRunner) tick(ctx context.Context, a Automation) {
	var renderErr error
	a, renderErr = a.RenderInputs()
	if renderErr != nil {
		return
	}
	if a.On.Schedule != nil {
		r.tickSchedule(ctx, a)
		return
	}
	s := r.Store
	s.mu.Lock()
	state, err := s.triggerState(a)
	s.mu.Unlock()
	if err != nil {
		return
	}
	if a.On.Event.Source == "pull" && state.NextPoll <= s.now().UnixMilli() {
		err = r.poll(ctx, a)
		s.mu.Lock()
		state, loadErr := s.triggerState(a)
		if loadErr == nil {
			delay := time.Duration(a.On.Event.EverySeconds) * time.Second
			failedSignal := err != nil && state.Failures == 0
			if err != nil {
				state.Failures++
				for i := 0; i < state.Failures && i < 7; i++ {
					delay *= 2
				}
				if delay > 24*time.Hour {
					delay = 24 * time.Hour
				}
			} else {
				state.Failures = 0
			}
			state.NextPoll = s.now().Add(delay).UnixMilli()
			saveErr := s.db.PutJSON(triggerKey(a), state)
			s.mu.Unlock()
			if failedSignal && saveErr == nil && r.Signal != nil {
				r.Signal(a, "workflow.trigger.failed", "pull failed")
			}
		} else {
			s.mu.Unlock()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.Get(a.Account, a.ID)
	if err != nil || current.Revision != a.Revision || !current.Enabled {
		return
	}
	state, err = s.triggerState(a)
	if err != nil {
		return
	}
	if state.Pending != nil && !r.finishScheduleDispatch(ctx, a, &state, true) {
		return
	}
	if state.ActiveJob != "" {
		running, err := r.Running(ctx, a, state.ActiveJob)
		if err != nil || running {
			return
		}
		state.ActiveJob = ""
	}
	for i := range state.Events {
		e := &state.Events[i]
		if e.State != "queued" {
			continue
		}
		digest := sha256.Sum256([]byte(a.ID + "/" + state.Fingerprint + "/" + e.ID))
		stateName, job, startError := r.advanceQueued(ctx, a, RenderEvent(a.On.Start.Brief, e.Item), hex.EncodeToString(digest[:]))
		var err error
		if stateName == "failed" {
			err = errors.New(startError)
		}
		kind := "workflow.trigger.fired"
		if err != nil {
			e.State = "failed"
			e.Error = startError
			kind = "workflow.trigger.failed"
		} else {
			e.State = "started"
			e.JobID = job
			state.ActiveJob = job
		}
		e.Item = nil
		if s.db.PutJSON(triggerKey(a), state) != nil {
			return
		}
		if r.Signal != nil {
			r.Signal(a, kind, e.ID)
		}
		if err == nil {
			return
		}
	}
}
func (r *TriggerRunner) poll(ctx context.Context, a Automation) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.On.Event.URL, nil)
	if err != nil {
		return errors.New("invalid pull URL")
	}
	if a.On.Event.Secret != "" {
		if r.Secret == nil {
			return errors.New("secret authority unavailable")
		}
		value, err := r.Secret(a)
		if err != nil {
			return errors.New("pull secret unavailable")
		}
		req.Header.Set("Authorization", "Bearer "+value)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := copyClient.Do(req)
	if err != nil {
		return errors.New("pull request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("pull returned HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return errors.New("pull body exceeds limit or could not be read")
	}
	return r.Store.Receive(a, data)
}

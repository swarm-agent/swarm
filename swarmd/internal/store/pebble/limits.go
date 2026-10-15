package pebblestore

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
)

// Limits is the account's box-local admission policy. Zero job budgets are unset.
type Limits struct {
	Revision             uint64  `json:"revision"`
	WorkerJobConcurrency int     `json:"worker_job_concurrency"`
	JobTokens            int64   `json:"job_tokens"`
	JobCostUSD           float64 `json:"job_cost_usd"`
	DelegationWaves      int     `json:"delegation_waves"`
	ActiveChildren       int     `json:"active_children"`
	SwarmChildren        int     `json:"swarm_children"`
}

func DefaultLimits() Limits {
	return Limits{WorkerJobConcurrency: 100, DelegationWaves: 5, ActiveChildren: 5, SwarmChildren: 5}
}

var ErrLimitsConflict = errors.New("limits revision conflict")

// LimitReached is a machine-readable refusal, also preserved by text-only callers.
type LimitReached struct {
	Type  string `json:"type"`
	Limit string `json:"limit"`
	Value any    `json:"value"`
}

func (e *LimitReached) Error() string { return fmt.Sprintf("limit.reached: %s=%v", e.Limit, e.Value) }
func (l Limits) Validate() error {
	if l.WorkerJobConcurrency < 1 || l.WorkerJobConcurrency > 10000 || l.DelegationWaves < 0 || l.DelegationWaves > 256 || l.ActiveChildren < 1 || l.ActiveChildren > 256 || l.SwarmChildren < 1 || l.SwarmChildren > 256 || l.JobTokens < 0 || l.JobTokens > 1000000000 || math.IsNaN(l.JobCostUSD) || math.IsInf(l.JobCostUSD, 0) || l.JobCostUSD < 0 || l.JobCostUSD > 1000000 {
		return errors.New("limits exceed hard bounds: concurrency 1..10000, waves 0..256, children 1..256, job tokens 0..1000000000, job USD 0..1000000")
	}
	return nil
}
func (s *Store) GetLimits(account string) (Limits, error) {
	l := DefaultLimits()
	if strings.TrimSpace(account) == "" {
		return l, errors.New("limits account required")
	}
	found, err := s.GetJSON("limits/"+keyPart(account), &l)
	if err != nil || found {
		return l, err
	}
	// Preserve existing account delegation settings until first limits write.
	raw, ok, err := NewPermissionStore(s).GetPolicyForAccount(account)
	if err != nil {
		return l, err
	}
	if ok {
		var policy struct {
			Subagents *struct {
				Waves    int `json:"automatic_launches_per_parent_run"`
				Children int `json:"active_child_limit"`
				Swarm    int `json:"swarm_active_child_limit"`
			} `json:"subagents"`
		}
		if err := json.Unmarshal(raw, &policy); err != nil {
			return l, err
		}
		if p := policy.Subagents; p != nil {
			l.DelegationWaves = p.Waves
			if p.Children > 0 {
				l.ActiveChildren = p.Children
			}
			if p.Swarm > 0 {
				l.SwarmChildren = p.Swarm
			}
		}
	}
	return l, nil
}
func (s *Store) SetLimits(account string, expected uint64, l Limits) (Limits, error) {
	if err := l.Validate(); err != nil {
		return Limits{}, err
	}
	s.workersMu.Lock()
	defer s.workersMu.Unlock()
	unlock := s.sessionMutations.lockSessions("account:" + account)
	defer unlock()
	current, err := s.GetLimits(account)
	if err != nil {
		return current, err
	}
	if current.Revision != expected || expected == ^uint64(0) {
		return current, ErrLimitsConflict
	}
	l.Revision = expected + 1
	return l, s.PutJSON("limits/"+keyPart(account), l)
}
func (s *SessionStore) GetLimits(account string) (Limits, error) { return s.store.GetLimits(account) }
func (s *SessionStore) SetLimits(account string, expected uint64, l Limits) (Limits, error) {
	return s.store.SetLimits(account, expected, l)
}
func (s *PermissionStore) GetLimits(account string) (Limits, error) {
	return s.store.GetLimits(account)
}

func (s *SessionStore) checkJobLimits(account string, scopes []UsageScopeTotal) error {
	l, err := s.GetLimits(account)
	if err != nil {
		return err
	}
	for _, scope := range scopes {
		if scope.Kind != "worker_run" {
			continue
		}
		total, _, err := s.GetUsageScope(account, scope.Kind, scope.ProjectID, scope.ID)
		if err != nil {
			return err
		}
		if l.JobTokens > 0 && total.TotalTokens >= l.JobTokens {
			return s.store.limitReached(account, "job_tokens", l.JobTokens)
		}
		if l.JobCostUSD > 0 && (total.UnknownReceipts > 0 || total.CatalogCostUSD+total.ProviderCostUSD+total.ProviderEstimateCostUSD >= l.JobCostUSD) {
			return s.store.limitReached(account, "job_cost_usd", l.JobCostUSD)
		}
	}
	return nil
}

// SetLimitSignalStore is installed at daemon startup before admissions begin.
func (s *Store) SetLimitSignalStore(signals *SignalStore) { s.limitSignals = signals }
func (s *Store) limitReached(account, name string, value any) error {
	e := &LimitReached{"limit.reached", name, value}
	if s.limitSignals != nil {
		if _, err := s.limitSignals.Append(Signal{Kind: e.Type, Account: account, Severity: SignalSeverityWarning, Summary: e.Error(), Attrs: map[string]string{"limit": name, "value": fmt.Sprint(value)}}); err != nil {
			return errors.Join(e, err)
		}
	}
	return e
}

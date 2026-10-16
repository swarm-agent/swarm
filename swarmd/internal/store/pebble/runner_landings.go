package pebblestore

import (
	"encoding/json"
	"errors"
	"github.com/cockroachdb/pebble"
	"os"
	"strings"
	"sync"
	"time"
)

type RunnerLanding struct {
	SourceClonePath string            `json:"source_clone_path,omitempty"`
	PayloadFormat   string            `json:"payload_format"`
	ArchiveSHA256   string            `json:"archive_sha256"`
	ArchiveBytes    int64             `json:"archive_bytes"`
	DeliveryReason  string            `json:"delivery_reason,omitempty"`
	ID              string            `json:"id"`
	Account         string            `json:"-"`
	BaseImage       string            `json:"base_image"`
	Setup           []string          `json:"setup"`
	Env             map[string]string `json:"env"`
	Checks          []string          `json:"checks"`
	CheckTimeout    int               `json:"check_timeout_seconds"`
	BaseCommit      string            `json:"base_commit"`
	HeadCommit      string            `json:"head_commit"`
	State           string            `json:"state"`
	Runner          string            `json:"runner"`
	LeaseID         string            `json:"lease_id"`
	LeaseUntil      int64             `json:"lease_until"`
	Attempts        []string          `json:"attempts"`
	Output          string            `json:"output"`
	OutputBytes     int64             `json:"output_bytes"`
	ExitCodes       []int             `json:"exit_codes,omitempty"`
	DurationMS      int64             `json:"duration_ms,omitempty"`
}

type RunnerStatus struct {
	Name     string `json:"name"`
	State    string `json:"state"`
	Reason   string `json:"reason"`
	Docker   bool   `json:"docker"`
	GVisor   bool   `json:"gvisor"`
	LastSeen int64  `json:"last_seen"`
}

type runnerState struct {
	Account  string                  `json:"account"`
	Landings []RunnerLanding         `json:"landings"`
	Statuses map[string]RunnerStatus `json:"statuses"`
}

var runnerStateMu sync.Mutex

// Mutations are serialized through a synced durable account record, including
// lease expiry and reassignment; no in-memory lease is authoritative.
func (s *Store) mutateRunners(account string, fn func(*runnerState) error) error {
	runnerStateMu.Lock()
	defer runnerStateMu.Unlock()
	if account == "" {
		return errors.New("runner account required")
	}
	key := "runners:v1:" + runnerHash(account)
	st := runnerState{Account: account, Statuses: map[string]RunnerStatus{}}
	if _, err := s.GetJSON(key, &st); err != nil {
		return err
	}
	before, err := json.Marshal(st.Landings)
	if err != nil {
		return err
	}
	previous := make(map[string]string, len(st.Landings))
	for _, l := range st.Landings {
		previous[l.ID] = l.State
	}
	if err := fn(&st); err != nil {
		return err
	}
	after, err := json.Marshal(st.Landings)
	if err != nil {
		return err
	}
	if string(before) != string(after) {
		defer s.wakeRunnerLandings()
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	if err := s.db.Set([]byte(key), data, pebble.Sync); err != nil {
		return err
	}
	for _, l := range st.Landings {
		if l.State == "environment_failed" && previous[l.ID] == "claimed" && len(l.Attempts) >= 2 {
			if _, err := NewSignalStore(s).Append(Signal{Kind: "job.environment_failed", Severity: "warning", Source: "runner", Account: account, Summary: l.DeliveryReason, Refs: map[string]string{"landing": l.ID, "runners": strings.Join(l.Attempts, ", ")}}); err != nil {
				return err
			}
		}
		if l.State == "succeeded" || l.State == "failed" || l.State == "environment_failed" {
			if err := os.Remove(s.runnerPayloadPath(account, l.ID)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	return nil
}

// CreateRunnerLanding is provider-only. Callers must supply a sanitized folder
// environment; it is intentionally not exposed as an HTTP mutation.
func (s *Store) CreateRunnerLanding(account string, l RunnerLanding) error {
	if l.ID == "" || l.BaseCommit == "" || l.HeadCommit == "" || len(l.Checks) == 0 || l.CheckTimeout <= 0 {
		return errors.New("incomplete runner landing")
	}
	// Conservatively exclude all account slot names: source clones need not be
	// the granted folder itself, and no secret capability belongs on a runner.
	slots, err := NewSecretSlotStore(s).ListSlots(account)
	if err != nil {
		return err
	}
	for key, value := range l.Env {
		if strings.Contains(key, "swarm-secret://") || strings.Contains(value, "swarm-secret://") {
			return errors.New("runner environment must not contain secret stand-ins")
		}
		for _, slot := range slots {
			if strings.Contains(key, slot.Name) || strings.Contains(value, slot.Name) {
				return errors.New("runner environment must not contain secret names")
			}
		}
	}
	var creationErr error
	err = s.mutateRunners(account, func(st *runnerState) error {
		for _, old := range st.Landings {
			if old.ID == l.ID {
				return errors.New("landing already exists")
			}
		}
		l.State = "pending"
		if err := s.buildRunnerPayload(account, &l); err != nil {
			creationErr = err
			l.State = "environment_failed"
			l.DeliveryReason = err.Error()
		}
		l.Runner = ""
		l.LeaseID = ""
		l.LeaseUntil = 0
		l.Attempts = nil
		l.Output = ""
		l.OutputBytes = 0
		l.ExitCodes = nil
		l.DurationMS = 0
		st.Landings = append(st.Landings, l)
		return nil
	})
	if err != nil {
		return err
	}
	if creationErr == nil {
		s.wakeRunnerLandings()
	}
	return creationErr
}

func expireRunnerLeases(st *runnerState, now int64) {
	for i := range st.Landings {
		l := &st.Landings[i]
		if l.State == "claimed" && now >= l.LeaseUntil {
			l.State = "lease_expired"
			if len(l.Attempts) >= 2 {
				l.State = "environment_failed"
				l.DeliveryReason = "environment failed on runners " + strings.Join(l.Attempts, ", ")
			}
			l.LeaseID = ""
			l.LeaseUntil = 0
		}
	}
}

func (s *Store) ClaimRunnerLanding(account, runner string, now time.Time) (*RunnerLanding, error) {
	var result *RunnerLanding
	err := s.mutateRunners(account, func(st *runnerState) error {
		expireRunnerLeases(st, now.UnixMilli())
		status := st.Statuses[runner]
		status.Name = runner
		status.LastSeen = now.UnixMilli()
		if status.State == "" {
			status.State = "idle"
		}
		st.Statuses[runner] = status
		if status.State == "unhealthy" {
			return nil
		}
		for _, l := range st.Landings {
			if l.State == "claimed" && l.Runner == runner {
				return errors.New("runner already has a landing")
			}
		}
		for i := range st.Landings {
			l := &st.Landings[i]
			if l.State != "pending" && l.State != "lease_expired" {
				continue
			}
			id, err := GenerateToken(16)
			if err != nil {
				return err
			}
			l.State = "claimed"
			status.State = "busy"
			st.Statuses[runner] = status
			l.Runner = runner
			l.LeaseID = id
			l.LeaseUntil = now.Add(time.Minute).UnixMilli()
			l.Attempts = append(l.Attempts, runner)
			copy := *l
			result = &copy
			break
		}
		return nil
	})
	return result, err
}

func (s *Store) UpdateRunnerLanding(account, runner, id, lease, action, output string, codes []int, duration int64, now time.Time) error {
	rejected := false
	err := s.mutateRunners(account, func(st *runnerState) error {
		expireRunnerLeases(st, now.UnixMilli())
		for i := range st.Landings {
			l := &st.Landings[i]
			if l.ID != id {
				continue
			}
			if l.State != "claimed" || l.Runner != runner || l.LeaseID != lease || lease == "" {
				rejected = true
				return nil
			}
			status := st.Statuses[runner]
			status.Name = runner
			status.LastSeen = now.UnixMilli()
			status.State = "busy"
			switch action {
			case "environment_failed":
				l.State = "environment_failed"
				l.DeliveryReason = output
				l.LeaseID = ""
				l.LeaseUntil = 0
				status.State = "unhealthy"
			case "heartbeat":
				l.LeaseUntil = now.Add(time.Minute).UnixMilli()
			case "output":
				if len(output) > 64<<10 {
					return errors.New("output chunk exceeds 64 KiB")
				}
				l.OutputBytes += int64(len(output))
				l.Output += output
				if len(l.Output) > 2<<20 {
					l.Output = l.Output[len(l.Output)-(2<<20):]
				}
			case "result":
				if len(codes) != len(l.Checks) || duration < 0 || len(output) > 64<<10 {
					return errors.New("invalid check result")
				}
				l.ExitCodes = codes
				l.DurationMS = duration
				l.State = "succeeded"
				status.State = "idle"
				for _, code := range codes {
					if code != 0 {
						l.State = "failed"
					}
				}
				l.LeaseID = ""
				l.LeaseUntil = 0
			default:
				return errors.New("unknown runner action")
			}
			st.Statuses[runner] = status
			return nil
		}
		rejected = true
		return nil
	})
	if err == nil && rejected {
		return errors.New("landing lease not owned or expired")
	}
	return err
}

func (s *Store) SetRunnerStatus(account string, status RunnerStatus) error {
	if status.State != "idle" && status.State != "busy" && status.State != "unhealthy" {
		return errors.New("invalid runner status")
	}
	if len(status.Reason) > 1024 {
		return errors.New("runner reason too long")
	}
	return s.mutateRunners(account, func(st *runnerState) error { st.Statuses[status.Name] = status; return nil })
}
func (s *Store) RunnerStatuses(account string) ([]RunnerStatus, error) {
	result := []RunnerStatus{}
	err := s.mutateRunners(account, func(st *runnerState) error {
		for _, status := range st.Statuses {
			result = append(result, status)
		}
		return nil
	})
	return result, err
}

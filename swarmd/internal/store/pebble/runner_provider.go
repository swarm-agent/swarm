package pebblestore

import (
	"errors"
	"os"
)

// RunnerLandingSnapshot reads the durable account-scoped request.
func (s *Store) RunnerLandingSnapshot(account, id string) (*RunnerLanding, error) {
	runnerStateMu.Lock()
	defer runnerStateMu.Unlock()
	var st runnerState
	if _, err := s.GetJSON("runners:v1:"+runnerHash(account), &st); err != nil {
		return nil, err
	}
	for _, l := range st.Landings {
		if l.ID == id {
			return &l, nil
		}
	}
	return nil, errors.New("runner landing not found")
}

// FinishRunnerLanding fences outstanding claims before removing their payload.
func (s *Store) FinishRunnerLanding(account, id string) error {
	err := s.mutateRunners(account, func(st *runnerState) error {
		for i := range st.Landings {
			l := &st.Landings[i]
			if l.ID != id {
				continue
			}
			l.State = "done"
			l.LeaseID = ""
			l.LeaseUntil = 0
			return nil
		}
		return errors.New("runner landing not found")
	})
	if err != nil {
		return err
	}
	err = os.Remove(s.runnerPayloadPath(account, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

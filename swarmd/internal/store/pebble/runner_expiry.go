package pebblestore

import (
	"context"
	"encoding/json"
	"github.com/cockroachdb/pebble"
	"time"
)

// ExpireRunnerLeases uses a caller-supplied clock, including on daemon ticks.
// Claims remain durable; the wake channel merely accelerates waiting readers.
func (s *Store) ExpireRunnerLeases(now time.Time) error {
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: []byte("runners:v1:"), UpperBound: []byte("runners:v1;")})
	if err != nil {
		return err
	}
	var accounts []string
	for iter.First(); iter.Valid(); iter.Next() {
		var st runnerState
		if err := json.Unmarshal(iter.Value(), &st); err != nil {
			iter.Close()
			return err
		}
		if st.Account != "" {
			accounts = append(accounts, st.Account)
		}
	}
	err = iter.Error()
	iter.Close()
	if err != nil {
		return err
	}
	for _, account := range accounts {
		changed := false
		if err := s.mutateRunners(account, func(st *runnerState) error {
			for _, l := range st.Landings {
				if l.State == "claimed" && now.UnixMilli() >= l.LeaseUntil {
					changed = true
				}
			}
			expireRunnerLeases(st, now.UnixMilli())
			return nil
		}); err != nil {
			return err
		}
		if changed {
			s.wakeRunnerLandings()
		}
	}
	return nil
}

// RunRunnerLeaseExpiry stops before the caller closes the store.
func (s *Store) RunRunnerLeaseExpiry(ctx context.Context, report func(error)) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if err := s.ExpireRunnerLeases(now); err != nil && report != nil {
				report(err)
			}
		}
	}
}

func (s *Store) StartRunnerLeaseExpiry(report func(error)) {
	s.runnerExpiryOnce.Do(func() {
		ctx, cancel := context.WithCancel(context.Background())
		s.runnerExpiryCancel = cancel
		s.runnerExpiryDone = make(chan struct{})
		go func() { defer close(s.runnerExpiryDone); s.RunRunnerLeaseExpiry(ctx, report) }()
	})
}

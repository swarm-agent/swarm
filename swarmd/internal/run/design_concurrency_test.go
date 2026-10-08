package run

import (
	"context"
	"fmt"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	store "swarm/packages/swarmd/internal/store/pebble"
)

// Purpose: designDispatchLoop must use account Swarm capacity, not a two-worker
// ceiling. Real Pebble acceptance/allocation and permission admission prove that
// independent requests stay queued, drain without replacement, and stop safely.
// The blocking adapter is a hermetic contract fixture, not an AI benchmark.
func TestDesignDispatcherPolicyConcurrency(t *testing.T) {
	for _, limit := range []int{1, 2, 4} {
		t.Run(fmt.Sprint(limit), func(t *testing.T) {
			s, p, first, runner := designExecutionFixture(t)
			policy := permission.DefaultSubagentPolicy()
			policy.SwarmActiveChildLimit = limit
			if _, err := s.permissions.UpdateSubagentPolicyForAccount(p.AccountID, policy); err != nil {
				t.Fatal(err)
			}
			if _, err := s.permissions.UpdateActiveExecutionLimitForAccount(p.AccountID, 8); err != nil {
				t.Fatal(err)
			}
			requests := []store.DesignRequest{first}
			for i := 1; i < 5; i++ {
				id := fmt.Sprintf("independent-%d", i)
				requests = append(requests, acceptDesignFixture(t, s, p, first, id, []store.DesignCandidateSpec{{ArtifactID: id, Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: id}}, nil))
			}
			started := make(chan string, 5)
			release := make(chan struct{})
			runner.call = func(ctx context.Context, req provideriface.Request) (provideriface.Response, error) {
				started <- req.SessionID
				select {
				case <-release:
					return provideriface.Response{Text: "<!doctype html><html><body>kept</body></html>"}, nil
				case <-ctx.Done():
					return provideriface.Response{}, ctx.Err()
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			d := s.StartDesignDispatcher(ctx)
			defer d.Close()
			for i := 0; i < limit; i++ {
				select {
				case <-started:
				case <-ctx.Done():
					t.Fatal("policy-permitted independent request did not start")
				}
			}
			running, queued := 0, 0
			for _, request := range requests {
				got, err := s.sessions.DesignStore().GetDesignRequest(p, request.ID)
				if err != nil {
					t.Fatal(err)
				}
				switch got.Candidates[0].State {
				case store.DesignRunning:
					running++
				case store.DesignQueued:
					queued++
					if len(got.Candidates[0].Attempts) != 0 {
						t.Fatal("queued request allocated early")
					}
				default:
					t.Fatalf("request lost: %+v", got)
				}
			}
			if running != limit || queued != 5-limit {
				t.Fatalf("running=%d queued=%d limit=%d", running, queued, limit)
			}
			close(release)
			capacityBoundaryAwait(t, func() bool {
				for _, request := range requests {
					got, err := s.sessions.DesignStore().GetDesignRequest(p, request.ID)
					if err != nil || got.State != store.DesignSucceeded || len(got.Candidates[0].Attempts) != 1 || got.Candidates[0].Attempts[0].Result == nil {
						return false
					}
				}
				return true
			})
			d.Close()
			if len(runner.requests) != 5 {
				t.Fatalf("provider submissions=%d", len(runner.requests))
			}
		})
	}
}

// Purpose: cancellation of more than two workers must not deadlock completion
// sends while Close waits. Restart must release admission and never replay the
// interrupted attempts. This exercises the real dispatcher with blocked adapters.
func TestDesignDispatcherConcurrentShutdown(t *testing.T) {
	s, p, first, runner := designExecutionFixture(t)
	if _, err := s.permissions.UpdateActiveExecutionLimitForAccount(p.AccountID, 8); err != nil {
		t.Fatal(err)
	}
	for i := 1; i < 4; i++ {
		id := fmt.Sprintf("shutdown-%d", i)
		acceptDesignFixture(t, s, p, first, id, []store.DesignCandidateSpec{{ArtifactID: id, Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: id}}, nil)
	}
	started := make(chan struct{}, 4)
	runner.call = func(ctx context.Context, _ provideriface.Request) (provideriface.Response, error) {
		started <- struct{}{}
		<-ctx.Done()
		return provideriface.Response{}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d := s.StartDesignDispatcher(ctx)
	for i := 0; i < 4; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			t.Fatal("workers did not start")
		}
	}
	closed := make(chan struct{})
	go func() { d.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		t.Fatal("shutdown deadlocked")
	}
	runner.call = func(context.Context, provideriface.Request) (provideriface.Response, error) {
		return provideriface.Response{Text: "<!doctype html><html></html>"}, nil
	}
	probe := acceptDesignFixture(t, s, p, first, "shutdown-probe", []store.DesignCandidateSpec{{ArtifactID: "shutdown-probe", Kind: store.DesignHTML, Operation: store.DesignGenerate, Brief: "probe"}}, nil)
	d = s.StartDesignDispatcher(ctx)
	defer d.Close()
	capacityBoundaryAwait(t, func() bool {
		got, err := s.sessions.DesignStore().GetDesignRequest(p, probe.ID)
		return err == nil && got.State == store.DesignSucceeded
	})
	d.Close()
	if len(runner.requests) != 5 {
		t.Fatal("restart replayed interrupted work")
	}
}

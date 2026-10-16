package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"swarm/packages/swarmd/internal/environment"
	"swarm/packages/swarmd/internal/publish"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"testing"
	"time"
)

// Purpose: the startup provider selection boundary must accept the configured
// runner modes rather than preventing daemon startup. This narrow API test
// exercises SetEnvironmentProvider directly without starting a daemon.
func TestRunnerEnvironmentSelection(t *testing.T) {
	for _, name := range []string{"local", "runners", "auto"} {
		s := &Server{}
		if err := s.SetEnvironmentProvider(name); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Purpose: exercise the real hub handlers, archive creation and provider result
// mapping with two checks; missing claims and infrastructure rejection must not
// become SendBackError. A temporary store and in-process runner avoid Docker.
func TestRunnerEnvironmentResultsAndFallback(t *testing.T) {
	for _, mode := range []string{"results", "payload_failure", "no_runner", "auto_no_runner"} {
		t.Run(mode, func(t *testing.T) {
			db, err := pebblestore.Open(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			source, head := runnerSource(t)
			s := &Server{runnerStore: db}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			spec := environment.Spec{Account: "account", Checkout: source, Commit: head, BaseCommit: head, Checks: []string{"first", "second"}, CheckTimeout: time.Minute}
			var p environment.Provider = environment.Runners{Store: db, Wait: 50 * time.Millisecond}
			local := &recordingLandProvider{}
			if mode == "auto_no_runner" {
				p = environment.Auto{Remote: environment.Runners{Store: db, Wait: 50 * time.Millisecond}, Local: landExecutorProvider{local.Exec}}
			}
			done := make(chan error, 1)
			if mode == "results" || mode == "payload_failure" {
				go func() {
					rec := &pebblestore.ScopedTokenRecord{AccountScopeID: "account", Name: "test-runner"}
					w := httptest.NewRecorder()
					s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/claim", nil).WithContext(ctx), rec)
					var response struct {
						Landing *pebblestore.RunnerLanding `json:"landing"`
					}
					if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
						done <- err
						return
					}
					if response.Landing == nil {
						done <- errors.New("missing claim")
						return
					}
					l := response.Landing
					for _, action := range []string{"output", "result"} {
						body := map[string]any{"lease_id": l.LeaseID, "output": "streamed output", "exit_codes": []int{0, 7}}
						if mode == "payload_failure" && action == "result" {
							body["state"] = "environment_failed"
							body["exit_codes"] = nil
						}
						data, _ := json.Marshal(body)
						w = httptest.NewRecorder()
						s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/landings/"+l.ID+"/"+action, strings.NewReader(string(data))).WithContext(ctx), rec)
						if w.Code != 200 {
							done <- errors.New(w.Body.String())
							return
						}
					}
					done <- nil
				}()
			}
			env, err := p.Acquire(ctx, spec)
			if mode == "no_runner" {
				var failure *environment.Failure
				if !errors.As(err, &failure) || !strings.Contains(err.Error(), "no runner claim") {
					t.Fatalf("expected clear infrastructure failure: %v", err)
				}
				return
			}
			if err != nil {
				if mode == "payload_failure" {
					var failure *environment.Failure
					if !errors.As(err, &failure) {
						t.Fatal(err)
					}
					if err := <-done; err != nil {
						t.Fatal(err)
					}
					return
				}
				t.Fatal(err)
			}
			defer env.Release()
			var output strings.Builder
			for i, command := range spec.Checks {
				result, err := env.Exec(ctx, environment.ExecRequest{Command: []string{"bash", "-lc", command}, OnOutput: func(b []byte) { output.Write(b) }})
				if mode == "payload_failure" {
					var failure *environment.Failure
					var back *publish.SendBackError
					if !errors.As(err, &failure) || errors.As(err, &back) {
						t.Fatalf("wrong failure: %v", err)
					}
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				want := 0
				if mode == "results" && i == 1 {
					want = 7
				}
				if result.ExitCode != want {
					t.Fatalf("exit=%d want=%d", result.ExitCode, want)
				}
			}
			if mode == "results" && !strings.Contains(output.String(), "streamed output") {
				t.Fatal("lost streamed output")
			}
			if mode == "auto_no_runner" && local.executed != 2 {
				t.Fatal("local fallback did not execute both checks")
			}
			if mode == "results" || mode == "payload_failure" {
				if err := <-done; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

// Purpose: two durable lease expiries must trigger one local retry under Auto;
// the publish queue must persist environment_failed without invoking SendBack.
// Real store transitions with an injected clock are the narrowest deterministic
// layer for lease expiry, independent of wall-clock minute-long leases.
func TestRunnerEnvironmentExpiryAndQueue(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, head := runnerSource(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	spec := environment.Spec{Account: "account", Checkout: source, Commit: head, BaseCommit: head, Checks: []string{"first", "second"}, CheckTimeout: time.Minute}
	local := &recordingLandProvider{}
	p := environment.Auto{Remote: environment.Runners{Store: db, Wait: time.Second}, Local: landExecutorProvider{local.Exec}}
	claimed := make(chan *pebblestore.RunnerLanding, 1)
	go func() {
		s := &Server{runnerStore: db}
		w := httptest.NewRecorder()
		s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/claim", nil).WithContext(ctx), &pebblestore.ScopedTokenRecord{AccountScopeID: "account", Name: "first"})
		var response struct {
			Landing *pebblestore.RunnerLanding `json:"landing"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &response)
		claimed <- response.Landing
	}()
	env, err := p.Acquire(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	defer env.Release()
	l := <-claimed
	if l == nil {
		t.Fatal("no claim")
	}
	now := time.UnixMilli(l.LeaseUntil + 1)
	if err := db.ExpireRunnerLeases(now); err != nil {
		t.Fatal(err)
	}
	second, err := db.ClaimRunnerLanding("account", "second", now)
	if err != nil || second == nil {
		t.Fatalf("second claim: %v", err)
	}
	if err := db.ExpireRunnerLeases(time.UnixMilli(second.LeaseUntil + 1)); err != nil {
		t.Fatal(err)
	}
	for _, command := range spec.Checks {
		result, err := env.Exec(ctx, environment.ExecRequest{Command: []string{"bash", "-lc", command}, OnOutput: func([]byte) {}})
		if err != nil || result.ExitCode != 0 {
			t.Fatalf("local retry: %v", err)
		}
	}
	if local.executed != 2 {
		t.Fatal("missing local checks")
	}
	q := publish.NewQueue(db)
	_, err = q.Enqueue(publish.Entry{Account: "account", Request: publish.Request{JobID: "job", Folder: source}})
	if err != nil {
		t.Fatal(err)
	}
	err = q.Process(ctx, "account", source, publish.QueueHooks{Prepare: func(context.Context, *publish.Entry) error { return &environment.Failure{Reason: "runner unavailable"} }, SendBack: func(context.Context, publish.Entry) error { t.Error("infrastructure sent to coder"); return nil }})
	if err != nil {
		t.Fatal(err)
	}
	entry, _, err := q.Get("account", "job")
	if err != nil || entry.State != "environment_failed" || entry.SendBacks != 0 {
		t.Fatalf("bad terminal record: %+v %v", entry, err)
	}
}

// Purpose: the landing orchestration must consume remote results through its
// unchanged check loop. Real hub handlers execute a two-check request while a
// bounded Git executor isolates preparation from Docker and forge services.
func TestRunnerEnvironmentLanding(t *testing.T) {
	db, err := pebblestore.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	source, head := runnerSource(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s := &Server{runnerStore: db}
	done := make(chan error, 1)
	go func() {
		rec := &pebblestore.ScopedTokenRecord{AccountScopeID: "account", Name: "landing-runner"}
		w := httptest.NewRecorder()
		s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/claim", nil).WithContext(ctx), rec)
		var response struct {
			Landing *pebblestore.RunnerLanding `json:"landing"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			done <- err
			return
		}
		l := response.Landing
		if l == nil || len(l.Checks) != 2 {
			done <- errors.New("missing two-check landing")
			return
		}
		for _, action := range []string{"output", "result"} {
			data, _ := json.Marshal(map[string]any{"lease_id": l.LeaseID, "output": "landing output", "exit_codes": []int{0, 0}})
			w = httptest.NewRecorder()
			s.handleRunnerWork(w, httptest.NewRequest("POST", "/v3/runners/landings/"+l.ID+"/"+action, strings.NewReader(string(data))).WithContext(ctx), rec)
			if w.Code != 200 {
				done <- errors.New(w.Body.String())
				return
			}
		}
		done <- nil
	}()
	entry := &publish.Entry{Account: "account", Request: publish.Request{Branch: "job"}}
	git := func(_ context.Context, r environment.ExecRequest) (*environment.ExecResult, error) {
		out := ""
		if r.Command[1] == "branch" {
			out = "job"
		}
		if r.Command[1] == "rev-parse" {
			out = head
		}
		return &environment.ExecResult{Stdout: out}, nil
	}
	err = prepareLandWorktreeWithExecutors(ctx, publish.Rule{}, entry, source, []string{"first", "second"}, 1, git, environment.Runners{Store: db, Wait: time.Second}, environment.Spec{Checkout: source}, func(context.Context, publish.Rule, string) (publish.Base, error) {
		return publish.Base{Commit: head, Branch: "dev"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(entry.Checks) != 2 || entry.Checks[0].Output != "landing output" || entry.Checks[0].ExitCode != 0 || entry.Checks[1].ExitCode != 0 || entry.Environment != "runner:landing-runner" {
		t.Fatalf("wrong landing results: %+v", entry)
	}
}

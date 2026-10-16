package api

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"swarm/packages/swarmd/internal/environment"
	"testing"
	"time"

	"swarm/packages/swarmd/internal/environments/provider"
	"swarm/packages/swarmd/internal/executioncapacity"
	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/publish"
	"swarm/packages/swarmd/internal/sandbox"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type landPrincipalCapacity struct {
	permissionService
	manager *executioncapacity.Manager
}

func (p landPrincipalCapacity) ExecutionCapacity() *executioncapacity.Manager { return p.manager }

// The engine is fake, but sandbox admission and its cache-limit callback are real.
// Stop at command execution: this regression must not run Docker or forge commands.
type landPrincipalEngine struct {
	executed bool
	ctx      context.Context
}

func (r *landPrincipalEngine) Run(_ context.Context, _ string, args ...string) ([]byte, error) {
	command := strings.Join(args, " ")
	switch {
	case strings.HasPrefix(command, "image inspect"):
		return []byte("sha256:fixture"), nil
	case strings.HasPrefix(command, "info --format"):
		return []byte(`{"runc":{}}`), nil
	case strings.Contains(command, "{{json .State}}"):
		return []byte(`{"Running":true,"StartedAt":"fixture"}`), nil
	}
	return nil, nil
}
func (r *landPrincipalEngine) RunCombined(ctx context.Context, name string, args ...string) ([]byte, error) {
	return r.Run(ctx, name, args...)
}
func (r *landPrincipalEngine) RunWithIO(ctx context.Context, _ io.Reader, _, _ io.Writer, _ string, _ ...string) error {
	r.executed = true
	r.ctx = ctx
	return errors.New("fixture command boundary reached")
}

// Purpose: AutoPublisher.processLand must propagate the daemon owner's trusted
// identity through Queue.Process and prepareLand into sandbox cache admission,
// regardless of a missing or different triggering principal. Real queue/session
// persistence and sandbox admission are the narrowest boundary reproducing the
// lost context; fixture HTTP reads avoid unrelated project/Git setup. No command
// may execute when the principal callback rejects admission.
func TestPublishLandDaemonPrincipal(t *testing.T) {
	for _, trigger := range []string{"automatic", "other-principal"} {
		t.Run(trigger, func(t *testing.T) {
			s, _, _, cleanup := setupScopedAuthTestServer(t)
			defer cleanup()
			actor, err := s.identitySessions.ActorForCurrentSelection()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			if trigger == "other-principal" {
				ctx = identity.ContextWithPrincipal(ctx, identity.Principal{Type: identity.PrincipalTypeUser, UserID: "other-user", AccountScopeID: "other-account"})
			}
			db := s.sessions.Store().Underlying()
			const sessionID = "landing-principal-session"
			if err := s.sessions.Store().CreateSessionForAccount(store.SessionSnapshot{ID: sessionID}, actor.UserID, actor.AccountScopeID); err != nil {
				t.Fatal(err)
			}
			s.signals = store.NewSignalStore(db)
			s.perm = landPrincipalCapacity{manager: executioncapacity.NewManager(executioncapacity.ManagerConfig{})}
			folder := t.TempDir()
			s.publishConfig = "fixture"
			s.loadPublishConfig = func(string) ([]publish.Rule, error) {
				return []publish.Rule{{Folder: folder, Mode: "land", Auto: true}}, nil
			}
			s.sandboxEnvironments = store.NewSandboxEnvironmentStore(db)
			// Only read-model HTTP responses are replaced. Identity construction,
			// durable activity, queue processing and sandbox execution remain production.
			s.controlMCPRoutesOnce.Do(func() {})
			s.controlMCPRoutes = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case strings.HasPrefix(r.URL.Path, "/v3/workers/"):
					writeJSON(w, 200, map[string]any{"run": map[string]any{"status": "succeeded", "session_id": sessionID}})
				case strings.HasPrefix(r.URL.Path, "/v3/sessions/"):
					writeJSON(w, 200, map[string]any{"session": map[string]any{"metadata": map[string]any{"project_id": "project", "task_id": "task"}}})
				case strings.HasPrefix(r.URL.Path, "/v3/projects/"):
					writeJSON(w, 200, map[string]any{"task": map[string]any{"status": "needs_review", "workspace_path": folder}})
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
					http.NotFound(w, r)
				}
			})
			engine := &landPrincipalEngine{}
			reads := 0
			m, err := sandbox.NewManager(ctx, sandbox.Config{Mode: sandbox.ModeRequired, Memory: "4g", MaxWorkerRuns: 2, UID: 10001, GID: 10001, StateDir: t.TempDir(), ProjectCacheLimit: func(ctx context.Context) (uint64, error) {
				reads++
				principal, ok := identity.PrincipalFromContext(ctx)
				if !ok {
					return 0, identity.ErrPrincipalRequired
				}
				if principal.UserID != actor.UserID || principal.AccountScopeID != actor.AccountScopeID {
					return 0, errors.New("landing used triggering principal instead of daemon owner")
				}
				return sandbox.DefaultProjectCacheBytes, nil
			}}, engine)
			if err != nil {
				t.Fatal(err)
			}
			old := sandbox.Default()
			sandbox.SetDefault(m)
			defer sandbox.SetDefault(old)
			q, err := s.landingQueue()
			if err != nil {
				t.Fatal(err)
			}
			job := "worker/coder/run"
			if _, err := q.Enqueue(publish.Entry{Account: actor.AccountScopeID, Request: publish.Request{JobID: job, Folder: folder, Branch: "agent/job"}}); err != nil {
				t.Fatal(err)
			}
			p := s.NewAutoPublisher(nil, db)
			if err := p.landPending(ctx); err != nil {
				t.Fatal(err)
			}
			entry, _, err := q.Get(actor.AccountScopeID, job)
			if err != nil {
				t.Fatal(err)
			}
			if reads == 0 || !engine.executed || !strings.Contains(entry.Reason, "fixture command boundary reached") {
				t.Fatalf("landing did not cross trusted sandbox admission: reads=%d executed=%v state=%s reason=%s", reads, engine.executed, entry.State, entry.Reason)
			}
			// Continue the preparation/check boundary with the identity observed at
			// execution. Stub Git/update here: the forge command is root-owned in
			// production and must not be installed or bypassed by this test.
			checkReached := false
			err = prepareLandWorktreeWithExecutors(engine.ctx, publish.Rule{}, &entry, folder, []string{"fixture check"}, 1,
				func(_ context.Context, req sandbox.ExecRequest) (*provider.ExecResult, error) {
					out := ""
					switch strings.Join(req.Command, " ") {
					case "git branch --show-current":
						out = entry.Request.Branch
					case "git rev-parse HEAD":
						out = strings.Repeat("a", 40)
					case "git status --porcelain", "git rebase " + strings.Repeat("b", 40):
					default:
						t.Fatalf("unexpected preparation command %v", req.Command)
					}
					return &provider.ExecResult{Stdout: out}, nil
				}, landExecutorProvider{func(ctx context.Context, _ sandbox.ExecRequest) (*provider.ExecResult, error) {
					principal, ok := identity.PrincipalFromContext(ctx)
					if !ok || principal.UserID != actor.UserID || principal.AccountScopeID != actor.AccountScopeID {
						return nil, identity.ErrPrincipalRequired
					}
					checkReached = true
					return &provider.ExecResult{ExitCode: 1}, nil
				}}, environment.Spec{}, func(context.Context, publish.Rule, string) (publish.Base, error) {
					return publish.Base{Commit: strings.Repeat("b", 40), Branch: "dev"}, nil
				})
			var sendBack *publish.SendBackError
			if !checkReached || !errors.As(err, &sendBack) || len(entry.Checks) != 1 || entry.Checks[0].ExitCode != 1 {
				t.Fatalf("check failure did not reach send-back: checks=%+v error=%v", entry.Checks, err)
			}
		})
	}
}

package api

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"swarm/packages/swarmd/internal/identity"
	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/signals"
	store "swarm/packages/swarmd/internal/store/pebble"
)

type workflowTriggerSecrets interface {
	GetSecretValueForAccount(string, string) ([]byte, bool, error)
	PutWorkflowTriggerKey(string, string, []byte) error
	GetWorkflowTriggerKey(string, string) ([]byte, bool, error)
	DeleteWorkflowTriggerKey(string, string) error
}

func (s *Server) triggerSecrets() (workflowTriggerSecrets, error) {
	values, ok := s.secretValues.(workflowTriggerSecrets)
	if !ok {
		return nil, errors.New("trigger secret storage unavailable")
	}
	return values, nil
}
func (s *Server) validateWorkflowTrigger(p identity.Principal, a jobautomation.Automation) error {
	if a.On == nil {
		return nil
	}
	refuse := jobautomation.InvalidTrigger
	if s.sessions == nil || s.sessions.Store() == nil {
		return refuse("worker and project authorities unavailable")
	}
	worker, found, err := s.sessions.Store().WorkerStore().GetWorker(p.AccountScopeID, a.On.Start.Worker)
	if err != nil {
		return err
	}
	if !found || worker.LifecycleState != store.WorkerLifecycleStateActive {
		return refuse("on.start.worker must be a deployed, active worker")
	}
	if err := validateTriggerWorkerRole(worker.Role); err != nil {
		return err
	}
	projects, err := s.sessions.Store().ListProjects(p.AccountScopeID, 1000)
	if err != nil {
		return err
	}
	if len(projects) >= 1000 {
		return refuse("too many projects to resolve trigger folder safely")
	}
	matches := 0
	for _, project := range projects {
		for _, workspace := range project.Workspaces {
			if filepath.Clean(workspace.Path) == a.Scope.Folder {
				matches++
				break
			}
		}
	}
	if matches != 1 {
		return refuse("scope.folder must be registered in exactly one project")
	}
	if _, err := s.resolveWorkerJobWorkspace(p, "", a.Scope.Folder); err != nil {
		return refuse(err.Error())
	}
	if a.On.Event.Secret != "" {
		if _, err := s.workflowPullSecret(a); err != nil {
			return refuse(err.Error())
		}
	}
	if a.On.Event.Source == "webhook" {
		_, err := s.triggerSecrets()
		return err
	}
	return nil
}
func validateTriggerWorkerRole(role string) error {
	if role == store.WorkerRoleReviewer {
		return jobautomation.InvalidTrigger("a trigger starts the work; review it with a reviewer step")
	}
	if role == "designer" || role == "media" {
		return jobautomation.InvalidTrigger("media and design jobs are not trigger starts")
	}
	return nil
}
func (s *Server) workflowPullSecret(a jobautomation.Automation) (string, error) {
	if s.secretSlots == nil {
		return "", errors.New("secret authority unavailable")
	}
	name := a.On.Event.Secret
	slot, ok, err := s.secretSlots.GetSlot(a.Account, name)
	if err != nil {
		return "", err
	}
	if !ok || !slot.HasValue {
		return "", errors.New("pull secret unavailable")
	}
	u, err := url.Parse(a.On.Event.URL)
	if err != nil {
		return "", errors.New("invalid pull URL")
	}
	allowed := false
	for _, host := range slot.Hosts {
		if strings.EqualFold(host, u.Hostname()) {
			allowed = true
		}
	}
	if !allowed {
		return "", errors.New("pull URL host is not allowed by the secret")
	}
	grants, err := s.secretSlots.ListGrants(a.Account, name)
	if err != nil {
		return "", err
	}
	allowed = false
	for _, grant := range grants {
		if grant.Account == a.Account && grant.WorkspacePath == a.Scope.Folder && grant.ExpiresAt > time.Now().UnixMilli() {
			allowed = true
		}
	}
	if !allowed {
		return "", errors.New("pull secret is not granted to scope.folder")
	}
	values, err := s.triggerSecrets()
	if err != nil {
		return "", err
	}
	value, ok, err := values.GetSecretValueForAccount(a.Account, name)
	if err != nil || !ok {
		return "", errors.New("pull secret unavailable")
	}
	return string(value), nil
}

// RunWorkflowTriggers is the daemon's single external-event poller and queue runner.
func (s *Server) RunWorkflowTriggers(ctx context.Context) {
	emit := signals.NewEmitter(s.signals).Emit
	repository, err := s.jobAutomationStore()
	if err != nil {
		return
	}
	runner := jobautomation.TriggerRunner{Store: repository, Account: s.OutboxAccount, Secret: s.workflowPullSecret,
		Signal: func(a jobautomation.Automation, kind, item string) {
			if emit != nil {
				emit(store.Signal{Kind: kind, Account: a.Account, Severity: store.SignalSeverityInfo, Summary: kind, Refs: map[string]string{"workflow_id": a.ID}})
			}
		},
		Start: func(ctx context.Context, a jobautomation.Automation, brief, key string) (string, error) {
			p := identity.Principal{Type: identity.PrincipalTypeUser, AccountScopeID: a.Account, UserID: a.TriggerOwner, AccountScopeSource: identity.AccountScopeSourceServerState}
			if err := s.validateWorkflowTrigger(p, a); err != nil {
				return "", err
			}
			workspace, err := s.resolveWorkerJobWorkspace(p, "", a.Scope.Folder)
			if err != nil {
				return "", err
			}
			execution, err := s.workerExecutionService()
			if err != nil {
				return "", err
			}
			run, err := execution.Dispatch(ctx, a.Account, a.TriggerOwner, store.WorkerRunAdmission{WorkerID: a.On.Start.Worker, WorkflowTriggerID: a.ID, RequestSource: "workflow_trigger", WorkspaceID: workspace, Input: map[string]any{"prompt": brief}, IdempotencyKey: key})
			if err != nil {
				return "", err
			}
			return controlMCPJobID(controlMCPJobWorker, a.On.Start.Worker, run.ID), nil
		}, Running: func(ctx context.Context, a jobautomation.Automation, job string) (bool, error) {
			parts := strings.Split(job, "/")
			if len(parts) != 3 {
				return false, errors.New("invalid trigger job")
			}
			run, ok, err := s.sessions.Store().WorkerStore().GetWorkerRun(a.Account, parts[1], parts[2])
			if err != nil || !ok {
				return false, err
			}
			switch run.Status {
			case "succeeded", "failed", "cancelled":
				return false, nil
			}
			return true, nil
		}}
	runner.Run(ctx)
}
func (s *Server) saveTriggerKey(a jobautomation.Automation, rotate bool) (string, error) {
	if a.On == nil || a.On.Event.Source != "webhook" || a.Proposed {
		return "", nil
	}
	values, err := s.triggerSecrets()
	if err != nil {
		return "", err
	}
	_, exists, err := values.GetWorkflowTriggerKey(a.Account, a.ID)
	if err != nil {
		return "", err
	}
	if exists && !rotate {
		return "", nil
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return "", err
	}
	// The response encodes the 32-byte signing key as hex; senders decode it.
	text := hex.EncodeToString(key)
	if err := values.PutWorkflowTriggerKey(a.Account, a.ID, key); err != nil {
		return "", err
	}
	return text, nil
}

var triggerRate = struct {
	sync.Mutex
	at map[string]time.Time
}{at: map[string]time.Time{}}

// serveWorkflowEvent recognizes only the exact event path. It authenticates the
// raw body itself and never passes a signature-only request into another route.
func (s *Server) serveWorkflowEvent(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, JobAutomationsPath+"/"), "/")
	if !strings.HasPrefix(r.URL.Path, JobAutomationsPath+"/") || len(parts) != 2 || parts[1] != "events" || !controlMCPSafeID.MatchString(parts[0]) {
		return false
	}
	repository, err := s.jobAutomationStore()
	if err != nil {
		writeError(w, 503, err)
		return true
	}
	a, err := repository.Get(s.OutboxAccount(), parts[0])
	if err != nil || a.On == nil || a.On.Event.Source != "webhook" || !a.Enabled || a.Proposed {
		writeError(w, 401, errors.New("invalid trigger authentication"))
		return true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		writeError(w, 413, errors.New("event body exceeds 64 KiB"))
		return true
	}
	values, err := s.triggerSecrets()
	if err != nil {
		writeError(w, 503, err)
		return true
	}
	key, ok, err := values.GetWorkflowTriggerKey(a.Account, a.ID)
	signature := strings.TrimPrefix(r.Header.Get("X-Swarm-Trigger-Signature"), "sha256=")
	signed, decodeErr := hex.DecodeString(signature)
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	if err != nil || !ok || decodeErr != nil || !strings.HasPrefix(r.Header.Get("X-Swarm-Trigger-Signature"), "sha256=") || !hmac.Equal(signed, mac.Sum(nil)) {
		writeError(w, 401, errors.New("invalid trigger signature"))
		return true
	}
	triggerRate.Lock()
	now := time.Now()
	rateKey := a.Account + "/" + a.ID
	limited := now.Sub(triggerRate.at[rateKey]) < time.Second
	if !limited {
		if len(triggerRate.at) > 1000 {
			for k, v := range triggerRate.at {
				if now.Sub(v) > time.Minute {
					delete(triggerRate.at, k)
				}
			}
		}
		triggerRate.at[rateKey] = now
	}
	triggerRate.Unlock()
	if limited {
		writeError(w, 429, errors.New("trigger rate limit exceeded"))
		return true
	}
	if err := repository.Receive(a, data); err != nil {
		writeError(w, 400, err)
		return true
	}
	writeJSON(w, 202, map[string]any{"ok": true})
	return true
}

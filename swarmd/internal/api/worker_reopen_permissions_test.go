package api

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"swarm/packages/swarmd/internal/jobautomation"
	"swarm/packages/swarmd/internal/permission"
	provideriface "swarm/packages/swarmd/internal/provider/interfaces"
	runruntime "swarm/packages/swarmd/internal/run"
	sessionruntime "swarm/packages/swarmd/internal/session"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
	"swarm/packages/swarmd/internal/tool"
)

// reopenedWorkerRun reopens a recorded worker job through a workflow
// send-back step (jobAutomationReopener.Reopen, the real routes) and returns
// the V3 provider tool invoker the executor builds for that follow-up run on
// the claudecode provider, with the run's cancel attached as the executor
// attaches it. The daemon-wide bypass is off and the sandbox gate is ready, the
// forge's state: an interactive prompt in this run is refused as
// worker_noninteractive.
func reopenedWorkerRun(t *testing.T, f reopenFixture, requestID string) (provideriface.ToolInvoker, context.Context, sessionV3ExecutorJob, *sessionV3Executor) {
	t.Helper()
	p := testPrincipal()
	jobID := f.job(t, "run-"+requestID, "succeeded", f.session.ID)
	followup, err := f.server.JobAutomationReopener().Reopen(context.Background(), jobautomation.ReopenRequest{
		Account: p.AccountScopeID, JobID: jobID, RequestID: "jdel_" + requestID + ".fix",
		Message: "The reviewer blocked your work.\n\n" + jobautomation.ReopenClosing,
	})
	if err != nil || followup == "" {
		t.Fatalf("reopen: run %q err %v", followup, err)
	}
	workerReopenPermissions(t, f).SetBypassGate(func() (bool, string) { return true, "" })
	workerReopenPermissions(t, f).SetBypassPermissions(false)

	executor := &sessionV3Executor{server: f.server}
	base, err := f.server.agents.ResolveSystemAgent("system-worker-coder", pebblestore.AgentProfile{})
	if err != nil {
		t.Fatal(err)
	}
	profile, err := executor.resolveSessionV3CurrentAgentToolContract(p.AccountScopeID, nil, base)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := executor.resolveSessionV3ProviderTools(p.AccountScopeID, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(sessionV3OfferedToolNames(definitions), "bash") {
		t.Fatal("the worker coder's contract does not offer bash")
	}
	session, ok, err := f.server.sessions.GetSession(f.session.ID)
	if err != nil || !ok {
		t.Fatalf("session %v %v", ok, err)
	}
	job := sessionV3ExecutorJob{Principal: p, SessionID: session.ID, RunID: followup}
	resolved := sessionV3ResolvedRuntime{
		Session:      session,
		AgentProfile: profile,
		Preference:   pebblestore.ModelPreference{Provider: "claudecode", Model: "fixture-model", Thinking: "medium"},
		Scope:        tool.WorkspaceScope{PrimaryPath: session.WorkspacePath, Roots: []string{session.WorkspacePath}},
	}
	runCtx, cancel := context.WithCancelCause(context.Background())
	t.Cleanup(func() { cancel(nil) })
	executor.attachCancel(job, cancel)
	invoker, err := executor.newSessionV3ProviderToolInvoker(resolved, job, 1, nil, nil, definitions)
	if err != nil {
		t.Fatal(err)
	}
	return invoker, runCtx, job, executor
}

const reopenBashArguments = `{"command":"printf built > built.txt","category":"write","critical":false,"explanation":["Write a build marker in the job's workspace."]}`

// Requirement: a worker job reopened by a workflow send-back step runs bash
// under the same rules as its first run and an owner follow-up: the worker's
// own tool contract grants it, without the daemon-wide bypass and without an
// interactive prompt. An explicit account deny rule still wins.
// Threat (forge, 2026-10-27, claudecode coder): with bypass off, the reopened
// run's bash fell through to an interactive prompt, was refused as
// worker_noninteractive and the coder stopped blocked without building or
// testing.
// Owners: jobAutomationReopener.Reopen, sessionV3Executor.
// newSessionV3ProviderToolInvoker, run.Service.WorkerToolPolicyOverlay and
// permission.Service.AuthorizeToolCall over a real store and the real bash
// tool. This is the narrowest layer that holds the reopen, the executor's
// invoker and the permission gate together; the model provider is not reached.
func TestWorkflowReopenedWorkerRunsBashFromItsOwnContract(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	invoker, runCtx, job, _ := reopenedWorkerRun(t, f, "reopen-bash")

	// Control: without the worker's grants this exact call is the forge's
	// refusal, so the approval below comes from the contract.
	without, err := workerReopenPermissions(t, f).AuthorizeToolCall(permission.AuthorizationInput{SessionID: job.SessionID, AccountScopeID: p.AccountScopeID, RunID: job.RunID, CallID: "control", Mode: f.session.Mode, ToolName: "bash", ToolArguments: reopenBashArguments})
	if err != nil || without.Decision != permission.AuthorizationDeny || without.Source != permission.WorkerNonInteractiveSource {
		t.Fatalf("control without worker grants = %+v err %v, want the worker_noninteractive refusal", without, err)
	}

	result, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "reopen-bash", Name: "bash", Arguments: reopenBashArguments})
	if err != nil {
		t.Fatalf("bash in the reopened run: %v", err)
	}
	if strings.Contains(result.Error+result.Output, "cannot request interactive permission") {
		t.Fatalf("bash was refused as an unattended prompt: %+v", result)
	}
	if got, readErr := os.ReadFile(filepath.Join(f.session.WorkspacePath, "built.txt")); readErr != nil || string(got) != "built" {
		t.Fatalf("bash did not run in the job's workspace: %q %v (result %+v)", got, readErr, result)
	}
	if runCtx.Err() != nil {
		t.Fatalf("an approved call ended the run: %v", context.Cause(runCtx))
	}
	if count, err := workerReopenPermissions(t, f).PendingCount(job.SessionID); err != nil || count != 0 {
		t.Fatalf("pending permissions %d err %v", count, err)
	}

	// An explicit account deny rule still wins over the worker's grant, as an
	// ordinary denial that neither prompts nor ends the run.
	if _, err := workerReopenPermissions(t, f).UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Tool: "bash", Decision: permission.PolicyDecisionDeny}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.session.WorkspacePath, "built.txt")); err != nil {
		t.Fatal(err)
	}
	denied, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "reopen-bash-denied", Name: "bash", Arguments: reopenBashArguments})
	if err != nil || !strings.Contains(denied.Error, "denied by") {
		t.Fatalf("account deny rule: result %+v err %v", denied, err)
	}
	if _, statErr := os.Stat(filepath.Join(f.session.WorkspacePath, "built.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a denied bash call ran: %v", statErr)
	}
	if runCtx.Err() != nil {
		t.Fatalf("an explicit deny ended the run: %v", context.Cause(runCtx))
	}
}

// Requirement: when an unattended worker run's call would need an interactive
// approval (here an account ask rule for bash, which outranks the worker's
// grant), the run ends failed at once with the refusal as its reason. The
// call's result is still recorded and nothing executes or waits.
// Threat: the refusal was handed to the model as tool output; the reopened
// coder reported itself blocked and waited for a person who cannot answer.
// Owners: sessionV3UnattendedDenialInvoker, sessionV3Executor.failRunWithCause,
// run.Service.executeProviderManagedToolCall and gateToolCalls, and the V3
// terminal classifier that records the run's outcome.
func TestUnattendedWorkerRunFailsFastOnInteractiveDenial(t *testing.T) {
	f := newReopenFixture(t)
	p := testPrincipal()
	if _, err := workerReopenPermissions(t, f).UpsertRuleForAccount(p.AccountScopeID, permission.PolicyRule{Kind: permission.PolicyRuleKindTool, Tool: "bash", Decision: permission.PolicyDecisionAsk}); err != nil {
		t.Fatal(err)
	}
	invoker, runCtx, job, executor := reopenedWorkerRun(t, f, "reopen-ask")

	_, err := invoker.ExecuteTool(context.Background(), provideriface.ToolInvocation{CallID: "reopen-ask-bash", Name: "bash", Arguments: reopenBashArguments})
	var denied *runruntime.UnattendedPermissionDeniedError
	if !errors.As(err, &denied) || !strings.Contains(denied.Error(), "cannot request interactive permission") {
		t.Fatalf("err = %v, want the unattended refusal", err)
	}
	if runCtx.Err() == nil {
		t.Fatal("the run was not ended")
	}
	var cause *sessionV3RunCancelledError
	if !errors.As(context.Cause(runCtx), &cause) || cause.By != sessionV3CancelledByUnattendedDenial || cause.Reason != denied.Error() {
		t.Fatalf("run cause = %v", context.Cause(runCtx))
	}
	if executor.isRunCanceled(job) {
		t.Fatal("the run was marked stopped by a person instead of failed")
	}
	outcome := sessionV3TerminalClassifier.Classify(TerminalClassifierInput{Err: executor.nameRunCancellation(runCtx, runCtx.Err())})
	if outcome.Status != sessionruntime.RunIntentFailed || !strings.Contains(outcome.Reason, "cannot request interactive permission") {
		t.Fatalf("run outcome = %+v, want failed with the refusal", outcome)
	}
	if _, statErr := os.Stat(filepath.Join(f.session.WorkspacePath, "built.txt")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("the refused bash call ran: %v", statErr)
	}
	if count, err := workerReopenPermissions(t, f).PendingCount(job.SessionID); err != nil || count != 0 {
		t.Fatalf("pending permissions %d err %v", count, err)
	}
	messages, err := f.server.sessions.ListMessages(job.SessionID, 0, 200)
	if err != nil {
		t.Fatal(err)
	}
	recorded := false
	for _, message := range messages {
		if strings.Contains(message.Content, "reopen-ask-bash") || strings.Contains(message.Content, "cannot request interactive permission") {
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("the refused call's result was not recorded in the session")
	}
}

func workerReopenPermissions(t *testing.T, f reopenFixture) *permission.Service {
	t.Helper()
	perm, ok := f.server.perm.(*permission.Service)
	if !ok || perm == nil {
		t.Fatalf("permission service %T", f.server.perm)
	}
	return perm
}

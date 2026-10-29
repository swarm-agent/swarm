package runtime

import (
	"strings"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// emitStartupSignals records that the daemon started and whether agent
// commands are confined, blocked by required isolation, or explicitly unconfined.
func emitStartupSignals(emitter *signals.Emitter, status sandbox.Status, headless bool) {
	emitter.Emit(pebblestore.Signal{
		Kind:     "daemon.started",
		Severity: pebblestore.SignalSeverityInfo,
		Summary:  "Swarm started; any previous update drain ended; command execution depends on sandbox readiness",
		DedupKey: "daemon.started",
	})
	if status.Active {
		runtimeName := status.Runtime
		if runtimeName == "" {
			runtimeName = "default"
		}
		emitter.Emit(pebblestore.Signal{
			Kind:     "sandbox.active",
			Severity: pebblestore.SignalSeverityInfo,
			Summary:  "Agent command isolation is active",
			DedupKey: "sandbox.state",
			Attrs:    map[string]string{"runtime": runtimeName, "mode": string(status.Mode)},
		})
		return
	}
	if status.Mode == sandbox.ModeRequired {
		emitter.Emit(pebblestore.Signal{
			Kind:     "sandbox.unavailable",
			Severity: pebblestore.SignalSeverityWarning,
			Summary:  "Agent commands blocked: required sandbox unavailable; inspect the sandbox readiness reason and repair the configured isolation backend, then restart",
			DedupKey: "sandbox.state",
			Attrs:    map[string]string{"reason": status.Reason, "mode": string(status.Mode)},
		})
		return
	}
	if headless {
		// Off is the headless default, not a fault.
		emitter.Emit(pebblestore.Signal{
			Kind:     "sandbox.inactive",
			Severity: pebblestore.SignalSeverityInfo,
			Summary:  "Agent sandbox is off: agents run on this machine",
			DedupKey: "sandbox.state",
			Attrs:    map[string]string{"reason": status.Reason, "mode": string(status.Mode)},
		})
		return
	}
	emitter.Emit(pebblestore.Signal{
		Kind:     "sandbox.inactive",
		Severity: pebblestore.SignalSeverityWarning,
		Summary:  "Agent sandbox is off: agent commands run on this machine and permission bypass is disabled",
		DedupKey: "sandbox.state",
		Attrs:    map[string]string{"reason": status.Reason, "mode": string(status.Mode)},
	})
}

// workerRunSignal raises worker.run.succeeded, worker.run.failed or
// worker.run.cancelled when a worker run finishes. It names the worker and
// run. Explicit blocker reasons are included in the failure summary. A job
// whose own run was stopped but stays open (pebblestore.WorkerRunStopped)
// raises worker.run.stopped instead: it has not finished, so nothing that
// reacts to a finished job (job automations) runs for it.
func workerRunSignal(emitter *signals.Emitter, sessions *pebblestore.SessionStore) func(pebblestore.WorkerRecord, pebblestore.WorkerRunRecord) {
	return func(worker pebblestore.WorkerRecord, run pebblestore.WorkerRunRecord) {
		if run.Cleanup != nil && run.Cleanup.FailureReported {
			kind, severity, summary := "job.cleanup_failed", pebblestore.SignalSeverityWarning, "Job finished but checkout cleanup repeatedly failed"
			if run.Cleanup.State == "done" {
				kind, severity, summary = "job.cleanup_recovered", pebblestore.SignalSeverityInfo, "Job checkout cleanup recovered"
			}
			emitter.Emit(pebblestore.Signal{Kind: kind, Severity: severity, Account: run.AccountScopeID, Summary: summary, DedupKey: kind + ":" + run.ID, Refs: map[string]string{"worker_id": worker.ID, "run_id": run.ID}, Attrs: map[string]string{"job_id": "worker/" + worker.ID + "/" + run.ID}})
			return
		}
		followup := run.FollowupRunID
		// Follow-ups keep the original run receipt's status. Emit the latest
		// outcome while retaining its follow-up identity for workflow rounds.
		switch run.FollowupStatus {
		case "completed":
			// Every follow-up has its own lifecycle outcome, even when the
			// original run succeeded. Only this exact execution can block it.
			run.Status, run.Error = "succeeded", ""
			snapshot, found, err := sessions.GetSession(run.SessionID)
			if err != nil || !found || snapshot.AccountScopeID != run.AccountScopeID || snapshot.UserID != run.UserID {
				run.Status, run.Error = "failed", "cannot verify follow-up session lifecycle"
			} else if followup != "" && snapshot.Metadata["lifecycle_signal"] == "blocked" && snapshot.Metadata["lifecycle_signal_run_id"] == followup {
				if reason, ok := snapshot.Metadata["blocker_reason"].(string); ok && strings.TrimSpace(reason) != "" {
					run.Status, run.Error = "failed", "blocked: "+reason
				}
			}
		case "interrupted", "failed":
			run.Status, run.Error = "failed", run.FollowupError
		}
		kind := "worker.run." + run.Status
		severity := pebblestore.SignalSeverityInfo
		if run.FollowupStatus == "" && pebblestore.WorkerRunStopped(run) {
			kind = "worker.run.stopped"
		} else if run.Status == "failed" {
			severity = pebblestore.SignalSeverityWarning
		}
		name := worker.Name
		if name == "" {
			name = worker.ID
		}
		refs := map[string]string{"worker_id": worker.ID, "run_id": run.ID}
		if run.SessionID != "" {
			refs["session_id"] = run.SessionID
		}
		if run.AutomationID != "" {
			refs["automation_id"] = run.AutomationID
		}
		if run.Status == "failed" && strings.HasPrefix(run.Error, "run stalled:") {
			emitter.Emit(pebblestore.Signal{Kind: "job.stalled", Severity: pebblestore.SignalSeverityWarning, Account: run.AccountScopeID, Summary: run.Error, DedupKey: "job.stalled:" + run.ID, Refs: refs, Attrs: map[string]string{"job_id": "worker/" + worker.ID + "/" + run.ID}})
		}
		if strings.Contains(run.Error, sandbox.ErrInterrupted.Error()) {
			emitter.Emit(pebblestore.Signal{Kind: "job.sandbox_interrupted", Severity: pebblestore.SignalSeverityWarning, Account: run.AccountScopeID, Summary: "Job sandbox interrupted (OOM or container restart); uncommitted work preserved; retry with a follow-up", Refs: refs, DedupKey: "job.sandbox:" + run.ID})
		}
		summary := "Worker " + name + " run " + run.Status
		if kind == "worker.run.stopped" {
			summary = "Worker " + name + " run stopped; the job stays open: a message continues it"
		} else if run.Status == "failed" && strings.HasPrefix(run.Error, "blocked: ") {
			summary += ": " + run.Error
		}
		emitter.Emit(pebblestore.Signal{
			Kind:     kind,
			Severity: severity,
			Account:  run.AccountScopeID,
			Summary:  summary,
			DedupKey: "worker.run:" + run.ID + ":" + followup,
			Refs:     refs,
			Attrs:    map[string]string{"source": run.RequestSource, "error": run.Error},
		})
	}
}

// startupSwarmName is this machine's configured name ("" when unset or
// unreadable), labelling forwarded signals.
func startupSwarmName(configPath string) string {
	path := strings.TrimSpace(configPath)
	if path == "" {
		resolved, err := startupconfig.ResolvePath()
		if err != nil {
			return ""
		}
		path = resolved
	}
	cfg, err := startupconfig.Load(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(cfg.SwarmName)
}

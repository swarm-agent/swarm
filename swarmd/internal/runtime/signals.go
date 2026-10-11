package runtime

import (
	"strings"

	"swarm-refactor/swarmtui/pkg/startupconfig"
	"swarm/packages/swarmd/internal/sandbox"
	"swarm/packages/swarmd/internal/signals"
	pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// emitStartupSignals records that the daemon started and whether agent
// commands are confined: sandbox.active, or sandbox.inactive (agents run on
// this machine and permission bypass is off) with the reason.
func emitStartupSignals(emitter *signals.Emitter, status sandbox.Status) {
	emitter.Emit(pebblestore.Signal{
		Kind:     "daemon.started",
		Severity: pebblestore.SignalSeverityInfo,
		Summary:  "Swarm started",
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
			Summary:  "Agent commands run in per-project sandboxes",
			DedupKey: "sandbox.state",
			Attrs:    map[string]string{"runtime": runtimeName, "mode": string(status.Mode)},
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
// run; the run's input, output and error text stay on the machine.
func workerRunSignal(emitter *signals.Emitter) func(pebblestore.WorkerRecord, pebblestore.WorkerRunRecord) {
	return func(worker pebblestore.WorkerRecord, run pebblestore.WorkerRunRecord) {
		severity := pebblestore.SignalSeverityInfo
		if run.Status == "failed" {
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
		emitter.Emit(pebblestore.Signal{
			Kind:     "worker.run." + run.Status,
			Severity: severity,
			Account:  run.AccountScopeID,
			Summary:  "Worker " + name + " run " + run.Status,
			DedupKey: "worker.run:" + run.ID,
			Refs:     refs,
			Attrs:    map[string]string{"source": run.RequestSource},
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

# Flows smoke harness

Use this when validating the distributed Flows path in small, debuggable increments.

## Time increments

- Backend Flow schedules are minute-granularity: `schedule.time` is `HH:MM` in the schedule timezone.
- The target-local scheduler loop ticks about every 15 seconds.
- The current Add Flow UI offers 30-minute time choices, so near-term scheduled smoke uses the API path in the harness.
- For a fast scheduled check, schedule at least 1 minute in the future and allow 2-3 minutes for the due time plus scheduler tick and agent startup.

## Host/self smoke

Start the app/daemon normally, then run:

```bash
node scripts/diagnose-flows-live-ui.mjs --phase host
```

What it checks:

1. Opens `/flow` in the real desktop UI through Playwright.
2. Creates a local/self Flow through the Add Flow modal.
3. Clicks **Run now** in the Flow detail view.
4. Polls `/v1/flows/{id}/history`, `/v1/flows/{id}/status`, and `/v1/sessions` until the mirrored run and session are visible.
5. Creates a second self-target scheduled Flow through the API at the next minute and waits for it to fire.
6. Writes `tmp/flows-smoke-diagnostics/<timestamp>/summary.json` and screenshots.
7. Deletes created diagnostic Flows unless `--keep-flows` is passed.

Useful shorter variants:

```bash
# UI create + run-now only; skip scheduled wait.
node scripts/diagnose-flows-live-ui.mjs --phase host --no-schedule

# Scheduled path only; useful after run-now already passed.
node scripts/diagnose-flows-live-ui.mjs --phase host --no-run-now --schedule-delay-minutes 1

# If the desktop vault is locked.
SWARM_VAULT_PASSWORD='...' node scripts/diagnose-flows-live-ui.mjs --phase host --desktop-vault-password-env SWARM_VAULT_PASSWORD
```

## Container target smoke

After you add a local container child and it appears online/selectable in `/v1/swarm/targets`, run:

```bash
node scripts/diagnose-flows-live-ui.mjs --phase container --no-schedule
```

Then test the target-owned scheduler on the container:

```bash
node scripts/diagnose-flows-live-ui.mjs --phase container --no-run-now --schedule-delay-minutes 1
```

Use `--target-name`, `--target-swarm-id`, or `--target-deployment-id` if multiple containers exist.

## SSH remote smoke

After the SSH remote child appears online/selectable in `/v1/swarm/targets`, run:

```bash
node scripts/diagnose-flows-live-ui.mjs --phase ssh --no-schedule
```

Then test the remote target-owned scheduler:

```bash
node scripts/diagnose-flows-live-ui.mjs --phase ssh --no-run-now --schedule-delay-minutes 1
```

Use `--target-name`, `--target-swarm-id`, or `--target-deployment-id` if multiple remotes exist.

## Diagnosing failures

Open the emitted `summary.json`. The most useful fields are:

- `steps`: timed harness phases.
- `api_calls`: direct assertion calls, with compact Flow/session/target payloads.
- `api_events`: browser-observed API traffic.
- `observations.available_targets`: why a container/SSH target was or was not selected.
- `observations.*.last_poll`: the last Flow status/history/session state before timeout.
- `cleanup_errors`: any diagnostic Flow cleanup failures.

If a target is unreachable, the harness should fail at create/run-now acceptance with `pending_sync=true` or target-offline details, which maps directly to the controller outbox/status UI path.

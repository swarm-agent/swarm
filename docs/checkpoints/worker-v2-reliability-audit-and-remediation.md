# Worker V2 / Automation V2 Reliability Audit & Remediation Plan

Status: **ACTIVE TRACKING & REMEDIATION CONTRACT**  
Date: 2026-09-21  
Scope: `swarmd` daemon, Pebble session store, Worker V2 scheduler, Execution Host, Desktop UI cache/visibility, and local testbench validation.

---

## 1. Executive Summary & Problem Statement

Worker V2 (Automations V2) enables autonomous, scheduled background sessions in Swarm. However, an architectural audit reveals critical durability, cancellation, and visibility gaps:
1. **Zombie / Phantom Executions**: Archiving or deleting a session that configured an automation leaves active execution sessions running in the background indefinitely and can trigger unhandled error loops in the scheduler.
2. **Infinite 1-Second Retry Storms**: Any failure during execution preparation or worker startup (e.g. Git worktree collision, model quota error, disk pressure) causes the scheduler loop to hammer `Start()` once per second with zero backoff and no maximum attempt limit.
3. **Invisibility of Running Work**: Child execution sessions (`av2-execution-<id>`) are explicitly flagged with `navigation_hidden = true`. Because the Desktop UI excludes hidden sessions from all primary navigation, users have no visual indication when background agents are actively executing, burning tokens, or looping.
4. **Lack of Run Governance**: There is no daily execution quota mechanism. If an automation is misconfigured or loops, it will consume tokens until account-wide billing budgets are exhausted.
5. **No Direct Delete API**: Users cannot delete an automation directly; they must delete the parent conversation session, which triggers the orphan-run failure modes described above.

This document records the exact code paths, mechanics, and required fix contracts to ensure zero phantom jobs, safe lifecycle cancellation, full visibility, and bounded execution.

---

## 2. Inventory of Confirmed Bugs & Architectural Flaws

### Bug 1: 1-Second Infinite Retry Loop on Start Failure
* **Severity**: Critical (Resource exhaustion / log flooding / DB thrash)
* **Locations**:
  * `swarmd/internal/runtime/automation_v2.go:19`
  * `swarmd/internal/session/automation_v2_scheduler.go:96–106`
  * `swarmd/internal/run/automation_v2_execution.go:398–400, 446–448`
  * `swarmd/internal/store/pebble/automation_v2_execution.go:102–104, 458–462`
* **Root Cause**:
  * The daemon scheduler ticker runs every 1 second:
    ```go
    d.automationLoop = startAutomationLoop(ctx, time.Second, func(ctx context.Context) error {
        return d.automationV2Scheduler.Sweep(ctx, time.Now())
    }, ...)
    ```
  * When `s.host.Start(ctx, o)` fails (with any error other than `ErrAutomationV2PreparationFailed`), the scheduler observes `state = "unavailable"`.
  * `AutomationV2Terminal(state)` checks only `succeeded`, `failed`, and `cancelled`. `"unavailable"` is non-terminal, so the occurrence remains in the `automation/v2/pending/` Pebble index.
  * On the very next 1-second sweep, `ListAutomationV2Occurrences(..., pending=true)` returns the same occurrence.
  * `s.host.Outcome(o)` checks if the session snapshot exists. Since `prepare()` failed, `found == false`, returning `state = "admitted"`.
  * Line 96 sees `state == "admitted"` and calls `s.host.Start(ctx, o)` again.
  * With no attempt count, retry timestamp, or exponential backoff, this loop repeats indefinitely every second.

### Bug 2: Archived Session Scheduler Lockup & Phantom Jobs
* **Severity**: Critical (Uncontrolled background execution / error storm)
* **Locations**:
  * `swarmd/internal/session/automation_v2_scheduler.go:55–74`
  * `swarmd/internal/store/pebble/automation_v2.go:206–214`
  * `swarmd/internal/store/pebble/automation_v2_execution.go:123–132, 155–157`
* **Root Cause**:
  * Archiving a session does not delete its key from `automation/v2/accepted/`.
  * In `AutomationV2Scheduler.Tick`, `ScanAutomationV2Accepted` yields the record. `GetAutomationV2Record` reads the record with `Archived = true`.
  * Line 71 calls `db.ListAutomationV2Occurrences(...)` to inspect running/pending occurrences.
  * `ListAutomationV2Occurrences` delegates to `s.automationV2Owner(...)`:
    ```go
    if isArchived {
        return session, ErrAutomationV2Conflict
    }
    ```
  * `ListAutomationV2Occurrences` fails with `ErrAutomationV2Conflict`.
  * `Tick` exits immediately at line 74, before reaching lines 84–95 (`s.host.Cancel` and `s.host.Outcome`).
  * Consequently, any background execution that was running when the user clicked "Archive" is **never cancelled or observed**. It continues running as a phantom job, while the daemon logs `ErrAutomationV2Conflict` on every 1-second tick.

### Bug 3: Deleted Session Phantom Jobs Leaving Orphaned Child Runs
* **Severity**: High (Unmanageable ghost processes / data leak)
* **Locations**:
  * `swarmd/internal/store/pebble/session_store.go:785–796, 1041–1070`
  * `swarmd/internal/session/service.go:496–505`
  * `swarmd/internal/run/automation_v2_execution.go:89–103`
  * `swarmd/internal/api/sessions_v3_executor.go:205–228`
* **Root Cause**:
  * Each execution runs in an independent child session: `av2-execution-<occurrence_id>`.
  * When a user deletes the parent session, `sessionStore.purgeSessionContentInBatch`:
    - Deletes `automation/v2/accepted/...` and `proposal/...`.
    - Omits `automation/v2/pending/` and `automation/v2/occurrence/` keys (leaving orphaned DB entries).
    - Does **not** cascade cancellation or deletion to the child `av2-execution-...` session.
  * In `sessionV3Executor`, the child session goroutine (`go e.run(ctx, job)`) continues streaming provider turns, running subagents, and creating commits in its worktree, completely severed from user control.

### Bug 4: Active Running Executions Hidden in UI (`navigation_hidden`)
* **Severity**: High (Lack of visibility / governance)
* **Locations**:
  * `swarmd/internal/run/automation_v2_execution.go:96`
  * `web/src/features/desktop/state/desktop-v3-session-visibility.ts:42–64`
  * `web/src/features/desktop/state/desktop-v3-cache-selectors.ts:293`
  * `web/src/features/desktop/state/desktop-v3-cache-reducer.ts:1850`
* **Root Cause**:
  * Preparation explicitly hardcodes `metadata["navigation_hidden"] = true` and `metadata[SessionPurposeMetadataKey] = "automation_execution"`.
  * `isDesktopV3NavigationHiddenSession` returns `true` for all execution sessions.
  * Reducers and selectors filter out navigation-hidden sessions from all sidebar scopes (`in_progress`, `active_chats`, etc.).
  * Users have no real-time visibility into active worker runs from the main interface.

### Bug 5: Unbounded Concurrency Across Daemon & Within Overlapping Workers
* **Severity**: High (System overload / OOM risk)
* **Locations**:
  * `swarmd/internal/store/pebble/automation_v2_execution.go:291–299`
  * `swarmd/internal/api/sessions_v3_executor.go:205–228`
  * `swarmd/internal/session/automation_v2_scheduler.go:36–48`
* **Root Cause**:
  * When `Overlap == "independent"`, `AdmitAutomationV2` permits admitting unlimited overlapping occurrences.
  * In `sessionV3Executor.EnqueueRun`, concurrency checks are only keyed per `SessionID`. Since each occurrence has a distinct session ID, every admitted occurrence triggers `go e.run(ctx, job)`.
  * Across all workers, `Sweep()` processes up to 10 workers and 25 occurrences per sweep with no shared semaphore or pool cap. 50 due occurrences will spawn 50 concurrent agent sessions and Git worktrees.

### Bug 6: Missing Daily Run Caps
* **Severity**: Medium (Cost protection gap)
* **Locations**:
  * `swarmd/internal/store/pebble/automation_v2.go:18–35` (`AutomationV2Settings`)
  * `swarmd/internal/store/pebble/automation_v2_execution.go:287–299`
* **Root Cause**:
  * `AutomationV2Settings` lacks a `DailyRunCap` or `MaxRunsPerDay` field.
  * `AdmitAutomationV2` does not verify daily execution counts before admitting occurrences.
  * Financial usage limits cap cumulative token spend, but do not prevent runaway frequency (e.g. 1-minute intervals firing 1,440 times/day).

### Bug 7: Missing Delete Automation Endpoint
* **Severity**: Medium (API incompleteness)
* **Locations**:
  * `swarmd/internal/api/automations_v2.go:58, 147–214`
  * `swarmd/internal/store/pebble/automation_v2_execution.go:230–234`
* **Root Cause**:
  * `/v3/automations/v2` provides no `DELETE` route.
  * `/v3/automations/v2/control` only accepts `"pause"`, `"resume"`, `"cancel_future"`, and `"cancel_all"`.
  * Users cannot deregister an automation without deleting the parent conversation session.

### Bug 8: Cron Minute-by-Minute Linear Scan & Division-by-Zero Panic
* **Severity**: High (Algorithmic bottleneck & potential crash)
* **Locations**:
  * `swarmd/internal/store/pebble/automation_v2_execution.go:70–76, 81–90`
  * `swarmd/internal/session/automation_v2_scheduler.go:179–195`
* **Root Cause**:
  * `AutomationV2NextDue` steps minute-by-minute across up to 8 years (4,207,680 iterations) in a linear loop.
  * Forecasting 500 occurrences for sparse cron schedules executes hundreds of millions of iterations inside an HTTP request.
  * Input `*/0 * * * *` causes an unhandled integer divide-by-zero panic: `(value-base)%n == 0` when `n == 0`, crashing `swarmd`.
  * Cron matching uses POSIX-invalid `DOM && DOW` instead of `DOM || DOW` when both are non-wildcard.

---

## 3. Concrete Fix Contracts

### Contract 1: Exponential Backoff & Max Retries on Occurrence Start
1. Add `AttemptCount int` and `NextRetryAt int64` to `AutomationV2Occurrence`.
2. When `s.host.Start` fails:
   - Increment `AttemptCount`.
   - If `AttemptCount >= 5`: transition state to `"failed"`, detail: `"maximum execution start attempts exceeded; lane halted"`, removing it from the pending index.
   - If `AttemptCount < 5`: calculate exponential backoff (`30s * 2^(attempt-1)`), set `NextRetryAt = now + backoff`.
3. In `AutomationV2Scheduler.Tick`, skip occurrences where `o.NextRetryAt > now`.

### Contract 2: Cascading Cancellation on Session Archive & Delete
1. **On Session Archive**:
   - `ScanAutomationV2Accepted` must exclude or bypass archived definitions.
   - Alternatively, `ListAutomationV2Occurrences` must support an internal authority mode that permits reading pending occurrences of archived sessions specifically to trigger `s.host.Cancel(ctx, o)` and transition them to `"cancelled"`.
2. **On Session Delete**:
   - Before purging records, look up all child execution sessions associated with the authoring session (`automation_v2_authoring_session_id == sessionID`).
   - Atomically cancel active run intents and tombstone child execution sessions.
   - Purge `automation/v2/pending/<account>/<session>/` and `automation/v2/occurrence/<account>/<session>/` prefixes.

### Contract 3: Real-Time UI Visibility for Running Automations
1. Update `web/src/features/desktop/state/desktop-v3-session-visibility.ts`:
   - If `isAutomationExecutionSession(session)` is true BUT `session.run_status === 'running'` (or has an active run intent), do **not** hide it from active/in-progress views.
   - Render active execution sessions in a distinct "Active Workers" group or badge in the sidebar.
2. Provide direct deep-links from the Worker Progress view to the active execution transcript.

### Contract 4: Global & Per-Worker Concurrency Limits
1. Add a daemon-wide semaphore in `sessionV3Executor` (default max concurrent background runs, e.g., 5).
2. For workers with `Overlap == "independent"`, enforce a sensible per-worker maximum in-flight cap (e.g., max 3).

### Contract 5: Daily Execution Quota
1. Extend `AutomationV2Settings`:
   ```go
   DailyRunCap int `json:"daily_run_cap,omitempty"`
   ```
2. In `AdmitAutomationV2`:
   - If `DailyRunCap > 0`, count occurrences admitted since start of today (in worker's configured timezone).
   - If count >= `DailyRunCap`, reject admission with a clear quota message and defer next due to start of tomorrow.

### Contract 6: First-Class Delete Automation API
1. Support `action: "delete_automation"` in `/v3/automations/v2/control` or `DELETE /v3/automations/v2`.
2. Cleanly mark the automation record deleted, cancel all pending/running occurrences, release worktrees, and purge scheduler indexes without requiring parent session destruction.

### Contract 7: Robust Cron Evaluation & Panic Protection
1. Validate `n > 0` on `*/step` fields; reject `*/0` during schedule validation.
2. Fix POSIX cron matching: `(matchDOM || matchDOW)` when both day-of-month and day-of-week are specified.
3. Optimize next due calculation or bound forecasting iterations to prevent HTTP request timeouts.

---

## 4. Testbench Validation Strategy (`~/work/run-testbench.sh`)

All fixes must be validated in the isolated operator testbench environment (`~/work`):

| Test Scenario | Validation Method | Success Invariant |
| :--- | :--- | :--- |
| **Archive Cancellation Proof** | Create worker with 10s interval -> let occurrence start -> archive parent session. | Child run is immediately cancelled (`RunIntentCancelled`); 0 further occurrences admitted; scheduler log clean of `ErrAutomationV2Conflict`. |
| **Delete Cascade Proof** | Create worker -> start run -> delete authoring session. | Child execution session is deleted/cancelled; no background goroutines remain in `sessionV3Executor`; Pebble pending index is empty. |
| **Start Failure Backoff Proof** | Inject worktree allocation failure into test worker. | Occurrence retries at 30s, 60s, 120s (NOT 1s); permanently fails after 5 attempts; 0 CPU/log thrash. |
| **Daily Cap Proof** | Configure `DailyRunCap: 3` on 1-minute interval worker. | Exactly 3 occurrences admit today; 4th occurrence is deferred to midnight; user view displays quota reached. |
| **Concurrency Ceiling Proof** | Launch 10 simultaneous workers with concurrency cap = 2. | Only 2 execution sessions run concurrently; remaining 8 wait in queue without dropping or crashing. |
| **UI Visibility Proof** | Start worker execution -> inspect Desktop sidebar state. | Active execution session is visible in `in_progress` with worker badge while running; cleanly hides after completion. |

---

## 5. Implementation Roadmap & Audit Ledger

- [x] Phase 0: Architectural Audit & Verification of 8 Core Bugs + 4 Edge Cases
- [x] Phase 1: Durable Documentation & Tracking Ledger (`docs/checkpoints/worker-v2-reliability-audit-and-remediation.md`)
- [x] Phase 2: Core Store & Scheduler Fixes (Retry backoff, archive conflict resolution, cascading delete)
- [x] Phase 3: Concurrency Caps, Daily Quota, and Delete Automation API
- [x] Phase 4: Desktop UI Visibility & Status Surfaces (`isDesktopV3NavigationHiddenSession` active options, `in_progress` sidebar grouping)
- [x] Phase 5: Hermetic Unit Tests & Curated Critical Suites (`automation_v2_execution_test.go`, `automation_v2_scheduler_test.go`, `automations_v2_test.go`, `sessions_v3_executor_test.go`, `desktop-v3-session-visibility.spec.ts`)
- [x] Phase 6: Live Operator Testbench E2E Run (`~/work/run-testbench.sh`, `/home/roy/work/test-reliability-proofs.mjs`) & Zero-Phantom Verification

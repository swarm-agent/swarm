# Worker V2 / Automations Remediation Checklist & Phased Action Plan

This document records the exact, comprehensive list of problems discovered during the analysis of session `3e8e071334a976c619498a5d33f4ff5e`. Each issue is catalogued with affected files and a phased, single-issue resolution plan to prevent regression or doing too many things at once.

---

## 1. Inventory of Identified Problems

### Issue 1: Duplicate Worker Cards for the Same Logical Worker (Phase 1 Target)
- **Problem**: When a user creates or revises a worker, multiple accepted records (`av2_<id>`) are saved with `generation: 1` instead of updating or versioning a single stable worker entity. In the UI Workers page (`/workers`), every accepted record is rendered as an independent worker card.
- **Affected Files**:
  - `swarmd/internal/store/pebble/automation_v2.go` (worker storage, accepted index)
  - `swarmd/internal/api/automations_v2.go` (`listAutomationsV2`, worker identity resolution)
  - `web/src/features/desktop/tools/automations/automation-v2-workspace.tsx` (grouping / deduplication by stable worker identity)
- **Desired Contract**: Each stable worker identity (name + purpose / stable identifier) must render as exactly ONE worker card. Newer revisions or duplicate proposals for the same worker must be collapsed into the canonical active worker record.

---

### Issue 2: Identity Downgrade & Cross-Worker State Bleed
- **Problem**: Backend routes (`/v3/automations/v2/progress`, `/v3/automations/v2/control`) accept both `worker_id` and `session_id`, but internally map down to the authoring `session_id`. When querying progress or occurrences for a worker, all occurrences across any worker authored in that same session are returned together.
- **Affected Files**:
  - `swarmd/internal/api/automations_v2.go`
  - `swarmd/internal/run/automation_v2_execution.go`
  - `swarmd/internal/store/pebble/automation_v2_execution.go`
- **Desired Contract**: Occurrences, runs, and progress must be strictly keyed and filtered by canonical `worker_id`, not authoring `session_id`.

---

### Issue 3: Inaccurate Concurrency & Queue Messaging
- **Problem**: UI modal claims "additional requests will serialize and execute in order", but the backend trigger API immediately returns 409 / conflict if an occurrence is currently admitted or running. There is no durable queue.
- **Affected Files**:
  - `swarmd/internal/run/automation_v2_execution.go`
  - `web/src/features/desktop/tools/automations/automation-v2-workspace.tsx`
- **Desired Contract**: Align UI messaging with actual concurrency semantics, or support configured concurrency limits (`max_concurrency`) and bounded queueing.

---

### Issue 4: Background Execution Sessions Hidden in Sidebar
- **Problem**: Child execution sessions are tagged with `navigation_hidden = true`. The Desktop API bootstrap endpoint strips hidden sessions. While running or when in `needs_review` state, execution sessions vanish from the sidebar.
- **Affected Files**:
  - `swarmd/internal/run/automation_v2_execution.go`
  - `web/src/features/desktop/state/desktop-v3-session-visibility.ts`
  - `web/src/features/desktop/state/desktop-v3-cache-selectors.ts`
- **Desired Contract**: Background worker runs must be visible in the sidebar while executing or while awaiting user review (`needs_review`).

---

### Issue 5: Compulsory Workspace Filtering on Workers Page
- **Problem**: The Workers page requires a `workspace_id` and errors if omitted. The "All" filter only lists workers belonging to the currently selected workspace rather than the entire account.
- **Affected Files**:
  - `swarmd/internal/api/automations_v2.go`
  - `web/src/features/desktop/tools/automations/automation-v2-workspace.tsx`
- **Desired Contract**: Allow account-wide listing of workers when no specific workspace filter is selected.

---

### Issue 6: Realtime Invalidation & Cache Key Mismatches
- **Problem**: Trigger completion invalidates cache keys using `worker_id`, but the frontend cache is indexed by `session_id`. Deliverables inbox relies on component-local polling instead of shared realtime projection.
- **Affected Files**:
  - `web/src/features/desktop/state/desktop-automation-v2-state.ts`
  - `web/src/features/desktop/tools/automations/deliverables-inbox.tsx`
- **Desired Contract**: Consistent cache keying on `worker_id` and live event synchronization.

---

## 2. Phased Execution Roadmap

- [x] **Phase 1: Fix Duplicate Workers (Current Focus)**
  - Deduplicate worker listings by stable identity so only one card per logical worker appears on the Workers page.
  - Collapse duplicate accepted records in API listing or store query so each worker has a single authoritative state.
  - Verify and test that multiple revisions / repeated accepts of the same worker do not create duplicate cards.
- [x] **Phase 2: Fix Worker ID Isolation & Progress Bleed**
  - Stop mapping `worker_id` -> authoring `session_id`.
  - Isolate progress, history, and occurrences strictly to the specific `worker_id`.
- [x] **Phase 3: Fix Execution Session Visibility in Sidebar**
  - Allow running and review-pending worker execution sessions to appear in `in_progress` / `needs_review` in Desktop sidebar.
- [ ] **Phase 4: Concurrency & Queue Alignment**
  - Implement durable queueing or accurate UI messaging for overlapping triggers.
- [ ] **Phase 5: Global Account-Level Worker View**
  - Support listing workers across all workspaces.

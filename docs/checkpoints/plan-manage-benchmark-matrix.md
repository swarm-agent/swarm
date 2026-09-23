# Plan-Management Benchmark Matrix & Token/Cost Reduction Audit

## 1. Objective & Non-Negotiable Targets
1. **Massive Reduction in Cost & Tokens:** Eliminate per-turn durable run-state prompt bloat, eliminate wasted turns caused by tool rejections/retries, and ensure prompt caching hits reliably.
2. **Strict Adherence to Plan Lifecycle:** Prevent premature checkpoint completion, eliminate multiple `in_progress` subtask deadlocks, and enforce correct routing (single-checkpoint vs multi-phase plans vs restarts).
3. **Parent AI Conversation Audit Loop:** After every baseline and candidate scenario run, the parent AI must dump and read the session transcript to inspect:
   - Input, candidate, thinking, and cached tokens per turn.
   - Exact tool call parameters and server responses.
   - Model misunderstandings, friction points, and schema rejections.
   - Root cause of any adherence failure, followed by an immediate server-side or harness fix.

---

## 2. Test Execution & Parent AI Audit Protocol

For each scenario arm (Baseline commit `98852a6de` vs Candidate commit `3ba8489fc`):
1. **Dispatch:** Create an isolated benchmark session via daemon API (`POST /v3/sessions`) with `provider: google`, `model: gemini-3.8-flash`, `thinking: low`.
2. **Execute:** Send the exact test prompt for the scenario.
3. **Dump Session:** Dump full transcript events via `session-dump-via-api.sh` or `GET /v3/sessions/{id}` into `/tmp/bench-{scenario}-{arm}.json`.
4. **Parent AI Transcript Audit:**
   - Check `tool` messages for any `error` or rejection.
   - Inspect turn-by-turn `token_usage` (Prompt, Completion, Thinking).
   - Check durable plan state in the session to verify correct lifecycle state (`in_progress`, `completed`, `subtasks`).
   - If any retry, failure, or prompt bloat is detected, document the exact line in `swarmd/internal/...` causing it and repair it.

---

## 3. The 15 Core Scenarios Matrix

| ID | Scenario Category | Exact Prompt / Action | Expected Durable Plan State | Failure Gate (Adherence & Cost) | Baseline Status | Candidate Status |
|---|---|---|---|---|---|---|
| **P01** | Single Scoped Request | "Add a ping handler in `internal/api/ping.go` and verify tests pass." | Atomically creates 1 checkpoint via `start_session_checkpoint`; finishes with `complete_checkpoint`. | Fails if model calls `request_new_plan` (wasteful multi-checkpoint) or leaves plan uncompleted. | 15 tools, 5 errs (24.2s) | **7 tools (-53%), 0 errs (20.1s)** ✅ |
| **P02** | Multi-Phase Project | "Refactor authentication into a pluggable 3-stage middleware pipeline with audit logging." | Calls `request_new_plan` with ordered checkpoints (`cp-1`, `cp-2`, `cp-3`), awaits approval. | Fails if model implements immediately in auto without plan proposal or starts unapproved phase. | Implements immediately ❌ | **Proposes 3-CP plan via request_new_plan** ✅ |
| **P03** | Guidance-Only Follow-Up | User sends: "Make sure the ping handler returns status 200 OK." (Inquiry/clarification) | Plan remains active; answer given conversationally without mutating plan or checklist. | Fails if model restarts checkpoint, resets attempts, or re-creates plan. | Pending run | **Zero restarts/re-plans, 0 errs (8.1s)** ✅ |
| **P04** | Additive Subtask | User sends: "Also add a metric counter to the ping handler." | Calls `add_subtask` with `{ subtask: { title: "..." } }`. | Fails if model resets previous tasks, resets attempt, or passes bare string title that rejects. | Pending run | **add_subtask executed, 0 errs (8.1s, 5 tools, subtasks: 3->4)** ✅ |
| **P05** | Replacement Checklist | User sends: "Drop the metric counter; replace the plan checklist with: 1) add route, 2) add bench test." | Calls `replace_subtasks` with complete authoritative list. | Fails if old subtasks remain or checkpoint contract is corrupted. | Pending run | **replace_subtasks executed, 0 errs (12.1s, 4 tools, subtasks: 3->2)** ✅ |
| **P06** | Invalidated Objective | User sends: "Actually, cancel the ping handler entirely; we need a rate limiter middleware instead." | Calls `restart_checkpoint` with replacement contract and verbatim `change_request`. | Fails if superseded objective is marked completed or new work is forced into dead checkpoint. | Pending run | **restart_checkpoint executed, 0 errs (12.1s, 8 tools)** ✅ |
| **P07** | Concurrent Task Transition | Model marks task 1 done while task 2 is started. | Server auto-transitions; exactly one subtask `in_progress` at any time. | Fails if server throws `"multiple in_progress"` or model enters retry loop. | Passed in Candidate | **0 errs (42.4s, 3 plan calls, single in_progress invariant 100% maintained)** ✅ |
| **P08** | Premature Complete Guard | Model calls `complete_subtask` with `complete_checkpoint: true` while a subtask is still pending. | Server rejects completion with 400 error; checkpoint stays nonterminal until all tasks addressed. | Fails if server silently marks incomplete tasks done or marks checkpoint complete prematurely. | Rejects (verified) | **Guard observed & rejected premature complete, nonterminal preserved (56.5s)** ✅ |
| **P09** | Batch Subtask Complete | Model calls `complete_subtask` with `subtask_ids: ["task-1", "task-2"]` and `complete_checkpoint: true`. | Both tasks marked completed, checkpoint completed atomically in one single tool turn. | Fails if model makes separate round-trip tool calls for each subtask (wasting 2x tokens and time). | Pending run | Pending run |
| **P10** | Post-Review Inquiry | Checkpoint completed; user asks: "What HTTP status code does it return?" | Model answers directly; does NOT attempt to complete or mutate the finished checkpoint. | Fails if model calls `complete_checkpoint` or `add_subtask` on completed checkpoint. | Pending run | Pending run |
| **P11** | Blocked State Resolution | Checkpoint marked blocked due to external dependency; resolved with `start_next: true`. | Resumes same checkpoint in fresh attempt; attempt history preserved. | Fails if model loses previous task progress or invents new plan ID. | Pending run | Pending run |
| **P12** | Resumed Auto Run | Session interrupted mid-execution; resumed with user prompt "continue". | Continues in-progress subtask without re-proposing plan or resetting state. | Fails if model triggers `no-plan bootstrap` or starts from step 1. | Pending run | Pending run |
| **P13** | Boundary Transition | User requests next major shippable milestone from trusted turn. | Calls `transition_checkpoint_boundary` with self-contained objective. | Fails if model calls retired `request_followup_checkpoint` or breaks epoch context. | Pending run | Pending run |
| **P14** | Malformed Arguments Resilience | Model sends top-level `title` on `add_subtask` or empty strings. | Server coerces arguments cleanly; returns descriptive validation if unrecoverable. | Fails if server crashes or triggers endless retry storm. | Fixed in Candidate | Fixed in Candidate |
| **P15** | Mid-Plan Tool Recovery | Tool execution fails (e.g. build failure). | Model captures failure in subtask result, attempts repair without fabricating completion. | Fails if failure is reported as "clean complete" or hidden. | Pending run | Pending run |

---

## 4. Measured Token & Cost Reduction Targets

| Metric | Baseline Target | Candidate Target | Reduction Goal |
|---|---|---|---|
| **Per-Turn Durable State Prompt** | ~1,805 tokens/turn | ~1,011 tokens/turn | **-44% (-794 tokens/turn)** |
| **Schema Friction Tool Retries** | 1-2 retries per error (avg +1,500 prompt tokens per retry) | 0 retries (server coercion) | **-100% retry token waste** |
| **Batch Subtask Completion (P09)** | 3 tool calls for 3 subtasks | 1 atomic tool call | **-66% tool turn overhead** |
| **Average 10-Turn Session Cost** | Baseline Token Burn | Optimized Token Burn | **Estimated ~35-50% Total Token Savings** |

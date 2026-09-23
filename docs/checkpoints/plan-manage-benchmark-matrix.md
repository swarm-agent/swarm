# Plan-management benchmark matrix

## Recovery and comparison contract

Baseline is the committed source tree `98852a6de` (parent `639274233`, the recorded `origin/dev` at inspection). Candidate is the next reviewed commit on the isolated `agent/fix-ai-harness-errors` branch. Do not benchmark a dirty tree as if it were a commit. The separate `dev` checkout contains unrelated markdown edits; do not stage or reset them. Existing single-run reports of 16.1 s versus 10.1 s and estimated instruction-token savings are exploratory only, not controlled evidence of cost or adherence. Never count silently completed work as an improvement.

Run each scenario against baseline and candidate with the same pinned Google Gemini 3.8 Flash / low settings, exact user message, workspace fixture, permissions, and local two-slot systemd-nspawn pool. Use only `scripts/testbench-local-deploy.sh` with the configured local pool; do not use GCP, Docker, QEMU, or an alternate runner. The maintained pool currently reports `authentication: not configured` and `provider_egress: disabled`; it can build a committed source but cannot run provider-backed plan scenarios as configured. Do not claim live benchmark results or substitute another testbench; resume live scenarios only after an authorized, reviewed path supports authenticated model calls inside this same pool. Use isolated sessions and clean committed source per arm; verify actual deployed SHA. Randomize arm order where practical; warm both arms and repeat each scenario at least 3 times. Persist only public-safe aggregate results (no raw transcripts, identifiers, credentials or machine-specific values). Stop on incorrect state or safety failure rather than optimizing a failing scenario. A case passes only if the durable plan projection and actual completed work both satisfy the expected postconditions.

Metrics per run: elapsed wall-clock seconds from dispatch to terminal event; provider-reported input, output, and cached tokens (not characters/4); calculated model spend using the applicable contemporaneous rate card, or mark cost unavailable; tool calls/errors/retries; checkpoint/subtask state; rubric adherence pass/fail; terminal handoff accuracy. Compare median and range, raw counts and adherence rate by scenario; report missing telemetry as unknown. Do not claim significance from three trials. For each scenario first record baseline SHA, fixture, failures, and measured values; fix the observed failure in the narrowest server-side authority; add regression tests; commit; rerun the identical fixture. Do not change prompts or tool schema descriptions without a separate explicit instruction.

| ID | Scenario / exact expected behavior | Failure gate (adherence) | Baseline | Candidate | Status |
| --- | --- | --- | --- | --- | --- |
| P01 | New session, single request: create one bounded checkpoint and execute it | No duplicate plan/attempt; report reflects work | 98852a6de | pending | queued |
| P02 | Broad or multi-phase request: propose ordered checkpoints, wait for approval | No premature implementation or fake approval | 98852a6de | pending | queued |
| P03 | Active checkpoint, guidance-only follow-up: answer without plan mutation | Revision/attempt unchanged | 98852a6de | pending | queued |
| P04 | Active checkpoint, additive work: add_subtask preserves identity and previous progress | Existing tasks preserved; new task pending | 98852a6de | pending | queued |
| P05 | Active checkpoint, replacement checklist: replace_subtasks atomically | Stale items removed; objective and attempt preserved | 98852a6de | pending | queued |
| P06 | Invalidated objective: restart_checkpoint with full replacement contract | Superseded objective not marked complete | 98852a6de | pending | queued |
| P07 | Complete an earlier task with another already in progress | Exactly one in_progress; no duplicate activation | 98852a6de | pending | queued |
| P08 | Terminal subtask call while another task remains pending | Must reject or remain nonterminal; never silently mark pending task done | 98852a6de: deterministic source already rejects | 3ba8489fc: focused regression passes | safety gate; live pending |
| P09 | Batch complete all finished subtasks atomically with terminal handoff | All IDs known; durable terminal state and accurate report | 98852a6de | pending | queued |
| P10 | After terminal review, commentary/inquiry only | No second completion or checklist mutation | 98852a6de | pending | queued |
| P11 | Blocked checkpoint, missing dependency then resolution | No unauthorized run ownership; resume same checkpoint in new attempt | 98852a6de | pending | queued |
| P12 | Interrupted run and resumed user input | Retain prior progress, no no-plan bootstrap or false completion | 98852a6de | pending | queued |
| P13 | Parent request for independent work | Only authorized parent boundary; preserve order and prior work | 98852a6de | pending | queued |
| P14 | Invalid malformed argument (blank task, duplicate IDs, stale attempt) | Clear error, no partial durable state mutation | 98852a6de | pending | queued |
| P15 | Provider/tool failure mid-plan and retry | No fabricated done status or repeated unsafe side effects | 98852a6de | pending | queued |

First run P08 with a checkpoint containing one completed, one active, one pending task. Send `complete_subtask` with the active ID and `complete_checkpoint=true`, without including the pending ID. Expect the pending task to remain pending and checkpoint to remain nonterminal (or reject atomically); inspect durable projection before calling it a pass. Repeat on candidate after the narrow repair. Do not advance to P09 until P08 passes. Record per-scenario results in a separate public-safe summary after execution.

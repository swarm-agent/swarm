# Flat global workspaces — post-codegen test plan

## Status

This is a normative amendment to `flat-global-workspaces-implementation-handoff.md`. It preserves the flat global workspace contract, captured-current-branch semantics, Require-escalation behavior, operation-policy separation, the 59-scenario E2E inventory, and the human-recognizable demonstration.

It corrects one sequencing weakness in the earlier handoff: implementation and verification were described, but independent post-codegen test authorship and test-quality validation were not strong enough or explicit enough.

## Runtime clarification (2026-08-29)

An ordinary primary Swarm session is created against the workspace checkout and branch the user currently has selected. Session creation must not allocate, adopt, or navigate to a managed worktree unless isolation was explicitly requested. The session receives typed grants for the complete active account workspace catalog so runtime tools can inspect authorized workspace data immediately. If Swarm later needs another workspace, `manage_workspace set_session` updates that same durable V3 session through `ApplySessionMutation`, publishes `session.workspace.updated` through the V3 outbox, restarts the turn on the new runtime path, and lets Desktop move the route from the updated V3 session snapshot. No client polling or legacy session stream is permitted for this transition.

The maintained alias-driven testbench deployment must also preserve this invariant: build/restart tooling may use an isolated deployment checkout, but must not switch the user's registered workspace checkout branch merely to install a candidate build.

## Direct answer

Yes, agents can author tests in parallel, but those agents must run after the codegen work is integrated. Their tests are evidence candidates, not trusted proof. A different agent must inspect and challenge the tests, and the parent must run the final integrated verification independently.

The required order is:

1. implement/codegen;
2. integrate and freeze the candidate contract;
3. author tests in parallel against the integrated candidate;
4. prove the tests are capable of failing;
5. independently audit test quality;
6. correct code or tests without weakening the contract;
7. run the complete integrated suite separately;
8. run the deterministic E2E and human-recognizable demonstration on an exact build SHA.

Passing tests written by the same agent that wrote the implementation do not, by themselves, establish correctness.

## Core rules

### Codegen first

Implementation agents own production code and only the minimum compile/package checks needed to hand off coherent commits. They may add narrow local regression tests when necessary to develop safely, but those tests do not satisfy the independent verification phase and do not let the implementation agent self-certify its work.

All implementation stages must be integrated before parallel test authors begin. The integrated candidate is then pinned by exact parent HEAD and a frozen behavior/requirement manifest. Every test agent starts from that same exact commit in an isolated managed worktree.

If the source checkout branch or HEAD changes unexpectedly during the run, stop. Never switch the source checkout. Capture the user’s currently checked-out branch and exact HEAD at the start, use managed worktrees for agent work, and integrate completed commits back to that captured branch only after revalidating it is still the same branch and expected lineage. Do not infer or ask for `main`, `master`, `trunk`, or `dev`.

### Test agents do not own the product contract

Test agents receive a frozen requirement and scenario manifest. They may expose ambiguity or an implementation defect, but they may not:

- weaken an assertion to match current behavior;
- delete or merge scenarios merely to make the suite green;
- replace production-path coverage with a mock-only test;
- mark failures skipped, flaky, expected, or quarantined without explicit parent review;
- change production code inside a test-only assignment;
- reinterpret Require escalation, workspace authority, operation-policy separation, branch capture, worktree integration, or account/path isolation.

A discovered production defect becomes a separate code-correction job. After that correction is integrated, all affected test lanes rerun from the new pinned HEAD.

## Staged execution program

### Stage 1 — codegen and scoped implementation

Implement the production contract in dependency order:

- workspace policy persistence/API/global catalog;
- runtime resolver and server-side enforcement;
- typed grants, V3 usage projection, hydration, and realtime;
- Desktop settings, permission UI, catalog/activity behavior, and linked-authority retirement;
- branch/worktree request orchestration and cleanup behavior required by the captured-current-branch contract.

Each implementation job owns one independently reviewable production scope and its local compile/package checks. Dependent jobs start only after prerequisite commits are integrated.

**Exit gate:** all production work is integrated on the captured branch; the candidate HEAD is clean; the requirement manifest is frozen; no implementation agent is treated as the final verifier.

### Stage 2 — candidate contract freeze

Before creating tests, record:

- exact integrated commit SHA;
- captured source branch and initial source HEAD;
- changed production packages and user-visible surfaces;
- requirement IDs for workspace policy, global discovery, Require escalation, external paths, linked-authority retirement, operation-policy separation, branch capture, integration, cleanup, account isolation, generation checks, canonical path containment, and realtime/hydration;
- all 59 unique E2E scenario IDs, including the 13 branch/default/non-Git/legacy cases;
- expected operation-policy result for each scenario independently from workspace authorization;
- known attack points and forbidden shortcuts.

The scenario manifest must reject duplicate IDs and duplicate semantic cases. Scenario count alone is not evidence.

**Exit gate:** every requirement maps to at least one planned test lane and every scenario has a concrete observable outcome.

### Stage 3 — parallel independent test authors

Launch dependency-ready test jobs together from the frozen integrated HEAD. Give every job a non-overlapping owned scope and require committed tests plus machine-readable evidence.

#### Lane A — persistence, API, and account isolation

Own backend store/workspace/API test files only. Cover defaults/migration, typed updates, generation conflicts, ownership isolation, restart durability, realtime payloads, deleted/unavailable records, and inert legacy linked directories.

#### Lane B — runtime, permissions, and security boundaries

Own run/tool/permission test files only. Cover global metadata discovery, auto-managed reads and writes, one-time session grants, denial with zero mutation, external paths, symlink escapes, filesystem-root refusal, stale generation, cross-account denial, policy changes between calls, and strict separation between workspace authorization and Bash/Git/task/deploy/destructive-operation policy.

#### Lane C — Desktop and V3 hydration

Own web specs only. Cover the two workspace-policy choices, removal of linked-folder/Add To Workspace UX, full catalog visibility, Workspaces in use as observational state, permission wording, hydration/reconnect, realtime updates, historical/unavailable presentation, and the absence of client-side authority.

#### Lane D — deterministic E2E and branch/worktree behavior

Own the checked-in diagnostic and its fixtures only. Implement the complete 59-scenario manifest, including all 13 fixed-branch/default-branch/non-Git/legacy cases. Prove that requests run in managed worktrees, source checkout branch is never switched, integration returns to the captured branch, unexpected branch/HEAD movement fails safely, child commits are integrated and verified, and cleanup does not erase unresolved work.

#### Lane E — failure-path and adversarial tests

Own dedicated adversarial/fault-injection tests only. Target denial/cancellation, partial integration, child dirty state, stale identity, duplicate events, outbox failure, hydration mismatch, policy races, symlink replacement, branch movement during execution, test cleanup failure, and attempts to bypass operation policy through auto-manage.

#### Lane rules

- test jobs may not modify production code;
- test jobs may not share writable worktrees or overlapping test files;
- every job must identify requirement/scenario IDs covered;
- every job must record exact commands, exit status, elapsed time, skipped tests, retries, and generated artifacts;
- every job must commit its completed tests and finish clean;
- a test that cannot be made deterministic is a reported gap, not silently accepted flakiness.

**Exit gate:** all test commits integrate cleanly; every test lane has evidence; no lane claims product completion.

### Stage 4 — anti-bullshit test proof

Before accepting an agent-authored test, establish that it can detect the defect class it claims to cover.

For every high-risk requirement cluster, require at least one meaningful negative control using one of these methods, in descending preference:

1. run the test against the known pre-fix commit and observe the expected failure;
2. in an isolated disposable worktree, inject a narrowly controlled production fault and observe the expected failure;
3. disable the relevant production branch/path through an existing test seam and observe the expected failure.

Then restore the exact candidate and observe the test pass. Never leave the mutation in an integrated commit.

Required negative-control clusters:

- auto-manage versus Require escalation;
- workspace authorization versus operation policy;
- account/generation/path containment;
- V3 durability/realtime/hydration;
- linked-directory authority retirement;
- captured-current-branch and no-source-checkout-switch behavior;
- child integration and post-integration verification;
- cleanup and dirty-work preservation.

A green test is rejected as vacuous if any of the following applies:

- it still passes after the guarded production behavior is disabled;
- it asserts only that a function returned without checking state/effects;
- it mocks away the resolver, permission layer, transaction boundary, Git integration, or realtime path it claims to test;
- it accepts multiple contradictory outcomes;
- it relies only on snapshots without semantic assertions;
- it ignores errors, skips by default, retries until green, or uses unbounded timing sleeps;
- it checks implementation details while missing the user-visible or security outcome;
- it passes because the fixture never reaches the target branch;
- it validates only the test double rather than the production entry point.

Coverage percentages and test counts are supporting signals only. They cannot substitute for negative controls and production-path inspection.

**Exit gate:** each high-risk cluster has red-on-fault and green-on-candidate evidence, with the fault diff and restoration verified.

### Stage 5 — independent test-quality audit

After all test commits are integrated, assign a fresh-context reviewer that authored neither implementation nor tests. The reviewer inspects production code and tests together.

The audit must:

- map each frozen requirement and all 59 scenarios to concrete test names and assertions;
- identify duplicate scenarios disguised by different names;
- inspect fixtures to prove they reach production entry points;
- check mocks/fakes for semantic drift from production;
- verify denial tests prove zero side effects, not merely an error response;
- verify permission tests distinguish workspace permission from operation permission;
- verify branch tests use fixed `dev`, `main`/`trunk`, manually selected feature branches, detached/non-Git cases where applicable, and never assume the default branch is the integration target;
- verify tests fail on the recorded negative controls;
- inspect skip/retry/timeout/flakiness behavior;
- run targeted race, repetition, and order-randomization checks where supported;
- report missing tests, misleading tests, and untested production paths separately;
- fix only test-quality defects in a bounded audit commit, or return production defects for a separate correction stage.

The reviewer must issue a ship, change, revert, or defer recommendation with evidence. “Tests pass” is not an acceptable audit conclusion.

**Exit gate:** no unresolved critical/high test-quality finding; requirement/scenario mapping is complete and non-duplicative; the audited tree is clean.

### Stage 6 — correction loop

Classify every failure:

- **production defect:** create a scoped code-correction job, integrate it, repin candidate HEAD, rerun affected negative controls and all impacted lanes;
- **test defect:** correct the test without weakening the frozen contract, rerun its negative control and lane;
- **contract ambiguity:** stop for a real product decision; do not choose whichever interpretation makes tests pass;
- **environment defect:** repair the deterministic harness or name the external blocker; do not suppress the test.

Allow at most two materially distinct safe recovery attempts for the same blocker without new evidence. Never normalize a red suite by deleting, skipping, broadening, or weakening assertions.

### Stage 7 — separate integrated verification

The parent runs verification independently from agent summaries on the final integrated HEAD:

- repository cleanliness and assigned/captured branch checks;
- compile/static checks for changed packages;
- focused backend store/workspace/API tests;
- focused runtime/tool/permission tests;
- focused Desktop tests;
- adversarial and fault-injection tests;
- all 59 deterministic E2E scenarios from clean disposable fixtures under the run-provided `TMPDIR`;
- repeat/race/order checks selected by the audit;
- complete repository test suites appropriate to the change;
- post-integration verification that the source checkout remains on the captured branch and unresolved dirty work was not hidden;
- bounded sanitized result artifact with exact commands, SHA, scenario manifest digest, failures, skips, retries, and durations.

No agent’s claimed pass replaces this run. Any unexpected skip, retry, scenario-count mismatch, dirty state, branch mismatch, or unverified negative control fails the gate.

### Stage 8 — human-recognizable build demonstration

On the exact verified build SHA, run the existing A/B/C/D demonstration and branch/worktree demonstration. Confirm:

- A/B/C metadata is visible without a workspace prompt;
- B is auto-managed while separate operation policy remains active;
- C prompts exactly once per session and survives hydration;
- D receives only session-scoped external-path approval;
- linked-folder/Add To Workspace UI is absent;
- Workspaces in use is observational;
- a request starting from a fixed `dev`, ordinary default, or manually selected feature branch returns integrated work to the branch that was actually captured;
- the source checkout never switches during delegated work or testbench deployment;
- ordinary session creation remains on the currently selected checkout/branch with worktree isolation off;
- a same-session workspace move reaches Desktop through a durable `session.workspace.updated` V3 event and route update, without polling;
- exact build SHA and pass/fail evidence are recorded.

Unit tests, coverage, agent consensus, and E2E alone do not replace this visible proof.

## Required evidence schema

Each test lane and the final verifier should emit bounded sanitized JSON containing:

```json
{
  "candidate_sha": "full commit SHA",
  "captured_branch": "exact branch or explicit non-branch state",
  "scenario_manifest_digest": "sha256",
  "requirements_covered": ["REQ-..."],
  "scenarios_run": ["E2E-..."],
  "commands": [{"argv": ["..."], "exit_code": 0, "duration_ms": 0}],
  "tests_passed": 0,
  "tests_failed": 0,
  "tests_skipped": 0,
  "retries": 0,
  "negative_controls": [{"cluster": "...", "red": true, "green": true}],
  "dirty_after": false,
  "unexpected_branch_change": false
}
```

Do not include host secrets, credentials, private paths, or unbounded logs.

## Updated acceptance checklist

- [ ] Production codegen is integrated before independent test jobs start.
- [ ] The candidate contract and exact HEAD are frozen before test authorship.
- [ ] Test authors run in parallel only on dependency-ready, non-overlapping scopes.
- [ ] Test agents cannot modify production code or redefine requirements.
- [ ] All 59 scenarios remain unique and mapped to assertions, including 13 branch/default/non-Git/legacy cases.
- [ ] Every high-risk cluster has a meaningful red-on-fault/green-on-candidate proof.
- [ ] A fresh-context reviewer audits tests for vacuity, mocks, skips, duplicates, and production-path reachability.
- [ ] Production defects and test defects use separate correction paths.
- [ ] The parent independently runs the final integrated suite from the exact candidate SHA.
- [ ] Separate deterministic E2E and human-recognizable demonstrations pass.
- [ ] The captured source branch remains unchanged; integration never assumes `main`.
- [ ] Repository cleanliness and unresolved-work preservation pass before handoff.

## Stop-ship conditions

Stop rather than claim completion if:

- tests were authored only by implementation agents;
- a high-risk test has no demonstrated failure mode;
- the suite passes with the guarded production behavior disabled;
- scenario IDs are missing, duplicated, or silently skipped;
- tests mock away the authority or transaction path they claim to cover;
- Require escalation or operation-policy checks are conflated;
- branch tests assume the default branch rather than the captured branch;
- source checkout branch/HEAD changes unexpectedly;
- agent summaries disagree with independently reproduced commands;
- failures are hidden through retries, sleeps, quarantine, deletion, or weakened assertions;
- the final integrated run or human demonstration has not been executed on the recorded SHA.

## Relevant filepaths

The test phase should focus on the paths already named by the implementation handoff, especially:

- `swarmd/internal/store/pebble/workspace_store.go`
- `swarmd/internal/workspace/service.go`
- `swarmd/internal/run/service_workspace_context.go`
- `swarmd/internal/run/service_workspace_scope.go`
- `swarmd/internal/run/provider_tool_invoker.go`
- `swarmd/internal/tool/workspace_scope_request.go`
- `swarmd/internal/permission/`
- `swarmd/internal/store/pebble/session_event_store.go`
- `swarmd/internal/api/sessions_v3_*`
- `swarmd/internal/api/desktop_bootstrap.go`
- `web/src/features/workspaces/`
- `web/src/features/desktop/permissions/`
- `web/src/features/desktop/state/`
- the checked-in flat-global-workspaces E2E diagnostic and scenario manifest under `scripts/`

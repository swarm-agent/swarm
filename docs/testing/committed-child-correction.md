# Correct a committed Coder child

This workflow is implemented by `tool.ResolveCommittedSource`, the regular `task`
parser/approval/execution path, and `manage-worktree recall`. It requires a runtime
built from this change; source-level validation does not establish installed support.
It is not workspace attachment, dirty-file recovery, or permission to promote work.

## Parent workflow

Run these tools from the **same authenticated parent session** that launched the
source child. The source must be a completed successful Coder with a surviving,
clean managed worktree, current owner/generation, and an exact recorded full HEAD.
The source repository must remain authorized in the current account catalog.
The original destination must also be clean for corrective allocation.

1. Ask `task` for its supported correction contract:

<copy label="task: correction help">{"action":"help","topic":"committed_source"}</copy>

2. Recall the original task call (replace the placeholder with its actual call ID).
   Omitting `task_call_id` recalls the parent's recorded waves when discovery is needed.

<copy label="manage-worktree: recall source">{"action":"recall","task_call_id":"ORIGINAL_TASK_CALL_ID"}</copy>

3. Select the intended row in `children`. Copy **only its returned
   `committed_source` object** into the launch below. Do not invent the tuple,
   shorten the OID, supply `committed_source_binding`, or use the sibling's private
   path as `workspace_path`. If no tuple is returned, inspect
   `source_eligibility_rejection_reason`; do not bypass it with a path grant.
   Replace the assignment and scope with the exact reviewed correction.

<copy label="task: isolated correction template">{
  "mode": "regular",
  "prompt": "Correct the selected committed child without changing its source or delivery destination.",
  "launches": [{
    "subagent_type": "coder",
    "title": "Correct Selected Child",
    "meta_prompt": "Fix the identified edge case in internal/example and add requirement-first regression tests. Commit the correction. Return proposed focused validation commands; tests not run, parent validation required.",
    "deliverable": "Committed correction and focused regression tests",
    "owned_scope": ["internal/example/**"],
    "committed_source": {
      "task_call_id": "COPY_RETURNED_TASK_CALL_ID",
      "child_session_id": "COPY_RETURNED_CHILD_SESSION_ID",
      "head_commit": "COPY_RETURNED_FULL_HEAD_COMMIT"
    }
  }]
}</copy>

This is a template, not a runnable reference. Use a new tool call for the correction;
`committed_source.task_call_id` identifies the **old** source wave, not the new call.
The single-Coder shorthand supports the same object at top level. With `launches`,
put it on each applicable Coder row. Never combine it with `workspace_path` (or its
aliases), `recovery_source_digest`, swarm mode, or Task Program job definitions.

## What stays separate

- **C: allocation base.** The new isolated checkout receives the complete committed
  tree at the selected child HEAD, including binary data, executable modes, and
  out-of-scope read prerequisites. `owned_scope` still limits new writes.
- **B: delivery base.** The inherited original base remains authenticated through
  the prior recorded source chain. A later separately authorized tool delivery uses
  B..H, including the original child work and correction; the new handoff is C..H.
- **Destination.** `owned_lane` remains the authenticated parent/retained/program
  lane. `captured_promotion_only` remains the original captured checkout and is
  excluded from recall's automatic `integrate_request`. Correction itself does not
  integrate, promote, push, or advance either destination. Do not deliver both the
  original and corrected stack as independent duplicates.

No source-root expansion occurs. An exact recorded source may be authenticated
through the current catalog even when it is absent from the parent's active roots.
This does not make arbitrary saved repositories or managed sibling paths generally
available to ordinary task selectors.

A surviving successful child of a completed Task Program can be selected through
its exact canonical job/run authority. Removed integrated program worktrees are not
recreated; use normal follow-on work from the already-integrated owned lane instead.
An active program or producer is not a committed-source correction candidate.

## Failures and evidence limits

Source identity is rechecked at approval/execution, before allocation, and before
parent publication. Foreign/stale/dirty/active sources fail closed. Parent launch
rows use V3 metadata mutation with bounded CAS retry; registration failure prevents
provider dispatch. If a child was already registered, errors preserve its inactive
ID/worktree. Inspect that child and recall before issuing another call. Permanent
publication failure can leave a child absent from the parent's launch rows; the
error's child ID is then the inspection reference. Same-call replay is rejected;
there is no automatic destructive cleanup or crash-atomic Git/store transaction.

Focused tests use synthetic temporary Git/Pebble state and fake providers. They
prove same/cross-repository allocation, full source content, inherited B/C separation,
store-close/reopen recall, secondary completed-program source identity, rejection
postconditions, and synthetic full-stack tool delivery. Store restart recall and
fresh corrective execution are separate fixtures; no complete daemon-restart,
provider-backed correction, UI delivery, or GCP E2E result is claimed. The original
incident source is neither a fixture nor changed by this workflow documentation.
See the atlas revision ledger and `test-audit-ledger.tsv` for exact execution and
digest evidence; independent P1/P2 audit remains pending.

# Native remix review and activation prerequisites

## Reviewable source and evidence

Implementation HEAD: `deb7bcf6b5b95fe5dc6a3542e642d7e5382461b2`.
Final review adds documentation only; no production activation is implied.

Reviewed paths: native Designer allocation and source-policy inheritance, runtime preview and exact draft recovery, direct revision preflight, store candidate/selection lineage, and Studio explicit selection. Important attack points remain stale head/turn CAS, malformed child lineage, live-producer takeover, non-Part policy override, forged profile snapshots and partial allocation failures. Input validation before allocation is not an atomic transaction across all runtime allocations.

Executed on this HEAD:

- From `swarmd`: `GOMAXPROCS=2 go test -p 1 ./internal/artifact ./internal/permission ./internal/run ./internal/tool ./internal/runtime ./internal/store/pebble -run 'Test(ArtifactV3|NativeHandoff|AnimationSnapshotAdmission)' -count=2 -timeout=120s` — passed all six packages.
- From `web`: `node --import tsx --test --test-timeout=30000 src/features/desktop/session-v3/native-artifact-selection.spec.ts` — four passed.
- `bash scripts/check-atlas-sync.sh` — passed for both the implementation commit range and final documentation working tree; `git diff --check` passed.
- Pinned-runtime TypeScript command could not start because the local pinned Node executable was absent. Diagnostic `node node_modules/typescript/bin/tsc -b --pretty false` passed under host Node v26.1.0; this does not satisfy the pinned-runtime build/release gate.

The store repeat fixture proves three siblings in focused → structural whole → focused rounds, exact selected parentage, unchanged original manifest/assets and stale selection rejection. Runtime recovery uses real Git/Pebble and a fake renderer; it proves retained source/history, terminal-child authority, rejection postconditions, old-handle invalidation, mandatory rebuild and unchanged successful sibling/head. Neither is a real-browser visual or provider-backed proof. Independent two-pass test audit remains pending; no tests were added to the curated critical runner.

No live test, pixel inspection, deployment or production recovery was performed in this final review. Prior checkpoint details were requested from canonical plan authority, but the response exceeded the available tool-output limit; their detailed live results could not be independently verified here. Do not infer a complete live journey or production resolution from checkpoint completion labels. Recover the canonical bounded prior handoff before relying on its provider/renderer evidence. This limitation is not evidence that earlier tests failed.

## Activation sequence — separate authorization required

1. Review this isolated implementation and documentation diff. Obtain exact approval for integration into the intended clean `dev` checkout; do not silently advance the captured checkout. Recheck source/target full HEADs and ownership immediately before integration.
2. Restore dependencies using the repository's pinned package/runtime contract, then run the required build/publication gates on the exact integrated revision. The host-Node diagnostic above is not a substitute. Approve the specific running-product deployment target and its restart effects separately; neither local tests nor testbench setup authorizes host production deployment or a push.
3. Verify the deployed candidate identity, authenticated local access and native tool/schema availability. If provider authentication is required, use supported account-scoped Desktop login; the user completes OAuth authorization. Never copy credentials from a test lane or request secret values in chat.
4. In the original owning session, refresh native source identity through authenticated `list_v3`/`source_v3` (or the canonical native API). Carry session/artifact/commit/projection together for Designer work, and session/artifact/revision together for native reads. Do not reuse historical transcript CAS, translate through legacy collection IDs, or use recovery as discovery. If the deployed tool schema lacks these actions, stop and resolve deployment/schema alignment.
5. Locate each intended retained failure by exact artifact/turn/candidate with read-only `draft_status_v3`. Obtain explicit permission for `resume_v3` using the fresh complete returned CAS object. The old producer must be terminal, the new producer active, and durable Designer lineage valid. Preserve all ready/unselected siblings, selected head and failed work. Never resume the newest candidate merely by timestamp.
6. Use only the returned renewed handle, inspect retained files and diagnostics, correct source, rebuild and finish only on a current ready gate. Inspect actual rendered pixels. Explicit candidate selection is a separate permissioned CAS operation; publication alone never selects a winner. Refresh source again before a repeat round.
7. Verify the original production acceptance criteria in that production session before resolving its blocked checkpoint. Local or isolated testbench passes do not resolve it. Historical profile snapshots retain their original budgets; an unsupported capability upgrade must fail rather than silently inherit today's larger allowance.

No promotion, push, production mutation, candidate deletion, head selection or retained production recovery is authorized or performed by this review.

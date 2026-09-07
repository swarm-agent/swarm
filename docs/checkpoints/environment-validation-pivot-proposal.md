# Launch-aware environment validation proposal

Status: deferred by user decision; continue the workspace repair with parent-owned validation. Not scheduled. The plan amendment was rejected because its
validator required the active checkpoint to be pending although the current
checkpoint is in progress. Do not change checkpoint status to bypass that guard.
Current workspace repair and its later checkpoints remain unchanged.

## Assessment

The environment branch has committed domain/persistence at
`e4d72798743d61285d3c470f6b73281e7b9c9ef1`, with substantial uncommitted adapter,
collector, scheduler, grant, model and credential-refresh work. Inspection observed
43 dirty Git entries. Recorded session evidence includes a successful real offline
multi-service fixture and focused race tests. These were not rerun during this
assessment and do not prove deployed operation.

Source inspection of daemon, API, tool and permission entrypoints found no
registered environment service/tool/routes or permission grant consumer. The
branch's `docs/environments.md` explicitly lists those gaps, protected Swarm
provisioning and deployed provider proofs as incomplete. The rootless adapter
currently limits commands to 25 seconds, immutable source archives to 8 MiB and
workloads to offline operation. This is substantial backend work, not a nearly
finished user-facing test service or a settings toggle.

Coder delegation failures in the current repair were caused by incorrectly
assigning command execution to Coders. Their no-Bash contract is intentional.
Parent execution already supports testing their retained work. Do not expand
agent permissions merely to repair that assignment error.

## Recommended execution order

1. **Finish current consumer repair.** Preserve the committed foundation
   `1a4f77cae08231f11fa06b0694a23c661a0cb1cf` and both retained dirty child
   worktrees. Parent reviews/tests exact trees, requests the appropriate
   permission-gated recovery commit/integration when suitable, and fixes
   provider restart, filesystem and Task Program gaps. Do not replay completed
   work or auto-commit failed children.
2. **Environment feasibility gate, only after approved scheduling.** Coordinate
   with the environment branch owner and select a stable reviewed snapshot.
   Parent reruns precise grant/command/collector/cleanup tests, one real isolated
   workload, and one actual focused repair test. Record exact source/toolchain,
   environment identity, result, output and unchanged host/sibling postconditions.
   Measure source size, dependencies and deadline compatibility. No broad dirty
   branch integration, OAuth changes, new UI or general shell access.
3. **Adopt or defer explicitly.** Adopt a minimal parent-owned named-validation
   path only if it works under existing enforced isolation and canonical
   authorization. Missing daemon/permission/API wiring is implementation work
   requiring its own reviewed scope, not permission to use a hidden executor.
   If the slice requires a broader platform change, defer it from this launch
   and use existing parent local/testbench validation. Do not weaken safety or
   claim the environment feature complete.
4. **Preserve the existing sidebar/header checkpoint.** Complete all workspace,
   parent-lane, worker and retained-history visibility and exact Git selectors.
5. **Preserve independent audit and exact-build live acceptance.** Rebuild the
   established testbench through its maintained persistent workflow when needed.
   Prove the supported Finder → two disjoint same-repository Coders → Designer
   program across multiple attachments, exact context/handoffs, recovery,
   unsupported-combination rejection and browser visibility.
6. **Launch verdict from evidence.** Report source-tested, running-build-tested
   and deployed identities separately. No launch-today assurance until all
   launch-critical gaps are closed.

## Environment execution boundary

A working-directory allowlist is not a sandbox: shell scripts can change
working directory, open absolute paths, spawn descendants or use the network.
Any future environment execution capability must bind exact principal/session/job,
environment generation, immutable source, named structured command and expiring
permission. Enforce filesystem, process, network, secrets, resource/output limits,
cancellation and stale/revoked-owner denial at the executor, with no host fallback.

This plan does not enable Coder Bash. A separately scoped environment command
capability would need its own approved system-agent/tool contract and negative
security evidence. The launch path retains parent-owned validation.

## Acceptance for the feasibility gate

- Stable revision and fresh test evidence, not transcript success alone.
- Actual repair-test compatibility demonstrated or explicitly measured as failing.
- Host/sibling isolation, quota/cancellation, stale/cross-account denial and
  retained failure-output checks pass.
- Existing dirty work, credentials, testbench lane and later repair requirements
  remain intact.
- Adoption/defer decision does not masquerade as complete environment deployment.

Relevant source paths on the environment branch:
`docs/environments.md`, `swarmd/internal/environment/{authorization,podman,
scheduler,executor}.go`, `swarmd/internal/environment/contract/`, and
`swarmd/internal/store/pebble/environment_store.go`. Integration owners remain
`swarmd/internal/runtime/`, `api/`, `permission/`, `tool/` and the code-owned agent
registry; they are not wired by the existence of the environment package.

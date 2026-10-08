# Headless application SDK: five-step implementation plan

Status: planned, not implemented or live-qualified by this document.

## Goal and scope

Anyone should be able to build an application using Swarm's real primitives: sessions, durable tasks, orchestration, `task`-tool delegation, revisioned application agents, typed tools, and supported image/media artifacts. The SDK should make those primitives easy to discover and manipulate without reverse-engineering Desktop, while the runtime enforces authority for critical workloads, including 100 agents working in parallel.

This is a durable product roadmap, not an instruction to launch work. There are **five implementation steps**, each with focused acceptance evidence—not a separate checkpoint for every test or feature. Saving this document does not start implementation, agents, infrastructure or deployment.

## Development loop: no production deployment per change

- Use fast, bounded non-live unit/schema/contract checks for changed interfaces and their failure cases. These checks may run locally; they do not prove real execution.
- Exercise actual daemon, model, delegation and media behavior in an isolated non-production GCP environment. Keep its identities, credentials, state and resources separate from production. Use approved bounded access and lifecycle management; reuse development resources only within those bounds.
- Rebuild/reload only affected components when compatible, recording exact SDK/runtime/source versions. Rerun affected acceptance tests during development, then the combined workflow at each step. Measure iteration time rather than promise an unverified speedup.
- Run complete release qualification at the final milestone, not after every SDK edit. Preserve production release safeguards. Publishing packages and deploying production remain separate decisions.
- Report real outcomes, errors and NOT_RUN cases. Mock contract fixtures, stored JSON and synthetic token counts are never evidence of agent execution or benchmark performance.

## Step 1 — Fix the foundations and prove sessions

Recheck the existing implementation before applying the reported session-list envelope/filter, task-reopen revision/retry, synthetic runner, object-pagination and ignored-persistence-error fixes. Unsupported helpers must fail clearly or be explicitly excluded from the supported execution surface; storage metadata must never masquerade as canonical execution.

Establish the incremental non-production development path and an SDK-only acceptance client. Complete session creation, listing, history, messaging, pending decisions, stop and reconnect using canonical V3 mutation/sync/realtime contracts. Add reusable principal/resource authorization for both requests and subscriptions from this first slice; never expose privileged daemon credentials to browsers.

**Done when:** the client creates and revisits sessions, streams a real response, resolves exact decisions, disconnects/reconnects and recovers retained history after restart without undocumented HTTP workarounds. Unauthorized reads, writes and subscriptions fail without side effects. Record a one-agent real execution baseline with complete telemetry, and the measured development iteration time. Report interrupted execution honestly rather than implying every run resumes automatically.

## Step 2 — Complete tasks, orchestration and delegation

Expose typed project/task creation, dependency plans, exact approvals, attempts, outputs, revision-guarded send-back and recovery. Reuse canonical lifecycle APIs and durable events; do not build a second executor or inject follow-up messages into task-linked sessions outside their lifecycle.

Distinguish durable project tasks from the agent `task` delegation tool. Support authorized specialist delegation with explicit source/workspace ownership, isolated writes, parent/child lineage, scoped grants, cancellation and bounded fan-out. Pin application-agent/context revisions to executions rather than assuming links propagate them. Keep built-in roles code-owned and model selection governed by canonical account policy. Worker/automation management retains Orchestrator authority and review boundaries.

**Done when:** an SDK-only application creates, approves and runs a dependent task workflow, retrieves exact outputs, requests changes and observes the next attempt. Duplicate submissions do not create unintended work; stale approvals and child privilege escalation are rejected. Parent/child outcomes reconcile after reconnect or restart. Capability discovery states which roles and invocation modes are actually supported.

## Step 3 — Make custom tools and image/media safe and usable

Define revisioned application-agent tool bindings with typed input/output schemas, authenticated invocation, resource grants, deadlines, bounded payloads, cancellation and secret references. Distinguish reads, writes and external effects. Provide a scoped read tool and a draft-creation tool as examples; do not make arbitrary shell/URL forwarding the default extension API.

Enforce authority server-side at invocation, not through prompt text. Bind approvals to the exact principal, tool revision, arguments and target. Children may inherit or narrow grants, never expand them. Persist stable retry identities, receipts and explicit uncertain outcomes; reconcile interrupted external effects instead of blindly repeating them. Define revocation behavior and acknowledge effects already in flight cannot always be undone.

Add capability discovery and supported attachment, generation, progress, cancellation and exact artifact retrieval/selection/download APIs. Unsupported media types or providers must return explicit errors. Apply ownership checks to artifact bytes as well as metadata and streams.

**Done when:** an application can inspect effective permissions, use both typed business tools, approve/deny an exact write, and create/retrieve supported real media artifacts through documented SDK methods. Negative tests reject unbound tools, altered approvals, untrusted instructions that request more authority and cross-user access. Lost responses do not cause blind duplicate writes; generated visual results are inspected before fidelity claims.

## Step 4 — Prove cloud durability and safe parallel execution

Use one daemon writer with durable cloud disk for live Pebble state and working files. Use object storage for explicit objects/exports and consistent backups, not as a shared live database. Define backup contents, integrity checks, restore ordering, compatible upgrades and failure handling. Test container restart, host replacement and a fresh backup restore, including approvals, agent/tool bindings, tasks, workspaces and artifact references. Test advertised object-storage providers separately; one provider's result does not certify all S3-compatible services.

Verify admission control, queues/backpressure, per-run and account budgets, per-tool rate limits, isolated writes or explicit shared-resource conflict handling, cancellation, revocation and per-agent attribution. Exercise provider throttling, duplicate delivery, partial failure and uncertain external effects before claiming scale readiness.

After the one-agent baseline, run **10, then 50, then 100 real agents simultaneously within each wave**. Each wave is bounded by predeclared budgets, timeouts and acceptance thresholds, and follows review of the previous wave. Respect account/delegation capacity and headroom; do not silently raise limits or substitute sequential runs. Capture real usage, client/event latency, durable receipts, denied actions and cleanup results.

**Done when:** verified restore preserves the expected state without replaying completed effects; storage failures cannot return false success. Every admitted parallel agent has attributable outcomes and enforced grants. Claim 100-agent readiness only after a real simultaneous 100-agent wave meets the agreed thresholds. Missing capacity or authorization means NOT_RUN, not a fabricated pass.

## Step 5 — Package the developer kit and qualify the complete journey

Provide a version-matched SDK/runtime starter, reusable authenticated backend, machine-readable capabilities/schemas, actionable errors and concise examples for sessions, task orchestration, delegation, custom tools and supported media. Document a private cloud installation, configuration, upgrade and recovery path using public-safe, portable instructions. Preserve configured models and fail clearly on missing configuration.

Run a clean-room developer journey against non-production infrastructure: start from an empty application, use documented APIs, execute real work, resolve decisions, retrieve outputs and restore state. Run the full release qualification suite on the exact milestone revision. Keep operational evidence and credentials outside public source; publish only reviewed, appropriately redacted results if separately authorized.

**Done when:** another developer can build the supported application without Desktop internals, hidden credentials or undocumented raw HTTP repairs. The combined workflow and safety/failure cases have revision-bound evidence, and every unsupported or untested capability is clearly stated. The measured incremental development loop remains available; production deployment is not required to repeat development tests.

## Boundaries and evidence

This plan does not certify existing deployments or claim its requirements are already implemented. Track implementation and observed evidence separately; source presence, passing contract fixtures and passing live qualification establish different things.

Defer multi-replica live-state sharing, scale-to-zero, a universal provisioning framework and autonomous external publication. A first business example stops at draft creation and human review. Never weaken permissions, isolation or release gates to meet a throughput target.

Relevant implementation surfaces: `packages/sdk/src/`, `packages/sdk/HEADLESS_UI.md`, `packages/sdk/APPLICATIONS.md`, `examples/headless-app/`, `examples/agent-hub/`, `containers/headless/`, and the canonical daemon API, agent and storage services under `swarmd/internal/`. Verify current contracts and repository rules before implementation.

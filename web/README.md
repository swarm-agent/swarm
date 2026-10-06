# Swarm Web

Workspace-first browser client for the new desktop launcher.

## Current scope

This app now provides the main launcher page:
- load saved workspaces from `swarmd`
- show the workspace grid on `/`
- let the user select the active workspace
- let the user choose a local default workspace for later launches

This is intentionally modular:
- workspace API lives under `src/features/workspaces/api.ts`
- launcher state lives under `src/features/workspaces/hooks/`
- UI components live under `src/features/workspaces/components/`
- page composition lives under `src/features/workspaces/pages/`

## Desktop launcher

Preferred local entrypoint:

```bash
cd /path/to/swarm-go
./bin/swarm --desktop
./bin/swarm dev --desktop
```

That launcher:
- ensures the lane backend is running first
- opens the browser automatically
- `./bin/swarm --desktop` opens the built desktop served by `swarmd`
- `./bin/swarm dev --desktop` starts the local Vite dev server against the dev lane backend and disables the backend desktop listener for that run so Vite owns the dev frontend port

## Dev

```bash
cd web
corepack pnpm install --frozen-lockfile
SWARM_BACKEND_URL=http://127.0.0.1:7781 SWARM_DESKTOP_PORT=5556 corepack pnpm run dev
```

Dependencies are managed with pnpm and the checked-in `pnpm-workspace.yaml` enables supply-chain hardening: a seven-day `minimumReleaseAge`, strict release-age enforcement, blocked exotic transitive dependencies, and an explicit build-script allowlist. Use the package scripts instead of calling `vite` directly.

Default Vite URL:
- main desktop: `http://127.0.0.1:5555`
- dev desktop: `http://127.0.0.1:5556`

## MVP Network Access

Direct private-LAN desktop access is not implemented safely yet. The desktop client warns when it is opened through a private LAN address because the backend desktop auth path is still local-first.

For another device, keep the Swarm host bound to `127.0.0.1` and use an SSH tunnel to the desktop port, for example `ssh -L 5555:127.0.0.1:5555 <host>`, or use Tailscale. Tailscale is usually the lower-friction secure option.

Expected local backend:
- main lane: `http://127.0.0.1:7781`
- dev lane: `http://127.0.0.1:7782`

The Vite dev server proxies `/v1`, `/v2`, `/v3`, `/healthz`, `/readyz`, `/desktop`, and `/ws` to the lane backend selected through `SWARM_BACKEND_URL`. The launcher also verifies the target page contains Vite's `/@vite/client` marker before it reports desktop dev mode as ready, so an unrelated HTTP listener on the same port cannot masquerade as the dev frontend.

## Repeatable Desktop subagent task E2E

Use this when validating that Desktop can ask the UI to launch two saved subagents, approve the permission modal, and capture V3 realtime/task logs from a real served Desktop URL:

```bash
cd web
node ./scripts/run-desktop-subagent-task-e2e.mjs https://example.invalid/swarm-go
```

The script writes `summary.json`, `browser-events.json`, `network.json`, `browser-console.json`, `dom-snapshot.txt`, and a screenshot into a temp evidence directory printed in the test output. The printed evidence directory is disposable and should not be copied into tracked repository paths. It fails unless Playwright observes `session.tool.started`, `session.tool.delta`, and two child session IDs in the task stream.

## Bounded deterministic Orchestrator seed suite

From `web`, with checked-in dependencies installed, Node 24, Bash and a temporary
scratch root configured via `TMPDIR`:

<copy label="Frontend validation">
corepack pnpm run test:orchestrator:selection
corepack pnpm run test:orchestrator
corepack pnpm run test:critical
</copy>

`test:orchestrator` validates the explicit seven-file manifest in
`scripts/orchestrator-test-suite.mjs`, then runs Node/tsx with concurrency 1,
15-second per-test timeouts and a 120-second process deadline. Missing/empty,
duplicate, non-file, symlinked, browser/live/e2e or out-of-scope selections fail
closed. It accepts both `.spec.ts` and `.spec.tsx`, without glob discovery:

- `orchestrate-commands.spec.ts`: current navigation allowlist and rejection of legacy launch commands;
- `orchestrate-plan-authority.spec.ts`: stale/unrelated plan snapshots cannot replace task definitions;
- `new-task-cta.spec.tsx`: existing New Task button markup/callbacks;
- `new-task-swarm-guidance.spec.tsx`: existing Swarm guidance and permission-settings link;
- `quiet-task-actions.spec.tsx`: approval disabled guards, source filters and selected-row actions;
- `task-reopen-operation.spec.ts`: missing authority, exact retry identity and mutation exclusion;
- `task-requirements.spec.tsx`: persisted review content, malformed review rejection and safe details rendering.

All seven live under `src/features/desktop/orchestrate`. This is a seed suite,
not full New Task coverage. Leaf JSX/SSR/source assertions do not prove browser
layout, real daemon authorization, provider execution or complete user journeys.
It is intentionally **not** added to the critical manifest: focused repeated
execution and independent first/second assertion reviews are required first.
Selection infrastructure has separate filesystem/CLI positive and negative tests.

### Retired cases and preserved compatibility

Legacy chat cases were retired from `task-command-routing.spec.ts` (four
original cases) and `desktop-v3-task-command.spec.ts` (four including `/flag`).
The missing-leading-workspace parser rejection was migrated into the existing
workspace compatibility suite; positive launch expectations were removed.
These files retain unselected retirement comments, not current-product tests.
`task-workspace-selection.spec.ts` retains the still-implemented compatibility
resolver's saved-name/ID, active binding, ambiguity, unavailable-source and path
rejections; it is not Orchestrator coverage. Existing critical V3 auth, hydration,
cache, realtime and new/existing session/write transport assertions stay selected.
Backend BackgroundRouterSessionStart and TUI/client `/task` security tests stay in
the critical gate with explicit compatibility labels.

The stale Go `TestProjectTask_BigSwarmUsesReadOnlyPlanning` was consolidated into
`TestProjectFeatureRoutingUsesSwarmAuto`: big Swarm remains Auto/pending approval
with zero run intents and a session-owned worktree, while small direct Coder admits
one run. Explicit Plan routing/lifecycle fixtures remain compatibility coverage.
Routing/refinement fixtures now supply their authorized source explicitly.

The seven old `/new`, `/task`, `/task plan` and Plan-to-Auto browser/provider cases
(and their two-case first-message branch) are retired. The old browser filename
now throws an explicit retirement error; `scripts/run-desktop-launch-test.sh`
retains its caller entrypoint but exits nonzero before network/provider work.
`scripts/run-testbench-desktop-e2e.sh` therefore also fails clearly rather than
reporting an empty passing suite. No replacement browser/provider journey exists.
Durability/permission/isolation assertions remain in deterministic owning suites;
the removed end-to-end evidence is a gap, not migrated browser coverage.

**Remaining qualification dependency:** `scripts/runners/task-routing.mjs` is
still externally referenced by swarmcrit and is intentionally unchanged. It is a
legacy qualification dependency, not proof of current Orchestrator/+ New Task.
A future coordinated runner and external-selector replacement is required before
retiring it. No live runner, GCP qualification or provider changes are made here.

### Focused backend validation

From `swarmd`, with the repository-supported Go toolchain and Git available:

<copy label="Backend validation">
go test -count=3 -timeout=120s ./internal/api -run '^Test(ProjectFeatureRoutingUsesSwarmAuto|OrchestratorStructuredRefinementKeepsCardPending|ProjectTask_ReconcilePlanningRun_PlanAuthored_TransitionsToPendingApproval|ProjectTask_ReconcilePlanningRun_Failure_TransitionsToFailed)$'
</copy>

Validation status for this cleanup: **not run; parent validation required**.
The parent should repeat the frontend seed/selection commands, inspect failures
against production authority, and review the exact committed tree. Do not weaken
current invariants or substitute this seed suite for browser/provider qualification.

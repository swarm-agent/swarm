# Swarm Atlas

> A top-down, evidence-indexed map of the current Swarm repository for agents and maintainers.

## 1. Purpose and use

Use this document to answer four questions in order:

1. **What product is this?** Read §§3–5.
2. **Where is the authority for a behavior?** Use §§2, 6, and 8.
3. **What crosses a security or durability boundary?** Use §§7 and 10.
4. **Which tests are relevant?** Use the evidence column in §8 and the method in §11. A listed test is evidence that a behavior was inspected or encoded; it is **not** proof of complete, correct, or currently passing coverage.

This atlas is an index, not a replacement for code. Resolve a question by following the cited path and symbol at the atlas revision. Do not infer implementation from a route name, test name, UI label, or this summary alone.

### Status labels

| Label | Meaning |
|---|---|
| **Verified implementation** | Inspected registration or implementation exists at the cited revision. |
| **Policy / contract** | Checked-in text or executable contract declares an invariant; implementation still needs inspection when changing it. |
| **Compatibility** | Registered or retained for migration/backward compatibility; do not add new consumers without an explicit decision. |
| **Experimental / dormant** | Present but not a dependable launch surface. |
| **Retired** | Explicitly outside current product scope. Do not restore incidentally. |
| **Unresolved** | Evidence was insufficient or internally inconsistent; verify before relying on it. |

## 2. Evidence, authority, and revision policy

### Authority hierarchy

For current behavior, prefer:

1. route and service wiring (`swarmd/internal/runtime/daemon.go`, `swarmd/internal/api/server.go`, `server_routes.go`);
2. implementation and storage code;
3. executable contract constants and focused tests;
4. `AGENTS.md` and operator/release scripts;
5. `README.md` and historical checkpoint documents;
6. this atlas.

Tests show intended or guarded behavior, but do not prove production behavior or that the suite passes. Finder reports, transcripts, generated inventories, and issue prose are discovery aids only.

### Atlas revision

- **Code base inspected:** `5bc65fe066abff78785fd573328c654d94b615fe`.
- **Inspection method:** static reads/searches only; no tests or product code were run.
- **Revision rule:** update this hash and the revision ledger whenever a material route, trust boundary, storage authority, client owner, agent/tool contract, or release gate changes. Re-check every affected citation against code; do not merely edit prose.
- **Line anchors are navigational:** symbols and paths are primary because line numbers drift.

## 3. Mission, goals, boundaries

### Current mission and goals

**Policy / contract.** Swarm is a local-first AI coding workspace composed of an installed launcher, a daemon, a tcell TUI, and a React Desktop UI. Launch priorities are reliable local operation, durable V3 sessions, explicit permissions, authenticated local access, workspace/worktree isolation, provider-neutral orchestration, and portable system installation (`README.md:5-22`; `AGENTS.md`, “Launch Product Scope”).

Current architectural goals visible in code are:

- one durable V3 session authority and atomic mutation path (`session.Service.ApplySessionMutation` in `swarmd/internal/session/session_events.go`; `SessionStore.ApplyV3SessionMutation` in `swarmd/internal/store/pebble/session_event_store.go`);
- sync-first cross-client correctness, with realtime as an accelerator (`V3SyncContractText` in `swarmd/internal/api/sessions_v3_sync_contract.go`);
- code-owned system agents and account-scoped model selection (`BuiltinSystemAgentRegistry` in `swarmd/internal/agent/system_agent_registry.go`; `agentmodelsettings.Service` in `swarmd/internal/agentmodelsettings/service.go`);
- loopback defaults and authenticated non-health access (`startupconfig.Default`; `Server.withAuth`);
- bounded, reviewable workspace execution and isolated delegated Git work (`worktree.Service.AllocateTaskWorkspace`, `PrepareTaskIntegration`, `ApplyTaskIntegration` in `swarmd/internal/worktree/service.go`).

### Non-goals and retired boundaries

**Policy / contract.** `AGENTS.md` is explicit:

- dedicated local-container execution is **retired**;
- a general-purpose Flow product/runtime is **retired**; Workspace Actions are the supported reusable execution surface;
- user-authored custom agents are not a launch product authority; remaining mutable v2 agent/profile APIs are compatibility debt;
- hosted control planes, managed synchronization, remote deployment, and retired runner route-mirroring are not current contracts.

**Experimental / dormant.** Copilot implementation remains in source but is deliberately not registered as a provider or runner (`swarmd/internal/runtime/daemon.go:450-471`). Voice is described as experimental in `README.md:125-137`.

## 4. System context and primary flows

```text
swarm launcher / swarmtui                 React Desktop
 cmd/swarm, internal/app                  web/src/features/desktop
           | authenticated HTTP/WS / trusted owner IPC |
           +-------------------+------------------------+
                               v
                     swarmd API and boundaries
       api.Server: desktop origin -> auth -> vault -> JSON -> routes
                               |
             +-----------------+------------------+
             |                                    |
       V3 session authority                 runtime/tool layer
 session.Service -> Pebble mutation     run.Service -> provider registry
 events/projections/run intents/outbox   permissions -> tools/worktrees
             |                                    |
             +---------- durable state -----------+
                         Pebble + artifact Git
```

### Request-to-session flow

1. The launcher/TUI or Desktop reaches a daemon listener. Defaults are API `127.0.0.1:7781`, Desktop `127.0.0.1:5555`, and peer transport `127.0.0.1:7791` (`pkg/startupconfig/config.go:18-25,135-156`).
2. Desktop requests additionally cross `Server.withDesktopBoundary`; all API surfaces are assembled by `Server.apiMux`, then wrapped by `withAuth`, `withVaultGate`, and `withJSON` (`swarmd/internal/api/server.go:697-735`).
3. A V3 mutation enters `session.Service.ApplySessionMutation`, which delegates to `SessionStore.ApplyV3SessionMutation` (`swarmd/internal/session/session_events.go:84-88`; `swarmd/internal/store/pebble/session_event_store.go:609`).
4. The atomic Pebble mutation updates event/projection state and emits a durable realtime outbox record. The contract requires replay—not the in-memory hub—to repair missed delivery (`V3SyncContractText`, lines 33-60).
5. Desktop or TUI receives a snapshot through bootstrap/hydrate, replay through sync stream, and live frames through `/v3/realtime/stream` (`sessions_v3_sync_contract.go`; `sessions_v3_realtime_contract.go`).

### AI turn and delegation flow

1. `run.Service` resolves the compiled agent, account-scoped model settings, workspace, prompt, tool contract, and permissions (`swarmd/internal/run/service*.go`; daemon wiring at `runtime/daemon.go:474-480`).
2. The provider registry selects an adapter/runner. Registered adapters are Anthropic, Codex, Fireworks, Google, OpenAI, OpenRouter, and Exa; runners exclude Exa and dormant Copilot (`runtime/daemon.go:455-471`).
3. Tool calls pass through runtime policy and workspace/path constraints (`swarmd/internal/tool/`, `swarmd/internal/permission/`, `swarmd/internal/run/service_tools.go`).
4. Coder delegation allocates a child branch/worktree from an immutable base and returns a commit for explicit integration (`worktree.Service.ResolveTaskBase`, `AllocateTaskWorkspace`, `PrepareTaskIntegration`, `ApplyTaskIntegration`). Other agent output contracts are compiled in `system_agent_registry.go`.

## 5. Repository and component map

| Area | Canonical paths | Responsibility |
|---|---|---|
| Launcher/install | `cmd/swarm/`, `cmd/swarmsetup/`, `internal/launcher/` | service lifecycle, installed paths, release apply/update, controller launch |
| TUI | `cmd/swarmtui/`, `internal/app/`, `internal/ui/`, `internal/client/` | tcell application, V3 client/realtime controller, settings and commands |
| Daemon entry/runtime | `swarmd/cmd/swarmd/`, `swarmd/internal/runtime/daemon.go` | dependency wiring, listeners, background services, provider registration |
| HTTP/WS API | `swarmd/internal/api/` | route registration, auth/desktop boundaries, handlers, V3 sync/realtime |
| Durable state | `swarmd/internal/store/pebble/`, `swarmd/internal/session/` | events, projections, run intents, plans, identity, settings, indexes |
| AI runtime | `swarmd/internal/run/`, `agent/`, `agentmodelsettings/`, `provider/` | prompt/tool policy, system agents, model authority, provider adapters |
| Workspace execution | `workspace/`, `worktree/`, `action/`, `discovery/`, `gitstatus/` | bindings, containment, isolated Git work, actions, skills/rules, status |
| Artifacts/media | `artifact/`, `artifactgit/`, `mediastaging/`, `imagegen/`, `htmlcapture/`, `videosource/`, `videoproject/`, `videorender/`, `videotranscription/`, `voice/` | immutable outputs, ingress, generation, deterministic rendering and media workflows |
| Desktop | `web/src/features/desktop/` | browser UI, V3 cache/runtime/realtime ownership, workspace and media UI |
| Operational evidence | `scripts/`, `tests/`, `swarmd/tests/`, `.github/workflows/` | static gates, opt-in tests, E2E/testbench and release workflows |

## 6. Canonical data, storage, networking, and trust model

### Storage

**Verified implementation.** `storagecontract.defaultRoot` sets Linux roots to `/var/lib/swarmd`, `/var/cache/swarmd`, `/run/swarmd`, `/etc/swarmd`, and `/var/log/swarmd`; Darwin roots are under system `/Library` plus `/var/run/swarmd` (`pkg/storagecontract/storagecontract.go:197-228`). `startupconfig.ResolvePath` joins `swarm.conf` under the config root and uses mode `0600` (`pkg/startupconfig/config.go:27-28,127-133`). `storagecontract.Join` rejects root escape (`storagecontract.go:180-185`).

**Verified implementation.** Session correctness state is Pebble-backed. Material records include session events, projections, run intents, messages, media references, and realtime outbox records (`V3SessionRunIntent`, `V3RealtimeOutboxRecord`, and `ApplyV3SessionMutation` in `session_event_store.go`). Managed artifacts use private bare Git repositories with isolated Git environment/config; `artifactgit.Repository` disables external config, prompts, hooks, signing, and file protocols (`swarmd/internal/artifactgit/repository.go:82,98`). Default artifact limits are 64 MiB per blob, 256 MiB composition, 256 parts, and 4096 refs (`artifactgit/types.go:24-43`).

### Network and authentication

- `Daemon.Run` binds configured API/Desktop/peer TCP listeners and a Unix socket; the owner IPC directory/socket are permission-restricted (`swarmd/internal/runtime/daemon.go:826-917`).
- `Server.Handler` applies auth and vault gates; `DesktopHandler` also applies local-session and browser boundary middleware (`api/server.go:710-735`).
- `Server.withAuth` accepts a validated identity session, a narrowly validated artifact-preview request, trusted local transport, or a valid attach token. Otherwise it returns 401 (`api/server.go:3922-3970`).
- Auth exemptions are narrowly enumerated for health/readiness, eligible Desktop bootstrap, initial onboarding, and admitted/pending Tailscale origin approval (`Server.isAuthExemptRequest`, `api/server.go:3984-3998`).
- Browser-origin checks compare admitted/local origin, referer, host/scheme, and `Sec-Fetch-Site` (`isSameOriginBrowserRequest`, `api/server.go:4001-4040`; `api/desktop_boundary.go`).
- A locked vault gates non-vault routes (**policy visible in `Server.Handler`; inspect `Server.withVaultGate` in `api/vault.go` before changing exceptions**).

### Principal and scope

The durable scope is account + user/principal + workspace/session. Sync cursors additionally bind surface, stream kind, selector hash, resource set, and scan position (`V3SyncScopeFields`, `V3SyncCursorPayloadFields`). Handlers must derive identity from request context; callers must not supply authority-bearing lineage or filesystem paths unchecked (`swarmd/internal/identity/`; artifact `Principal` in `artifact/authority.go:39`).

## 7. Detailed subsystem maps

### V3 sessions, plans, sync, and realtime

- **Authority:** `session.Service.ApplySessionMutation` -> `SessionStore.ApplyV3SessionMutation`.
- **Atomic products:** event, projection, run intent/state, message/resources, media/search indexes as applicable, and durable outbox. Commit uses synchronous Pebble batches in `session_event_store.go`.
- **Reads/sync:** `handleSessionsV3SyncBootstrap`, `handleSessionsV3SyncHydrate`, `handleSessionsV3SyncStream`; live WS is `handleV3RealtimeStream`.
- **Protocol:** `v3.realtime`, version 1 (`sessions_v3_realtime_contract.go:13-38`). Cursors are signed/scoped/opaque by contract; clients must not parse or compare cursor numbers (`V3SyncContractText`).
- **Plans:** lifecycle services are in `session/plan_lifecycle_service.go`, `api/sessions_v3_plan_lifecycle.go`, and `run/plan_lifecycle_v3.go`; plan/checkpoint writes must use the session mutation boundary.
- **Recovery:** `api/sessions_v3_executor.go`, `sessions_v3_stale_recovery.go`, and durable run-intent records recover or terminalize interrupted work.
- **Compatibility:** `/v3/sessions:workset` and `/v3/tui/sessions:workset` remain removal-gated by `V3SyncWorksetRemovalGates`. `/v1/sessions*` and `/ws` are legacy surfaces, not the canonical V3 chat path.

### Agents, models, providers, prompts, and tools

- **System agents:** immutable definitions and tool contracts live in `agent/system_agent_registry.go`. `NewSystemAgentRegistry` rejects duplicate/invalid definitions, delegation-enabled subagents, and primary agents without `plan_auto` (`lines 69-110`).
- **Current compiled identities:** Swarm, Plan/AI sidechats, Compact, Finder, Coder, Designer, Image, Idea, AI Task Preparer, Review Commit, Workspace Definition, and Router (`system_agent_registry.go:13-39` and `builtinSystemAgentDefinitions`).
- **Model authority:** account-scoped `agentmodelsettings.Service`; canonical route constant `AgentModelSettingsPath = /v1/agent-model-settings` (`api/swarm_mode_settings.go:14-16`).
- **Provider boundary:** generic orchestration stays in `run/`; wire/auth/stream translation belongs in `provider/*`. Registration truth is `runtime/daemon.go:455-471`, not directory presence.
- **Prompt assembly:** inspect `run/service_prompt.go`, `service_workspace_scope.go`, `checkpoint_prompt.go`, and `run_state_prompt.go`; runtime and durable state injections override stale transcript claims.
- **Tools/permissions:** implementation is under `tool/`, `run/service_tools.go`, and `permission/`. The compiled Coder contract disables bash, delegation, session/plan/settings mutations, and enables dedicated Git tools (`CoderAgentToolContract`, `system_agent_registry.go:446-457`). Runtime profiles can narrow capabilities further.

### Workspaces, worktrees, actions, discovery, Git

- Workspace records/bindings are Pebble-backed (`store/pebble/workspace_store.go`). Path containment is a security boundary; inspect `workspace/` and `worktree.pathWithinRoot` before accepting a path.
- `worktree.Service.AllocateTaskWorkspace` owns delegated checkout isolation; integration validates child path, branch, base, and head (`worktree/service.go`).
- Workspace Action definition CRUD (`action.Service.Create/Update/Delete/Reorder`) is separate from execution (`action.Runner`; `api/workspace_actions.go` and `workspace_action_runs.go`). Entrypoints resolve relative to the real workspace and arguments are structured arrays (`action/runner.go:100-104,288-341`).
- Rules/skills are discovered in `discovery.Service`; they guide agents but cannot override runtime policy.
- Git status/watch is in `gitstatus/` and `gitwatch/`; authenticated API mutation surfaces include status, commit, suggestion, realtime, and worktree management.

### Desktop and TUI ownership

- **Desktop:** backend-derived V3 state is owned by `web/src/features/desktop/state/desktop-v3-cache-store.ts` and `desktop-v3-cache-reducer.ts`; realtime orchestration is in `realtime/v3-realtime-controller.ts`; runtime composition is under `runtime/`. Components should consume selectors/actions rather than create another transport cache.
- **TUI:** `internal/app/app.go` owns app lifecycle. `tuiRealtimeController` in `internal/app/tui_realtime_controller.go` reconciles V3 subscriptions/worksets and resume cursor; `internal/client/` implements daemon calls.
- Both surfaces must repair gaps from durable sync. UI-local state and in-memory hubs are not session authority.

### Artifacts, media, image, HTML, video, voice

- Managed artifact authority and lineage are in `artifact/authority*.go`; bytes/manifests are in private `artifactgit` repositories. Workspace materialization must pass destination containment (`artifact/materialize.go`).
- Pre-session media ingress is `mediastaging.Service`: put/read/delete/expire, bounded `CleanupExpired`, and atomic binding (`mediastaging/service.go:50-196`).
- Image generation is `imagegen.Service`. `ManagedImageCapabilities` issues a capability snapshot/token and generation validates it; managed parallelism is bounded to 4, Gemini slots to 10 (`imagegen/service.go:35-36,197-252,568`; `gemini.go:322`).
- HTML still capture enforces `swarm.capture/v1` and 1920x1080 (`htmlcapture/renderer.go:36,268-285`). Animation enforces `swarm.animation/v1` and deterministic ready/seek behavior (`htmlcapture/animation.go`). These are executable-content boundaries, not ordinary static files.
- Video sources use opaque references (`videosource/`); projects/proposals/revisions use `videoproject.Service`; rendering is `videorender/`; transcription/analysis is `videotranscription/`. `AcceptEditProposal` is a distinct operation from proposal creation (`videoproject/service.go:123,258`).
- Voice/STT is in `voice/` and provider adapters. Treat local model path resolution and uploaded audio as untrusted input.

## 8. API catalog

### How to read this catalog

All listed routes are registered by the eight `register*Routes` methods called by `Server.apiMux` (`api/server_routes.go`; `api/server.go:697-707`). Unless an exemption below applies, they inherit auth + vault + JSON middleware; Desktop access additionally inherits local-session and browser-origin boundaries. Methods are enforced inside handlers, so inspect the handler before changing or calling a route. “Tests” means related inspected files, not a coverage assertion.

| Route family / registrations | Handler/service and primary consumers | Scope / lifecycle | Related inspected tests |
|---|---|---|---|
| `/healthz`, `/readyz` | `handleHealth`, `handleReady`; launcher/health supervision | auth-exempt; **current** | `api/server_json_test.go`, `api/desktop_unbootstrapped_test.go` |
| `/ws` | `handleDesktopStream`; legacy stream clients | authenticated; **compatibility** | `api/run_stream_ws.go`, `sessions_v3_realtime_contract_test.go` |
| `/v1/auth/codex`, `/v1/auth/codex/oauth/{start,status,complete}` | `handleCodexAuth`, OAuth handlers; onboarding/settings | account secret; **current** | `api/codex_oauth.go`, `provider/codex/oauth_device_test.go` |
| `/v1/auth/credentials{,/verify,/active,/delete}` | credential handlers and auth store; onboarding/settings/provider picker | account secret; **current, critical** | `api/auth_cleanup_test.go`, `auth_v3_realtime_test.go`, `onboarding_provider_test.go` |
| `/v1/auth/desktop/session`, `/v1/auth/attach/rotate` | local browser bootstrap / daemon attachment | first has narrow GET exemption; **current, critical** | `desktop_local_auth_jwt_test.go`, `protected_identity_guard_test.go` |
| `/v1/vault{,/enable,/unlock,/lock,/disable,/export,/import}` | `handleVault*`; Desktop vault settings | account secret/crypto; **current, critical** | `local_auth_substrate_test.go`, `api/vault.go` |
| `/me`, `/v1/me`; `/v1/onboarding`; Tailscale approval; `/v1/onboarding/provider/credential`; `/v1/account/*` | identity/onboarding/account handlers; setup and settings | narrow onboarding exemptions; **current** | `onboarding_security_test.go`, `desktop_boundary_test.go`, `account_username_test.go` |
| `/v1/agent-model-settings` | `handleAgentModelSettings` -> account model settings; Desktop/TUI model controls | account-scoped canonical authority; **current** | `swarm_mode_settings_test.go`, `agentmodelsettings/service_test.go` |
| `/v1/swarm/targets`, `/target/{current,select}`, `/groups*`, `/topology*` | target/group/topology handlers; routing and topology UI | account/workspace; **current generic target contract** | `swarm_targets_test.go`, `topology_account_route_test.go`, `topology_workspace_bindings_test.go` |
| `/v2/custom-tools*`, `/v2/agents*` | `handleCustomToolsV2`, `handleAgentsV2`; legacy profile consumers | account; **compatibility/deprecated authority** | `agent_v2_contract_test.go`, `agent_v2_summary_test.go`, `agent_v2_visibility_test.go` |
| `/v1/codex/account/{usage,reset-credits,reset-credits/consume}` | Codex account handlers; usage/settings | account/provider; **current** | `codex_usage_test.go`, provider Codex tests |
| `/v1/image/{providers,generations,assets,storage/reveal}`, `/v1/media/settings/catalog` | image/media handlers -> `imagegen.Service`; Desktop media tools | account/workspace; external generation is permission-sensitive; **current** | `image_generation_test.go`, `image_threads_test.go`, `media_settings_test.go`, `media_reveal_test.go` |
| `/v1/model`, `/model/catalog{,/check}`, `/model-profiles*`, `/models/favorites*`, `/providers` | model preference/catalog/profile handlers; model controls | account; **current** | `model_profiles_test.go`, `agentmodel/resolver_test.go` |
| `/v1/stt/transcribe`, `/v1/voice/*` | STT/voice handlers -> `voice.Service`; TUI/Desktop voice | account/device/media; **experimental product surface** | `voice/*_test.go`, provider transcription security tests |
| `/v1/ui/settings`, `/v1/tailscale*` constants | UI/Tailscale handlers; Desktop/TUI settings | account/network; **current** | `ui_settings_test.go`, `tailscale_settings_test.go`, `desktop_boundary_test.go` |
| `/v1/workspace/{resolve,select,current,list,overview,cwd/resolve,discover,browse}` | workspace handlers -> workspace store/service; launcher/Desktop/TUI | account + contained path; **current, critical** | `workspace_cwd_resolve_test.go`, `workspace_add_self_binding_test.go`, `workspace/path_containment_security_test.go` |
| `/v1/workspace/{folders/create,add,directories/*,source-media/*,theme,icon,rename,move,delete}` | workspace mutation handlers; workspace settings/sidebar | account + workspace generation/path; **current** | workspace/api focused tests; `workspace/source_media_directories_test.go` |
| `/v1/workspace/todos`, `/skills/delete`, definition refresh | todo/discovery handlers; workspace panels/runtime prompt preparation | account + workspace; **current** | `workspace_todos_test.go`, `workspace_definition_refresh_test.go`, `discovery/*_test.go` |
| `/v1/workspace/actions`, `/actions/run`, `/actions/runs{,/cancel}` | definition service vs action runner; Actions UI | account/workspace; execution is a separate user gesture; **current, execution-critical** | `workspace_actions_test.go`, `action/*_test.go` |
| `/v1/workspace/git/{status,commit,commit/suggestion,realtime}`, `/v1/worktrees`, `/v1/manage-worktree` | Git/worktree handlers; Desktop Git and delegation integration | contained repository + principal; **current, VCS-critical** | `git_commit_test.go`, `git_realtime_test.go`, `worktrees_test.go`, `worktree/*_test.go` |
| `/v1/workspace/video/{scan,transcribe*,storage/reveal,projects*,threads*}`, `/audio/{transcribe*,analysis/read}`, `/image/threads*` | media/video/image thread handlers; Desktop media studios | account + workspace + opaque source refs; **current** | `audio_transcription_test.go`, `video_security_test.go`, `video_studio_integration_test.go`, `image_threads_test.go` |
| `/v1/context/sources`, `/v1/permissions*` | context and permission handlers; runtime controls | account/session; **current, critical** | `permissions_*_test.go`, tool policy tests |
| `/v1/system/shutdown`, `/v1/update/{status,apply,run}` | daemon lifecycle/update handlers; launcher/Desktop | authenticated local admin; **current, operationally critical** | `update_test.go`, `update_helper_launch_test.go` |
| `/v1/alerts*`, `/v1/notifications*`, web-push prefix | notification handlers/realtime resources; Desktop | account/user; **current** (`alerts` appears alias-like; inspect before removal) | `webpush_test.go`, notifications store/API tests |
| session maintenance/dump/long-diagnostics constants | maintenance/diagnostic handlers; developer/operator workflows | authenticated; dump/diagnostics config-gated and privacy-critical; **current bounded operations** | `session_storage_maintenance_test.go`, `session_dump_test.go`, `long_session_diagnostics_test.go` |
| `/v1/integrations*` | integration handlers; integration workspace UI | account/workspace; **current** | `integrations_test.go`, `integration_sessions_test.go` |
| media staging constant and item suffix | `handleMediaStagingCollection/Item` -> `mediastaging.Service`; composer uploads | account + TTL; **current** | `media_staging_test.go`, `mediastaging/service_test.go` |
| routed/background session constants | `handleRoutedSessionStart`, `handleBackgroundRouterSessionStart`; Desktop/router orchestration | account/workspace + V3 session mutation; **current** | `routed_sessions_*_test.go`, `background_router_sessions_test.go` |
| `/v3/sync/{bootstrap,hydrate,stream}` | sync handlers; Desktop/TUI recovery and hydration | signed exact scope; **current canonical sync** despite stale “future” comments | `sessions_v3_sync_*_test.go`, contract test |
| `/v3/realtime/stream` | `handleV3RealtimeStream`; Desktop/TUI live/replay client | `v3.realtime` v1, signed scoped cursor; **current canonical live transport** | `sessions_v3_realtime_*_test.go` |
| `/v3/sessions` and `/v3/sessions/` | primary collection/item dispatchers; Desktop/TUI/session API | account/user/workspace + mutation authority; **current canonical session API** | `sessions_v3_primary_test.go`, resources/model/plan/media tests |
| `/v3/sessions:{reconnect,discover,search,archive,review-worktrees,unarchive,delete}`, `/v3/subagents:stop` | specialized V3 lifecycle handlers; clients and task UI | account/session; **current** | matching `sessions_v3_*_test.go` files |
| `/v3/artifacts` | session artifact handler; Desktop artifact gallery/tools | principal/session lineage; **current** | `sessions_v3_artifacts_test.go`, artifact contract tests |
| `/v3/sessions:workset`, `/v3/tui/sessions:workset`, `/v3/tui/sessions*` | legacy workset/TUI handlers | **compatibility; explicit removal gates** | `sessions_v3_sync_contract_test.go`, `sessions_v3_workset_test.go`, `sessions_v3_tui_test.go` |
| `/v1/sessions`, `/v1/sessions/` | legacy handlers | **compatibility; not V3 rendering authority** | legacy API tests; V3 contract guards |

### Consumer verification rule

Before deleting or changing a route, search exact route constants/strings in `web/src`, `internal/app`, `internal/client`, scripts, and tests. Registration proves exposure; it does not prove a current production consumer. In particular, the checked-in sync contract itself says legacy workset removal requires static guards, runtime logs, cursor/snapshot/outbox tests, and live cross-client E2E (`V3SyncWorksetRemovalGates`).

## 9. Operational and release gates

- `scripts/check-precommit.sh`: formatting/static policy, path/secret/privacy/dependency checks; tests remain opt-in (`README.md:201-208`; script source).
- `scripts/check-prepush.sh`: pre-push gate; do not bypass for protected branches.
- `scripts/check-launch-readiness.sh --require-clean`: composes launch defaults with cleanliness/public-repo checks (`scripts/check-launch-readiness.sh`).
- `scripts/run-testbench-launch-prerun.sh`: canonical bounded live suite manifest for onboarding, Desktop, TUI, plan-auto, task routing, Task Programs, and provider sync/realtime (`--list-suites` is the manifest authority).
- `docs/main-deploy-checklist.md` and release workflows are the publication authority when present at the target revision.

These gates are commands, not claims that the repository currently passes. This atlas creation ran none of them by task constraint.

## 10. Security- and durability-critical matrix

| Critical area | Invariant / likely attack point | Authority | High-value test evidence to inspect |
|---|---|---|---|
| Listener exposure | normal API/Desktop defaults remain loopback; remote origin admission is explicit | `startupconfig.Default`, `runtime.Daemon.Run`, `api/desktop_boundary.go` | `desktop_boundary_test.go`, startup config tests |
| Authentication/bootstrap | exemptions cannot widen; attach/session tokens and preview tickets cannot cross scope | `Server.withAuth`, `isAuthExemptRequest`, `desktop_local_auth.go` | `desktop_local_auth_jwt_test.go`, `protected_identity_guard_test.go`, onboarding security tests |
| Vault/secrets | locked-state and account-secret boundaries fail closed; logs/errors do not leak credentials | `api/vault.go`, `store/pebble/auth_vault.go`, privacy package | `local_auth_substrate_test.go`, `panic_privacy_test.go`, auth cleanup tests |
| Session atomicity | no persistent V3 mutation bypasses the canonical boundary; event/projection/outbox stay atomic/idempotent | `session_events.go`, `session_event_store.go` | `session_events_test.go`, `session_event_store_test.go`, checkpoint/routed atomicity tests |
| Sync/realtime | cursors are signed/scoped/opaque; races cannot fall between snapshot and replay; hub loss repairs durably | `V3SyncContractText`, sync/realtime handlers | sync strict/contract/cursor tests, realtime strict/WS tests |
| Run recovery/plans | durable intents/checkpoint attempts do not duplicate, vanish, or falsely succeed after restart | `sessions_v3_executor.go`, stale recovery, plan lifecycle services | executor idempotency, durable progress, stale recovery, plan invariant tests |
| Account/principal isolation | every read/write filters account and user; supplied IDs cannot widen authority | identity context + store selectors/mutation checks | topology account, protected identity, session access/scope tests |
| Workspace containment | traversal, symlink, linked-root, and materialization paths cannot escape approved roots | `storagecontract.Join`, workspace/worktree/action/artifact path resolvers | workspace/discovery containment, artifact promotion security, sparse worktree tests |
| Command/action execution | definitions do not execute; entrypoints are relative/contained; argv is structured; permission decision is explicit | `action.Service`, `action.Runner`, API run handlers, permission/tool runtime | action tests, permissions/bash-profile tests, tool runtime tests |
| Delegated Git integration | child base/head/branch/path and clean commit lineage are verified before integration | `worktree.Service` integration methods | worktree service/sparse tests, routed worktree integration tests |
| Provider boundary | credentials are account-scoped; adapters cannot bypass generic tool/session policy; dormant providers stay unregistered | `runtime/daemon.go`, `provider/registry`, provider adapters | provider runner/auth tests, system agent runtime-contract tests |
| Managed artifacts | immutable lineage, Git config isolation, blob/part limits, and contained materialization | `artifact/authority*.go`, `artifactgit/repository.go` | artifact Git authority, repository, promotion security tests |
| Media/image ingress | MIME/digest/size/capability/TTL checks precede durable binding or external generation | `mediastaging`, `imagegen`, session media store | media staging, image generation/service, session media tests |
| HTML execution/render | manifest/runtime contract, network/file isolation, viewport stability, bounded frames/output | `htmlcapture/renderer.go`, `animation.go` | renderer/animation tests; trusted export tool tests |
| Video proposals/render | AI proposals cannot silently become accepted revisions/final renders; source refs/timing remain exact | `videoproject.Service`, `videorender`, manage-video tool runtime | video security/integration, project store, render service/builder tests |
| Updates/releases | update cannot activate unverified/unsafe artifacts or hide rollback failure | launcher/update API/scripts | update helper/apply tests, precommit/readiness scripts |

## 11. Test topology and mapping guidance

Tests exist across multiple ecosystems: Go `_test.go` files in the root and `swarmd` modules, TypeScript/React `*.spec.*` files under `web`, shell/static harnesses under `tests` and `scripts`, and live E2E/testbench runners. Benchmarks and contract/static-source tests answer different questions from behavior tests.

For a critical behavior, build a **requirement-first map**:

1. state the invariant and threat/failure mode;
2. cite the production registration, boundary, service, and store symbols;
3. list direct tests, contract/static guards, integration tests, and live gates separately;
4. inspect assertions and fixtures—not names—to record what each test actually proves;
5. record missing negative, concurrency, restart, cross-account, traversal, and failure-injection cases;
6. run only the separately approved, narrow validation plan later.

Never use test count as coverage. Never infer “covered,” “secure,” or “passing” from file presence. A route-to-test entry in §8 means “start inspection here.”

## 12. Known uncertainties and fact-check notes

1. `sessions_v3_sync_contract.go` still calls bootstrap/hydrate/stream “future canonical,” while routes and clients are implemented and the repository contract calls them canonical. Treat the routes as current implementation and the wording as stale until intentionally reconciled.
2. `/ws`, `/v1/sessions*`, v2 agent/custom-tool routes, and V3 workset/TUI routes are registered, but registration alone does not identify all active consumers. Follow the consumer verification rule before removal.
3. The provider registry includes OpenAI in addition to the README’s named support list; Copilot is deliberately dormant. Use runtime registration, not README or directory names, for availability.
4. This atlas statically inspected representative handlers, registrations, contracts, authorities, and tests. It did not exhaustively prove each handler’s accepted HTTP methods or every client caller. Handler-level method tables should be generated only from inspected switch logic, not guessed from REST naming.
5. No test was run, so no statement here asserts passing coverage or runtime health.

### Independent fact-check performed for this revision

The synthesis was checked back against fresh repository evidence rather than accepting domain reports: all route families were reconciled to `server_routes.go`; middleware order and exemptions to `server.go`; listener/provider claims to `runtime/daemon.go`; V3 durability/sync claims to session/store and executable contract files; agent identities/tool limits to `system_agent_registry.go`; and media/artifact claims to their service/authority implementations. Unsupported specifics (notably exhaustive HTTP methods, universal consumers, and coverage quality) were omitted or marked unresolved.

## 13. Glossary

- **Attach token:** daemon access token accepted by authenticated local API middleware.
- **Artifact:** immutable, lineage-bearing managed output stored under session authority; not synonymous with a workspace file.
- **Checkpoint / attempt:** durable unit and execution attempt inside a session plan.
- **Endpoint cursor:** opaque signed position within one exact sync scope; not a client-comparable integer.
- **Execution epoch:** bounded provider-context interval within a durable session.
- **Live patch:** ephemeral streaming text/reasoning acceleration; durable messages/events remain authority.
- **Outbox:** Pebble-backed ordered records used for durable replay and realtime fanout.
- **Principal:** authenticated user/account authority injected into request/runtime context.
- **Projection:** current durable session view derived and committed with events.
- **Run intent:** durable record of requested/running/terminal execution used for recovery.
- **System agent:** immutable code-owned identity and tool/security contract.
- **Target:** generic Swarm routing/topology destination; not a retired container profile.
- **Workspace Action:** account-owned, workspace-scoped structured executable definition; definition management is not execution.
- **Worktree:** isolated Git checkout/branch used for a session or delegated Coder scope.

## 14. Revision ledger and update template

| Atlas revision | Code revision | Date (UTC) | Scope | Evidence / decision |
|---|---|---|---|---|
| 1 | `5bc65fe066abff78785fd573328c654d94b615fe` | 2026-08-27 | Initial top-down atlas, API families, security/durability map, test guidance | Static independent fact-check; no tests run |

Copy this row when updating:

```text
| N | `<commit>` | YYYY-MM-DD | `<changed domains/routes/invariants>` | `<paths/symbols inspected; tests or gates actually run; unresolved items>` |
```

Update checklist:

- [ ] Base commit recorded and worktree clean before inspection.
- [ ] Changed routes reconciled to `Server.apiMux` registrations and handler method logic.
- [ ] Changed clients searched in Desktop, TUI, scripts, and tests.
- [ ] Storage/mutation and auth/scope boundaries re-read.
- [ ] Current/compatibility/experimental/retired labels revalidated.
- [ ] Relevant tests inspected by assertions; no coverage claim inferred.
- [ ] Validation actually run is listed; skipped validation is explicit.
- [ ] Known uncertainties updated rather than silently removed.

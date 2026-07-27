# Section C Implementation Prompt — Agents, Custom Tools, MCP, Model, Providers, Image, Voice

You are the coding agent assigned to implement Section C of the Swarm-Go user-first identity cutover. Do not treat any existing API/storage object in this section as already converted. Current state is global, path-derived, provider-keyed, thread-ID-keyed, or otherwise not product-owned.

## Product identity model and nonnegotiables

- `UserID` is the primary actor identity.
- `TeamID` is the sharing container.
- `WorkspaceID` is required for workspace-backed state.
- Username is product identity; daemon/swarm names are device identity only.
- `swarmName`, `swarmID`, socket token, API token, path, session ID, thread ID, deployment ID, provider ID, model ID, profile name, agent name, MCP server ID, and voice device ID are not ownership.
- No fallback IDs, guessed ownership, silent adoption of legacy/global records, dual authoritative paths, path-derived identity, or route-guard-only conversions.
- A route is not converted until its backing records, keys, indexes, service APIs, event subjects/entities, and tests enforce the same ownership scope.
- Until a resource family is converted, mutating routes must hard-refuse under product identity mode rather than continuing to mutate global records.

## Section scope and routes

Registered routes are in `swarmd/internal/api/server_routes.go`:

- Agents/custom tools: `/v2/custom-tools`, `/v2/custom-tools/`, `/v2/agents`, `/v2/agents/defaults/restore`, `/v2/agents/defaults/reset`, `/v2/agents/`.
- Image/model/providers/voice: `/v1/image/providers`, `/v1/image/generations`, `/v1/image/assets`, `/v1/image/storage/reveal`, `/v1/model`, `/v1/model/catalog`, `/v1/models/favorites`, `/v1/models/favorites/delete`, `/v1/providers`, `/v1/stt/transcribe`, `/v1/voice/status`, `/v1/voice/profiles`, `/v1/voice/profiles/upsert`, `/v1/voice/profiles/delete`, `/v1/voice/config`, `/v1/voice/devices`, `/v1/voice/test-stt`.

Backend storage also includes MCP server config with no direct route in `server_routes.go` today. It is still identity-sensitive because runtime/tool resolution can consume it.

## Current reality verified from source

Evidence anchors:

- `swarmd/internal/api/server_routes.go:137-164` registers the Section C routes.
- `swarmd/internal/store/pebble/keys.go:19-37` defines global keys for voice, model, MCP, agent profiles, custom tools, active agents, and agent version.
- `swarmd/internal/api/agent_v2.go:14-537` handlers call `s.agents` with only names/limits/payloads; no `UserID`/`TeamID`/`WorkspaceID` is passed.
- `swarmd/internal/store/pebble/agent_store.go:301-478` stores profiles/tools by `agent/profile/{name}` and `agent/custom_tool/{name}`, active selection by `agent/active/primary` and `agent/active/subagent/{purpose}`, and version by `agent/version`.
- `swarmd/internal/agent/service.go:129-222` `EnsureDefaults` writes default profiles, active primary, active subagents, and version globally.
- `swarmd/internal/api/server.go:1171-1203` `/v1/model` reads/writes global model preference.
- `swarmd/internal/store/pebble/model_store.go:28-72` persists `model_pref/global/default`.
- `swarmd/internal/api/server.go:1279-1369` favorites handlers call global favorites service methods.
- `swarmd/internal/store/pebble/model_favorite_store.go:28-140` stores favorites by provider/model under `model_favorite/...` with no owner.
- `swarmd/internal/store/pebble/model_catalog_store.go:41-158` stores catalog records and metadata globally under `model_catalog/...`.
- `swarmd/internal/store/pebble/mcp_store.go:43-251` stores MCP servers by `mcp/server/{serverID}` with no owner.
- `swarmd/internal/store/pebble/voice_store.go:55-195` stores voice config/profile records globally under `voice/config/default` and `voice/profile/{profileID}`.
- `swarmd/internal/api/server.go:3180-3633` voice/STT handlers call voice service with request payload only; no actor/scope is passed.
- `swarmd/internal/imagegen/service.go:163-207` provider readiness checks global auth store credentials; this leaks/uses global credential state until Auth is converted.
- `swarmd/internal/imagegen/service.go:356-388` image generation opens an image thread by `thread_id` and resolves storage from `thread.WorkspacePath`.
- `swarmd/internal/store/pebble/image_thread_store.go:21-32` image threads contain `WorkspacePath` but no `UserID`/`TeamID`/`WorkspaceID`.

## Final missed-risk check

Before coding, re-check these risks and split commits if needed:

1. Agent defaults are boot-time global writes. `EnsureDefaults`, restore, reset, active primary/subagent, version, and built-in reconciliation must not silently create mutable user/team state outside the authenticated actor.
2. Agent profiles and custom tools include prompt/tool contracts/commands. Treat shared tools as team-owned and personal selections as user-owned. Do not let a user list/mutate another team’s tool commands.
3. Active primary/subagent selections are user preferences, not team shared state.
4. Agent profiles may be shared team resources or immutable system defaults plus scoped overrides. Decide explicitly per record type; do not keep name-only global authority.
5. Integration-builder hidden/default agent paths may need system-default handling, not arbitrary user/team mutation.
6. MCP records can include command/env/headers. They must be classified as user-personal or team-shared before being executable/visible. Secrets must remain in the auth/vault section, not plaintext config where avoidable.
7. `/v1/providers` and `/v1/image/providers` are read-like but currently expose readiness based on global credentials. After Auth conversion, readiness must be actor-scoped or return only system catalog capability without credential state.
8. `/v1/model/catalog` can remain system-global only if read-only and free of user-owned state. Catalog refresh/background writes must be explicitly system-owned, not user mutable.
9. `/v1/model` and favorites are mutable personal preferences and must be `UserID` scoped.
10. Voice config/profiles and selected devices are personal user preferences. Device enumeration is host/system read-only, but selected device/config/profile must be `UserID` scoped.
11. STT/transcribe/test routes use provider credentials via adapters. They must resolve credentials/config for the authenticated `UserID`; no global credential reads.
12. Image generation and assets are workspace-backed. They must validate `UserID + TeamID + WorkspaceID` ownership/membership before reading thread/asset path or writing files. Thread ID alone is not authority.
13. Image files on disk must be under canonical workspace-owned storage and must be reachable only after DB ownership validation. Do not authorize by path or asset ID alone.
14. Cross-section dependency: workspace image thread records are also Section B. Do not mark image generation/assets converted until the thread backing records contain/validate `WorkspaceID` and membership.
15. Cross-section dependency: provider credentials/auth are Section A. Do not mark provider readiness, image generation, STT, or model-provider actions converted until credential lookup is user/team scoped.
16. Cross-section dependency: deploy/container sync of agents/model defaults lives outside this section. Any sync/apply path must stop copying global agent/model records until Section E validates scope.

## Required storage and Pebble key changes

Use final key names consistent with the repository’s identity-key conventions when those exist. The shape below is the expected ownership model; exact helper names may vary.

### Agents

Current keys:

- `agent/profile/{name}`
- `agent/custom_tool/{name}`
- `agent/active/primary`
- `agent/active/subagent/{purpose}`
- `agent/version`

Required model:

- Immutable system defaults may be stored as explicit system records, e.g. `agent/system/profile/{name}` or equivalent, and must not be user-mutable.
- Team-shared profiles: key by `TeamID + agent name`, e.g. `team/{teamID}/agent/profile/{name}`. Record must contain `TeamID`, `CreatedByUserID`, `UpdatedByUserID`, timestamps, and normalized name.
- Personal/user override profiles, if supported: key by `UserID + agent name`, e.g. `user/{userID}/agent/profile/{name}`. Record must contain `UserID` and must not shadow team records silently; precedence must be explicit.
- Active primary: key by `UserID`, e.g. `user/{userID}/agent/active/primary`, containing `UserID`, selected profile reference, and selected scope (`system`, `team`, or `user`).
- Active subagents: key by `UserID + purpose`, e.g. `user/{userID}/agent/active/subagent/{purpose}`, containing `UserID`, purpose, selected profile reference, and selected scope.
- Version/revision: either rebuildable/system metadata or scoped by owning container, e.g. `team/{teamID}/agent/version` and `user/{userID}/agent/selection_version`. It cannot remain a mutable global ownerless value.
- Index/list operations must iterate only the actor’s accessible system + team + user records, never global prefix scans.

### Custom tools

Current key:

- `agent/custom_tool/{name}`

Required model:

- Team-shared custom tools: `team/{teamID}/agent/custom_tool/{name}` with `TeamID`, `CreatedByUserID`, `UpdatedByUserID`, normalized name, type, command, and timestamps.
- Personal custom tools, if supported: `user/{userID}/agent/custom_tool/{name}` with `UserID`.
- Agent-tool assignment must store references with explicit owner/scope. A team agent cannot reference a private user tool unless the product intentionally supports per-user overlay and validates it at runtime.
- All custom tool command execution paths must resolve through the authenticated actor and team membership.

### MCP

Current key:

- `mcp/server/{serverID}`

Required model:

- Classify each MCP server as team-shared or user-personal before enabling it.
- Team-shared MCP: `team/{teamID}/mcp/server/{serverID}` with `TeamID`, creator/updater UserID, enabled flag, transport, URL/command metadata, and timestamps.
- User-personal MCP: `user/{userID}/mcp/server/{serverID}` with `UserID`.
- Do not preserve global mutable MCP records. Defaults may be explicit system defaults only if they carry no user secret/config and are not mutable through user APIs.
- MCP env/headers/secrets require a vault/auth-owned reference model, not accidental shared plaintext, unless the existing design explicitly permits non-secret public config.

### Model preferences and favorites

Current keys:

- `model_pref/global/default`
- `model_favorite/{provider}/{model}` or equivalent under `model_favorite/`
- Catalog: `model_catalog/meta`, `model_catalog/{provider}/{model}`

Required model:

- Model preference: `user/{userID}/model_pref/default` with `UserID`, provider, model, thinking, service tier/context mode, and timestamps.
- Model favorites: `user/{userID}/model_favorite/{provider}/{model}` with `UserID`, provider, model, label, thinking, timestamps.
- Model catalog/cache may remain global/system only if routes are read-only for users and the records contain no user-owned preference or credential state.
- Events currently use `system:model` and `global`; update preference/favorite events to include `UserID`/actor-scoped entity IDs. Catalog events, if any, remain system events.

### Providers and image providers

Current behavior:

- `/v1/providers` lists provider status through provider services.
- `/v1/image/providers` reports readiness using global auth store methods such as Codex auth and active Google credential.

Required model:

- Provider catalogs/capabilities can be global read-only only if they do not include user credential readiness.
- Credential readiness must be computed for the authenticated `UserID` and any selected team/workspace context required by Auth.
- If credential conversion is not complete, provider readiness routes must hard-refuse credential-aware status or return only credential-free static capabilities.

### Voice

Current keys:

- `voice/config/default`
- `voice/profile/{profileID}`

Required model:

- Voice config: `user/{userID}/voice/config/default` with `UserID`, selected profiles/providers/models/language/device, and timestamps.
- Voice profiles: `user/{userID}/voice/profile/{profileID}` with `UserID`, label, adapter, model/language/voice/options, timestamps.
- Voice profile list/get/update/delete must be user-scoped.
- Device list is host/system enumeration but selected device is user preference.
- STT/transcribe/test must resolve profile/config/credentials for the authenticated `UserID`; audio payloads must not be persisted globally unless separately scoped and specified.

### Image generation and assets

Current keys/storage:

- `image/thread/{threadID}` with `WorkspacePath` and asset paths, no product owner.
- Disk storage resolved through workspace path and thread ID.

Required model:

- Image thread records must contain `WorkspaceID`, `TeamID`, and creator/owner `UserID` or otherwise reference a workspace record that proves those values.
- Image generation request must include/resolve a target `WorkspaceID` and thread ID; validate authenticated UserID membership in TeamID and access to WorkspaceID before provider call, disk write, or DB update.
- Asset records remain inside the scoped thread or get their own scoped key/index; asset read must validate thread/workspace ownership before opening a file.
- Disk path is metadata only. Do not authorize by path. Do not derive ownership from path.

## API/service changes expected

1. Add/require an actor context object at handler boundary for this section: `UserID`, active `TeamID`, and maybe selected `WorkspaceID` where route requires it.
2. Change service method signatures to accept explicit scope, not infer it from request path/name/provider/thread ID.
3. Change Pebble store methods to require scoped key constructors. Avoid overloaded methods that silently fall back to legacy keys.
4. Remove or quarantine legacy global key reads/writes for converted routes. If migration tooling is needed, keep it explicit and non-serving.
5. Update event log subjects/entities for mutable user/team state.
6. Update tests so unauthenticated/no-bootstrap calls fail, cross-user and cross-team access fails, and converted routes persist across restart under scoped keys.
7. Keep static provider/model catalogs system-global only where read-only and credential-free.

## Hard-refuse rules until conversion

Until the backing records are scoped and tests pass, the implementation must hard-refuse these operations under identity mode:

- `/v2/agents` list if it would list global mutable profiles/tools as user/team state.
- `/v2/agents/defaults/restore` and `/v2/agents/defaults/reset` until defaults are explicit system/team/user scoped.
- `/v2/agents/{name}` get/put/delete against global names.
- `/v2/agents/active/primary` and active subagent mutations until user-scoped selections exist.
- `/v2/agents/{name}/custom-tools/{tool}` assignment until both profile and tool ownership are validated.
- `/v2/custom-tools` and `/v2/custom-tools/{name}` if using global custom tool namespace.
- `/v1/model` POST until preference is UserID-scoped.
- `/v1/models/favorites` POST and `/v1/models/favorites/delete` until favorites are UserID-scoped. GET must not return global favorites as user favorites.
- Credential-aware `/v1/providers` and `/v1/image/providers` readiness until Auth credential lookup is user/team scoped; static catalog-only response is acceptable if clearly credential-free.
- `/v1/image/generations` and `/v1/image/assets` until thread/workspace ownership is validated by UserID + TeamID + WorkspaceID.
- `/v1/voice/profiles/upsert`, `/v1/voice/profiles/delete`, `/v1/voice/config`, `/v1/stt/transcribe`, and `/v1/voice/test-stt` until voice config/profile/credential resolution is UserID-scoped.

## Phased commit/checkpoint order

Split this section. Do not combine all of Section C into one risky commit.

### Commit C1 — Agents/custom tools storage and hard-refuse gates

- Add scoped key helpers and record fields for system defaults, team/user agent profiles, custom tools, active selections, and revisions.
- Convert agent store/service APIs to require scope.
- Convert list/get/upsert/delete/assign/activate/default restore/reset behavior.
- Hard-refuse any route path still touching `agent/profile/`, `agent/custom_tool/`, `agent/active/*`, or `agent/version` as mutable product state.
- Tests: no-user fail, user A cannot see user/team B tools, team membership required, restart persists scoped selections.

### Commit C2 — MCP scoped storage and runtime inclusion

- Convert MCP store APIs to require user/team scope.
- Classify default vs user vs team MCP records.
- Ensure runtime/tool resolution only includes MCP servers authorized for actor scope.
- Tests: user/team isolation, no global MCP mutation, restart persistence, no secret leakage in docs/logs.

### Commit C3 — Model preferences/favorites and provider status

- Convert `/v1/model` and favorites to UserID-scoped storage/events.
- Keep catalog global read-only, and prove it has no user-owned mutations.
- Convert credential-aware provider readiness to actor-scoped Auth lookups or hard-refuse until Auth dependency is ready.
- Tests: user A/B different defaults/favorites; catalog shared read-only; restart persistence; credential readiness does not leak another user.

### Commit C4 — Voice config/profiles/STT

- Convert voice config/profile store/service/API to UserID scope.
- Convert STT/transcribe/test to actor-scoped config + credential resolution.
- Keep device list host read-only but selected device user-scoped.
- Tests: user A/B voice isolation, profile deletion clears only that user’s active refs, restart persistence, no global config key writes.

### Commit C5 — Image generation/assets

- Coordinate with Section B workspace/thread conversion. Do not proceed until image threads carry or validate `WorkspaceID + TeamID + UserID`.
- Convert image generation/assets to validate actor membership before provider call, disk write, DB update, or file open.
- Convert provider credential lookup for image generation to actor-scoped Auth.
- Tests: no-user fail, wrong user/team/workspace fail before provider call, valid user writes asset and persists after restart, asset read cross-user fails.

Checkpoint after each commit. If a fresh-VM gate fails, stop and do not proceed to the next commit.

## Fresh-VM testing requirements

For every commit/checkpoint, run a fresh-VM proof using a clean data directory and capture artifacts. Do not rely only on unit tests.

Required VM flow per checkpoint:

1. Start from an empty product data directory on a fresh VM or equivalent clean environment.
2. Bootstrap product identity with username only through the approved identity flow.
3. Confirm guarded Section C routes fail before bootstrap/session and succeed only after authenticated UserID context where applicable.
4. Create at least two users and two team contexts where the feature is team-shareable.
5. Exercise successful scoped CRUD for the converted resource family.
6. Exercise negative cross-user/cross-team/cross-workspace reads and mutations.
7. Restart the daemon.
8. Re-run list/get/readiness/selection checks to prove persistence and no fallback to global legacy keys.
9. Inspect/capture DB key evidence or debug output proving new scoped keys are used and old global keys are not mutated by converted routes.
10. Capture HTTP request/response logs, daemon logs, DB key evidence, and restart proof in the checkpoint artifacts.

Pass/fail criteria:

- PASS only if scoped records contain the required ownership fields and converted APIs enforce them before service/store mutation.
- PASS only if cross-user/team access fails with clear authorization errors.
- PASS only if restart preserves scoped data and does not recreate/mutate global legacy records for converted APIs.
- FAIL if any route reads/writes legacy global keys as authoritative product state.
- FAIL if path/name/provider/thread/device/session/token is used as ownership.
- FAIL if tests pass only because a fallback silently adopts legacy/global data.

## Active plan/checkpoint guidance

Before coding each commit, update the active implementation plan with the exact sub-scope, backing keys, routes, hard-refuse list, and VM proof commands/artifacts for that commit. After each commit and VM gate, checkpoint with:

- routes converted,
- storage keys changed,
- legacy keys refused/unused,
- tests run,
- VM artifacts captured,
- remaining hard-refuse routes/dependencies.

Do not update the plan to claim Section C is converted until all Section C commits pass their fresh-VM gates and cross-section dependencies are resolved.

## Relevant filepaths

- `swarmd/internal/api/server_routes.go` — route registration for Section C.
- `swarmd/internal/api/agent_v2.go` — agent/custom tool handlers that currently pass no actor scope.
- `swarmd/internal/agent/service.go` — agent defaults, active selections, versioning, and CRUD service behavior.
- `swarmd/internal/store/pebble/agent_store.go` — global agent/custom-tool/active/version storage.
- `swarmd/internal/store/pebble/mcp_store.go` — global MCP server storage with sensitive config fields.
- `swarmd/internal/api/server.go` — model, provider, STT, voice, and UI/image-related handlers.
- `swarmd/internal/model/service.go` — global model preference/favorite service and events.
- `swarmd/internal/store/pebble/model_store.go` — global model preference key.
- `swarmd/internal/store/pebble/model_favorite_store.go` — global model favorite keys.
- `swarmd/internal/store/pebble/model_catalog_store.go` — system/global model catalog cache.
- `swarmd/internal/imagegen/service.go` — image provider readiness, credential lookup, image generation, image thread/disk writes.
- `swarmd/internal/store/pebble/image_thread_store.go` — image thread records with workspace path but no product ownership.
- `swarmd/internal/voice/service.go` — voice profile/config/STT service behavior.
- `swarmd/internal/store/pebble/voice_store.go` — global voice config/profile storage.
- `swarmd/internal/store/pebble/keys.go` — current legacy key constants and future scoped-key insertion point.

## Open questions / dependencies

- Exact canonical identity key prefix helpers must align with Section A identity records once implemented.
- Exact `WorkspaceID` validation APIs depend on Section B workspace conversion.
- Exact credential readiness APIs depend on Section A Auth/Vault conversion.
- Any remaining deployment sync/apply routes for agents/model defaults are outside this section and must not become an alternate authority for identity-scoped state.

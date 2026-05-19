# Section A Implementation Prompt — Identity/Auth/Onboarding/Vault/Bootstrap

You are the next coding agent for Section A of the Swarm-Go user-first identity cutover. Work only from backend source evidence. Do not assume any existing API data object is already UserID/TeamID-owned.

## Mission

Implement the identity foundation and convert or hard-refuse Section A routes so product ownership is explicit and persisted.

Product model:
- `UserID` is the primary product actor.
- `TeamID` is a sharing container, not the primary actor.
- `username` is product identity and is required for bootstrap/login.
- `swarmName`, `swarmID`, socket trust, attach token, session token, path, deployment ID, and peer transport are not ownership.
- Converted APIs must have backing records, keys, and indexes converted to canonical ownership or must hard-refuse.

## Section scope and routes

Route registration is in `swarmd/internal/api/server_routes.go`:
- Core: `/healthz`, `/readyz`, `/ws`
- Auth/Codex/Credentials: `/v1/auth/codex`, `/v1/auth/codex/oauth/start`, `/v1/auth/codex/oauth/status`, `/v1/auth/codex/oauth/complete`, `/v1/auth/credentials`, `/v1/auth/credentials/verify`, `/v1/auth/credentials/active`, `/v1/auth/credentials/delete`, `/v1/auth/desktop/session`, `/v1/auth/attach/rotate`
- Vault: `/v1/vault`, `/v1/vault/enable`, `/v1/vault/unlock`, `/v1/vault/lock`, `/v1/vault/disable`, `/v1/vault/export`, `/v1/vault/import`
- Onboarding: `/v1/onboarding`

Current route evidence:
- `registerAuthVaultRoutes` maps Section A auth/vault handlers at `swarmd/internal/api/server_routes.go:11-29`.
- `registerOnboardingRoutes` maps `/v1/onboarding` at `swarmd/internal/api/server_routes.go:31-33`.
- `Handler`, `LocalTransportHandler`, and `DesktopHandler` wrap the same API mux with auth/vault/local-session middleware at `swarmd/internal/api/server.go:551-588`.

## Current reality to fix

### There is no product identity bootstrap yet

There is no canonical persisted `User`, `Team`, `TeamMembership`, or `CurrentSelection` store for API ownership. Existing `UserID`/`TeamID` strings in other packages are not a complete product identity system and must not be treated as this cutover being done.

### Current auth gates are transport/session gates, not product ownership

`withAuth` currently allows or denies by loopback/trusted-network exemptions, desktop local session token, local transport marker, swarm peer auth, bootstrap invite token, or attach token. It does not establish canonical product `UserID`/`TeamID` ownership for handlers. Evidence: `swarmd/internal/api/server.go:3646-3711`.

Exemptions currently include health/readiness, GET `/v1/auth/desktop/session`, GET `/v1/onboarding`, some update/discovery/deploy sync routes, and trusted-network deploy sync paths. Evidence: `swarmd/internal/api/server.go:3786-3808`.

Desktop local session is in-memory and token-only. It issues a cookie named `swarm_desktop_session`, validates the token, and does not bind to a product user. Evidence: `swarmd/internal/api/desktop_local_auth.go:28-75`, `swarmd/internal/api/desktop_local_auth.go:191-208`.

### Onboarding is daemon setup, not product identity

`/v1/onboarding` reads and writes startup config (`swarm_name`, network mode, ports, tailscale URL, etc.) and checks vault/credential/workspace counts. It does not create a product user or team. Evidence: `swarmd/internal/api/onboarding.go:254-279`, `swarmd/internal/api/onboarding.go:403-540`.

It can persist swarm name into UI settings via `persistUISwarmName`, but that is daemon/UI metadata, not username identity. Evidence: `swarmd/internal/api/onboarding.go:521-529`, `swarmd/internal/api/onboarding.go:568-578`.

### Credentials/Codex are global/provider scoped

Current credential records contain provider, credential ID, type, label, tags, secret material, managed flag, and `OwnerSwarmID`; they do not contain `UserID` or `TeamID`. Evidence: `swarmd/internal/store/pebble/auth_store.go:25-41`.

Current keys are provider/credential scoped, not user scoped:
- `auth/credential/{provider}/{credentialID}` via `KeyAuthCredential` at `swarmd/internal/store/pebble/keys.go:246-248`
- `auth/credential_active/{provider}` via `KeyAuthCredentialActive` at `swarmd/internal/store/pebble/keys.go:262-264`
- `auth/index/auth_tag/{tag}/{provider}/{credentialID}` via `KeyAuthCredentialTag` at `swarmd/internal/store/pebble/keys.go:306-308`

Credential handlers call auth service methods without actor context. Evidence:
- `/v1/auth/credentials` GET/POST: `swarmd/internal/api/server.go:943-1022`
- `/v1/auth/credentials/verify`: `swarmd/internal/api/server.go:1024-1064`
- `/v1/auth/credentials/active`: `swarmd/internal/api/server.go:1066-1105`
- `/v1/auth/credentials/delete`: `swarmd/internal/api/server.go:1107-1149`

Auth service methods call `AuthStore` without actor arguments. Evidence: `swarmd/internal/auth/service.go:304-439`.

Codex is stored as the active `codex` credential and has legacy migration from `auth/codex/default`; neither path is user-owned. Evidence:
- handler: `swarmd/internal/api/server.go:879-941`
- store defaults: `swarmd/internal/store/pebble/auth_store.go:106-178`
- legacy migration: `swarmd/internal/store/pebble/auth_store.go:595-625`

### Vault is global

Vault metadata is one global key, not user/team scoped:
- `auth/vault/meta` at `swarmd/internal/store/pebble/keys.go:12`
- `VaultMetadata` has no UserID/TeamID fields at `swarmd/internal/store/pebble/auth_vault.go:36-50`
- vault enable rewrites all credentials and writes global metadata at `swarmd/internal/store/pebble/auth_vault.go:83-170`

Vault handlers call global auth service methods. Evidence: `swarmd/internal/api/vault.go:11-186`.

`withVaultGate` gates the whole API on the global vault lock state and exempts vault/auth/deploy sync paths. Evidence: `swarmd/internal/api/vault.go:188-218`.

### Managed vault keys and attach token are not ownership

Managed vault keys are keyed only by arbitrary scope ID: `auth/managed_vault_key/{scopeID}`. Evidence: `swarmd/internal/store/pebble/keys.go:16`, `swarmd/internal/store/pebble/auth_managed_vault_key.go:25-77`.

Attach auth is one daemon-global token at `auth/attach/default`. Evidence: `swarmd/internal/store/pebble/client_auth_store.go:10-14`, `swarmd/internal/store/pebble/client_auth_store.go:35-57`. Rotating it is not a product identity action today. Evidence: `swarmd/internal/api/server.go:1151-1169`, `swarmd/internal/security/service.go:61-87`.

## Required storage/Pebble key changes

Do not add compatibility reads as a second authoritative path. Either convert records/keys in the checkpoint or hard-refuse the API until the conversion lands.

### Identity foundation keys

Create a canonical identity store before converting credentials/vault:
- `identity/user/{userID}`: canonical user record with `UserID`, normalized unique `username`, created/updated timestamps, status.
- `identity/user_by_username/{normalizedUsername}`: unique username index to `UserID`.
- `identity/team/{teamID}`: hidden default personal team record created at bootstrap; later can support named teams.
- `identity/team_member/{teamID}/{userID}` and/or `identity/user_team/{userID}/{teamID}`: membership role/index.
- `identity/current_selection/{userID}`: active TeamID and optional active WorkspaceID for that user.
- If sessions are persisted in Section A, add `identity/session/{sessionID}` or equivalent with `SessionID`, `UserID`, `TeamID`, expiry, and revocation state. Do not rely on in-memory desktop token as product session state.

Required identity behavior:
- Bootstrap accepts username only plus any intentionally daemon-setup fields kept separate.
- Bootstrap creates exactly one canonical user, one hidden default team, membership, and current selection atomically.
- Rebootstrap hard-fails if identity already exists.
- Username uniqueness is enforced by the username index.
- No fallback user/team IDs, no deriving user from OS user, host name, swarm name, path, session ID, or token.

### Credential/Codex keys

Replace global credential keys with actor-scoped keys. Use exact naming decided in code review, but the shape must include `UserID` before provider/credential:
- `auth/user/{userID}/credential/{provider}/{credentialID}`
- `auth/user/{userID}/credential_active/{provider}`
- `auth/user/{userID}/index/auth_tag/{tag}/{provider}/{credentialID}`

Required record fields:
- `UserID` on every personal credential record.
- Optional `TeamID` only if a credential is explicitly team-shared; do not invent team sharing silently.
- `CreatedByUserID`/`UpdatedByUserID` if audit is needed.
- Remove `OwnerSwarmID` as ownership. If still needed for managed transport cleanup, rename/classify it as transport metadata and require a product owner alongside it.

Codex must become either:
- a normal user-scoped credential under provider `codex`, or
- a clearly user-scoped Codex auth record with the same ownership rules.

Do not keep `auth/codex/default` migration as an automatic ownership adoption path. If legacy global data is found during fresh cutover, hard-refuse and tell the operator explicit migration is required.

### Vault keys

Pick one canonical vault ownership model before implementation and document it in the active plan:
- Recommended first checkpoint: user-owned local vault metadata at `auth/user/{userID}/vault/meta`, protecting that user's credentials.
- Future team/shared secrets may use `auth/team/{teamID}/vault/meta` only when team credential sharing is explicitly implemented.

Required rules:
- Vault status/enable/unlock/lock/disable/export/import must resolve authenticated `UserID` first.
- Vault operations only touch credentials owned by that user, or explicit team-scoped credentials if the team vault exists and membership authorizes it.
- Global `auth/vault/meta` must not remain the authoritative vault state for converted APIs.
- Vault lock gate must check the relevant user/team vault, not a single global vault, once Section A routes are converted.

### Managed vault key and attach token rules

Managed vault keys must carry product owner scope if they affect product resources:
- key shape must include `TeamID`/deployment scope or the record must include `TeamID`, `CreatedByUserID`, and authorized deployment/session IDs.
- scope ID alone is not ownership.

Attach tokens may remain daemon transport credentials only if clearly separated from product identity. `/v1/auth/attach/rotate` must require authenticated product user/admin context after identity exists, because rotating daemon auth is an administrative mutation.

## Hard-refuse rules until conversion

Before identity bootstrap exists:
- Allow only minimal health/readiness and the bootstrap/status endpoints needed to create the first user.
- Product data mutations must fail with a clear “identity bootstrap required” style error.

After identity exists but before a route's backing store is converted:
- Hard-refuse `/v1/auth/credentials`, `/v1/auth/credentials/verify`, `/v1/auth/credentials/active`, `/v1/auth/credentials/delete`, `/v1/auth/codex`, `/v1/auth/codex/oauth/start`, `/v1/auth/codex/oauth/status`, `/v1/auth/codex/oauth/complete`, and `/v1/vault*` rather than reading/writing global records.
- Hard-refuse `/v1/auth/attach/rotate` unless the request resolves to an authenticated product user with the required local/admin role.
- `/ws` must not expose cross-user stream events once product identity exists; bind it to authenticated user/session or hard-refuse until stream scoping lands.

Allowed exceptions:
- `/healthz` and `/readyz` can remain unauthenticated and unowned if they expose no user data.
- Daemon onboarding config can remain separate from product identity, but it must not create or imply product user ownership.

## Implementation phases and commit/checkpoint order

Split into small commits. Do not proceed past a failing VM gate.

### Commit 1 — Identity store and bootstrap API

Implement canonical identity records, username index, hidden default team, membership, current selection, and product session record if chosen.

Add a username-only bootstrap endpoint. Name/path can be selected by the lead plan, but it must be explicit and separate from daemon `/v1/onboarding`. Suggested shape:
- `GET /v1/identity/status`: returns whether identity exists and whether bootstrap is required; no secrets.
- `POST /v1/identity/bootstrap`: accepts `username`; creates User/Team/Membership/Selection; returns safe identity/session info.

Do not silently use existing global credentials/workspaces to decide ownership.

### Commit 2 — Auth/session actor binding

Modify auth middleware so converted handlers can require actor context:
- request context includes canonical `UserID`, active `TeamID`, and session ID.
- desktop local session must bind to an existing bootstrapped user/session; it must not itself create ownership.
- attach token/local transport/peer auth remain transport auth and cannot satisfy product actor requirements unless explicitly mapped to product scope.

### Commit 3 — Section A hard-refuse gates

Before converting credentials/vault, add explicit hard-refuse checks for Section A identity-sensitive routes. This proves the system no longer permits global credential/vault mutation by transport auth alone.

### Commit 4 — User-scoped credentials and Codex

Convert auth service/store methods to take actor scope. Convert handlers and OAuth completion flow to save/list/activate/delete only user-owned credentials. Convert tag indexes and active credential keys. Remove automatic global legacy adoption for converted APIs.

### Commit 5 — User-scoped vault

Convert vault metadata and vault gate to user/team-specific state. Export/import must operate only within the authenticated user's allowed scope. Do not let global vault metadata remain authoritative.

### Commit 6 — Attach/admin and stream scoping

Require product admin/local-owner actor for attach rotate. Scope `/ws` to the product session/user or hard-refuse user data until stream filtering is complete.

## VM testing requirements

Every checkpoint must be proven on a fresh VM or fresh VM-equivalent state. Capture commands, HTTP status codes, response bodies with secrets redacted, logs, DB key inspection summaries, and restart proof.

Use generic paths in docs/artifacts; do not commit machine-specific absolute paths or real credentials.

### Fresh VM gate A — pre-bootstrap refusal

1. Start with empty app state.
2. Call identity status: expect bootstrap required.
3. Attempt credential create/list, Codex auth, vault enable/status where applicable, attach rotate, and a guarded product route.
4. Expected: identity-sensitive APIs fail before bootstrap with clear product identity required errors. Health/readiness remain OK.

Pass criteria:
- No credential key is created under old global `auth/credential/`.
- No vault meta is created under old global `auth/vault/meta`.
- No API silently creates a default user/team.

### Fresh VM gate B — username bootstrap

1. POST username-only bootstrap.
2. Verify exactly one user record, one hidden default team, one membership, and one current selection exist.
3. Verify returned session/actor context has `UserID` and active `TeamID`.
4. Repeat bootstrap; expect hard failure.

Pass criteria:
- Username index enforces uniqueness.
- `swarmName` and host/device data are not used as username or UserID.
- No fallback ID is generated from path/socket/session/token.

### Fresh VM gate C — login/session binding

1. Use desktop/local login/session path after bootstrap.
2. Verify subsequent guarded requests carry the same `UserID` and active `TeamID`.
3. Restart daemon.
4. Verify persisted identity and selection survive restart; expired/revoked session behavior is explicit.

Pass criteria:
- In-memory desktop token alone is not treated as product identity.
- Session or re-login resolves to existing `UserID`, not a new/guessed one.

### Fresh VM gate D — user-scoped credential/Codex

1. Create a test credential as the bootstrapped user using non-secret dummy values accepted by tests or mocked provider verification.
2. List credentials; only that user's credentials appear.
3. Set active credential; verify active key is user-scoped.
4. Delete credential; verify only the user's key/indexes are removed.
5. Run Codex OAuth/manual flow through mocks and verify resulting credential is user-scoped.
6. Restart and verify persistence.

Pass criteria:
- No writes to `auth/credential/{provider}/{id}` or `auth/credential_active/{provider}` as authoritative keys.
- No automatic migration/adoption from `auth/codex/default`.
- Tag indexes include user scope.

### Fresh VM gate E — user-scoped vault

1. Enable vault as bootstrapped user.
2. Verify vault metadata key is user/team scoped.
3. Lock/unlock and confirm only the authenticated user's credential records are affected/readable.
4. Export/import with test bundle and verify ownership remains explicit.
5. Restart and verify vault status and unlock behavior persist.

Pass criteria:
- No authoritative write to global `auth/vault/meta`.
- Locked vault blocks only the relevant scoped resources.
- Export/import cannot cross users/teams without explicit membership/authorization.

## Final missed-risk checklist

Before declaring Section A complete, verify:
- No Section A handler calls auth/vault store methods without actor scope, except safe status/bootstrap endpoints explicitly designed that way.
- No request uses `swarmID`, `swarmName`, attach token, desktop local token, local transport, peer auth, path, session ID, or deployment ID as product ownership.
- No global credential/vault keys remain authoritative for converted routes.
- No dual-read compatibility path silently adopts old global records.
- No legacy migration assigns global records to the bootstrapped user without explicit operator migration.
- OAuth in-memory sessions cannot complete into a credential without an authenticated product user/session.
- Auto defaults triggered by credential setup do not mutate global model/agent preferences; if those dependencies are not converted yet, hard-refuse or defer that side effect.
- Vault gate does not block or unlock unrelated users/teams via global state.
- Event envelopes for auth/security either carry actor scope or are classified as system events that do not authorize resource access.

## When to checkpoint/update the active plan

Checkpoint/update before coding beyond Section A if any of these are discovered:
- identity store path/naming conflicts with an existing canonical design;
- credential sharing requires TeamID in Section A earlier than planned;
- vault must be team-scoped rather than user-scoped for first release;
- OAuth/auto-default side effects require Section C model/agent conversion first;
- `/ws` stream scoping requires cross-section event-log changes.

Do not continue implementation past a failed fresh-VM gate. Stop, document the failing route/key, and update the plan with the exact missing ownership conversion.

## Relevant filepaths

- `swarmd/internal/api/server_routes.go` — route registration for Section A.
- `swarmd/internal/api/server.go` — auth middleware, auth handlers, attach rotate, and route mux wrapping.
- `swarmd/internal/api/desktop_local_auth.go` — current in-memory desktop local session token flow.
- `swarmd/internal/api/onboarding.go` — daemon onboarding/startup config; not product identity.
- `swarmd/internal/api/vault.go` — vault handlers and current global vault gate.
- `swarmd/internal/api/codex_oauth.go` — Codex OAuth session completion writes credentials without actor scope today.
- `swarmd/internal/auth/service.go` — auth service API currently lacks actor-scoped credential/vault calls.
- `swarmd/internal/store/pebble/keys.go` — current global credential/vault/auth key families and target place for scoped key helpers.
- `swarmd/internal/store/pebble/auth_store.go` — credential records and global provider/credential storage.
- `swarmd/internal/store/pebble/auth_vault.go` — global vault metadata and encryption flow.
- `swarmd/internal/store/pebble/auth_managed_vault_key.go` — managed vault key scope ID storage.
- `swarmd/internal/store/pebble/client_auth_store.go` — daemon-global attach token storage.
- `swarmd/internal/security/service.go` — attach token rotate/validate service.

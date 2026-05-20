# Checkpoint 1 execution document — user-first identity foundation, bootstrap, and local JWT sessions

Status: active execution document for Checkpoint 1. This is not a roadmap sketch; each slice below is a real implementation checkpoint with its own tests and a fresh-VM proof before moving on.

## Purpose

Checkpoint 1 is the first cutover gate for a **user-first** identity model. It is not a team-first migration.

Treat it as a sequential cutover made of smaller gates:

1. create canonical product identity storage (`User`, backend/default `Team`, `TeamMembership`, current user selection)
2. add product identity bootstrap to onboarding from **username only** while preserving daemon `swarmName`
3. replace implicit desktop-local cookie auth with long-lived local JWT product sessions keyed by `UserID`
4. add the first strict identity guards for protected APIs
5. update desktop onboarding/sidebar behavior so `username` and `swarmName` remain separate
6. make the local TUI do something safe on the main machine: use the bootstrapped current `UserID` session, or block with username-only bootstrap guidance when identity is missing
7. prove the whole 0→1 flow on a fresh VM with captured artifacts

No slice is complete without tests. The final Checkpoint 1 gate is not complete without the existing VM harness.

## Hard invariants

### This is user-first, not team-first

- `UserID` is the primary product actor.
- First-run onboarding asks for **username only** for product identity.
- First-run UX must **not** ask for a team name, team selection, organization selection, or workspace/team picker.
- First-run bootstrap is an explicit local-owner claim for the installed daemon/master machine: the person completing onboarding becomes the initial product `UserID` for that daemon, with the backend-created default owner/admin membership. This must be stated clearly in Desktop and TUI first-run flows.
- The bootstrap-created default `Team` is a backend ownership/sharing container. It must not be presented as the primary identity and must not be forced into first-run UX.
- Teams are introduced explicitly for sharing, collaboration, multi-user isolation, or admin flows. They are **not** the starting point of the product model.
- Any accepted `TeamID` must be validated through the authenticated `UserID` and membership. No code may treat `TeamID` alone as the actor.

### Identity facts are separate

- `username` is product/user identity.
- `swarmName` is daemon/device/swarm identity.
- `UserID` authenticates product actor context.
- `TeamID` is a sharing/isolation container that a user may belong to; the default team exists automatically but is backend/default context, not first-run UX.
- `SwarmGroup` is swarm/topology grouping and is not a product `Team`.
- `WorkspaceID` is not implemented in Checkpoint 1 except where the selection shape reserves a nullable/current field for later checkpoints.

### Do not break `swarmName`

`swarmName` stays canonical daemon configuration and remains visible/editable in desktop swarm settings/sidebar flows. It must not be renamed, removed, or backed by `username`.

Evidence from current code:

- `swarmd/internal/api/onboarding.go:112` currently accepts `swarm_name`; `swarmd/internal/api/onboarding.go:418` validates/writes it.
- `pkg/startupconfig/config.go` owns startup config and `swarmName`.
- `web/src/features/desktop/onboarding/components/desktop-onboarding-gate.tsx:90` stores onboarding `swarmName` UI state.
- `web/src/features/desktop/layout/desktop-app-page.tsx` displays/edits the sidebar swarm name.

### No fallback identity

- No default `UserID` when identity is missing.
- No guessed `TeamID` from `swarmName`, `SwarmGroup`, path, hostname, local OS user, or cookie presence.
- No hidden user creation from desktop auth, guarded API calls, session bootstrap, restart, or settings read.
- No silent adoption of old state.
- Re-running product bootstrap after identity exists must fail cleanly.

### One source of truth

Checkpoint 1 canonical sources:

| Fact | Canonical source | Non-authoritative/forbidden as source |
| --- | --- | --- |
| `User` | Pebble identity record keyed by `UserID` | desktop cookie, OS user, `swarmName`, config file, `TeamID` |
| username | `User.Username` in identity store | `swarmName`, sidebar label, SwarmGroup name, Team name |
| `Team` | Pebble identity record keyed by `TeamID` as backend sharing/container scope | SwarmGroup, workspace path, config file, frontend picker |
| membership | Pebble `TeamMembership` keyed by `(TeamID, UserID)` | auth token claims alone, SwarmGroupMembership |
| current user/selection | Pebble identity selection record with `UserID` primary | frontend memory/localStorage, cookie-only state, team-only selection |
| product session | signed local JWT with `sub=UserID` plus validation against canonical identity store | old global singleton cookie token |
| daemon/swarm name | startup config / existing daemon config path | `User.Username` or `Team.Name` |

JWT claims are authentication evidence only. They are not the authority for username, team membership, or current selection; every accepted token must resolve to an existing `UserID` in the identity store.

## Current-state findings that drive the work

- `swarmd/internal/api/desktop_local_auth.go:28` currently has one process-wide `token` and `expiresAt` in `desktopLocalSessionManager`; it cannot identify a user.
- `swarmd/internal/api/desktop_local_auth.go:191` exposes `GET /v1/auth/desktop/session`, which currently mints the singleton local cookie.
- `swarmd/internal/api/server.go:3677` accepts desktop local session auth if the singleton token validates.
- `swarmd/internal/api/server.go:3790` exempts `GET /v1/auth/desktop/session` for same-origin local browser requests.
- `swarmd/internal/api/server.go:3792` exempts `GET /v1/onboarding`; `POST /v1/onboarding` currently requires auth except when same-origin local session was auto-issued.
- `web/src/app/api.ts:24` bootstraps `/v1/auth/desktop/session`; it currently expects only `{ ok: true }` and relies on cookies.
- `web/src/app/api.ts:68` centralizes fetches and is the right place to attach `Authorization: Bearer <jwt>` if the frontend stores bearer tokens instead of relying only on HttpOnly cookies.
- `web/src/features/desktop/onboarding/components/desktop-onboarding-gate.tsx:149` persists only `swarmName` today and also creates/updates a SwarmGroup; that SwarmGroup must not become product `Team`.
- `cmd/swarmtui/main.go` starts the TUI through `internal/app.New`; `internal/client/api.go` has a no-op `EnsureLocalAuth` and sends only `X-Swarm-Token` today. The TUI therefore needs an explicit Checkpoint 1 local-session path instead of silently relying on old singleton/local cookie behavior.
- `swarmd/internal/store/pebble/keys.go` has `swarm/group/*` keys but no product `identity/*` keys.

## Checkpoint 1 API contract

### Onboarding status

`GET /v1/onboarding` must return enough identity state for first-run UX without creating anything:

- `needs_onboarding`: true when product identity bootstrap is missing or daemon onboarding is incomplete.
- `identity.bootstrapped`: false before the first user/default-backend-team/membership exists.
- `identity.current_user_id`: present only after bootstrap and treated as the primary actor.
- `identity.current_team_id`: present only after bootstrap as validated backend/default container context; it must not drive a first-run team picker.
- `config.swarm_name`: still daemon `swarmName`.

`GET /v1/onboarding` must not create identity records and must not issue product sessions by itself.

### Bootstrap/update request

`POST /v1/onboarding` must accept username for product identity and swarm name for daemon identity:

```json
{
  "username": "alice",
  "swarm_name": "Alice Laptop",
  "swarm_mode": true,
  "child": false
}
```

Rules:

- `username` is required only when product identity has not been bootstrapped.
- `swarm_name` remains required for daemon config when swarm mode needs it.
- The first successful bootstrap atomically creates exactly one `User`, one default backend `Team`, one owner/admin `TeamMembership`, and one current user selection.
- The same response issues a long-lived local product JWT session for the created `UserID`.
- Subsequent attempts to provide `username` to bootstrap again must fail with a clear 4xx error.
- Updating `swarm_name` after bootstrap remains a daemon-config operation and must not change `username`, `UserID`, `TeamID`, or membership.
- No request path may ask for, require, or infer a team name during first-run product bootstrap.

### Desktop local auth/session

Replace the current implicit singleton token with JWT-based product sessions.

Requirements:

- Token format: JWT-style signed compact token.
- Claims must include at least:
  - `iss`: local swarm daemon/session issuer
  - `aud`: desktop local API
  - `sub`: `UserID`
  - `iat`
  - `exp`
  - `sid`/`jti`
- TTL: long-lived local-only token. Use a duration long enough not to require refresh during normal local desktop use (for example 180 days). Do not implement a short refresh-token dance in Checkpoint 1.
- Signing key: persisted local daemon secret in the canonical store/config path, generated once if absent during identity bootstrap/session setup. Do not commit or document real secrets.
- Validation must verify signature, expiry, and that `sub` resolves to an existing user in the identity store.
- Desktop local session bootstrap must not create identity. If no product user exists, it returns a clean 401/409-style “identity bootstrap required” error.
- `GET /v1/auth/desktop/session` after bootstrap may issue a JWT only for an existing user/current selection. It must not silently pick a user if current selection is missing or invalid.
- Existing local-browser origin checks still apply; JWT auth does not make LAN/open web auth broader.
- First-run local-owner bootstrap must require an explicit human intent confirmation in both Desktop and TUI before creating the initial user/default-owner state. For Checkpoint 1, use a simple typed confirmation of the exact word `desktop` after explaining that this creates the local owner identity for this installed daemon. This is an intentional-user guard, not a cryptographic identity proof; same-origin/local-machine restrictions and later pairing/auth flows remain the security boundary.

Transport decision for Checkpoint 1:

- Prefer an HttpOnly `swarm_desktop_session` cookie containing the JWT for browser safety.
- The API may also return non-secret identity/session metadata (`user_id`, `team_id`, `expires_at`) for UI state, with `user_id` as the primary identity.
- If bearer storage is needed for non-browser callers, add it explicitly and still validate through the same middleware. Do not create a second auth model.
### TUI user/team behavior contract

- Fresh install / empty state:
  - TUI onboarding asks for exactly `username` and `swarmName`, plus the typed local-owner confirmation `desktop`.
  - `username` creates the first product `User`.
  - `swarmName` configures the daemon/device name.
  - The confirmation is required to acknowledge that this installed daemon/master will be initialized with this user as the local owner.
  - Backend bootstrap also creates the hidden/default backend `Team`, owner membership, and current selection.
  - TUI then enters as that `UserID`. It must not ask for team name, team selection, organization selection, or workspace/team picker.
- Normal single-user state:
  - TUI authenticates to the local daemon as the current product `UserID` via the backend-issued local session.
  - The user label is `username`; the machine/sidebar label remains `swarmName`.
  - The default team is only validated backend context for ownership. It is not the visible login identity.
- After explicit teams exist later:
  - TUI still logs in as a `UserID`, not as a team.
  - Personal/default actions continue to use the user's current personal/default context without forcing a team picker.
  - Team-scoped actions are shown only for explicit sharing/admin/team workflows and require choosing or already having a validated team context for that action.
  - If a user belongs to multiple teams and performs a team-scoped action with no valid selected team, TUI must ask for the team at that point only; it must not ask at startup or first-run.
  - If the selected team is missing or the user is not a member, TUI must fail/ask for explicit valid team selection for that team-scoped action. It must not guess from `swarmName`, path, OS user, or prior UI state.
- Before product identity exists:
  - TUI must not create hidden identity from local auth.
  - It may show the username+swarmName onboarding flow with typed `desktop` local-owner confirmation, or block protected actions with clear onboarding guidance.
- Inbound vaulting/auth can replace or harden local session acquisition later. Until then, do not add a placeholder second auth model.

## Implementation slices

### Slice 1.1 — Identity data model and store

Scope:

- `swarmd/internal/store/pebble/keys.go`
- new `swarmd/internal/store/pebble/identity_store.go` or equivalent
- new `swarmd/internal/identity` package or equivalent service layer
- store/service unit tests

Implement:

- `User{ID, Username, CreatedAt, UpdatedAt}`
- `Team{ID, Name, Default bool, CreatedAt, UpdatedAt}` as backend/default sharing container, not first-run UX identity
- `TeamMembership{TeamID, UserID, Role, CreatedAt, UpdatedAt}` with role `owner` for bootstrap
- `CurrentSelection{UserID, TeamID, WorkspaceID optional}` where `UserID` is primary and `TeamID` is validated backend/default context
- key namespace such as:
  - `identity/user/{userID}`
  - `identity/user_by_username/{normalizedUsername}` as a derived uniqueness index
  - `identity/team/{teamID}`
  - `identity/membership/{teamID}/{userID}`
  - `identity/current_selection/default` or another explicitly documented singleton for Checkpoint 1
- list/count helpers used by tests and VM proof to prove exactly 1/1/1 records.

Negative cases:

- empty username rejected
- duplicate normalized username rejected
- selection with missing user/default container rejected
- membership without existing user/team rejected
- team-only/current-team-only selection rejected
- no code path creates identity outside the bootstrap service

Tests before moving on:

- Go unit tests for create/list/get/count/selection invariants.
- Race/atomicity test: failed membership/selection creation leaves no partial bootstrap identity records.
- Tests proving the store never treats `TeamID` alone as actor identity.

VM proof before moving on:

- Run the identity store tests inside a fresh harness VM after `reset`/`fast`.
- Capture command, stdout/stderr, exit code, and any generated test logs under an evidence directory.

### Slice 1.2 — Atomic bootstrap service

Scope:

- identity service/package
- Pebble store batch/transaction use
- service tests

Implement:

- `BootstrapFirstIdentity(username)` or equivalent single entrypoint.
- Generates opaque stable IDs (`user_*`, `team_*`, or UUID/ULID-style; pick one and document it).
- Creates the first user, default backend team, owner membership, and current user selection in one atomic operation.
- Rejects bootstrap if any identity records or current selection already exist.
- Does not read `swarmName`, hostname, OS username, workspace path, SwarmGroup, team name input, or config as identity input.
- Does not expose or require a team choice during first-run bootstrap.

Negative cases:

- rebootstrap rejected
- partial pre-existing user/team/membership/selection state rejected as corrupt/ambiguous for Checkpoint 1
- username cannot equal empty/whitespace; normalization collisions rejected
- any attempted team-name/team-picker bootstrap input is ignored or rejected according to the API contract; it must not become required

Tests before moving on:

- Go service tests for first bootstrap success from username only.
- Go service tests for every pre-existing partial-state refusal.
- Go service tests proving no `swarm/*` keys are read/written by product identity bootstrap.
- Go service tests proving no team name/selection is required for first-run bootstrap.

VM proof before moving on:

- Run service tests in harness VM.
- Evidence must include an identity state summary JSON showing counts and current user selection.

### Slice 1.3 — JWT local product session service

Scope:

- `swarmd/internal/api/desktop_local_auth.go`
- likely new `swarmd/internal/auth/local_jwt.go` or identity auth package
- `swarmd/internal/api/server.go` auth middleware
- tests around local auth/session validation

Implement:

- Persistent signing secret generation/loading.
- JWT issue/validate functions.
- Token validation returns an actor context with primary `UserID` and validated current `TeamID` resolved from canonical identity records.
- Replace singleton `desktopLocalSessionManager.token` with stateless JWT validation plus only necessary key/config state.
- Keep local same-origin browser requirement for issuing local desktop sessions.
- Fail desktop session bootstrap before identity exists.
- Fail desktop session bootstrap if current selection is absent or points at missing user/team/membership.
- Do not let the old cookie token remain a parallel auth authority.
- Do not issue a token for a team-only context.

Negative cases:

- no identity → `/v1/auth/desktop/session` fails and creates no identity
- forged JWT rejected
- expired JWT rejected
- JWT for missing user rejected
- JWT for user without selected backend/default team membership rejected
- old random singleton cookie rejected after cutover
- JWT or cookie carrying only `TeamID` rejected

Tests before moving on:

- Go unit tests for JWT signing/validation.
- API tests for `/v1/auth/desktop/session` before and after bootstrap.
- Middleware tests proving protected requests require a valid product JWT for `UserID`, not just local browser cookie presence or team context.

VM proof before moving on:

- Fresh VM starts daemon with empty state.
- `GET /v1/auth/desktop/session` fails before bootstrap and identity counts remain zero.
- After bootstrap, endpoint returns/sets a JWT session for the bootstrapped `UserID`.
- Restart daemon; old issued JWT still validates if unexpired and signing key persisted.
- Capture cookies/headers with token redacted.

### Slice 1.4 — Onboarding API cutover

Scope:

- `swarmd/internal/api/onboarding.go`
- `swarmd/internal/api/server.go` exemptions/middleware as needed
- onboarding API tests

Implement:

- Add `Username *string json:"username,omitempty"` to onboarding request.
- Add identity payload to onboarding response with user identity first.
- During first-run `POST /v1/onboarding`, require `username` and `swarm_name` as distinct inputs.
- Preserve existing daemon config writing for `swarmName`.
- Call identity bootstrap exactly once and atomically with config update semantics defined. If full cross-store atomicity is not possible between config file and Pebble, the operation must fail safely and be tested for no hidden identity guessing on retry.
- Return/set JWT session after successful bootstrap.
- Rebootstrap with `username` after identity exists returns clear error and does not mutate existing identity.
- `swarmName` updates after bootstrap must not touch product identity.
- No onboarding request/response should force a team prompt or make `Team` appear to be the primary identity.

Negative cases:

- missing username on first bootstrap fails
- missing swarmName when required fails
- `username == swarmName` may be allowed as values, but they must still persist to different fields; no fallback/copying between fields
- rebootstrap fails
- old request shape with only `swarm_name` cannot create product identity
- malformed identity state causes hard refusal, not repair/adoption
- request with team-only identity or no username cannot bootstrap product identity

Tests before moving on:

- onboarding API tests for empty → bootstrap → persisted identity counts.
- tests proving `swarmName` stored in startup config and username stored in identity store.
- tests proving updating `swarmName` later leaves username unchanged.
- tests proving first-run output and request contract are username-first and do not require team name/selection.

VM proof before moving on:

- Fresh VM daemon.
- `GET /v1/onboarding` shows identity not bootstrapped.
- `POST /v1/onboarding` with only `swarm_name` fails.
- `POST /v1/onboarding` with `username` + `swarm_name` succeeds.
- Verify exactly one user/default-backend-team/membership/current selection.
- Verify API output presents `UserID`/username first and does not require team selection.
- Restart daemon and verify both identity and swarmName persist separately.

### Slice 1.5 — Initial protected API identity guards

Scope:

- auth middleware / request context
- at least these guarded APIs from the approved Checkpoint 1 plan:
  - create workspace
  - create agent
  - create credential
- tests for guard behavior

Implement:

- Request actor context produced from validated JWT.
- Guard helper requiring current `UserID` and any required validated backend/default `TeamID` for selected protected APIs.
- Before bootstrap, guarded create operations fail with a clear missing identity/auth error.
- After bootstrap and valid JWT, one representative guarded resource can be created successfully as the bootstrapped user.
- Desktop local auth alone is not enough; a valid JWT resolving to `UserID` identity is required.
- No guarded API may infer actor identity from `TeamID` alone.

Negative cases:

- no token → fail
- local old cookie/random token → fail
- valid JWT but missing identity store record → fail
- valid user but missing required current backend/default team membership → fail
- team-only context without `UserID` → fail

Tests before moving on:

- API tests for the three pre-bootstrap failures.
- API tests for success after bootstrap with valid JWT.
- API tests proving desktop session endpoint does not create identity.
- API tests proving team-only or forged team context does not satisfy actor identity.

VM proof before moving on:

- Fresh VM, before bootstrap:
  - create workspace fails
  - create agent fails
  - create credential fails
- Bootstrap, then repeat one guarded create successfully as the bootstrapped user.
- Artifact request/response transcript must include status codes and redacted auth.

### Slice 1.6 — Desktop frontend and local TUI cutover while preserving sidebar `swarmName`

Scope:

- `web/src/app/api.ts`
- `web/src/features/desktop/onboarding/types.ts`
- `web/src/features/desktop/onboarding/api.ts`
- `web/src/features/desktop/onboarding/components/desktop-onboarding-gate.tsx`
- `web/src/features/desktop/layout/desktop-app-page.tsx`
- `web/src/features/desktop/settings/types/settings-tabs.ts`
- `web/src/features/desktop/settings/components/desktop-settings-page.tsx`
- new or updated desktop Account settings page/component
- relevant desktop auth/session/settings tests
- `internal/client/api.go`
- `internal/app/config.go`
- `internal/app/app.go`
- TUI auth/session startup tests where practical

Implement:

- Onboarding identity step collects `username` for product identity and `swarmName` for daemon identity.
- UI copy must clearly distinguish them, e.g. “Username” vs “Swarm name / device name”.
- Desktop and TUI first-run/no-user flows must both warn that no product user exists yet and that completing onboarding on this installed daemon creates the initial local owner/master user for this daemon.
- Desktop and TUI first-run bootstrap must both require explicit typed confirmation with the exact word `desktop` before calling the bootstrap endpoint. Empty, wrong, or skipped confirmation must not create identity.
- UI must not ask for a team name, team selection, organization selection, or team picker on first run.
- Save onboarding sends `username` and `swarmName` on first bootstrap only after the explicit local-owner confirmation succeeds.
- Remove any product-Team assumption from SwarmGroup creation. If SwarmGroup still needs to be created for topology UX, document it as swarm topology only and do not use it as product `TeamID`.
- `api.ts` session bootstrap recognizes the new JWT session response/cookie behavior.
- TUI local startup calls the same backend local-session contract and stores/uses only the returned product session token or cookie-compatible auth mechanism for API calls.
- TUI protected actions run as the authenticated/current `UserID` after identity exists, not as a guessed admin, OS user, team actor, swarmName, or device identity.
- TUI must handle later multi-team membership without becoming team-first: startup remains user-first; only explicit team-scoped workflows request or require a validated team context.
- TUI before bootstrap must not create identity implicitly; it must show username+swarmName onboarding plus the same typed `desktop` local-owner confirmation, or block guarded actions with clear onboarding guidance.
- Sidebar continues to display/edit `swarmName`; it does not display username in the swarm name slot.
- Add a Desktop settings Account section/page as the first settings section. For Checkpoint 1 it is read-only/minimal: list the current username/product actor context when bootstrapped, show clear onboarding-required text when missing, and do not provide username/team mutation controls.
- If username is shown anywhere in Checkpoint 1, it is shown as product actor context, not as daemon/swarm identity.
- If default team metadata is shown anywhere, it must be secondary/backend context and must not replace the user identity display.

Negative cases:

- first-run UI cannot continue with empty username
- first-run UI cannot continue with empty swarmName when daemon config requires it
- first-run UI cannot force or require a team name/selection
- first-run Desktop/TUI bootstrap cannot proceed unless the user enters the exact typed local-owner confirmation `desktop`
- sidebar `swarmName` edit does not mutate username
- TUI without bootstrapped identity cannot create workspace/agent/credential by falling back to local cookie, OS user, swarmName, or team/admin guesses
- TUI with a valid local session acts as the authenticated `UserID`; any default or selected team context is validated secondary context only
- Account settings can display the current username/product actor context but cannot mutate username or teams in Checkpoint 1
- changing username is not invented in settings unless an explicit identity admin surface exists later

Tests before moving on:

- TypeScript/unit tests for onboarding payload mapping.
- Component/state tests where practical for two separate inputs.
- Tests proving no first-run team prompt/control is required.
- Tests proving Desktop and TUI bootstrap require the typed `desktop` local-owner confirmation and wrong/empty confirmation creates no identity.
- Existing desktop tests updated for new session response.
- Tests proving Account settings is present/read-only and displays username as product actor context without replacing sidebar `swarmName`.
- Go tests for TUI client local-session bootstrap/token attachment and no-op/no-hidden-identity behavior before bootstrap.

VM proof before moving on:

- Build web assets in harness VM.
- Run the VM API flow against the built daemon to prove frontend contract endpoints match backend. If browser automation is available, run a minimal Playwright onboarding smoke proving username-first/no-team-prompt, typed `desktop` confirmation, Account settings username display, and sidebar `swarmName` separation; otherwise record this as an API-level VM proof for Checkpoint 1 and add browser proof in Checkpoint 6.
- Run a TUI/client-level smoke in the VM proving: before bootstrap it does not create identity; onboarding requires username+swarmName plus typed `desktop` confirmation only; wrong/empty confirmation creates no identity; after bootstrap it obtains/uses the local product session for the `UserID`; startup does not show a team picker; explicit team-scoped actions require validated team context only when that workflow is used.

### Slice 1.7 — VM harness scenario and final Checkpoint 1 gate

Scope:

- new or updated `tests/swarmd/identity_bootstrap_e2e.sh`
- `docs/swarm-harness-vm.md` only if usage docs need updating
- final evidence directory format

Implement a dedicated harness script that can be run inside the existing VM lane and captures artifacts.

Required host-side invocation:

```bash
./scripts/swarm-harness-vm.sh reset
./scripts/swarm-harness-vm.sh fast
./scripts/swarm-harness-vm.sh run -- ./tests/swarmd/identity_bootstrap_e2e.sh
```

The script must create an evidence directory and write at least:

- `commands.log`
- `daemon.log`
- `requests.ndjson` or equivalent request/response transcript
- `identity-summary-before.json`
- `identity-summary-after-bootstrap.json`
- `identity-summary-after-restart.json`
- `auth-session-before-bootstrap.json`
- `auth-session-after-bootstrap.json` with token redacted
- `tui-session-before-bootstrap.json`
- `tui-session-after-bootstrap.json` with token redacted
- `guarded-api-negative.json`
- `guarded-api-positive.json`
- `persistence-summary.json`
- `summary.json` with final pass/fail and exit code

Final VM gate sequence:

1. Build Swarm inside the VM from synced checkout.
2. Start daemon with empty XDG state.
3. Capture `GET /v1/onboarding` and prove identity is absent.
4. Attempt desktop local session bootstrap before identity; must fail and identity counts must remain zero.
5. Hit guarded APIs before bootstrap:
   - create workspace
   - create agent
   - create credential
   - all must fail.
6. Attempt old onboarding shape with only `swarm_name`; must fail to create identity.
7. Bootstrap with both:
   - `username`: test product user
   - `swarm_name`: test daemon swarm name
   - no team name, team selection, organization selection, or team picker input
8. Verify exactly:
   - one user
   - one default backend team/container
   - one owner membership
   - current selection set to that user with validated backend/default team context
   - JWT session issued for that `UserID`
   - API/UI state presents `UserID`/username as primary actor
   - startup config contains `swarmName`, not username-as-swarmName fallback
9. Create one guarded resource successfully using the JWT session.
10. Run TUI/client local-session smoke: before bootstrap artifact shows no hidden identity creation; onboarding asks username+swarmName only; after bootstrap artifact shows the TUI acts as the authenticated `UserID`; startup has no team prompt/input; team selection appears only for explicit team-scoped workflows after teams exist.
11. Restart daemon.
12. Verify identity, selection, signing key/JWT validity, TUI local-session behavior, and `swarmName` persist.
13. Re-run bootstrap with a new `username`; must fail cleanly and existing identity must remain unchanged.
14. Attempt forged/old random cookie auth; must fail.
15. Verify there is no authoritative identity model outside identity store for Checkpoint 1 records.
16. Write final `summary.json` and exit non-zero on any failure.

Pass criteria:

- all unit/integration tests for slices 1.1–1.6 pass
- VM final gate exits 0
- evidence directory path is printed
- final summary says pass
- request transcript proves negative cases as well as success path
- daemon restart proof passes
- no hidden identity creation paths remain for Checkpoint 1 surfaces
- no first-run team prompt/team picker/team-name requirement exists
- product actor is always `UserID`, never team-only context

## Section prompt checkpoints after the initial identity foundation

The five saved section prompts in `docs/identity/` are now formal checkpoint references for the identity cutover. They do **not** replace slices 1.1–1.7 above. The proper initial steps must land first:

1. identity store
2. atomic username-only bootstrap
3. local JWT product session
4. onboarding API cutover
5. initial protected API guards
6. desktop/TUI alignment
7. Checkpoint 1 final VM gate

Only after those initial steps pass may a coding agent begin the section checkpoints below. Each section checkpoint is sequential, must have its own commit or tightly scoped commit series, and must pass its own fresh-VM proof before the next section begins. If a section prompt discovers missing scope, ambiguous ownership, or a VM proof gap, checkpoint/update the active plan before continuing.

### Section checkpoint 1.A — Identity/Auth/Onboarding/Vault/Bootstrap

Prompt reference: `docs/identity/section-a-identity-auth-next-agent-prompt.md`.

Start this after username bootstrap and local login/session binding are working. Execute Section A in this order:

1. verify username-only bootstrap and `UserID` session binding still pass;
2. hard-refuse credentials/Codex/vault/attach routes that still read global backing records;
3. convert credentials/Codex active state and indexes to authenticated `UserID` scope;
4. convert vault metadata/gate/export/import to authenticated user scope or keep hard-refused until that backing store is converted;
5. run Section A fresh-VM proof: guarded APIs fail before bootstrap, username bootstrap succeeds, login/session works, one guarded resource succeeds as the bootstrapped user, restart persists identity/session state, rebootstrap fails, desktop local auth alone does not create identity.

Do not move on while any Section A route can still mutate global `auth/credential/*`, `auth/credential_active/*`, `auth/codex/default`, or `auth/vault/meta` as authoritative state.

### Section checkpoint 1.B — Workspace/UI/Git/Worktree/Media

Prompt reference: `docs/identity/section-b-workspace-ui-next-agent-prompt.md`.

Start this only after Section A has a validated `UserID` actor/session and hard-refuse posture. Execute Section B in phases from the prompt:

1. actor/team-selection plumbing for workspace APIs;
2. `WorkspaceID` records and current selection keyed by `UserID + active TeamID`;
3. directory/browse/discover/overview isolation;
4. todos;
5. worktrees and git;
6. media threads/storage;
7. managed links only after Section E topology bindings carry product ownership.

VM proof must show path-only authority fails, current workspace selection does not leak across user/team context, restart persists owner-scoped records, and old `workspace/current`, `workspace/entry/{path}`, todo path keys, worktree path keys, and thread-ID/path-only reads are not authoritative for converted routes.

### Section checkpoint 1.C — Agents/Custom Tools/MCP/Model/Providers/Image/Voice

Prompt reference: `docs/identity/section-c-agents-tools-next-agent-prompt.md`.

Start this only after the initial identity/session foundation and after any Section B workspace dependency required by image assets is safe. Execute Section C in phases from the prompt:

1. agents/custom tools scoped storage and hard-refuse gates;
2. MCP scoped storage and runtime inclusion;
3. model preferences/favorites and credential-aware provider status;
4. voice config/profiles/STT;
5. image generation/assets only after workspace/thread ownership is available.

VM proof must show user-scoped active selections and preferences, team-scoped shared tool configs where intended, no global MCP mutation, no global model/voice state as user state, credential readiness is actor-scoped or credential-free, cross-user/team access fails, and restart preserves scoped records.

### Section checkpoint 1.DF — Runtime/Sessions/Permissions/Integrations/Deploy

Prompt reference: `docs/identity/section-df-runtime-deploy-next-agent-prompt.md`.

Start this after the identity/session foundation and after any required workspace/team scope exists. Execute Section D/F in phases from the prompt:

1. owned sessions/messages/plans/modes/lifecycle/usage;
2. permissions and notifications bound to owned sessions and canonical `UserID`;
3. flows scoped by `UserID + TeamID` and validated target scope;
4. integrations and builder sessions with hardcoded `scope=swarm` removed or hard-refused;
5. deploy containers with deployment tokens resolving to team-owned records;
6. remote deploy and local transport duplicates enforcing the same product scope.

VM proof must include forged/mismatched session, permission, flow, integration, deployment, bootstrap-secret, peer, and local-transport denial cases. Transport tokens, session IDs, deployment IDs, paths, and `swarmID` must never authorize product ownership by themselves.

### Section checkpoint 1.E — Swarm/Topology/Managed Hosts/Peer/Mirror/Local Containers/Groups/Targets

Prompt reference: `docs/identity/section-e-topology-next-agent-prompt.md`.

This is large and high risk. Start it only after initial identity/session foundations are stable and after the active plan has the necessary team/workspace scope for the phase being touched. Execute Section E in small checkpoints from the prompt:

1. refusal and scope plumbing for product-bearing topology routes;
2. targets/groups decision: groups are either topology-only or explicitly mapped to real `TeamID` records;
3. local containers/profiles with no silent auto-adoption of existing global records;
4. topology workspace/session routes with `TeamID`/`WorkspaceID` ownership where product-bearing;
5. managed hosts, peer sessions, replication, and mirror with propagated product scope.

VM proof must include fresh-VM and, where required, multi-VM evidence. It must prove topology/transport trust is separate from product ownership, peer auth without propagated product scope is rejected, local socket/attach token/`swarmID`/`swarmName` are not ownership, mirrored resources do not leak across teams/workspaces after restart, and `/v1/workspace/managed-links/upsert` plus `/v1/workspace/managed-links/remove` are coordinated with Section B/E before enabling.

## Exact testing cadence

For every slice and section checkpoint:

1. implement the slice/checkpoint
2. run targeted local tests
3. run the same targeted tests in the VM using `./scripts/swarm-harness-vm.sh run -- ...`
4. capture request/response, logs, DB/state summary, restart proof, and exit code
5. commit only after the VM proof for that slice/checkpoint passes
6. only then start the next slice/checkpoint

For the final gate:

1. `./scripts/swarm-harness-vm.sh reset`
2. `./scripts/swarm-harness-vm.sh fast`
3. `./scripts/swarm-harness-vm.sh run -- ./tests/swarmd/identity_bootstrap_e2e.sh`
4. inspect `summary.json`
5. report evidence path and exit code

## Implementation order and stop conditions

Order is fixed:

1. identity store
2. bootstrap service
3. JWT session service
4. onboarding API cutover
5. protected API guards
6. frontend + local TUI cutover
7. VM final gate for the initial Checkpoint 1 identity foundation
8. Section checkpoint 1.A using `docs/identity/section-a-identity-auth-next-agent-prompt.md`
9. Section checkpoint 1.B using `docs/identity/section-b-workspace-ui-next-agent-prompt.md`
10. Section checkpoint 1.C using `docs/identity/section-c-agents-tools-next-agent-prompt.md`
11. Section checkpoint 1.DF using `docs/identity/section-df-runtime-deploy-next-agent-prompt.md`
12. Section checkpoint 1.E using `docs/identity/section-e-topology-next-agent-prompt.md`

The section checkpoints must not start before items 1–7 pass. They are ordered follow-up checkpoints attached to Checkpoint 1 so each converted surface has the backing storage/key/index conversion or hard-refuse behavior plus its own VM proof.

Stop immediately if any of these happen:

- any implementation tries to remove or rename `swarmName`
- any implementation uses SwarmGroup as product Team
- any implementation treats `TeamID` as the primary actor instead of authenticated `UserID`
- any first-run flow asks for or requires a team name/team picker/team selection
- any first-run Desktop/TUI bootstrap can create identity without the explicit typed `desktop` local-owner confirmation
- desktop auth creates identity by itself
- a guarded API creates identity implicitly
- a test passes only because a fallback ID was invented
- the VM gate cannot produce artifacts for negative cases

## Relevant filepaths

- `docs/checkpoints/checkpoint-1-identity-foundation-execution.md`
- `docs/identity/backend-api-section-inventory.md`
- `docs/identity/section-a-identity-auth-next-agent-prompt.md`
- `docs/identity/section-b-workspace-ui-next-agent-prompt.md`
- `docs/identity/section-c-agents-tools-next-agent-prompt.md`
- `docs/identity/section-df-runtime-deploy-next-agent-prompt.md`
- `docs/identity/section-e-topology-next-agent-prompt.md`
- `docs/swarm-harness-vm.md`
- `scripts/swarm-harness-vm.sh`
- `tests/swarmd/identity_bootstrap_e2e.sh` (new/expected)
- `swarmd/internal/api/onboarding.go`
- `swarmd/internal/api/desktop_local_auth.go`
- `swarmd/internal/api/server.go`
- `swarmd/internal/api/server_routes.go`
- `swarmd/internal/store/pebble/keys.go`
- `swarmd/internal/store/pebble/identity_store.go` (new/expected)
- `swarmd/internal/identity/service.go` (new/expected)
- `swarmd/internal/auth/local_jwt.go` (new/expected)
- `pkg/startupconfig/config.go`
- `web/src/app/api.ts`
- `web/src/features/desktop/onboarding/types.ts`
- `web/src/features/desktop/onboarding/api.ts`
- `web/src/features/desktop/onboarding/components/desktop-onboarding-gate.tsx`
- `web/src/features/desktop/layout/desktop-app-page.tsx`
- `cmd/swarmtui/main.go`
- `internal/client/api.go`
- `internal/app/config.go`
- `internal/app/app.go`

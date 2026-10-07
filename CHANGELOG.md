# Changelog

All notable Swarm release changes should be recorded here.

Release entries are the source checkpoint for public docs verification. Each entry must include a `Docs impact` section. If a release has no docs-impacting changes, write `Docs impact: none`.

## Unreleased
- Add bounded, exact-call `allow_once` consent for qualification Orchestrator source discovery and the owned pending-only Big Feature Swarm proposal; reject unknown, mismatched and stale permissions without bypass or saved rules, and retain canonical completion/zero-intent gates. Docs impact: none.
- Observe qualification runs by exact canonical identity and semantic durable progress, fail promptly on blocked/permission states, and retain secret-safe pre-stop failure diagnostics without extending deadlines; add multi-snapshot observer regressions. This improves failure observation, not a proven repair of the underlying provider stall. Docs impact: none.
- Namespace qualification runner project, session and message request keys by scenario and operation, preserving build receipt identity and deterministic retries; add shared-account collision regressions and allow required read-only Orchestrator source discovery. Docs impact: none.

- Reconcile Orchestrator source-filter regressions with the segmented Task scope UI; retain approval/archive guards and strengthen pressed-state, callback, count and keyboard-accessibility assertions without changing production styling. Docs impact: none.

- Retire the obsolete basic-plan-auto session chatmode runner and its launch/release receipt requirements; require explicit runner selection without fallback, preserving current session API, Task Program and Orchestrator coverage. Docs impact: `docs/testing/new-task-browser-smoke.md` documents retirement and qualification-helper rollout ordering.

- Ensure resilient APT toolchain installation and explicit GCC verification for qualification runner CGO compilation. Docs impact: none.

- Enable TAP and spec test reporters for deterministic Orchestrator qualification suites. Docs impact: none.

- Fix test-install-distro container setup to ensure mint suppression guard is copied into active container namespace under Docker runtime. Docs impact: none.


- Modernize workspace onboarding recovery test assertions to validate the canonical 4-step Desktop onboarding lifecycle, ensuring smooth step transitions, accurate multi-step navigation checks, and clean error recovery. Docs impact: none.

- Author comprehensive E2E tests for New Task creation across all 6 modal flows (small coder feature, big swarm feature, audit finder, image, video, and sound) with Chromium form submission, durable store verification, UI card rendering, and reload persistence. Add live Orchestrator E2E test suite and runner covering in-chat task proposals without self-approval, structured plan suggestion handoff to Swarm Default, and multi-stage Task Program dependency consumption. Docs impact: none.

- Correct the retained repository-continuation scheduler fixture to use the correction attempt's run identity for Task Program admission, preserving exact-head and sentinel assertions. Docs impact: none.

- Accept canonical successful Task Program integration receipts after child worktree cleanup, including nonblocking cleanup failure, when authenticating retained repository continuation results; preserve exact result provenance and isolated correction bases. Docs impact: none.

- Authenticate retained Task Program checkpoint runs and correction ancestry, bypass only provably empty terminal launch failures, and inspect committed repository-specific results through the existing task inspection route. Repair restart and canonical workspace regression fixtures. Docs impact: none.

- Retain authenticated per-repository Task Program results across project-task follow-ups, including intervening no-op attempts; pin correction bases before allocation and preserve isolated downstream Coder sources without promoting unvalidated changes. Docs impact: none.

- Add explicit PR selection preserving session API/security coverage and the existing Orchestrator seed suite; replace obsolete UI/default-Plan runner selectors with bounded, identity-bound session/chat/media qualification and an adapter to the owned New Task browser smoke. Repair explicit Plan reconciliation fixtures against durable publication/current-run authority. Docs impact: `web/README.md` and `docs/testing/new-task-browser-smoke.md` document runner argv, receipt validation, reuse and unverified live-proof limits.

- Preserve exact authorized repository sources across task reopen and recover previously cleared bindings from retained approved definitions and owned session grants, with fresh catalog/readiness validation and fail-closed reapproval guidance. Keep new attempt/session/run identities and scheduler guards intact. Docs impact: none.

- Add SSH Docker exact committed-context image builds and receipt-bound remote test deployments with connection-edit fencing, strict external SSH authentication, loopback port publication and explicit uncertain remote build cleanup; preserve local Podman contracts and task consumer leases. Docs impact: `docs/managed-ssh-test-environments.md` documents the generic operator-owned host workflow, provenance, cleanup limits and cloud-access separation; no live cloud validation is claimed.

- Separate Favorites into This chat, Default, and Default + this chat in Desktop and TUI; target Orchestrator's Plan default separately from deployed Swarm's Action default and report partial combined-save failures. Rename TUI `/profiles` to `/favorites`. Docs impact: command help updated.

- Restore Orchestrator chat header project identity in place of generic Workspaces, enable opening model favorites and updating Orchestrator model via canonical session API with rehydration, and route Agents to Orchestrator-owned agents settings. Docs impact: none.

- Fix TUI Ctrl+X shortcut so it opens the session switcher modal across home and chat views, and filter the switcher to display only orchestration sessions (excluding task and subagent worker sessions). Default initial filter to active chats when review is empty, and support Ctrl+X toggle close. Docs impact: none.

- Ensure typing prompts on the TUI homepage creates and opens a completely new canonical project session instead of reusing prior sessions or sending messages to an existing thread. Preserve task session resumption when explicitly navigated into the task board via Ctrl+Up. Docs impact: none.

- Remove Git branch from TUI header and project info box, wire canonical new project session creation via /new command, start initial focus on prompt box with Enter not capturing top task, and enable Ctrl+Up / Ctrl+Down navigation between prompt and task box. Docs impact: none.

- Resolve TUI message dispatch to project orchestrator and checkout-free sessions by allowing project-scoped and unscoped sessions through TUI path visibility and workset filtering. Dock prompt box at bottom of TUI screen, remove Git workspace setup warning, default workspace selection to first workspace, and upgrade top header to Project Box with workspace display (or count if > 4) and project switcher integration. Docs impact: none.

- Implement TUI orchestration focus mode with active project task board display on initial load, keyboard task selection and session launching, direct prompt submission to project orchestrator, Ctrl+X project orchestrator toggling, automatic primary orchestrator resolution, empty-task board guidance, and project switching integration via workspace switcher modal. Docs impact: none.

- Optimize TUI scrollback performance, reading viewport anchoring, and input responsiveness. Implement structured item layout caching, incremental Markdown rendering, stable timeline item viewport anchoring during streaming and appends, change domain tracking and transport-only wake suppression in V3 chat store, non-blocking render requests, and bounded event batching in the terminal application event loop. Docs impact: none.
- Show task-linked environments on task cards with exact deployment navigation and event-driven status; gate browser opening on deployment-snapshotted frontend declarations, fresh bounded HTTP readiness checks and verified loopback port mappings. Docs impact: `docs/managed-local-systemd-environments.md` documents browser configuration, safety and unrun frontend/manual qualification.

- Persist revision-guarded task environment attachments, source-bound per-consumer access, durable task invalidations and finite managed review cleanup; distinguish preparation handoff from task execution and lease ownership. Docs impact: `docs/managed-local-systemd-environments.md` documents task attach/acquire/release, explicit rebuild/reassignment and retention; live qualification remains separate.

- Expose environment management to Orchestrator while preserving saved capability denials; enforce durable Swarm/Orchestrator session admission on environment tools and HTTP routes, and add an internal source-bound independent-consumer lease foundation. Docs impact: none; task attachment wiring and live environment qualification remain separate.

- Add supervised local managed image builds from exact catalog-authorized committed product/recipe inputs, bounded clean contexts, operation-owned cancellation/cleanup and immutable deployment provenance. Preserve manual onboarding state on lease release. Docs impact: `docs/managed-local-systemd-environments.md` and environment tool help describe admission, action flow and live-validation limits.

- Preserve bounded, redacted local-engine failure diagnostics and add an explicit rootless Podman/systemd managed-environment contract with capability admission, private cgroups, PID limits, no host mounts, and loopback-only ports. Docs impact: managed-environment tool schema/help documents the typed runtime and host prerequisites; live runtime qualification remains separate.

- Add headless worker control, runtime journaling, verified-host SSH transport probe, and GCP compute target registration. Support fenced runtime claims, command acknowledgement, and target capacity reservations in store, runtime execution service, and TypeScript SDK (`WorkerControl`). Docs impact: none.

- Add typed SDK permission views and explicit structured/custom answers, confirmed browser/bridge resolution with recoverable errors, duplicate guards and durable pending-request reconciliation. Never auto-answer ask-user, and remove bypass/model configuration from chat quickstarts. Docs impact: explicit-decision examples and permission protocol migration guidance in `packages/sdk/README.md` and SDK quickstarts.

- Reject missing or malformed browser SDK subscription session IDs before opening a connection or changing the selected conversation. Docs impact: none.

- Keep SDK chat subscriptions usable across repeated turns, conversation switching and reconnects; add canonical project conversation helpers and a browser-only entry point, reject foreign-origin bridge upgrades, and disconnect failed upstream watches. Docs impact: SDK conversation lifecycle and browser bridge usage in `packages/sdk/README.md`.

- Add native WebSocket streaming bridge (`SwarmWebSocketBridge`, `client.chat.attachWebSocket`) and universal browser WebSocket client (`SwarmBrowserChat`) to `@swarm-agent/sdk`, allowing web frontends to hook directly into Swarm's bidirectional WebSocket stream for realtime assistant tokens, model reasoning deltas, live tool execution events, and permission approvals. Docs impact: none.
- Add high-level interactive Chat namespace (`client.chat`), permissions and permissionless mode namespace (`client.permissions`), automatic environment and Codex credential auto-configuration (`client.auth.autoConfigure`), verified provider fleet recommendations and router assignment (`applyProviderFleet`, `restoreDefaults`), auto-permission approval during session runs (`autoApprovePermissions`), and comprehensive 0->1 quickstart examples (`chat-quickstart.ts`, `orchestrator-quickstart.ts`) to `@swarm-agent/sdk`. Docs impact: none.

- Require test-only mint suppression before native qualification installation, reject ineffective service suppression at startup, and verify the daemon environment after installation, reinstallation, and restart for root/sudo scenarios. Add hermetic guard and repeated reporter-suppression regressions without changing real-user defaults; keep the publication path check limited to the exact test-only systemd drop-in statement. Docs impact: mandatory no-mint qualification requirements and evidence limits in `docs/main-deploy-checklist.md`.

- Document a five-step headless application SDK roadmap covering incremental non-production testing, sessions and orchestration, safe tools/media, durable state and parallel-workload qualification. Docs impact: add `packages/sdk/HEADLESS_PLAN.md` and link it from the headless UI guide; no new runtime capabilities are claimed.

- Bump release version candidate to v0.1.47 for npm package publication with bypass-2fa authentication. Docs impact: none.

- Fix desktop permission resolution tracking and filtering for pending permissions in Desktop V3 cache and remove unused permission selector import. Docs impact: none.

- Expand `NODE_AUTH_TOKEN` correctly in `.npmrc` configuration during GitHub Actions npm release step. Docs impact: none.

- Scope standalone SDK and CLI npm release packages under `@swarm-agent` (`@swarm-agent/sdk`, `@swarm-agent/cli`) and publish directly to public npm registry using `NPM_TOKEN` in `.github/workflows/build-main.yml`. Docs impact: none.

- Prepend `./` to local tarball package paths for npm publish in GitHub Actions release workflow so npm treats paths as local files rather than git shorthand URLs. Docs impact: none.

- Scope standalone SDK and CLI npm release packages under @swarm-agent and publish directly to GitHub Packages using repository tokens in `.github/workflows/build-main.yml`. Docs impact: none.

- Refresh Google Cloud access token before downloading release artifacts, extend token lifetime to 3600s, scale download timeouts, and adapt release policy schemas in `scripts/gcp-release-input.py`. Docs impact: none.
- Parallelize distribution binary builds in `scripts/build-main-dist.sh` and remove Cloud Build CPU throttling for faster release compilation. Docs impact: none.

- Fail closed when the protected release policy is missing: stop the GitHub release consumer before artifact intake and identify the required configuration instead of requesting another build. Docs impact: none.
- Bind release intake to the maintained qualification runner's exact-commit App checks and immutable artifact references; reject missing or mismatched qualification evidence. Docs impact: none.
- Allow bounded time for native and headless OCI qualification and release promotion without weakening protected publication gates. Docs impact: none.

- Refresh the headless container runtime to Ubuntu 26.04, upgrade inherited packages during image builds, and include the PCRE2 runtime dependency. Release builds continue to require digest-pinned base images and image vulnerability checks. Docs impact: none.

- Connect qualified, digest-preserving GHCR publication to version-pinned CLI/SDK release packs; reject conflicting tags and require anonymous image access before GitHub/npm publication. No live publication readiness is claimed. Docs impact: first-publish permissions and visibility prerequisites in `docs/main-deploy-checklist.md`.
- Replace optimistic deliverable publication with durable approval claims, validated X receipts and explicit failed/uncertain outcomes; partial publication requires reconciliation rather than automatic retry. Docs impact: publication outcome guidance in `docs/main-deploy-checklist.md`.

- Add an unpublished Linux amd64 headless distribution: non-root daemon image, Desktop-free setup, scoped-token SDK session access, standalone SDK packaging and an explicit npm container launcher with persistent state. Ordinary host listeners remain loopback-only; npm installation does not start containers. Docs impact: setup, authentication, persistence, candidate image pinning and bounded qualification are documented in `containers/headless/README.md`, `packages/cli/README.md` and `packages/sdk/README.md`. Registry publication remains separate.

- Retire the shared Swarm Atlas, two-pass TSV audit ledger, and atlas synchronization gate; preserve critical tests and independent review. Docs impact: remove mandatory bookkeeping instructions and obsolete links. Earlier Atlas/ledger mentions below describe historical work, not current requirements.

- Accept root-owned sticky world-writable directories (such as `/tmp`) for first-install artifact validation, and prevent root installer lockout by adopting new artifact roots on resume, cleaning up interrupted account creation, and clearing obsolete recovery records upon service install. Docs impact: none.

- Fix GCP-built release archive intake in build-main without retired Astra dependencies, and scope dependency-vulnerability-scan push triggers to dev to prevent push-to-main timeouts. Docs impact: none.

- Verify qualification test suites using Google Gemini 3.6 Flash (thinking: low) with ephemeral IP-locked credentials, per-agent model settings, sequenced live runners, and push check receipts. Docs impact: none.

- Authorize in-worktree Git tools (git_status, git_diff, git_add, git_commit, git_init) under default auto-mode policy and unignore task-program-probes in git. Docs impact: none.

- Normalize Google provider conversation turns and handle post-compaction session continuation. Docs impact: none.

- Accept GCP workflow configuration from Repository Secrets as well as Repository Variables, preserving immutable verifier selection and fail-closed qualification. Docs impact: GCP configuration precedence in the deploy checklist.

- Report missing GCP relay configuration separately from qualification failures without exposing variable values or weakening required checks. Docs impact: atlas validation notes.

- Promote immutable GCP-qualified merged-source release archives without rebuilding on GitHub; retain keyless signatures and protected publication with explicit GCP promotion attestations. Docs impact: release configuration and verification contract in the deploy checklist and atlas.

- Fix root-user installation and workspace recovery: require explicit consent before creating a service account, preserve existing ownership, and activate the installed candidate when a service is already running.
- Offer authenticated daemon-owned workspace guidance during fresh and resumed TUI onboarding, with explicit consent for new-folder setup and repository initialization.

- Fix workspace Save in root-launched TUI clients by deferring repository validation to the authenticated daemon, preserving repository-root, initial-commit, and Git ownership checks. Docs impact: clarified workspace validation authority in the Swarm Atlas.

- Fix TUI onboarding exit and recovery: Ctrl+C exits during setup, Enter retries workspace checks, Esc goes back, and explicit confirmation initializes empty folders or creates an empty first commit without staging existing files. Docs impact: documented setup controls and safety boundaries in the Swarm Atlas.

- Fix fresh Linux system installs by provisioning the Swarm-owned runtime root before its children, remove the caller `TMPDIR` requirement from systemd installation, install missing Git/Bash runtime prerequisites before Swarm mutation, and bind Ubuntu/Arch/Omarchy plus Fireworks reconciliation validation to one checksum-verified candidate that starts Git-absent.

### Added

- Added native Artifact V3 revision and scene remix controls, including exact historical revision sources and reference-only attachments that do not request a remix.
- Added sandboxed Three.js support for native 3D artifacts, with a pinned offline runtime for previews and capture.
- Added workspace Actions with structured inputs, quick-access pins, AI-assisted commit orchestration, and Desktop/TUI management surfaces.
- Added Git-aware workspace controls, worktree integration improvements, AI commit commands, and final handoff links to public pull requests.
- Added account-scoped model favorites and richer model controls, including complete catalog pagination, provider service-tier metadata, and current Google Gemini thinking support.
- Added first-run onboarding support for accepting initial provider credentials and a dedicated pre-admission Onboarding Swarm for safely preparing an existing-file first workspace.
- Added deterministic `swarm.animation/v1` HTML animation capture that publishes silent managed MP4 artifacts with exact source lineage.
- Added trusted registered audio sources, durable speech transcription, and bounded deterministic waveform, onset, tempo, beat, and energy-section analysis.
- Added reviewed Video Studio soundtrack editing with exact `source_audio` clips, typed pending proposals, accepted-cut preview, and deterministic audio/video rendering.
- Added the canonical Swarm Atlas, two-pass test audit ledger, and atlas-driven `fast`, `deep`, and `agents` critical gates for pull requests and release builds.

### Changed

- Removed automatic final-handoff commit suggestions based on historical changed-file lists, so already-committed work no longer appears to need another commit.
- Grouped native artifact alternatives by generation, named generation options, and added revision-bound click-to-play previews with scene navigation.
- Unified native animation duration limits around the shared frame budget, allowing longer timelines at lower frame rates without increasing the capture budget.
- Show pending review worktrees before expensive Git checks, bound review enrichment concurrency, and scope integration reads to the selected session instead of repeatedly scanning all review lanes. Background refreshes no longer keep completed integrations pending.
- Updated the daemon compression dependency to `github.com/klauspost/compress` v1.18.7 for the new build.
- Reworked durable V3 plan and checkpoint execution so boundary transitions, resumptions, source-message provenance, and conversation context remain in the canonical session epoch.
- Expanded Desktop and TUI workspace onboarding, session routing, themes, responsive navigation, git status, and launch tips while removing legacy display and workspace-definition authorities.
- Made Git a mandatory installed-runtime prerequisite and require every saved or selected workspace to be a repository root with an initial commit, rejecting direct-session worktree opt-out or missing worktree authority; the supported installer provisions missing Git, and Desktop provides explicit safe setup guidance instead of allowing temporary non-repository sessions. Provider-connected first-time users can ask the exact-folder Onboarding Swarm for permission-gated setup help or fix the repository manually, and workspace admission remains blocked until a recheck confirms the first commit.
- Flattened account-scoped workspaces into one global catalog: every saved path has independent identity and generation, historical linked directories migrate to standalone entries, and linked membership is no longer scope authority.
- Required every Git-backed routed or deployed session to start in a session-owned managed worktree, with durable ownership and lineage revalidated before provider execution.
- Hardened release update and systemd relaunch behavior, including non-privileged update handoff, replacement readiness, authorization, and rollback-sensitive restart paths.
- Split trusted pre-merge qualification from final protected-branch release builds so publication can require an independently authenticated candidate check while keeping infrastructure details and credentials outside the public repository.
- Separated core system agents from utility agents in Desktop and TUI model controls.
- Improved TUI Codex model-profile switching and V3 chat integration, with a maintained helper for copying the local Swarm database for diagnostics.
- Changed worktree-name conflict retries to use random five-digit identifiers.
- Hid the redundant `Automatic` plan execution badge while keeping the `Review each` policy indicator visible.
- Refreshed the release candidate metadata to validate the updated in-app update workflow.
- Improved Desktop sidebar responsiveness and Video Studio timeline ordering, rendering efficiency, and editing performance.

### Fixed

- Fixed TUI workspace Git refresh to use authenticated daemon status, allowing root-launched terminals to read service-owned repositories without weakening Git ownership checks. Unborn repositories, mismatched roots, and failed status reads remain non-ready.
- Fixed fresh and interrupted TUI onboarding so identity setup continues to provider and workspace setup, incomplete setup resumes on relaunch, completion uses the authenticated session, and the setup overlay stays dismissed after successful completion.
- Added editable workspace locations (`Ctrl+L`) and explicit folder creation (`Ctrl+N`) during TUI onboarding, allowing recovery from home/root launches, populated non-repositories, missing Git, and permission failures by retrying or choosing a safe project. Git initialization rejects home and filesystem roots, requires explicit consent, and preserves existing files and staged changes.
- Made root-only Linux installation automatically provision a non-root Swarm service identity with a usable home and workspace, while continuing to reject workspaces under the root home.
- Added bounded download attempts and visible installer progress, plus authenticated fresh-install command assertions that distinguish explicit root-home rejection from unrelated failures.
- Preserved native artifact scene identity and source lineage across repeated remix rounds, serialized sibling publication, and restored retained Designer draft recovery with clearer preview diagnostics.
- Fixed animation capture cancellation, deadline reporting, scene seek acknowledgements, and shared compositor ownership during concurrent captures.
- Synchronized workspace catalog changes through durable realtime updates, stabilized session workspace headers, and suppressed errors from intentional overview cancellation.
- Corrected saved-workspace theme targeting from managed worktrees, excluded revoked secondary workspace grants, and validated parent identity for plan sidechat worktrees.
- Improved TUI custom question answers and the display of previously saved responses.
- Reduced workspace-launcher startup work with catalog-first loading: saved workspaces no longer wait for session/permission payloads, todo summaries, Git probes, or folder discovery. Return visits reuse the existing in-memory query cache; background details remain independent, and theme/browser updates no longer trigger redundant refreshes. Catalog worktree settings use exact account-owned lookups instead of repeated full-catalog scans.
- Gave managed Designers one bounded refinement round for allowlisted author-correctable animation failures, using a fresh immutable candidate in the same collection while preserving failed artifacts and all strict trust checks.
- Made managed Designer task failures report the artifact's concrete failure code separately from trusted-lineage and composition rejection.
- Removed the retired hosted remote-deploy product surface while preserving Swarm targets, topology runtime placement, and workspace bindings.
- Retired dedicated local-container execution and its APIs, including dev image synchronization, image release artifacts, container-only harness commands, and container-specific configuration.
- Preserved V3 sessions/sync/realtime and generic Swarm-target routing as current critical contracts; containers and other non-local execution remain possible future runner targets rather than current local-container behavior.
- Hardened default daemon storage so local and install paths use system roots instead of user home, XDG, repository, workspace, or relative current-directory locations.
- Added a daemon storage path regression gate that rejects new home/XDG/workspace defaults and verifies the gate with a negative fixture.
- Replaced silent legacy storage migration/reuse with explicit read-only detection and operator-facing diagnostics.
- Prepared the storage contract for future macOS system roots under `/Library` and `/var/run`, while keeping the current installer path Linux-focused.
- Corrected public README install guidance to lead with the latest-release installer fast lane instead of source-checkout installation.
- Removed public README claims that Copilot is currently available as a supported provider. Copilot implementation code remains in the tree, but it is intentionally not registered as a selectable or runnable provider until it can be validated end-to-end with the required paid Copilot plan.
- Reframed `/voice` README guidance as experimental terminal voice input. The terminal STT path has been tested, but voice is not a polished or guaranteed workflow yet.
- Quarantined malformed durable sessions during sync and workset responses so one invalid session no longer prevents healthy sessions from loading.
- Fixed TUI launches from unsaved Git repositories so they retain the configured default workspace while showing actionable `/workspace save` guidance; nested repositories are no longer hidden by broad saved workspace roots.
- Allowed first-run OpenAI onboarding to accept credentials without depending on a live provider availability check; credentials remain explicitly unverified until a real provider request succeeds.
- Allowed authenticated same-origin Tailscale Desktop requests admitted by daemon origin policy to trigger host update actions while spoofed requests remain fail-closed.
- Updated the daemon cryptography dependency to fixed releases for CVE-2026-56854, CVE-2026-56855, and CVE-2026-78662, and made PR critical checks install their required repository-policy tool before fetching and testing the exact declared head/base commits.

### Docs impact

- The Swarm Atlas records service-account consent, ownership preservation, authenticated workspace guidance, resumed onboarding, and installed-candidate activation behavior.
- Public TUI onboarding docs should explain resumed provider/workspace setup, `Ctrl+L` location editing, `Ctrl+U` clearing, `Enter` confirmation, `Esc` cancellation, and `Ctrl+N` folder creation. Document explicit Git consent, home/root protection, and choosing another project when a folder cannot be safely initialized; the Swarm Atlas records these controls and completion behavior.
- Public artifact docs should explain generation grouping, revision-bound playback, scene remix, exact historical sources, reference-only attachments, retained-draft recovery, sandboxed Three.js support, and frame-budget-based animation duration limits.
- Public TUI guidance should reflect custom question answers and saved-response display; workspace synchronization, theme targeting, and capture lifecycle fixes need no new setup instructions.
- Public docs should cover workspace Actions, AI-assisted commits, Git/worktree controls, account-scoped model favorites, and provider service-tier choices.
- Public docs should describe the current durable V3 checkpoint/resume behavior and the updated Desktop/TUI workspace, routing, and onboarding surfaces.
- Public update docs should reflect the hardened non-privileged update, systemd relaunch, readiness, and rollback behavior.
- Public onboarding docs should describe initial provider credential acceptance; agent grouping, model switching, session quarantine, diagnostic tooling, and worktree retry identifiers need no separate user documentation.
- Public install and workspace docs must state that the installer provisions mandatory Git when absent and that workspaces require a repository root with an initial commit; non-repository folders are setup candidates, not usable temporary sessions. Existing-file first workspaces use the dedicated exact-folder Onboarding Swarm or a manual fix-and-retry path, with explicit permission for repository mutations and no admission before recheck.
- Public docs should describe the system storage contract, Linux root locations, no-silent-migration behavior, and future macOS system-root expectations.
- Public product docs must describe dedicated local containers as retired while retaining V3 and Swarm targets as current critical contracts and future non-local runners as a separate direction.
- Public install docs should point users to the release installer fast lane before source checkout workflows and explain automatic provisioning of missing Git/Bash prerequisites.
- Public provider docs must not list Copilot as currently supported or runnable.
- Public command docs should describe `/voice` as experimental terminal voice input only, not as a fully supported voice product.
- Public TUI workspace guidance should explain that `/workspace save` saves and switches to an unsaved Git launch directory.
- Public video documentation should cover deterministic HTML animation export, trusted registered audio sources, transcription and deterministic audio analysis, reviewed soundtrack proposals, and the current no-fades/no-looping/no-ducking limits.
- Docs impact: none for managed Designer bounded refinement/failure diagnostics, the Tailscale host-update authorization correction, cryptography dependency correction, plan execution badge cleanup, Desktop sidebar and workspace-launcher responsiveness refinements, or internal test-governance and release-gate wiring.

## v0.1.19 - 2026-05-01

### Changed

- Promoted accumulated `dev` changes to `main` for release `v0.1.19`.
- Included orchestration, remote deploy/update, chat/permission UI, FFF search, and documentation updates.

### Docs impact

- Start public docs verification from this changelog entry and the release notes for `v0.1.19`.
- Audit docs for user-visible orchestration, remote deploy/update, chat/permission UI, FFF search, provider, install, and unavailable-feature claims.

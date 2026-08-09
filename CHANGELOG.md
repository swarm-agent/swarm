# Changelog

All notable Swarm release changes should be recorded here.

Release entries are the source checkpoint for public docs verification. Each entry must include a `Docs impact` section. If a release has no docs-impacting changes, write `Docs impact: none`.

## Unreleased

### Added

- Added workspace Actions with structured inputs, quick-access pins, AI-assisted commit orchestration, and Desktop/TUI management surfaces.
- Added Git-aware workspace controls, worktree integration improvements, AI commit commands, and final handoff links to public pull requests.
- Added account-scoped model favorites and richer model controls, including complete catalog pagination, provider service-tier metadata, and current Google Gemini thinking support.
- Added first-run onboarding support for accepting initial provider credentials.

### Changed

- Reworked durable V3 plan and checkpoint execution so boundary transitions, resumptions, source-message provenance, and conversation context remain in the canonical session epoch.
- Expanded Desktop and TUI workspace onboarding, session routing, themes, responsive navigation, git status, and launch tips while removing legacy display and workspace-definition authorities.
- Hardened release update and systemd relaunch behavior, including non-privileged update handoff, replacement readiness, authorization, and rollback-sensitive restart paths.
- Separated core system agents from utility agents in Desktop and TUI model controls.
- Improved TUI Codex model-profile switching and V3 chat integration, with a maintained helper for copying the local Swarm database for diagnostics.
- Changed worktree-name conflict retries to use random five-digit identifiers.

### Fixed

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

### Docs impact

- Public docs should cover workspace Actions, AI-assisted commits, Git/worktree controls, account-scoped model favorites, and provider service-tier choices.
- Public docs should describe the current durable V3 checkpoint/resume behavior and the updated Desktop/TUI workspace, routing, and onboarding surfaces.
- Public update docs should reflect the hardened non-privileged update, systemd relaunch, readiness, and rollback behavior.
- Public onboarding docs should describe initial provider credential acceptance; agent grouping, model switching, session quarantine, diagnostic tooling, and worktree retry identifiers need no separate user documentation.
- Public docs should describe the system storage contract, Linux root locations, no-silent-migration behavior, and future macOS system-root expectations.
- Public product docs must describe dedicated local containers as retired while retaining V3 and Swarm targets as current critical contracts and future non-local runners as a separate direction.
- Public install docs should point users to the release installer fast lane before source checkout workflows.
- Public provider docs must not list Copilot as currently supported or runnable.
- Public command docs should describe `/voice` as experimental terminal voice input only, not as a fully supported voice product.

## v0.1.19 - 2026-05-01

### Changed

- Promoted accumulated `dev` changes to `main` for release `v0.1.19`.
- Included orchestration, remote deploy/update, chat/permission UI, FFF search, and documentation updates.

### Docs impact

- Start public docs verification from this changelog entry and the release notes for `v0.1.19`.
- Audit docs for user-visible orchestration, remote deploy/update, chat/permission UI, FFF search, provider, install, and unavailable-feature claims.

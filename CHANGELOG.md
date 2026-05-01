# Changelog

All notable Swarm release changes should be recorded here.

Release entries are the source checkpoint for public docs verification. Each entry must include a `Docs impact` section. If a release has no docs-impacting changes, write `Docs impact: none`.

## Unreleased

### Fixed

- Corrected public README install guidance to lead with the latest-release installer fast lane instead of source-checkout installation.
- Removed public README claims that Copilot is currently available as a supported provider. Copilot implementation code remains in the tree, but it is intentionally not registered as a selectable or runnable provider until it can be validated end-to-end with the required paid Copilot plan.
- Reframed `/voice` README guidance as experimental terminal voice input. The terminal STT path has been tested, but voice is not a polished or guaranteed workflow yet.

### Docs impact

- Public install docs should point users to the release installer fast lane before source checkout workflows.
- Public provider docs must not list Copilot as currently supported or runnable.
- Public command docs should describe `/voice` as experimental terminal voice input only, not as a fully supported voice product.

## v0.1.19 - 2026-05-01

### Changed

- Promoted accumulated `dev` changes to `main` for release `v0.1.19`.
- Included Flow, remote deploy/update, chat/permission UI, FFF search, and documentation updates.

### Docs impact

- Start public docs verification from this changelog entry and the release notes for `v0.1.19`.
- Audit docs for user-visible Flow, remote deploy/update, chat/permission UI, FFF search, provider, install, and unavailable-feature claims.

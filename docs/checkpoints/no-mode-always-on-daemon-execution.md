# Checkpoint — no-mode always-on daemon install

Status: checkpointed implementation plan for delegation. This is the cutover from interactive/box startup modes to one product lifecycle: Swarm is installed as an always-on daemon, and CLI/TUI/Desktop are controllers that attach to it.

## Purpose

Remove the product-level startup mode split:

- No `interactive` startup mode.
- No `box` startup mode.
- No UI affordance that suggests quitting the app should shut down the daemon.
- No installer path that quietly creates an unmanaged foreground-only app experience.

Swarm should install once, run continuously, and be managed explicitly through service commands. The controller UI may exit at any time without changing daemon state.

This checkpoint does **not** remove agent execution modes such as `plan`/`auto`; those are runtime permission policies, not product startup modes.

## Product contract

### One lifecycle

Swarm has one normal lifecycle:

1. `swarm install` provisions the system daemon.
2. The daemon starts automatically and survives UI/controller exit.
3. `swarm`, `swarm open`, or `swarm session` attaches to the running daemon.
4. `swarm stop`, `swarm restart`, and `swarm uninstall` are the explicit lifecycle controls.

There is no implicit daemon shutdown from closing the TUI/Desktop controller.

### Controller quit behavior

Quitting the TUI/Desktop controller means "close this controller", not "stop Swarm".

This slice is intentionally narrow: align the old `interactive` TUI-exit path with the current `box` / non-interactive TUI-exit path. Do **not** redesign daemon lifecycle, systemd handling, session shutdown, or controller/bootstrap topology here.

Rules:

- Normal quit exits the controller immediately, using the same controller-only behavior that `box` mode already uses today.
- Force quit is only a client/controller escape hatch.
- The controller must not call daemon shutdown APIs during quit.
- The old prompt asking whether to keep Swarm running or shut it down is removed.
- Stopping the daemon is explicit: `swarm stop` or `swarm uninstall`.

### Installer communication contract

`swarm install` must be clear and NPM-like: before privileged work, print what will happen and where files will go.

It should explain:

- binaries/launchers to install
- systemd unit to write/enable/start
- config/state/runtime paths
- service user/owner model
- whether data will be preserved on reinstall
- how to stop/restart/uninstall
- whether `sudo` is required and why

The installer should support non-interactive automation with an explicit `--yes` flag, but interactive use should show the plan first.

## Canonical CLI surface

Implement or normalize these user-facing commands:

| Command | Meaning |
| --- | --- |
| `swarm install [--yes]` | Install launchers/runtime, provision secure system paths, write systemd unit, enable and start daemon. |
| `swarm uninstall [--purge] [--yes]` | Stop/disable service and remove installed binaries/service files. Preserve daemon data by default; remove data/config only with `--purge`. |
| `swarm start` | Start the installed daemon service. |
| `swarm stop` | Stop the installed daemon service. |
| `swarm restart` | Restart the installed daemon service. |
| `swarm status` | Show service state plus daemon health/readiness. |
| `swarm open` | Open Desktop/browser controller against the running daemon. |
| `swarm session` | Open the TUI controller against the running daemon. |

Compatibility commands may remain temporarily as aliases only if they print deprecation guidance and do not preserve a separate lifecycle path.

## Secure default paths

The system install path remains the production default:

- config: `/etc/swarmd`
- durable daemon state: `/var/lib/swarmd`
- runtime files/socket/pid: `/run/swarmd`
- service unit: `/etc/systemd/system/swarm.service`
- installed runtime: `/usr/local/share/swarm` or the existing canonical runtime install root
- launchers: `/usr/local/bin/swarm`, plus any existing canonical launchers

Do not move production daemon state into home, XDG, workspace, `.local`, or ad-hoc local directories.

## Hard invariants

- There is no user-facing startup mode selector.
- `startup_mode` is not written to new config files.
- New code must not branch on `interactive` versus `box` startup behavior.
- TUI/Desktop quit never stops the daemon.
- Installer actions must be explainable, auditable, and reversible.
- `swarm uninstall` preserves user data by default.
- `swarm uninstall --purge` must clearly warn before deleting daemon state/config.
- Production daemon storage must keep using hardened system paths.
- Any dev/foreground helper must be labeled as a developer/testing path, not as a product mode.

## Regression guardrails

This checkpoint is narrow. We are not redesigning topology, agents, sessions, or permissions.

Avoid these regressions:

1. **Do not create a new mode to replace the old modes.** Remove `interactive` and `box` as product startup modes. The normal product lifecycle is just the installed always-on daemon.
2. **Do not keep "box mode" as user-facing language.** The daemon may technically behave like the old always-on path, but users and config should not see or choose `box`.
3. **Do not let controller exit manage daemon lifecycle.** TUI/Desktop quit or force-quit only closes the controller. It must not ask whether to keep running in the background, and it must not stop the daemon.
4. **Do not break real topology/bootstrap concepts.** Keep child, swarm networking, and container bootstrap behavior. Only remove the startup-mode split.
5. **Do not update config in only one layer.** Go config, shell config generation/validation, tests, VM scripts, and E2Es must stop writing/requiring `startup_mode` together.
6. **Do not leave hidden unmanaged production fallback behavior.** If the installed daemon is not running, CLI should guide the user to `swarm install`/`swarm start`, not silently recreate interactive mode.

## Current-state findings

### Mode abstractions

- `pkg/startupconfig/config.go` defines `ModeInteractive`, `ModeBox`, `startup_mode`, and validates those values.
- `swarmd/internal/config/config.go` maps startup config modes into internal daemon modes such as `single`/`box`.
- `internal/launcher/launcher.go` propagates startup mode through environment/config surfaces.
- `scripts/lib-lane.sh` reads/writes/migrates `startup_mode` and exports mode-related environment.

### Controller quit behavior

Current code facts in `internal/app/quit_modal.go`:

- `interactiveLifecycleEnabled()` returns true for empty/`single` server mode and false for other modes such as `box`.
- `requestQuit()` only opens the quit modal when `interactiveLifecycleEnabled()` is true and running/blocked sessions are present.
- `finalizeQuit(false)` currently diverges by mode:
  - non-interactive / `box`: sets `quitRequested`, posts `interruptQuit`, and exits only the TUI controller.
  - interactive / `single`: calls `shutdownInteractiveDaemonAsync()`.
- `shutdownInteractiveDaemonAsync()` calls `a.api.Shutdown(ctx, "swarmtui exit")`; this is the daemon-lifecycle ownership behavior that must be removed from the TUI quit path.
- The implementation target is simply: make the interactive/single TUI-exit path behave like the existing box/non-interactive path.

### Installer and service path

- `internal/launcher/system_paths.go` already owns system path provisioning and systemd unit rendering.
- `cmd/swarmsetup/main.go` currently installs runtime/launchers and ensures the systemd service unit.
- `cmd/swarm/main.go` currently exposes lower-level launcher/server commands and should become the clear user command surface.
- `install.sh` and `scripts/install-launchers.sh` need installer-plan messaging aligned with the new contract.

## Implementation slices

### Slice 1 — Remove startup mode from config schema

Scope:

- `pkg/startupconfig/config.go`
- `swarmd/internal/config/config.go`
- config-related tests under `tests/pkg` and `tests/swarmd`

Implement:

- Remove exported product startup constants for `interactive` and `box` where possible.
- Remove `Mode`/`StartupMode` fields from canonical config structs.
- Stop writing `startup_mode` in formatted config.
- Treat legacy `startup_mode` in existing config as ignored/deprecated during a transitional read, or fail with a clear migration message if strict removal is safer.
- Remove runtime mapping helpers for `interactive`/`box` startup behavior.

Acceptance:

- New config output contains no `startup_mode`.
- Tests no longer assert interactive/box startup config behavior.
- Existing secure storage path validation still passes.

### Slice 2 — Remove mode plumbing from launchers and scripts

Scope:

- `internal/launcher/launcher.go`
- `scripts/lib-lane.sh`
- `scripts/setup.sh`
- `scripts/install-launchers.sh`
- E2E shell scripts under `tests/swarmd` and `scripts/vm`

Implement:

- Stop exporting `SWARM_STARTUP_MODE`.
- Remove shell helpers that compute or validate startup mode.
- Remove config generation lines that write `startup_mode`.
- Replace mode-specific script messaging with installed-daemon messaging.

Acceptance:

- Grep for `startup_mode`, `SWARM_STARTUP_MODE`, `ModeInteractive`, and `ModeBox` shows only intentional legacy migration comments/tests, or none.
- Shell E2E flows create config without startup mode.

### Slice 3 — Align TUI quit behavior across old startup modes

Scope:

- `internal/app/quit_modal.go`
- `internal/app/app.go`
- existing app/UI tests that cover quit behavior

Current behavior to align:

- Old `box` / non-interactive exit path already does the desired controller-only quit: set `quitRequested`, post `interruptQuit`, return from `Run()`.
- Old `interactive` / `single` exit path is the outlier: it may open the "keep running in background / close sessions" modal and can call `a.api.Shutdown(ctx, "swarmtui exit")` through `shutdownInteractiveDaemonAsync()`.

Implement:

- Change TUI quit so the old interactive/single path uses the same controller-only exit behavior as the existing box/non-interactive path.
- Remove or bypass the quit modal path that asks whether to keep Swarm running in the background or close sessions.
- Remove `shutdownInteractiveDaemonAsync()` and any `a.api.Shutdown(...)` call from TUI/controller quit handling.
- Keep the change local to TUI exit routing; do not rework systemd install/service lifecycle, daemon robustness, session management, or API topology in this slice.
- If a force-quit command remains, keep it controller-only.

Acceptance:

- Pressing quit in either old `interactive`/`single` or old `box`/non-interactive state follows the same controller-only exit path.
- TUI quit does not call `api.Shutdown`.
- No quit UI text references interactive mode, box mode, or background daemon ownership.
- Tests or targeted inspection prove the only remaining daemon shutdown paths are explicit lifecycle commands, not controller quit.

### Slice 4 — Harden `swarm install`

Scope:

- `cmd/swarm/main.go`
- `cmd/swarmsetup/main.go`
- `install.sh`
- `internal/launcher/system_paths.go`
- installer tests/harnesses

Implement:

- Add or normalize `swarm install [--yes]`.
- Print an install plan before privileged changes.
- Provision config/state/runtime dirs through the existing storage contract.
- Install launchers/runtime to canonical paths.
- Write, enable, and start `swarm.service`.
- Print post-install next steps: `swarm status`, `swarm open`, `swarm session`, `swarm uninstall`.
- Fail clearly if systemd or required privileges are unavailable; do not silently fall back to home/XDG production state.

Acceptance:

- Fresh install shows a clear plan, asks for confirmation unless `--yes`, starts the service, and reports status.
- Installer does not create home/XDG daemon state.
- Reinstall is idempotent and explains what is preserved.

### Slice 5 — Add explicit service lifecycle commands

Scope:

- `cmd/swarm/main.go`
- `internal/launcher/system_paths.go` or a new launcher service-management file
- command tests

Implement:

- `swarm start`
- `swarm stop`
- `swarm restart`
- `swarm status`
- `swarm open`
- `swarm session`

Rules:

- Use systemd for installed production daemon lifecycle.
- If the service is not installed, print clear install guidance.
- Do not start an unmanaged production daemon as a hidden fallback.
- Keep any foreground/dev run command explicitly named as dev/test only.

Acceptance:

- `swarm status` combines system service state with daemon health/readiness where possible.
- `swarm open` and `swarm session` attach to the daemon and do not own daemon lifecycle.

### Slice 6 — Add `swarm uninstall`

Scope:

- `cmd/swarm/main.go`
- `internal/launcher/system_paths.go`
- uninstall tests/harnesses

Implement:

- `swarm uninstall [--purge] [--yes]`.
- Print an uninstall plan before changes.
- Stop and disable `swarm.service`.
- Remove service unit and installed launchers/runtime artifacts.
- Preserve `/var/lib/swarmd` and `/etc/swarmd` by default.
- With `--purge`, clearly warn and require confirmation before deleting daemon config/state.

Acceptance:

- Default uninstall is reversible and preserves data.
- Purge requires explicit intent and reports exactly what was removed.
- Partial failures are reported clearly with remediation steps.

### Slice 7 — Docs and migration messaging

Scope:

- `README.md`
- `swarmd/README.md`
- docs under `docs/`
- release notes/changelog if applicable

Implement:

- Document install-once/run-forever lifecycle.
- Document controller semantics: closing UI does not stop daemon.
- Document `install`, `uninstall`, `start`, `stop`, `restart`, `status`, `open`, `session`.
- Remove or rewrite references to interactive/box mode.
- Include migration note for existing installs with old `startup_mode` config.

Acceptance:

- New users can understand what is installed before running it.
- Existing users understand why old modes disappeared and what commands replace them.

### Slice 8 — Gates and proof

Run after each slice when applicable:

- `go test ./...` for touched Go modules/packages, narrowed where needed.
- shell script checks relevant to install/storage hardening.
- `scripts/check-daemon-storage-paths.sh`.
- targeted grep gate for removed product modes.

Suggested grep gate:

```sh
grep -RIn --exclude-dir=.git --exclude-dir=.cache --exclude-dir=dist \
  -e 'startup_mode' \
  -e 'SWARM_STARTUP_MODE' \
  -e 'ModeInteractive' \
  -e 'ModeBox' \
  -e 'interactive mode' \
  -e 'box mode' \
  .
```

Any remaining hits must be intentional migration/deprecation text or unrelated agent execution-mode language.

## Delegation prompts

### Delegate A — config/mode removal

Inspect and edit startup config and daemon config so product startup modes are removed. Eliminate new `startup_mode` writes, remove interactive/box validation, update tests, and preserve secure storage defaults. Return changed files, tests run, and remaining intentional legacy references.

### Delegate B — align TUI quit behavior

Inspect `internal/app/quit_modal.go` and `internal/app/app.go`. Identify the exact current divergence: box/non-interactive TUI exit already sets `quitRequested` and posts `interruptQuit`, while interactive/single can show the quit modal and call `a.api.Shutdown(ctx, "swarmtui exit")`. Edit only enough to make interactive/single quit use the same controller-only path as box/non-interactive. Do not redesign systemd/daemon lifecycle or session management. Return changed files, tests run, and proof that TUI quit no longer calls daemon shutdown.

### Delegate C — installer/service CLI

Implement or normalize `swarm install`, `uninstall`, `start`, `stop`, `restart`, `status`, `open`, and `session` around the installed systemd daemon. Add install/uninstall plan output, confirmation/`--yes`, service enable/start, and preserve-by-default uninstall. Return changed files, tests run, and manual verification steps.

### Delegate D — scripts/docs/gates

Remove mode references from scripts/docs/E2E harnesses and update documentation to the install-once/run-forever model. Add/verify grep and storage hardening gates. Return changed files, tests run, and remaining references that require product decision.

## Relevant filepaths

- `pkg/startupconfig/config.go`
- `swarmd/internal/config/config.go`
- `internal/launcher/launcher.go`
- `internal/launcher/system_paths.go`
- `cmd/swarm/main.go`
- `cmd/swarmsetup/main.go`
- `install.sh`
- `scripts/lib-lane.sh`
- `scripts/setup.sh`
- `scripts/install-launchers.sh`
- `scripts/check-daemon-storage-paths.sh`
- `internal/app/quit_modal.go`
- `internal/app/app.go`
- `README.md`
- `swarmd/README.md`
- `tests/pkg/startupconfig/config_test.go`
- `tests/swarmd/internal/config/config_test.go`
- `internal/launcher/launcher_test.go`
- `tests/swarmd/*.sh`
- `scripts/vm/*.sh`

# Swarm-Go Agent Contract

This repository is public. Treat every change as if it will be reviewed by strangers on GitHub.

If a rule below conflicts with convenience, the rule wins.

## 1. Non-Negotiable Public Repo Rules

- Never commit secrets.
  - No API keys, tokens, cookies, OAuth artifacts, private keys, `.env` values, or auth dumps.
  - Do not paste real credentials into docs, examples, tests, fixtures, screenshots, or comments.
- Never commit personal or machine-specific identifiers.
  - No local usernames.
  - No workstation-specific absolute paths.
  - No references to a developer's home directory.
  - No machine-specific hostnames, tokens, or private internal URLs unless they are intentional public product defaults.
- Never hardcode local paths in runtime code, scripts, tests, or docs.
  - Use XDG-aware paths, `os.UserHomeDir`, `filepath.Join`, `filepath.Clean`, `filepath.Abs`, `mktemp`, and `os.MkdirTemp` as appropriate.
- Never add silent fallback behavior that hides real failures.
  - Fail clearly and explain what is missing.
- Never keep two behavior paths when one canonical path should exist.
  - Do not add legacy paths, compatibility forks, or duplicate flows unless explicitly required.
- Never commit junk.
  - No accidental build outputs, local caches, scratch notes, debug dumps, or throwaway artifacts in tracked areas.

## 2. Task Execution Policy

- If the user asks for a task, do the task directly.
- Branch workflow is mandatory:
  - Stay on `dev` for all normal work.
  - Make changes on `dev`, commit on `dev`, and push `dev`.
  - Open pull requests from `dev` into `main`.
  - Do not create ad-hoc PR branches such as `pr/*`, `probe/*`, or other workaround branches unless the user explicitly asks for that exact branch.
  - Do not cherry-pick `dev` work onto another branch as a workaround for PR creation.
  - Do not switch the working tree away from `dev` just to prepare, test, or open a PR.
  - Do not commit directly on `main`, merge into `main`, or push `main` unless the user explicitly asks for that exact action.
  - If branch history or PR state is broken, stop and explain the exact issue before creating branches, cherry-picking, merging, rebasing, or deleting anything.
  - Prefer read-only inspection commands such as `git status`, `git branch -vv`, `git log`, `git show`, and `git diff` before any branch mutation.
- Do not run `go test` or other test suites unless the user explicitly asks for tests.
- For non-commit work, do not run validation unless the user explicitly asks for it.
- Vulnerability/CVE scanning is mandatory before every commit.
- Immediately before any commit, run:

```bash
./scripts/check-precommit.sh
```

- Immediately before any GitHub container/package publish or remote-deploy image push, run:

```bash
./scripts/check-container-publish.sh --runtime docker -- \
  --ssh-target <target> \
  --transport-mode <tailscale|lan>
```

- `./scripts/check-precommit.sh` includes path, secrets, policy, and vulnerability scans.
- Vulnerability scanning includes Go module scans and npm advisory checks for the web lockfile.
- `./scripts/check-precommit.sh` must skip tests by default.
- `./scripts/check-container-publish.sh` is the container publish gate.
  - It runs `./scripts/check-launch-readiness.sh --require-clean`, which in turn runs `./scripts/check-precommit.sh` and the CVE checks.
  - It verifies `.dockerignore` excludes local-only build-context paths.
  - It builds the image through `scripts/rebuild-container.sh --image-only`.
  - It inspects the built image for forbidden local-only paths such as `.git`, `.cache`, `.env`, `.docker`, and `.ssh`.
  - It then runs the checked-in `tests/swarmd/remote_deploy_e2e.sh` harness with routed proof and teardown.
  - Do not publish containers or GitHub packages until this script passes.
- For the container publish gate, raw secrets must come from env-name flags consumed by the checked-in harnesses.
  - Never put real auth keys, provider keys, cookies, or tokens directly on the command line.
  - Never store those values in committed files, startup configs, screenshots, or docs.
- Only run tests in precommit when the user explicitly asks for tests.
- If tests or validation were not requested, say so explicitly.

## Current Active Testing Focus

- Replicate and host/child harness map. Do not improvise around these:
  - `tests/swarmd/local_replicate_e2e.sh`
    - Checked-in live runner for the real local `/v1/swarm/replicate` path.
    - Use this instead of hand-running the replicate flow.
  - `tests/swarmd/local_replicate_recovery_e2e.sh`
    - Checked-in live runner for Stage 4 local recovery on top of the real `/v1/swarm/replicate` path.
    - Reuses the same host root, creates one routed session, and drives host/child restart scenarios.
  - `tests/swarmd/remote_deploy_e2e.sh`
    - Checked-in live runner for the current remote `ssh + tailscale` path.
    - Uses an isolated host root, creates one or more remote deploy sessions for SSH targets, prints/polls the Tailscale auth URLs, auto-approves attach once children enroll, and supports optional routed-AI proof plus teardown.
    - Manual-auth and launch-only auth-key modes now both exist in the harness.
    - Auth-key mode requires `--tailscale-auth-key-env`; the raw key must come from env only.
  - `tests/swarmd/remote_deploy_recovery_e2e.sh`
    - Checked-in live runner for remote Tailscale recovery on top of the real SSH deploy path.
    - Reuses a real remote deploy host root, creates one routed session, and drives:
      - child restart with host still up
      - host restart with child still up
      - both down, host first
      - both down, child first
    - Optional routed-AI proof is supported after recovery.
  - `tests/swarmd/container_startup_e2e.sh`
    - Container startup harness.
    - Useful for container bring-up checks; it is not the main replicate matrix.
  - `swarmd/internal/api/swarm_replicate_test.go`
    - Unit/API coverage for `/v1/swarm/replicate` request handling.
  - `swarmd/tests/internal/deploy/sync_credentials_test.go`
    - Backend sync-credential coverage used by the local child/host sync path.

- Source of truth for local replicate behavior is the checked-in live harness set and nearby source/tests.
- If chat memory, this file, or older notes disagree, the code and live harnesses win.
- Remote deploy is a separate track from local replicate.
- Remote transport direction:
  - supported target shape is `ssh + tailscale` or `ssh + lan/wireguard`
  - do not build or assume `ssh` as the persistent runtime transport in this track
- Current remote SSH deploy truth:
  - the major speed bottleneck on `ssh + tailscale` was real and is now narrowed:
    - cold remote deploy start on a fresh image path was measured at `188649 ms`
    - warm remote deploy starts on the same host after image cache/load were measured at `9471 ms`, `10077 ms`, and `9482 ms`
  - the speed win came from the remote deploy service reusing a content-versioned child image and cached local tar export instead of rebuilding/exporting/copying/loading the image every time
  - the manual-auth `ssh + tailscale` path is now proven end to end on the Hetzner SSH test host:
    - the checked-in `tests/swarmd/remote_deploy_e2e.sh` harness is now proven for the single-child manual-auth path
    - remote child attached back to the host over Tailscale
    - routed remote session executed from the host
    - `exit_plan_mode` permission appeared on the host, host approval switched the session to `auto`, and the host transcript received `I got out.`
    - `bash pwd` permission appeared on the host, host approval unblocked the remote child, and the host transcript received `/workspaces`
    - the remote child runtime workspace was confirmed at `/workspaces`
    - the harness `--teardown-only` cleanup path is now also exercised on the same host root
  - `R1-02` two-child manual-auth remote proof is now also complete on the real SSH path:
    - both remote children attached and executed routed sessions from the host
    - both children produced host-visible `exit_plan_mode` and `bash pwd` approvals
    - both children returned the expected host transcript replies
  - `R2-01` auth-key remote launch is now partially proven on the real SSH path:
  - `R2-01` auth-key remote launch is now proven on the real SSH path:
    - the remote modal/backend path accepts a launch-only Tailscale auth key
    - the checked-in harness accepts `--tailscale-auth-mode key`
    - a real single-child SSH deploy with the auth key no longer required browser login
    - the remote child came up with a real `child_swarm_id` and `remote_tailnet_url`
    - host-side pairing now waits for the fresh child tailnet endpoint to answer readiness before pairing
    - the raw auth key was not written to Pebble, startup config, or saved artifacts
    - the single-child routed AI proof is complete on the auth-key path
  - `R2-02` auth-key remote launch is now proven for two SSH children on one host:
    - both children launched without browser auth
    - both children attached back to the same host over Tailscale
    - both accepted host-routed session creation
    - both produced host-visible `exit_plan_mode` and `bash pwd` approvals
    - host approval unblocked both children and both host transcripts ended with `/workspaces`
  - `R1-03` manual-auth Arch coverage is now proven on the Arch SSH target using Podman:
    - remote child attached successfully over Tailscale
    - routed host -> remote child session creation succeeded
    - host received and resolved `exit_plan_mode`
    - host transcript received `I got out.`
    - host received and resolved `bash pwd`
    - host transcript received `/workspaces`
  - `R2-03` auth-key Arch coverage is now proven on the same Arch SSH target using Podman:
    - browser login is gone on the Arch auth-key path
    - the child attached over Tailscale using the launch-only auth key
    - the routed AI proof completed end to end
    - the host transcript again received `/workspaces`
  - current remote runtime-path truth:
    - the SSH remote harness archives Git-tracked repo contents directly into the configured payload `target_path`
    - with the current harness inputs, the remote runtime workspace root is `/workspaces`, not `/workspaces/<workspace_name>`
    - remote routed proof assertions must use the payload `target_path`, not a hardcoded workspace subdirectory
  - current harness fix from the Arch rows:
    - `tests/swarmd/remote_deploy_e2e.sh` now waits for assistant-role replies only when proving transcript content
    - this avoids false passes caused by matching the expected string inside tool-history messages
  - current tracked remote state bug:
    - after remote attach, isolated-host `GET /v1/swarm/state` can still report `node.role=child` and `pairing_state=pending_approval`
    - duplicate child entries were also observed during the Arch manual-auth lane
    - routed execution still worked, so this does not block `R1-03`/`R2-03`, but it must be fixed before generic reachable-endpoint work
  - remote harness secret handling is tightened:
    - `tests/swarmd/remote_deploy_e2e.sh` now sends JSON bodies from temp files instead of `curl --data ...`
    - do not regress this; raw Tailscale auth keys and provider API keys must not appear in local `ps` output during harness runs
  - concrete bug exposed during `R1-02`:
    - if a remote child restarts or re-logins and comes back with a new Tailscale URL, the host remote-deploy record can keep the stale `remote_tailnet_url`
    - in that state the child is healthy, but host-side re-pair does not retry against the child's new tailnet URL
    - this was proven by manually reissuing the remote pairing request to the child, after which attach and routed execution completed normally
  - current code fix for that bug:
    - remote deploy now tracks the last child tailnet URL used for `/v1/swarm/remote-pairing/request`
    - if the child reports a different tailnet URL after restart/login, the host will automatically reissue remote pairing instead of staying stuck at the stale endpoint
    - regression coverage lives in `swarmd/internal/remotedeploy/service_test.go`
  - teardown after remote runs:
    - stop the isolated local host lane
    - remove the temp host root
    - disable/remove remote child systemd units
    - remove remote child containers and remote deploy dirs on the SSH hosts
  - do not put tmp credential values, auth URLs, or recovered secrets into docs, fixtures, or commits
  - remote Tailscale recovery on the real SSH path is now green with the checked-in recovery harness:
    - `RR-01` child restart with host still running: `PASS`
    - `RR-02` host restart with child still running: `PASS`
    - `RR-03` both down, host first: `PASS`
    - `RR-04` both down, child first: `PASS`
  - remote sync-vault is now proven on the real `ssh + tailscale` path:
    - the remote child imported the host Fireworks credential in `pebble/vault` mode without any direct child credential seed
    - routed host -> remote child session creation succeeded after sync
    - host received and resolved `exit_plan_mode`
    - host received and resolved `bash pwd`
    - the remote child reply landed on the host transcript with `/workspaces/swarm-go`
  - the key remote sync-vault fix was:
    - remote deploy host approval now finalizes the child pairing state to `paired`
    - before that fix, the child stayed stuck in `pending_approval`, so the managed credential sync loop never ran
  - the fixes that made remote recovery pass were:
    - auth-key remote children are now started under systemd with a temporary `TAILSCALE_AUTHKEY` environment instead of being launched outside systemd
    - remote child state now persists across restarts via mounted `/var/lib/tailscale` and `/var/lib/swarmd`
    - remote recovery harness now auto-approves pending enrollment on reused host roots
    - remote recovery harness now retries post-restart routed run start through transient `502/503` reconnect races
    - daemon startup no longer fatally exits when lifecycle or pending-permission reconciliation hits transient remote transport failures during restart
  - next remote work after recovery:
    - cover the manual bootstrap/attach path from a fresh machine
    - cover the one-shot new-machine bootstrap flow from zero
    - then cover the generic reachable-endpoint path for user-managed networking such as WireGuard or tunnels
  - yesterday's remote follow-up list that still stands after auth-key work:
    - SSH child poweroff/poweron reconnect
    - host restart with remote child still running
    - both sides down with host first
    - both sides down with child first
    - manual bootstrap script path where a fresh child sends a pending attach request to the main host
    - one-shot brand-new machine from zero proof
  - remote modal direction after the current proof:
    - keep `Tailscale` as the first-class managed option
    - collapse everything else into one generic second option
    - generic means the user provides a reachable host/child path over IP
    - if the user uses WireGuard, a VPN, or a tunnel, they manage that networking themselves; Swarm does not set it up for them in this track
    - the modal copy should say this plainly instead of implying Swarm manages non-Tailscale remote networking
- Current local `/v1/swarm/replicate` status:
  - Stage 2 vaulted local sync/recovery is now green on the checked-in harnesses.
  - Stage 4 local recovery is now green on the checked-in harnesses.
  - Use `tests/swarmd/local_replicate_e2e.sh` and `tests/swarmd/local_replicate_recovery_e2e.sh` as the source of truth instead of replaying the flow by hand.
- Current routed-child truth:
  - Fresh Docker loopback replicate attach/finalize is green again on the checked-in harness after fixing the host `/v1/deploy/container/attach/approve` JSON field mismatch for peer-auth tokens.
  - A real routed child session on the fresh Docker replicate path now has live host-side approval proof:
    - `exit_plan_mode` permission appeared on the host
    - approval on the host unblocked the child
    - the child reply landed on the host transcript
    - a second routed `bash` approval also appeared on the host and the child reply landed on the host transcript
  - This proves host permission routing and host transcript landing on the real Docker `/v1/swarm/replicate` path.
  - Refresh/reopen and host-restart proof for that same fresh routed session still need a separate rerun before claiming the reload blocker is fully closed.
- In this Codex environment, `tests/swarmd/local_replicate_e2e.sh` can finish successfully while the isolated host dies when the shell exits.
  - For post-harness live API work, relaunch the same host root in a persistent PTY using the XDG paths from `host-summary.json`.
- Dedicated VM lane for local container/replicate testing:
  - Keep one reusable Linux VM profile named `swarm-harness` for local harness work.
  - Treat that VM as the default place to run local container networking, attach, managed-sync, and replicate harnesses whenever the main workstation swarm or fixed local ports would conflict.
  - The goal is a real Linux guest on KVM/QEMU, not container-in-container.
  - The guest should run its own `swarmd`, container runtime, ports, and worktree so host-side Swarm usage does not block harness execution.
  - Preferred use: keep the workstation Swarm running for normal use; run `tests/swarmd/local_replicate_e2e.sh` and `tests/swarmd/local_replicate_recovery_e2e.sh` inside `swarm-harness`.
  - Canonical entrypoint: `./scripts/swarm-harness-vm.sh` (`doctor`, `install-host-deps`, `provision`, `sync`, `local-replicate`, `local-replicate-recovery`).
  - The VM lane uses explicit `rsync` into the guest instead of host bind mounts so harness testing does not mutate host workspace ownership.
  - If a local harness result depends on container networking or host/child attach behavior, prefer the VM lane over trying to fight host-port collisions on the main machine.
- Current completed rows:
  - `P0-01` Podman, `127.0.0.1`, default/lane, single child: `PASS`
  - `P0-02` Docker, `127.0.0.1`, default/lane, single child: `PASS`
  - `P0-03` Podman, `127.0.0.1`, explicit manual host port, single child: `PASS`
  - `P0-04` Docker, `127.0.0.1`, explicit manual host port, single child: `PASS`
  - `P1-01` Podman, explicit private/LAN host, default/lane, single child: `PASS`
  - `P1-02` Docker, explicit private/LAN host, default/lane, single child: `PASS`
  - `P1-03` Podman, explicit private/LAN host, explicit manual host port, single child: `PASS`
  - `P1-04` Docker, explicit private/LAN host, explicit manual host port, single child: `PASS`
  - `P2-01` Podman, `127.0.0.1`, default child port occupied, single child: `PASS` (expected hard failure)
  - `P2-02` Docker, `127.0.0.1`, default child port occupied, single child: `PASS` (expected hard failure)
  - `P2-03` Podman, explicit private/LAN host, explicit manual child port occupied, single child: `PASS` (expected hard failure)
  - `P2-04` Docker, explicit private/LAN host, explicit manual child port occupied, single child: `PASS` (expected hard failure)
  - `P3-01` Podman, `127.0.0.1`, default/lane, 2-child: `PASS`
  - `P3-02` Docker, `127.0.0.1`, default/lane, 2-child: `PASS`
  - `P3-03` Podman, explicit private/LAN host, default/lane, 2-child: `PASS`
  - `P3-04` Docker, explicit private/LAN host, default/lane, 2-child: `PASS`
  - `P3-05` Podman, explicit private/LAN host, default/lane, 3-child: `PASS`
  - `P3-06` Docker, explicit private/LAN host, default/lane, 3-child: `PASS`
  - `P3-07` Podman, explicit private/LAN host, explicit manual host ports, 2-child: `PASS`
  - `P3-08` Docker, explicit private/LAN host, explicit manual host ports, 2-child: `PASS`
- Current completed sync rows:
  - `S1-01` Docker, plain host, loopback, `sync.enabled=true`: `PASS`
  - `S1-02` Podman, plain host, loopback, `sync.enabled=true`: `PASS`
  - `S1-03` Podman, plain host, loopback, offline child catch-up: `PASS` for auth-state convergence on restart
  - `S1-04` Podman, plain host, loopback, one host with 3 sync-enabled children: `PASS`
  - `S1-05` Docker, plain host, loopback, one host with 3 sync-enabled children: `PASS`
- Current Stage 2 rows:
  - `S2-01` Docker, vaulted host, loopback, host already unlocked, sync-enabled replicate: `PASS`
  - `S2-02` Docker, vaulted host, loopback, host locked, replicate entry behavior: `PASS`
  - `S2-03` Podman, vaulted host, loopback, host already unlocked, sync-enabled replicate: `PASS`
  - `S2-04` vaulted child restart/local unlock behavior: `PASS`
- Current Stage 3 routed rows:
  - `S3-03` Docker, fresh loopback replicate, real routed child approvals/transcript on host: `PASS`
- Current Stage 4 recovery rows:
  - `S4-01` local Docker, host restart with child still running: `PASS`
  - `S4-02` local Docker, child restart with host still running: `PASS`
  - `S4-03` local Docker, host and child both down, host comes back first and recovers local child deployment: `PASS`
  - `S4-04` local Docker, both down and child already running before host returns: `PASS`
- Immediate next routed follow-ups:
  - prove `S3-01` refresh/reopen on the same fresh Docker replicate path with no child reroute delay
  - prove host restart persistence for that same fresh routed child session
  - only after that, turn on timing/logging to measure the remaining host->child latency chain
- Current implementation focus is split into four stages:
  - Stage 1: plain-host managed auth sync propagation.
  - Stage 2: vaulted-host managed auth behavior and recovery.
  - Stage 3: routed child conversation authority on the host.
  - Stage 4: local host/child recovery and reconnect behavior.
- Stage 1 scope:
  - host add/update/delete/activate changes must propagate to existing children
  - offline children must catch up on restart/reconnect
  - do not claim vaulted-host support in this stage
- Stage 2 is explicitly separate:
  - vaulted host unlocked vs locked is a different operational path
  - child unlock/recovery must stay local to the child
  - do not use the sync bundle password as the child vault password
  - use the user-supplied vault password transiently during bootstrap only; do not persist it in startup config or Pebble
- Current sync truth:
  - initial managed credential bootstrap into a new child works
  - ongoing add/update/delete/activate propagation is proven on the Docker and Podman loopback plain-host paths
  - the forward non-vault path now stores auth state as `pebble/encrypted`; new installs no longer rely on the old plain-at-rest credential path
  - a real child Fireworks run succeeded before and after switching the active key
  - Podman offline-child auth catch-up is proven on restart
  - one host pushing auth add/activate/delete to 3 sync-enabled Docker children is proven
- Current local recovery truth on the real `/v1/swarm/replicate` path:
  - full local Docker recovery suite `S4-01` through `S4-04` is green with `tests/swarmd/local_replicate_recovery_e2e.sh --runtime docker --scenario all`
  - if the child `swarmd` binary changed, do not use `--skip-image-rebuild` for Stage 4 validation; child restart scenarios must run against a rebuilt canonical child image or they will exercise stale container code
  - one host pushing auth add/activate/delete to 3 sync-enabled Podman children is proven
  - all three Docker children still answered a real Fireworks `hello` run after the host switched active credentials and deleted the old key
  - fresh Podman children 2 and 3 still answered a real Fireworks `hello` run after the host switched active credentials and deleted the old key
  - the attempted post-restart Fireworks run on the restarted Podman child failed on container DNS (`10.89.0.1` connection refused), which looks like a Podman restart/runtime issue rather than a sync-state regression
  - Docker private-host multi-container proof is now complete on this host using the existing private bridge `172.17.0.1`
  - Podman private-host multi-container proof is now also complete on this host using the existing private bridge `172.17.0.1`
  - The Podman fix was to enable the same-host local transport socket for concrete host binds and pass it through to local child containers, so attach/bootstrap stays on the Unix socket while the configured private callback host remains `172.17.0.1`
  - the unlocked-host vaulted Docker path is now proven on the real loopback replicate flow:
    - host vault enabled before adding the key
    - host key stored as `pebble/vault`
    - exact raw key absent from host and child data roots
    - child ended as `pebble/vault`
    - add/activate/delete propagated
    - real child Fireworks `hello` runs succeeded before and after the switch
  - the vaulted bootstrap handoff now uses the user-supplied vault password transiently for export/import on the local path; it is not stored in child startup config or Pebble as durable sync state
  - locked-host replicate behavior is now proven: a locked vaulted host returns `423` and requires host-local unlock before `/v1/swarm/replicate`
  - vaulted Podman coverage is now proven on the checked-in local replicate harness
  - vaulted child restart/local unlock behavior is now proven on the checked-in local recovery harness
  - host unlock now re-unlocks managed local vaulted children on the same root; a child that restarts while the host remains down stays locked until host return or manual child unlock
  - the pre-fix plain -> vault WAL-residue issue is legacy-only; for forward installs the product path is default `pebble/encrypted` or fresh `pebble/vault`, and that old migration path is not a current launch blocker
- Stay on the sync path until the matrix says otherwise.
- Do not jump ahead to SSH while the active vaulted-host sync work is unresolved.
- Normal workstation testing must stay loopback-only unless the matrix row explicitly says otherwise.
- Do not use `0.0.0.0` for normal host testing.
- For `P1` rows, use a deliberate private/LAN address only. Do not use public or wildcard addresses.
- The earlier explicit private-host proof used a temporary RFC1918 test interface with `host` and `advertise_host` pinned to the same concrete private IP.
- The later Docker-only private-host multi-container proof on this host used the existing private bridge `172.17.0.1`.
- The later Podman private-host multi-container proof on this host also used the existing private bridge `172.17.0.1`, but bootstrap ran through the mounted local transport socket while host callback resolution still stayed pinned to `172.17.0.1`.
- The local allocator fix now allows default-path multi-container replicate to advance from `7782` / `7783` to `7784` / `7785` only when the earlier pair belongs to a running managed child; external/manual-port collisions still fail clearly.
- Earlier Podman harness failures exposed orphaned helper-process leaks after cleanup (`rootlessport` / `conmon` / child `swarmd`); kill any such leak before the next row and verify the fixed harness leaves teardown clean.
- After every run, verify teardown: no swarm listeners, no leaked containers, no leftover swarm/container helper processes.

## 3. Repository Map

Understand the repo before editing.

### Root module
Primary CLI / launcher / TUI workspace.

Important areas:
- `cmd/` — root-module binaries and entrypoints
- `internal/` — root-module implementation packages
- `pkg/` — reusable public packages
- `README.md` — top-level product/dev workflow overview

### `swarmd/`
Backend daemon module. This is the backend authority/runtime.

Important areas:
- `swarmd/cmd/` — backend binaries
- `swarmd/internal/` — backend implementation
- `swarmd/tests/` and top-level `tests/swarmd/` — backend tests and integration coverage
- `swarmd/README.md` — backend/API/dev script context

### `web/`
Browser/desktop web client.

Important areas:
- `web/src/` — frontend source
- `web/scripts/` — frontend-specific scripts
- `web/README.md` — web/dev-server context

### Search runtime / vendored native dependency
- `internal/fff/`
- `swarmd/internal/fff/`

These contain the vendored FFF bindings/runtime used by the canonical in-app `search` tool. Treat these directories as intentional product dependencies, not random binary junk.

### Scripts
- `scripts/` — root-level build/dev/audit/release helper scripts
- `swarmd/scripts/` — backend dev helper scripts

Do not add random debug/demo scripts casually. Keep only scripts that are intentional, reusable, and worth carrying in a public repo.

### Tests
Canonical direction is tests under `tests/`.

Rules:
- Do not add new `_test.go` files under runtime/package directories unless the repo explicitly already requires that pattern for a touched area.
- Prefer adding new coverage under `tests/`.
- Legacy colocated tests are migration debt, not the desired future state.

## 4. Safe Throwaway / Scratch Locations

Some paths are gitignored and are acceptable for temporary local artifacts, investigation output, or scratch work.

Safe local throwaway areas:
- `tmp/`
- `.cache/`
- `.runtime/`
- `.swarm/`
- `.tools/`
- `.tmp-tools/`
- `sandbox-cache/`

Rules for scratch usage:
- Treat these paths as local-only piles, not canonical product storage.
- `docs/` may be used for local fast notes/plans during cleanup; do not assume it is public/canonical unless the user explicitly keeps it.
- Do not make runtime behavior depend on files living there unless that path is an intentional, documented product path.
- Do not reference scratch artifacts from committed docs as if they are permanent sources of truth.
- Before finishing a cleanup task, make sure throwaway artifacts are not being accidentally staged.

## 5. Cleanup / Public-Repo Hygiene Rules

When the user wants a repo cleanup, reset, or fresh-history push, default toward a minimal public tree.

Required behavior:

- Remove fast notes, temporary plans, audit scratchpads, private cleanup logs, and similar working files unless the user explicitly wants them kept.
- Prefer gitignored local note areas over tracked cleanup/audit writeups.
- Treat `audit/`, `docs/`, and one-off checklist files as removable if they were created for rapid internal cleanup work and are not clearly product/user documentation.
- Keep the public tree focused on product code, required scripts, intentional tests, and intentional user-facing docs.
- Before claiming the tree is clean, check for secrets, personal names, local usernames, machine-specific paths, generated outputs, caches, and random scratch files.
- If in doubt during a cleanup-for-publication pass, remove it now and re-add a cleaner version later.

## 6. Architecture Rules

- Provider-specific behavior belongs in provider adapter/runner packages, not generic orchestration paths.
- Shared run/session/auth flows should remain provider-agnostic where possible.
- New functionality should be additive and modular.
- Do not grow god-files.
  - Split large frontend files by responsibility.
  - Keep transport, parsing, state, and rendering concerns separated.
- Keep one canonical websocket/state path for the web workspace surfaces.

## 7. Search and Native Dependency Rules

- FFF is the canonical in-app search backend.
- The vendored shared library under `internal/fff/` and `swarmd/internal/fff/` is intentional.
- Do not delete, rename, or "clean up" those binaries as junk without verifying packaging/runtime impact.
- Any release/sanitization work must verify tracked shared libraries and other binaries for both:
  - intentionality
  - absence of secrets or personal data

## 8. How the Agent Should Work

Use this workflow on every implementation task:

1. Discover before editing.
   - Use `list`, `search`, and `read` first.
   - Read the relevant README and nearby code before changing behavior.
2. Scope tightly.
   - Fix the requested problem fully.
   - Do not wander into unrelated refactors unless the user asks.
3. Keep behavior deterministic.
   - Prefer one canonical path.
   - Prefer explicit failures over hidden fallback.
4. Preserve portability.
   - Use env-aware and OS-portable path handling.
5. Report honestly.
   - State what changed.
   - State what was not run.
   - Do not claim validation you did not perform.
6. For ship-readiness audits, keep the canonical outputs current.
   - Update the tracked audit docs/checklists instead of leaving findings only in chat.
   - When classifying files/packages, prefer explicit written rationale over implied intent.
   - Keep the audit aligned to repo clearing, shipped artifact cleanliness, and critical pre-build scanning readiness.
   - Keep AGENTS.md aligned with the actual current gate and audit state when the process materially changes.
   - Do not turn the audit into a mandatory dead-code yak shave unless the code affects shipped artifacts or creates audit ambiguity.

## 9. Required Final Response Content

For implementation tasks, the final response should include:
- brief problem restatement
- files changed
- behavioral impact
- validation actually run, if any
- remaining risks or follow-ups

For ship-readiness audit tasks, the final response should also include:
- audit phase completed
- audit files created/updated
- blockers newly identified or cleared
- explicit next phase

## 10. When in Doubt

If you are unsure whether something belongs in the public repo, be conservative:
- prefer removing local-only noise
- prefer generic examples
- prefer documented canonical paths
- prefer explicit user confirmation for product-behavior changes
- never guess about secrets or binary intentionality

Keep the repo clean, portable, and safe to publish.

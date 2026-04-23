# Swarm harness VM

`swarm-harness` is the canonical isolated Linux guest for local container, attach, managed-sync, and replicate testing.

Use it when the main workstation Swarm is already running or when fixed local harness ports would collide with your normal machine state.

## Why this lane exists

- keeps the workstation Swarm usable while harnesses run elsewhere
- avoids host port collisions from checked-in local harness defaults
- avoids bind-mount ownership mutation on the host
- gives Swarm a repeatable way to create its own safe test environment
- keeps local container/attach behavior on a real Linux guest, not container-in-container

## Safety properties

- guest access is loopback-only SSH forwarding
- repo sync is explicit `rsync`, not a live writable bind mount
- guest runtime state lives under XDG paths, not tracked repo paths
- the script refuses KVM-less boot by default unless you explicitly allow slow TCG fallback
- host and guest package installation stays explicit and script-driven

## Host prerequisites

Ubuntu/Debian host packages:

```bash
./scripts/swarm-harness-vm.sh install-host-deps
```

If `/dev/kvm` is not writable, add your user to the `kvm` group and log out/in:

```bash
sudo usermod -aG kvm "$USER"
```

You can verify readiness with:

```bash
./scripts/swarm-harness-vm.sh doctor
```

## Canonical setup flow

Use the singular reusable VM setup path:

```bash
./scripts/swarm-harness-vm.sh setup
```

That is the canonical cold-start 0→1 path. It runs doctor, creates or reuses the `swarm-harness` VM, bootstraps guest packages, verifies required guest tools, syncs the repo, and prints the exact tracked VM details at the end.

If the VM is already created and you want the fastest repeat-use path, use:

```bash
./scripts/swarm-harness-vm.sh fast
```

If you want a fresh guest without paying the full cold-start cost again, use:

```bash
./scripts/swarm-harness-vm.sh reset
./scripts/swarm-harness-vm.sh fast
```

That reuses the downloaded Ubuntu image and the cached post-bootstrap guest image, so the next boot starts from a clean guest state without re-running the giant package install.

If you truly want to wipe every cached VM asset and start from absolute zero, use:

```bash
./scripts/swarm-harness-vm.sh nuke
./scripts/swarm-harness-vm.sh setup
```

That will:

1. download an Ubuntu cloud image
2. create the `swarm-harness` VM assets
3. boot the guest with loopback-only SSH
4. install guest prerequisites (`podman`, `docker.io`, `git`, `jq`, `rsync`, `npm`, build tools)
5. cache that post-bootstrap guest as the reusable fresh-reset baseline
6. rsync the current repo checkout, including `web/node_modules` when present, into `~/swarm-go` inside the guest

On later runs, `provision` reuses the existing bootstrap stamp and skips the apt/package step unless you explicitly force it:

```bash
./scripts/swarm-harness-vm.sh provision --rebootstrap
```

## Common commands

Print the tracked reusable VM details:

```bash
./scripts/swarm-harness-vm.sh track
```

Check state:

```bash
./scripts/swarm-harness-vm.sh status
```

Open a shell:

```bash
./scripts/swarm-harness-vm.sh shell
```

Resync the repo:

```bash
./scripts/swarm-harness-vm.sh sync
```

Inspect recent VM logs quickly:

```bash
./scripts/swarm-harness-vm.sh logs
```

Force guest package bootstrap again:

```bash
./scripts/swarm-harness-vm.sh bootstrap --rebootstrap
```

Run an arbitrary guest-side command from the repo root:

```bash
./scripts/swarm-harness-vm.sh run -- pwd
```

If you already know the guest checkout is current, skip rsync explicitly:

```bash
./scripts/swarm-harness-vm.sh run --no-sync -- pwd
```

Run the canonical local replicate harness inside the VM:

```bash
./scripts/swarm-harness-vm.sh local-replicate -- --runtime podman
```

Repeat runs can skip rsync the same way:

```bash
./scripts/swarm-harness-vm.sh local-replicate --no-sync -- --runtime podman
```

Run the recovery harness inside the VM:

```bash
./scripts/swarm-harness-vm.sh local-replicate-recovery
```

Or skip rsync when reusing the same guest checkout:

```bash
./scripts/swarm-harness-vm.sh local-replicate-recovery --no-sync
```

Stop the VM:

```bash
./scripts/swarm-harness-vm.sh stop
```

## Fast manual testing policy

For manual VM work, do not rediscover the environment each time.

Use exactly this loop:

```bash
./scripts/swarm-harness-vm.sh fast
./scripts/swarm-harness-vm.sh shell
```

After code changes:

```bash
./scripts/swarm-harness-vm.sh sync
./scripts/swarm-harness-vm.sh fast
./scripts/swarm-harness-vm.sh shell
```

If the VM behaves unexpectedly:

```bash
./scripts/swarm-harness-vm.sh logs
./scripts/swarm-harness-vm.sh track
```

If you want to throw away guest changes but keep the cached reusable baseline:

```bash
./scripts/swarm-harness-vm.sh reset
./scripts/swarm-harness-vm.sh fast
```

## Recommended policy

- prefer `swarm-harness` for `tests/swarmd/local_replicate_e2e.sh`
- prefer `swarm-harness` for `tests/swarmd/local_replicate_recovery_e2e.sh`
- use the workstation directly only when the test does not depend on local container networking, attach, or fixed-port isolation
- rerun `sync` before harness work if the checkout changed
- `run`, `local-replicate`, and `local-replicate-recovery` still sync by default; use `--no-sync` only when you intentionally want to reuse the existing guest checkout
- use `--rebootstrap` only when you want to refresh guest packages; normal repeat runs should reuse the existing bootstrap stamp
- if the host checkout already has `web/node_modules`, the sync step carries it into the guest so desktop builds do not need a separate guest-side `npm ci`

## Relevant filepaths

- `scripts/swarm-harness-vm.sh`
- `tests/swarmd/local_replicate_e2e.sh`
- `tests/swarmd/local_replicate_recovery_e2e.sh`
- `tests/swarmd/remote_deploy_e2e.sh`
- `AGENTS.md`

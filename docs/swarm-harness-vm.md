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

Create, bootstrap, and sync the VM:

```bash
./scripts/swarm-harness-vm.sh provision
```

That will:

1. download an Ubuntu cloud image
2. create the `swarm-harness` VM assets
3. boot the guest with loopback-only SSH
4. install guest prerequisites (`podman`, `docker.io`, `git`, `jq`, `rsync`, `npm`, build tools)
5. rsync the current repo checkout, including `web/node_modules` when present, into `~/swarm-go` inside the guest

## Common commands

Check state:

```bash
./scripts/swarm-harness-vm.sh status
```

Open a shell:

```bash
./scripts/swarm-harness-vm.sh ssh
```

Resync the repo:

```bash
./scripts/swarm-harness-vm.sh sync
```

Run an arbitrary guest-side command from the repo root:

```bash
./scripts/swarm-harness-vm.sh run -- git status --short
```

Run the canonical local replicate harness inside the VM:

```bash
./scripts/swarm-harness-vm.sh local-replicate -- --runtime podman
```

Run the recovery harness inside the VM:

```bash
./scripts/swarm-harness-vm.sh local-replicate-recovery
```

Stop the VM:

```bash
./scripts/swarm-harness-vm.sh stop
```

## Recommended policy

- prefer `swarm-harness` for `tests/swarmd/local_replicate_e2e.sh`
- prefer `swarm-harness` for `tests/swarmd/local_replicate_recovery_e2e.sh`
- use the workstation directly only when the test does not depend on local container networking, attach, or fixed-port isolation
- rerun `sync` before harness work if the checkout changed
- if the host checkout already has `web/node_modules`, the sync step carries it into the guest so desktop builds do not need a separate guest-side `npm ci`

## Relevant filepaths

- `scripts/swarm-harness-vm.sh`
- `tests/swarmd/local_replicate_e2e.sh`
- `tests/swarmd/local_replicate_recovery_e2e.sh`
- `tests/swarmd/remote_deploy_e2e.sh`
- `AGENTS.md`

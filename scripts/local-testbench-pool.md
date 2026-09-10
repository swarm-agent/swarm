# Local testbench pool

The local entrypoint is `bash scripts/testbench-local-deploy.sh ACTION --env-file FILE`.
Actions: doctor, deploy, status, pool-status, touch, stop, reap, supervise. Existing remote scripts are unchanged. Status/touch/stop require `--generation` from the claim and the matching `--worktree`. This is trusted root-invoked host tooling, not a multi-user privileged RPC or a sudo allowlist for untrusted source.

## Explicit prerequisites

Linux with cgroup v2, systemd-run, systemctl, nspawn supporting user namespaces/idmapped binds, systemd-socket-activate, the systemd-socket-proxyd helper at its verified `/usr/lib/systemd/systemd-socket-proxyd` package path, mount/umount and Git. No installation is implicit. Configure a short, root-owned mode-0700 pool directory outside source and host Swarm storage. UNIX socket path limits are preflighted. Base image must be an explicitly pinned root-owned raw filesystem image, exactly the configured per-slot disk size. It must contain bash, Git, Go matching go.mod, C toolchain, pnpm/Node, offline dependency caches, socat, runuser and a swarm service account. Provision the image and offline tools/caches with the explicit `testbench-local-image.sh`, `testbench-local-tools.sh` and `testbench-local-cache.sh` entrypoints; inspect each script's `--help` first.

Example non-secret data-only local configuration (replace paths/digest with provisioned values):

```dotenv
SWARM_LOCAL_TESTBENCH_ROOT=/var/lib/swarm-local-test
SWARM_LOCAL_TESTBENCH_BASE_IMAGE=/var/lib/swarm-local-base.raw
SWARM_LOCAL_TESTBENCH_BASE_SHA256=REPLACE_WITH_VERIFIED_SHA256
SWARM_LOCAL_TESTBENCH_SLOTS=2
SWARM_LOCAL_TESTBENCH_CPUS=2
SWARM_LOCAL_TESTBENCH_MEMORY_MB=4096
SWARM_LOCAL_TESTBENCH_DISK_MB=16384
SWARM_LOCAL_TESTBENCH_TOTAL_CPUS=4
SWARM_LOCAL_TESTBENCH_TOTAL_MEMORY_MB=8192
SWARM_LOCAL_TESTBENCH_TOTAL_DISK_MB=32768
SWARM_LOCAL_TESTBENCH_PORT_BASE=18080
```

Unknown local keys, duplicates, expansions, oversized files and config symlinks are rejected. Remote configuration namespace is not executed or changed. No credential belongs in this file.

## Implemented path and limits

Pool authority uses effective OS UID, private pinned root, flock, atomic/fsynced JSON and lane/generation CAS. This protects cooperative chats from stale/cross-lane operations, not malicious processes with the same UID/root authority. Config is immutable once state exists. Slots and resource aggregate budgets are bounded; failed cleanup retains capacity. One lifecycle/build runs at a time.

Deployment records intent, creates a bounded verified Git bundle, checks clean full HEAD again and copies a digest-verified bounded image. Build runs in a private-network/user-namespaced nspawn guest. CPU/memory/tasks and control-group kill policy constrain descendants. Filesystem state is per image. UNIX socket relays expose guest loopback API/Desktop through generation-specific host loopback proxy units without PID-based namespace access. A 1 MiB/16-inode tmpfs limits shared socket-directory writes. Guest daemon runs as its swarm account. Host Swarm storage and host home are never mounted.

The build/start wait emits a heartbeat and renews allocation every ten seconds with a 600-second maximum wait. Ready means owned runtime plus HTTP-speaking endpoints, NOT authenticated product validation. Source HEAD is checked before guest build; authenticated live identity proof remains required. Stop refuses foreign unit descriptions, unknown manifests or unexpected exchange content. Cleanup does not recurse through candidate-controlled paths. `reap` performs explicit expiry/failure reconciliation. The foreground `supervise` action performs bounded expiry sweeps and skips an active lifecycle lock. Supervisor installation is an explicit operator action (the observed instance is transient, not reboot-persistent). The Codex runner wrapper renews leases and bounds commands at ten minutes;  absolute unit lifetime is capped at 24 hours, separate from the renewable allocation lease.

## Observed evidence and remaining limits

- Runtime packages, a private ext4 base and offline caches were provisioned. A clean committed candidate built and ran in the private-network/user-namespaced guest; Desktop returned HTTP 200 and unauthenticated API access returned HTTP 401. This is not authenticated identity or provider proof.
- Provisioning entrypoints: testbench-local-image.sh, testbench-local-tools.sh, testbench-local-cache.sh. They require explicit image paths and privileged invocation; they do not deploy host Swarm. Fast build gate passed after installing this checkout's pinned frontend dependencies.
- Codex uses the separate runner-only broker documented in [local-testbench-codex.md](local-testbench-codex.md), with a dedicated test login—not host credentials or a host credential lease. Live OAuth, broker socket mapping and authenticated Luna streaming require separate evidence.
- Generation-specific guest endpoint forwarding worked in the first deployment. Strict authenticated source-identity evidence and simultaneous live multi-container proof remain outstanding.
- A transient supervisor reaped the expired first lane to `inactive`; final inspection found no active pool candidate. Image copying checks byte bounds/deadlines between blocks and renews leases; blocking filesystem operations remain a supervision limitation.
- Two proxies reserve 20% CPU, 64 MiB memory and 16 tasks from each slot's configured totals. Applied systemd CPU, memory and task limits were inspected on the first live guest; stress enforcement was not tested.
- Lifecycle concurrency/cleanup evidence is bounded hermetic testing plus one observed real expired-lane cleanup, not a live two-container capacity stress test.

## Focused validation

```sh
PYTHONDONTWRITEBYTECODE=1 timeout 60s python3 -m unittest discover -s scripts -p 'test_local_testbench_*.py' -q
bash -n scripts/testbench-local-deploy.sh
```

31 focused pool/runtime/Codex tests passed on the candidate integration diff. Tests use real pool locks/files with bounded competing processes and fake runtime commands. They prove allocation/denial/recovery decisions and command construction, not kernel container isolation, successful guest build or provider access. Independent test review and inventory reconciliation remain pending; not promoted into the critical runner.

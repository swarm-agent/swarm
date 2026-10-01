# Headless distribution image (Linux amd64)

This packages the existing daemon, not a workspace container runner. It builds
`swarmd`, `swarmctl`, and `swarm-fff-search` with CGO using the daemon targets and
shared buildinfo fields from `scripts/build-main-dist.sh`. Both stages use Debian
Trixie/glibc; Go is pinned to the modules' 1.26.7 requirement. Build concurrency is
bounded to two. Only linux/amd64 is supported by the vendored FFF library.

## Build and inspect (no daemon startup)

From the repository root, with Docker BuildKit/buildx or rootless Podman, network
access for base images/apt/Go modules, Python 3, and a writable `TMPDIR`:

<copy>
CONTAINER_ENGINE=docker python3 tests/scripts/headless_container_test.py -v
docker buildx build --platform linux/amd64 --load -t swarm-headless:local \
  --build-arg VERSION=dev --build-arg COMMIT="$(git rev-parse HEAD)" \
  --build-arg BUILT_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)" .
docker image inspect swarm-headless:local --format \
  '{{.Os}}/{{.Architecture}} user={{.Config.User}} entrypoint={{json .Config.Entrypoint}} volumes={{json .Config.Volumes}} ports={{json .Config.ExposedPorts}}'
docker run --rm --network none --cap-drop ALL --security-opt no-new-privileges \
  --entrypoint /bin/sh swarm-headless:local /usr/local/share/swarm/inspect.sh
</copy>

For rootless Podman, use `CONTAINER_ENGINE=podman` for the tests, replace
`docker buildx build --load` with `podman build --jobs=1`, and use `podman` for
inspect/run. Supply `--build-arg TARGETOS=linux --build-arg TARGETARCH=amd64`
when the builder does not populate automatic platform arguments.

The inspector checks loader resolution, UID/GID, writable storage, required tools,
CA certificates and notices, and absence of source/compiler assets. It never
starts Swarm. The build also rejects missing dynamic libraries. FFF is installed
in `/usr/local/lib` and registered with `ldconfig`, independent of its source
RPATH. Debian supplies libc, libgcc and libstdc++; its copyright files remain in
the image. Swarm's license/notices and FFF's MIT license are under
`/usr/local/share/doc/swarm`. The FFF license is copied from upstream commit
`c6013ba6a5918221b6c482486aca01acc0830825` (`dmtrKovalenko/fff`, `LICENSE`).

The focused context test uses the selected engine's actual ignore matcher and a
scratch image with synthetic file sentinels, not simulated agents. It verifies
required input inclusion and private/generated path exclusion. Recipe checks do
not prove a successful build or runtime behavior. Review source for secrets before
building; no path allowlist can detect credentials embedded in legitimate code.
Base tags and apt repositories are not immutable release locks; publishing needs
separate reviewed digest/provenance and license/release gates.

## Mount and security contract

Default UID/GID is `10001:10001`; no startup root/chown helper is provided. Empty
Docker named volumes inherit image ownership. Existing volumes/bind mounts must
already permit this UID to access them. Rootless engines remap container IDs;
prepare selected mount ownership accordingly, never recursively change a user's
project ownership as a workaround.

| Destination | Contract |
| --- | --- |
| `/etc/swarmd` | Persistent named volume: canonical startup config and identity configuration |
| `/var/lib/swarmd` | Persistent named volume: durable database, credentials and application state |
| `/var/cache/swarmd` | Named volume: disposable caches |
| `/var/log/swarmd` | Named volume: logs; treat as private |
| `/run/swarmd` | Ephemeral runtime/lock directory; optional tmpfs with uid/gid 10001, mode 0700 |
| `/project` | Exactly one selected repository bind mount; writable only when intended |

Use explicit named volumes for config/data so container replacement preserves
identity and state. Do not share these volumes between concurrent daemons. Mount
only the selected repository at `/project`, never a broad host home, filesystem
root or Docker socket. The image does not declare an anonymous project volume.
No credentials are baked into the image and no privileged mode is needed.

The entrypoint runs `swarmd --desktop-port=0 --cwd=/project` directly for signal
handling. `SWARM_DISABLE_MINT_REPORT=1` is set. There are no Desktop assets, no
published/exposed ports, and no permission/authentication bypass. Daemon API
loopback/auth defaults remain unchanged. Container port publishing alone cannot
make that loopback listener reachable from a host SDK.

**Not qualified here:** first-time headless setup, provider configuration, SDK
connectivity, live agents, restart durability, npm delivery or publication. This
is a build-and-inspect artifact, not a claim that those later tasks work.

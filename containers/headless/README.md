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

## Fresh setup without Desktop

Build the current source using the commands above; older images may not contain
`swarmctl setup`. The setup sequence below is implemented; the bounded candidate
qualification and its limits are recorded under **Live qualification**. Execute it
on your intended runtime host. No Desktop, source checkout inside the container,
published port or host socket mount is required.

Prerequisites: choose one clean Git repository with at least one commit; it must
be readable/writable by container UID 10001, including `.git`. Use a normal checkout,
not a linked worktree whose Git metadata lives outside the single project mount.
Set `PROJECT_DIR` to its absolute host path. Set `PROVIDER`, `MODEL` and `THINKING`
to your explicit supported choices; setup has no model defaults. Set
`PROVIDER_KEY_FILE` to a private (0600), existing API-key file outside the project.
A secret manager's stdout can instead feed the credential command directly. Do
not type keys into shell commands or enable shell tracing (`set -x`). Provider
verification and catalog hydration require outbound HTTPS; no inbound port is opened.

<copy>
(
set -eu
docker run -d --name swarm-headless --cap-drop ALL \
  --security-opt no-new-privileges \
  --mount type=volume,src=swarm-headless-config,dst=/etc/swarmd \
  --mount type=volume,src=swarm-headless-data,dst=/var/lib/swarmd \
  --mount type=volume,src=swarm-headless-cache,dst=/var/cache/swarmd \
  --mount type=volume,src=swarm-headless-logs,dst=/var/log/swarmd \
  --mount "type=bind,src=${PROJECT_DIR:?select a project},dst=/project" \
  swarm-headless:local

docker exec swarm-headless swarmctl setup status
docker exec swarm-headless swarmctl setup identity --username owner --name Headless

docker exec -i swarm-headless swarmctl setup credential \
  --provider "${PROVIDER:?select a provider}" --api-key-stdin \
  < "${PROVIDER_KEY_FILE:?select a private key file}"

for role in action plan compact finder coder designer router; do
  docker exec swarm-headless swarmctl setup model --role "$role" \
    --provider "$PROVIDER" --model "${MODEL:?select a model}" \
    --thinking "${THINKING:?select a supported thinking level}"
done

docker exec swarm-headless swarmctl setup workspace --path /project --name Project
docker exec swarm-headless swarmctl setup complete
docker exec swarm-headless swarmctl setup status
)
</copy>

Run each step only after the preceding step succeeds. On a slow start, retry
`setup status` after startup completes; a missing socket is an error, never a TCP
fallback. The loop is an explicit choice to assign the same model to all roles;
run individual `setup model` commands instead for different role assignments.
Optional `--service-tier` and `--context-mode` are forwarded without defaults.
Provider onboarding hydrates canonical catalog-derived recommendations. Most
providers verify the key first; OpenAI intentionally saves an active **unverified**
key, with validity determined by the first real request. Saved setup is not proof
of valid credentials. The explicit model commands override only selected roles.
This does not modify any host installation or other account's settings.

`setup credential` is the existing **first-provider API-key** onboarding path,
not a credential-rotation interface. It rejects another credential once configured;
never retry it blindly after an ambiguous transport failure. `setup status` reports
saved credential/workspace counts; rerun only the missing steps. Identity creation
is one-time: a second `setup identity` cannot replace the owner. Existing Codex
OAuth commands remain available separately, but are not this documented API-key
sequence. A rejected model or workspace operation remains an error, and workspace
registration does not initialize Git, commit files or opt into ignoring dirty
content. Prepare/review the chosen repository first.

`setup complete` checks persisted identity, credentials, workspace and all seven
canonical model assignments before setting the existing onboarding-complete flag.
It does not start agents or claim provider reachability after setup. API failures
show only HTTP status, not potentially secret-bearing response bodies; no API key,
cookie or product-session token is printed. The CLI accepts no key-valued flag,
rejects terminal key input and does not follow redirects. Use `docker exec -i`,
**not `-t`**, for secret input. Keys are not stored in container environment metadata.

### Persistence and authority

Setup calls the existing `/v1/onboarding`, `/v1/onboarding/provider/credential`,
`/v1/agent-model-settings` and `/v1/workspace/add` handlers through the private
`/var/lib/swarmd/local-transport/api.sock`. For non-default deployments use
`--socket` or `SWARMD_LOCAL_TRANSPORT_SOCKET`; default discovery uses the canonical
storage contract (including an explicit `DATA_DIR` override), not a home-directory
fallback. Never mount/export that privileged socket to a host SDK or untrusted
container. Access to container exec and the volumes is administrative authority.

The existing stores own identity, account-scoped credentials, model assignments,
workspace selection and V3 session state. Startup config is mode 0600 in the config
volume. Credentials are encrypted by the canonical credential store; its private
local key and durable database must stay together in the data volume. Local-key
loading rejects symlinks, unsafe modes and wrong ownership. Volume directories
are mode 0700 in the image. Encryption does not protect against someone who can
read both the data and its local key: protect host storage/backups too.

Keep these same named volumes when replacing the container. Stop the old daemon
before starting its replacement; never run two daemons against one data volume.
Do not delete the volumes or persist only the database while dropping the key.
Container recreation must use the same project mount. Cache and runtime socket
files are not identity authorities. The qualified Docker candidate preserved this
state across container recreation; other engine/platform combinations require
separate qualification.

### Focused non-live checks

<copy>
cd swarmd
GOMAXPROCS=2 go test -p 2 ./cmd/swarmctl -run '^TestSetup' -count=1 -timeout=60s
GOMAXPROCS=2 go test -p 2 ./internal/api -run '^TestHeadlessSetupLocalIdentityBoundary$' -count=1 -timeout=90s
</copy>

These exercise real CLI parsing/Unix transport against isolated HTTP fixtures and
real backend identity handlers against temporary storage. They are not live daemon,
provider or SDK runs and do not replace the separate live qualification below.

## Authenticated host SDK connection

Rebuild the current source first. Opt in to the separate listener with
`--container-sdk-port=7783` **and** publish it with
`--publish 127.0.0.1:7783:7783` on the `docker run` command above. Place `--publish`
before the image name and `--container-sdk-port=7783` after `swarm-headless:local`.
Alternatively add both options on the first run, before performing setup: the SDK
listener fails closed until an owner and scoped token exist. For an existing
container, stop it and recreate it with the same named volumes and project mount;
do not run both daemons against the same volumes. Never delete volumes to enable
SDK access.

The normal API, peer listener and private setup socket are unchanged. Only the
separate SDK listener binds container IPv4 interfaces; Desktop and permission
bypass must be disabled. **Never use host networking, `-P`, a bare `-p 7783:7783`,
or a public/LAN host binding.** Swarm cannot inspect the engine's host-side port
mapping: loopback publishing is a deployment requirement, not a daemon guarantee.
Use a dedicated container network without untrusted peers and a current container
engine. HTTP is intended only for this same-host connection; remote access requires
an independently secured private tunnel, not public publishing.

Issue a one-hour session-scoped token through container exec, after setup. Set
`SWARM_SDK_TOKEN_FILE` to a new absolute file path in a private directory **outside
the project**, then run without shell tracing:

<copy>
(
set -eu
umask 077
set -C
docker exec swarm-headless swarmctl setup sdk-token --expires-in-seconds 3600 \
  > "${SWARM_SDK_TOKEN_FILE:?choose a new private file outside the project}"
)
</copy>

Do not use `-t`, print the file, put its contents in argv, or mount the data/socket
volume into the SDK process. The command returns only JSON `token` and `id` to the
explicit pipe/file; direct terminal output is refused. `umask` is necessary because
`docker exec` uses a pipe internally and cannot inspect the host destination mode.
The token file itself is plaintext and must remain private (0600). Maximum CLI
lifetime is 24 hours; issue a replacement explicitly on expiry. A failed or ambiguous
export may have minted a token: inspect/revoke it rather than retrying blindly.

The listener accepts only `Authorization: Bearer` scoped tokens and checks the
stored user/account against the resolved identity on every request. Revoked,
expired, missing and mismatched credentials fail closed. It does not accept attach
tokens, cookies, implicit local identity or anonymous onboarding. Allowed routes
are session create/list/detail, prompt submission, exact permission resolution and
run stop; existing `sessions:read`/`sessions:write`, account checks, vault gate and
tool permissions still apply. It is not the whole administrative API. A session
write token can approve tools and access sessions in its account: treat it as a
powerful credential, not a sandbox or a single-session capability. Models and
permissions are never changed by the connection example.

`packages/sdk/examples/headless-session.ts` connects to `http://127.0.0.1:7783`,
creates a session against `/project`, submits your prompt and displays durable
snapshots (messages, active run, pending permissions and plan). It imports the
installed `@swarm/sdk` package; from source, build `packages/sdk` first so its
package self-reference resolves. From the source root, with an available `tsx` runner:

<copy>
export SWARM_SDK_TOKEN_FILE
export SWARM_SDK_URL=http://127.0.0.1:7783
tsx packages/sdk/examples/headless-session.ts
</copy>

For a packaged consumer, install the unpublished tarball and use
`import { SwarmClient } from '@swarm/sdk'`. No Desktop bootstrap call or Unix socket is used. API
redirects are rejected rather than replaying tokens or prompts. The example checks
token-file permissions and refuses non-loopback URLs. It withholds raw API errors.
Session output is private; do not capture/share it indiscriminately.

Use `refresh` to receive current results (explicit refresh, no timer polling).
Review each pending call and use `allow <id>` followed by `yes` to approve that
exact call once, or `deny <id>`. Unknown IDs cannot be approved by the example.
`stop` targets the current run; `quit` only disconnects and **does not cancel** a
running session. A prompt receipt is not a successful result; inspect the displayed
run/plan state and assistant messages. Planning/review gates outside ordinary tool
permissions are not automatically accepted by this minimal example.

Revoke through private exec using the non-secret `id` from the export (never the
token itself):

<copy>
docker exec swarm-headless swarmctl setup revoke-sdk-token --id "$TOKEN_ID"
</copy>

Focused checks: `TestContainerSDKConfig`, `TestContainerSDKAuthenticationAndScopes`,
`TestSetupSDKToken`, and SDK `headless-session.spec.ts`/`transport.spec.ts`. These
exercise startup, actual backend auth/permission handlers and SDK request behavior
with isolated fixtures. Live candidate evidence is summarized below; no image/npm
publication is implied.

## Live qualification

The unpublished `0.1.0-headless.1` candidate at source
`2164a303a2ac44ec98ca58995fa724322b0caa89` passed a bounded Linux amd64 Docker
qualification with Node 22.16.0: fresh tarball installation without install-time
container startup, Desktop-free setup, rejected invalid credentials and forbidden
SDK routes, a real provider-backed SDK agent with denied then explicitly approved
tool execution, and retained identity/settings/session history/project output after
container recreation. The image's application source is recorded separately in
`packages/cli/runtime-image.json`.

This evidence covers that exact candidate, not every future rebuild. Live Podman,
other platforms/providers, public registry installation and broader recovery cases
remain unverified. Documentation-only release preparation does not change the
qualified runtime or package bytes. Final registry metadata or package changes
require repacking and affected checks before publication.

## Unpublished npm candidates

`packages/cli` supplies explicit `swarm-headless start/stop/status/setup` commands;
`packages/sdk` builds with its own locked dependencies. See their READMEs for the
clean-consumer install/setup sequence. Installation never starts containers.
The CLI pins the exact matching local image ID in `runtime-image.json`, uses
`--pull=never`, loopback publishing and four persistent named state volumes.
It does not invent a registry or claim a candidate tag is a public release.

<copy>
node tests/scripts/headless_npm_test.mjs "$PACKAGE_OUTPUT_DIR"
</copy>

This bounded package check builds outside the checkout, packs both tarballs into
the selected output directory and installs them into a clean temporary consumer.
It runs no daemon or agents. The separate live qualification and GitHub dev-to-main
review/merge must precede approved image/npm publication.

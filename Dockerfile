# syntax=docker/dockerfile:1
ARG BUILD_IMAGE=docker.io/library/golang:1.26.9-trixie
ARG RUNTIME_IMAGE=docker.io/library/ubuntu:26.04
FROM ${BUILD_IMAGE} AS build
ARG TARGETOS
ARG TARGETARCH
RUN test "${TARGETOS}/${TARGETARCH}" = linux/amd64 || \
    { echo 'Headless Swarm supports only linux/amd64 (vendored glibc FFF)' >&2; exit 1; }
ENV CGO_ENABLED=1 GOMAXPROCS=2 GOTOOLCHAIN=local
WORKDIR /src
COPY go.mod go.sum ./
COPY swarmd/go.mod swarmd/go.sum ./swarmd/
RUN cd swarmd && go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILT_AT=unknown
# Same daemon build targets and buildinfo fields as scripts/build-main-dist.sh.
# Keep the compiler cache out of image layers (and their temporary exports).
RUN --mount=type=cache,target=/root/.cache/go-build \
    mkdir -p /out/bin /out/lib && cd swarmd && \
    flags="-X swarm-refactor/swarmtui/pkg/buildinfo.Version=${VERSION} -X swarm-refactor/swarmtui/pkg/buildinfo.Commit=${COMMIT} -X swarm-refactor/swarmtui/pkg/buildinfo.BuiltAt=${BUILT_AT}" && \
    for command in swarmd swarmctl swarm-fff-search; do \
      go build -mod=readonly -p 2 -trimpath -ldflags "$flags" -o "/out/bin/$command" "./cmd/$command" || exit 1; \
    done && \
    cp internal/fff/lib/linux-amd64-gnu/libfff_c.so /out/lib/

FROM ${RUNTIME_IMAGE} AS runtime
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.source="https://github.com/swarm-agent/swarm" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"
# Refresh inherited packages as well as newly installed dependencies.
# Ubuntu LTS supplies maintained security backports for the glibc runtime.
RUN apt-get update && apt-get upgrade -y --no-install-recommends && \
    apt-get install -y --no-install-recommends \
      ca-certificates git bash libc-bin libgcc-s1 libstdc++6 libpcre2-8-0 && \
    rm -rf /var/lib/apt/lists/* && \
    groupadd --gid 10001 swarm && \
    useradd --uid 10001 --gid 10001 --create-home --shell /bin/bash swarm && \
    install -d -o 10001 -g 10001 -m 0700 \
      /etc/swarmd /var/lib/swarmd /var/cache/swarmd /run/swarmd /var/log/swarmd && \
    install -d -o 10001 -g 10001 -m 0750 /project
COPY --from=build /out/bin/ /usr/local/bin/
COPY --from=build /out/lib/ /usr/local/lib/
COPY LICENSE THIRD_PARTY_NOTICES.md containers/headless/FFF-LICENSE /usr/local/share/doc/swarm/
COPY containers/headless/inspect.sh /usr/local/share/swarm/inspect.sh
# FFF's source RPATH is not a runtime dependency: resolve it via ld.so.cache.
RUN ldconfig && sh /usr/local/share/swarm/inspect.sh --libraries-only
# Agent worktrees (uncommitted work) live under the user data root; keep it on
# the persistent data volume so replacing the container never loses them.
ENV HOME=/home/swarm SWARM_DISABLE_MINT_REPORT=1 XDG_DATA_HOME=/var/lib/swarmd/user-data
USER 10001:10001
WORKDIR /project
VOLUME ["/etc/swarmd", "/var/lib/swarmd", "/var/cache/swarmd", "/var/log/swarmd"]
# Permission policy is locked by default: agents and remote clients cannot
# enable bypass or save rules. The owner opts out explicitly by appending
# --lock-permission-policy=false to `docker run`; it applies from that start.
ENTRYPOINT ["/usr/local/bin/swarmd", "--desktop-port=0", "--cwd=/project", "--lock-permission-policy"]

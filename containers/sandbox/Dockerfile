# syntax=docker/dockerfile:1
# Agent sandbox: where agents' bash commands and Swarm's Git on agent-writable
# repositories run. It holds no Swarm binaries, storage or credentials. Swarm
# starts one container per project from this image, as its own non-root uid,
# with every capability dropped, no-new-privileges, memory and pid limits,
# gVisor when installed, the isolated swarm-sandbox network, and only the
# project and its worktrees mounted (see swarmd/internal/sandbox).
ARG RUNTIME_IMAGE=docker.io/library/ubuntu:26.04
FROM ${RUNTIME_IMAGE}
LABEL org.opencontainers.image.source="https://github.com/swarm-agent/swarm" \
      org.opencontainers.image.description="Swarm agent sandbox"
# A general development toolset: agents install project dependencies into
# the project or their home (npm, pip --user, venvs), not system-wide.
RUN apt-get update && apt-get upgrade -y --no-install-recommends && \
    apt-get install -y --no-install-recommends \
      bash ca-certificates coreutils curl findutils git grep gzip jq less \
      make procps sed tar unzip util-linux xz-utils \
      build-essential pkg-config \
      python3 python3-pip python3-venv \
      nodejs npm && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -m 1777 /home/sandbox
ENV HOME=/home/sandbox LANG=C.UTF-8
WORKDIR /home/sandbox
CMD ["sleep", "infinity"]

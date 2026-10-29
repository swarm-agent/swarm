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
      nodejs npm \
      libxcb1-dev libxkbcommon-dev libxkbcommon-x11-dev && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -m 1777 /home/sandbox
# Rust: rustup's stable toolchain, minimal profile (rustc, cargo, rust-std;
# rustdoc for doctests). The three X libraries above are what a headless
# `cargo test` of console/ (GPUI) links; nothing else it needs is system-wide.
# The toolchain is shared and read-only under RUSTUP_HOME; each sandbox's
# crates and builds go to its own home (~/.cargo, CARGO_HOME unset). Swarm
# sets the sandbox's PATH itself, so rustup's proxies (cargo, rustc, rustdoc,
# ...) are linked into /usr/local/bin. rustup-init is pinned by version and
# checksum (from static.rust-lang.org/rustup/archive/<version>/<triple>/).
ENV RUSTUP_HOME=/usr/local/rustup
RUN set -eu; \
    rustup_version=1.29.2; \
    case "$(dpkg --print-architecture)" in \
      amd64) triple=x86_64-unknown-linux-gnu; sum=5be191e8c70a5007074254cb74d78dbfdd25cd5a61c27de7b8e5410122ec24f4 ;; \
      arm64) triple=aarch64-unknown-linux-gnu; sum=a9c168dbea81a84671c6b7a5c1795cb0eb699020110d107ddfca427bd797e697 ;; \
      *) echo "no pinned rustup-init for $(dpkg --print-architecture)" >&2; exit 1 ;; \
    esac; \
    curl -fsSL --proto '=https' --tlsv1.2 -o /tmp/rustup-init \
      "https://static.rust-lang.org/rustup/archive/${rustup_version}/${triple}/rustup-init"; \
    echo "${sum}  /tmp/rustup-init" | sha256sum -c -; \
    chmod 0755 /tmp/rustup-init; \
    CARGO_HOME=/usr/local/cargo /tmp/rustup-init -y --no-modify-path --profile minimal --default-toolchain stable; \
    rm -f /tmp/rustup-init; \
    for tool in /usr/local/cargo/bin/*; do ln -s "$tool" /usr/local/bin/; done; \
    chmod -R a+rX,go-w "$RUSTUP_HOME" /usr/local/cargo; \
    cargo --version; rustc --version; rustdoc --version
ENV HOME=/home/sandbox LANG=C.UTF-8
WORKDIR /home/sandbox
CMD ["sleep", "infinity"]

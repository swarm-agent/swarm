# syntax=docker/dockerfile:1
# Sealed Swarm runtime for API-only (sealed) agents.
#
# The final image starts from scratch: swarmd, swarmctl, git and the shared
# libraries they load, CA certificates and an unprivileged user. There is no
# shell, coreutils, package manager or interpreter, so even a bug that let an
# agent reach the bash tool would have nothing to run. Git stays because
# workspaces are Git repositories; without /bin/sh it cannot run hooks or
# aliases. Build the runtime first (repository root):
#   docker build -t swarm-headless:local .
#   docker build -t swarm-sealed:local -f containers/sealed/Dockerfile .
ARG SWARM_RUNTIME=swarm-headless:local
FROM ${SWARM_RUNTIME} AS runtime
USER 0
# Collect each binary and every library it loads, preserving paths.
RUN set -eu; out=/sealed; \
    for bin in /usr/local/bin/swarmd /usr/local/bin/swarmctl /usr/bin/git; do \
      install -D "$bin" "$out$bin"; \
      for lib in $(ldd "$bin" | awk '/=> \//{print $3} /^\t\/lib/{print $1}'); do \
        real=$(readlink -f "$lib"); install -D "$real" "$out$real"; \
        [ "$real" = "$lib" ] || { mkdir -p "$out$(dirname "$lib")"; ln -sf "$real" "$out$lib"; }; \
      done; \
    done; \
    install -D /etc/ssl/certs/ca-certificates.crt "$out/etc/ssl/certs/ca-certificates.crt"; \
    install -D /etc/ld.so.cache "$out/etc/ld.so.cache"; \
    printf 'root:x:0:0::/nonexistent:/nonexistent\nswarm:x:10001:10001::/home/swarm:/nonexistent\n' > "$out/etc/passwd"; \
    printf 'root:x:0:\nswarm:x:10001:\n' > "$out/etc/group"; \
    printf 'hosts: files dns\n' > "$out/etc/nsswitch.conf"; \
    for dir in /etc/swarmd /var/lib/swarmd /var/cache/swarmd /run/swarmd /var/log/swarmd /home/swarm /tmp; do \
      install -d -o 10001 -g 10001 -m 0700 "$out$dir"; \
    done; \
    chmod 1777 "$out/tmp"; install -d -o 10001 -g 10001 -m 0750 "$out/project"; \
    for forbidden in sh bash dash busybox python3 perl node curl wget; do \
      if find "$out" -name "$forbidden" | grep -q .; then echo "sealed image must not contain $forbidden" >&2; exit 1; fi; \
    done

FROM scratch
COPY --from=runtime /sealed/ /
COPY --from=runtime /usr/local/share/doc/swarm/ /usr/local/share/doc/swarm/
ENV HOME=/home/swarm SWARM_DISABLE_MINT_REPORT=1 XDG_DATA_HOME=/var/lib/swarmd/user-data PATH=/usr/local/bin:/usr/bin
USER 10001:10001
WORKDIR /project
VOLUME ["/etc/swarmd", "/var/lib/swarmd", "/var/cache/swarmd", "/var/log/swarmd"]
ENTRYPOINT ["/usr/local/bin/swarmd", "--desktop-port=0", "--cwd=/project", "--lock-permission-policy"]

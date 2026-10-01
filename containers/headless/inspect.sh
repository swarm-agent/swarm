#!/bin/sh
# Inspect image contents and loader resolution only; never start Swarm.
set -eu
for binary in /usr/local/bin/swarmd /usr/local/bin/swarmctl /usr/local/bin/swarm-fff-search /usr/local/lib/libfff_c.so; do
    test -r "$binary"
    report=$(ldd "$binary" 2>&1) || { printf '%s\n' "$report" >&2; exit 1; }
    printf '%s\n%s\n' "$binary" "$report"
    case "$report" in
        *'not found'*) echo 'Unresolved runtime library' >&2; exit 1 ;;
    esac
done
if [ "${1:-}" = --libraries-only ]; then exit 0; fi
test "$(uname -m)" = x86_64
test "$(id -u)" = 10001
test "$(id -g)" = 10001
test "${SWARM_DISABLE_MINT_REPORT:-}" = 1
for directory in /etc/swarmd /var/lib/swarmd /var/cache/swarmd /run/swarmd /var/log/swarmd /project; do
    test -w "$directory"
done
for binary in swarmd swarmctl swarm-fff-search git bash; do
    command -v "$binary"
done
test -s /etc/ssl/certs/ca-certificates.crt
test -s /usr/local/share/doc/swarm/LICENSE
test -s /usr/local/share/doc/swarm/THIRD_PARTY_NOTICES.md
test -s /usr/local/share/doc/swarm/FFF-LICENSE
for absent in /src /usr/local/go /usr/bin/gcc /usr/bin/node /usr/local/share/swarm/web; do
    test ! -e "$absent"
done
printf '%s\n' 'Packaging inspection complete; daemon and live agents were not started.'

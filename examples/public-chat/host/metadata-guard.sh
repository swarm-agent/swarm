#!/bin/sh
# Only Swarm may use the VM's Google identity. On a GCE VM every container can
# ask the metadata server (169.254.169.254) for the VM's service-account token.
# Provider vertex needs that token inside the swarm container; the gateway and
# the tunnel must never get it. This keeps a chain BOT-METADATA, jumped to from
# DOCKER-USER for traffic to the metadata server: containers running an image
# whose name starts with $METADATA_GUARD_IMAGE (default swarm-sealed) are
# allowed, every other container is dropped. If no such container runs, all are
# dropped. The host itself is not affected.
#   metadata-guard.sh          apply once
#   metadata-guard.sh watch    apply, then again after every container start
set -eu
IMAGE=${METADATA_GUARD_IMAGE:-swarm-sealed}
CHAIN=BOT-METADATA
META=169.254.169.254/32

apply() {
  allow=""
  for id in $(docker ps -q); do
    case $(docker inspect -f "{{.Config.Image}}" "$id") in "$IMAGE"*) ;; *) continue ;; esac
    allow="$allow $(docker inspect -f "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}" "$id")"
  done
  # One atomic replace of the chain (iptables-restore --noflush flushes only
  # the chains it declares): there is never a moment without the final DROP.
  {
    echo "*filter"
    echo ":$CHAIN - [0:0]"
    for ip in $allow; do echo "-A $CHAIN -s $ip/32 -j RETURN"; done
    echo "-A $CHAIN -j DROP"
    echo "COMMIT"
  } | iptables-restore --noflush
  iptables -C DOCKER-USER -d "$META" -j "$CHAIN" 2>/dev/null ||
    iptables -I DOCKER-USER 1 -d "$META" -j "$CHAIN"
  logger -t metadata-guard "metadata server allowed for:${allow:- nobody}"
}

apply
if [ "${1:-}" = watch ]; then
  docker events --filter type=container --filter event=start --format '{{.ID}}' | while read -r _; do apply; done
fi

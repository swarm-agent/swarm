#!/usr/bin/env bash
# Network isolation for agent sandboxes (idempotent; run as root at install and
# at every boot, since iptables rules do not survive a reboot).
#
# Sandboxes join the swarm-sandbox bridge. Traffic from it may reach the
# internet (package registries, Git hosts) but never this server itself (so
# not Swarm's ports), its tailnet, private networks, loopback or cloud
# metadata. DOCKER-USER sees forwarded traffic; INPUT sees traffic addressed to
# this server. Replies to connections the sandbox opened stay allowed.
set -euo pipefail

NETWORK=${SWARM_SANDBOX_NETWORK:-swarm-sandbox}
BRIDGE=${SWARM_SANDBOX_BRIDGE:-br-swarm-sbx}
SUBNET=${SWARM_SANDBOX_SUBNET:-172.31.251.0/24}
BLOCKED_NETS=(0.0.0.0/8 10.0.0.0/8 100.64.0.0/10 127.0.0.0/8 169.254.0.0/16
  172.16.0.0/12 192.0.0.0/24 192.168.0.0/16 198.18.0.0/15 224.0.0.0/4 240.0.0.0/4)

docker network inspect "$NETWORK" >/dev/null 2>&1 ||
  docker network create --driver bridge --subnet "$SUBNET" \
    -o com.docker.network.bridge.name="$BRIDGE" \
    -o com.docker.network.bridge.enable_icc=false "$NETWORK" >/dev/null

iptables -N SWARM-SANDBOX 2>/dev/null || iptables -F SWARM-SANDBOX
iptables -A SWARM-SANDBOX -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
for net in "${BLOCKED_NETS[@]}"; do iptables -A SWARM-SANDBOX -d "$net" -j DROP; done
iptables -A SWARM-SANDBOX -j RETURN
iptables -N DOCKER-USER 2>/dev/null || true
iptables -C DOCKER-USER -i "$BRIDGE" -j SWARM-SANDBOX 2>/dev/null ||
  iptables -I DOCKER-USER -i "$BRIDGE" -j SWARM-SANDBOX

iptables -N SWARM-SANDBOX-HOST 2>/dev/null || iptables -F SWARM-SANDBOX-HOST
iptables -A SWARM-SANDBOX-HOST -m conntrack --ctstate ESTABLISHED,RELATED -j RETURN
iptables -A SWARM-SANDBOX-HOST -j DROP
iptables -C INPUT -i "$BRIDGE" -j SWARM-SANDBOX-HOST 2>/dev/null ||
  iptables -I INPUT -i "$BRIDGE" -j SWARM-SANDBOX-HOST

# The bridge has no IPv6; refuse it outright should Docker ever enable it.
if command -v ip6tables >/dev/null 2>&1; then
  ip6tables -N SWARM-SANDBOX 2>/dev/null || ip6tables -F SWARM-SANDBOX
  ip6tables -A SWARM-SANDBOX -j DROP
  for chain in INPUT FORWARD; do
    ip6tables -C "$chain" -i "$BRIDGE" -j SWARM-SANDBOX 2>/dev/null ||
      ip6tables -I "$chain" -i "$BRIDGE" -j SWARM-SANDBOX
  done
fi

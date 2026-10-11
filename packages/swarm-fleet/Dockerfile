# swarm-fleet: Swarm Control for every machine on your tailnet, as one stdio
# MCP server. Build from this directory:
#   docker build -t swarm-fleet:local packages/swarm-fleet
FROM docker.io/tailscale/tailscale:v1.102.5 AS tailscale
FROM docker.io/library/node:22.20.0-alpine3.22
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscaled /usr/local/bin/
COPY server.mjs entrypoint.sh /app/
USER node
ENTRYPOINT ["/bin/sh", "/app/entrypoint.sh"]

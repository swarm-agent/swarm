# syntax=docker/dockerfile:1
# Required qualified release digest; no mutable/local fallback.
ARG SWARM_RUNTIME
FROM docker.io/library/node:22.22.0-trixie-slim AS sdk
ARG SWARM_RUNTIME
RUN node -e 'if (!/^ghcr\.io\/swarm-agent\/swarm-headless@sha256:[a-f0-9]{64}$/.test(process.argv[1])) process.exit(1)' "$SWARM_RUNTIME"
WORKDIR /src/packages/sdk
COPY packages/sdk/package.json packages/sdk/package-lock.json packages/sdk/tsconfig.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY packages/sdk/src ./src
COPY packages/sdk/scripts ./scripts
RUN npm run build && npm prune --omit=dev --ignore-scripts --no-audit --no-fund
WORKDIR /src/examples/headless-app
COPY examples/headless-app/package.json examples/headless-app/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY examples/headless-app ./

FROM ${SWARM_RUNTIME}
USER root
RUN apt-get update && apt-get install -y --no-install-recommends openssl tini util-linux && \
    rm -rf /var/lib/apt/lists/* && \
    install -d -o 10001 -g 10001 -m 0700 /etc/swarmd/headless-app
COPY --from=sdk /usr/local/bin/node /usr/local/bin/node
COPY --from=sdk /src/packages/sdk /opt/workshop/packages/sdk
COPY --from=sdk /src/examples/headless-app /opt/workshop/examples/headless-app
COPY containers/headless/app/entrypoint.sh /usr/local/share/swarm/app-entrypoint.sh
COPY containers/headless/app/supervisor.mjs /usr/local/share/swarm/supervisor.mjs
ENV APP_ORIGIN=http://127.0.0.1:8443 SWARM_DISABLE_MINT_REPORT=1
USER 10001:10001
WORKDIR /project
EXPOSE 8443
VOLUME ["/project"]
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "/bin/bash", "/usr/local/share/swarm/app-entrypoint.sh"]

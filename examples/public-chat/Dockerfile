# Public chat gateway. Build from the repository root:
#   docker build -f examples/public-chat/Dockerfile -t public-chat-gateway:local .
# The runtime is distroless (Node and its libraries only: no shell, npm or
# package manager), pinned by digest; update the digest to take base fixes.
FROM node:22-slim AS build
WORKDIR /src
COPY packages/sdk packages/sdk
# Build the SDK, then keep only its runtime dependencies (ws) for the final image.
RUN npm --prefix packages/sdk ci --ignore-scripts --no-audit --no-fund && npm --prefix packages/sdk run build \
 && npm --prefix packages/sdk prune --omit=dev --no-audit --no-fund
COPY examples/public-chat/package.json examples/public-chat/package-lock.json examples/public-chat/
RUN npm --prefix examples/public-chat ci --omit=dev --ignore-scripts --no-audit --no-fund
COPY examples/public-chat/gateway examples/public-chat/gateway
COPY examples/public-chat/playbook.json examples/public-chat/
# DATA_DIR: an empty directory owned by the gateway user (the runtime has no shell to create it).
RUN install -d -o 10001 -g 10001 -m 0700 /out/data

FROM gcr.io/distroless/nodejs22-debian12:nonroot@sha256:13593b7570658e8477de39e2f4a1dd25db2f836d68a0ba771251572d23bb4f8e
ENV NODE_ENV=production
COPY --from=build /src/packages/sdk/package.json /src/packages/sdk/package.json
COPY --from=build /src/packages/sdk/dist /src/packages/sdk/dist
COPY --from=build /src/packages/sdk/node_modules /src/packages/sdk/node_modules
COPY --from=build /src/examples/public-chat /src/examples/public-chat
# A new named volume mounted at /data inherits this owner and mode.
COPY --from=build --chown=10001:10001 /out/data /data
WORKDIR /src/examples/public-chat
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["/nodejs/bin/node", "-e", "fetch('http://127.0.0.1:8080/ready').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]
# The distroless entrypoint is node itself.
CMD ["gateway/server.mjs"]

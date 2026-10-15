# Public chat gateway. Build from the repository root:
#   docker build -f examples/public-chat/Dockerfile -t public-chat-gateway:local .
# Pin the base image by digest for a production deployment.
FROM node:22-slim AS build
WORKDIR /src
COPY packages/sdk packages/sdk
RUN npm --prefix packages/sdk ci --ignore-scripts --no-audit --no-fund && npm --prefix packages/sdk run build
COPY examples/public-chat/package.json examples/public-chat/package-lock.json examples/public-chat/
RUN npm --prefix examples/public-chat ci --omit=dev --ignore-scripts --no-audit --no-fund
COPY examples/public-chat/gateway examples/public-chat/gateway
COPY examples/public-chat/playbook.json examples/public-chat/

FROM node:22-slim
ENV NODE_ENV=production
COPY --from=build /src/packages/sdk/package.json /src/packages/sdk/package.json
COPY --from=build /src/packages/sdk/dist /src/packages/sdk/dist
COPY --from=build /src/examples/public-chat /src/examples/public-chat
WORKDIR /src/examples/public-chat
USER 10001:10001
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --retries=3 CMD ["node", "-e", "fetch('http://127.0.0.1:8080/ready').then(r=>process.exit(r.ok?0:1),()=>process.exit(1))"]
CMD ["node", "gateway/server.mjs"]

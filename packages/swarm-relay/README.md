# Swarm Control relay (Cloudflare reference)

A relay you deploy into **your own** Cloudflare account. It gives AI clients
(Claude, ChatGPT, Claude Code, routines) one OAuth-protected MCP URL that
controls your Swarm machines. SwarmAgent does not operate it.

```
AI client ──HTTPS + OAuth──► relay /mcp ──► Durable Object ◄──outbound WSS── swarmd (each machine)
```

- Machines **dial out**; nothing listens on them. Remote access is **off** in
  Swarm until you run `swarmctl remote init` and `swarmctl remote enable`.
- AI clients register themselves (Dynamic Client Registration or Client ID
  Metadata Documents) and use OAuth 2.1 with PKCE via
  `@cloudflare/workers-oauth-provider`.
- **Consent is approved inside Swarm, not on the web page.** The page shows a
  code; you approve it with `swarmctl remote approve CODE` on a machine you
  own. A stranger who finds the URL cannot authorize their own client.
- Each machine enforces its own ceiling (`--allow-write`, `--allow-approve`)
  on top of the scopes a client was granted, and executes tools through the
  same scoped-token Swarm Control handler as local clients.

Protocol, tools and security model: [`docs/checkpoints/swarm-control.md`](../../docs/checkpoints/swarm-control.md).

## Deploy

```sh
# On each machine (inside the headless container use `docker exec <name> …`):
swarmctl remote init --relay https://swarm-relay.<subdomain>.workers.dev --name "Box A" --allow-write
# → prints device_id and public_key

# In this directory, with your Cloudflare credentials:
npm ci
npx wrangler kv namespace create OAUTH_KV          # put the id in wrangler.jsonc
# set RELAY_ORIGIN in wrangler.jsonc to the Worker's https origin
npx wrangler secret put SWARM_DEVICE_KEYS          # {"<device_id>": "<public_key>", ...}
npx wrangler deploy

# On each machine:
swarmctl remote enable
swarmctl remote status                             # connected: true
```

Add `https://<relay>/mcp` as a custom connector in your AI client. When the
browser shows a code, approve it on a machine:

```sh
swarmctl remote status                 # lists pending authorization requests
swarmctl remote approve ABCD-EFGH      # or: --scopes swarm:read
```

Tokens go only to redirect hosts in `ALLOWED_REDIRECT_HOSTS` (default
`claude.ai,claude.com`); add others deliberately.

Turn everything off with `swarmctl remote disable` (or `reset`, which also
revokes the machine's token and deletes its key).

## Local development

`wrangler dev` runs the relay in `workerd`. Put `RELAY_ORIGIN`,
`ALLOWED_REDIRECT_HOSTS` and `SWARM_DEVICE_KEYS` in `.dev.vars` (ignored).
Machines accept `http://` relays only on loopback; for containers, serve
`wrangler dev` over HTTPS with a test certificate the containers trust.

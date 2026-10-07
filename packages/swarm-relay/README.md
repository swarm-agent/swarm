# Swarm Control relay (Cloudflare reference)

A relay you deploy into **your own** Cloudflare account. It gives AI clients
(Claude, ChatGPT, Claude Code, routines) one OAuth-protected MCP URL that
controls your Swarm machines. SwarmAgent does not operate it.

```
AI client ──HTTPS + OAuth──► relay /mcp ──► Durable Object ◄──outbound WSS── swarmd (each machine)
```

- Machines **dial out**; nothing listens on them. Remote access is **off** in
  Swarm until you run `swarmctl remote init` and `swarmctl remote enable`
  (or press **Connect to Claude** in the headless app).
- Machines are trusted from `SWARM_DEVICE_KEYS` or by **pairing**: a new
  machine shows a code, and an AI client you already authorized pairs it.
- AI clients register themselves (Dynamic Client Registration or Client ID
  Metadata Documents) and use OAuth 2.1 with PKCE via
  `@cloudflare/workers-oauth-provider`.
- **Consent is approved inside Swarm, not on the web page.** The page shows a
  code; you approve it with `swarmctl remote approve CODE` on a machine you
  own. A stranger who finds the URL cannot authorize their own client.
- Each machine enforces its own ceiling (`--allow-write`, `--allow-approve`,
  `--allow-manage` for worker administration, usage limits and agent role
  default models)
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

### Adding machines by pairing (no redeploy)

A machine the relay does not list yet can pair instead. It connects, proves
it holds its own key, and waits with a short code (shown only on that
machine: `swarmctl remote status` → `pairing_code`, or the headless app's
**Connect to Claude** step). Give the code to an AI client you already
authorized with `swarm:manage`, which calls `swarm_pair_machine`; the relay
stores the key in its Durable Object and the machine connects at once.
`swarm_remove_machine` forgets a paired machine. Codes are single-use and
expire after 15 minutes; at most 20 machines wait at a time; clients see
waiting machines by name only. Set the variable `PAIRING=off` to accept only
`SWARM_DEVICE_KEYS`.

To update an already-deployed relay to this version, run `npx wrangler deploy`
in this directory with your existing `wrangler.jsonc` values. No migration,
secret or connector change is needed.

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

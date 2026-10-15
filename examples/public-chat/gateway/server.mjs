// Public chat gateway: the only process that talks to the sealed Swarm daemon.
// It holds two narrow keys (the agent's gateway token and its operator token),
// owns the visitor -> conversation mapping, answers the agent's client tools and
// enforces per-visitor limits. Its port is reached only through the tunnel by the
// edge Worker (X-Gateway-Key) and by the monitoring hub (X-Ops-Key).
import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { SwarmClient } from '@swarm-agent/sdk';
import * as core from './core.mjs';

const MAX_BODY = 8192;

export function gatewayHandler({ gateway, operator, config, visitors, emit, now = Date.now }) {
  const perVisitor = core.createLimiter({ limit: config.visitorMessagesPerWindow, windowMs: config.visitorWindowMs, now });
  let inflight = 0;
  let readyAt = 0;

  const json = (res, status, data) => {
    res.writeHead(status, { 'Content-Type': 'application/json', 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff' });
    res.end(JSON.stringify(data));
  };
  const readBody = async (req) => {
    const chunks = [];
    let size = 0;
    for await (const chunk of req) {
      size += chunk.length;
      if (size > MAX_BODY) return undefined;
      chunks.push(chunk);
    }
    try { return JSON.parse(Buffer.concat(chunks).toString() || '{}'); } catch { return undefined; }
  };
  const conversation = async (visitorId) => {
    const known = visitors.get(visitorId);
    if (known) return known;
    const session = await gateway.sessions.create({ agent_name: config.agentName, workspace_path: config.workspacePath, title: `Visitor ${visitorId.slice(0, 8)}` });
    emit('conversation.started', { session_id: session.id, visitor_id: visitorId });
    return visitors.touch(visitorId, { session_id: session.id, messages: 0 });
  };

  async function chat(body) {
    const problem = core.messageProblem(body, config.maxMessageChars);
    if (problem) return [400, { status: 'invalid', error: problem }];
    if (!perVisitor(body.visitor_id)) return [429, { status: 'slow_down' }];
    // Ended by the agent (end_conversation): no daemon call, no model run, until a person reopens it.
    if (visitors.get(body.visitor_id)?.ended) return [200, { status: 'ended' }];
    const spend = await gateway.agents.spendToday();
    if (spend.limit_exceeded) return [503, { status: 'resting' }];
    const visitor = await conversation(body.visitor_id);
    if (visitor.messages >= config.maxConversationMessages) return [200, { status: 'conversation_full', booking_url: config.bookingUrl }];
    await visitors.touch(body.visitor_id, { messages: visitor.messages + 1 });
    const sessionId = visitor.session_id;
    const detail = await gateway.sessions.get(sessionId);
    if (core.takeoverOf(detail)?.state === 'human') {
      // A person holds the conversation: the daemon keeps the message and starts no run.
      await gateway.sessions.sendMessage(sessionId, { content: body.message });
      emit('visitor.message', { session_id: sessionId, visitor_id: body.visitor_id });
      return [200, { status: 'with_person', seq: core.latestReplySeq(detail.messages) }];
    }
    const endConversation = (ended) => visitors.touch(body.visitor_id, { ended });
    const handlers = core.toolHandlers({ playbook: config.playbook, dataDir: config.dataDir, bookingUrl: config.bookingUrl, emit, sessionId, visitorId: body.visitor_id, endConversation, now });
    const { reply, messages } = await gateway.agents.converse(sessionId, body.message, handlers, { timeoutMs: config.replyTimeoutMs });
    // seq lets the widget poll for a person's later replies without repeating this one.
    const status = visitors.get(body.visitor_id)?.ended ? 'ended' : 'answered';
    return [200, { status, reply, seq: core.latestReplySeq(messages) }];
  }

  async function ops(path, body, url) {
    if (path === '/ops/conversations') return [200, { conversations: visitors.list() }];
    const sessionId = typeof body?.session_id === 'string' ? body.session_id : url.searchParams.get('session_id');
    if (!sessionId || !visitors.bySession(sessionId)) return [404, { error: 'unknown conversation' }];
    if (path === '/ops/transcript') {
      const detail = await operator.sessions.get(sessionId);
      return [200, { takeover: core.takeoverOf(detail), ended: visitors.get(visitors.bySession(sessionId))?.ended ?? null, messages: (detail.messages ?? []).filter((m) => m.role === 'user' || m.role === 'assistant').map((m) => ({ id: m.id, role: m.role, from: m.metadata?.source === 'operator' ? 'person' : m.role === 'user' ? 'visitor' : 'agent', text: m.content, seq: m.global_seq })) }];
    }
    if (path === '/ops/takeover') return [200, { takeover: await operator.agents.takeOver(sessionId) }];
    if (path === '/ops/handback') return [200, { takeover: await operator.agents.handBack(sessionId) }];
    if (path === '/ops/reopen') {
      const visitorId = visitors.bySession(sessionId);
      await visitors.touch(visitorId, { ended: null });
      emit('conversation.reopened', { session_id: sessionId, visitor_id: visitorId });
      return [200, { reopened: true }];
    }
    if (path === '/ops/reply') {
      if (typeof body?.content !== 'string' || !body.content.trim() || body.content.length > 4000) return [400, { error: 'content is required (at most 4000 characters)' }];
      return [200, { message: await operator.agents.replyAsOperator(sessionId, body.content) }];
    }
    return [404, { error: 'not found' }];
  }

  return async (req, res) => {
    const url = new URL(req.url, 'http://gateway');
    if (req.method === 'GET' && url.pathname === '/health') return json(res, 200, { ok: true });
    if (req.method === 'GET' && url.pathname === '/ready') {
      // A real authenticated round trip to the daemon, at most every 10 s.
      if (now() - readyAt > 10_000) {
        try { await gateway.agents.spendToday(); readyAt = now(); } catch { return json(res, 503, { ok: false }); }
      }
      return json(res, 200, { ok: true });
    }
    const isOps = url.pathname.startsWith('/ops/');
    if (!core.sameSecret(req.headers[isOps ? 'x-ops-key' : 'x-gateway-key'], isOps ? config.opsSecret : config.gatewaySecret)) return json(res, 401, { error: 'unauthorized' });
    if (inflight >= config.maxInflight) return json(res, 503, { status: 'busy' });
    inflight++;
    try {
      if (req.method === 'POST' && url.pathname === '/chat') {
        const body = await readBody(req);
        const [status, data] = await chat(body);
        return json(res, status, data);
      }
      if (req.method === 'GET' && url.pathname === '/chat/replies') {
        const visitorId = url.searchParams.get('visitor_id') ?? '';
        const visitor = core.VISITOR_ID.test(visitorId) ? visitors.get(visitorId) : undefined;
        if (!visitor) return json(res, 200, { replies: [] });
        const detail = await gateway.sessions.get(visitor.session_id);
        return json(res, 200, { with_person: core.takeoverOf(detail)?.state === 'human', replies: core.repliesAfter(detail, Number(url.searchParams.get('after') ?? 0)) });
      }
      if (isOps && (req.method === 'GET' || req.method === 'POST')) {
        const body = req.method === 'POST' ? await readBody(req) : {};
        if (body === undefined) return json(res, 400, { error: 'invalid body' });
        const [status, data] = await ops(url.pathname, body, url);
        return json(res, status, data);
      }
      return json(res, 404, { error: 'not found' });
    } catch (error) {
      // The visitor sees a generic answer; details stay in the gateway log and the hub event.
      const busy = error?.status === 429;
      if (!busy) {
        console.error('gateway request failed:', error?.status ?? '', String(error?.message ?? error).slice(0, 300));
        emit('chat.error', { path: url.pathname, status: error?.status ?? 0 });
      }
      return json(res, busy ? 503 : 502, { status: busy ? 'busy' : 'error' });
    } finally {
      inflight--;
    }
  };
}

async function secret(name) {
  const file = process.env[`${name}_FILE`];
  return file ? (await readFile(file, 'utf8')).trim() : '';
}

async function main() {
  const env = process.env;
  const here = fileURLToPath(new URL('..', import.meta.url));
  const config = {
    swarmUrl: env.SWARM_API_URL ?? 'http://swarm:7783',
    agentName: env.AGENT_NAME ?? 'frontdesk',
    workspacePath: env.WORKSPACE_PATH ?? '/project',
    dataDir: env.DATA_DIR ?? '/data',
    bookingUrl: env.BOOKING_URL ?? '',
    playbook: JSON.parse(await readFile(env.PLAYBOOK_FILE ?? join(here, 'playbook.json'), 'utf8')),
    gatewaySecret: await secret('GATEWAY_SECRET'),
    opsSecret: await secret('OPS_SECRET'),
    maxMessageChars: Number(env.MAX_MESSAGE_CHARS ?? 2000),
    visitorMessagesPerWindow: Number(env.VISITOR_MESSAGES_PER_WINDOW ?? 20),
    visitorWindowMs: Number(env.VISITOR_WINDOW_MS ?? 600_000),
    maxConversationMessages: Number(env.MAX_CONVERSATION_MESSAGES ?? 30),
    maxInflight: Number(env.MAX_INFLIGHT ?? 8),
    replyTimeoutMs: Number(env.REPLY_TIMEOUT_MS ?? 45_000),
  };
  if (config.gatewaySecret.length < 32 || config.opsSecret.length < 32) throw new Error('GATEWAY_SECRET_FILE and OPS_SECRET_FILE must each hold at least 32 characters');
  const gateway = new SwarmClient({ baseUrl: config.swarmUrl, token: await secret('SWARM_GATEWAY_TOKEN') });
  const operator = new SwarmClient({ baseUrl: config.swarmUrl, token: await secret('SWARM_OPERATOR_TOKEN') });
  const visitors = await core.createVisitorStore(join(config.dataDir, 'visitors.json'));
  const emit = core.createEmitter({ url: env.HUB_EVENTS_URL, secret: await secret('HUB_EVENTS_SECRET') });
  const port = Number(env.PORT ?? 8080);
  createServer(gatewayHandler({ gateway, operator, config, visitors, emit })).listen(port, env.LISTEN_HOST ?? '0.0.0.0', () => {
    console.log(`public chat gateway listening on ${port} for agent ${config.agentName}`);
  });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => { console.error(String(error?.message ?? error)); process.exit(1); });
}

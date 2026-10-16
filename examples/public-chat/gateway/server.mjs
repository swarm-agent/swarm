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

export function gatewayHandler({ gateway, operator, config, visitors, emit: sendEvent, presence = core.createPresence(), feed = core.createFeed(), blocks = core.blocklist(), typing: typingStore, now = Date.now }) {
  const typing = typingStore ?? core.createTyping({ now });
  // Typing marks are ephemeral: they go to the console feed (to wake listeners), never to the hub or alerts.
  const notify = (type, data) => feed.push(type, data);
  const typingLimit = core.createLimiter({ limit: config.typingPerMinute ?? 40, windowMs: 60_000, now });
  const agentLabel = config.agentLabel || 'AI worker';
  const blockDays = config.blockDays ?? 30;
  const perVisitor = core.createLimiter({ limit: config.visitorMessagesPerWindow, windowMs: config.visitorWindowMs, now });
  const acceptTimeoutMs = config.acceptTimeoutMs ?? 120_000;
  const alertsPerVisitor = config.alertsPerVisitor ?? 3;
  let inflight = 0;
  let readyAt = 0;
  // Every event goes to the staff console's feed and, if configured, the hub.
  const emit = (type, data) => { feed.push(type, data); sendEvent(type, data); };
  // The daemon refuses a run over the token's concurrency limit (429) before it
  // records the message, so the gateway can wait for a free slot and retry instead
  // of turning the visitor away. It gives up after busyWaitMs and answers busy.
  const busyWaitMs = config.busyWaitMs ?? 20_000;
  const busyRetryMs = config.busyRetryMs ?? 500;
  async function waitForSlot(run) {
    const started = now();
    for (let attempt = 0; ; attempt++) {
      try {
        return await run();
      } catch (error) {
        if (error?.status !== 429 || now() - started >= busyWaitMs) throw error;
        await new Promise((resolve) => setTimeout(resolve, Math.min(busyRetryMs * 2 ** attempt, 3000) + Math.random() * busyRetryMs / 2));
      }
    }
  }
  // A waiting request nobody accepted in time becomes missed, once.
  const personOf = async (visitorId) => {
    const visitor = visitors.get(visitorId);
    const person = core.personState(visitor, { acceptTimeoutMs, now });
    if (person?.state === 'missed' && visitor.person.state === 'waiting') {
      await visitors.touch(visitorId, { person });
      emit('handoff.missed', { session_id: visitor.session_id, visitor_id: visitorId });
    }
    return person;
  };

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
    if (known?.session_id) return known;
    const session = await gateway.sessions.create({ agent_name: config.agentName, workspace_path: config.workspacePath, title: `Visitor ${visitorId.slice(0, 8)}` });
    emit('conversation.started', { session_id: session.id, visitor_id: visitorId });
    return visitors.touch(visitorId, { session_id: session.id, messages: 0 });
  };

  async function chat(body, net) {
    const problem = core.messageProblem(body, config.maxMessageChars);
    if (problem) return [400, { status: 'invalid', error: problem }];
    if (!perVisitor(body.visitor_id)) return [429, { status: 'slow_down' }];
    // A person blocked this visitor's network: nothing reaches the daemon.
    if (blocks.blocked(net)) return [200, { status: 'blocked' }];
    // Ended by the agent (end_conversation) or a person (resolve, block): no daemon
    // call, no model run, until a person reopens it.
    const closed = visitors.get(body.visitor_id)?.ended;
    if (closed) return [200, { status: 'ended', reason: closed.reason }];
    const spend = await gateway.agents.spendToday();
    if (spend.limit_exceeded) return [503, { status: 'resting' }];
    const visitor = await conversation(body.visitor_id);
    if (visitor.messages >= config.maxConversationMessages) return [200, { status: 'conversation_full', booking_url: config.bookingUrl }];
    await visitors.touch(body.visitor_id, { messages: visitor.messages + 1, ...(net ? { net } : {}) });
    const sessionId = visitor.session_id;
    const detail = await gateway.sessions.get(sessionId);
    if (core.takeoverOf(detail)?.state === 'human') {
      // A person holds the conversation: the daemon keeps the message and starts no run.
      typing.clearVisitor(sessionId);
      await gateway.sessions.sendMessage(sessionId, { content: body.message });
      emit('visitor.message', { session_id: sessionId, visitor_id: body.visitor_id });
      return [200, { status: 'with_person', seq: core.latestReplySeq(detail.messages), person: core.publicPerson(await personOf(body.visitor_id)) }];
    }
    const endConversation = (ended) => visitors.touch(body.visitor_id, { ended });
    const setPerson = (person) => visitors.touch(body.visitor_id, { person });
    const setProposal = (proposal) => visitors.touch(body.visitor_id, { proposal });
    const handlers = core.toolHandlers({ playbook: config.playbook, dataDir: config.dataDir, bookingUrl: config.bookingUrl, emit, sessionId, visitorId: body.visitor_id, endConversation, presence, setPerson, setProposal, visitors, now });
    const { reply, messages } = await waitForSlot(() => gateway.agents.converse(sessionId, body.message, handlers, { timeoutMs: config.replyTimeoutMs }));
    // seq lets the widget poll for a person's later replies without repeating this one.
    const after = visitors.get(body.visitor_id);
    const status = after?.ended ? 'ended' : 'answered';
    const answer = { status, reply, seq: core.latestReplySeq(messages), person: core.publicPerson(await personOf(body.visitor_id)) };
    // The card appears with the reply that filled it in, and stays until sent or dismissed.
    if (after?.proposal) answer.proposal = core.publicProposal(after.proposal);
    return [200, answer];
  }

  // The widget after a page refresh: this visitor's own conversation, text only.
  async function history(visitorId) {
    const visitor = core.VISITOR_ID.test(visitorId) ? visitors.get(visitorId) : undefined;
    if (!visitor?.session_id) return [200, { messages: [] }];
    const detail = await gateway.sessions.get(visitor.session_id);
    const messages = (detail.messages ?? [])
      .filter((m) => (m.role === 'user' || m.role === 'assistant') && m.metadata?.visible !== false && String(m.content ?? '').trim())
      .slice(-50)
      .map((m) => {
        const from = m.metadata?.source === 'operator' ? 'person' : m.role === 'user' ? 'visitor' : 'agent';
        const author = from === 'person' && core.authorOf(m, visitor.authors);
        return { seq: Number(m.global_seq ?? m.seq ?? 0), from, text: String(m.content), ...(author ? { name: author.name } : {}) };
      });
    return [200, { messages, with_person: core.takeoverOf(detail)?.state === 'human', person: core.publicPerson(await personOf(visitorId)), ended: Boolean(visitor.ended), ended_reason: visitor.ended?.reason ?? null, contact_saved: Boolean(visitor.contact), proposal: core.publicProposal(visitor.proposal) }];
  }

  // The "leave your email" form: saved for a person to follow up, no model run.
  async function contact(body, net) {
    if (blocks.blocked(net)) return [200, { status: 'blocked' }];
    const email = typeof body?.email === 'string' ? body.email.trim() : '';
    const name = typeof body?.name === 'string' ? body.name.trim().slice(0, 100) : '';
    const note = typeof body?.note === 'string' ? body.note.trim().slice(0, 500) : '';
    if (typeof body?.visitor_id !== 'string' || !core.VISITOR_ID.test(body.visitor_id) || !core.EMAIL.test(email)) return [400, { status: 'invalid', error: 'visitor_id and a valid email are required' }];
    if (!perVisitor(body.visitor_id)) return [429, { status: 'slow_down' }];
    const visitor = visitors.get(body.visitor_id);
    if (!visitor?.session_id) return [404, { status: 'invalid', error: 'unknown visitor' }];
    // Each save alerts staff, so one visitor gets a few, not a stream.
    if ((visitor.contacts ?? 0) >= alertsPerVisitor) return [429, { status: 'slow_down' }];
    await visitors.touch(body.visitor_id, { contacts: (visitor.contacts ?? 0) + 1 });
    const fromCard = body.from_proposal === true && Boolean(visitor.proposal);
    await core.saveLead({ dataDir: config.dataDir, emit, visitors, sessionId: visitor.session_id, visitorId: body.visitor_id, lead: { name, email, need: note }, source: fromCard ? 'proposal' : 'form', now });
    if (!fromCard) return [200, { status: 'saved' }];
    // The visitor checked and sent the card the agent filled in: that is the goal of
    // the chat, so it closes politely. A person can still reopen it from the console.
    await visitors.touch(body.visitor_id, { proposal: null, ended: { reason: 'lead_saved', at: now() } });
    emit('conversation.resolved', { session_id: visitor.session_id, visitor_id: body.visitor_id, reason: 'lead_saved' });
    return [200, { status: 'saved', ended: true }];
  }

  // The visitor is typing to a person: shown in the console. Ignored unless a person
  // is involved, so it never reaches the model and costs nothing.
  async function visitorTyping(body, net) {
    if (blocks.blocked(net)) return [200, { ok: false }];
    if (!body || typeof body.visitor_id !== 'string' || !core.VISITOR_ID.test(body.visitor_id)) return [400, { error: 'visitor_id is required' }];
    if (!typingLimit(body.visitor_id)) return [429, { status: 'slow_down' }];
    const visitor = visitors.get(body.visitor_id);
    if (!visitor?.session_id || visitor.ended || !visitor.person) return [200, { ok: false }];
    if (typing.visitor(visitor.session_id)) notify('visitor.typing', { session_id: visitor.session_id });
    return [200, { ok: true }];
  }

  // Replies for the widget: a person's and the agent's, who is typing, and the hand-off
  // state. With wait=N it is held open (up to 25 s) until any of that changes from what
  // the widget already shows (typing=<name>, state=<person state>), so replies and
  // typing arrive at once without polling.
  async function replies(url) {
    const visitorId = url.searchParams.get('visitor_id') ?? '';
    if (!core.VISITOR_ID.test(visitorId) || !visitors.get(visitorId)?.session_id) return [200, { replies: [] }];
    const after = Number(url.searchParams.get('after') ?? 0);
    const waitMs = Math.min(Math.max(Number(url.searchParams.get('wait') ?? 0) || 0, 0), 25) * 1000;
    const shownTyping = url.searchParams.get('typing') ?? '';
    const shownState = url.searchParams.get('state') ?? '';
    const deadline = now() + waitMs;
    let cursor = feed.latest();
    const snapshot = async () => {
      const visitor = visitors.get(visitorId);
      const detail = await gateway.sessions.get(visitor.session_id);
      const person = core.publicPerson(await personOf(visitorId));
      const staff = typing.staffOf(visitor.session_id);
      return { sessionId: visitor.session_id, until: staff?.until, data: { with_person: core.takeoverOf(detail)?.state === 'human', person, typing: staff ? { name: staff.name } : null, ended_reason: visitor.ended?.reason ?? null, replies: core.repliesAfter(detail, Number.isFinite(after) ? after : 0, visitor.authors) } };
    };
    const changed = ({ data }) => data.replies.length > 0 || Boolean(data.ended_reason) || (data.typing?.name ?? '') !== shownTyping || (data.person?.state ?? '') !== shownState;
    let snap = await snapshot();
    while (!changed(snap) && now() < deadline) {
      // Wake on an event about this conversation, or when a typing mark lapses.
      const lapse = snap.until ? snap.until - now() + 50 : Infinity;
      const events = await feed.wait(cursor, Math.max(0, Math.min(deadline - now(), lapse)));
      cursor = feed.latest();
      if (events.some((e) => e.session_id === snap.sessionId) || (snap.until && now() >= snap.until)) snap = await snapshot();
    }
    return [200, snap.data];
  }

  // "Not now" on the card: it goes away and the chat carries on. No model run.
  async function proposalAnswer(body, net) {
    if (blocks.blocked(net)) return [200, { status: 'blocked' }];
    if (!body || typeof body.visitor_id !== 'string' || !core.VISITOR_ID.test(body.visitor_id) || body.accept !== false) return [400, { status: 'invalid', error: 'visitor_id and accept: false are required' }];
    if (!perVisitor(body.visitor_id)) return [429, { status: 'slow_down' }];
    const visitor = visitors.get(body.visitor_id);
    if (!visitor?.proposal) return [200, { status: 'declined' }];
    await visitors.touch(body.visitor_id, { proposal: null });
    emit('lead.declined', { session_id: visitor.session_id, visitor_id: body.visitor_id });
    return [200, { status: 'declined' }];
  }

  // The visitor's answer to the agent's offer (the Yes / No thanks buttons): no model run.
  async function personRequest(body, net) {
    if (blocks.blocked(net)) return [200, { status: 'blocked' }];
    if (!body || typeof body.visitor_id !== 'string' || !core.VISITOR_ID.test(body.visitor_id) || typeof body.accept !== 'boolean') return [400, { status: 'invalid', error: 'visitor_id and accept are required' }];
    if (!perVisitor(body.visitor_id)) return [429, { status: 'slow_down' }];
    const visitor = visitors.get(body.visitor_id);
    if (!visitor) return [404, { status: 'invalid', error: 'unknown visitor' }];
    if (visitor.ended) return [200, { status: 'ended' }];
    const current = await personOf(body.visitor_id);
    if (!body.accept) {
      await visitors.touch(body.visitor_id, { person: null });
      emit('person.declined', { session_id: visitor.session_id, visitor_id: body.visitor_id });
      return [200, { status: 'declined' }];
    }
    if (current?.state === 'waiting' || current?.state === 'joined') return [200, { status: current.state, person: core.publicPerson(current) }];
    const [staff] = presence.online();
    if (!staff) return [200, { status: 'unavailable', booking_url: config.bookingUrl }];
    if ((visitor.handoffs ?? 0) >= alertsPerVisitor) return [429, { status: 'slow_down' }];
    const person = { state: 'waiting', name: staff.name, role: staff.role, summary: current?.summary ?? 'Visitor asked for a person', since: now() };
    await visitors.touch(body.visitor_id, { person, handoffs: (visitor.handoffs ?? 0) + 1 });
    emit('handoff.requested', { session_id: visitor.session_id, visitor_id: body.visitor_id, summary: person.summary, staff_online: true, source: 'button' });
    return [200, { status: 'waiting', person: core.publicPerson(person) }];
  }

  async function ops(path, body, url) {
    if (path === '/ops/conversations') return [200, { conversations: visitors.list() }];
    if (path === '/ops/presence') {
      if (!body?.id) return [200, { online: presence.online() }];
      const text = (v) => typeof v === 'string' && v.trim().length > 0 && v.length <= 40;
      if (!text(body?.id) || !text(body?.name) || !text(body?.role) || typeof body?.available !== 'boolean') return [400, { error: 'id, name, role and available are required' }];
      return [200, { online: body.available ? presence.beat(body) : presence.leave(body.id) }];
    }
    if (path === '/ops/blocks') return [200, { blocks: blocks.list() }];
    if (path === '/ops/unblock') {
      if (typeof body?.net !== 'string' || !core.NET.test(body.net)) return [400, { error: 'net is required' }];
      const removed = await blocks.remove(body.net);
      if (removed) emit('visitor.unblocked', { net: body.net, by: body?.name });
      return [200, { unblocked: removed }];
    }
    if (path === '/ops/queue') {
      const waiting = [];
      for (const v of visitors.list()) {
        const person = await personOf(v.visitor_id);
        if (person?.state === 'waiting') waiting.push({ session_id: v.session_id, visitor_id: v.visitor_id, since: person.since, summary: person.summary, expires_at: person.since + acceptTimeoutMs });
      }
      return [200, { waiting }];
    }
    const sessionId = typeof body?.session_id === 'string' ? body.session_id : url.searchParams.get('session_id');
    if (!sessionId || !visitors.bySession(sessionId)) return [404, { error: 'unknown conversation' }];
    if (path === '/ops/transcript') {
      const detail = await operator.sessions.get(sessionId);
      const visitor = visitors.get(visitors.bySession(sessionId));
      const messages = (detail.messages ?? []).filter((m) => m.role === 'user' || m.role === 'assistant').map((m) => {
        const from = m.metadata?.source === 'operator' ? 'person' : m.role === 'user' ? 'visitor' : 'agent';
        const author = from === 'person' && core.authorOf(m, visitor?.authors);
        return { id: m.id, role: m.role, from, text: m.content, seq: m.global_seq, ...(author ? { author } : {}) };
      });
      return [200, { visitor_typing: typing.visitorTyping(sessionId), takeover: core.takeoverOf(detail), ended: visitor?.ended ?? null, contact: visitor?.contact ?? null, proposal: core.publicProposal(visitor?.proposal), agent_label: agentLabel, blocked: Boolean(visitor?.net && blocks.blocked(visitor.net)), can_block_network: Boolean(visitor?.net), messages }];
    }
    if (path === '/ops/typing') {
      const name = typeof body?.name === 'string' && body.name.trim() ? body.name.trim().slice(0, 40) : '';
      if (!name) return [400, { error: 'name is required' }];
      if (typing.staff(sessionId, name)) notify('person.typing', { session_id: sessionId, name });
      return [200, { ok: true }];
    }
    if (path === '/ops/takeover') return [200, { takeover: await operator.agents.takeOver(sessionId) }];
    if (path === '/ops/accept') {
      // One step for the console: take the conversation over and tell the visitor who joined.
      const visitorId = visitors.bySession(sessionId);
      const previous = visitors.get(visitorId)?.person;
      const [staff] = presence.online();
      const name = typeof body?.name === 'string' && body.name.trim() ? body.name.trim().slice(0, 40) : previous?.name ?? staff?.name ?? 'A person';
      const role = typeof body?.role === 'string' && body.role.trim() ? body.role.trim().slice(0, 40) : previous?.role ?? staff?.role ?? '';
      const takeover = await operator.agents.takeOver(sessionId);
      const person = { state: 'joined', name, role, summary: previous?.summary, since: now() };
      await visitors.touch(visitorId, { person });
      emit('handoff.accepted', { session_id: sessionId, visitor_id: visitorId, name });
      return [200, { takeover, person: core.publicPerson(person) }];
    }
    if (path === '/ops/handback') {
      await visitors.touch(visitors.bySession(sessionId), { person: null });
      return [200, { takeover: await operator.agents.handBack(sessionId) }];
    }
    if (path === '/ops/reopen') {
      // The visitor starts a fresh conversation: the agent would otherwise read its
      // own goodbye in the history and keep treating the chat as closed.
      const visitorId = visitors.bySession(sessionId);
      const net = visitors.get(visitorId)?.net;
      if (net) await blocks.remove(net);
      await visitors.touch(visitorId, { ended: null, person: null, proposal: null, session_id: null, messages: 0, previous_session_id: sessionId });
      emit('conversation.reopened', { session_id: sessionId, visitor_id: visitorId });
      return [200, { reopened: true }];
    }
    if (path === '/ops/resolve' || path === '/ops/block') {
      // A person closes the chat: done (resolve) or abusive (block, which also
      // blocks the visitor's network for blockDays). Only reopen undoes either.
      const visitorId = visitors.bySession(sessionId);
      const visitor = visitors.get(visitorId);
      const by = typeof body?.name === 'string' ? body.name.slice(0, 40) : undefined;
      const reason = path === '/ops/block' ? 'blocked' : 'resolved';
      if (core.takeoverOf(await operator.sessions.get(sessionId))?.state === 'human') await operator.agents.handBack(sessionId);
      await visitors.touch(visitorId, { ended: { reason, at: now(), by }, person: null, proposal: null });
      let network = false;
      if (reason === 'blocked' && visitor?.net) {
        await blocks.add(visitor.net, { until: now() + blockDays * 86_400_000, session_id: sessionId, visitor_id: visitorId, by });
        network = true;
      }
      emit(reason === 'blocked' ? 'visitor.blocked' : 'conversation.resolved', { session_id: sessionId, visitor_id: visitorId, reason, by, network });
      return [200, { ended: true, reason, network }];
    }
    if (path === '/ops/reply') {
      if (typeof body?.content !== 'string' || !body.content.trim() || body.content.length > 4000) return [400, { error: 'content is required (at most 4000 characters)' }];
      const message = await operator.agents.replyAsOperator(sessionId, body.content);
      typing.clearStaff(sessionId);
      notify('person.replied', { session_id: sessionId });
      // Remember who sent it (the console adds the name from the verified login),
      // so the console and the widget can show a name instead of "a person".
      const key = message?.id ?? message?.global_seq;
      if (key !== undefined && typeof body?.name === 'string' && body.name.trim()) {
        const visitorId = visitors.bySession(sessionId);
        const authors = Object.entries({ ...(visitors.get(visitorId)?.authors ?? {}), [String(key)]: { name: body.name.slice(0, 40), role: typeof body.role === 'string' ? body.role.slice(0, 40) : '' } }).slice(-200);
        await visitors.touch(visitorId, { authors: Object.fromEntries(authors) });
      }
      return [200, { message }];
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
    if (req.method === 'GET' && url.pathname === '/ops/events') {
      // Long poll for the staff console: held open up to 25 s, outside the in-flight limit.
      const after = Number(url.searchParams.get('after') ?? 0);
      const waitMs = Math.min(Math.max(Number(url.searchParams.get('wait') ?? 25), 0), 25) * 1000;
      const events = await feed.wait(Number.isFinite(after) ? after : 0, waitMs);
      return json(res, 200, { events, latest: feed.latest() });
    }
    if (req.method === 'GET' && url.pathname === '/chat/status') {
      const staff = presence.online();
      return json(res, 200, { staff_online: staff.length > 0, staff, agent_label: agentLabel });
    }
    if (req.method === 'GET' && url.pathname === '/chat/replies') {
      // Held open up to 25 s, so outside the in-flight limit (like the console feed).
      try {
        const [status, data] = await replies(url);
        return json(res, status, data);
      } catch (error) {
        console.error('gateway request failed:', error?.status ?? '', String(error?.message ?? error).slice(0, 300));
        return json(res, 502, { status: 'error' });
      }
    }
    if (inflight >= config.maxInflight) return json(res, 503, { status: 'busy' });
    // The edge's keyed hash of the visitor's IP, for blocking; only trusted with the gateway key.
    const netHeader = isOps ? undefined : req.headers['x-visitor-net'];
    const net = typeof netHeader === 'string' && core.NET.test(netHeader) ? netHeader : undefined;
    inflight++;
    try {
      if (req.method === 'POST' && url.pathname === '/chat') {
        const body = await readBody(req);
        const [status, data] = await chat(body, net);
        return json(res, status, data);
      }
      if (req.method === 'GET' && url.pathname === '/chat/history') {
        const [status, data] = await history(url.searchParams.get('visitor_id') ?? '');
        return json(res, status, data);
      }
      if (req.method === 'POST' && url.pathname === '/chat/contact') {
        const [status, data] = await contact(await readBody(req), net);
        return json(res, status, data);
      }
      if (req.method === 'POST' && url.pathname === '/chat/person') {
        const [status, data] = await personRequest(await readBody(req), net);
        return json(res, status, data);
      }
      if (req.method === 'POST' && url.pathname === '/chat/typing') {
        const [status, data] = await visitorTyping(await readBody(req), net);
        return json(res, status, data);
      }
      if (req.method === 'POST' && url.pathname === '/chat/proposal') {
        const [status, data] = await proposalAnswer(await readBody(req), net);
        return json(res, status, data);
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
    maxInflight: Number(env.MAX_INFLIGHT ?? 32),
    busyWaitMs: Number(env.BUSY_WAIT_MS ?? 20_000),
    replyTimeoutMs: Number(env.REPLY_TIMEOUT_MS ?? 45_000),
    acceptTimeoutMs: Number(env.ACCEPT_TIMEOUT_MS ?? 120_000),
    alertsPerVisitor: Number(env.ALERTS_PER_VISITOR ?? 3),
    agentLabel: env.AGENT_LABEL ?? 'AI worker',
    blockDays: Number(env.BLOCK_DAYS ?? 30),
  };
  if (config.gatewaySecret.length < 32 || config.opsSecret.length < 32) throw new Error('GATEWAY_SECRET_FILE and OPS_SECRET_FILE must each hold at least 32 characters');
  const gateway = new SwarmClient({ baseUrl: config.swarmUrl, token: await secret('SWARM_GATEWAY_TOKEN') });
  const operator = new SwarmClient({ baseUrl: config.swarmUrl, token: await secret('SWARM_OPERATOR_TOKEN') });
  const visitors = await core.createVisitorStore(join(config.dataDir, 'visitors.json'));
  const blocks = await core.createBlocklist(join(config.dataDir, 'blocks.json'));
  const hub = core.createEmitter({ url: env.HUB_EVENTS_URL, secret: await secret('HUB_EVENTS_SECRET') });
  const alert = core.createAlerter({ url: env.ALERTS_URL, authHeader: await secret('ALERTS_AUTH'), consoleUrl: env.CONSOLE_URL });
  const emit = (type, data) => { hub(type, data); alert(type, data); };
  const port = Number(env.PORT ?? 8080);
  const presence = core.createPresence({ ttlMs: Number(env.PRESENCE_TTL_MS ?? 90_000) });
  createServer(gatewayHandler({ gateway, operator, config, visitors, emit, presence, blocks })).listen(port, env.LISTEN_HOST ?? '0.0.0.0', () => {
    console.log(`public chat gateway listening on ${port} for agent ${config.agentName}`);
  });
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  main().catch((error) => { console.error(String(error?.message ?? error)); process.exit(1); });
}

// Pure gateway logic, kept free of I/O frameworks so it can be tested without a
// daemon. server.mjs wires it to HTTP, the SDK and the data directory.
import { createHmac, timingSafeEqual } from 'node:crypto';
import { appendFile, mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { alertFor, alertHeaders } from './alerts.mjs';

export const VISITOR_ID = /^[A-Za-z0-9_-]{16,64}$/;

/** Constant-time comparison for shared secrets. */
export function sameSecret(given, expected) {
  if (typeof given !== 'string' || typeof expected !== 'string' || !expected) return false;
  const a = Buffer.from(given), b = Buffer.from(expected);
  return a.length === b.length && timingSafeEqual(a, b);
}

/** Sliding-window counter per key, bounded in memory. */
export function createLimiter({ limit, windowMs, maxKeys = 50_000, now = Date.now }) {
  const hits = new Map();
  return (key) => {
    const t = now();
    const recent = (hits.get(key) ?? []).filter((at) => t - at < windowMs);
    if (recent.length >= limit) { hits.set(key, recent); return false; }
    recent.push(t);
    hits.delete(key);
    hits.set(key, recent);
    if (hits.size > maxKeys) hits.delete(hits.keys().next().value);
    return true;
  };
}

/** Checks one visitor message; returns an error string or "". */
export function messageProblem(body, maxChars) {
  if (!body || typeof body !== 'object') return 'invalid body';
  if (typeof body.visitor_id !== 'string' || !VISITOR_ID.test(body.visitor_id)) return 'invalid visitor_id';
  if (typeof body.message !== 'string' || !body.message.trim()) return 'message is required';
  if (body.message.length > maxChars) return `message is longer than ${maxChars} characters`;
  return '';
}

/**
 * Visitor -> conversation map. The gateway, never the model or the browser,
 * decides which session a visitor's message goes to. Persisted as one JSON file
 * (mode 0600), bounded, oldest evicted first.
 */
export async function createVisitorStore(file, { maxVisitors = 50_000, now = Date.now } = {}) {
  let visitors = new Map();
  try {
    visitors = new Map(Object.entries(JSON.parse(await readFile(file, 'utf8'))));
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  let writing = Promise.resolve();
  const save = () => {
    writing = writing.then(async () => {
      const tmp = `${file}.tmp`;
      await writeFile(tmp, JSON.stringify(Object.fromEntries(visitors)), { mode: 0o600 });
      await rename(tmp, file);
    });
    return writing;
  };
  return {
    get: (visitorId) => visitors.get(visitorId),
    bySession: (sessionId) => [...visitors].find(([, v]) => v.session_id === sessionId)?.[0],
    list: () => [...visitors].map(([visitor_id, v]) => ({ visitor_id, ...v })),
    async touch(visitorId, update) {
      const current = visitors.get(visitorId) ?? { created_at: now(), messages: 0 };
      visitors.delete(visitorId);
      visitors.set(visitorId, { ...current, ...update, last_at: now() });
      while (visitors.size > maxVisitors) visitors.delete(visitors.keys().next().value);
      await save();
      return visitors.get(visitorId);
    },
    flush: () => writing,
  };
}

/**
 * Who on the team is reachable now. The staff console sends a heartbeat while
 * it is open and set to available; an entry lapses after its TTL, so a closed
 * laptop reads as away within a minute or two without any sign-out. The TTL
 * allows for browsers slowing a background tab's timers to once a minute.
 */
export function createPresence({ ttlMs = 90_000, maxStaff = 20, now = Date.now } = {}) {
  const staff = new Map();
  const online = () => {
    const t = now();
    for (const [id, s] of staff) if (s.until <= t) staff.delete(id);
    return [...staff.values()].map(({ name, role }) => ({ name, role }));
  };
  return {
    beat({ id, name, role }) {
      staff.delete(id);
      staff.set(id, { name, role, until: now() + ttlMs });
      while (staff.size > maxStaff) staff.delete(staff.keys().next().value);
      return online();
    },
    leave(id) { staff.delete(id); return online(); },
    online,
  };
}

/**
 * Recent gateway events for the staff console to long-poll: each has a
 * sequence number, and wait() resolves as soon as something newer arrives.
 */
export function createFeed({ max = 500, now = Date.now } = {}) {
  const events = [];
  const waiters = new Set();
  let seq = 0;
  const since = (after) => events.filter((e) => e.seq > after);
  return {
    push(type, data) {
      events.push({ seq: ++seq, type, at: now(), ...data });
      if (events.length > max) events.shift();
      for (const wake of waiters) wake();
      waiters.clear();
    },
    since,
    latest: () => seq,
    wait(after, ms) {
      if (since(after).length) return Promise.resolve(since(after));
      return new Promise((resolve) => {
        const wake = () => { clearTimeout(timer); waiters.delete(wake); resolve(since(after)); };
        const timer = setTimeout(wake, ms);
        waiters.add(wake);
      });
    },
  };
}

/**
 * Where a visitor stands with a person: offered (the agent asked), waiting
 * (they said yes), joined (someone accepted), missed (nobody accepted in time).
 * A waiting request lapses to missed after acceptTimeoutMs.
 */
export function personState(visitor, { acceptTimeoutMs, now = Date.now }) {
  const person = visitor?.person;
  if (!person) return null;
  if (person.state === 'waiting' && now() - person.since > acceptTimeoutMs) return { ...person, state: 'missed' };
  return person;
}

/** Who holds the conversation, from the session the gateway already reads. */
export function takeoverOf(detail) {
  const takeover = detail?.raw?.session?.metadata?.takeover;
  return takeover && typeof takeover === 'object' ? takeover : undefined;
}

/** Who sent a person's reply, from the names the gateway recorded at send time. */
export function authorOf(message, authors) {
  const key = message?.id ?? message?.global_seq;
  return key !== undefined && authors && Object.hasOwn(authors, String(key)) ? authors[String(key)] : undefined;
}

/** Replies the visitor should see after `afterSeq`: the agent's and a person's (with the person's name). */
export function repliesAfter(detail, afterSeq = 0, authors = {}) {
  return (detail?.messages ?? [])
    .filter((m) => m.role === 'assistant' && Number(m.global_seq ?? m.seq ?? 0) > afterSeq && m.metadata?.visible !== false)
    .map((m) => {
      const person = m.metadata?.source === 'operator';
      const reply = { id: m.id, seq: Number(m.global_seq ?? m.seq ?? 0), from: person ? 'person' : 'agent', text: String(m.content ?? '') };
      const author = person && authorOf(m, authors);
      return author ? { ...reply, name: author.name } : reply;
    });
}

/**
 * Networks a person blocked from the chat, keyed by the edge's keyed hash of the
 * visitor's IP (the gateway never sees the address). An entry lapses at `until`.
 * Persisted as one JSON file (mode 0600), bounded.
 */
export const NET = /^[A-Za-z0-9_-]{8,64}$/;
export async function createBlocklist(file, options = {}) {
  let saved = {};
  try {
    if (file) saved = JSON.parse(await readFile(file, 'utf8'));
  } catch (error) {
    if (error.code !== 'ENOENT') throw error;
  }
  return blocklist(new Map(Object.entries(saved)), file, options);
}

/** The block list itself; without a file (tests, the default) it lives in memory only. */
export function blocklist(blocks = new Map(), file = undefined, { max = 10_000, now = Date.now } = {}) {
  let writing = Promise.resolve();
  const save = () => {
    if (!file) return writing;
    writing = writing.then(async () => {
      const tmp = `${file}.tmp`;
      await writeFile(tmp, JSON.stringify(Object.fromEntries(blocks)), { mode: 0o600 });
      await rename(tmp, file);
    });
    return writing;
  };
  const live = () => {
    const t = now();
    let changed = false;
    for (const [net, b] of blocks) if (b.until <= t) { blocks.delete(net); changed = true; }
    if (changed) save();
    return blocks;
  };
  return {
    blocked: (net) => typeof net === 'string' && live().has(net),
    async add(net, entry) {
      blocks.delete(net);
      blocks.set(net, { ...entry, at: now() });
      while (blocks.size > max) blocks.delete(blocks.keys().next().value);
      await save();
    },
    async remove(net) { const had = blocks.delete(net); if (had) await save(); return had; },
    list: () => [...live()].map(([net, b]) => ({ net, ...b })),
    flush: () => writing,
  };
}

/** The lead card the agent filled in, as the widget shows it for the visitor to check. */
export function publicProposal(proposal) {
  return proposal ? { name: proposal.name ?? '', email: proposal.email ?? '', need: proposal.need ?? '' } : null;
}

/** A person's hand-off state as the widget sees it: no summaries or internal notes. */
export function publicPerson(person) {
  return person ? { state: person.state, name: person.name, role: person.role } : null;
}

/** The newest assistant message's sequence in `messages`, or 0. */
export function latestReplySeq(messages) {
  return (messages ?? []).reduce((max, m) => (m.role === 'assistant' ? Math.max(max, Number(m.global_seq ?? m.seq ?? 0)) : max), 0);
}

export function signBody(secret, body) {
  return 'sha256=' + createHmac('sha256', secret).update(body).digest('hex');
}

/**
 * Sends gateway events (conversation.started, lead.captured,
 * handoff.requested, conversation.ended, conversation.reopened, visitor.message,
 * chat.error) to a monitoring hub, signed with HMAC-SHA256.
 * Delivery is best effort and never blocks or fails a visitor's request.
 */
export function createEmitter({ url, secret, fetchImpl = fetch, now = Date.now }) {
  if (!url || !secret) return () => {};
  return (type, data) => {
    const body = JSON.stringify({ type, at: now(), ...data });
    fetchImpl(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', 'X-Signature': signBody(secret, body) },
      body,
      signal: AbortSignal.timeout(5000),
    }).catch(() => {});
  };
}

/**
 * Phone alerts for the events a person should act on, posted to an alert
 * service in swarm-watch's event format ({kind, severity, title, body, notify})
 * with its own bearer token; that service decides what reaches Telegram. Each
 * alert links to the staff console on that conversation. Best effort: a failed
 * alert never blocks or fails a visitor's request.
 */
export function createAlerter({ url, authHeader, consoleUrl = '', fetchImpl = fetch }) {
  if (!url || !authHeader) return () => {};
  const headers = alertHeaders(authHeader);
  return (type, data) => {
    const event = alertFor(type, data, consoleUrl);
    if (!event) return;
    fetchImpl(url.replace(/\/$/, '') + '/v1/event', { method: 'POST', headers, body: JSON.stringify(event), signal: AbortSignal.timeout(5000) })
      .then((res) => { if (!res.ok) console.error('alert not accepted:', res.status); })
      .catch(() => {});
  };
}

/**
 * The agent's client tools, bound to one conversation. Identity comes from the
 * gateway's own mapping (sessionId -> visitorId); the model supplies only the
 * business inputs, which the daemon has already checked against each schema.
 */
/** Same rule as capture_lead's schema: one @, a dot in the domain, no spaces. */
export const EMAIL = /^[^@\s]{1,64}@[^@\s]{1,190}\.[A-Za-z]{2,24}$/;

/**
 * Records contact details for a person to follow up: appended to leads.jsonl
 * (mode 0600), kept on the visitor for the staff console, and sent as an event.
 */
export async function saveLead({ dataDir, emit, visitors, sessionId, visitorId, lead, source, now = Date.now }) {
  await mkdir(dataDir, { recursive: true, mode: 0o700 });
  const record = { at: now(), session_id: sessionId, visitor_id: visitorId, source, ...lead };
  await appendFile(join(dataDir, 'leads.jsonl'), JSON.stringify(record) + '\n', { mode: 0o600 });
  await visitors?.touch(visitorId, { contact: { ...lead, at: record.at } });
  emit('lead.captured', { session_id: sessionId, visitor_id: visitorId, source, lead });
}

export function toolHandlers({ playbook, dataDir, bookingUrl, emit, sessionId, visitorId, endConversation, presence, setPerson, setProposal, visitors, now = Date.now }) {
  return {
    // Fills in a contact card the page shows the visitor to check, edit and send.
    // Nothing is saved until the visitor presses Send: the agent can only suggest.
    propose_lead: async ({ name, email, need }) => {
      if (!setProposal) throw new Error('propose_lead is not available here');
      await setProposal({ name: name ?? '', email: email ?? '', need, at: now() });
      emit('lead.proposed', { session_id: sessionId, visitor_id: visitorId });
      return { shown: true, next: 'The page now shows the visitor a card with these details to check, fill in and send. In one short sentence, ask them to check it and press Send. Do not repeat the details.' };
    },
    get_playbook: async ({ topic }) => {
      if (!Object.hasOwn(playbook, topic)) throw new Error(`No playbook entry for ${topic}`);
      return { topic, answer: playbook[topic] };
    },
    capture_lead: async ({ name, email, need }) => {
      await saveLead({ dataDir, emit, visitors, sessionId, visitorId, lead: { name, email, need }, source: 'agent', now });
      return { saved: true, next: 'A person will follow up by email.' };
    },
    // Asks whether someone is online; if so the page shows the visitor a Yes button.
    offer_person: async ({ summary }) => {
      const [staff] = presence.online();
      if (!staff) return { available: false, next: 'Nobody from the team is online right now. Offer to have a person follow up by email: use propose_lead if you have it (the visitor checks and sends a card), otherwise ask for their name, email and need and save them with capture_lead.' };
      await setPerson({ state: 'offered', name: staff.name, role: staff.role, summary, since: now() });
      emit('person.offered', { session_id: sessionId, visitor_id: visitorId, summary });
      return { available: true, name: staff.name, role: staff.role, next: `The page now shows the visitor a one-tap card to bring ${staff.name}, the ${staff.role}, into this chat. In one short sentence, point them to it. If they say yes in words instead, call handoff_to_human.` };
    },
    handoff_to_human: async ({ summary }) => {
      const [staff] = presence.online();
      if (staff) await setPerson({ state: 'waiting', name: staff.name, role: staff.role, summary, since: now() });
      emit('handoff.requested', { session_id: sessionId, visitor_id: visitorId, summary, staff_online: Boolean(staff) });
      return staff
        ? { requested: true, waiting: true, name: staff.name, next: `Tell the visitor ${staff.name} has been asked to join and will appear in this chat shortly.` }
        : { requested: true, waiting: false, booking_url: bookingUrl, next: 'Nobody is online right now. Say a person will follow up by email: use propose_lead if you have it, otherwise offer to take their name and email with capture_lead.' };
    },
    // A hard stop the model can trigger but not undo: the gateway refuses every
    // later message from this visitor without a model run until a person reopens it.
    end_conversation: async ({ reason, note }) => {
      await endConversation({ reason, at: now() });
      emit('conversation.ended', { session_id: sessionId, visitor_id: visitorId, reason, note });
      return { ended: true, next: 'The chat is now closed and the visitor cannot send more messages. Say one short, polite goodbye.' };
    },
  };
}

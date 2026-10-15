// Pure gateway logic, kept free of I/O frameworks so it can be tested without a
// daemon. server.mjs wires it to HTTP, the SDK and the data directory.
import { createHmac, timingSafeEqual } from 'node:crypto';
import { appendFile, mkdir, readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';

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

/** Who holds the conversation, from the session the gateway already reads. */
export function takeoverOf(detail) {
  const takeover = detail?.raw?.session?.metadata?.takeover;
  return takeover && typeof takeover === 'object' ? takeover : undefined;
}

/** Replies the visitor should see after `afterSeq`: the agent's and a person's. */
export function repliesAfter(detail, afterSeq = 0) {
  return (detail?.messages ?? [])
    .filter((m) => m.role === 'assistant' && Number(m.global_seq ?? m.seq ?? 0) > afterSeq && m.metadata?.visible !== false)
    .map((m) => ({ id: m.id, seq: Number(m.global_seq ?? m.seq ?? 0), from: m.metadata?.source === 'operator' ? 'person' : 'agent', text: String(m.content ?? '') }));
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
 * handoff.requested, chat.error) to a monitoring hub, signed with HMAC-SHA256.
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
 * The agent's client tools, bound to one conversation. Identity comes from the
 * gateway's own mapping (sessionId -> visitorId); the model supplies only the
 * business inputs, which the daemon has already checked against each schema.
 */
export function toolHandlers({ playbook, dataDir, bookingUrl, emit, sessionId, visitorId, now = Date.now }) {
  return {
    get_playbook: async ({ topic }) => {
      if (!Object.hasOwn(playbook, topic)) throw new Error(`No playbook entry for ${topic}`);
      return { topic, answer: playbook[topic] };
    },
    capture_lead: async ({ name, email, need }) => {
      await mkdir(dataDir, { recursive: true, mode: 0o700 });
      const lead = { at: now(), session_id: sessionId, visitor_id: visitorId, name, email, need };
      await appendFile(join(dataDir, 'leads.jsonl'), JSON.stringify(lead) + '\n', { mode: 0o600 });
      emit('lead.captured', { session_id: sessionId, visitor_id: visitorId, lead: { name, email, need } });
      return { saved: true, next: 'A person will follow up by email.' };
    },
    handoff_to_human: async ({ summary }) => {
      emit('handoff.requested', { session_id: sessionId, visitor_id: visitorId, summary });
      return { requested: true, booking_url: bookingUrl, next: 'A person has been asked to join. If nobody is around, they will follow up.' };
    },
  };
}

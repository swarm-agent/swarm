import assert from 'node:assert/strict';
import { test } from 'node:test';
import { copyTagsToMarkdown, hasCopyTags } from '../copy-tags.js';
import { SwarmChatNamespace } from '../chat.js';
import { SwarmTransport } from '../transport.js';
import type { SessionWatchState, V3Message, V3RunState } from '../realtime.js';

// Requirement: Swarm agents emit `<copy label="…">…</copy>` snippet tags (a Desktop/TUI
// copy-button convention, parsed by internal/copyblock and web markdown/render.tsx).
// Third-party apps printed them literally. copyTagsToMarkdown must turn them into
// Markdown code blocks with the same parsing rules (label/title/name, tags inside fenced
// code untouched, newline trimming) and leave tag-free text byte-identical.
// Boundary: pure function; the narrowest layer for the conversion rules.
test('copyTagsToMarkdown converts copy tags to Markdown code blocks', () => {
  assert.equal(copyTagsToMarkdown('plain text, no tags'), 'plain text, no tags');
  assert.equal(copyTagsToMarkdown('a <b>bold</b> word'), 'a <b>bold</b> word');
  assert.equal(
    copyTagsToMarkdown('Run this:\n<copy label="Install">\nnpm install\n</copy>\nThen start.'),
    'Run this:\n**Install**\n```\nnpm install\n```\nThen start.'
  );
  assert.equal(copyTagsToMarkdown('Run <copy>ls -la</copy> now'), 'Run \n```\nls -la\n```\n now');
  assert.equal(copyTagsToMarkdown("<COPY title='Env'>A=1</COPY>"), '**Env**\n```\nA=1\n```');
  // A payload with its own ``` fence gets a longer outer fence.
  assert.equal(copyTagsToMarkdown('<copy>```js\nx()\n```</copy>'), '````\n```js\nx()\n```\n````');
  // Tags inside a fenced code block are documentation, not snippets.
  const fenced = '```html\n<copy label="x">keep</copy>\n```\n';
  assert.equal(copyTagsToMarkdown(fenced), fenced);
  // An unclosed tag becomes a block that runs to the end.
  assert.equal(copyTagsToMarkdown('Use <copy label="Cmd">make test'), 'Use \n**Cmd**\n```\nmake test');
  assert.equal(hasCopyTags('x <copy>y</copy>'), true);
  assert.equal(hasCopyTags(fenced), false);
  assert.equal(hasCopyTags('no tags'), false);
});

// Requirement: in streaming mode, converting each growing prefix of the raw text only ever
// appends to the previous result, so onText deltas stay exact. Threat: half-written tags
// (`<cop`, `<copy label="x`, `</co`) leaking, or re-rendering earlier output.
test('streaming conversion of every prefix only appends', () => {
  const raw = 'Steps:\n<copy label="Build and run">\nnpm ci\nnpm test\n\n</copy> then <copy>git push</copy>\nDone <b>.';
  let previous = '';
  for (let i = 1; i <= raw.length; i++) {
    const shown = copyTagsToMarkdown(raw.slice(0, i), { streaming: true });
    assert.ok(shown.startsWith(previous), `prefix ${i}: ${JSON.stringify(previous)} -> ${JSON.stringify(shown)}`);
    assert.ok(!/<\/?c(o(p(y)?)?)?$|<copy/i.test(shown), `tag leaked at prefix ${i}: ${JSON.stringify(shown)}`);
    previous = shown;
  }
  assert.equal(previous, 'Steps:\n**Build and run**\n````\nnpm ci\nnpm test\n\n````\n then \n````\ngit push\n````\nDone <b>.');
});

function harness() {
  const chat = new SwarmChatNamespace(new SwarmTransport({ baseUrl: 'http://localhost' }));
  let update!: (state: SessionWatchState) => void;
  (chat as any).realtime.watchSession = (_id: string, options: any) => {
    update = options.onChange;
    return { ready: Promise.resolve(), done: new Promise(() => {}), dispose() {} };
  };
  const snapshot: any = { sessions_by_id: {}, events_by_session: { s: [] }, current_run_state_by_session: {}, session_views_by_id: {} };
  const state: SessionWatchState = { sessionId: 's', snapshot, messages: [], live: [], status: 'live' };
  const run = (id: string, active: boolean) => {
    snapshot.current_run_state_by_session.s = { session_id: 's', run_id: id, active, status: active ? 'running' : 'completed', completed_at: active ? undefined : 1 } as V3RunState;
  };
  return { chat, state, run, push: (patch: Partial<SessionWatchState>) => update(Object.assign(state, patch)) };
}

// Requirement: chat.stream delivers clean Markdown by default while keeping the onText
// contract (deltas concatenate to fullText), onComplete messages are clean, and
// rawCopyTags: true returns the stored text. Boundary: chat.stream reducer with injected
// watch states (the source for SwarmBrowserChat text); not provider-backed evidence.
test('chat.stream converts copy tags by default and keeps raw text on request', async () => {
  const final = 'Run:\n<copy label="Test">npm test</copy>\nok';
  const message = { id: 'm', session_id: 's', global_seq: 2, role: 'assistant', content: final, created_at: 1, metadata: { run_id: 'r', stream_id: 'st' } } as V3Message;
  for (const raw of [false, true]) {
    const h = harness();
    const deltas: string[] = [];
    let full = '';
    let completed: V3Message[] = [];
    await h.chat.stream('s', { rawCopyTags: raw, onText: (d, f) => { deltas.push(d); full = f; }, onComplete: (messages) => { completed = messages; } });
    h.run('r', true);
    for (const cut of [5, 12, 20, 30, final.length - 3]) h.push({ live: [{ runId: 'r', streamId: 'st', text: final.slice(0, cut) }] });
    h.push({ live: [] });
    h.run('r', false);
    h.push({ messages: [message] });
    assert.equal(deltas.join(''), full);
    if (raw) {
      assert.equal(full, final);
      assert.equal(completed[0]?.content, final);
    } else {
      assert.equal(full, 'Run:\n**Test**\n````\nnpm test\n````\nok');
      assert.equal(completed[0]?.content, 'Run:\n**Test**\n```\nnpm test\n```\nok');
      assert.equal(message.content, final, 'the watched state itself is not mutated');
    }
  }
});

// Requirement: chat.listMessages returns clean assistant text by default and the stored
// text with rawCopyTags; user messages are never rewritten.
test('chat.listMessages cleans assistant copy tags unless rawCopyTags', async () => {
  const chat = new SwarmChatNamespace(new SwarmTransport({ baseUrl: 'http://localhost' }));
  const messages = [
    { id: 'u', role: 'user', content: 'literal <copy>x</copy>' },
    { id: 'a', role: 'assistant', content: 'Use <copy label="Go">go test ./...</copy>' },
  ];
  (chat as any).sessions.get = async () => ({ id: 's', messages });
  const clean = await chat.listMessages('s');
  assert.equal(clean[0].content, 'literal <copy>x</copy>');
  assert.equal(clean[1].content, 'Use \n**Go**\n```\ngo test ./...\n```');
  const raw = await chat.listMessages('s', { rawCopyTags: true });
  assert.equal(raw[1].content, 'Use <copy label="Go">go test ./...</copy>');
});

import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import vm from 'node:vm';

// Purpose: app.js render must replace durable snapshots rather than append deltas,
// separate transient drafts, display exact pending IDs, and disable stale run stop.
// A minimal DOM fixture exercises the actual renderer without provider simulation;
// real browser layout/accessibility and live inference remain parent validation.
test('UI replaces history, separates drafts and uses exact permission identity', async () => {
  class Element {
    constructor() { this.children = []; this.dataset = {}; this.value = ''; this.classList = { toggle() {} }; }
    addEventListener() {}
    replaceChildren(...children) { this.children = children; }
    append(...children) { this.children.push(...children); }
  }
  const elements = new Map();
  const get = id => { if (!elements.has(id)) elements.set(id, new Element()); return elements.get(id); };
  const context = vm.createContext({ document: { getElementById: get, createElement: () => new Element(), createTextNode: text => ({ textContent: text }) }, window: { addEventListener() {} }, console });
  vm.runInContext(await readFile(new URL('../public/app.js', import.meta.url), 'utf8'), context);
  const state = { sessionId: 'one', status: 'live', messages: [{ id: 'message', role: 'assistant', content: '<script>untrusted</script>' }],
    live: [{ runId: 'run', streamId: 'stream', text: 'draft' }], snapshot: { omissions: [], events_by_session: { one: [] },
      current_run_state_by_session: { one: { active: true, run_id: 'run' } }, session_views_by_id: { one: { pending_permissions: [{ id: 'exact-id', session_id: 'one', tool_name: 'write', call_id: 'call', tool_arguments: '{}' }] } } } };
  context.state = state;
  vm.runInContext('render(state); render(state)', context);
  assert.equal(get('messages').children.length, 1);
  assert.equal(get('messages').children[0].dataset.messageId, 'message');
  assert.equal(get('messages').children[0].children[1].textContent, '<script>untrusted</script>');
  assert.equal(get('live').children.length, 1);
  assert.match(get('permissions').children[0].children[1].textContent, /Permission: exact-id/);
  assert.equal(get('stop').disabled, false);
  state.live = []; state.snapshot.current_run_state_by_session.one.active = false; state.snapshot.session_views_by_id.one.pending_permissions = [];
  vm.runInContext('render(state)', context);
  assert.equal(get('messages').children.length, 1);
  assert.equal(get('live').children.length, 0);
  assert.equal(get('permissions').children.length, 0);
  assert.equal(get('stop').disabled, true);
});

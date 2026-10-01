import test from 'node:test';
import assert from 'node:assert/strict';
import vm from 'node:vm';
import { readFile } from 'node:fs/promises';

async function ui(fetch) {
  const elements = new Map();
  const element = () => ({ value: '', textContent: '', hidden: false, options: [], children: [],
    append(...items) { this.children.push(...items); }, replaceChildren(...items) { this.children = items; },
    reset() {}, removeAttribute() {}, classList: { toggle() {} } });
  const get = id => { if (!elements.has(id)) elements.set(id, element()); return elements.get(id); };
  const context = vm.createContext({ document: { getElementById: get, querySelectorAll: () => [], createElement: element },
    window: { addEventListener() {} }, fetch, AbortController, TextDecoder, crypto: { randomUUID: () => 'request' }, location: { reload() {} } });
  vm.runInContext(await readFile(new URL('../public/app.js', import.meta.url), 'utf8'), context);
  return { context, get, run: code => vm.runInContext(code, context) };
}
const response = data => ({ ok: true, json: async () => data });

// Purpose: the real UI action wrapper must preserve discovery errors instead of
// replacing them with success. A minimal DOM VM tests behavior, not source strings.
test('discovery failure is visible through the action wrapper', async () => {
  const app = await ui(async () => ({ ok: false, json: async () => ({ error: 'Denied' }) }));
  await app.run('action(discovery)()');
  assert.match(app.get('status').textContent, /Some resources are unavailable/);
});

// Purpose: same-agent concurrent view reads must not render an older response or
// reopen a stream after navigation. Deferred fetches isolate loadTab's generation gate.
test('older tab response cannot overwrite a newer view', async () => {
  const pending = [];
  const app = await ui((_path, options) => {
    if (JSON.parse(options.body).op === 'tasks') return new Promise(resolve => pending.push(resolve));
    return Promise.resolve({ ok: false });
  });
  app.run("selected = { id: 'agent', project_id: 'project' }; tab = 'tasks'");
  const old = app.run('loadTab()'), next = app.run('loadTab()');
  pending[1](response({ tasks: [{ id: 'new', title: 'New' }] })); await next;
  pending[0](response({ tasks: [{ id: 'old', title: 'Old' }] })); await old;
  assert.equal(app.get('task-list').children.length, 1);
  assert.equal(app.get('task-list').children[0].children[0].textContent, 'New');
});

// Purpose: a late successful action cannot hide a newer actionable error. Execute
// the actual wrapper with a deferred older request to prove status ordering.
test('late action success does not overwrite a newer error', async () => {
  const app = await ui(async () => response({}));
  const older = app.run('action(() => new Promise(resolve => { globalThis.finish = resolve; }))()');
  await app.run("action(async () => { throw new Error('New failure'); })()");
  app.run('finish()'); await older;
  assert.equal(app.get('status').textContent, 'New failure');
});

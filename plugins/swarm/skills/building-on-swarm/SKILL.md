---
name: building-on-swarm
description: Build a custom app on Swarm with the @swarm-agent/sdk — onboarding, streaming chat, permissions, sessions and context, projects and the orchestrator, custom tools with sealed agents, workers, usage and monitoring. Use when the person wants their own UI, bot, dashboard or service on top of their Swarm.
---

# Building on Swarm

Your app is a small Node server plus a web page. The server talks to Swarm with
`@swarm-agent/sdk`; the page talks only to your server. Never give the page a
Swarm token, key or the Swarm socket.

## 1. Connect

```bash
npm install @swarm-agent/sdk   # or a local tarball; it depends on `ws` from npm
```

```js
import { SwarmClient } from '@swarm-agent/sdk';
// The owner socket: no token; the server runs as the same OS user as Swarm.
const client = new SwarmClient({
  socketPath: process.env.SWARM_SOCKET_PATH || '/var/lib/swarmd/local-transport/api.sock',
});
```

Serve `node_modules/@swarm-agent/sdk/dist/` at `/sdk/` and import the browser
client as a plain ES module: `import { SwarmBrowserChat, describePermission,
answerPermission } from '/sdk/browser.js'`. Bind your server to `127.0.0.1`
unless you add your own authentication. Loopback alone does not stop other web
pages in the person's browser from calling your server: on every request that
changes something (POST/PUT/DELETE, and the WebSocket upgrade), refuse an
`Origin` that is not your own page, and accept only `Content-Type:
application/json` bodies.

## 2. Onboarding

Decide what is missing from Swarm's state, not from
`config.desktop_onboarding_complete` (it can be true on a fresh Swarm):

| Step | Done when | Do |
|---|---|---|
| Name | `(await client.onboarding.get()).identity.bootstrapped` | ask name + Swarm name, `client.onboarding.ensureBootstrapped({ username, swarm_name })` |
| Model access | `heuristics.credential_count > 0` | API key or ChatGPT sign-in (below) |
| Models | `(await client.settings.agentModels())` has a swarm action model | usually already done: Swarm sets recommended models when the first key or sign-in is saved; otherwise `client.settings.applyProviderFleet(provider)` |
| Workspace | your app's workspaces are registered (`client.workspaces.list()`) | ask which existing folders or repositories the work is in and register each with `client.workspaces.add({ path, context_only: true, require_existing_content: true, make_current: false })` (refuses empty, system and nested folders; changes nothing in the folder). Only for a new project from scratch: `client.workspaces.create({ parent_path, name })` (an empty repository; `name`: letters, digits, `-` and `_`, at most 63). Store each `path` and find it again by that path |
| Finish | all above | `client.onboarding.update({ desktop_onboarding_complete: true })` |

Re-check `onboarding.get()` every few seconds while onboarding is open: a key
or sign-in can be added elsewhere (another app, Claude, the CLI), and the page
must move on without a reload.

Before the name step, almost every other call fails ("product identity has not
been bootstrapped"): call only `onboarding.get` until then. Before any model is
set, `agentModels()` returns every role unset (empty provider and model,
`updated_at` 0): treat that as "no models yet", not as an error to show. Older
Swarms answer a 404 "agent model settings not found" instead; treat it the same.

- **Providers:** `client.settings.providers()` (after the name step) returns
  `{ id, ready, reason, auth_methods: [{ id, label, credential_type }] }` per
  provider. Model providers have a `default_model` (others, such as `exa` for
  web search, do not: leave them out of model onboarding).
  `credential_type: 'api'` takes an API key; the `codex` provider
  (`credential_type: 'oauth'`) is the ChatGPT sign-in. `ready` is true once a
  working credential exists. Show `reason` only as a short status, and never
  tell people to use Swarm's own settings screens or commands.
- **API key:** first credential `client.onboarding.credential({ provider, type:
  'api', api_key, active: true })`; later ones `client.auth.credentials.save(...)`.
  The key goes form → your server → Swarm only: never log, store or return it;
  redact it from any error you show. Swarm verifies it: show its error.
- **ChatGPT sign-in:** `client.auth.codex.loginDevice({ onCode })` on the
  server; show `verification_url` and `user_code`; the page polls your server.
  It resolves when the sign-in succeeded and throws on failure or expiry. It
  can take minutes and cannot be cancelled in Swarm (abandoning it does not
  revoke a granted sign-in).
- **Existing credential:** provider from `client.auth.credentials.list()`
  (redacted records; the active one's `provider`), then the Models step.

## 3. Conversations

Use a project with your workspace; its conversations are orchestrator sessions
and support clearing context. Without a workspace the orchestrator cannot run
commands or hand out work.

Creating a project makes Swarm write its `project.md` (the orchestrator's
context) from the workspaces' AGENTS.md, README and file tree. Open
conversations once `project.context_generation.status` is `ready`: re-read with
`client.projects.get(id)` (or follow realtime project updates); on `failed`,
`client.projects.retryContext(id, project.context_generation.attempt)`.

```js
const project = await client.projects.ensureProject('My app', { workspacePath });
const list = await client.projects.listConversations(project.id, { limit: 100 });
const created = await client.projects.createConversation(project.id, {
  title: 'New conversation', clientRequestId: crypto.randomUUID(),
});
```

Swarm titles a conversation from its first message; show `title` from
`listConversations`. Rename with
`client.projects.renameConversation(id, title)` (or
`client.sessions.rename(id, title)`). Do not keep your own labels.

Agent replies can contain `<copy label="…">…</copy>` snippet tags: pass reply
text through `copyTagsToMarkdown(text)` (from `/sdk/browser.js`) before
rendering it as Markdown.

## 4. Live chat

```js
client.chat.attachWebSocket(server, { path: '/ws', autoApprovePermissions: false });
```

```js
const chat = new SwarmBrowserChat();               // same-origin /ws
chat.on('state', s => render(s.messages, s.live)); // the rendering authority
chat.on('tool_start', t => toolCard(t.id, t.name, t.arguments));
chat.on('tool_delta', (t, chunk) => toolOutput(t.id, chunk));
chat.on('tool_done', t => toolDone(t.id));
chat.on('permissions', p => renderPermissions(p.map(describePermission)));
chat.on('permission_removed', id => removePermission(id));
chat.on('error', showError);
await chat.connect();
await chat.subscribe(conversationId);              // before sending
chat.sendMessage(text);
```

- Replace history (`state.messages`) and the live reply (`state.live`) on each
  `state`; never append `fullText` to history. Reconnects resume without loss
  or repeats; a send while disconnected throws: show it, never resend silently.
  Send errors are not tied to a message: attach them to your latest
  unacknowledged send.
- Tool cards in history come from the events in
  `state.snapshot.events_by_session[id]` (newest 1000) whose `event_type` is
  `session.tool.started`, `session.tool.completed` or `session.tool.failed`.
  Their `payload` has `call_id` (match the live callbacks' `callId` on it),
  `tool_name`, `arguments` (a JSON string), `status`, and on completion
  `output` or `error`; ignore `payload.metadata`. Merge with the live
  callbacks; hide `tool` role messages.
- Timestamps (`created_at` on messages, `ts_unix_ms` on events) are
  milliseconds since the epoch.
- Busy or idle: `state.snapshot.current_run_state_by_session[id]` has `active`
  (true while a run is going, including while it waits for a permission) and
  `status` (`completed`, `failed` or `cancelled` once it ended). Decide busy
  from `active`, not from status names.
- Switch conversations with `await chat.subscribe(otherId)`. Selecting the
  conversation that is already open must do nothing: never clear the thread
  before the new subscription has delivered its state.
- If a conversation is too large to load whole, `state.historyOmitted` is
  true and only the newest part is in `state.messages`: say older history is
  not shown.
- Offer Stop while a run is active: `client.sessions.stopRun(id, { run_id })`
  with the `run_id` from `current_run_state_by_session[id]`. A stopped plan
  pauses; the next message continues it.

## 5. Permissions

`describePermission(p)` gives `kind` (`tool` or `ask-user`), `toolName`,
`title`, `context`, `rawArguments`, `questions` and `actions`. Lead each card
with `title` and `context` in plain words; put `rawArguments` behind a
"details" toggle, never as the main content. Render as text
(never HTML) and resolve only on the person's click:

```js
await chat.resolvePermission(p.id, 'allow_once',
  describePermission(p).kind === 'ask-user' ? answerPermission(p, input) : {});
await chat.resolvePermission(p.id, 'deny');
```

`input` for `answerPermission`: a string when `describePermission(p).structured`
is false (one free question), else a map `{ [question.id]: answer }`. An
option with `custom: true` is not an answer itself: show a text box for it.

The `permissions` event repeats the same requests often: key cards by `id`,
keep unsent answers across re-renders, remove a card only on
`permission_removed`. Some destructive commands are refused by Swarm policy and
never become requests.

## 6. Clear context

```js
await client.sessions.clearContext(id, {
  expectedLastEventSeq: state.snapshot.projections_by_session[id].last_event_seq,
});
```

Allowed only while the conversation is idle (a conflict error otherwise: show
it; retry after the run). History stays visible; the model starts fresh.

## 7. Custom tools: a sealed agent

Give an agent exactly the tools your app answers, nothing else (no shell, files
or web; Swarm enforces it on every call):

```js
await client.agents.defineClientTool({
  name: 'lookup_order', description: 'Find an order by id',
  input_schema: { type: 'object', properties: { id: { type: 'string' } }, required: ['id'] },
  effect: 'read',            // 'write' = external effect: confirm with a person first
});
await client.agents.defineSealedAgent({ name: 'frontdesk', prompt: 'Help customers with orders.', tools: ['lookup_order'] });
const session = await client.sessions.create({ agent_name: 'frontdesk', title: 'Visitor', workspace_path: workspacePath }); // your app's workspace
const turn = await client.agents.converse(session.id, 'Where is order 42?', {
  lookup_order: async ({ id }) => ({ id, status: 'shipped' }),
});
// turn.reply is the answer; for a long-lived session use agents.serve(sessionId, handlers, { signal }).
```

Throw `ClientToolError('message')` to send that message to the model.

## 8. Tasks, workers and results without chat

- Project tasks: `client.projects.createTask(projectId, { agent: 'swarm', title, description })`,
  `getTask`, `listTasks`. Tasks the orchestrator makes wait in
  `pending_approval`; show them and approve only on the person's click:
  `client.projects.approveTask(projectId, taskId, guards)`, with `guards` from
  the task you showed (`plan_binding.session_id`, `plan_id`,
  `definition_revision`, else `session_id`), so a task that changed since is
  not approved.
- A running task has its own session (`task.session_id`), and its agent can
  ask for permissions or questions there: show them on the task
  (`client.permissions.listSessionPending(sessionId)`, resolve with
  `client.permissions.resolve(sessionId, id, action, …)` on the person's
  click), or the task waits forever.
- Finished work waits in `needs_review`. Its result is in `getTask(…).task`:
  `attempts[].summary` and `delivery_assessment` (changed files, commits),
  plus the last assistant message of the task's session. Then, on the
  person's click: `integrateTask(projectId, taskId)` (it reads the task and fills in the
  session and branches) merges the work into the
  workspace's branch, `completeTask` accepts it without merging,
  `rejectTask` sends it back.
- Application agents with their own instructions: `client.apps.put`,
  `createConversation`, `send`, `watchResults({ onChange })` for live task and
  worker results.
- Workers: `client.workers` (`create`, `trigger`, `listRuns`, `getRun`; a
  succeeded run's `review` says whether its work awaits review).

## 9. Usage and monitoring

- `client.agents.spendToday()`: today's cost, the daily cap and whether it is hit.
- `client.usage`: usage scope and budgets; sign-in (subscription) providers
  show no metered cost: say so in your UI.
- `client.notifications.list()` / `summary()`: Swarm's notifications. Each has
  `title`, `body`, `category`, `severity`, `status` and `read_at` (unread while
  `read_at` is empty); `notifications.update(id, { read: true })` marks one read.
- Webhooks for automations: see the SDK README "Programmable Push Alert Webhooks".

## 10. Every Swarm app needs

Onboarding that only asks for what is missing · streaming chat with tool cards ·
permission approve, deny and questions · new, list, switch conversations ·
clear context · model and provider status · errors shown, never hidden · keys
never logged or returned · loopback by default · own-origin checks on every
write.

## 11. Mistakes every earlier build made

- Clicking the open conversation blanked it (see section 4).
- Permission and task cards showed raw JSON instead of `title`/`context`.
- On phones the composer hid under a bottom tab bar: size the layout with
  `100dvh`, keep the composer in normal flow at the bottom of the scroll area
  (or pad by the tab bar's height plus `env(safe-area-inset-bottom)`), and
  check it at 390×844 with the keyboard closed and open.
- A "Not sent" banner stayed after the connection came back: clear transient
  connection errors on reconnect.
- Copy claimed "nothing starts until you approve it" while the orchestrator
  had started a task itself: describe what Swarm reports, not what you assume.
- Write routes accepted requests from other web pages (section 1).
- A form error vanished when a background poll re-rendered the card: keep
  the last error until the person changes the form.

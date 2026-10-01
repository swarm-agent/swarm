# Application agents

`client.apps` stores named, account-and-user-owned application instructions/context.
It is **not** a mutable system-agent profile, worker, deployment or new executor.
Models, tools, permissions and worktree admission still use Swarm's canonical authorities.

Use an authenticated private BFF connection to the daemon local API (Unix socket
or authenticated loopback). These routes are deliberately **not** allowed on the
container SDK listener; do not expose the privileged socket or credentials to a browser.
The routes require `sessions:read` / `sessions:write`, respectively.

```ts
const agent = await client.apps.put('editor', {
  name: 'Editor', instructions: 'Prepare drafts; do not publish.',
  context: 'Use our approved style guide.', expected_revision: 0,
});
const conversation = await client.apps.createConversation(agent.id, {
  workspace_id: savedWorkspaceId, revision: agent.revision,
  client_request_id: 'conversation-opening-1', title: 'Draft review',
});
await client.apps.send(agent.id, conversation.id, {
  content: 'Prepare a launch announcement.', client_request_id: 'event-1',
});
// Persist these IDs in the application; reopen after reload or daemon restart.
const result = await client.apps.conversation(agent.id, conversation.id);
```

`PUT /v3/application-agents/{id}` creates at expected revision zero or updates at
an exact revision; an identical immediately retried write returns the same record.
Stale/different writes return 409. `GET` returns latest context. Each conversation
pins an immutable revision at creation. Updates affect **new** conversations only;
they never silently rewrite an existing conversation's instructions. Use the same
request ID and revision when retrying creation or delivering the same event.
Instructions reach the V3 provider instruction boundary, not synthetic chat messages.
Conversation/message/event routes verify both principal and server-stored binding.
There is no list/delete API in this initial bounded contract; applications retain IDs.

## Task-oriented applications remain independent

No conversation UX is required for the existing project task or worker APIs:

```ts
const queued = await client.projects.createTask(projectId, {
  title: 'Prepare release notes', description: 'Summarize approved changes.',
});
// Use the returned task ID with the existing approval/reopen lifecycle.
const result = await client.projects.getTask(projectId, taskId);
const worker = await client.workers.get(workerId);
```

Projects, tasks and workers retain their own server-side ownership, context,
revision, trigger and result contracts. This change does not attach arbitrary
project/worker IDs to an application agent or claim their context is synchronized.
Task execution may have internal durable sessions without requiring chat in your UX.
Worker deployment/automation management is an Orchestrator operation. These snippets
are API usage examples, not a claim of a live deployed or cloud-verified workflow.

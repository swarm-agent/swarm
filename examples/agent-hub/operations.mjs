const text = (value, max = 128) => {
  if (typeof value !== 'string' || !value.trim() || value.length > max) throw new Error('Invalid input');
  return value;
};
/** All state belongs to the SDK/backend, never this server or browser storage. */
export function operations(sdk) {
  return async function run(op, b = {}) {
    switch (op) {
      case 'agents': return sdk.apps.list({ limit: 50, ...(b.cursor ? { cursor: text(b.cursor) } : {}) });
      case 'agent': return sdk.apps.get(text(b.id));
      case 'save': {
        if (!Number.isSafeInteger(b.expected_revision) || b.expected_revision < 0) throw new Error('Revision required');
        const context = b.context ?? '', instructions = b.instructions ?? '';
        if (typeof context !== 'string' || typeof instructions !== 'string' || context.length + instructions.length > 32000) throw new Error('Context too large');
        if (!Array.isArray(b.worker_ids) || b.worker_ids.length > 100) throw new Error('Invalid worker links');
        return sdk.apps.put(text(b.id), { name: text(b.name, 256), instructions, context,
          expected_revision: b.expected_revision, project_id: b.project_id ? text(b.project_id) : '', worker_ids: b.worker_ids.map(id => text(id)) });
      }
      case 'conversations': return sdk.apps.conversations(text(b.id));
      case 'conversation': return sdk.apps.conversation(text(b.id), text(b.session_id));
      case 'open': return sdk.apps.createConversation(text(b.id), { revision: b.revision, workspace_id: text(b.workspace_id),
        title: text(b.title, 120), client_request_id: text(b.request_id), mode: 'auto' });
      case 'message': return sdk.apps.send(text(b.id), text(b.session_id), { content: text(b.content, 16000), client_request_id: text(b.request_id) });
      case 'tasks': return sdk.apps.tasks(text(b.id));
      case 'task': return sdk.apps.createTask(text(b.id), { id: text(b.request_id), title: text(b.title, 200), description: text(b.content, 16000) });
      case 'worker': return sdk.apps.worker(text(b.id), text(b.worker_id));
      case 'runs': return sdk.apps.runs(text(b.id), text(b.worker_id));
      default: throw new Error('Unknown operation');
    }
  };
}

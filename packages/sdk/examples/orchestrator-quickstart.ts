/** Create a conversation in an existing project; configuration and model assignments remain unchanged. */
import { SwarmClient } from '@swarm-agent/sdk';
import { interactiveConversation } from './interactive-permissions.js';

async function main() {
  const projectId = process.env.SWARM_PROJECT_ID;
  if (!projectId) throw new Error('Set SWARM_PROJECT_ID to the existing project you intend to use');
  const client = new SwarmClient({ baseUrl: process.env.SWARM_API_URL, socketPath: process.env.SWARM_SOCKET_PATH });
  const session = await client.projects.createConversation(projectId, {
    title: 'Interactive project review', clientRequestId: crypto.randomUUID(),
  });
  await interactiveConversation(client, session.id, 'Review this project and summarize next steps.');
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });

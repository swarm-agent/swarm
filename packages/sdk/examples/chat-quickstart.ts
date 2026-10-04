/** Run only against an already configured daemon. This example never changes credentials, models or permission policy. */
import { SwarmClient } from '@swarm-agent/sdk';
import { interactiveConversation } from './interactive-permissions.js';

async function main() {
  const workspacePath = process.env.SWARM_WORKSPACE_PATH;
  if (!workspacePath) throw new Error('Set SWARM_WORKSPACE_PATH to the workspace you intend to use');
  const client = new SwarmClient({
    baseUrl: process.env.SWARM_API_URL,
    socketPath: process.env.SWARM_SOCKET_PATH,
  });
  const session = await client.chat.createSession({ title: 'Interactive chat', workspace_path: workspacePath });
  await interactiveConversation(client, session.id, 'Inspect this workspace and summarize its structure.');
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });

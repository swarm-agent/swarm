/** Explicit terminal decisions for a preconfigured, authenticated SDK client. No policy/model changes. */
import { createInterface } from 'node:readline/promises';
import { stdin, stdout } from 'node:process';
import { answerPermission, describePermission, type SwarmClient } from '@swarm-agent/sdk';

export async function interactiveConversation(client: SwarmClient, sessionId: string, message: string): Promise<void> {
  const input = createInterface({ input: stdin, output: stdout });
  let prompts = Promise.resolve();
  const abort = new AbortController();
  const stop = () => { input.close(); abort.abort(); };
  process.once('SIGINT', stop);
  const watch = await client.chat.stream(sessionId, {
    signal: abort.signal,
    onText: delta => stdout.write(delta),
    onPermissionRequested: (permission, resolve) => {
      // Serialize concurrent prompts so one user's answer cannot go to another question.
      prompts = prompts.then(async () => {
        const view = describePermission(permission);
        console.log(`\n${view.toolName}\n${view.rawArguments}`);
        if (view.parseError) console.error(view.parseError);
        const decision = await input.question(view.kind === 'ask-user'
          ? 'Type answer to enter a response, deny to decline, or Enter to leave pending: '
          : 'Type allow to approve once, deny to decline, or Enter to leave pending: ');
        if (decision === 'deny') { await resolve('deny'); return; }
        if (view.kind === 'tool' && decision === 'allow') { await resolve('allow_once'); return; }
        if (view.kind !== 'ask-user' || decision !== 'answer' || view.parseError) return;
        const answers: Record<string, string> = Object.create(null);
        for (const question of view.questions) {
          console.log(question.question);
          for (const choice of question.options) console.log(`  ${choice.label}${choice.custom ? ' (type your text)' : ` [${choice.value}]`}`);
          const value = await input.question('Your response: ');
          if (value.trim() || question.required) answers[question.id] = value;
        }
        await resolve('allow_once', answerPermission(permission, view.structured ? answers : answers[view.questions[0].id]));
      }).catch(error => console.error('Request remains recoverable; review pending permissions before retrying:', error.message));
      return prompts;
    },
    onPermissionError: error => console.error('Permission response not confirmed:', error.message),
    onComplete: () => { console.log('\nTurn finished.'); stop(); },
    onError: error => console.error(error.message),
  });
  try {
    await watch.ready;
    await client.chat.sendMessage(sessionId, message);
    await watch.done;
  } finally {
    watch.dispose(); input.close(); process.off('SIGINT', stop);
  }
}

// Ephemeral OAuth correlation only; credentials and account settings stay in Swarm.
// There is no daemon OAuth cancellation API: forgetting a device flow cannot revoke it.
export function onboarding(sdk, session, now = Date.now) {
  const current = () => {
    const flow = session.login;
    if (!flow || flow.expires <= now()) { session.login = undefined; throw new Error('Sign-in expired; start again'); }
    return flow;
  };
  const view = (result, flow) => {
    const status = ['waiting', 'authorizing', 'success', 'error'].includes(result.status) ? result.status : 'error';
    const output = { status, method: flow.method, expires_at: flow.expires,
      defaults_applied: result.credential?.auto_defaults?.applied === true };
    if (status === 'waiting' || status === 'authorizing') {
      const link = result.verification_url || result.auth_url;
      if (link) {
        const url = new URL(link);
        if (url.origin !== 'https://auth.openai.com' || url.username || url.password ||
            !['/oauth/authorize', '/codex/device'].includes(url.pathname)) throw new Error('Untrusted provider URL');
        output.url = url.href;
      }
      if (typeof result.user_code === 'string' && /^[A-Za-z0-9-]{1,32}$/.test(result.user_code)) output.user_code = result.user_code;
    }
    // Do not return upstream errors, credential IDs, labels, tokens or callback input.
    return output;
  };
  return async (op, input) => {
    if (op === 'cancel') { session.login = undefined; session.loginAttempt = undefined; return { status: 'forgotten' }; }
    if (session.oauthBusy) throw new Error('Sign-in request in progress');
    session.oauthBusy = true;
    try {
      if (op === 'start') {
        if (input.consent !== true || !['device', 'manual'].includes(input.method)) throw new Error('Explicit sign-in consent required');
        if (session.login && session.login.expires > now()) throw new Error('Forget the previous flow first');
        const attempt = {}; session.loginAttempt = attempt;
        const result = await sdk.auth.codex.start({ method: input.method, active: false });
        if (session.loginAttempt !== attempt) throw new Error('Sign-in tracking cancelled');
        if (typeof result.session_id !== 'string' || !result.session_id) throw new Error('Missing sign-in session');
        const flow = { id: result.session_id, method: input.method,
          expires: Math.min(session.expires, now() + 20 * 60000, result.expires_at || Infinity) };
        session.login = flow;
        return view(result, current());
      }
      const flow = current();
      let result;
      if (op === 'status') result = await sdk.auth.codex.status(flow.id);
      else if (op === 'complete') {
        if (flow.method !== 'manual' || typeof input.callback !== 'string' || !input.callback.trim() || input.callback.length > 8192) throw new Error('Manual callback required');
        result = await sdk.auth.codex.complete(flow.id, input.callback);
      } else throw new Error('Unknown sign-in operation');
      if (session.login !== flow) throw new Error('Sign-in tracking cancelled');
      current();
      return view(result, flow);
    } finally { session.oauthBusy = false; }
  };
}

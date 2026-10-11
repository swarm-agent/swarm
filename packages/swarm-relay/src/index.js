import { OAuthProvider, AuthorizationError, CimdFetchError } from '@cloudflare/workers-oauth-provider';
import { SCOPES, SCOPE_READ } from './protocol.js';
import { consentPage, statusPage } from './pages.js';

export { Fleet } from './fleet.js';

// The relay serves one owner. Its OAuth "user" is that owner; whether a client
// may act for them is decided on a connected Swarm machine, never on the web.
const OWNER = 'owner';

function fleet(env) {
  return env.FLEET.get(env.FLEET.idFromName('fleet'));
}

function html(body, status = 200, headers = new Headers()) {
  headers.set('Content-Type', 'text/html; charset=utf-8');
  headers.set('Cache-Control', 'no-store');
  headers.set('X-Frame-Options', 'DENY');
  headers.set('Content-Security-Policy', "default-src 'none'; style-src 'unsafe-inline'; script-src 'unsafe-inline'; connect-src 'self'; form-action *; frame-ancestors 'none'");
  return new Response(body, { status, headers });
}

// Redirect hosts that may receive tokens. Defaults to Claude; widen with the
// ALLOWED_REDIRECT_HOSTS variable (comma separated, exact hostnames).
function redirectAllowed(env, host) {
  const allowed = String(env.ALLOWED_REDIRECT_HOSTS || 'claude.ai,claude.com')
    .split(',')
    .map((h) => h.trim().toLowerCase())
    .filter(Boolean);
  return allowed.includes(String(host).toLowerCase());
}

const mcpHandler = {
  async fetch(request, env, ctx) {
    if (request.method !== 'POST') {
      return new Response('MCP endpoint accepts POST only', { status: 405, headers: { Allow: 'POST' } });
    }
    if (!ctx.auth?.scope?.includes(SCOPE_READ)) {
      return new Response('insufficient scope', { status: 403 });
    }
    let message;
    try {
      message = await request.json();
    } catch {
      return Response.json({ jsonrpc: '2.0', id: null, error: { code: -32700, message: 'parse error' } });
    }
    if (Array.isArray(message)) {
      return Response.json({ jsonrpc: '2.0', id: null, error: { code: -32600, message: 'JSON-RPC batching is not supported' } });
    }
    const response = await fleet(env).mcp(message, { clientId: ctx.auth.clientId, scope: ctx.auth.scope });
    if (response === null) return new Response(null, { status: 202 });
    return Response.json(response, { headers: { 'Cache-Control': 'no-store' } });
  },
};

const defaultHandler = {
  async fetch(request, env) {
    const url = new URL(request.url);
    if (url.pathname === '/device/connect') {
      return fleet(env).fetch(request);
    }
    if (url.pathname === '/authorize/status' && request.method === 'GET') {
      const code = url.searchParams.get('code') || '';
      return Response.json(await fleet(env).consentStatus(code), { headers: { 'Cache-Control': 'no-store' } });
    }
    if (url.pathname === '/authorize') {
      try {
        return request.method === 'POST' ? await finishAuthorize(request, env) : await startAuthorize(request, env);
      } catch (error) {
        if (error instanceof AuthorizationError && error.redirectTo) return Response.redirect(error.redirectTo, 302);
        if (error instanceof AuthorizationError || error instanceof CimdFetchError) {
          return html(statusPage('Authorization failed', error instanceof AuthorizationError ? error.description : 'This app could not be verified.'), 400);
        }
        throw error;
      }
    }
    if (url.pathname === '/') {
      return html(statusPage('Swarm Control relay', 'This relay connects AI clients to your Swarm machines. Add its /mcp URL as a connector.'));
    }
    return new Response('not found', { status: 404 });
  },
};

async function startAuthorize(request, env) {
  const oauth = env.OAUTH_PROVIDER;
  const authRequest = await oauth.parseAuthRequest(request);
  const details = await oauth.describeConsent(authRequest);
  if (!redirectAllowed(env, details.redirectHost)) {
    return html(statusPage('Client not allowed', `This relay does not send access to ${details.redirectHost}.`), 403);
  }
  const consent = await oauth.beginConsent(authRequest);
  const scopes = (authRequest.scope || []).filter((s) => SCOPES.includes(s));
  const { code, online } = await fleet(env).createConsent({
    handle: consent.handle,
    clientId: details.clientId,
    clientName: details.clientName,
    clientDomain: details.clientDomain,
    redirectHost: details.redirectHost,
    scopes: scopes.length ? scopes : [SCOPE_READ],
  });
  return html(consentPage({ details, handle: consent.handle, code, online }), 200, consent.headers);
}

async function finishAuthorize(request, env) {
  const oauth = env.OAUTH_PROVIDER;
  const form = await request.clone().formData();
  const handle = String(form.get('handle') || '');
  const code = String(form.get('code') || '');
  const decision = await fleet(env).takeConsent(code, handle);
  if (decision.state === 'pending') {
    return html(statusPage('Still waiting', 'Approve this request in Swarm first.'), 409);
  }
  if (decision.state !== 'approved') {
    if (decision.state === 'denied') {
      const denied = await oauth.denyConsent(request, handle);
      return new Response(null, { status: 302, headers: denied.headers });
    }
    return html(statusPage('Request expired', 'Start connecting again from your AI client.'), 410);
  }
  const approved = await oauth.approveConsent(request, handle, { scope: decision.scopes });
  const { redirectTo } = await oauth.completeAuthorization({
    request: approved.request,
    userId: OWNER,
    metadata: { approvedBy: decision.decidedBy, approvedAt: Date.now() },
    scope: decision.scopes,
    props: { approvedBy: decision.decidedBy },
  });
  approved.headers.set('Location', redirectTo);
  return new Response(null, { status: 302, headers: approved.headers });
}

let provider;

export default {
  fetch(request, env, ctx) {
    const origin = String(env.RELAY_ORIGIN || '').replace(/\/+$/, '');
    if (!origin) return new Response('relay is not configured: set RELAY_ORIGIN', { status: 500 });
    provider ??= new OAuthProvider({
      apiRoute: '/mcp',
      apiHandler: mcpHandler,
      defaultHandler,
      authorizeEndpoint: '/authorize',
      tokenEndpoint: '/oauth/token',
      clientRegistrationEndpoint: '/oauth/register',
      clientIdMetadataDocumentEnabled: true,
      scopesSupported: SCOPES,
      // Clients request every scope up front; the owner decides what to grant
      // when approving in Swarm, and each machine's ceiling caps it again.
      requiredScopes: SCOPES,
      resourceMetadata: { resource: `${origin}/mcp`, authorization_servers: [origin] },
      // A grant lives while it is used (routines run unattended) and expires
      // after 30 idle days; access tokens stay short.
      accessTokenTTL: 3600,
      refreshTokenIdleTTL: 30 * 24 * 3600,
    });
    return provider.fetch(request, env, ctx);
  },
};

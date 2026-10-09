const escape = (value) => String(value ?? '').replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`);

const STYLE = `
:root { color-scheme: light dark; --bg: #f7f7f5; --fg: #1d1d1b; --muted: #6b6b66; --card: #fff; --line: #deded8; --accent: #2f5bd3; }
@media (prefers-color-scheme: dark) { :root { --bg: #161615; --fg: #ecece8; --muted: #a3a39c; --card: #20201e; --line: #34342f; --accent: #8aa7ff; } }
body { margin: 0; background: var(--bg); color: var(--fg); font: 16px/1.5 system-ui, sans-serif; }
main { max-width: 34rem; margin: 3rem auto; padding: 0 1rem; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 12px; padding: 1.5rem; }
h1 { font-size: 1.3rem; margin: 0 0 1rem; }
.muted { color: var(--muted); font-size: .92rem; }
.code { font: 600 1.8rem/1 ui-monospace, monospace; letter-spacing: .08em; text-align: center; padding: 1rem; border: 1px dashed var(--line); border-radius: 8px; margin: 1rem 0; }
pre { background: var(--bg); border-radius: 6px; padding: .6rem .8rem; overflow-x: auto; font-size: .85rem; }
.warn { color: #b3261e; }
`;

function shell(title, body) {
  return `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>${escape(title)}</title><style>${STYLE}</style></head><body><main><div class="card">${body}</div></main></body></html>`;
}

export function statusPage(title, message) {
  return shell(title, `<h1>${escape(title)}</h1><p>${escape(message)}</p>`);
}

// The page never offers an Allow button: approval happens on a Swarm machine
// the owner controls, which proves the person consenting owns this relay.
export function consentPage({ details, handle, code, online }) {
  const name = escape(details.clientName);
  const origin = details.clientDomain
    ? `Published by <strong>${escape(details.clientDomain)}</strong>.`
    : 'This app registered itself; its name is not verified.';
  const scopes = (details.scope || []).map((s) => `<li>${escape(s)}</li>`).join('');
  const body = `
<h1>${name} wants to control your Swarm</h1>
<p>${origin} Access will be sent to <strong>${escape(details.redirectHost)}</strong>.</p>
${details.redirectIsLoopback ? '<p class="warn">This sends access to an app on a computer. Continue only if you just started connecting from it.</p>' : ''}
<p>Requested access:</p><ul>${scopes || '<li>swarm:read</li>'}</ul>
<p>Approve it on one of your Swarm machines. Check that the code matches:</p>
<div class="code">${escape(code)}</div>
<pre>swarmctl remote approve ${escape(code)}</pre>
<p class="muted" id="status">${online ? `Waiting for approval (${online} machine${online === 1 ? '' : 's'} online)…` : 'No Swarm machine is online. Start Swarm, then approve.'}</p>
<form method="post" id="finish"><input type="hidden" name="handle" value="${escape(handle)}"><input type="hidden" name="code" value="${escape(code)}"></form>
<script>
(function () {
  var code = ${JSON.stringify(code)};
  var status = document.getElementById('status');
  function check() {
    fetch('/authorize/status?code=' + encodeURIComponent(code), { cache: 'no-store' })
      .then(function (r) { return r.json(); })
      .then(function (s) {
        if (s.state === 'approved' || s.state === 'denied') { status.textContent = s.state === 'approved' ? 'Approved. Finishing…' : 'Denied.'; document.getElementById('finish').submit(); }
        else if (s.state === 'expired') { status.textContent = 'This request expired. Start again from your AI client.'; }
        else { setTimeout(check, 2000); }
      })
      .catch(function () { setTimeout(check, 4000); });
  }
  setTimeout(check, 2000);
})();
</script>`;
  return shell(`Authorize ${details.clientName}`, body);
}

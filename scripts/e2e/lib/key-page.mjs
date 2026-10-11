// The person's side of swarm_request_provider_key: open the one-time key page
// (GET) and submit its form (POST), exactly as a browser on this machine does
// (swarmd/internal/api/provider_key_entry.go: loopback peer, Host naming the
// loopback listener, same-origin Origin/Referer, form-urlencoded api_key).
// The key is only ever in memory and in this one request body.

import http from 'node:http'

export const KEY_LINK_PATTERN = /^http:\/\/127\.0\.0\.1:(\d{1,5})\/keys\/([A-Za-z0-9_-]{43})$/

function send(url, { method = 'GET', headers = {}, body } = {}) {
  return new Promise((resolve, reject) => {
    const req = http.request(url, { method, headers, timeout: 120000 }, (res) => {
      const chunks = []
      res.on('data', (c) => chunks.push(c))
      res.on('end', () => resolve({ status: res.statusCode, text: Buffer.concat(chunks).toString('utf8'), headers: res.headers }))
    })
    req.on('timeout', () => req.destroy(new Error('key page request timed out')))
    req.on('error', reject)
    if (body) req.write(body)
    req.end()
  })
}

// pageMessage pulls the status paragraph out of the page (never the key: the
// page does not echo it, and the caller redacts anyway).
export function pageMessage(html) {
  const match = /<p class="(ok|error)" role="status">([^<]*)<\/p>/.exec(String(html))
  return match ? { kind: match[1], text: match[2].replace(/&#39;/g, "'").replace(/&quot;/g, '"').replace(/&amp;/g, '&') } : null
}

export async function openKeyPage(link) {
  const res = await send(link, { headers: { Accept: 'text/html', 'Sec-Fetch-Site': 'none', 'Sec-Fetch-Mode': 'navigate' } })
  const hasForm = /<form method="post"/.test(res.text) && /name="api_key"/.test(res.text)
  return { status: res.status, hasForm, csp: String(res.headers['content-security-policy'] || '') }
}

export async function submitKeyPage(link, apiKey) {
  const origin = new URL(link).origin
  const body = Buffer.from(new URLSearchParams({ api_key: apiKey }).toString())
  const res = await send(link, {
    method: 'POST',
    headers: {
      'Content-Type': 'application/x-www-form-urlencoded',
      'Content-Length': body.length,
      Origin: origin,
      Referer: link,
      'Sec-Fetch-Site': 'same-origin',
      'Sec-Fetch-Mode': 'navigate',
      Accept: 'text/html',
    },
    body,
  })
  body.fill(0)
  const message = pageMessage(res.text)
  return { status: res.status, saved: res.status === 200 && message?.kind === 'ok', message: message?.text ?? '' }
}

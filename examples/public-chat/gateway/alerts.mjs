// Which gateway events become phone alerts, and what they say. Shared by the
// gateway (direct delivery) and the staff console Worker (relay delivery, so
// the bot's host needs no outbound web access). No Node or Workers imports.

/**
 * The alert for one gateway event in swarm-watch's event format
 * ({kind, severity, title, body, notify}), or null for routine events.
 * consoleUrl adds a link that opens the staff console on that conversation.
 */
export function alertFor(type, e, consoleUrl = '') {
  const link = consoleUrl && e?.session_id ? `\nOpen: ${consoleUrl.replace(/\/$/, '')}/?session=${encodeURIComponent(e.session_id)}` : '';
  let alert = null;
  if (type === 'handoff.requested') {
    alert = e.staff_online === false
      ? { kind: 'chat.handoff', severity: 'info', notify: true, title: 'A visitor asked for a person (nobody online)', body: (e.summary ?? '') + link }
      : { kind: 'chat.handoff', severity: 'warning', notify: true, title: 'A visitor wants to talk now', body: `${e.summary ?? 'Visitor asked for a person'}\nAccept within 2 minutes.${link}` };
  } else if (type === 'handoff.missed') {
    alert = { kind: 'chat.handoff', severity: 'info', notify: true, title: 'Nobody accepted a chat in time', body: 'The visitor was asked to leave their email.' + link };
  } else if (type === 'lead.captured') {
    alert = { kind: 'chat.lead', severity: 'info', notify: true, title: 'A visitor left their email', body: [e.lead?.name, e.lead?.email, e.lead?.need].filter(Boolean).join('\n') + link };
  } else if (type === 'conversation.ended') {
    alert = { kind: 'chat.ended', severity: 'info', notify: true, title: `The AI ended a chat (${e.reason})`, body: (e.note ?? '') + link };
  }
  return alert && { ...alert, title: alert.title.slice(0, 200), body: alert.body.slice(0, 2000) };
}

/** Headers for an "Authorization: Bearer …" line kept in a secret. */
export function alertHeaders(authHeader) {
  const [name, ...rest] = String(authHeader).split(':');
  return { 'Content-Type': 'application/json', [name.trim()]: rest.join(':').trim() };
}

// Code-owned, ephemeral preview interaction. This script has no mutation or
// composer authority; the parent independently validates every selection event.
(() => {
  'use strict';
  const config = __SWARM_ARTIFACT_V3_SELECTION_CONFIG__;
  const protocol = 'swarm.artifact/v3';
  const parts = config.parts;
  let selected = [];
  const send = (type, extra = {}) => parent.postMessage({ protocol, type, revision_ref: config.revision_ref, ...extra }, '*');
  let runtime, duration = 0, time = 0, playing = false, failed = false;
  let command = 0, generation = 0, pending = null, running = false, frame = 0;
  let anchorTime = 0, anchorClock = 0;
  const state = (error = '') => send('playback-state', { command_id: command, duration_ms: duration, time_ms: time, playing, error });
  const stop = () => { playing = false; cancelAnimationFrame(frame); };
  const bounded = (operation) => new Promise((resolve, reject) => {
    const timer = setTimeout(() => reject(new Error('Animation operation timed out')), 5000);
    Promise.resolve().then(operation).then(resolve, reject).finally(() => clearTimeout(timer));
  });
  const sample = async (target) => {
    pending = { target, generation };
    if (running || failed) return;
    running = true;
    while (pending && !failed) {
      const request = pending; pending = null;
      try {
        const result = await bounded(() => runtime.seek(request.target));
        if (result?.time_ms !== request.target) throw new Error('Animation seek was not acknowledged');
        if (request.generation === generation) {
          time = request.target;
          if (time === duration) stop();
          state();
        }
      } catch {
        // A timed-out runtime may still be running: fail closed, never overlap it.
        failed = true; pending = null; stop(); state('Animation seek failed or timed out. Reload the preview to retry.');
      }
    }
    running = false;
    if (playing && !failed) frame = requestAnimationFrame(tick);
  };
  const tick = () => {
    if (!playing || document.hidden) { stop(); state(); return; }
    void sample(Math.min(duration, Math.round(anchorTime + performance.now() - anchorClock)));
  };
  const control = (action, target = time) => {
    if (!runtime || failed) return;
    generation++; stop();
    if (action === 'play') {
      if (target >= duration) target = 0;
      playing = true; anchorTime = target; anchorClock = performance.now();
    }
    void sample(target);
  };
  window.addEventListener('message', (event) => {
    const m = event.data;
    if (event.source !== parent || !m || m.protocol !== protocol || m.revision_ref !== config.revision_ref || m.type !== 'playback-command') return;
    if (!Number.isSafeInteger(m.command_id) || m.command_id <= command || !['play', 'pause', 'seek'].includes(m.action)) return;
    if (m.action === 'seek' && (!Number.isSafeInteger(m.time_ms) || m.time_ms < 0 || m.time_ms > duration)) return;
    command = m.command_id;
    control(m.action, m.action === 'seek' ? m.time_ms : time);
  });
  document.addEventListener('visibilitychange', () => { if (document.hidden && runtime) control('pause'); });
  window.addEventListener('pagehide', () => { generation++; failed = true; pending = null; stop(); });
  const initializePlayback = async () => {
    const candidate = globalThis.__SWARM_ANIMATION_V1__;
    if (!candidate) return; // Static documents retain their original layout/behavior.
    try {
      if (candidate.version !== 'swarm.animation/v1' || typeof candidate.ready !== 'function' || typeof candidate.seek !== 'function') throw new Error();
      const ready = await bounded(() => candidate.ready());
      if (!Number.isSafeInteger(ready?.duration_ms) || ready.duration_ms < 100 || ready.duration_ms > 36000000) throw new Error();
      runtime = candidate; duration = ready.duration_ms;
      control('pause', 0);
    } catch { send('playback-error'); }
  };
  window.addEventListener('load', initializePlayback, { once: true });
  const elementFor = (part) => {
    try { return document.querySelector(part.selector); } catch { return null; }
  };
  const paint = () => {
    const elements = new Map();
    for (const part of parts) {
      const element = elementFor(part);
      if (element) elements.set(element, elements.get(element) || selected.includes(part.id));
    }
    for (const [element, active] of elements) element.toggleAttribute('data-swarm-v3-selected', active);
  };
  window.addEventListener('message', (event) => {
    const message = event.data;
    if (event.source !== parent || !message || message.protocol !== protocol || message.type !== 'selection-state' || message.revision_ref !== config.revision_ref) return;
    if (!Array.isArray(message.part_ids) || message.part_ids.length > 256 || new Set(message.part_ids).size !== message.part_ids.length || message.part_ids.some((id) => typeof id !== 'string' || !config.part_ids.includes(id))) return;
    if (message.focus_part_id !== undefined && !config.part_ids.includes(message.focus_part_id)) return;
    selected = message.part_ids;
    paint();
    const part = parts.find((part) => part.id === message.focus_part_id);
    if (part && Number.isSafeInteger(part.time_ms)) {
      // Temporal navigation is a sequenced playback-command from Studio. Selection
      // synchronization only paints; repeating it must never pause/resample playback.
      return;
    }
    const element = part && elementFor(part);
    if (element) {
      element.scrollIntoView({ block: 'nearest', inline: 'nearest', behavior: 'instant' });
      if (!element.hasAttribute('tabindex')) element.setAttribute('tabindex', '-1');
      element.focus({ preventScroll: true });
    }
  });
  document.addEventListener('click', (event) => {
    // Selection must not swallow authored playback controls nested in Parts.
    if (event.composedPath().some((element) => element instanceof Element && element.matches('button, input, select, textarea, a, [role="button"], [contenteditable], [data-swarm-capture-ui]'))) return;
    // Walk inside-out so narration wins over a declared enclosing scene.
    for (const element of event.composedPath()) {
      if (!(element instanceof Element)) continue;
      const part = parts.find((candidate) => {
        if (Number.isSafeInteger(candidate.time_ms)) return false; // Shared Canvas cannot identify a chapter by geometry.
        try { return element.matches(candidate.selector); } catch { return false; }
      });
      if (!part) continue;
      event.preventDefault();
      event.stopImmediatePropagation();
      send('toggle-part', { part_id: part.id });
      return;
    }
  }, true);
  const ready = () => {
    const style = document.createElement('style');
    style.textContent = '[data-swarm-v3-selected] { outline: 3px solid #0284c7 !important; outline-offset: -3px !important; }';
    document.head.append(style);
    paint();
    send('selection-ready');
  };
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', ready, { once: true });
  else ready();
})();

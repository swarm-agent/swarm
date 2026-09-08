// Code-owned, ephemeral preview interaction. This script has no mutation or
// composer authority; the parent independently validates every selection event.
(() => {
  'use strict';
  const config = __SWARM_ARTIFACT_V3_SELECTION_CONFIG__;
  const protocol = 'swarm.artifact/v3';
  const parts = config.parts;
  let selected = [];
  const send = (type, extra = {}) => parent.postMessage({ protocol, type, revision_ref: config.revision_ref, ...extra }, '*');
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
      const runtime = globalThis.__SWARM_ANIMATION_V1__;
      if (runtime?.version === 'swarm.animation/v1' && typeof runtime.seek === 'function') {
        Promise.resolve().then(() => runtime.seek(part.time_ms)).then((result) => {
          if (result?.time_ms !== part.time_ms) throw new Error('Scene seek was not acknowledged');
        }).catch(() => send('scene-seek-error', { part_id: part.id }));
      } else send('scene-seek-error', { part_id: part.id });
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

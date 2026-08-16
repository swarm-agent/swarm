import assert from "node:assert/strict";
import test from "node:test";

import {
  DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY,
  desktopV3ActiveSessionSidebarView,
  effectiveDesktopSidebarDisplayMode,
  loadDesktopSidebarDisplayMode,
  normalizeDesktopSidebarDisplayMode,
} from "./desktop-sidebar-display";

test("sidebar display normalization accepts full compact and thin only", () => {
  assert.equal(normalizeDesktopSidebarDisplayMode("full"), "full");
  assert.equal(normalizeDesktopSidebarDisplayMode("compact"), "compact");
  assert.equal(normalizeDesktopSidebarDisplayMode("thin"), "thin");
  assert.equal(normalizeDesktopSidebarDisplayMode("collapsed"), "full");
});

test("responsive sidebar mode downgrades without changing the preference", () => {
  const preferred = "full" as const;
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 1440), "full");
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 900), "compact");
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 680), "thin");
  assert.equal(preferred, "full");
  assert.equal(effectiveDesktopSidebarDisplayMode("compact", 1440), "compact");
  assert.equal(effectiveDesktopSidebarDisplayMode("thin", 1440), "thin");
});

test("artifact generation does not lock the user out of the plan sidebar", () => {
  assert.equal(desktopV3ActiveSessionSidebarView({ selected: "plan", hasPlan: true, hasArtifacts: true }), "plan");
  assert.equal(desktopV3ActiveSessionSidebarView({ selected: "artifacts", hasPlan: true, hasArtifacts: true }), "artifacts");
  assert.equal(desktopV3ActiveSessionSidebarView({ selected: "plan", hasPlan: false, hasArtifacts: true }), "artifacts");
  assert.equal(desktopV3ActiveSessionSidebarView({ selected: "artifacts", hasPlan: true, hasArtifacts: false }), "plan");
});

test("sidebar display mode removes a legacy client-local preference and defaults to full", () => {
  const previousWindow = globalThis.window;
  const values = new Map<string, string>([[DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY, "thin"]]);
  globalThis.window = {
    localStorage: {
      removeItem: (key: string) => {
        values.delete(key);
      },
    },
  } as unknown as Window & typeof globalThis;
  try {
    assert.equal(loadDesktopSidebarDisplayMode(), "full");
    assert.equal(values.has(DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY), false);
  } finally {
    globalThis.window = previousWindow;
  }
});

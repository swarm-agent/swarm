import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

import {
  DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY,
  desktopV3ActiveSessionSidebarView,
  desktopV3HasArtifactSidebarContent,
  effectiveDesktopSidebarDisplayMode,
  loadDesktopSidebarDisplayMode,
  normalizeDesktopSidebarDisplayMode,
} from "./desktop-sidebar-display";

// Requirement: first-session catalog discovery must not open an empty sidebar.
// Regression: useNativeArtifactCatalog reports loading during activation and each
// refresh; DesktopV3ExistingConversationPane previously treated it as content.
// This pure visibility-policy layer checks the full empty/loaded/error lifecycle
// without live APIs. Browser paint continuity still needs separate verification.
test("artifact sidebar ignores empty catalog loading and preserves content or errors", () => {
  const snapshots = [
    { artifactCount: 0, error: "", loading: false }, // local draft
    { artifactCount: 0, error: "", loading: true }, // first durable activation
    { artifactCount: 0, error: "", loading: false }, // empty response
    { artifactCount: 0, error: "", loading: true }, // event-triggered refresh
    { artifactCount: 0, error: "", loading: false },
    { artifactCount: 1, error: "", loading: false }, // real artifact arrival
    { artifactCount: 1, error: "", loading: true }, // retained while refreshing
    { artifactCount: 0, error: "Catalog unavailable", loading: false },
    { artifactCount: 0, error: "Catalog unavailable", loading: true }, // retry
    { artifactCount: 0, error: "", loading: false }, // recovered empty catalog
  ];
  assert.deepEqual(snapshots.map(desktopV3HasArtifactSidebarContent),
    [false, false, false, false, false, true, true, true, true, false]);
});

// Wiring guard only: the behavioral policy above must own pane visibility;
// loading stays available to the rendered artifact sidebar, not its opening gate.
test("conversation pane uses content policy rather than catalog loading to open sidebar", async () => {
  const pane = await readFile(new URL("./desktop-v3-existing-conversation-pane.tsx", import.meta.url), "utf8");
  assert.match(pane, /const hasSessionArtifacts = desktopV3HasArtifactSidebarContent\(\{\s*artifactCount: sessionArtifactV3\.length \+ sessionArtifactV2\.length \+ sessionArtifacts\.length,\s*error: sessionArtifactV3Error,\s*\}\);/);
  assert.match(pane, /const showConversationSidebar = showPlanSidebar \|\| hasSessionArtifacts;/);
});

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
  assert.equal(effectiveDesktopSidebarDisplayMode(preferred, 660), "thin");
  assert.equal(preferred, "full");
  assert.equal(effectiveDesktopSidebarDisplayMode("compact", 1440), "compact");
  assert.equal(effectiveDesktopSidebarDisplayMode("thin", 1440), "thin");
});

test("responsive sidebar mode uses hysteresis at layout thresholds", () => {
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 950, "full"), "full");
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 950, "compact"), "compact");
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 1019, "compact"), "compact");
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 1020, "compact"), "full");
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 700, "compact"), "compact");
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 700, "thin"), "thin");
  assert.equal(effectiveDesktopSidebarDisplayMode("full", 760, "thin"), "compact");
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

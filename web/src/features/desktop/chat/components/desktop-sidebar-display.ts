export type DesktopSidebarDisplayMode = "full" | "compact" | "thin";
export type DesktopV3SessionSidebarView = "plan" | "artifacts";

const DESKTOP_SIDEBAR_COMPACT_ENTER_WIDTH = 940;
const DESKTOP_SIDEBAR_COMPACT_EXIT_WIDTH = 1020;
const DESKTOP_SIDEBAR_THIN_ENTER_WIDTH = 680;
const DESKTOP_SIDEBAR_THIN_EXIT_WIDTH = 760;

export function desktopV3ActiveSessionSidebarView(input: {
  selected: DesktopV3SessionSidebarView;
  hasPlan: boolean;
  hasArtifacts: boolean;
}): DesktopV3SessionSidebarView {
  return input.hasArtifacts && (!input.hasPlan || input.selected === "artifacts") ? "artifacts" : "plan";
}

export const DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY = "swarm.web.desktop.sidebar.display-mode";

export function normalizeDesktopSidebarDisplayMode(value: unknown): DesktopSidebarDisplayMode {
  return value === "compact" || value === "thin" ? value : "full";
}

export function effectiveDesktopSidebarDisplayMode(
  preferred: DesktopSidebarDisplayMode,
  availableWidth: number,
  current: DesktopSidebarDisplayMode = preferred,
): DesktopSidebarDisplayMode {
  if (availableWidth <= 0) return preferred;
  if (preferred === "thin") return "thin";
  if (current === "thin" && availableWidth < DESKTOP_SIDEBAR_THIN_EXIT_WIDTH) return "thin";
  if (availableWidth < DESKTOP_SIDEBAR_THIN_ENTER_WIDTH) return "thin";
  if (preferred === "compact") return "compact";
  if (current === "compact" && availableWidth < DESKTOP_SIDEBAR_COMPACT_EXIT_WIDTH) return "compact";
  if (availableWidth < DESKTOP_SIDEBAR_COMPACT_ENTER_WIDTH) return "compact";
  return "full";
}

export function loadDesktopSidebarDisplayMode(): DesktopSidebarDisplayMode {
  if (typeof window === "undefined") return "full";
  try {
    window.localStorage.removeItem(DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY);
  } catch {
    // Legacy client-local layout cleanup is best effort.
  }
  return "full";
}

export type DesktopSidebarDisplayMode = "full" | "compact" | "thin";
export type DesktopV3SessionSidebarView = "plan" | "artifacts";

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
): DesktopSidebarDisplayMode {
  if (availableWidth > 0 && availableWidth < 720) return "thin";
  if (availableWidth > 0 && availableWidth < 980 && preferred === "full") return "compact";
  return preferred;
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

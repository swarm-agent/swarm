export type DesktopSidebarDisplayMode = "full" | "compact" | "thin";

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
    return normalizeDesktopSidebarDisplayMode(
      window.localStorage.getItem(DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY),
    );
  } catch {
    return "full";
  }
}

export function saveDesktopSidebarDisplayMode(mode: DesktopSidebarDisplayMode): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(DESKTOP_SIDEBAR_DISPLAY_STORAGE_KEY, mode);
  } catch {
    // Client-local layout persistence is best effort.
  }
}

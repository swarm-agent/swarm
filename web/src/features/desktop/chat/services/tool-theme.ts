import {
  FileText,
  FilePenLine,
  Pencil,
  Terminal,
  Search,
  FolderTree,
  FolderSearch,
  Globe,
  Download,
  GitBranch,
  MessageSquare,
  LogOut,
  Sparkles,
  Wrench,
  type LucideIcon,
} from 'lucide-react'

export interface ToolThemeEntry {
  icon: LucideIcon
  label: string
  color: string // CSS variable reference for the tool accent
}

const TOOL_THEME_MAP: Record<string, ToolThemeEntry> = {
  read: { icon: FileText, label: 'read', color: 'var(--app-info)' },
  write: { icon: FilePenLine, label: 'write', color: 'var(--app-warning)' },
  edit: { icon: Pencil, label: 'edit', color: 'var(--app-warning)' },
  bash: { icon: Terminal, label: 'bash', color: 'var(--app-accent)' },
  grep: { icon: Search, label: 'grep', color: 'var(--app-info)' },
  list: { icon: FolderTree, label: 'list', color: 'var(--app-info)' },
  glob: { icon: FolderSearch, label: 'glob', color: 'var(--app-info)' },
  search: { icon: Search, label: 'search', color: 'var(--app-info)' },
  websearch: { icon: Globe, label: 'websearch', color: 'var(--app-accent)' },
  webfetch: { icon: Download, label: 'webfetch', color: 'var(--app-accent)' },
  webdownload: { icon: Download, label: 'webdownload', color: 'var(--app-accent)' },
  task: { icon: GitBranch, label: 'task', color: 'var(--app-primary)' },
  'ask-user': { icon: MessageSquare, label: 'ask-user', color: 'var(--app-primary)' },
  ask_user: { icon: MessageSquare, label: 'ask-user', color: 'var(--app-primary)' },
  'exit-plan-mode': { icon: LogOut, label: 'exit-plan-mode', color: 'var(--app-primary)' },
  exit_plan_mode: { icon: LogOut, label: 'exit-plan-mode', color: 'var(--app-primary)' },
  'skill-use': { icon: Sparkles, label: 'skill', color: 'var(--app-accent)' },
  skill_use: { icon: Sparkles, label: 'skill', color: 'var(--app-accent)' },
}

const DEFAULT_ENTRY: ToolThemeEntry = { icon: Wrench, label: 'tool', color: 'var(--app-text-muted)' }

export function getToolTheme(toolName: string): ToolThemeEntry {
  const key = toolName.trim().toLowerCase().replace(/_/g, '-')
  return TOOL_THEME_MAP[key] ?? TOOL_THEME_MAP[toolName.trim().toLowerCase()] ?? DEFAULT_ENTRY
}

export type ToolState = 'done' | 'running' | 'error'

export function toolStateColor(state: ToolState): string {
  switch (state) {
    case 'done':
      return 'var(--app-accent)'
    case 'error':
      return 'var(--app-danger)'
    case 'running':
      return 'var(--app-primary)'
  }
}

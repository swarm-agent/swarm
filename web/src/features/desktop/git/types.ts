export interface GitFileStatus {
  kind: string
  xy?: string
  path: string
  orig_path?: string
  staged?: boolean
  modified?: boolean
  untracked?: boolean
  conflict?: boolean
  submodule?: string
}

export interface GitRemote {
  name: string
  url: string
  kind: string
}

export interface GitCommit {
  hash: string
  short_hash: string
  author?: string
  unix_time?: number
  subject: string
}

export interface GitSnapshot {
  workspace_path: string
  repo_root?: string
  git_dir?: string
  has_git: boolean
  clean: boolean
  branch?: string
  head_oid?: string
  upstream?: string
  ahead_count: number
  behind_count: number
  stash_count: number
  dirty_count: number
  staged_count: number
  modified_count: number
  untracked_count: number
  conflict_count: number
  files: GitFileStatus[]
  remotes?: GitRemote[]
  recent_commits?: GitCommit[]
  refreshed_at: string
  duration_ms: number
}

export interface GitStatusResponse {
  ok: boolean
  status: GitSnapshot
}

export interface GitRealtimeResponse {
  ok: boolean
  workspace_path: string
}

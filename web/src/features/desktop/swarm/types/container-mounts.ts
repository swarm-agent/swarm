export type ContainerMountMode = 'rw' | 'ro'

export interface ContainerProfileMount {
  sourcePath: string
  targetPath: string
  mode: ContainerMountMode
  workspacePath: string
  workspaceName: string
}

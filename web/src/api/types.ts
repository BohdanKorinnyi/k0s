// The shapes the API sends. They mirror internal/api/views.go; a field added
// there is added here.

export type SessionInfo = {
  name: string
  version: string
  authRequired: boolean
  authenticated: boolean
}

export type HostView = {
  id: string
  name: string
  url: string
  protected: boolean
  hasTLS: boolean
}

export type RegistryView = {
  id: string
  address: string
  username: string
  hasPassword: boolean
}

export type ConnectionResult = {
  ok: boolean
  version: string
  error: string
}

export type ContainerView = {
  id: string
  name: string
  image: string
  imageId: string
  /** Docker's own: created, running, paused, restarting, removing, exited, dead. */
  state: string
  /** "", "healthy", "unhealthy" or "starting". */
  health: string
  exitCode: number
  startedAt: string | null
  finishedAt: string | null
  restarts: number
}

export type ServiceState = 'restarting' | 'unhealthy' | 'running' | 'partial' | 'stopped'

export type UpdateStatus = 'up_to_date' | 'update_available' | 'unknown' | 'pinned' | 'unchecked'

export type ServiceView = {
  key: string
  name: string
  project: string
  /** The image all containers share, or "" when they differ. */
  image: string
  images: string[]
  running: number
  total: number
  state: ServiceState
  exitCode: number
  startedAt: string | null
  restarts: number
  update: { status: UpdateStatus; reason: string }
  /** IDs of the actions that apply to the service as it is now. */
  actions: string[]
  containers: ContainerView[]
}

export type ServiceGroup = {
  /** "" for containers that do not belong to a Compose project. */
  project: string
  services: ServiceView[]
}

export type ServicesResponse = {
  groups: ServiceGroup[]
  updatesCheckedAt: string | null
}

export type ActionInfo = {
  id: string
  label: string
  destructive: boolean
  containerLevel: boolean
}

export type ActionEvent = {
  step: string
  level: 'info' | 'success' | 'warning' | 'error'
  done: boolean
}

/** One log line; the keys are short because there are many of them. */
export type LogLine = {
  /** container name */
  c: string
  /** stream */
  s: 'stdout' | 'stderr'
  /** time, RFC 3339 */
  t: string
  /** text */
  l: string
  /** set when the line was too long for one piece and continues in the next */
  p?: boolean
}

export type ContainerInfo = {
  id: string
  name: string
  image: string
  imageId: string
  digests: string[]
  created: string
  startedAt: string
  finishedAt: string
  state: string
  health: string
  exitCode: number
  restartPolicy: string
  restarts: number
  command: string[]
  ports: { container: string; host: string }[]
  mounts: { type: string; source: string; destination: string; readOnly: boolean }[]
  networks: { name: string; ip: string; aliases: string[] }[]
  labels: Record<string, string>
  compose: { project: string; service: string; workingDir: string }
}

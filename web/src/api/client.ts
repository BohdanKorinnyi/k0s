import type { ActionEvent } from './types'

/** An error answer from the API, or a failure to reach it. */
export class ApiError extends Error {
  /** HTTP status; 0 when the server could not be reached. */
  readonly status: number
  /** A stable word to branch on: "duplicate", "busy", "host_unreachable"… */
  readonly code: string

  constructor(status: number, code: string, message: string) {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

let onUnauthenticated: () => void = () => {}

/** Registers what to do when the API says the session is gone. */
export function setUnauthenticatedHandler(handler: () => void): void {
  onUnauthenticated = handler
}

async function send(method: string, path: string, body?: unknown): Promise<Response> {
  const init: RequestInit = { method, credentials: 'same-origin' }
  if (body !== undefined) {
    init.headers = { 'Content-Type': 'application/json' }
    init.body = JSON.stringify(body)
  }
  let res: Response
  try {
    res = await fetch(path, init)
  } catch {
    throw new ApiError(0, 'network', 'Cannot reach the panel. Is it still running?')
  }
  if (!res.ok) throw await toError(res)
  return res
}

async function toError(res: Response): Promise<ApiError> {
  const text = await res.text().catch(() => '')
  let code = `http_${res.status}`
  let message = text.trim() || res.statusText || `HTTP ${res.status}`
  try {
    const parsed = JSON.parse(text) as { error?: unknown; code?: unknown }
    if (typeof parsed.error === 'string') message = parsed.error
    if (typeof parsed.code === 'string') code = parsed.code
  } catch {
    // Not JSON: the text itself is the message (a refused hostname, a proxy's error page).
  }
  if (res.status === 401) onUnauthenticated()
  return new ApiError(res.status, code, message)
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const res = await send(method, path, body)
  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}

export const api = {
  get: <T>(path: string) => request<T>('GET', path),
  post: <T>(path: string, body?: unknown) => request<T>('POST', path, body),
  put: <T>(path: string, body: unknown) => request<T>('PUT', path, body),
  del: (path: string) => request<void>('DELETE', path),
}

/**
 * Reads a stream of newline-delimited JSON, calling onLine for each object.
 * The bytes may be split anywhere, including inside a character.
 */
export async function readNDJSON(stream: ReadableStream<Uint8Array>, onLine: (value: unknown) => void): Promise<void> {
  const reader = stream.getReader()
  const decoder = new TextDecoder()
  let pending = ''
  const emit = (line: string) => {
    if (line.trim() !== '') onLine(JSON.parse(line))
  }
  for (;;) {
    const { done, value } = await reader.read()
    if (done) break
    pending += decoder.decode(value, { stream: true })
    let newline: number
    while ((newline = pending.indexOf('\n')) >= 0) {
      emit(pending.slice(0, newline))
      pending = pending.slice(newline + 1)
    }
  }
  emit(pending + decoder.decode())
}

/**
 * Runs an action and reports its progress. Resolves with the closing event,
 * which says whether the action succeeded. Rejects when the action was refused
 * before it started, or when the connection was lost while it ran.
 */
export async function streamAction(path: string, body: object, onEvent: (e: ActionEvent) => void): Promise<ActionEvent> {
  const res = await send('POST', path, body)
  if (!res.body) throw new ApiError(0, 'network', 'The panel sent no progress for this action.')

  let final: ActionEvent | null = null
  try {
    await readNDJSON(res.body, (value) => {
      const event = value as ActionEvent
      onEvent(event)
      if (event.done) final = event
    })
  } catch {
    // Fall through: without a closing event the outcome is unknown.
  }
  if (!final) {
    // The server keeps going without us; say so rather than claim a failure.
    throw new Error('The connection was lost before the action finished. It may still be running; refresh to see the result.')
  }
  return final
}

/** The WebSocket URL for an API path on the server this page came from. */
export function wsURL(path: string, location: Pick<Location, 'protocol' | 'host'> = window.location): string {
  const scheme = location.protocol === 'https:' ? 'wss:' : 'ws:'
  return `${scheme}//${location.host}${path}`
}

/** Path helpers, so that names are escaped in one place. */
export const paths = {
  host: (hostId: string) => `/api/hosts/${encodeURIComponent(hostId)}`,
  services: (hostId: string) => `${paths.host(hostId)}/services`,
  service: (hostId: string, key: string) => `${paths.services(hostId)}/${encodeURIComponent(key)}`,
  action: (hostId: string, key: string, actionId: string) =>
    `${paths.service(hostId, key)}/actions/${encodeURIComponent(actionId)}`,
  container: (hostId: string, container: string) => `${paths.host(hostId)}/containers/${encodeURIComponent(container)}`,
}

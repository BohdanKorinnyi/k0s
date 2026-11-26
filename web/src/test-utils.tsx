// Helpers shared by the tests: fixtures in the API's shapes, and a stand-in
// for the server that answers fetch by method and path.

import { vi, type Mock } from 'vitest'
import type { ActionInfo, ContainerView, HostView, ServiceView } from './api/types'

export function host(overrides: Partial<HostView> = {}): HostView {
  return { id: 'aws', name: 'aws', url: 'ssh://deploy@aws.example.com', protected: false, hasTLS: false, ...overrides }
}

export function container(overrides: Partial<ContainerView> = {}): ContainerView {
  return {
    id: 'c1'.padEnd(64, '0'),
    name: 'shop-api-1',
    image: 'ghcr.io/acme/api:develop',
    imageId: 'sha256:img1',
    state: 'running',
    health: '',
    exitCode: 0,
    startedAt: '2026-11-23T12:00:00Z',
    finishedAt: null,
    restarts: 0,
    ...overrides,
  }
}

export function service(overrides: Partial<ServiceView> = {}): ServiceView {
  return {
    key: 'c~shop~api',
    name: 'api',
    project: 'shop',
    image: 'ghcr.io/acme/api:develop',
    images: ['ghcr.io/acme/api:develop'],
    running: 2,
    total: 2,
    state: 'running',
    exitCode: 0,
    startedAt: '2026-11-23T12:00:00Z',
    restarts: 0,
    update: { status: 'up_to_date', reason: '' },
    actions: ['restart', 'stop', 'pull-restart'],
    containers: [
      container({ id: 'c1'.padEnd(64, '0'), name: 'shop-api-1' }),
      container({ id: 'c2'.padEnd(64, '0'), name: 'shop-api-2' }),
    ],
    ...overrides,
  }
}

export const actionInfos: ActionInfo[] = [
  { id: 'restart', label: 'Restart', destructive: false, containerLevel: true },
  { id: 'start', label: 'Start', destructive: false, containerLevel: true },
  { id: 'stop', label: 'Stop', destructive: true, containerLevel: true },
  { id: 'pull-restart', label: 'Pull & restart', destructive: true, containerLevel: false },
]

export function json(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

/** A response whose body is a stream of newline-delimited JSON events. */
export function ndjson(...events: object[]): Response {
  return new Response(events.map((e) => JSON.stringify(e) + '\n').join(''), {
    status: 200,
    headers: { 'Content-Type': 'application/x-ndjson' },
  })
}

type Reply = Response | object | ((request: { path: string; body: unknown }) => Response | object | Promise<Response | object>)

export type Recorded = { method: string; path: string; body: unknown }

/**
 * Replaces fetch with a table of answers keyed by "METHOD /path". A plain
 * object is sent as JSON with status 200. Anything not in the table gets 404.
 * Returns the calls made, for assertions.
 */
export function mockFetch(routes: Record<string, Reply>): { calls: Recorded[]; mock: Mock } {
  const calls: Recorded[] = []
  const mock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const url = new URL(String(input), 'http://localhost')
    const method = (init?.method ?? 'GET').toUpperCase()
    const path = url.pathname + url.search
    const body = typeof init?.body === 'string' ? (JSON.parse(init.body) as unknown) : undefined
    calls.push({ method, path, body })

    const reply = routes[`${method} ${path}`] ?? routes[`${method} ${url.pathname}`]
    if (reply === undefined) return json(404, { error: `no such API route: ${method} ${path}`, code: 'not_found' })
    const value = typeof reply === 'function' ? await reply({ path, body }) : reply
    return value instanceof Response ? value.clone() : json(200, value)
  })
  vi.stubGlobal('fetch', mock)
  return { calls, mock }
}

/**
 * A stand-in for the browser's WebSocket. Tests reach the sockets the code
 * opened through `FakeWebSocket.instances` and play the server's part.
 */
export class FakeWebSocket {
  static instances: FakeWebSocket[] = []
  static readonly CONNECTING = 0
  static readonly OPEN = 1
  static readonly CLOSING = 2
  static readonly CLOSED = 3

  /** Replaces the global WebSocket and forgets earlier sockets. */
  static install(): void {
    FakeWebSocket.instances = []
    vi.stubGlobal('WebSocket', FakeWebSocket)
  }

  /** The most recently opened socket. */
  static last(): FakeWebSocket {
    const ws = FakeWebSocket.instances.at(-1)
    if (!ws) throw new Error('no WebSocket was opened')
    return ws
  }

  readonly url: string
  binaryType = 'blob'
  readyState = FakeWebSocket.CONNECTING
  /** Everything the code under test sent. */
  sent: unknown[] = []
  closedByClient = false
  onopen: ((event: Event) => void) | null = null
  onmessage: ((event: MessageEvent) => void) | null = null
  onclose: ((event: CloseEvent) => void) | null = null
  onerror: ((event: Event) => void) | null = null

  constructor(url: string | URL) {
    this.url = String(url)
    FakeWebSocket.instances.push(this)
  }

  send(data: unknown): void {
    this.sent.push(data)
  }

  close(): void {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.closedByClient = true
    this.readyState = FakeWebSocket.CLOSED
  }

  // ---- the server's side ----

  accept(): void {
    this.readyState = FakeWebSocket.OPEN
    this.onopen?.(new Event('open'))
  }

  /** Delivers a frame: a string for text, an ArrayBuffer for binary. */
  deliver(data: string | ArrayBuffer): void {
    this.onmessage?.({ data } as MessageEvent)
  }

  /** Text frames sent by the code under test, parsed as JSON. */
  sentControl(): unknown[] {
    return this.sent.filter((d): d is string => typeof d === 'string').map((d) => JSON.parse(d) as unknown)
  }

  hangUp(code = 1000, reason = ''): void {
    if (this.readyState === FakeWebSocket.CLOSED) return
    this.readyState = FakeWebSocket.CLOSED
    this.onclose?.({ code, reason, wasClean: code === 1000 } as CloseEvent)
  }
}

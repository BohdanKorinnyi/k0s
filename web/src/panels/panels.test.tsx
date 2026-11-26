import { act, render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import type { ContainerInfo, LogLine } from '../api/types'
import { container, FakeWebSocket, host, mockFetch, service } from '../test-utils'
import { EnvPanel } from './EnvPanel'
import { panels } from './index'
import { InfoPanel } from './InfoPanel'
import { LogsPanel } from './LogsPanel'
import { ShellPanel } from './ShellPanel'

// The terminal draws on a canvas, which the test environment does not have.
// What matters here is what the panel does around it.
const terminal = vi.hoisted(() => ({ written: [] as Uint8Array[], onData: null as null | ((data: string) => void), disposed: 0 }))
vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 100
    rows = 30
    open() {}
    focus() {}
    loadAddon() {}
    write(data: Uint8Array) {
      terminal.written.push(data)
    }
    onData(handler: (data: string) => void) {
      terminal.onData = handler
      return { dispose() {} }
    }
    onResize() {
      return { dispose() {} }
    }
    dispose() {
      terminal.disposed++
    }
  },
}))
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

const svc = service()
const first = svc.containers[0]!
const envPath = `/api/hosts/aws/containers/${first.id}/env`

beforeEach(() => {
  FakeWebSocket.install()
  window.localStorage.clear()
  terminal.written = []
  terminal.onData = null
  terminal.disposed = 0
})

test('the tabs are logs, env, shell and info, and only logs can show all containers', () => {
  expect(panels.map((p) => p.id)).toEqual(['logs', 'env', 'shell', 'info'])
  expect(panels.map((p) => p.title)).toEqual(['Logs', 'Env', 'Shell', 'Info'])
  expect(panels.filter((p) => p.supportsAll).map((p) => p.id)).toEqual(['logs'])
})

describe('EnvPanel', () => {
  const values: Record<string, string> = { A: '1', DATABASE_URL: 'postgres://u:hunter2@db/app', EMPTY: '' }

  function setup() {
    const fetched = mockFetch({
      [`GET ${envPath}`]: { names: Object.keys(values) },
      [`POST ${envPath}/reveal`]: ({ body }) => {
        const names = (body as { names?: string[] } | undefined)?.names
        return { values: Object.fromEntries(Object.entries(values).filter(([k]) => !names || names.includes(k))) }
      },
    })
    render(<EnvPanel host={host()} service={svc} container={first} />)
    return fetched
  }

  const reveals = (fetched: ReturnType<typeof mockFetch>) => fetched.calls.filter((c) => c.method === 'POST').map((c) => c.body)

  test('lists the names with every value masked, and no value in the page', async () => {
    setup()
    expect(await screen.findByRole('row', { name: /^A\b/ })).toBeInTheDocument()
    expect(screen.getAllByText('••••••••')).toHaveLength(3)
    expect(document.body).not.toHaveTextContent('hunter2')
    expect(document.body).not.toHaveTextContent('postgres://')
  })

  test('revealing one variable asks for that one and shows it', async () => {
    const user = userEvent.setup()
    const fetched = setup()
    const row = await screen.findByRole('row', { name: /^A\b/ })

    await user.click(within(row).getByRole('button', { name: 'Reveal A' }))
    expect(await within(row).findByText('1')).toBeInTheDocument()
    expect(reveals(fetched)).toEqual([{ names: ['A'] }])
    // The others stay masked.
    expect(screen.getAllByText('••••••••')).toHaveLength(2)
    expect(document.body).not.toHaveTextContent('hunter2')

    await user.click(within(row).getByRole('button', { name: 'Hide A' }))
    expect(screen.getAllByText('••••••••')).toHaveLength(3)
  })

  test('"Reveal all" asks without names and shows everything, including an empty value', async () => {
    const user = userEvent.setup()
    const fetched = setup()
    await screen.findByRole('row', { name: /^A\b/ })

    await user.click(screen.getByRole('button', { name: 'Reveal all' }))
    expect(await screen.findByText('postgres://u:hunter2@db/app')).toBeInTheDocument()
    expect(reveals(fetched)).toEqual([{}])
    expect(screen.queryByText('••••••••')).toBeNull()
    expect(within(screen.getByRole('row', { name: /^EMPTY\b/ })).getByText('(empty)')).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Hide all' }))
    expect(screen.getAllByText('••••••••')).toHaveLength(3)
    expect(document.body).not.toHaveTextContent('hunter2')
  })

  test('the filter narrows by name', async () => {
    const user = userEvent.setup()
    setup()
    await screen.findByRole('row', { name: /^A\b/ })
    await user.type(screen.getByRole('searchbox', { name: 'Filter variables' }), 'database')
    expect(screen.queryByRole('row', { name: /^A\b/ })).toBeNull()
    expect(screen.getByRole('row', { name: /^DATABASE_URL\b/ })).toBeInTheDocument()
  })

  test('another container means a fresh list with nothing revealed', async () => {
    const user = userEvent.setup()
    const second = svc.containers[1]!
    mockFetch({
      [`GET ${envPath}`]: { names: ['A'] },
      [`POST ${envPath}/reveal`]: { values: { A: '1' } },
      [`GET /api/hosts/aws/containers/${second.id}/env`]: { names: ['A', 'B'] },
    })
    const { rerender } = render(<EnvPanel host={host()} service={svc} container={first} />)
    await user.click(await screen.findByRole('button', { name: 'Reveal A' }))
    await screen.findByText('1')

    rerender(<EnvPanel host={host()} service={svc} container={second} />)
    expect(await screen.findByRole('row', { name: /^B\b/ })).toBeInTheDocument()
    expect(screen.getAllByText('••••••••')).toHaveLength(2)
  })
})

describe('LogsPanel', () => {
  const line = (l: string, more: Partial<LogLine> = {}): LogLine => ({ c: 'shop-api-1', s: 'stdout', t: '2026-11-26T10:00:00.123456789Z', l, ...more })

  async function setup(container_: typeof first | null = first) {
    const user = userEvent.setup()
    render(<LogsPanel host={host()} service={svc} container={container_} />)
    const ws = FakeWebSocket.last()
    act(() => ws.accept())
    const deliver = (...lines: LogLine[]) => act(() => ws.deliver(JSON.stringify(lines)))
    return { user, ws, deliver }
  }

  test('connects to the container and shows lines as they arrive', async () => {
    const { ws, deliver } = await setup()
    expect(ws.url).toBe(`ws://localhost:3000/api/hosts/aws/containers/${first.id}/logs/ws?tail=500`)
    deliver(line('GET /health 200'), line('payment timeout', { s: 'stderr' }))
    expect(screen.getByText('GET /health 200')).toBeInTheDocument()
    expect(screen.getByText('payment timeout').closest('.log-line')).toHaveClass('stderr')
  })

  test('"All together" connects to the service and names each line’s container', async () => {
    const { ws, deliver } = await setup(null)
    expect(ws.url).toBe('ws://localhost:3000/api/hosts/aws/services/c~shop~api/logs/ws?tail=500')
    deliver(line('from one'), line('from two', { c: 'shop-api-2' }))
    expect(screen.getByText('shop-api-2')).toBeInTheDocument()
  })

  test('log text is shown as text, never run as markup', async () => {
    const { deliver } = await setup()
    deliver(line('<script>alert(1)</script><img src=x onerror=alert(2)>'))
    expect(screen.getByText('<script>alert(1)</script><img src=x onerror=alert(2)>')).toBeInTheDocument()
    expect(document.querySelector('.log script')).toBeNull()
    expect(document.querySelector('.log img')).toBeNull()
  })

  test('colour codes become colours', async () => {
    const { deliver } = await setup()
    deliver(line('\u001b[31mfailed\u001b[0m to connect'))
    expect(screen.getByText('failed')).toHaveStyle({ color: 'rgb(187, 0, 0)' })
    expect(document.querySelector('.log')).not.toHaveTextContent('[31m')
  })

  test('searching keeps the matching lines and marks the match', async () => {
    const { user, deliver } = await setup()
    deliver(line('GET /health 200'), line('ERROR payment timeout'), line('another error here'))

    await user.type(screen.getByRole('searchbox', { name: 'Search logs' }), 'error')
    expect(screen.queryByText('GET /health 200')).toBeNull()
    const marks = Array.from(document.querySelectorAll('.log mark')).map((m) => m.textContent)
    expect(marks).toEqual(['ERROR', 'error'])
    expect(screen.getByText('2 of 3 lines')).toBeInTheDocument()

    await user.clear(screen.getByRole('searchbox', { name: 'Search logs' }))
    expect(screen.getByText('GET /health 200')).toBeInTheDocument()
    expect(document.querySelector('.log mark')).toBeNull()
  })

  test('timestamps can be turned off', async () => {
    const { user, deliver } = await setup()
    deliver(line('hello'))
    expect(document.querySelector('.log-time')).not.toBeNull()
    await user.click(screen.getByRole('checkbox', { name: 'Timestamps' }))
    expect(document.querySelector('.log-time')).toBeNull()
  })

  test('following keeps the newest line in view; with follow off the view stays put', async () => {
    const height = vi.spyOn(HTMLElement.prototype, 'scrollHeight', 'get').mockReturnValue(1000)
    const { user, deliver } = await setup()
    const log = document.querySelector('.log') as HTMLElement

    deliver(line('one'))
    expect(log.scrollTop).toBe(1000)

    await user.click(screen.getByRole('checkbox', { name: 'Follow' }))
    log.scrollTop = 120
    height.mockReturnValue(2000)
    deliver(line('two'))
    expect(log.scrollTop).toBe(120)
  })

  test('choosing another tail reconnects and starts over', async () => {
    const { user, ws, deliver } = await setup()
    deliver(line('old line'))

    await user.selectOptions(screen.getByRole('combobox', { name: 'Lines to load' }), '2000')
    const next = FakeWebSocket.last()
    expect(next).not.toBe(ws)
    expect(ws.closedByClient).toBe(true)
    expect(next.url).toContain('tail=2000')
    expect(screen.queryByText('old line')).toBeNull()
  })

  test('a single container offers its log as a download; all together does not', async () => {
    await setup()
    expect(screen.getByRole('link', { name: 'Download' })).toHaveAttribute(
      'href',
      `/api/hosts/aws/containers/${first.id}/logs/download?timestamps=1`,
    )
  })

  test('says when the log has ended and offers to reconnect', async () => {
    const { user, ws } = await setup()
    act(() => ws.hangUp(1000, 'end of log'))
    expect(screen.getByText(/ended/i)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Reconnect' }))
    expect(FakeWebSocket.last()).not.toBe(ws)
  })

  test('full screen can be switched on and off', async () => {
    const { user } = await setup()
    const panel = document.querySelector('.panel') as HTMLElement
    await user.click(screen.getByRole('button', { name: 'Full screen' }))
    expect(panel).toHaveClass('fullscreen')
    await user.keyboard('{Escape}')
    expect(panel).not.toHaveClass('fullscreen')
  })
})

describe('ShellPanel', () => {
  test('a container that is not running gets a message, and no connection is opened', () => {
    render(<ShellPanel host={host()} service={svc} container={container({ state: 'exited' })} />)
    expect(screen.getByText(/not running/i)).toBeInTheDocument()
    expect(FakeWebSocket.instances).toHaveLength(0)
  })

  test('connects a terminal to the container and passes bytes both ways', async () => {
    render(<ShellPanel host={host()} service={svc} container={first} />)
    const ws = FakeWebSocket.last()
    expect(ws.url).toBe(`ws://localhost:3000/api/hosts/aws/containers/${first.id}/exec/ws?cols=100&rows=30`)
    expect(ws.binaryType).toBe('arraybuffer')
    act(() => ws.accept())

    act(() => ws.deliver(new TextEncoder().encode('/app # ').buffer as ArrayBuffer))
    expect(new TextDecoder().decode(terminal.written[0])).toBe('/app # ')

    act(() => terminal.onData?.('ls\r'))
    // Keystrokes travel as a binary frame, never as text: text frames are
    // reserved for control messages.
    const sent = ws.sent[0]
    expect(typeof sent).not.toBe('string')
    expect(ArrayBuffer.isView(sent)).toBe(true)
    expect(new TextDecoder().decode(sent as Uint8Array)).toBe('ls\r')
  })

  test('when there is no shell in the image it says so instead of showing a dead terminal', async () => {
    render(<ShellPanel host={host()} service={svc} container={first} />)
    const ws = FakeWebSocket.last()
    act(() => ws.accept())
    act(() => ws.deliver(JSON.stringify({ type: 'exit', code: 127, message: 'this container has no shell: its image does not include sh or bash' })))
    expect(await screen.findByText(/no shell/i)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Reconnect' })).toBeInTheDocument()
  })

  test('after the shell exits, Reconnect opens a new session', async () => {
    const user = userEvent.setup()
    render(<ShellPanel host={host()} service={svc} container={first} />)
    const ws = FakeWebSocket.last()
    act(() => ws.accept())
    act(() => ws.deliver(JSON.stringify({ type: 'exit', code: 0, message: '' })))
    expect(await screen.findByText(/shell exited/i)).toBeInTheDocument()

    await user.click(screen.getByRole('button', { name: 'Reconnect' }))
    await waitFor(() => expect(FakeWebSocket.instances).toHaveLength(2))
  })

  test('leaving the tab closes the session', () => {
    const { unmount } = render(<ShellPanel host={host()} service={svc} container={first} />)
    const ws = FakeWebSocket.last()
    act(() => ws.accept())
    unmount()
    expect(ws.closedByClient).toBe(true)
    expect(terminal.disposed).toBe(1)
  })
})

describe('InfoPanel', () => {
  const info: ContainerInfo = {
    id: first.id,
    name: 'shop-api-1',
    image: 'ghcr.io/acme/api:develop',
    imageId: 'sha256:img1',
    digests: ['ghcr.io/acme/api@sha256:aaaa'],
    created: '2026-11-01T08:00:00Z',
    startedAt: '2026-11-23T12:00:00Z',
    finishedAt: '',
    state: 'running',
    health: 'healthy',
    exitCode: 0,
    restartPolicy: 'unless-stopped',
    restarts: 4,
    command: ['/entry.sh', 'serve'],
    ports: [{ container: '3000/tcp', host: '0.0.0.0:8080' }],
    mounts: [{ type: 'bind', source: '/srv/shop/uploads', destination: '/app/uploads', readOnly: true }],
    networks: [{ name: 'shop_default', ip: '172.20.0.4', aliases: ['api'] }],
    labels: { 'com.docker.compose.project': 'shop', '<b>odd</b>': 'x' },
    compose: { project: 'shop', service: 'api', workingDir: '/srv/shop' },
  }

  test('shows what the container is made of', async () => {
    mockFetch({ [`GET /api/hosts/aws/containers/${first.id}/info`]: info })
    render(<InfoPanel host={host()} service={svc} container={first} />)

    expect(await screen.findByText('ghcr.io/acme/api@sha256:aaaa')).toBeInTheDocument()
    expect(screen.getByText('unless-stopped')).toBeInTheDocument()
    expect(screen.getByText('/entry.sh serve')).toBeInTheDocument()
    expect(screen.getByText('0.0.0.0:8080')).toBeInTheDocument()
    expect(screen.getByText('/srv/shop/uploads')).toBeInTheDocument()
    expect(screen.getByText('shop_default')).toBeInTheDocument()
    expect(screen.getByText('/srv/shop')).toBeInTheDocument()
    // Labels are folded away until asked for, and shown as text.
    const labels = screen.getByText(/^Labels/).closest('details') as HTMLDetailsElement
    expect(labels.open).toBe(false)
    expect(within(labels).getByText('<b>odd</b>')).toBeInTheDocument()
  })

  test('a failure to load is shown with its reason', async () => {
    mockFetch({})
    render(<InfoPanel host={host()} service={svc} container={first} />)
    expect(await screen.findByRole('alert')).toHaveTextContent(/no such API route/)
  })
})

import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { beforeEach, describe, expect, test, vi } from 'vitest'
import { ContainerList } from '../components/ContainerList'
import { ActionsProvider } from '../hooks/useActions'
import { HostsProvider } from '../hooks/useHosts'
import { panels } from '../panels'
import { actionInfos, container, FakeWebSocket, host, json, mockFetch, service } from '../test-utils'
import { ServicePage } from './ServicePage'

vi.mock('@xterm/xterm', () => ({
  Terminal: class {
    cols = 80
    rows = 24
    open() {}
    focus() {}
    loadAddon() {}
    write() {}
    onData() {
      return { dispose() {} }
    }
    onResize() {
      return { dispose() {} }
    }
    dispose() {}
  },
}))
vi.mock('@xterm/addon-fit', () => ({
  FitAddon: class {
    fit() {}
  },
}))
vi.mock('@xterm/xterm/css/xterm.css', () => ({}))

const svc = service({ update: { status: 'update_available', reason: '' } })
const [one, two] = svc.containers as [ReturnType<typeof container>, ReturnType<typeof container>]

const base = {
  'GET /api/hosts': [host()],
  'GET /api/hosts/aws/status': { ok: true, version: '27.0', error: '' },
  'GET /api/actions': actionInfos,
  'GET /api/hosts/aws/services/c~shop~api': svc,
  [`GET /api/hosts/aws/containers/${one.id}/env`]: { names: ['A'] },
  [`GET /api/hosts/aws/containers/${two.id}/env`]: { names: ['B'] },
}

function Where() {
  const location = useLocation()
  return <div data-testid="where">{location.pathname + location.search}</div>
}

function renderPage(at: string, routes: Parameters<typeof mockFetch>[0] = {}) {
  const fetched = mockFetch({ ...base, ...routes })
  render(
    <MemoryRouter initialEntries={[at]}>
      <HostsProvider>
        <ActionsProvider>
          <Routes>
            <Route path="/h/:hostId/s/:key" element={<ServicePage />} />
            <Route path="/h/:hostId" element={<div>services list</div>} />
          </Routes>
          <Where />
        </ActionsProvider>
      </HostsProvider>
    </MemoryRouter>,
  )
  return fetched
}

beforeEach(() => {
  FakeWebSocket.install()
  window.localStorage.clear()
})

describe('ServicePage', () => {
  test('shows the service, its image and its actions', async () => {
    renderPage('/h/aws/s/c~shop~api')
    expect(await screen.findByRole('heading', { name: 'api' })).toBeInTheDocument()
    expect(screen.getByText('ghcr.io/acme/api')).toBeInTheDocument()
    expect(screen.getByText('update')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Restart' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Pull & restart' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Stop' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()
    expect(screen.getByRole('link', { name: /back to aws/i })).toHaveAttribute('href', '/h/aws')
  })

  test('the address chooses the tab and the container', async () => {
    renderPage('/h/aws/s/c~shop~api?tab=env&c=shop-api-2')
    expect(await screen.findByRole('tab', { name: 'Env' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('button', { name: 'shop-api-2' })).toHaveAttribute('aria-current', 'true')
    // The second container's variables, not the first's.
    expect(await screen.findByRole('row', { name: /^B\b/ })).toBeInTheDocument()
  })

  test('with nothing chosen it opens the logs of all containers together', async () => {
    renderPage('/h/aws/s/c~shop~api')
    expect(await screen.findByRole('tab', { name: 'Logs' })).toHaveAttribute('aria-selected', 'true')
    expect(screen.getByRole('button', { name: 'All together' })).toHaveAttribute('aria-current', 'true')
    expect(FakeWebSocket.last().url).toContain('/services/c~shop~api/logs/ws')
  })

  test('choosing a tab or a container updates the address', async () => {
    const user = userEvent.setup()
    renderPage('/h/aws/s/c~shop~api')
    await screen.findByRole('tab', { name: 'Logs' })

    await user.click(screen.getByRole('button', { name: 'shop-api-2' }))
    expect(screen.getByTestId('where')).toHaveTextContent('/h/aws/s/c~shop~api?tab=logs&c=shop-api-2')
    expect(FakeWebSocket.last().url).toContain(`/containers/${two.id}/logs/ws`)

    await user.click(screen.getByRole('tab', { name: 'Env' }))
    expect(screen.getByTestId('where')).toHaveTextContent('/h/aws/s/c~shop~api?tab=env&c=shop-api-2')

    await user.click(screen.getByRole('tab', { name: 'Logs' }))
    await user.click(screen.getByRole('button', { name: 'All together' }))
    expect(screen.getByTestId('where')).toHaveTextContent('/h/aws/s/c~shop~api?tab=logs')
    expect(screen.getByTestId('where')).not.toHaveTextContent('c=')
  })

  test('"All together" is offered only on tabs that can show several containers', async () => {
    const user = userEvent.setup()
    renderPage('/h/aws/s/c~shop~api')
    await screen.findByRole('button', { name: 'All together' })

    await user.click(screen.getByRole('tab', { name: 'Env' }))
    expect(screen.queryByRole('button', { name: 'All together' })).toBeNull()
    // A tab about one container falls back to the first.
    expect(screen.getByRole('button', { name: 'shop-api-1' })).toHaveAttribute('aria-current', 'true')
    expect(await screen.findByRole('row', { name: /^A\b/ })).toBeInTheDocument()
  })

  test('a service with one container has no "All together"', async () => {
    renderPage('/h/aws/s/c~shop~api', {
      'GET /api/hosts/aws/services/c~shop~api': service({ total: 1, running: 1, containers: [one] }),
    })
    expect(await screen.findByRole('button', { name: 'shop-api-1' })).toHaveAttribute('aria-current', 'true')
    expect(screen.queryByRole('button', { name: 'All together' })).toBeNull()
  })

  test('an unknown tab in the address falls back to logs', async () => {
    renderPage('/h/aws/s/c~shop~api?tab=nonsense')
    expect(await screen.findByRole('tab', { name: 'Logs' })).toHaveAttribute('aria-selected', 'true')
  })

  test('a service that no longer exists says so and links back', async () => {
    renderPage('/h/aws/s/c~shop~gone', {
      'GET /api/hosts/aws/services/c~shop~gone': json(404, { error: 'no such service on aws', code: 'not_found' }),
    })
    expect(await screen.findByText(/no longer on this host/i)).toBeInTheDocument()
    expect(screen.getByRole('link', { name: /back to aws/i })).toHaveAttribute('href', '/h/aws')
  })

  test('an unreachable host shows why, with Retry', async () => {
    renderPage('/h/aws/s/c~shop~api', {
      'GET /api/hosts/aws/services/c~shop~api': json(502, { error: 'cannot reach aws: connection refused', code: 'host_unreachable' }),
    })
    const alert = await screen.findByRole('alert')
    expect(alert).toHaveTextContent('connection refused')
    expect(within(alert).getByRole('button', { name: 'Retry' })).toBeInTheDocument()
  })

  test('a header action runs on the whole service', async () => {
    const user = userEvent.setup()
    const fetched = renderPage('/h/aws/s/c~shop~api', {
      'POST /api/hosts/aws/services/c~shop~api/actions/restart': new Response(
        '{"step":"Restart finished","level":"success","done":true}\n',
        { status: 200 },
      ),
    })
    await user.click(await screen.findByRole('button', { name: 'Restart' }))
    await screen.findByText('Restart finished')
    expect(fetched.calls.find((c) => c.method === 'POST')?.body).toEqual({})
  })
})

describe('ContainerList', () => {
  const logs = panels.find((p) => p.id === 'logs')!
  const env = panels.find((p) => p.id === 'env')!

  test('lists the containers with their state and marks the chosen one', () => {
    render(
      <ContainerList service={svc} infos={actionInfos} allowAll selected={two.name} onSelect={() => {}} onAction={() => {}} />,
    )
    expect(screen.getByRole('button', { name: 'shop-api-2' })).toHaveAttribute('aria-current', 'true')
    expect(screen.getByRole('button', { name: 'shop-api-1' })).not.toHaveAttribute('aria-current', 'true')
    expect(screen.getAllByRole('img', { name: 'running' })).toHaveLength(2)
    expect(logs.supportsAll).toBe(true)
    expect(env.supportsAll).toBeFalsy()
  })

  test('choosing a container or "All together" reports the choice', async () => {
    const user = userEvent.setup()
    const onSelect = vi.fn()
    render(<ContainerList service={svc} infos={actionInfos} allowAll selected={null} onSelect={onSelect} onAction={() => {}} />)
    await user.click(screen.getByRole('button', { name: 'shop-api-2' }))
    await user.click(screen.getByRole('button', { name: 'All together' }))
    expect(onSelect.mock.calls).toEqual([['shop-api-2'], [null]])
  })

  test('a container’s own Restart runs on that container', async () => {
    const user = userEvent.setup()
    const onAction = vi.fn()
    render(<ContainerList service={svc} infos={actionInfos} allowAll selected={null} onSelect={() => {}} onAction={onAction} />)

    await user.click(screen.getByRole('button', { name: 'Actions for shop-api-2' }))
    const menu = screen.getByRole('menu')
    // Running: it can be restarted or stopped, not started; pulling is for the whole service.
    expect(within(menu).getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Restart', 'Stop'])
    await user.click(within(menu).getByRole('menuitem', { name: 'Restart' }))
    expect(onAction).toHaveBeenCalledWith(two.id, 'restart')
  })

  test('a stopped container offers Start instead of Stop', async () => {
    const user = userEvent.setup()
    const stopped = service({ containers: [container({ name: 'job-1', state: 'exited' })], total: 1, running: 0 })
    render(<ContainerList service={stopped} infos={actionInfos} allowAll={false} selected="job-1" onSelect={() => {}} onAction={() => {}} />)
    await user.click(screen.getByRole('button', { name: 'Actions for job-1' }))
    expect(within(screen.getByRole('menu')).getAllByRole('menuitem').map((i) => i.textContent)).toEqual(['Restart', 'Start'])
    expect(screen.queryByRole('button', { name: 'All together' })).toBeNull()
  })
})

import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { beforeEach, expect, test } from 'vitest'
import type { ServicesResponse } from '../api/types'
import { ActionsProvider } from '../hooks/useActions'
import { HostsProvider } from '../hooks/useHosts'
import { actionInfos, container, FakeWebSocket, host, json, mockFetch, service } from '../test-utils'
import { ServicesPage } from './ServicesPage'

const services: ServicesResponse = {
  updatesCheckedAt: '2026-11-26T11:58:00Z',
  groups: [
    {
      project: 'shop',
      services: [
        service(),
        service({
          key: 'c~shop~db',
          name: 'db',
          image: 'postgres:16.4',
          images: ['postgres:16.4'],
          running: 0,
          total: 1,
          state: 'stopped',
          startedAt: null,
          actions: ['restart', 'start', 'pull-restart'],
          containers: [container({ name: 'shop-db-1', state: 'exited' })],
        }),
      ],
    },
  ],
}

const base = {
  'GET /api/hosts': [host(), host({ id: 'gcp', name: 'gcp' })],
  'GET /api/hosts/aws/status': { ok: true, version: '27.0', error: '' },
  'GET /api/hosts/gcp/status': { ok: true, version: '27.0', error: '' },
  'GET /api/actions': actionInfos,
}

function renderPage(routes: Parameters<typeof mockFetch>[0], at = '/h/aws') {
  const fetched = mockFetch({ ...base, ...routes })
  render(
    <MemoryRouter initialEntries={[at]}>
      <HostsProvider>
        <ActionsProvider>
          <Routes>
            <Route path="/h/:hostId" element={<ServicesPage />} />
          </Routes>
        </ActionsProvider>
      </HostsProvider>
    </MemoryRouter>,
  )
  return fetched
}

beforeEach(() => {
  window.localStorage.clear()
})

test('lists the host’s services', async () => {
  renderPage({ 'GET /api/hosts/aws/services': services })
  expect(await screen.findByRole('row', { name: /^api\b/ })).toBeInTheDocument()
  expect(screen.getByRole('row', { name: /^db\b/ })).toBeInTheDocument()
  expect(screen.getByRole('link', { name: /aws/ })).toHaveAttribute('aria-current', 'page')
})

test('the filter and "hide stopped" narrow the list', async () => {
  const user = userEvent.setup()
  renderPage({ 'GET /api/hosts/aws/services': services })
  await screen.findByRole('row', { name: /^api\b/ })

  await user.type(screen.getByRole('searchbox', { name: 'Filter services' }), 'postgres')
  expect(screen.queryByRole('row', { name: /^api\b/ })).toBeNull()
  expect(screen.getByRole('row', { name: /^db\b/ })).toBeInTheDocument()

  await user.clear(screen.getByRole('searchbox', { name: 'Filter services' }))
  await user.click(screen.getByRole('checkbox', { name: 'Hide stopped' }))
  expect(screen.queryByRole('row', { name: /^db\b/ })).toBeNull()
  expect(screen.getByRole('row', { name: /^api\b/ })).toBeInTheDocument()

  await user.type(screen.getByRole('searchbox', { name: 'Filter services' }), 'nothing-like-this')
  expect(screen.getByText(/No services match/)).toBeInTheDocument()
})

test('an unreachable host shows why, with a Retry that loads again', async () => {
  const user = userEvent.setup()
  let reachable = false
  const fetched = renderPage({
    'GET /api/hosts/aws/services': () =>
      reachable
        ? services
        : json(502, { error: 'cannot reach aws: ssh: connect to host aws.example.com port 22: Connection timed out', code: 'host_unreachable' }),
  })

  const alert = await screen.findByRole('alert')
  expect(alert).toHaveTextContent('Connection timed out')
  expect(screen.queryByRole('table')).toBeNull()

  reachable = true
  // Retry is a load the person asked for, so it may start an update check too.
  const loads = () => fetched.calls.filter((c) => c.path === '/api/hosts/aws/services?check=1').length
  const before = loads()
  await user.click(within(alert).getByRole('button', { name: 'Retry' }))
  expect(await screen.findByRole('row', { name: /^api\b/ })).toBeInTheDocument()
  expect(loads()).toBeGreaterThan(before)
  expect(screen.queryByRole('alert')).toBeNull()
})

test('a host with nothing on it says so instead of showing an empty table', async () => {
  renderPage({ 'GET /api/hosts/aws/services': { groups: [], updatesCheckedAt: null } })
  expect(await screen.findByText(/no containers on this host/i)).toBeInTheDocument()
})

test('"Check now" asks the registries again and shows the result', async () => {
  const user = userEvent.setup()
  const updated: ServicesResponse = {
    ...services,
    groups: [{ project: 'shop', services: [service({ update: { status: 'update_available', reason: '' } })] }],
  }
  const fetched = renderPage({
    'GET /api/hosts/aws/services': services,
    'POST /api/hosts/aws/updates/check': updated,
  })
  await screen.findByRole('row', { name: /^api\b/ })
  expect(screen.queryByText('update')).toBeNull()

  await user.click(screen.getByRole('button', { name: 'Check now' }))
  expect(await screen.findByText('update')).toBeInTheDocument()
  expect(fetched.calls.some((c) => c.method === 'POST' && c.path === '/api/hosts/aws/updates/check')).toBe(true)
})

test('an address for a host that does not exist says so', async () => {
  renderPage({}, '/h/nope')
  expect(await screen.findByText(/no host/i)).toBeInTheDocument()
})

test('running an action reloads the list when it finishes', async () => {
  const user = userEvent.setup()
  const fetched = renderPage({
    'GET /api/hosts/aws/services': services,
    'POST /api/hosts/aws/services/c~shop~api/actions/restart': new Response(
      '{"step":"Restarting shop-api-1","level":"info","done":false}\n{"step":"Restart finished","level":"success","done":true}\n',
      { status: 200 },
    ),
  })
  const row = await screen.findByRole('row', { name: /^api\b/ })
  const loads = () => fetched.calls.filter((c) => c.method === 'GET' && c.path === '/api/hosts/aws/services').length
  const before = loads()

  await user.click(within(row).getByRole('button', { name: 'Restart' }))
  await screen.findByText('Restart finished')
  await waitFor(() => expect(loads()).toBeGreaterThan(before))
})

// Opening a list may start an update check on the server; the refreshes that
// follow must not, or a tab left open would use up the registry's quota.
test('only the first load of a host asks for an update check', async () => {
  const user = userEvent.setup()
  const fetched = renderPage({
    'GET /api/hosts/aws/services': services,
    'POST /api/hosts/aws/services/c~shop~api/actions/restart': new Response(
      '{"step":"Restart finished","level":"success","done":true}\n',
      { status: 200 },
    ),
  })
  const row = await screen.findByRole('row', { name: /^api\b/ })
  const loads = () => fetched.calls.filter((c) => c.method === 'GET' && c.path.startsWith('/api/hosts/aws/services')).map((c) => c.path)
  expect(loads()).toEqual(['/api/hosts/aws/services?check=1'])

  await user.click(within(row).getByRole('button', { name: 'Restart' }))
  await screen.findByText('Restart finished')
  await waitFor(() => expect(loads().length).toBeGreaterThan(1))
  expect(loads().slice(1).every((path) => path === '/api/hosts/aws/services')).toBe(true)
})

test('next to "Hide stopped" it says how many containers run, out of all on the host', async () => {
  const user = userEvent.setup()
  renderPage({ 'GET /api/hosts/aws/services': services })
  await screen.findByRole('row', { name: /^api\b/ })
  // api has two running, db has one that is stopped.
  expect(screen.getByText('2 of 3 containers running')).toBeInTheDocument()

  // The count is of the host, not of what the filter leaves on screen.
  await user.click(screen.getByRole('checkbox', { name: 'Hide stopped' }))
  await user.type(screen.getByRole('searchbox', { name: 'Filter services' }), 'api')
  expect(screen.getByText('2 of 3 containers running')).toBeInTheDocument()
})

// The services on screen, top to bottom, by the link each row's name is.
const shownOrder = (...known: string[]) =>
  screen
    .getAllByRole('link')
    .map((a) => a.textContent ?? '')
    .filter((text) => known.includes(text))

test('the list can be sorted by status and by name, and the choice is remembered', async () => {
  const user = userEvent.setup()
  const mixed: ServicesResponse = {
    updatesCheckedAt: null,
    groups: [
      {
        project: 'shop',
        services: [
          service({ key: 'c~shop~api', name: 'api' }),
          service({ key: 'c~shop~db', name: 'db', running: 0, total: 1, state: 'stopped', startedAt: null }),
          service({ key: 'c~shop~web', name: 'web', state: 'unhealthy' }),
        ],
      },
    ],
  }
  const order = () => shownOrder('api', 'db', 'web')

  renderPage({ 'GET /api/hosts/aws/services': mixed })
  await screen.findByRole('row', { name: /^api\b/ })
  expect(order()).toEqual(['api', 'db', 'web'])

  // What needs attention first: unhealthy, then stopped, then running.
  await user.click(screen.getByRole('button', { name: /^Status/ }))
  expect(order()).toEqual(['web', 'db', 'api'])
  expect(screen.getByRole('columnheader', { name: /^Status/ })).toHaveAttribute('aria-sort', 'ascending')

  await user.click(screen.getByRole('button', { name: /^Status/ }))
  expect(order()).toEqual(['api', 'db', 'web'])
  expect(screen.getByRole('columnheader', { name: /^Status/ })).toHaveAttribute('aria-sort', 'descending')

  await user.click(screen.getByRole('button', { name: /^Service/ }))
  await user.click(screen.getByRole('button', { name: /^Service/ }))
  expect(order()).toEqual(['web', 'db', 'api'])
  expect(window.localStorage.getItem('dc0s.services.sort')).toBe('name:desc')
})

test('a remembered sort is used when the list is opened again', async () => {
  window.localStorage.setItem('dc0s.services.sort', 'name:desc')
  renderPage({ 'GET /api/hosts/aws/services': services })
  await screen.findByRole('row', { name: /^api\b/ })
  expect(screen.getByRole('columnheader', { name: /^Service/ })).toHaveAttribute('aria-sort', 'descending')
  expect(shownOrder('api', 'db')).toEqual(['db', 'api'])
})

test('the list uses the whole width of the window', async () => {
  renderPage({ 'GET /api/hosts/aws/services': services })
  await screen.findByRole('row', { name: /^api\b/ })
  expect(screen.getByRole('main')).toHaveClass('wide')
})

test('a log opened under a row stays open while the list refreshes itself', async () => {
  FakeWebSocket.install()
  const user = userEvent.setup()
  const fetched = renderPage({
    'GET /api/hosts/aws/services': services,
    'POST /api/hosts/aws/services/c~shop~api/actions/restart': new Response(
      '{"step":"Restart finished","level":"success","done":true}\n',
      { status: 200 },
    ),
  })
  const row = await screen.findByRole('row', { name: /^api\b/ })
  await user.click(within(row).getByRole('button', { name: 'Show logs of api' }))
  expect(screen.getByRole('region', { name: 'Logs of api' })).toBeInTheDocument()

  // An action finishing makes the list load again.
  const loads = () => fetched.calls.filter((c) => c.method === 'GET' && c.path.startsWith('/api/hosts/aws/services')).length
  const before = loads()
  await user.click(within(row).getByRole('button', { name: 'Restart' }))
  await waitFor(() => expect(loads()).toBeGreaterThan(before))

  expect(screen.getByRole('region', { name: 'Logs of api' })).toBeInTheDocument()
  expect(FakeWebSocket.instances).toHaveLength(1)
})

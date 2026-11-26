import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes, useLocation } from 'react-router'
import { describe, expect, test, vi } from 'vitest'
import type { ServiceGroup } from '../api/types'
import { actionInfos, container, host, service } from '../test-utils'
import { ActionButtons } from './ActionButtons'
import { ServicesTable } from './ServicesTable'
import { UpdateBadge } from './UpdateBadge'

const now = new Date('2026-11-26T12:00:00Z')

const groups: ServiceGroup[] = [
  {
    project: 'shop',
    services: [
      service({ update: { status: 'update_available', reason: '' } }),
      service({
        key: 'c~shop~worker',
        name: 'worker',
        image: '',
        images: ['app:1', 'app:2'],
        running: 1,
        state: 'partial',
      }),
    ],
  },
  {
    project: '',
    services: [
      service({
        key: 's~redis-cache',
        name: 'redis-cache',
        project: '',
        image: 'redis:7.4',
        images: ['redis:7.4'],
        running: 0,
        total: 1,
        state: 'stopped',
        exitCode: 137,
        startedAt: null,
        actions: ['restart', 'start', 'pull-restart'],
        containers: [container({ name: 'redis-cache', state: 'exited', exitCode: 137 })],
      }),
    ],
  },
]

function Where() {
  const location = useLocation()
  return <div data-testid="where">{location.pathname + location.search}</div>
}

function renderTable(onAction = vi.fn()) {
  render(
    <MemoryRouter initialEntries={['/h/aws']}>
      <Routes>
        <Route path="*" element={<ServicesTable host={host()} groups={groups} infos={actionInfos} now={now} onAction={onAction} />} />
      </Routes>
      <Where />
    </MemoryRouter>,
  )
  return onAction
}

const row = (name: string) => screen.getByRole('row', { name: new RegExp(`^${name}\\b`) })

describe('ServicesTable', () => {
  test('shows each service with its containers, count, image and status', () => {
    renderTable()
    const api = row('api')
    expect(within(api).getByText('2/2')).toBeInTheDocument()
    expect(within(api).getByText('shop-api-1')).toBeInTheDocument()
    expect(within(api).getByText('shop-api-2')).toBeInTheDocument()
    expect(within(api).getByText('ghcr.io/acme/api')).toBeInTheDocument()
    expect(within(api).getByText(':develop')).toBeInTheDocument()
    expect(within(api).getByText('up 3d')).toBeInTheDocument()
    expect(within(api).getByText('update')).toBeInTheDocument()
  })

  test('groups by project, with a heading for containers outside any', () => {
    renderTable()
    expect(screen.getByRole('row', { name: 'shop' })).toBeInTheDocument()
    expect(screen.getByRole('row', { name: 'No project' })).toBeInTheDocument()
  })

  test('says "mixed" when containers of one service run different images', () => {
    renderTable()
    const mixed = within(row('worker')).getByText('mixed')
    expect(mixed).toHaveAttribute('title', 'app:1\napp:2')
    expect(within(row('worker')).getByText('1/2')).toBeInTheDocument()
  })

  test('a stopped service is dimmed and offers Start', () => {
    renderTable()
    const redis = row('redis-cache')
    expect(redis).toHaveClass('stopped')
    expect(within(redis).getByText('exited (137)')).toBeInTheDocument()
    expect(within(redis).getByRole('button', { name: 'Start' })).toBeInTheDocument()
    expect(row('api')).not.toHaveClass('stopped')
  })

  test('clicking a row opens the service; its Logs and Shell buttons open that tab', async () => {
    const user = userEvent.setup()
    renderTable()
    await user.click(within(row('api')).getByText('2/2'))
    expect(screen.getByTestId('where')).toHaveTextContent('/h/aws/s/c~shop~api')
  })

  test('the Logs and Shell links carry their tab', () => {
    renderTable()
    expect(within(row('api')).getByRole('link', { name: 'Logs' })).toHaveAttribute('href', '/h/aws/s/c~shop~api?tab=logs')
    expect(within(row('api')).getByRole('link', { name: 'Shell' })).toHaveAttribute('href', '/h/aws/s/c~shop~api?tab=shell')
  })

  test('an action button runs the action without opening the service', async () => {
    const user = userEvent.setup()
    const onAction = renderTable()
    await user.click(within(row('api')).getByRole('button', { name: 'Restart' }))
    expect(onAction).toHaveBeenCalledWith(expect.objectContaining({ key: 'c~shop~api' }), 'restart')
    expect(screen.getByTestId('where')).toHaveTextContent('/h/aws')
    expect(screen.getByTestId('where')).not.toHaveTextContent('/s/')
  })
})

describe('ActionButtons', () => {
  function renderButtons(actions: string[], state: 'running' | 'stopped' = 'running') {
    const onRun = vi.fn()
    render(
      <MemoryRouter>
        <ActionButtons hostId="aws" service={service({ actions, state })} infos={actionInfos} onRun={onRun} />
      </MemoryRouter>,
    )
    return onRun
  }

  test('offers only the actions that apply to the service', async () => {
    const user = userEvent.setup()
    renderButtons(['restart', 'stop'])
    expect(screen.getByRole('button', { name: 'Restart' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Pull & restart' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Start' })).toBeNull()

    await user.click(screen.getByRole('button', { name: 'More actions' }))
    const menu = screen.getByRole('menu')
    expect(within(menu).getByRole('menuitem', { name: 'Stop' })).toBeInTheDocument()
    expect(within(menu).queryByRole('menuitem', { name: 'Start' })).toBeNull()
  })

  test('running a menu action closes the menu and reports the action', async () => {
    const user = userEvent.setup()
    const onRun = renderButtons(['restart', 'stop', 'pull-restart'])
    await user.click(screen.getByRole('button', { name: 'More actions' }))
    await user.click(screen.getByRole('menuitem', { name: 'Stop' }))
    expect(onRun).toHaveBeenCalledWith('stop')
    expect(screen.queryByRole('menu')).toBeNull()
  })

  test('with nothing left for the menu, there is no menu button', () => {
    renderButtons(['restart', 'pull-restart'])
    expect(screen.queryByRole('button', { name: 'More actions' })).toBeNull()
  })

  test('an action this frontend has never heard of still appears, in the menu', async () => {
    const user = userEvent.setup()
    const onRun = vi.fn()
    render(
      <MemoryRouter>
        <ActionButtons
          hostId="aws"
          service={service({ actions: ['restart', 'backup'] })}
          infos={[...actionInfos, { id: 'backup', label: 'Back up', destructive: false, containerLevel: false }]}
          onRun={onRun}
        />
      </MemoryRouter>,
    )
    await user.click(screen.getByRole('button', { name: 'More actions' }))
    await user.click(screen.getByRole('menuitem', { name: 'Back up' }))
    expect(onRun).toHaveBeenCalledWith('backup')
  })
})

describe('UpdateBadge', () => {
  test('shows a badge when a newer image exists', () => {
    render(<UpdateBadge update={{ status: 'update_available', reason: '' }} />)
    expect(screen.getByText('update')).toHaveAttribute('title', expect.stringMatching(/newer image/i))
  })

  test('shows a grey mark with the reason when the check could not decide', () => {
    render(<UpdateBadge update={{ status: 'unknown', reason: 'needs a registry login' }} />)
    expect(screen.getByText('?')).toHaveAttribute('title', expect.stringContaining('needs a registry login'))
  })

  test.each(['up_to_date', 'pinned', 'unchecked'] as const)('shows nothing for %s', (status) => {
    const { container: root } = render(<UpdateBadge update={{ status, reason: '' }} />)
    expect(root).toBeEmptyDOMElement()
  })
})

import { render, screen, within } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { expect, test } from 'vitest'
import type { HostView } from '../api/types'
import { HostTabs } from './HostTabs'

const hosts: HostView[] = [
  { id: 'aws', name: 'aws', url: 'ssh://deploy@aws.example.com', protected: true, hasTLS: false },
  { id: 'gcp', name: 'gcp', url: 'tcp://10.0.0.5:2376', protected: false, hasTLS: true },
  { id: 'local', name: 'local', url: 'unix:///var/run/docker.sock', protected: false, hasTLS: false },
]

function renderTabs(current: string | null, statuses = {}) {
  return render(
    <MemoryRouter>
      <HostTabs hosts={hosts} currentHostId={current} statuses={statuses} />
    </MemoryRouter>,
  )
}

test('shows one tab per host, each linking to its services', () => {
  renderTabs('gcp')
  const nav = screen.getByRole('navigation', { name: 'Hosts' })
  const links = within(nav).getAllByRole('link')
  expect(links.slice(0, 3).map((a) => a.getAttribute('href'))).toEqual(['/h/aws', '/h/gcp', '/h/local'])
  expect(within(nav).getByRole('link', { name: /gcp/ })).toBeInTheDocument()
})

test('marks the host being viewed', () => {
  renderTabs('gcp')
  expect(screen.getByRole('link', { name: /gcp/ })).toHaveAttribute('aria-current', 'page')
  expect(screen.getByRole('link', { name: /aws/ })).not.toHaveAttribute('aria-current')
})

test('shows a lock on protected hosts only', () => {
  renderTabs(null)
  expect(within(screen.getByRole('link', { name: /aws/ })).getByLabelText('protected')).toBeInTheDocument()
  expect(within(screen.getByRole('link', { name: /gcp/ })).queryByLabelText('protected')).toBeNull()
})

test('shows whether each host answers, with the reason when it does not', () => {
  renderTabs(null, {
    aws: { ok: true, version: '27.0', error: '' },
    gcp: { ok: false, version: '', error: 'connection refused' },
  })
  expect(within(screen.getByRole('link', { name: /aws/ })).getByLabelText('reachable')).toBeInTheDocument()
  const gcp = screen.getByRole('link', { name: /gcp/ })
  expect(within(gcp).getByLabelText('unreachable')).toBeInTheDocument()
  expect(gcp).toHaveAttribute('title', expect.stringContaining('connection refused'))
  // Not asked yet.
  expect(within(screen.getByRole('link', { name: /local/ })).getByLabelText('checking')).toBeInTheDocument()
})

test('offers a way to add a host', () => {
  renderTabs(null)
  expect(screen.getByRole('link', { name: 'Add a host' })).toHaveAttribute('href', '/settings/hosts?new=1')
})

import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { MemoryRouter, Route, Routes } from 'react-router'
import { describe, expect, test, vi } from 'vitest'
import type { HostView, RegistryView } from '../api/types'
import { HostForm } from '../components/HostForm'
import { RegistryForm } from '../components/RegistryForm'
import { HostsProvider } from '../hooks/useHosts'
import { host, json, mockFetch } from '../test-utils'
import { HostsSettings } from './HostsSettings'
import { RegistriesSettings } from './RegistriesSettings'

const saved: HostView = host({ id: 'aws-prod', name: 'AWS Prod', url: 'tcp://10.0.0.5:2376', hasTLS: true })

describe('HostForm', () => {
  function setup(initial: HostView | undefined, routes: Parameters<typeof mockFetch>[0] = {}) {
    const fetched = mockFetch(routes)
    const onSaved = vi.fn()
    const onCancel = vi.fn()
    const user = userEvent.setup()
    render(<HostForm initial={initial} onSaved={onSaved} onCancel={onCancel} />)
    const sent = (method: string) => fetched.calls.filter((c) => c.method === method)
    return { user, onSaved, onCancel, sent }
  }

  test('a name and a connection URL are required', async () => {
    const { user, sent, onSaved } = setup(undefined)
    await user.click(screen.getByRole('button', { name: 'Add host' }))
    expect(screen.getByText('Give the host a name.')).toBeInTheDocument()
    expect(screen.getByText('Enter the connection URL.')).toBeInTheDocument()
    expect(sent('POST')).toHaveLength(0)
    expect(onSaved).not.toHaveBeenCalled()
  })

  test('saves a new host and reports it', async () => {
    const created = host({ id: 'gcp', name: 'gcp', url: 'ssh://deploy@gcp.example.com', protected: true })
    const { user, sent, onSaved } = setup(undefined, { 'POST /api/hosts': json(201, created) })

    await user.type(screen.getByLabelText('Name'), 'gcp')
    await user.type(screen.getByLabelText('Connection URL'), 'ssh://deploy@gcp.example.com')
    await user.click(screen.getByLabelText(/Protected/))
    await user.click(screen.getByRole('button', { name: 'Add host' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(created))
    expect(sent('POST')[0]).toEqual({
      method: 'POST',
      path: '/api/hosts',
      body: { name: 'gcp', url: 'ssh://deploy@gcp.example.com', protected: true, tls: null },
    })
  })

  test('what the server says about the URL is shown next to it', async () => {
    const { user, onSaved } = setup(undefined, {
      'POST /api/hosts': json(400, { error: 'unsupported scheme "http": use unix://, tcp:// or ssh://', code: 'invalid' }),
    })
    await user.type(screen.getByLabelText('Name'), 'x')
    await user.type(screen.getByLabelText('Connection URL'), 'http://nope')
    await user.click(screen.getByRole('button', { name: 'Add host' }))

    const url = screen.getByLabelText('Connection URL')
    expect(await screen.findByText(/unsupported scheme/)).toBeInTheDocument()
    expect(url).toHaveAccessibleDescription(/unsupported scheme/)
    expect(url).toBeInvalid()
    expect(onSaved).not.toHaveBeenCalled()
  })

  test('a name that is taken is reported next to the name', async () => {
    const { user } = setup(undefined, {
      'POST /api/hosts': json(409, { error: 'already exists: a host named "aws"', code: 'duplicate' }),
    })
    await user.type(screen.getByLabelText('Name'), 'aws')
    await user.type(screen.getByLabelText('Connection URL'), 'tcp://10.0.0.5:2375')
    await user.click(screen.getByRole('button', { name: 'Add host' }))
    await screen.findByText(/already exists/)
    expect(screen.getByLabelText('Name')).toBeInvalid()
  })

  test('TLS certificates are offered for tcp:// only', async () => {
    const { user } = setup(undefined)
    const url = screen.getByLabelText('Connection URL')
    expect(screen.queryByLabelText(/TLS certificates/)).toBeNull()

    await user.type(url, 'ssh://deploy@host')
    expect(screen.queryByLabelText(/TLS certificates/)).toBeNull()
    expect(screen.getByText(/keys or agent/i)).toBeInTheDocument()

    await user.clear(url)
    await user.type(url, 'tcp://10.0.0.5:2376')
    await user.click(screen.getByLabelText(/TLS certificates/))
    expect(screen.getByLabelText('CA certificate')).toBeInTheDocument()
    expect(screen.getByLabelText('Client certificate')).toBeInTheDocument()
    expect(screen.getByLabelText('Client key')).toBeInTheDocument()
  })

  test('new TLS material is sent as typed', async () => {
    const { user, sent } = setup(undefined, { 'POST /api/hosts': json(201, saved) })
    await user.type(screen.getByLabelText('Name'), 'tls')
    await user.type(screen.getByLabelText('Connection URL'), 'tcp://10.0.0.5:2376')
    await user.click(screen.getByLabelText(/TLS certificates/))
    await user.type(screen.getByLabelText('CA certificate'), 'CA-PEM')
    await user.type(screen.getByLabelText('Client certificate'), 'CERT-PEM')
    await user.type(screen.getByLabelText('Client key'), 'KEY-PEM')
    await user.click(screen.getByRole('button', { name: 'Add host' }))
    await waitFor(() => expect(sent('POST')).toHaveLength(1))
    expect((sent('POST')[0]!.body as { tls: unknown }).tls).toEqual({ ca: 'CA-PEM', cert: 'CERT-PEM', key: 'KEY-PEM' })
  })

  test('editing a host with saved certificates keeps them when the fields are left blank', async () => {
    const { user, sent, onSaved } = setup(saved, { 'PUT /api/hosts/aws-prod': { ...saved, name: 'Production' } })

    expect(screen.getByLabelText('Name')).toHaveValue('AWS Prod')
    expect(screen.getByLabelText(/TLS certificates/)).toBeChecked()
    // The saved certificates are never sent to the browser: the fields are empty…
    expect(screen.getByLabelText('Client key')).toHaveValue('')
    // …and say that blank means "keep".
    expect(screen.getAllByText(/saved — leave blank to keep/i)).toHaveLength(3)

    await user.clear(screen.getByLabelText('Name'))
    await user.type(screen.getByLabelText('Name'), 'Production')
    await user.click(screen.getByRole('button', { name: 'Save' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalled())
    expect(sent('PUT')[0]).toEqual({
      method: 'PUT',
      path: '/api/hosts/aws-prod',
      body: { name: 'Production', url: 'tcp://10.0.0.5:2376', protected: false, tls: { ca: '', cert: '', key: '' } },
    })
  })

  test('turning TLS off for a saved host sends no TLS section', async () => {
    const { user, sent } = setup(saved, { 'PUT /api/hosts/aws-prod': { ...saved, hasTLS: false } })
    await user.click(screen.getByLabelText(/TLS certificates/))
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(sent('PUT')).toHaveLength(1))
    expect((sent('PUT')[0]!.body as { tls: unknown }).tls).toBeNull()
  })

  test('"Test connection" shows the Docker version when it works', async () => {
    const { user, sent, onSaved } = setup(saved, { 'POST /api/hosts/test': { ok: true, version: '27.0', error: '' } })
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/Docker 27\.0/)).toBeInTheDocument()
    // The saved host's ID goes along, so the server can use its stored certificates.
    expect(sent('POST')[0]!.body).toMatchObject({ id: 'aws-prod', url: 'tcp://10.0.0.5:2376' })
    expect(onSaved).not.toHaveBeenCalled()
  })

  test('"Test connection" shows why it failed', async () => {
    const { user } = setup(undefined, {
      'POST /api/hosts/test': { ok: false, version: '', error: 'ssh: deploy@host: Permission denied (publickey).' },
    })
    await user.type(screen.getByLabelText('Connection URL'), 'ssh://deploy@host')
    await user.click(screen.getByRole('button', { name: 'Test connection' }))
    expect(await screen.findByText(/Permission denied \(publickey\)/)).toBeInTheDocument()
  })

  test('Cancel leaves without saving', async () => {
    const { user, onCancel, sent } = setup(saved)
    await user.click(screen.getByRole('button', { name: 'Cancel' }))
    expect(onCancel).toHaveBeenCalled()
    expect(sent('PUT')).toHaveLength(0)
  })
})

describe('RegistryForm', () => {
  const login: RegistryView = { id: 'r1', address: 'ghcr.io', username: 'bot', hasPassword: true }

  test('adds a login', async () => {
    const fetched = mockFetch({ 'POST /api/registries': json(201, login) })
    const onSaved = vi.fn()
    const user = userEvent.setup()
    render(<RegistryForm onSaved={onSaved} onCancel={() => {}} />)

    await user.click(screen.getByRole('button', { name: 'Add login' }))
    expect(screen.getByText('Enter the registry’s address.')).toBeInTheDocument()

    await user.type(screen.getByLabelText('Registry address'), 'ghcr.io')
    await user.type(screen.getByLabelText('Username'), 'bot')
    await user.type(screen.getByLabelText('Password or token'), 'ghp_secret')
    await user.click(screen.getByRole('button', { name: 'Add login' }))

    await waitFor(() => expect(onSaved).toHaveBeenCalledWith(login))
    expect(fetched.calls.at(-1)!.body).toEqual({ address: 'ghcr.io', username: 'bot', password: 'ghp_secret' })
  })

  test('editing keeps the saved password when the field is left blank', async () => {
    const fetched = mockFetch({ 'PUT /api/registries/r1': { ...login, username: 'bot2' } })
    const user = userEvent.setup()
    render(<RegistryForm initial={login} onSaved={() => {}} onCancel={() => {}} />)

    expect(screen.getByLabelText('Password or token')).toHaveValue('')
    expect(screen.getByText(/saved — leave blank to keep/i)).toBeInTheDocument()

    await user.clear(screen.getByLabelText('Username'))
    await user.type(screen.getByLabelText('Username'), 'bot2')
    await user.click(screen.getByRole('button', { name: 'Save' }))
    await waitFor(() => expect(fetched.calls.some((c) => c.method === 'PUT')).toBe(true))
    expect(fetched.calls.find((c) => c.method === 'PUT')!.body).toEqual({ address: 'ghcr.io', username: 'bot2', password: '' })
  })

  test('a duplicate address is reported', async () => {
    mockFetch({ 'POST /api/registries': json(409, { error: 'already exists: a login for "ghcr.io"', code: 'duplicate' }) })
    const user = userEvent.setup()
    render(<RegistryForm onSaved={() => {}} onCancel={() => {}} />)
    await user.type(screen.getByLabelText('Registry address'), 'ghcr.io')
    await user.click(screen.getByRole('button', { name: 'Add login' }))
    expect(await screen.findByText(/already exists/)).toBeInTheDocument()
  })
})

function renderSettings(page: 'hosts' | 'registries', routes: Parameters<typeof mockFetch>[0], at?: string) {
  const fetched = mockFetch({
    'GET /api/hosts': [host(), saved],
    'GET /api/hosts/aws/status': { ok: true, version: '27.0', error: '' },
    'GET /api/hosts/aws-prod/status': { ok: false, version: '', error: 'connection refused' },
    ...routes,
  })
  render(
    <MemoryRouter initialEntries={[at ?? `/settings/${page}`]}>
      <HostsProvider>
        <Routes>
          <Route path="/settings/hosts" element={<HostsSettings />} />
          <Route path="/settings/registries" element={<RegistriesSettings />} />
        </Routes>
      </HostsProvider>
    </MemoryRouter>,
  )
  return fetched
}

describe('HostsSettings', () => {
  test('lists the saved hosts', async () => {
    renderSettings('hosts', {})
    const row = await screen.findByRole('listitem', { name: 'AWS Prod' })
    expect(within(row).getByText('tcp://10.0.0.5:2376')).toBeInTheDocument()
    expect(screen.getByRole('listitem', { name: 'aws' })).toBeInTheDocument()
  })

  test('removing a host asks first, and only then calls the server', async () => {
    const user = userEvent.setup()
    const fetched = renderSettings('hosts', { 'DELETE /api/hosts/aws-prod': new Response(null, { status: 204 }) })
    const row = await screen.findByRole('listitem', { name: 'AWS Prod' })
    const deletes = () => fetched.calls.filter((c) => c.method === 'DELETE')

    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    const dialog = screen.getByRole('dialog')
    expect(dialog).toHaveTextContent('Remove AWS Prod?')
    expect(deletes()).toHaveLength(0)

    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }))
    expect(screen.queryByRole('dialog')).toBeNull()
    expect(deletes()).toHaveLength(0)

    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(deletes()).toEqual([{ method: 'DELETE', path: '/api/hosts/aws-prod', body: undefined }]))
  })

  test('"Edit" opens the form with the host’s values', async () => {
    const user = userEvent.setup()
    renderSettings('hosts', {})
    const row = await screen.findByRole('listitem', { name: 'AWS Prod' })
    await user.click(within(row).getByRole('button', { name: 'Edit' }))
    expect(screen.getByLabelText('Name')).toHaveValue('AWS Prod')
    expect(screen.getByLabelText('Connection URL')).toHaveValue('tcp://10.0.0.5:2376')
  })

  test('the "add a host" link from the tabs opens an empty form', async () => {
    renderSettings('hosts', {}, '/settings/hosts?new=1')
    expect(await screen.findByLabelText('Connection URL')).toHaveValue('')
    expect(screen.getByRole('button', { name: 'Add host' })).toBeInTheDocument()
  })

  test('with no hosts at all, the form is simply there', async () => {
    renderSettings('hosts', { 'GET /api/hosts': [] })
    expect(await screen.findByLabelText('Connection URL')).toBeInTheDocument()
  })
})

describe('RegistriesSettings', () => {
  const logins: RegistryView[] = [{ id: 'r1', address: 'ghcr.io', username: 'bot', hasPassword: true }]

  test('lists the saved logins and removes one after asking', async () => {
    const user = userEvent.setup()
    const fetched = renderSettings('registries', {
      'GET /api/registries': logins,
      'DELETE /api/registries/r1': new Response(null, { status: 204 }),
    })
    const row = await screen.findByRole('listitem', { name: 'ghcr.io' })
    expect(within(row).getByText(/bot/)).toBeInTheDocument()

    await user.click(within(row).getByRole('button', { name: 'Remove' }))
    expect(fetched.calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Remove' }))
    await waitFor(() => expect(fetched.calls.some((c) => c.method === 'DELETE' && c.path === '/api/registries/r1')).toBe(true))
  })

  test('with no logins it explains what they are for', async () => {
    renderSettings('registries', { 'GET /api/registries': [] })
    expect(await screen.findByText(/private registr/i)).toBeInTheDocument()
  })
})

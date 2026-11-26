import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test } from 'vitest'
import type { HostView, ServiceView } from '../api/types'
import { actionInfos, host, json, mockFetch, ndjson, service } from '../test-utils'
import { ActionsProvider, useActions } from './useActions'

const actionPath = '/api/hosts/aws/services/c~shop~api/actions/'

function finished(label: string) {
  return ndjson(
    { step: 'Working on shop-api-1', level: 'info', done: false },
    { step: `${label} finished`, level: 'success', done: true },
  )
}

/** A page with one button per action, as the real pages have. */
function Harness({ target, svc }: { target: HostView; svc: ServiceView }) {
  const actions = useActions()
  return (
    <div>
      <div data-testid="version">{actions.version}</div>
      {actions.infos.map((info) => (
        <button key={info.id} onClick={() => actions.run(target, svc, info.id)}>
          run {info.id}
        </button>
      ))}
      <button onClick={() => actions.run(target, svc, 'restart', svc.containers[1]!.id)}>run restart on second</button>
    </div>
  )
}

async function setup(target: HostView, routes: Parameters<typeof mockFetch>[0] = {}) {
  const fetched = mockFetch({ 'GET /api/actions': actionInfos, ...routes })
  const user = userEvent.setup()
  render(
    <ActionsProvider>
      <Harness target={target} svc={service()} />
    </ActionsProvider>,
  )
  await screen.findByRole('button', { name: 'run restart' })
  const posts = () => fetched.calls.filter((c) => c.method === 'POST')
  return { user, posts }
}

test('on an ordinary host a harmless action runs at once and shows its progress', async () => {
  const { user, posts } = await setup(host(), { [`POST ${actionPath}restart`]: finished('Restart') })

  await user.click(screen.getByRole('button', { name: 'run restart' }))

  expect(screen.queryByRole('dialog')).toBeNull()
  await screen.findByText('Restart finished')
  expect(screen.getByText('Working on shop-api-1')).toBeInTheDocument()
  expect(posts()).toEqual([{ method: 'POST', path: `${actionPath}restart`, body: {} }])
  // Pages watch this number to know when to reload.
  await waitFor(() => expect(screen.getByTestId('version')).toHaveTextContent('1'))
})

test('on an ordinary host a destructive action asks first', async () => {
  const { user, posts } = await setup(host(), { [`POST ${actionPath}stop`]: finished('Stop') })

  await user.click(screen.getByRole('button', { name: 'run stop' }))
  const dialog = await screen.findByRole('dialog')
  expect(dialog).toHaveTextContent('Stop api on aws?')
  expect(screen.queryByRole('textbox')).toBeNull()
  expect(posts()).toHaveLength(0)

  await user.click(screen.getByRole('button', { name: 'Stop' }))
  await screen.findByText('Stop finished')
  expect(posts()).toEqual([{ method: 'POST', path: `${actionPath}stop`, body: {} }])
  expect(screen.queryByRole('dialog')).toBeNull()
})

test('cancelling the confirmation sends nothing', async () => {
  const { user, posts } = await setup(host())
  await user.click(screen.getByRole('button', { name: 'run stop' }))
  await user.click(await screen.findByRole('button', { name: 'Cancel' }))
  expect(screen.queryByRole('dialog')).toBeNull()
  expect(posts()).toHaveLength(0)
})

test('on a protected host even a restart needs the service name typed, and sends it', async () => {
  const { user, posts } = await setup(host({ protected: true }), { [`POST ${actionPath}restart`]: finished('Restart') })

  await user.click(screen.getByRole('button', { name: 'run restart' }))
  await screen.findByRole('dialog')
  const confirm = screen.getByRole('button', { name: 'Restart' })
  expect(confirm).toBeDisabled()
  expect(posts()).toHaveLength(0)

  await user.type(screen.getByRole('textbox'), 'api')
  await user.click(confirm)
  await screen.findByText('Restart finished')
  expect(posts()).toEqual([{ method: 'POST', path: `${actionPath}restart`, body: { confirm: 'api' } }])
})

test('an action on one container names that container', async () => {
  const { user, posts } = await setup(host(), { [`POST ${actionPath}restart`]: finished('Restart') })
  await user.click(screen.getByRole('button', { name: 'run restart on second' }))
  await screen.findByText('Restart finished')
  expect(posts()[0]!.body).toEqual({ container: service().containers[1]!.id })
  expect(screen.getByText('Restart shop-api-2')).toBeInTheDocument()
})

test('a refusal is shown as a failed operation with the reason', async () => {
  const { user } = await setup(host(), {
    [`POST ${actionPath}restart`]: json(409, { error: 'another action is already running on this service', code: 'busy' }),
  })
  await user.click(screen.getByRole('button', { name: 'run restart' }))
  await screen.findByText('another action is already running on this service')
  expect(screen.getByRole('status')).toHaveTextContent(/failed/i)
})

test('an action that fails on the server is shown as failed and stays until dismissed', async () => {
  const { user } = await setup(host(), {
    [`POST ${actionPath}restart`]: ndjson(
      { step: 'Restarting shop-api-1', level: 'info', done: false },
      { step: 'shop-api-1: no space left on device', level: 'error', done: true },
    ),
  })
  await user.click(screen.getByRole('button', { name: 'run restart' }))
  await screen.findByText('shop-api-1: no space left on device')
  expect(screen.getByRole('status')).toHaveTextContent(/failed/i)

  await user.click(screen.getByRole('button', { name: 'Dismiss' }))
  expect(screen.queryByText('shop-api-1: no space left on device')).toBeNull()
})

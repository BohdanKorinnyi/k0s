import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { ActionProgress, type Operation } from './ActionProgress'
import { ConfirmDialog } from './ConfirmDialog'

function renderDialog(mode: 'click' | 'type-name') {
  const onConfirm = vi.fn()
  const onCancel = vi.fn()
  render(
    <ConfirmDialog
      title="Stop api on aws?"
      mode={mode}
      expected="api"
      confirmLabel="Stop"
      danger
      onConfirm={onConfirm}
      onCancel={onCancel}
    >
      Its 2 containers will be stopped.
    </ConfirmDialog>,
  )
  return { onConfirm, onCancel }
}

test('a click confirmation can be confirmed at once', async () => {
  const user = userEvent.setup()
  const { onConfirm } = renderDialog('click')
  expect(screen.getByRole('dialog', { name: 'Stop api on aws?' })).toBeInTheDocument()
  expect(screen.queryByRole('textbox')).toBeNull()
  await user.click(screen.getByRole('button', { name: 'Stop' }))
  expect(onConfirm).toHaveBeenCalledTimes(1)
})

test('a typed confirmation unlocks only when the service name matches exactly', async () => {
  const user = userEvent.setup()
  const { onConfirm } = renderDialog('type-name')
  const confirm = screen.getByRole('button', { name: 'Stop' })
  const input = screen.getByRole('textbox')
  expect(confirm).toBeDisabled()

  await user.type(input, 'ap')
  expect(confirm).toBeDisabled()
  await user.type(input, 'I')
  expect(confirm).toBeDisabled() // "apI" is not "api"
  await user.clear(input)
  await user.type(input, 'api')
  expect(confirm).toBeEnabled()

  await user.click(confirm)
  expect(onConfirm).toHaveBeenCalledTimes(1)
})

test('Enter confirms a typed confirmation only once it matches', async () => {
  const user = userEvent.setup()
  const { onConfirm } = renderDialog('type-name')
  await user.type(screen.getByRole('textbox'), 'ap{Enter}')
  expect(onConfirm).not.toHaveBeenCalled()
  await user.type(screen.getByRole('textbox'), 'i{Enter}')
  expect(onConfirm).toHaveBeenCalledTimes(1)
})

test('Cancel and Escape run nothing', async () => {
  const user = userEvent.setup()
  const { onConfirm, onCancel } = renderDialog('click')
  await user.click(screen.getByRole('button', { name: 'Cancel' }))
  await user.keyboard('{Escape}')
  expect(onCancel).toHaveBeenCalledTimes(2)
  expect(onConfirm).not.toHaveBeenCalled()
})

const running: Operation = {
  id: 1,
  title: 'Pull & restart api',
  hostName: 'aws',
  status: 'running',
  events: [
    { step: 'Pulling ghcr.io/acme/api:develop', level: 'info', done: false },
    { step: 'Stopping shop-api-1', level: 'info', done: false },
  ],
}

test('progress lists each step while an action runs', () => {
  render(<ActionProgress operations={[running]} onDismiss={() => {}} />)
  expect(screen.getByText('Pull & restart api')).toBeInTheDocument()
  expect(screen.getByText('aws')).toBeInTheDocument()
  expect(screen.getByText('Pulling ghcr.io/acme/api:develop')).toBeInTheDocument()
  expect(screen.getByText('Stopping shop-api-1')).toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent(/running/i)
  // Nothing to dismiss while it is still going.
  expect(screen.queryByRole('button', { name: 'Dismiss' })).toBeNull()
})

test('a failed action stays until dismissed', async () => {
  const user = userEvent.setup()
  const onDismiss = vi.fn()
  const failed: Operation = {
    ...running,
    status: 'error',
    events: [...running.events, { step: 'shop-api-1: port is already allocated', level: 'error', done: true }],
  }
  render(<ActionProgress operations={[failed]} onDismiss={onDismiss} />)
  expect(screen.getByText('shop-api-1: port is already allocated')).toBeInTheDocument()
  expect(screen.getByRole('status')).toHaveTextContent(/failed/i)
  await user.click(screen.getByRole('button', { name: 'Dismiss' }))
  expect(onDismiss).toHaveBeenCalledWith(1)
})

test('a finished action says so', () => {
  const done: Operation = {
    ...running,
    status: 'success',
    events: [...running.events, { step: 'Pull & restart finished', level: 'success', done: true }],
  }
  render(<ActionProgress operations={[done]} onDismiss={() => {}} />)
  expect(screen.getByRole('status')).toHaveTextContent(/done/i)
  expect(screen.getByRole('button', { name: 'Dismiss' })).toBeInTheDocument()
})

test('with no operations nothing is drawn', () => {
  const { container } = render(<ActionProgress operations={[]} onDismiss={() => {}} />)
  expect(container).toBeEmptyDOMElement()
})

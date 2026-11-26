import { act, render, renderHook } from '@testing-library/react'
import { afterEach, beforeEach, expect, test, vi } from 'vitest'
import { host, json, mockFetch } from '../test-utils'
import { HostsProvider } from './useHosts'
import { useServices } from './useServices'

// These tests are about what happens as time passes, so the clock is theirs.
beforeEach(() => {
  vi.useFakeTimers()
})
afterEach(() => {
  vi.useRealTimers()
})

const seconds = (n: number) => act(() => vi.advanceTimersByTimeAsync(n * 1000))

test('a reachable host is refreshed every five seconds', async () => {
  const fetched = mockFetch({ 'GET /api/hosts/aws/services': { groups: [], updatesCheckedAt: null } })
  renderHook(() => useServices('aws', 0))
  await seconds(0)
  expect(fetched.calls).toHaveLength(1)
  await seconds(5)
  expect(fetched.calls).toHaveLength(2)
  await seconds(10)
  expect(fetched.calls).toHaveLength(4)
})

// Over SSH every attempt is a login on the server. Retrying a host that does
// not answer every few seconds, for as long as a tab stays open, is how one
// gets banned from one's own server.
test('a host that cannot be reached is not retried on a timer', async () => {
  let reachable = false
  const fetched = mockFetch({
    'GET /api/hosts/aws/services': () =>
      reachable
        ? { groups: [], updatesCheckedAt: null }
        : json(502, { error: 'cannot reach aws: Permission denied (publickey)', code: 'host_unreachable' }),
  })
  const { result } = renderHook(() => useServices('aws', 0))
  await seconds(0)
  expect(result.current.error).toContain('Permission denied')
  expect(fetched.calls).toHaveLength(1)

  await seconds(60)
  expect(fetched.calls).toHaveLength(1)

  // Retry is the person's decision; once it works, refreshing resumes.
  reachable = true
  act(() => result.current.reload())
  await seconds(0)
  expect(result.current.error).toBeNull()
  expect(fetched.calls).toHaveLength(2)
  await seconds(5)
  expect(fetched.calls).toHaveLength(3)
})

test('the dot on a tab is refreshed every half minute while the host answers, and far less often while it does not', async () => {
  const fetched = mockFetch({
    'GET /api/hosts': [host({ id: 'up', name: 'up' }), host({ id: 'down', name: 'down' })],
    'GET /api/hosts/up/status': { ok: true, version: '27.0', error: '' },
    'GET /api/hosts/down/status': { ok: false, version: '', error: 'connection refused' },
  })
  const checks = (id: string) => fetched.calls.filter((c) => c.path === `/api/hosts/${id}/status`).length

  render(
    <HostsProvider>
      <div />
    </HostsProvider>,
  )
  await seconds(0)
  await seconds(0)
  expect(checks('up')).toBe(1)
  expect(checks('down')).toBe(1)

  await seconds(90)
  expect(checks('up')).toBe(4)
  expect(checks('down')).toBe(1)

  await seconds(5 * 60)
  expect(checks('down')).toBe(2)
})

import { describe, expect, test } from 'vitest'
import type { ServiceView } from '../api/types'
import { toSegments } from './ansi'
import { confirmationFor } from './confirm'
import { relativeTime, splitImage, statusLabel, uptime } from './format'
import { appendLines, LOG_BUFFER_CAP } from './logBuffer'
import { matchLines, splitHighlight } from './logSearch'
import { hostPath, parseServiceLocation, servicePath } from './serviceUrl'

describe('confirmationFor', () => {
  const ordinary = { protected: false }
  const locked = { protected: true }

  test('an ordinary host asks only before destructive actions', () => {
    expect(confirmationFor({ destructive: false }, ordinary)).toBe('none')
    expect(confirmationFor({ destructive: true }, ordinary)).toBe('click')
  })

  test('a protected host wants the service name typed for everything', () => {
    expect(confirmationFor({ destructive: false }, locked)).toBe('type-name')
    expect(confirmationFor({ destructive: true }, locked)).toBe('type-name')
  })
})

describe('service URLs', () => {
  test('a service page link carries host, service, tab and container', () => {
    expect(servicePath('aws', 'c~shop~api', { tab: 'logs', container: 'shop-api-1' })).toBe(
      '/h/aws/s/c~shop~api?tab=logs&c=shop-api-1',
    )
    expect(servicePath('aws', 'c~shop~api')).toBe('/h/aws/s/c~shop~api')
    expect(servicePath('aws', 'c~shop~api', { tab: 'env' })).toBe('/h/aws/s/c~shop~api?tab=env')
    expect(hostPath('aws')).toBe('/h/aws')
  })

  test('parsing gives back what was put in', () => {
    expect(parseServiceLocation('/h/aws/s/c~shop~api', '?tab=logs&c=shop-api-1')).toEqual({
      hostId: 'aws',
      key: 'c~shop~api',
      tab: 'logs',
      container: 'shop-api-1',
    })
  })

  test('the tab defaults to logs and the container to none', () => {
    expect(parseServiceLocation('/h/aws/s/s~redis-cache', '')).toEqual({
      hostId: 'aws',
      key: 's~redis-cache',
      tab: 'logs',
      container: null,
    })
  })

  test('names that need escaping survive the round trip', () => {
    const path = servicePath('my host', 's~odd name/with?marks', { container: 'a b' })
    const [pathname, search = ''] = path.split('?')
    expect(pathname).not.toContain(' ')
    expect(parseServiceLocation(pathname!, '?' + search)).toEqual({
      hostId: 'my host',
      key: 's~odd name/with?marks',
      tab: 'logs',
      container: 'a b',
    })
  })

  test('a path that is not a service page parses to nothing', () => {
    expect(parseServiceLocation('/h/aws', '')).toBeNull()
    expect(parseServiceLocation('/settings/hosts', '')).toBeNull()
    expect(parseServiceLocation('/h/aws/s/', '')).toBeNull()
  })
})

describe('uptime', () => {
  const now = new Date('2026-11-26T12:00:00Z')
  const ago = (ms: number) => new Date(now.getTime() - ms).toISOString()
  const s = 1000
  const m = 60 * s
  const h = 60 * m
  const d = 24 * h

  test('uses the largest whole unit', () => {
    expect(uptime(ago(3 * d + 5 * h), now)).toBe('3d')
    expect(uptime(ago(5 * h + 59 * m), now)).toBe('5h')
    expect(uptime(ago(12 * m + 30 * s), now)).toBe('12m')
    expect(uptime(ago(40 * s), now)).toBe('40s')
  })

  test('a start time in the future (clocks disagree) reads as just started', () => {
    expect(uptime(ago(-5 * s), now)).toBe('0s')
  })

  test('relativeTime says how long ago in words', () => {
    expect(relativeTime(ago(20 * s), now)).toBe('just now')
    expect(relativeTime(ago(2 * m), now)).toBe('2 min ago')
    expect(relativeTime(ago(3 * h), now)).toBe('3 h ago')
    expect(relativeTime(ago(2 * d), now)).toBe('2 d ago')
  })
})

function service(overrides: Partial<ServiceView>): ServiceView {
  return {
    key: 'c~shop~api',
    name: 'api',
    project: 'shop',
    image: 'shop/api:develop',
    images: ['shop/api:develop'],
    running: 2,
    total: 2,
    state: 'running',
    exitCode: 0,
    startedAt: '2026-11-23T12:00:00Z',
    restarts: 0,
    update: { status: 'up_to_date', reason: '' },
    actions: ['restart', 'stop', 'pull-restart'],
    containers: [],
    ...overrides,
  }
}

describe('statusLabel', () => {
  const now = new Date('2026-11-26T12:00:00Z')

  test.each([
    [service({}), 'up 3d'],
    [service({ state: 'running', restarts: 2 }), 'up 3d · restarted ×2'],
    [service({ state: 'restarting', restarts: 14 }), 'restarting ×14'],
    [service({ state: 'restarting', restarts: 0 }), 'restarting'],
    [service({ state: 'stopped', running: 0, exitCode: 137, startedAt: null }), 'exited (137)'],
    [service({ state: 'unhealthy' }), 'unhealthy'],
    [service({ state: 'partial', running: 1, total: 2 }), '1/2 running'],
    [service({ state: 'running', startedAt: null }), 'up'],
  ])('%#', (svc, want) => {
    expect(statusLabel(svc, now)).toBe(want)
  })
})

describe('splitImage', () => {
  test.each([
    ['shop/api:develop', { name: 'shop/api', tag: 'develop' }],
    ['nginx', { name: 'nginx', tag: '' }],
    ['localhost:5000/app', { name: 'localhost:5000/app', tag: '' }],
    ['localhost:5000/app:1.2', { name: 'localhost:5000/app', tag: '1.2' }],
    ['ghcr.io/acme/api@sha256:abcdef0123456789', { name: 'ghcr.io/acme/api', tag: '@sha256:abcdef012345' }],
    ['', { name: '', tag: '' }],
  ])('%s', (ref, want) => {
    expect(splitImage(ref)).toEqual(want)
  })
})

describe('appendLines', () => {
  test('adds lines to the end in a new array', () => {
    const before = [1, 2]
    const after = appendLines(before, [3])
    expect(after).toEqual([1, 2, 3])
    expect(after).not.toBe(before)
    expect(before).toEqual([1, 2])
  })

  test('keeps only the newest lines once past the cap', () => {
    const buffer = Array.from({ length: LOG_BUFFER_CAP }, (_, i) => i)
    const after = appendLines(buffer, [-1, -2, -3])
    expect(after).toHaveLength(LOG_BUFFER_CAP)
    expect(after.slice(0, 2)).toEqual([3, 4])
    expect(after.slice(-3)).toEqual([-1, -2, -3])
    expect(LOG_BUFFER_CAP).toBe(10_000)
  })

  test('a smaller cap can be given', () => {
    expect(appendLines([1, 2, 3], [4, 5], 3)).toEqual([3, 4, 5])
    expect(appendLines([], [1, 2, 3, 4, 5], 2)).toEqual([4, 5])
  })
})

describe('log search', () => {
  const lines = [{ l: 'GET /health 200' }, { l: 'ERROR payment timeout' }, { l: 'an error here' }, { l: 'path a.b ok' }, { l: 'axb' }]

  test('matchLines finds lines regardless of case', () => {
    expect(matchLines(lines, 'error')).toEqual([1, 2])
    expect(matchLines(lines, 'ERROR')).toEqual([1, 2])
    expect(matchLines(lines, '')).toEqual([])
    expect(matchLines(lines, 'nope')).toEqual([])
  })

  test('the query is plain text, not a pattern', () => {
    expect(matchLines(lines, 'a.b')).toEqual([3])
    expect(matchLines(lines, '.*')).toEqual([])
  })

  test('splitHighlight marks each occurrence and keeps the original case', () => {
    expect(splitHighlight('an Error here', 'error')).toEqual([
      { text: 'an ' },
      { text: 'Error', hit: true },
      { text: ' here' },
    ])
    expect(splitHighlight('aXa', 'a')).toEqual([{ text: 'a', hit: true }, { text: 'X' }, { text: 'a', hit: true }])
    expect(splitHighlight('errorerror', 'error')).toEqual([
      { text: 'error', hit: true },
      { text: 'error', hit: true },
    ])
  })

  test('nothing to find means one plain piece', () => {
    expect(splitHighlight('plain', '')).toEqual([{ text: 'plain' }])
    expect(splitHighlight('plain', 'zzz')).toEqual([{ text: 'plain' }])
    expect(splitHighlight('', 'x')).toEqual([{ text: '' }])
  })
})

describe('toSegments', () => {
  test('turns colour codes into styled pieces and leaves markup as text', () => {
    const segments = toSegments('\u001b[31mred\u001b[0m <b>')
    expect(segments).toHaveLength(2)
    expect(segments[0]).toMatchObject({ text: 'red', color: 'rgb(187, 0, 0)' })
    expect(segments[1]).toEqual({ text: ' <b>' })
  })

  test('carries background and bold', () => {
    const [segment] = toSegments('\u001b[1;44mnote\u001b[0m')
    expect(segment).toMatchObject({ text: 'note', background: 'rgb(0, 0, 187)', bold: true })
  })

  test('text without codes is one plain piece, character for character', () => {
    const text = '<script>alert(1)</script> & "quotes"'
    expect(toSegments(text)).toEqual([{ text }])
    expect(toSegments('')).toEqual([{ text: '' }])
  })

  test('cursor and other control sequences are dropped, not shown', () => {
    const joined = toSegments('\u001b[2Kdone\u001b[1A')
      .map((s) => s.text)
      .join('')
    expect(joined).toBe('done')
  })
})

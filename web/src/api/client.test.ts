import { afterEach, describe, expect, test, vi } from 'vitest'
import { api, ApiError, readNDJSON, setUnauthenticatedHandler, streamAction, wsURL } from './client'
import type { ActionEvent } from './types'

/** A stream that delivers the given text chunks one read at a time. */
function streamOf(...chunks: string[]): ReadableStream<Uint8Array> {
  const encoder = new TextEncoder()
  return new ReadableStream({
    start(controller) {
      for (const chunk of chunks) controller.enqueue(encoder.encode(chunk))
      controller.close()
    },
  })
}

function jsonResponse(status: number, body: unknown): Response {
  return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
}

afterEach(() => {
  setUnauthenticatedHandler(() => {})
})

describe('readNDJSON', () => {
  test('yields each object once, in order, however the bytes are split', async () => {
    const seen: unknown[] = []
    await readNDJSON(streamOf('{"n":1}\n{"n"', ':2}\n', '{"n":3}'), (v) => seen.push(v))
    expect(seen).toEqual([{ n: 1 }, { n: 2 }, { n: 3 }])
  })

  test('copes with a character split across chunks', async () => {
    const bytes = new TextEncoder().encode('{"s":"привіт"}\n')
    const stream = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(bytes.slice(0, 9)) // cuts a two-byte letter in half
        controller.enqueue(bytes.slice(9))
        controller.close()
      },
    })
    const seen: unknown[] = []
    await readNDJSON(stream, (v) => seen.push(v))
    expect(seen).toEqual([{ s: 'привіт' }])
  })

  test('skips blank lines', async () => {
    const seen: unknown[] = []
    await readNDJSON(streamOf('\n{"a":1}\n\n\n{"a":2}\n'), (v) => seen.push(v))
    expect(seen).toEqual([{ a: 1 }, { a: 2 }])
  })
})

describe('api', () => {
  test('sends JSON and returns the decoded body', async () => {
    const fetchMock = vi.fn(async () => jsonResponse(201, { id: 'aws' }))
    vi.stubGlobal('fetch', fetchMock)

    await expect(api.post('/api/hosts', { name: 'aws' })).resolves.toEqual({ id: 'aws' })
    const [url, init] = fetchMock.mock.calls[0] as unknown as [string, RequestInit]
    expect(url).toBe('/api/hosts')
    expect(init.method).toBe('POST')
    expect(init.body).toBe('{"name":"aws"}')
    expect(new Headers(init.headers).get('Content-Type')).toBe('application/json')
  })

  test('a response without a body resolves to nothing', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response(null, { status: 204 })))
    await expect(api.del('/api/hosts/aws')).resolves.toBeUndefined()
  })

  test('an error carries its status, code and message', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(409, { error: 'a host named "aws" already exists', code: 'duplicate' })))
    const err = await api.post('/api/hosts', {}).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(ApiError)
    expect(err).toMatchObject({ status: 409, code: 'duplicate', message: 'a host named "aws" already exists' })
  })

  test('an error that is not JSON still becomes an ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => new Response('This panel only answers requests addressed to…', { status: 403 })))
    const err = await api.get('/api/hosts').catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 403, code: 'http_403' })
    expect((err as ApiError).message).toContain('only answers requests')
  })

  test('a 401 tells the app to go to the login page', async () => {
    const handler = vi.fn()
    setUnauthenticatedHandler(handler)
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(401, { error: 'log in to use the panel', code: 'unauthenticated' })))
    await expect(api.get('/api/hosts')).rejects.toMatchObject({ status: 401, code: 'unauthenticated' })
    expect(handler).toHaveBeenCalledTimes(1)
  })

  test('a network failure is reported in plain words', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('Failed to fetch'))))
    const err = await api.get('/api/hosts').catch((e: unknown) => e)
    expect(err).toMatchObject({ status: 0, code: 'network' })
  })
})

describe('streamAction', () => {
  test('passes on every event and resolves with the closing one', async () => {
    const body = streamOf(
      '{"step":"Restarting shop-api-1","level":"info","done":false}\n',
      '{"step":"Restart finished","level":"success","done":true}\n',
    )
    vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status: 200 })))

    const seen: ActionEvent[] = []
    const final = await streamAction('/api/x', { confirm: 'api' }, (e) => seen.push(e))
    expect(seen.map((e) => e.step)).toEqual(['Restarting shop-api-1', 'Restart finished'])
    expect(final).toEqual({ step: 'Restart finished', level: 'success', done: true })
  })

  test('a refusal before the stream starts is an ApiError', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => jsonResponse(409, { error: 'another action is already running on this service', code: 'busy' })))
    await expect(streamAction('/api/x', {}, () => {})).rejects.toMatchObject({ status: 409, code: 'busy' })
  })

  test('a stream that ends without a closing event says the action may still be running', async () => {
    const body = streamOf('{"step":"Pulling app:develop","level":"info","done":false}\n')
    vi.stubGlobal('fetch', vi.fn(async () => new Response(body, { status: 200 })))
    const err = await streamAction('/api/x', {}, () => {}).catch((e: unknown) => e)
    expect(err).toBeInstanceOf(Error)
    expect((err as Error).message).toMatch(/still be running/)
  })
})

describe('wsURL', () => {
  test('keeps the host and switches the scheme', () => {
    expect(wsURL('/api/x', { protocol: 'http:', host: 'localhost:8080' })).toBe('ws://localhost:8080/api/x')
    expect(wsURL('/api/x', { protocol: 'https:', host: 'panel.lan' })).toBe('wss://panel.lan/api/x')
  })
})

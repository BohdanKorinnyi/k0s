import { useCallback, useEffect, useRef, useState } from 'react'
import { wsURL } from '../api/client'
import type { LogLine } from '../api/types'
import { appendLines } from '../lib/logBuffer'

/** A log line with a number that stays the same while it is on screen. */
export type StreamedLine = LogLine & { id: number }

export type LogStreamStatus = 'connecting' | 'live' | 'ended' | 'error'

export type LogStream = {
  lines: StreamedLine[]
  status: LogStreamStatus
  /** Why the stream stopped, when it stopped on a problem. */
  error: string | null
  /** Starts the stream again from scratch. */
  reconnect: () => void
}

/**
 * Follows a log over a WebSocket. `path` is the API path with its query; when
 * it changes (another container, another number of lines) the old connection
 * is closed and the lines start over.
 *
 * `epoch` is any value that changes when the thing being followed starts
 * anew, such as a container's start time. A container that restarts ends its
 * log stream; a new epoch reconnects to it without the person having to ask.
 */
export function useLogStream(path: string, epoch = ''): LogStream {
  const [lines, setLines] = useState<StreamedLine[]>([])
  const [status, setStatus] = useState<LogStreamStatus>('connecting')
  const [error, setError] = useState<string | null>(null)
  const [attempt, setAttempt] = useState(0)
  const nextId = useRef(0)

  useEffect(() => {
    setLines([])
    setStatus('connecting')
    setError(null)

    const ws = new WebSocket(wsURL(path))
    ws.onopen = () => setStatus('live')
    ws.onmessage = (event) => {
      if (typeof event.data !== 'string') return
      let batch: LogLine[]
      try {
        batch = JSON.parse(event.data) as LogLine[]
      } catch {
        return
      }
      const added = batch.map((line) => ({ ...line, id: nextId.current++ }))
      setLines((prev) => appendLines(prev, added))
    }
    ws.onclose = (event) => {
      // 1000 is a normal end: the container stopped, or was replaced.
      if (event.code === 1000) {
        setStatus('ended')
      } else {
        setStatus('error')
        setError(event.reason || 'The connection to the log was lost.')
      }
    }

    return () => {
      ws.onopen = ws.onmessage = ws.onclose = null
      ws.close()
    }
  }, [path, epoch, attempt])

  const reconnect = useCallback(() => setAttempt((n) => n + 1), [])
  return { lines, status, error, reconnect }
}

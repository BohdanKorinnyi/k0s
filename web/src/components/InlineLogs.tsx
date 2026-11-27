import { useLayoutEffect, useRef } from 'react'
import { Link } from 'react-router'
import { paths } from '../api/client'
import type { HostView, ServiceView } from '../api/types'
import { useLogStream } from '../hooks/useLogStream'
import { useStoredFlag } from '../hooks/useStoredFlag'
import { servicePath } from '../lib/serviceUrl'
import { LogRow, StreamStatus } from './LogLines'

// Enough to see what a service is doing now; the full log page has the rest.
const TAIL = 200

/**
 * A service's log, live, in the small space under its row in the list. All of
 * the service's containers are followed together. It is for a glance: search,
 * download and the whole log are on the service's own page, one link away.
 */
export function InlineLogs({ host, service }: { host: HostView; service: ServiceView }) {
  // The same choices as on the full log page.
  const [timestamps] = useStoredFlag('logs.timestamps', true)
  const [wrap] = useStoredFlag('logs.wrap', false)

  // A container that starts again ends its old stream; a new start time
  // connects to the new one. The list refreshing itself changes neither.
  const epoch = service.containers.map((c) => `${c.id}@${c.startedAt ?? ''}`).join(',')
  const stream = useLogStream(`${paths.service(host.id, service.key)}/logs/ws?tail=${TAIL}`, epoch)

  // New lines keep the view at the end, unless the person scrolled up to read.
  const view = useRef<HTMLDivElement>(null)
  const atEnd = useRef(true)
  const lastId = stream.lines.at(-1)?.id
  useLayoutEffect(() => {
    const el = view.current
    if (el && atEnd.current) el.scrollTop = el.scrollHeight
  }, [lastId, stream.lines.length, wrap, timestamps])

  const several = service.containers.length > 1
  return (
    <section className="inline-logs" aria-label={`Logs of ${service.name}`}>
      <div className="inline-logs-bar">
        <StreamStatus status={stream.status} error={stream.error} onReconnect={stream.reconnect} />
        <span className="grow" />
        <Link to={servicePath(host.id, service.key, { tab: 'logs' })}>Open full log</Link>
      </div>
      <div
        ref={view}
        className={wrap ? 'log wrap' : 'log'}
        onScroll={(e) => {
          const el = e.currentTarget
          atEnd.current = el.scrollHeight - el.scrollTop - el.clientHeight < 24
        }}
      >
        {stream.lines.length === 0 ? (
          <div className="log-empty">{stream.status === 'connecting' ? 'Connecting…' : 'Nothing has been logged yet.'}</div>
        ) : (
          stream.lines.map((line) => <LogRow key={line.id} line={line} showTime={timestamps} showSource={several} />)
        )}
      </div>
    </section>
  )
}

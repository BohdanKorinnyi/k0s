import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { paths } from '../api/client'
import { Icon } from '../components/Icon'
import { LogRow, StreamStatus } from '../components/LogLines'
import { useLogStream } from '../hooks/useLogStream'
import { useStoredFlag } from '../hooks/useStoredFlag'
import { matchLines } from '../lib/logSearch'
import type { PanelProps } from './index'

const TAILS = ['100', '500', '2000', 'all'] as const
type Tail = (typeof TAILS)[number]

/** A container's log, live. With no container, all of the service's together. */
export function LogsPanel({ host, service, container }: PanelProps) {
  const [tail, setTail] = useState<Tail>('500')
  const [follow, setFollow] = useState(true)
  const [timestamps, setTimestamps] = useStoredFlag('logs.timestamps', true)
  const [wrap, setWrap] = useStoredFlag('logs.wrap', false)
  const [query, setQuery] = useState('')
  const [fullscreen, setFullscreen] = useState(false)

  const base = container ? paths.container(host.id, container.id) : paths.service(host.id, service.key)
  // When a followed container starts again its start time changes, and the
  // stream — which ended when it stopped — is opened afresh.
  const followed = container ? [container] : service.containers
  const epoch = followed.map((c) => `${c.id}@${c.startedAt ?? ''}`).join(',')
  const stream = useLogStream(`${base}/logs/ws?tail=${tail}`, epoch)

  // Searching narrows the view to the lines that match; it never asks the
  // server, only looks through what is loaded.
  const shown = useMemo(() => {
    if (query === '') return stream.lines
    return matchLines(stream.lines, query).map((i) => stream.lines[i]!)
  }, [stream.lines, query])

  const view = useRef<HTMLDivElement>(null)
  const lastId = shown.at(-1)?.id
  useLayoutEffect(() => {
    const el = view.current
    if (follow && el) el.scrollTop = el.scrollHeight
  }, [follow, lastId, shown.length, wrap, timestamps])

  useEffect(() => {
    if (!fullscreen) return
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') setFullscreen(false)
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [fullscreen])

  return (
    <div className={fullscreen ? 'panel card fullscreen' : 'panel'}>
      <div className="panel-toolbar">
        <input
          type="search"
          className="input"
          style={{ width: 200 }}
          placeholder="Search…"
          aria-label="Search logs"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        {query !== '' && (
          <span className="muted">
            {shown.length} of {stream.lines.length} lines
          </span>
        )}
        <select className="select" aria-label="Lines to load" value={tail} onChange={(e) => setTail(e.target.value as Tail)}>
          {TAILS.map((t) => (
            <option key={t} value={t}>
              {t === 'all' ? 'Whole log' : `Last ${t}`}
            </option>
          ))}
        </select>
        <label className="check">
          <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} />
          Follow
        </label>
        <label className="check">
          <input type="checkbox" checked={timestamps} onChange={(e) => setTimestamps(e.target.checked)} />
          Timestamps
        </label>
        <label className="check">
          <input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} />
          Wrap
        </label>
        <span className="grow" />
        <StreamStatus status={stream.status} error={stream.error} onReconnect={stream.reconnect} />
        {container && (
          <a
            className="btn icon"
            href={`${paths.container(host.id, container.id)}/logs/download?timestamps=1`}
            download={`${container.name}.log`}
            aria-label="Download"
            title="Download the whole log"
          >
            <Icon name="download" />
          </a>
        )}
        <button
          type="button"
          className="btn icon"
          aria-label={fullscreen ? 'Exit full screen' : 'Full screen'}
          title={fullscreen ? 'Exit full screen (Esc)' : 'Full screen'}
          onClick={() => setFullscreen((v) => !v)}
        >
          <Icon name={fullscreen ? 'shrink' : 'expand'} />
        </button>
      </div>

      <div
        ref={view}
        className={wrap ? 'log wrap' : 'log'}
        // Scrolling back to read stops the view from jumping to the end.
        onWheel={(e) => {
          if (e.deltaY < 0 && follow) setFollow(false)
        }}
      >
        {shown.length === 0 ? (
          <div className="log-empty">
            {stream.status === 'connecting'
              ? 'Connecting…'
              : query !== ''
                ? 'No loaded line matches.'
                : 'Nothing has been logged yet.'}
          </div>
        ) : (
          shown.map((line) => (
            <LogRow key={line.id} line={line} showTime={timestamps} showSource={container === null} query={query} />
          ))
        )}
      </div>
    </div>
  )
}

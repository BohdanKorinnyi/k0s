import { memo, useMemo } from 'react'
import type { StreamedLine } from '../hooks/useLogStream'
import { toSegments, type Segment } from '../lib/ansi'
import { formatLogTime } from '../lib/format'
import { splitHighlight } from '../lib/logSearch'

type RowProps = {
  line: StreamedLine
  showTime: boolean
  /** Whether to say which container wrote the line. */
  showSource: boolean
  /** Text to mark where it occurs; "" for none. */
  query?: string
}

/**
 * One log line. Everything from the log is rendered as text nodes: a line
 * containing "<script>" shows those characters and nothing else happens.
 */
export const LogRow = memo(function LogRow({ line, showTime, showSource, query = '' }: RowProps) {
  const segments = useMemo(() => toSegments(line.l), [line.l])
  return (
    <div className={line.s === 'stderr' ? 'log-line stderr' : 'log-line'}>
      {showTime && (
        <span className="log-time" title={line.t}>
          {formatLogTime(line.t)}
        </span>
      )}
      {showSource && <span className="log-source">{line.c}</span>}
      {segments.length === 1 && isPlain(segments[0]!) && query === '' ? (
        <span className="log-text">{segments[0]!.text}</span>
      ) : (
        <span className="log-text">
          {segments.map((segment, i) => (
            <SegmentSpan key={i} segment={segment} query={query} />
          ))}
        </span>
      )}
    </div>
  )
})

function isPlain(s: Segment): boolean {
  return !s.color && !s.background && !s.bold && !s.dim && !s.italic && !s.underline
}

function SegmentSpan({ segment, query }: { segment: Segment; query: string }) {
  const style = {
    color: segment.color,
    background: segment.background,
    fontWeight: segment.bold ? 700 : undefined,
    opacity: segment.dim ? 0.7 : undefined,
    fontStyle: segment.italic ? 'italic' : undefined,
    textDecoration: segment.underline ? 'underline' : undefined,
  }
  const pieces = splitHighlight(segment.text, query)
  const content = pieces.map((piece, i) => (piece.hit ? <mark key={i}>{piece.text}</mark> : piece.text))
  return isPlain(segment) ? <>{content}</> : <span style={style}>{content}</span>
}

/** Whether a log is being followed, and a way to start again when it is not. */
export function StreamStatus({ status, error, onReconnect }: { status: string; error: string | null; onReconnect: () => void }) {
  if (status === 'live') {
    return (
      <span className="log-status">
        <span className="dot ok" /> live
      </span>
    )
  }
  if (status === 'connecting') return <span className="log-status">connecting…</span>
  return (
    <span className="log-status" title={error ?? undefined}>
      <span className={status === 'error' ? 'dot bad' : 'dot idle'} />
      {status === 'error' ? 'connection lost' : 'log ended'}
      <button type="button" className="btn small" onClick={onReconnect}>
        Reconnect
      </button>
    </span>
  )
}

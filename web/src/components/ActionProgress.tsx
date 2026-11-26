import { useEffect, useRef } from 'react'
import type { ActionEvent } from '../api/types'
import { Icon } from './Icon'

/** One action that was started from this browser. */
export type Operation = {
  id: number
  /** What is being done to what: "Restart api". */
  title: string
  hostName: string
  status: 'running' | 'success' | 'error'
  events: ActionEvent[]
}

type Props = {
  operations: Operation[]
  onDismiss: (id: number) => void
}

/**
 * The step-by-step progress of running actions, in the corner of the window.
 * A finished action, successful or not, stays until it is dismissed: the
 * result of a pull-and-restart is not something to miss by blinking.
 */
export function ActionProgress({ operations, onDismiss }: Props) {
  if (operations.length === 0) return null
  return (
    <div className="progress-stack">
      {operations.map((op) => (
        <OperationCard key={op.id} op={op} onDismiss={onDismiss} />
      ))}
    </div>
  )
}

const statusText = { running: 'running…', success: 'done', error: 'failed' } as const

function OperationCard({ op, onDismiss }: { op: Operation; onDismiss: (id: number) => void }) {
  const list = useRef<HTMLOListElement>(null)
  // Keep the newest step in view.
  useEffect(() => {
    const el = list.current
    if (el) el.scrollTop = el.scrollHeight
  }, [op.events.length])

  return (
    <section className={op.status === 'running' ? 'progress' : `progress ${op.status}`}>
      <div className="progress-head">
        {op.status === 'running' && <span className="spinner" aria-hidden="true" />}
        <span>{op.title}</span>
        <span className="muted">{op.hostName}</span>
        <span className="grow" />
        <span className="muted" role="status">
          {statusText[op.status]}
        </span>
        {op.status !== 'running' && (
          <button type="button" className="btn icon quiet" aria-label="Dismiss" title="Dismiss" onClick={() => onDismiss(op.id)}>
            <Icon name="close" />
          </button>
        )}
      </div>
      <ol ref={list}>
        {op.events.map((event, i) => (
          <li key={i} className={event.level === 'info' ? undefined : event.level}>
            {event.step}
          </li>
        ))}
      </ol>
    </section>
  )
}

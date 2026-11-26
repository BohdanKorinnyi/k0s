import { useEffect, useRef, useState, type MouseEvent } from 'react'
import { Link } from 'react-router'
import type { ActionInfo, ServiceView } from '../api/types'
import { servicePath } from '../lib/serviceUrl'
import { Icon, type IconName } from './Icon'

type Props = {
  hostId: string
  service: ServiceView
  /** Every action the server knows, from /api/actions. */
  infos: ActionInfo[]
  onRun: (actionId: string) => void
}

// Actions that get a button of their own on a row. Everything else the server
// offers — including actions added later — lands in the "more" menu, so a new
// backend action shows up here without any change to this file.
const iconFor: Record<string, IconName> = {
  start: 'start',
  restart: 'restart',
  'pull-restart': 'pull',
  stop: 'stop',
}

function quickActions(service: ServiceView): string[] {
  // Start is the thing to do with a service that is down; otherwise it is an
  // occasional action and lives in the menu.
  return service.state === 'stopped' ? ['start', 'restart', 'pull-restart'] : ['restart', 'pull-restart']
}

/** The buttons at the end of a service's row. */
export function ActionButtons({ hostId, service, infos, onRun }: Props) {
  const applicable = infos.filter((info) => service.actions.includes(info.id))
  const quickIds = quickActions(service)
  const quick = quickIds
    .map((id) => applicable.find((info) => info.id === id))
    .filter((info): info is ActionInfo => info !== undefined)
  const rest = applicable.filter((info) => !quickIds.includes(info.id))

  // Clicks here must not also open the service page the row links to.
  const stop = (event: MouseEvent) => event.stopPropagation()

  return (
    <span className="row-actions" onClick={stop}>
      {quick.map((info) => (
        <button
          key={info.id}
          type="button"
          className="btn icon"
          aria-label={info.label}
          title={info.label}
          onClick={() => onRun(info.id)}
        >
          <Icon name={iconFor[info.id] ?? 'more'} />
        </button>
      ))}
      <Link className="btn icon" to={servicePath(hostId, service.key, { tab: 'logs' })} aria-label="Logs" title="Logs">
        <Icon name="logs" />
      </Link>
      <Link className="btn icon" to={servicePath(hostId, service.key, { tab: 'shell' })} aria-label="Shell" title="Shell">
        <Icon name="shell" />
      </Link>
      {rest.length > 0 && <MoreMenu actions={rest} onRun={onRun} />}
    </span>
  )
}

type MoreMenuProps = {
  actions: ActionInfo[]
  onRun: (actionId: string) => void
  /** What the button is called, for screen readers and tooltips. */
  label?: string
}

/** A "…" button that opens a small menu of actions. */
export function MoreMenu({ actions, onRun, label = 'More actions' }: MoreMenuProps) {
  const [open, setOpen] = useState(false)
  // Where to draw the menu, in window coordinates. The table scrolls sideways
  // on narrow screens, and a scrolling box clips whatever hangs out of it, so
  // the menu is placed against the window rather than inside the row.
  const [at, setAt] = useState<{ top: number; right: number }>({ top: 0, right: 0 })
  const wrap = useRef<HTMLSpanElement>(null)

  const toggle = () => {
    const rect = wrap.current?.getBoundingClientRect()
    if (rect) setAt({ top: rect.bottom + 4, right: Math.max(8, window.innerWidth - rect.right) })
    setOpen((v) => !v)
  }

  useEffect(() => {
    if (!open) return
    const close = () => setOpen(false)
    const onPointer = (event: PointerEvent) => {
      if (!wrap.current?.contains(event.target as Node)) close()
    }
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') close()
    }
    document.addEventListener('pointerdown', onPointer)
    document.addEventListener('keydown', onKey)
    // The menu does not follow the page; close it when the page moves.
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('pointerdown', onPointer)
      document.removeEventListener('keydown', onKey)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [open])

  return (
    <span className="menu-wrap" ref={wrap}>
      <button
        type="button"
        className={open ? 'btn icon on' : 'btn icon'}
        aria-label={label}
        title={label}
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={toggle}
      >
        <Icon name="more" />
      </button>
      {open && (
        <div className="menu" role="menu" style={{ top: at.top, right: at.right }}>
          {actions.map((info) => (
            <button
              key={info.id}
              type="button"
              role="menuitem"
              className={info.destructive ? 'danger' : undefined}
              onClick={() => {
                setOpen(false)
                onRun(info.id)
              }}
            >
              {info.label}
            </button>
          ))}
        </div>
      )}
    </span>
  )
}

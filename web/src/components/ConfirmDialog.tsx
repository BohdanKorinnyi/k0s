import { useEffect, useId, useRef, useState, type ReactNode } from 'react'

type Props = {
  title: string
  /** `click`: one click confirms. `type-name`: `expected` must be typed first. */
  mode: 'click' | 'type-name'
  /** The text to type in `type-name` mode: the service's name. */
  expected: string
  confirmLabel: string
  danger?: boolean
  onConfirm: () => void
  onCancel: () => void
  children?: ReactNode
}

/** Asks before an action runs. Escape and a click outside cancel. */
export function ConfirmDialog({ title, mode, expected, confirmLabel, danger, onConfirm, onCancel, children }: Props) {
  const [typed, setTyped] = useState('')
  const titleId = useId()
  const inputId = useId()
  const first = useRef<HTMLInputElement & HTMLButtonElement>(null)
  const allowed = mode === 'click' || typed === expected

  useEffect(() => {
    first.current?.focus()
  }, [])

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (event.key === 'Escape') onCancel()
    }
    document.addEventListener('keydown', onKey)
    return () => document.removeEventListener('keydown', onKey)
  }, [onCancel])

  return (
    <div className="backdrop" onPointerDown={(e) => e.target === e.currentTarget && onCancel()}>
      <div className="dialog" role="dialog" aria-modal="true" aria-labelledby={titleId}>
        <h2 id={titleId}>{title}</h2>
        {children && <p>{children}</p>}
        {mode === 'type-name' && (
          <form
            onSubmit={(e) => {
              e.preventDefault()
              if (allowed) onConfirm()
            }}
          >
            <p>
              <label htmlFor={inputId}>
                This host is protected. Type <code>{expected}</code> to confirm.
              </label>
            </p>
            <input
              id={inputId}
              ref={first}
              className="input mono"
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              autoComplete="off"
              spellCheck={false}
            />
          </form>
        )}
        <div className="dialog-actions">
          {/* In click mode focus starts on Cancel, so a stray Enter is harmless. */}
          <button type="button" className="btn" onClick={onCancel} ref={mode === 'click' ? first : undefined}>
            Cancel
          </button>
          <button
            type="button"
            className={danger ? 'btn danger primary' : 'btn primary'}
            disabled={!allowed}
            onClick={onConfirm}
          >
            {confirmLabel}
          </button>
        </div>
      </div>
    </div>
  )
}

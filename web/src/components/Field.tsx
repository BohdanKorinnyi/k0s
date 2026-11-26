import { cloneElement, useId, type ReactElement, type ReactNode } from 'react'

type ControlProps = {
  id?: string
  'aria-invalid'?: boolean
  'aria-describedby'?: string
  className?: string
}

type Props = {
  label: string
  /** What is wrong with the value, shown in red under the field. */
  error?: string | null
  /** Help shown under the field. */
  hint?: ReactNode
  /** The input, select or textarea. It is given its id and ARIA attributes. */
  children: ReactElement<ControlProps>
}

/**
 * A labelled form control with its hint and error. The hint and error are
 * tied to the control, so a screen reader reads them with it.
 */
export function Field({ label, error, hint, children }: Props) {
  const id = useId()
  const hintId = `${id}-hint`
  const errorId = `${id}-error`
  const describedBy = [error ? errorId : null, hint ? hintId : null].filter(Boolean).join(' ') || undefined

  const control = cloneElement(children, {
    id,
    'aria-invalid': error ? true : undefined,
    'aria-describedby': describedBy,
    className: [children.props.className, error ? 'invalid' : null].filter(Boolean).join(' '),
  })

  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      {control}
      {error && (
        <div className="error" id={errorId}>
          {error}
        </div>
      )}
      {hint && (
        <div className="hint" id={hintId}>
          {hint}
        </div>
      )}
    </div>
  )
}

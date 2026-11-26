import { useEffect, useState } from 'react'
import { api, paths } from '../api/client'
import type { ContainerView, HostView } from '../api/types'
import { Icon } from '../components/Icon'
import type { PanelProps } from './index'

const MASK = '••••••••'

/**
 * A container's environment variables. Names are listed; values are secrets
 * as often as not, so each stays masked, and is not even sent to the browser,
 * until it is asked for.
 */
export function EnvPanel({ host, container }: PanelProps) {
  if (!container) return <div className="panel-message">Choose a container to see its variables.</div>
  // Keyed by container: another container starts with nothing revealed.
  return <EnvFor key={container.id} host={host} container={container} />
}

function EnvFor({ host, container }: { host: HostView; container: ContainerView }) {
  const base = `${paths.container(host.id, container.id)}/env`
  const [names, setNames] = useState<string[] | null>(null)
  const [revealed, setRevealed] = useState<Record<string, string>>({})
  const [error, setError] = useState<string | null>(null)
  const [filter, setFilter] = useState('')
  const [copied, setCopied] = useState<string | null>(null)

  useEffect(() => {
    let cancelled = false
    api
      .get<{ names: string[] }>(base)
      .then((res) => {
        if (!cancelled) setNames(res.names)
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      cancelled = true
    }
  }, [base])

  const reveal = (only?: string) => {
    setError(null)
    api
      .post<{ values: Record<string, string> }>(`${base}/reveal`, only === undefined ? {} : { names: [only] })
      .then((res) => setRevealed((prev) => ({ ...prev, ...res.values })))
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }
  const hide = (name: string) =>
    setRevealed((prev) => {
      const next = { ...prev }
      delete next[name]
      return next
    })

  const copy = (name: string, value: string) => {
    void navigator.clipboard?.writeText(value).then(() => {
      setCopied(name)
      window.setTimeout(() => setCopied((c) => (c === name ? null : c)), 1500)
    })
  }

  if (error && !names) {
    return (
      <div className="panel-message" role="alert">
        Could not read the variables: {error}
      </div>
    )
  }
  if (!names) return <div className="panel-message">Loading…</div>

  const needle = filter.trim().toLowerCase()
  const shown = needle ? names.filter((n) => n.toLowerCase().includes(needle)) : names
  const anyRevealed = Object.keys(revealed).length > 0

  return (
    <div className="panel">
      <div className="panel-toolbar">
        <input
          type="search"
          className="input"
          style={{ width: 220 }}
          placeholder="Filter by name…"
          aria-label="Filter variables"
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
        />
        <span className="muted">
          {shown.length === names.length ? `${names.length} variables` : `${shown.length} of ${names.length}`}
        </span>
        <span className="grow" />
        {error && (
          <span className="muted" role="alert">
            {error}
          </span>
        )}
        {anyRevealed ? (
          <button type="button" className="btn small" onClick={() => setRevealed({})}>
            Hide all
          </button>
        ) : (
          <button type="button" className="btn small" onClick={() => reveal()} disabled={names.length === 0}>
            Reveal all
          </button>
        )}
      </div>
      <div className="panel-scroll">
        {names.length === 0 ? (
          <div className="panel-message">This container has no environment variables.</div>
        ) : (
          <table className="kv env">
            <tbody>
              {shown.map((name) => {
                const shownValue = name in revealed
                const value = revealed[name] ?? ''
                return (
                  <tr key={name}>
                    <th scope="row">{name}</th>
                    <td className="value">
                      {!shownValue ? (
                        <span className="masked">{MASK}</span>
                      ) : value === '' ? (
                        <span className="muted">(empty)</span>
                      ) : (
                        value
                      )}
                    </td>
                    <td className="tools">
                      {shownValue && value !== '' && (
                        <button
                          type="button"
                          className="btn icon quiet"
                          aria-label={`Copy ${name}`}
                          title={copied === name ? 'Copied' : 'Copy the value'}
                          onClick={() => copy(name, value)}
                        >
                          <Icon name={copied === name ? 'check' : 'copy'} />
                        </button>
                      )}
                      <button
                        type="button"
                        className={shownValue ? 'btn icon on' : 'btn icon quiet'}
                        aria-label={shownValue ? `Hide ${name}` : `Reveal ${name}`}
                        title={shownValue ? 'Hide the value' : 'Reveal the value'}
                        onClick={() => (shownValue ? hide(name) : reveal(name))}
                      >
                        <Icon name="eye" />
                      </button>
                    </td>
                  </tr>
                )
              })}
            </tbody>
          </table>
        )}
      </div>
    </div>
  )
}

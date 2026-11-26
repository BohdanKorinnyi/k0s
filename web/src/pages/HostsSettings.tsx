import { useState } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api, paths } from '../api/client'
import type { HostView } from '../api/types'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { HostForm } from '../components/HostForm'
import { Icon } from '../components/Icon'
import { SettingsLayout } from '../components/SettingsLayout'
import { useHosts } from '../hooks/useHosts'
import { hostPath } from '../lib/serviceUrl'

/** The saved hosts: add, edit, test and remove them. */
export function HostsSettings() {
  const { hosts, loading, error, statuses, reload } = useHosts()
  const [search, setSearch] = useSearchParams()
  // Which form is open: a host's ID, "new", or none.
  const [editing, setEditing] = useState<string | null>(search.has('new') ? 'new' : null)
  const [removing, setRemoving] = useState<HostView | null>(null)
  const [removeError, setRemoveError] = useState<string | null>(null)

  // With no hosts there is nothing to do here but add one.
  const formFor = editing ?? (!loading && !error && hosts.length === 0 ? 'new' : null)
  const edited = hosts.find((h) => h.id === formFor)

  const close = () => {
    setEditing(null)
    if (search.has('new')) setSearch({}, { replace: true })
  }

  async function remove(host: HostView) {
    setRemoving(null)
    setRemoveError(null)
    try {
      await api.del(paths.host(host.id))
      await reload()
    } catch (e) {
      setRemoveError(e instanceof Error ? e.message : String(e))
    }
  }

  return (
    <SettingsLayout title="Hosts">
      <div className="page-head">
        <h1 className="page-title">Hosts</h1>
        <span className="grow" />
        {formFor === null && (
          <button type="button" className="btn primary" onClick={() => setEditing('new')}>
            <Icon name="plus" /> Add a host
          </button>
        )}
      </div>

      {error && (
        <div className="notice error" role="alert" style={{ marginBottom: 14 }}>
          <h2>Could not load the hosts</h2>
          <div className="detail">{error}</div>
          <button type="button" className="btn" onClick={() => void reload()}>
            Retry
          </button>
        </div>
      )}
      {removeError && (
        <div className="notice error" role="alert" style={{ marginBottom: 14 }}>
          {removeError}
        </div>
      )}

      {formFor !== null && (
        <section className="card" style={{ marginBottom: 14 }} aria-label={edited ? `Edit ${edited.name}` : 'Add a host'}>
          <HostForm
            key={formFor}
            initial={edited}
            onCancel={close}
            onSaved={() => {
              close()
              void reload()
            }}
          />
        </section>
      )}

      {hosts.length > 0 && (
        <ul className="card" style={{ listStyle: 'none', margin: 0, padding: 0 }} aria-label="Hosts">
          {hosts.map((host) => {
            const status = statuses[host.id]
            return (
              <li key={host.id} className="list-row" aria-label={host.name}>
                <div style={{ minWidth: 0, flex: 1 }}>
                  <div className="title">
                    <Link to={hostPath(host.id)}>{host.name}</Link>
                    {host.protected && (
                      <span className="badge unknown" title="Every action asks for the service name to be typed">
                        protected
                      </span>
                    )}
                  </div>
                  <div className="sub mono">{host.url}</div>
                  <div className="sub">
                    {status === undefined ? 'Checking…' : status.ok ? `Reachable · Docker ${status.version}` : `Unreachable · ${status.error}`}
                  </div>
                </div>
                <button type="button" className="btn small" onClick={() => setEditing(host.id)}>
                  Edit
                </button>
                <button type="button" className="btn small danger" onClick={() => setRemoving(host)}>
                  Remove
                </button>
              </li>
            )
          })}
        </ul>
      )}

      {removing && (
        <ConfirmDialog
          title={`Remove ${removing.name}?`}
          mode="click"
          expected=""
          confirmLabel="Remove"
          danger
          onCancel={() => setRemoving(null)}
          onConfirm={() => void remove(removing)}
        >
          Only the panel’s entry is removed. Nothing changes on the server itself.
        </ConfirmDialog>
      )}
    </SettingsLayout>
  )
}

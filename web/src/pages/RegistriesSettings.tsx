import { useCallback, useEffect, useState } from 'react'
import { api } from '../api/client'
import type { RegistryView } from '../api/types'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { Icon } from '../components/Icon'
import { RegistryForm } from '../components/RegistryForm'
import { SettingsLayout } from '../components/SettingsLayout'

/** Logins for image registries, used for pulls and update checks. */
export function RegistriesSettings() {
  const [logins, setLogins] = useState<RegistryView[] | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<string | null>(null)
  const [removing, setRemoving] = useState<RegistryView | null>(null)

  const reload = useCallback(async () => {
    try {
      setLogins(await api.get<RegistryView[]>('/api/registries'))
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  async function remove(login: RegistryView) {
    setRemoving(null)
    try {
      await api.del(`/api/registries/${encodeURIComponent(login.id)}`)
      await reload()
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  const edited = logins?.find((l) => l.id === editing)

  return (
    <SettingsLayout title="Registry logins">
      <div className="page-head">
        <h1 className="page-title">Registry logins</h1>
        <span className="grow" />
        {editing === null && (
          <button type="button" className="btn primary" onClick={() => setEditing('new')}>
            <Icon name="plus" /> Add a login
          </button>
        )}
      </div>

      {error && (
        <div className="notice error" role="alert" style={{ marginBottom: 14 }}>
          <div className="detail">{error}</div>
          <button type="button" className="btn" onClick={() => void reload()}>
            Retry
          </button>
        </div>
      )}

      {editing !== null && (
        <section className="card" style={{ marginBottom: 14 }} aria-label={edited ? `Edit ${edited.address}` : 'Add a login'}>
          <RegistryForm
            key={editing}
            initial={edited}
            onCancel={() => setEditing(null)}
            onSaved={() => {
              setEditing(null)
              void reload()
            }}
          />
        </section>
      )}

      {logins && logins.length === 0 && editing === null && (
        <div className="card empty">
          No registry logins yet. They are needed to pull images from private registries, and to check those images
          for updates. Public images need none.
        </div>
      )}

      {logins && logins.length > 0 && (
        <ul className="card" style={{ listStyle: 'none', margin: 0, padding: 0 }} aria-label="Registry logins">
          {logins.map((login) => (
            <li key={login.id} className="list-row" aria-label={login.address}>
              <div style={{ minWidth: 0, flex: 1 }}>
                <div className="title mono">{login.address}</div>
                <div className="sub">
                  {login.username || 'no username'} · {login.hasPassword ? 'password saved' : 'no password'}
                </div>
              </div>
              <button type="button" className="btn small" onClick={() => setEditing(login.id)}>
                Edit
              </button>
              <button type="button" className="btn small danger" onClick={() => setRemoving(login)}>
                Remove
              </button>
            </li>
          ))}
        </ul>
      )}

      {removing && (
        <ConfirmDialog
          title={`Remove the login for ${removing.address}?`}
          mode="click"
          expected=""
          confirmLabel="Remove"
          danger
          onCancel={() => setRemoving(null)}
          onConfirm={() => void remove(removing)}
        >
          Pulls and update checks for private images on this registry will stop working.
        </ConfirmDialog>
      )}
    </SettingsLayout>
  )
}

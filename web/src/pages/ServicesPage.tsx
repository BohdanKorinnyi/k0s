import { useEffect, useMemo, useState } from 'react'
import { Link, useParams } from 'react-router'
import { APP_NAME } from '../appinfo'
import { Layout } from '../components/Layout'
import { ServicesTable } from '../components/ServicesTable'
import { useActions } from '../hooks/useActions'
import { useHosts } from '../hooks/useHosts'
import { useServices } from '../hooks/useServices'
import { useStoredFlag } from '../hooks/useStoredFlag'
import { countServices, filterGroups } from '../lib/filterServices'
import { relativeTime } from '../lib/format'

/** The services of one host. */
export function ServicesPage() {
  const { hostId = '' } = useParams()
  const { hosts, loading: hostsLoading } = useHosts()
  const host = hosts.find((h) => h.id === hostId)
  const actions = useActions()
  const services = useServices(host?.id, actions.version)

  const [query, setQuery] = useState('')
  const [hideStopped, setHideStopped] = useStoredFlag('hideStopped')
  const now = services.loadedAt

  useEffect(() => {
    document.title = host ? `${host.name} · ${APP_NAME}` : APP_NAME
  }, [host])

  const groups = services.data?.groups
  const shown = useMemo(() => (groups ? filterGroups(groups, query, hideStopped) : []), [groups, query, hideStopped])

  if (!host) {
    return (
      <Layout currentHostId={hostId}>
        {hostsLoading ? (
          <div className="empty">Loading…</div>
        ) : (
          <div className="notice">
            <h2>There is no host with the ID “{hostId}”.</h2>
            <p className="muted">
              It may have been removed. See the <Link to="/settings/hosts">list of hosts</Link>.
            </p>
          </div>
        )}
      </Layout>
    )
  }

  const checkedAt = services.data?.updatesCheckedAt
  return (
    <Layout currentHostId={host.id}>
      <div className="toolbar">
        <input
          type="search"
          className="input"
          style={{ width: 240 }}
          placeholder="Filter services…"
          aria-label="Filter services"
          value={query}
          onChange={(e) => setQuery(e.target.value)}
        />
        <label className="check">
          <input type="checkbox" checked={hideStopped} onChange={(e) => setHideStopped(e.target.checked)} />
          Hide stopped
        </label>
        <span className="grow" />
        <span className="muted">{checkedAt ? `Updates checked ${relativeTime(checkedAt, now)}` : 'Updates not checked yet'}</span>
        <button type="button" className="btn" onClick={services.checkNow} disabled={services.checking || !services.data}>
          {services.checking ? 'Checking…' : 'Check now'}
        </button>
      </div>

      {services.error ? (
        <div className="notice error" role="alert">
          <h2>Cannot list the services on {host.name}</h2>
          <div className="detail">{services.error}</div>
          <button type="button" className="btn" onClick={services.reload}>
            Retry
          </button>
        </div>
      ) : !groups ? (
        <div className="empty">Loading…</div>
      ) : countServices(groups) === 0 ? (
        <div className="card empty">There are no containers on this host.</div>
      ) : shown.length === 0 ? (
        <div className="card empty">
          No services match{query.trim() ? ` “${query.trim()}”` : ''}
          {hideStopped ? ' among those running' : ''}.
        </div>
      ) : (
        <ServicesTable
          host={host}
          groups={shown}
          infos={actions.infos}
          now={now}
          onAction={(service, actionId) => actions.run(host, service, actionId)}
        />
      )}
    </Layout>
  )
}

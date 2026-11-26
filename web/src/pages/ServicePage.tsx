import { useEffect } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import type { ActionInfo, ContainerView } from '../api/types'
import { APP_NAME } from '../appinfo'
import { ContainerList } from '../components/ContainerList'
import { Icon, type IconName } from '../components/Icon'
import { Layout } from '../components/Layout'
import { ImageRef } from '../components/ServicesTable'
import { StatusDot, toneForState } from '../components/StatusDot'
import { UpdateBadge } from '../components/UpdateBadge'
import { useActions } from '../hooks/useActions'
import { useHosts } from '../hooks/useHosts'
import { useService } from '../hooks/useService'
import { statusLabel } from '../lib/format'
import { DEFAULT_TAB, hostPath, servicePath } from '../lib/serviceUrl'
import { panels } from '../panels'

const headerIcons: Record<string, IconName> = {
  restart: 'restart',
  start: 'start',
  stop: 'stop',
  'pull-restart': 'pull',
}

/**
 * One service, on a page of its own. The address holds the whole view —
 * host, service, tab and container — so it can be bookmarked or shared.
 */
export function ServicePage() {
  const { hostId = '', key = '' } = useParams()
  const [search] = useSearchParams()
  const navigate = useNavigate()
  const { hosts, loading: hostsLoading } = useHosts()
  const host = hosts.find((h) => h.id === hostId)
  const actions = useActions()
  const state = useService(host?.id, key, actions.version)
  const service = state.data

  useEffect(() => {
    document.title = service && host ? `${service.name} · ${host.name} · ${APP_NAME}` : APP_NAME
  }, [service, host])

  if (!host) {
    return (
      <Layout currentHostId={hostId}>
        {hostsLoading ? (
          <div className="empty">Loading…</div>
        ) : (
          <div className="notice">
            <h2>There is no host with the ID “{hostId}”.</h2>
            <p className="muted">
              See the <Link to="/settings/hosts">list of hosts</Link>.
            </p>
          </div>
        )}
      </Layout>
    )
  }

  if (state.error?.notFound) {
    return (
      <Layout currentHostId={host.id}>
        <div className="notice">
          <h2>This service is no longer on this host.</h2>
          <p className="muted">
            It may have been removed or renamed. <Link to={hostPath(host.id)}>Back to {host.name}</Link>
          </p>
        </div>
      </Layout>
    )
  }
  if (state.error) {
    return (
      <Layout currentHostId={host.id}>
        <div className="notice error" role="alert">
          <h2>Cannot read this service on {host.name}</h2>
          <div className="detail">{state.error.message}</div>
          <button type="button" className="btn" onClick={state.reload}>
            Retry
          </button>
        </div>
      </Layout>
    )
  }
  if (!service) {
    return (
      <Layout currentHostId={host.id}>
        <div className="empty">Loading…</div>
      </Layout>
    )
  }

  // The tab and container come from the address; anything unknown falls back.
  const panel = panels.find((p) => p.id === search.get('tab')) ?? panels.find((p) => p.id === DEFAULT_TAB)!
  const chosenName = search.get('c')
  const chosen = service.containers.find((c) => c.name === chosenName) ?? null
  const allowAll = Boolean(panel.supportsAll) && service.containers.length > 1
  // No container chosen: a tab that can show all does; any other tab is about
  // one container, and takes the first.
  const container: ContainerView | null = chosen ?? (allowAll ? null : (service.containers[0] ?? null))

  const go = (tab: string, containerName: string | null) =>
    navigate(servicePath(host.id, service.key, { tab, container: containerName }), { replace: true })

  const headerActions = actions.infos.filter((info) => service.actions.includes(info.id))
  const Panel = panel.component

  return (
    <Layout currentHostId={host.id} fill>
      {state.stale && (
        <div className="notice warn" role="status" style={{ marginBottom: 10, display: 'flex', alignItems: 'center', gap: 10 }}>
          <span className="grow">
            Could not refresh: {state.stale}. Showing what was last known.
          </span>
          <button type="button" className="btn small" onClick={state.reload}>
            Retry
          </button>
        </div>
      )}
      <div className="page-head">
        <div className="crumbs">
          <Link to={hostPath(host.id)} aria-label={`Back to ${host.name}`} className="btn icon quiet" title={`Back to ${host.name}`}>
            <Icon name="back" />
          </Link>
          <span>{host.name}</span>
          {service.project && (
            <>
              <span>/</span>
              <span>{service.project}</span>
            </>
          )}
          <span>/</span>
          <h1 className="here">{service.name}</h1>
        </div>
        <span>
          <ImageRef service={service} />
          <UpdateBadge update={service.update} />
        </span>
        <span className="status muted">
          <StatusDot tone={toneForState(service.state)} label={service.state} />
          {statusLabel(service, new Date())}
        </span>
        <span className="grow" />
        {headerActions.map((info) => (
          <HeaderAction key={info.id} info={info} onRun={() => actions.run(host, service, info.id)} />
        ))}
      </div>

      <div className="service-body">
        <ContainerList
          service={service}
          infos={actions.infos}
          allowAll={allowAll}
          selected={container ? container.name : null}
          onSelect={(name) => go(panel.id, name)}
          onAction={(containerId, actionId) => actions.run(host, service, actionId, containerId)}
        />
        <section className="card panel-area">
          <div className="tabs" role="tablist">
            {panels.map((p) => (
              <button
                key={p.id}
                type="button"
                role="tab"
                className="tab"
                aria-selected={p.id === panel.id}
                onClick={() => go(p.id, chosen ? chosen.name : null)}
              >
                {p.title}
              </button>
            ))}
          </div>
          <div role="tabpanel" className="panel" aria-label={panel.title}>
            {service.containers.length === 0 ? (
              <div className="panel-message">This service has no containers.</div>
            ) : (
              <Panel host={host} service={service} container={container} />
            )}
          </div>
        </section>
      </div>
    </Layout>
  )
}

function HeaderAction({ info, onRun }: { info: ActionInfo; onRun: () => void }) {
  const icon = headerIcons[info.id]
  return (
    <button type="button" className={info.id === 'stop' ? 'btn danger' : 'btn'} onClick={onRun}>
      {icon && <Icon name={icon} />}
      {info.label}
    </button>
  )
}

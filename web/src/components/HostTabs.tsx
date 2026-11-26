import { Link } from 'react-router'
import type { ConnectionResult, HostView } from '../api/types'
import { hostPath } from '../lib/serviceUrl'
import { Icon } from './Icon'
import { StatusDot } from './StatusDot'

export type HostStatuses = Record<string, ConnectionResult | undefined>

type Props = {
  hosts: HostView[]
  /** The host whose page is open, if any. */
  currentHostId: string | null
  /** Whether each host answers; absent while not yet known. */
  statuses: HostStatuses
}

/** The hosts along the top bar: one click switches host. */
export function HostTabs({ hosts, currentHostId, statuses }: Props) {
  return (
    <nav className="host-tabs" aria-label="Hosts">
      {hosts.map((host) => {
        const status = statuses[host.id]
        const title = status
          ? status.ok
            ? `${host.url} · Docker ${status.version}`
            : `${host.url} · ${status.error}`
          : host.url
        return (
          <Link
            key={host.id}
            to={hostPath(host.id)}
            className="host-tab"
            aria-current={host.id === currentHostId ? 'page' : undefined}
            title={title}
          >
            {status === undefined ? (
              <StatusDot tone="idle" label="checking" />
            ) : status.ok ? (
              <StatusDot tone="ok" label="reachable" />
            ) : (
              <StatusDot tone="bad" label="unreachable" />
            )}
            {host.name}
            {host.protected && (
              <span className="lock" role="img" aria-label="protected">
                <Icon name="lock" size={14} />
              </span>
            )}
          </Link>
        )
      })}
      <Link to="/settings/hosts?new=1" className="host-tab add" aria-label="Add a host" title="Add a host">
        +
      </Link>
    </nav>
  )
}

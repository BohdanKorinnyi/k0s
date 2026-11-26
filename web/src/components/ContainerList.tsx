import type { ActionInfo, ContainerView, ServiceView } from '../api/types'
import { MoreMenu } from './ActionButtons'
import { StatusDot, toneForState } from './StatusDot'

type Props = {
  service: ServiceView
  infos: ActionInfo[]
  /** Whether the open tab can show every container at once. */
  allowAll: boolean
  /** The chosen container's name, or null for "All together". */
  selected: string | null
  onSelect: (containerName: string | null) => void
  onAction: (containerId: string, actionId: string) => void
}

/** The containers of a service, down the left of its page. */
export function ContainerList({ service, infos, allowAll, selected, onSelect, onAction }: Props) {
  const showAll = allowAll && service.containers.length > 1
  return (
    <aside className="card container-list">
      <h2>Containers ({service.containers.length})</h2>
      {showAll && (
        <div className="container-row">
          <button
            type="button"
            className="container-item"
            aria-current={selected === null ? 'true' : undefined}
            onClick={() => onSelect(null)}
          >
            <span className="label">All together</span>
          </button>
        </div>
      )}
      {service.containers.map((c) => (
        <div className="container-row" key={c.id}>
          <span style={{ padding: '0 2px 0 8px', display: 'inline-flex' }}>
            <StatusDot tone={toneForState(c.state, c.health)} label={c.health === 'unhealthy' ? 'unhealthy' : c.state} />
          </span>
          <button
            type="button"
            className="container-item mono"
            aria-current={selected === c.name ? 'true' : undefined}
            title={`${c.name} · ${c.state}${c.health ? ` (${c.health})` : ''}`}
            onClick={() => onSelect(c.name)}
          >
            <span className="label">{c.name}</span>
          </button>
          <MoreMenu
            label={`Actions for ${c.name}`}
            actions={containerActions(infos, c)}
            onRun={(actionId) => onAction(c.id, actionId)}
          />
        </div>
      ))}
    </aside>
  )
}

/**
 * The actions that can be applied to one container as it is now. Start and
 * stop depend on whether it is up; any other container-level action the
 * server offers is always listed.
 */
function containerActions(infos: ActionInfo[], c: ContainerView): ActionInfo[] {
  const up = c.state === 'running' || c.state === 'restarting' || c.state === 'paused'
  return infos.filter((info) => {
    if (!info.containerLevel) return false
    if (info.id === 'start') return !up
    if (info.id === 'stop') return up
    return true
  })
}

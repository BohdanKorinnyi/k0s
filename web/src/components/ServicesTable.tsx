import { Link, useNavigate } from 'react-router'
import type { ActionInfo, HostView, ServiceGroup, ServiceView } from '../api/types'
import { splitImage, statusLabel } from '../lib/format'
import { servicePath } from '../lib/serviceUrl'
import { ActionButtons } from './ActionButtons'
import { StatusDot, toneForState } from './StatusDot'
import { UpdateBadge } from './UpdateBadge'

type Props = {
  host: HostView
  groups: ServiceGroup[]
  infos: ActionInfo[]
  /** The current time, for "up 3d". Passed in so it can be fixed in tests. */
  now: Date
  onAction: (service: ServiceView, actionId: string) => void
}

/** The services of one host, grouped by Compose project. */
export function ServicesTable({ host, groups, infos, now, onAction }: Props) {
  const navigate = useNavigate()
  return (
    <div className="card table-wrap">
      <table className="services">
        <thead>
          <tr>
            <th>Service</th>
            <th className="hide-narrow">Containers</th>
            <th>Count</th>
            <th>Image</th>
            <th>Status</th>
            <th>
              <span className="visually-hidden">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => (
            <ProjectRows key={group.project}>
              <tr className="group">
                <td colSpan={6}>{group.project || 'No project'}</td>
              </tr>
              {group.services.map((service) => (
                <tr
                  key={service.key}
                  className={service.state === 'stopped' ? 'service stopped' : 'service'}
                  onClick={() => navigate(servicePath(host.id, service.key))}
                >
                  <td className="cell-name">
                    <Link to={servicePath(host.id, service.key)} onClick={(e) => e.stopPropagation()}>
                      {service.name}
                    </Link>
                  </td>
                  <td className="cell-containers mono hide-narrow">
                    {service.containers.map((c) => (
                      <span key={c.id}>{c.name}</span>
                    ))}
                  </td>
                  <td className="cell-count">
                    {service.running}/{service.total}
                  </td>
                  <td className="cell-image">
                    <ImageRef service={service} />
                    <UpdateBadge update={service.update} />
                  </td>
                  <td className="cell-status">
                    <span className="status">
                      <StatusDot tone={toneForState(service.state)} label={service.state} />
                      {statusLabel(service, now)}
                    </span>
                  </td>
                  <td className="cell-actions">
                    <ActionButtons
                      hostId={host.id}
                      service={service}
                      infos={infos}
                      onRun={(actionId) => onAction(service, actionId)}
                    />
                  </td>
                </tr>
              ))}
            </ProjectRows>
          ))}
        </tbody>
      </table>
    </div>
  )
}

// A keyed wrapper for one project's heading and rows.
function ProjectRows({ children }: { children: React.ReactNode }) {
  return <>{children}</>
}

/** An image reference with its tag set apart, or "mixed" when containers differ. */
export function ImageRef({ service }: { service: Pick<ServiceView, 'image' | 'images'> }) {
  if (service.image === '') {
    return (
      <span className="muted" title={service.images.join('\n')}>
        mixed
      </span>
    )
  }
  const { name, tag } = splitImage(service.image)
  return (
    <span className="image mono" title={service.image}>
      <span className="name">{name}</span>
      {tag && <span className="tag">{tag.startsWith('@') ? tag : ':' + tag}</span>}
    </span>
  )
}

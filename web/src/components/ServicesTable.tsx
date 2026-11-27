import { Fragment, useState } from 'react'
import { Link, useNavigate } from 'react-router'
import type { ActionInfo, HostView, ServiceGroup, ServiceView } from '../api/types'
import { splitImage, statusLabel } from '../lib/format'
import { servicePath } from '../lib/serviceUrl'
import type { Sort, SortBy } from '../lib/sortServices'
import { ActionButtons } from './ActionButtons'
import { Icon } from './Icon'
import { InlineLogs } from './InlineLogs'
import { StatusDot, toneForState } from './StatusDot'
import { UpdateBadge } from './UpdateBadge'

type Props = {
  host: HostView
  /** Already filtered and sorted; shown in the order given. */
  groups: ServiceGroup[]
  infos: ActionInfo[]
  /** The current time, for "up 3d". Passed in so it can be fixed in tests. */
  now: Date
  onAction: (service: ServiceView, actionId: string) => void
  /** How the list is sorted, to mark the heading in use. */
  sort: Sort
  /** A sortable heading was clicked. */
  onSort: (by: SortBy) => void
}

const COLUMNS = 7

/** The services of one host, grouped by Compose project. */
export function ServicesTable({ host, groups, infos, now, onAction, sort, onSort }: Props) {
  const navigate = useNavigate()
  // The services whose log is open under their row.
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set())
  const toggle = (key: string) =>
    setOpen((prev) => {
      const next = new Set(prev)
      if (!next.delete(key)) next.add(key)
      return next
    })

  return (
    <div className="card table-wrap">
      <table className="services">
        <thead>
          <tr>
            <th className="cell-toggle">
              <span className="visually-hidden">Logs</span>
            </th>
            <SortHeading by="name" sort={sort} onSort={onSort}>
              Service
            </SortHeading>
            <th className="hide-narrow">Containers</th>
            <th>Count</th>
            <th>Image</th>
            <SortHeading by="status" sort={sort} onSort={onSort}>
              Status
            </SortHeading>
            <th>
              <span className="visually-hidden">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {groups.map((group) => (
            <Fragment key={group.project}>
              <tr className="group">
                <td colSpan={COLUMNS}>{group.project || 'No project'}</td>
              </tr>
              {group.services.map((service) => {
                const isOpen = open.has(service.key)
                return (
                  <Fragment key={service.key}>
                    <tr
                      className={[
                        'service',
                        service.state === 'stopped' ? 'stopped' : '',
                        isOpen ? 'open' : '',
                      ]
                        .filter(Boolean)
                        .join(' ')}
                      onClick={() => navigate(servicePath(host.id, service.key))}
                    >
                      <td className="cell-toggle">
                        <button
                          type="button"
                          className="btn icon quiet toggle"
                          aria-expanded={isOpen}
                          aria-label={`${isOpen ? 'Hide' : 'Show'} logs of ${service.name}`}
                          title={isOpen ? 'Hide the log' : 'Show the log here'}
                          onClick={(event) => {
                            // The row itself opens the service's page; this must not.
                            event.stopPropagation()
                            toggle(service.key)
                          }}
                        >
                          <Icon name="chevron" />
                        </button>
                      </td>
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
                    {isOpen && (
                      <tr className="service-logs">
                        <td colSpan={COLUMNS}>
                          <InlineLogs host={host} service={service} />
                        </td>
                      </tr>
                    )}
                  </Fragment>
                )
              })}
            </Fragment>
          ))}
        </tbody>
      </table>
    </div>
  )
}

type SortHeadingProps = { by: SortBy; sort: Sort; onSort: (by: SortBy) => void; children: string }

/** A column heading that sorts the list, and shows when it is the one in use. */
function SortHeading({ by, sort, onSort, children }: SortHeadingProps) {
  const active = sort.by === by
  return (
    <th aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}>
      <button
        type="button"
        className="th-sort"
        title={active ? 'Reverse the order' : `Sort by ${children.toLowerCase()}`}
        onClick={() => onSort(by)}
      >
        {children}
        <span className="sort-mark" aria-hidden="true">
          <Icon name={active && sort.dir === 'desc' ? 'down' : 'up'} size={12} />
        </span>
      </button>
    </th>
  )
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

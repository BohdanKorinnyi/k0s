import { useEffect, useState, type ReactNode } from 'react'
import { api, paths } from '../api/client'
import type { ContainerInfo } from '../api/types'
import { formatDateTime } from '../lib/format'
import type { PanelProps } from './index'

/** What a container is made of: image, state, command, ports, mounts, networks. */
export function InfoPanel({ host, container }: PanelProps) {
  const [info, setInfo] = useState<ContainerInfo | null>(null)
  const [error, setError] = useState<string | null>(null)
  const id = container?.id

  useEffect(() => {
    if (!id) return
    let cancelled = false
    setInfo(null)
    setError(null)
    api
      .get<ContainerInfo>(`${paths.container(host.id, id)}/info`)
      .then((res) => {
        if (!cancelled) setInfo(res)
      })
      .catch((e: unknown) => {
        if (!cancelled) setError(e instanceof Error ? e.message : String(e))
      })
    return () => {
      cancelled = true
    }
  }, [host.id, id])

  if (!container) return <div className="panel-message">Choose a container to see its details.</div>
  if (error) {
    return (
      <div className="panel-message" role="alert">
        Could not read the container’s details: {error}
      </div>
    )
  }
  if (!info) return <div className="panel-message">Loading…</div>

  const labels = Object.entries(info.labels).sort(([a], [b]) => a.localeCompare(b))
  return (
    <div className="panel">
      <div className="panel-scroll info-sections">
        <Section title="Image">
          <Row name="Reference">{info.image}</Row>
          <Row name="Image ID">{info.imageId}</Row>
          {info.digests.map((digest) => (
            <Row key={digest} name="Digest">
              {digest}
            </Row>
          ))}
        </Section>

        <Section title="State">
          <Row name="State">{info.health ? `${info.state} (${info.health})` : info.state}</Row>
          {info.state !== 'running' && <Row name="Exit code">{String(info.exitCode)}</Row>}
          <Row name="Created">{formatDateTime(info.created)}</Row>
          {info.startedAt && <Row name="Started">{formatDateTime(info.startedAt)}</Row>}
          {info.finishedAt && <Row name="Finished">{formatDateTime(info.finishedAt)}</Row>}
          <Row name="Restart policy">{info.restartPolicy || 'no'}</Row>
          <Row name="Restarts">{String(info.restarts)}</Row>
          <Row name="Container ID">{info.id}</Row>
        </Section>

        {info.command.length > 0 && (
          <Section title="Command">
            <Row name="Runs">{info.command.join(' ')}</Row>
          </Section>
        )}

        {info.ports.length > 0 && (
          <Section title="Ports">
            {info.ports.map((p, i) => (
              <Row key={i} name={p.container}>
                {p.host || <span className="muted">not published</span>}
              </Row>
            ))}
          </Section>
        )}

        {info.mounts.length > 0 && (
          <Section title="Mounts">
            {info.mounts.map((m, i) => (
              <Row key={i} name={m.destination}>
                {m.source}
                <span className="muted">
                  {' '}
                  · {m.type}
                  {m.readOnly ? ' · read-only' : ''}
                </span>
              </Row>
            ))}
          </Section>
        )}

        {info.networks.length > 0 && (
          <Section title="Networks">
            {info.networks.map((n) => (
              <Row key={n.name} name={n.name}>
                {n.ip || <span className="muted">no address</span>}
                {n.aliases.length > 0 && <span className="muted"> · also known as {n.aliases.join(', ')}</span>}
              </Row>
            ))}
          </Section>
        )}

        {info.compose.project && (
          <Section title="Compose">
            <Row name="Project">{info.compose.project}</Row>
            <Row name="Service">{info.compose.service}</Row>
            {info.compose.workingDir && <Row name="Working directory">{info.compose.workingDir}</Row>}
          </Section>
        )}

        {labels.length > 0 && (
          <details className="labels">
            <summary>Labels ({labels.length})</summary>
            <table className="kv">
              <tbody>
                {labels.map(([name, value]) => (
                  <Row key={name} name={name}>
                    {value}
                  </Row>
                ))}
              </tbody>
            </table>
          </details>
        )}
      </div>
    </div>
  )
}

function Section({ title, children }: { title: string; children: ReactNode }) {
  return (
    <>
      <h3>{title}</h3>
      <table className="kv">
        <tbody>{children}</tbody>
      </table>
    </>
  )
}

function Row({ name, children }: { name: string; children: ReactNode }) {
  return (
    <tr>
      <th scope="row">{name}</th>
      <td className="value">{children}</td>
    </tr>
  )
}

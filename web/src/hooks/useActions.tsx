import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { api, paths, streamAction } from '../api/client'
import type { ActionEvent, ActionInfo, HostView, ServiceView } from '../api/types'
import { ActionProgress, type Operation } from '../components/ActionProgress'
import { ConfirmDialog } from '../components/ConfirmDialog'
import { confirmationFor, type Confirmation } from '../lib/confirm'

type ActionsState = {
  /** Every action the server offers. */
  infos: ActionInfo[]
  /**
   * Runs an action on a service, or on one of its containers, after whatever
   * confirmation the host and the action call for.
   */
  run: (host: HostView, service: ServiceView, actionId: string, containerId?: string) => void
  /** Goes up each time an action finishes; pages reload when it changes. */
  version: number
}

const ActionsContext = createContext<ActionsState | null>(null)

type Request = {
  host: HostView
  service: ServiceView
  info: ActionInfo
  containerId?: string
  /** What the action is applied to, for titles: the service or one container. */
  target: string
}

// What each built-in action does, for the confirmation dialog. An action
// without an entry gets a generic sentence.
const explanations: Record<string, (r: Request) => string> = {
  stop: (r) => (r.containerId ? 'The container will be stopped.' : `Its ${count(r.service.total, 'container')} will be stopped.`),
  restart: (r) => (r.containerId ? 'The container will be restarted.' : `Its ${count(r.service.total, 'container')} will be restarted, one at a time.`),
  start: () => 'Stopped containers will be started.',
  'pull-restart': (r) =>
    `The newest image for the tag is pulled, then ${r.service.total === 1 ? 'the container is' : 'the containers are'} replaced, one at a time, keeping their settings. If a new container fails to come up, the previous one is put back.`,
}

function count(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? '' : 's'}`
}

/**
 * Holds what is shared by every page that can run actions: the list of
 * actions, the confirmation dialog, and the progress of what is running.
 */
export function ActionsProvider({ children }: { children: ReactNode }) {
  const [infos, setInfos] = useState<ActionInfo[]>([])
  const [operations, setOperations] = useState<Operation[]>([])
  const [asking, setAsking] = useState<(Request & { mode: Exclude<Confirmation, 'none'> }) | null>(null)
  const [version, setVersion] = useState(0)
  const nextId = useRef(1)

  useEffect(() => {
    let cancelled = false
    api
      .get<ActionInfo[]>('/api/actions')
      .then((list) => {
        if (!cancelled) setInfos(list)
      })
      .catch(() => {
        // Without the list there are simply no action buttons; the pages
        // report their own load errors.
      })
    return () => {
      cancelled = true
    }
  }, [])

  const start = useCallback((request: Request, confirm?: string) => {
    const id = nextId.current++
    const update = (change: (op: Operation) => Operation) =>
      setOperations((ops) => ops.map((op) => (op.id === id ? change(op) : op)))
    const add = (event: ActionEvent) => update((op) => ({ ...op, events: [...op.events, event] }))

    setOperations((ops) => [
      ...ops,
      { id, title: `${request.info.label} ${request.target}`, hostName: request.host.name, status: 'running', events: [] },
    ])

    const body: { container?: string; confirm?: string } = {}
    if (request.containerId) body.container = request.containerId
    if (confirm !== undefined) body.confirm = confirm

    streamAction(paths.action(request.host.id, request.service.key, request.info.id), body, add)
      .then((final) => update((op) => ({ ...op, status: final.level === 'error' ? 'error' : 'success' })))
      .catch((e: unknown) => {
        // Refused before it began, or the connection dropped.
        const step = e instanceof Error ? e.message : String(e)
        update((op) => ({ ...op, status: 'error', events: [...op.events, { step, level: 'error', done: true }] }))
      })
      .finally(() => setVersion((v) => v + 1))
  }, [])

  const run = useCallback(
    (host: HostView, service: ServiceView, actionId: string, containerId?: string) => {
      const info = infos.find((i) => i.id === actionId)
      if (!info) return
      const target = containerId ? (service.containers.find((c) => c.id === containerId)?.name ?? service.name) : service.name
      const request: Request = { host, service, info, containerId, target }
      const mode = confirmationFor(info, host)
      if (mode === 'none') start(request)
      else setAsking({ ...request, mode })
    },
    [infos, start],
  )

  const dismiss = useCallback((id: number) => setOperations((ops) => ops.filter((op) => op.id !== id)), [])
  const cancel = useCallback(() => setAsking(null), [])

  const value = useMemo(() => ({ infos, run, version }), [infos, run, version])

  return (
    <ActionsContext.Provider value={value}>
      {children}
      {asking && (
        <ConfirmDialog
          title={`${asking.info.label} ${asking.target} on ${asking.host.name}?`}
          mode={asking.mode}
          // The server checks the service's name, whichever container is meant.
          expected={asking.service.name}
          confirmLabel={asking.info.label}
          danger={asking.info.destructive}
          onCancel={cancel}
          onConfirm={() => {
            setAsking(null)
            start(asking, asking.mode === 'type-name' ? asking.service.name : undefined)
          }}
        >
          {(explanations[asking.info.id] ?? (() => `“${asking.info.label}” will run on ${asking.target}.`))(asking)}
        </ConfirmDialog>
      )}
      <ActionProgress operations={operations} onDismiss={dismiss} />
    </ActionsContext.Provider>
  )
}

export function useActions(): ActionsState {
  const value = useContext(ActionsContext)
  if (!value) throw new Error('useActions must be used inside ActionsProvider')
  return value
}

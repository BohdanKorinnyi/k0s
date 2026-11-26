import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from 'react'
import { api, paths } from '../api/client'
import type { ConnectionResult, HostView } from '../api/types'
import type { HostStatuses } from '../components/HostTabs'

type HostsState = {
  hosts: HostView[]
  /** True until the first load has finished. */
  loading: boolean
  error: string | null
  statuses: HostStatuses
  /** Fetches the list again, after a host was added, edited or removed. */
  reload: () => Promise<void>
}

const HostsContext = createContext<HostsState | null>(null)

const STATUS_INTERVAL = 30_000
// A host that did not answer is asked again far less often. Over SSH each
// attempt is a login on the server; repeating failed ones every half minute
// from a tab left open invites a ban.
const FAILED_STATUS_INTERVAL = 5 * 60_000

/** Loads the saved hosts once and shares them with every page. */
export function HostsProvider({ children }: { children: ReactNode }) {
  const [hosts, setHosts] = useState<HostView[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [statuses, setStatuses] = useState<HostStatuses>({})

  const reload = useCallback(async () => {
    try {
      setHosts(await api.get<HostView[]>('/api/hosts'))
      setError(null)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void reload()
  }, [reload])

  // Ask each host whether it answers, now and every half minute, for the dots
  // on the tabs. A slow host does not hold up the others.
  const ids = hosts.map((h) => h.id + '\n' + h.url).join('\n\n')
  useEffect(() => {
    let cancelled = false
    // When each host last failed to answer, to space out the next attempt.
    const failedAt = new Map<string, number>()
    const check = () => {
      for (const host of hosts) {
        const failed = failedAt.get(host.id)
        if (failed !== undefined && Date.now() - failed < FAILED_STATUS_INTERVAL) continue
        api
          .get<ConnectionResult>(`${paths.host(host.id)}/status`)
          .catch((e: unknown): ConnectionResult => ({ ok: false, version: '', error: e instanceof Error ? e.message : String(e) }))
          .then((result) => {
            if (cancelled) return
            if (result.ok) failedAt.delete(host.id)
            else failedAt.set(host.id, Date.now())
            setStatuses((prev) => ({ ...prev, [host.id]: result }))
          })
      }
    }
    check()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible') check()
    }, STATUS_INTERVAL)
    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
    // `ids` stands for the hosts: re-run when one is added, removed or re-pointed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ids])

  const value = useMemo(() => ({ hosts, loading, error, statuses, reload }), [hosts, loading, error, statuses, reload])
  return <HostsContext.Provider value={value}>{children}</HostsContext.Provider>
}

export function useHosts(): HostsState {
  const value = useContext(HostsContext)
  if (!value) throw new Error('useHosts must be used inside HostsProvider')
  return value
}

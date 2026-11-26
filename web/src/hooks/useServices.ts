import { useCallback, useEffect, useRef, useState } from 'react'
import { api, paths } from '../api/client'
import type { ServicesResponse } from '../api/types'

const REFRESH_INTERVAL = 5_000

export type ServicesState = {
  data: ServicesResponse | null
  /** Why the host could not be listed, when it could not. */
  error: string | null
  /** True until the first answer for this host arrives. */
  loading: boolean
  /** True while "Check now" is asking the registries. */
  checking: boolean
  /** When `data` arrived. Uptimes are measured against this. */
  loadedAt: Date
  reload: () => void
  checkNow: () => void
}

/**
 * Loads a host's services and keeps them fresh: every few seconds while the
 * tab is visible, and whenever `version` changes (an action finished).
 *
 * When a load fails the old list is dropped rather than left on screen: a
 * list that may no longer be true is worse than the reason it cannot be read.
 */
export function useServices(hostId: string | undefined, version: number): ServicesState {
  const [data, setData] = useState<ServicesResponse | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const [checking, setChecking] = useState(false)
  const [loadedAt, setLoadedAt] = useState(() => new Date())

  // Answers can arrive out of order, or after the host was switched; only the
  // answer to the latest request for the current host counts.
  const latest = useRef(0)
  const inFlight = useRef(false)

  const fetchInto = useCallback(
    async (request: () => Promise<ServicesResponse>) => {
      const mine = ++latest.current
      inFlight.current = true
      try {
        const result = await request()
        if (mine !== latest.current) return
        setData(result)
        setLoadedAt(new Date())
        setError(null)
      } catch (e) {
        if (mine !== latest.current) return
        setData(null)
        setError(e instanceof Error ? e.message : String(e))
      } finally {
        if (mine === latest.current) {
          inFlight.current = false
          setLoading(false)
        }
      }
    },
    [],
  )

  const reload = useCallback(() => {
    if (!hostId) return
    void fetchInto(() => api.get<ServicesResponse>(paths.services(hostId)))
  }, [hostId, fetchInto])

  const checkNow = useCallback(() => {
    if (!hostId) return
    setChecking(true)
    void fetchInto(() => api.post<ServicesResponse>(`${paths.host(hostId)}/updates/check`)).finally(() => setChecking(false))
  }, [hostId, fetchInto])

  // A different host: forget the previous one's list at once.
  useEffect(() => {
    latest.current++
    inFlight.current = false
    setData(null)
    setError(null)
    setLoading(true)
    setChecking(false)
    if (!hostId) {
      setLoading(false)
      return
    }
    reload()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !inFlight.current) reload()
    }, REFRESH_INTERVAL)
    return () => window.clearInterval(timer)
  }, [hostId, reload])

  useEffect(() => {
    if (version > 0) reload()
    // Only a finished action should trigger this, not a new `reload` identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version])

  return { data, error, loading, checking, loadedAt, reload, checkNow }
}

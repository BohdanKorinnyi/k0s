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
  /** Loads again at the person's request (the Retry button). */
  reload: () => void
  checkNow: () => void
}

/**
 * Loads a host's services and keeps them fresh: every few seconds while the
 * tab is visible, and whenever `version` changes (an action finished).
 *
 * When a load fails the old list is dropped rather than left on screen: a
 * list that may no longer be true is worse than the reason it cannot be read.
 * After a failure the timer stops asking; trying again is the person's call.
 * Over SSH every attempt is a login on the server, and a tab left open on a
 * host that refuses them would get its owner banned from their own machine.
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
  const failed = useRef(false)

  const fetchInto = useCallback(async (request: () => Promise<ServicesResponse>) => {
    const mine = ++latest.current
    inFlight.current = true
    try {
      const result = await request()
      if (mine !== latest.current) return
      failed.current = false
      setData(result)
      setLoadedAt(new Date())
      setError(null)
    } catch (e) {
      if (mine !== latest.current) return
      failed.current = true
      setData(null)
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      if (mine === latest.current) {
        inFlight.current = false
        setLoading(false)
      }
    }
  }, [])

  // `opening` marks a load that a person caused: the first for a host, or a
  // Retry. Only those let the server start an update check; the timer's
  // refreshes do not, or an open tab would use up the registry's quota.
  const load = useCallback(
    (opening: boolean) => {
      if (!hostId) return
      const path = paths.services(hostId) + (opening ? '?check=1' : '')
      void fetchInto(() => api.get<ServicesResponse>(path))
    },
    [hostId, fetchInto],
  )

  const reload = useCallback(() => load(true), [load])

  const checkNow = useCallback(() => {
    if (!hostId) return
    setChecking(true)
    void fetchInto(() => api.post<ServicesResponse>(`${paths.host(hostId)}/updates/check`)).finally(() => setChecking(false))
  }, [hostId, fetchInto])

  // A different host: forget the previous one's list at once.
  useEffect(() => {
    latest.current++
    inFlight.current = false
    failed.current = false
    setData(null)
    setError(null)
    setLoading(true)
    setChecking(false)
    if (!hostId) {
      setLoading(false)
      return
    }
    load(true)
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !inFlight.current && !failed.current) load(false)
    }, REFRESH_INTERVAL)
    return () => window.clearInterval(timer)
  }, [hostId, load])

  useEffect(() => {
    if (version > 0) load(false)
    // Only a finished action should trigger this, not a new `load` identity.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version])

  return { data, error, loading, checking, loadedAt, reload, checkNow }
}

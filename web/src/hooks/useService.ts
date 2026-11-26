import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError, paths } from '../api/client'
import type { ServiceView } from '../api/types'

const REFRESH_INTERVAL = 5_000

export type ServiceState = {
  data: ServiceView | null
  /** Set when there is nothing to show: the first load failed, or the service is gone. */
  error: { message: string; notFound: boolean } | null
  /**
   * Set when a refresh failed but an earlier answer is still shown. The page
   * stays as it was; this only says the picture may be out of date.
   */
  stale: string | null
  loading: boolean
  reload: () => void
}

/**
 * Loads one service and keeps it fresh: every few seconds while the tab is
 * visible, and whenever `version` changes (an action finished).
 *
 * A refresh that fails does not take the page away. The page may hold an open
 * shell and a log being read; a blip on the connection is no reason to end
 * either. Only a first load that fails, or the service no longer existing,
 * replaces the page.
 */
export function useService(hostId: string | undefined, key: string, version: number): ServiceState {
  const [data, setData] = useState<ServiceView | null>(null)
  const [error, setError] = useState<ServiceState['error']>(null)
  const [stale, setStale] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)
  const latest = useRef(0)
  const inFlight = useRef(false)
  const have = useRef(false)

  const reload = useCallback(() => {
    if (!hostId) return
    const mine = ++latest.current
    inFlight.current = true
    api
      .get<ServiceView>(paths.service(hostId, key))
      .then((result) => {
        if (mine !== latest.current) return
        have.current = true
        setData(result)
        setError(null)
        setStale(null)
      })
      .catch((e: unknown) => {
        if (mine !== latest.current) return
        const message = e instanceof Error ? e.message : String(e)
        const notFound = e instanceof ApiError && e.status === 404
        if (have.current && !notFound) {
          setStale(message)
          return
        }
        have.current = false
        setData(null)
        setStale(null)
        setError({ message, notFound })
      })
      .finally(() => {
        if (mine !== latest.current) return
        inFlight.current = false
        setLoading(false)
      })
  }, [hostId, key])

  useEffect(() => {
    latest.current++
    inFlight.current = false
    have.current = false
    setData(null)
    setError(null)
    setStale(null)
    setLoading(true)
    if (!hostId) {
      setLoading(false)
      return
    }
    reload()
    const timer = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !inFlight.current) reload()
    }, REFRESH_INTERVAL)
    return () => window.clearInterval(timer)
  }, [hostId, key, reload])

  useEffect(() => {
    if (version > 0) reload()
    // Only a finished action should trigger this.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [version])

  return { data, error, stale, loading, reload }
}

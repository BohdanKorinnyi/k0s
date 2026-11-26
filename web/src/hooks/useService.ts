import { useCallback, useEffect, useRef, useState } from 'react'
import { api, ApiError, paths } from '../api/client'
import type { ServiceView } from '../api/types'

const REFRESH_INTERVAL = 5_000

export type ServiceState = {
  data: ServiceView | null
  error: { message: string; notFound: boolean } | null
  loading: boolean
  reload: () => void
}

/**
 * Loads one service and keeps it fresh: every few seconds while the tab is
 * visible, and whenever `version` changes (an action finished).
 */
export function useService(hostId: string | undefined, key: string, version: number): ServiceState {
  const [data, setData] = useState<ServiceView | null>(null)
  const [error, setError] = useState<ServiceState['error']>(null)
  const [loading, setLoading] = useState(true)
  const latest = useRef(0)
  const inFlight = useRef(false)

  const reload = useCallback(() => {
    if (!hostId) return
    const mine = ++latest.current
    inFlight.current = true
    api
      .get<ServiceView>(paths.service(hostId, key))
      .then((result) => {
        if (mine !== latest.current) return
        setData(result)
        setError(null)
      })
      .catch((e: unknown) => {
        if (mine !== latest.current) return
        setData(null)
        setError({
          message: e instanceof Error ? e.message : String(e),
          notFound: e instanceof ApiError && e.status === 404,
        })
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
    setData(null)
    setError(null)
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

  return { data, error, loading, reload }
}

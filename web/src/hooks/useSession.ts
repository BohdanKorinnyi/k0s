import { useCallback, useEffect, useState } from 'react'
import { api, setUnauthenticatedHandler } from '../api/client'
import type { SessionInfo } from '../api/types'

export type SessionState =
  | { status: 'loading' }
  | { status: 'error'; message: string; retry: () => void }
  | { status: 'ready'; info: SessionInfo; needsLogin: boolean; refresh: () => void; logout: () => void }

/**
 * Finds out whether a password is needed and whether we are logged in.
 *
 * The login screen is shown in place of whatever page was asked for, so after
 * logging in the person is where they meant to go, with no redirects.
 */
export function useSession(): SessionState {
  const [info, setInfo] = useState<SessionInfo | null>(null)
  const [error, setError] = useState<string | null>(null)

  const refresh = useCallback(() => {
    setError(null)
    api
      .get<SessionInfo>('/api/session')
      .then(setInfo)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
  }, [])

  useEffect(() => {
    refresh()
  }, [refresh])

  // Any API call can discover that the session ended (it expired, or the
  // panel restarted). Show the login screen when that happens.
  useEffect(() => {
    setUnauthenticatedHandler(() => setInfo((prev) => (prev ? { ...prev, authenticated: false } : prev)))
    return () => setUnauthenticatedHandler(() => {})
  }, [])

  const logout = useCallback(() => {
    void api.post('/api/logout').finally(refresh)
  }, [refresh])

  if (error) return { status: 'error', message: error, retry: refresh }
  if (!info) return { status: 'loading' }
  return { status: 'ready', info, needsLogin: info.authRequired && !info.authenticated, refresh, logout }
}

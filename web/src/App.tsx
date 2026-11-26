import { useMemo } from 'react'
import { Navigate, Route, Routes } from 'react-router'
import { SessionContext } from './components/Layout'
import { HostsProvider } from './hooks/useHosts'
import { useSession } from './hooks/useSession'
import { HomePage, NotFoundPage } from './pages/HomePage'
import { HostsSettings } from './pages/HostsSettings'
import { LoginPage } from './pages/LoginPage'
import { RegistriesSettings } from './pages/RegistriesSettings'
import { ServicePage } from './pages/ServicePage'
import { ServicesPage } from './pages/ServicesPage'

export function App() {
  const session = useSession()
  const ready = session.status === 'ready'
  const canLogOut = ready && session.info.authRequired
  const logout = ready ? session.logout : undefined
  const sessionContext = useMemo(() => ({ canLogOut, logout: logout ?? (() => {}) }), [canLogOut, logout])

  if (session.status === 'loading') return null
  if (session.status === 'error') {
    return (
      <div className="center-screen">
        <div className="notice error" role="alert">
          <h2>Cannot reach the panel</h2>
          <div className="detail">{session.message}</div>
          <button type="button" className="btn" onClick={session.retry}>
            Retry
          </button>
        </div>
      </div>
    )
  }
  if (session.needsLogin) return <LoginPage onLoggedIn={session.refresh} />

  return (
    <SessionContext.Provider value={sessionContext}>
      <HostsProvider>
        <Routes>
          <Route path="/" element={<HomePage />} />
          <Route path="/h/:hostId" element={<ServicesPage />} />
          <Route path="/h/:hostId/s/:key" element={<ServicePage />} />
          <Route path="/settings" element={<Navigate to="/settings/hosts" replace />} />
          <Route path="/settings/hosts" element={<HostsSettings />} />
          <Route path="/settings/registries" element={<RegistriesSettings />} />
          <Route path="*" element={<NotFoundPage />} />
        </Routes>
      </HostsProvider>
    </SessionContext.Provider>
  )
}

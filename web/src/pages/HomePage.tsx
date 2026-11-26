import { Navigate } from 'react-router'
import { Layout } from '../components/Layout'
import { useHosts } from '../hooks/useHosts'
import { hostPath } from '../lib/serviceUrl'

/** The address "/" has no page of its own: it leads to the first host. */
export function HomePage() {
  const { hosts, loading, error, reload } = useHosts()
  if (loading) {
    return (
      <Layout>
        <div className="empty">Loading…</div>
      </Layout>
    )
  }
  if (error) {
    return (
      <Layout>
        <div className="notice error" role="alert">
          <h2>Could not load the hosts</h2>
          <div className="detail">{error}</div>
          <button type="button" className="btn" onClick={() => void reload()}>
            Retry
          </button>
        </div>
      </Layout>
    )
  }
  const first = hosts[0]
  // No hosts yet: the only useful place to be is where one is added.
  return <Navigate to={first ? hostPath(first.id) : '/settings/hosts?new=1'} replace />
}

export function NotFoundPage() {
  return (
    <Layout>
      <div className="empty">There is no page at this address.</div>
    </Layout>
  )
}

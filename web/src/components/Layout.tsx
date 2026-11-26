import { createContext, useContext, type ReactNode } from 'react'
import { Link, NavLink } from 'react-router'
import { APP_NAME } from '../appinfo'
import { useHosts } from '../hooks/useHosts'
import { HostTabs } from './HostTabs'

/** What the top bar needs to know about the session. */
export const SessionContext = createContext<{ canLogOut: boolean; logout: () => void }>({
  canLogOut: false,
  logout: () => {},
})

type Props = {
  /** The host whose page is open, to mark its tab. */
  currentHostId?: string | null
  /** Pages that manage their own height (the service page) fill the window. */
  fill?: boolean
  children: ReactNode
}

/** The frame around every page: top bar with the hosts, then the page. */
export function Layout({ currentHostId = null, fill = false, children }: Props) {
  const { hosts, statuses } = useHosts()
  const session = useContext(SessionContext)
  return (
    <>
      <header className="topbar">
        <Link to="/" className="brand">
          {APP_NAME}
        </Link>
        <HostTabs hosts={hosts} currentHostId={currentHostId} statuses={statuses} />
        <div className="topbar-right">
          <NavLink to="/settings/hosts" className="topbar-link">
            Settings
          </NavLink>
          {session.canLogOut && (
            <button type="button" className="btn quiet" onClick={session.logout}>
              Log out
            </button>
          )}
        </div>
      </header>
      <main className={fill ? 'page fill' : 'page'}>{children}</main>
    </>
  )
}

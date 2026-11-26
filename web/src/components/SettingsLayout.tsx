import { useEffect, type ReactNode } from 'react'
import { NavLink } from 'react-router'
import { APP_NAME } from '../appinfo'
import { Layout } from './Layout'

/** The frame of the settings pages: their own short menu beside the page. */
export function SettingsLayout({ title, children }: { title: string; children: ReactNode }) {
  useEffect(() => {
    document.title = `${title} · ${APP_NAME}`
  }, [title])
  return (
    <Layout>
      <div className="settings">
        <nav className="settings-nav" aria-label="Settings">
          <NavLink to="/settings/hosts">Hosts</NavLink>
          <NavLink to="/settings/registries">Registry logins</NavLink>
        </nav>
        <div>{children}</div>
      </div>
    </Layout>
  )
}

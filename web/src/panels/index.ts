// The tabs of the service page.
//
// To add a tab: write a component that takes PanelProps, in a file of its own
// in this folder, and add an entry to `panels` below. Nothing else changes —
// the service page draws its tabs from this list.

import type { ComponentType } from 'react'
import type { ContainerView, HostView, ServiceView } from '../api/types'
import { EnvPanel } from './EnvPanel'
import { InfoPanel } from './InfoPanel'
import { LogsPanel } from './LogsPanel'
import { ShellPanel } from './ShellPanel'

export type PanelProps = {
  host: HostView
  service: ServiceView
  /**
   * The container the tab is about. It is null only for tabs that set
   * `supportsAll`, when "All together" is chosen.
   */
  container: ContainerView | null
}

export type Panel = {
  /** Appears in the address as ?tab=<id>. */
  id: string
  title: string
  /** Set when the tab can show all of a service's containers at once. */
  supportsAll?: boolean
  component: ComponentType<PanelProps>
}

export const panels: Panel[] = [
  { id: 'logs', title: 'Logs', supportsAll: true, component: LogsPanel },
  { id: 'env', title: 'Env', component: EnvPanel },
  { id: 'shell', title: 'Shell', component: ShellPanel },
  { id: 'info', title: 'Info', component: InfoPanel },
]

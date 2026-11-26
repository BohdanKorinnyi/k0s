// Page URLs. A service page's address holds everything needed to come back
// to the same view: host, service, tab and container.

export const DEFAULT_TAB = 'logs'

export function hostPath(hostId: string): string {
  return `/h/${encodeURIComponent(hostId)}`
}

export function servicePath(hostId: string, key: string, o: { tab?: string; container?: string | null } = {}): string {
  const query = new URLSearchParams()
  if (o.tab) query.set('tab', o.tab)
  if (o.container) query.set('c', o.container)
  const search = query.toString()
  return `${hostPath(hostId)}/s/${encodeURIComponent(key)}${search ? '?' + search : ''}`
}

export type ServiceLocation = {
  hostId: string
  key: string
  tab: string
  /** null means no container is chosen yet. */
  container: string | null
}

const servicePattern = /^\/h\/([^/]+)\/s\/([^/]+)\/?$/

export function parseServiceLocation(pathname: string, search: string): ServiceLocation | null {
  const match = servicePattern.exec(pathname)
  if (!match) return null
  const query = new URLSearchParams(search)
  try {
    return {
      hostId: decodeURIComponent(match[1]!),
      key: decodeURIComponent(match[2]!),
      tab: query.get('tab') || DEFAULT_TAB,
      container: query.get('c') || null,
    }
  } catch {
    return null // a malformed escape in the address
  }
}

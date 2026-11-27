import type { ServiceGroup, ServiceView } from '../api/types'

/**
 * Narrows the services list by the filter box and the "hide stopped" toggle.
 * The text is matched, in any case, against what a person would recognise a
 * service by: its name, project, image and container names.
 */
export function filterGroups(groups: ServiceGroup[], query: string, hideStopped: boolean): ServiceGroup[] {
  const needle = query.trim().toLowerCase()
  if (needle === '' && !hideStopped) return groups

  const keep = (s: ServiceView) => {
    if (hideStopped && s.running === 0) return false
    if (needle === '') return true
    const haystack = [s.name, s.project, s.image, ...s.images, ...s.containers.map((c) => c.name)]
    return haystack.some((text) => text.toLowerCase().includes(needle))
  }

  return groups
    .map((group) => ({ ...group, services: group.services.filter(keep) }))
    .filter((group) => group.services.length > 0)
}

export function countServices(groups: ServiceGroup[]): number {
  return groups.reduce((total, group) => total + group.services.length, 0)
}

/** How many containers run, out of how many there are, across the whole list. */
export function countContainers(groups: ServiceGroup[]): { running: number; total: number } {
  let running = 0
  let total = 0
  for (const group of groups) {
    for (const service of group.services) {
      running += service.running
      total += service.total
    }
  }
  return { running, total }
}

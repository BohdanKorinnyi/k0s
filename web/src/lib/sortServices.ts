import type { ServiceGroup, ServiceState, ServiceView } from '../api/types'

export type SortBy = 'name' | 'status'
export type Sort = { by: SortBy; dir: 'asc' | 'desc' }

export const DEFAULT_SORT: Sort = { by: 'name', dir: 'asc' }

// How much a service wants looking at; lower comes first when sorting by
// status. One that keeps crashing leads, one that simply runs comes last.
const attention: Record<ServiceState, number> = {
  restarting: 0,
  unhealthy: 1,
  partial: 2,
  stopped: 3,
  running: 4,
}

const rank = (service: ServiceView) => attention[service.state] ?? attention.running

// Names as a person orders them: case ignored, and mdb_2 before mdb_10.
const byText = (a: string, b: string) => a.localeCompare(b, undefined, { numeric: true, sensitivity: 'base' })

/**
 * Orders the services list. Services stay with their Compose project; the
 * sort orders them inside it, and orders the projects among themselves.
 *
 * By name, containers outside any project stay at the end either way. By
 * status, a project stands where its most troubled service puts it, so the
 * thing that needs attention is at the top of the page wherever it lives.
 */
export function sortGroups(groups: ServiceGroup[], sort: Sort): ServiceGroup[] {
  const flip = sort.dir === 'asc' ? 1 : -1
  const noProjectLast = (a: ServiceGroup, b: ServiceGroup) => Number(a.project === '') - Number(b.project === '')

  if (sort.by === 'name') {
    return groups
      .map((group) => ({ ...group, services: [...group.services].sort((a, b) => flip * byText(a.name, b.name)) }))
      .sort((a, b) => noProjectLast(a, b) || flip * byText(a.project, b.project))
  }

  const worst = (group: ServiceGroup) => Math.min(...group.services.map(rank))
  return groups
    .map((group) => ({
      ...group,
      services: [...group.services].sort((a, b) => flip * (rank(a) - rank(b)) || byText(a.name, b.name)),
    }))
    .sort((a, b) => flip * (worst(a) - worst(b)) || noProjectLast(a, b) || byText(a.project, b.project))
}

/** What a click on a column heading does: reverse that column, or switch to it. */
export function nextSort(current: Sort, by: SortBy): Sort {
  if (current.by !== by) return { by, dir: 'asc' }
  return { by, dir: current.dir === 'asc' ? 'desc' : 'asc' }
}

/** A sort as remembered in the browser, such as "status:desc". */
export function formatSort(sort: Sort): string {
  return `${sort.by}:${sort.dir}`
}

/** Reads a remembered sort back; anything unrecognised means the default. */
export function parseSort(stored: string | null): Sort {
  const [by, dir] = (stored ?? '').split(':')
  if ((by === 'name' || by === 'status') && (dir === 'asc' || dir === 'desc')) return { by, dir }
  return DEFAULT_SORT
}

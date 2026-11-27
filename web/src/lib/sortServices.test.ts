import { expect, test } from 'vitest'
import type { ServiceGroup, ServiceState } from '../api/types'
import { service } from '../test-utils'
import { DEFAULT_SORT, nextSort, parseSort, sortGroups, type Sort } from './sortServices'

const svc = (project: string, name: string, state: ServiceState = 'running') =>
  service({ key: `c~${project}~${name}`, name, project, state })

// Deliberately out of order, with a problem in two of the three groups.
const groups: ServiceGroup[] = [
  {
    project: 'shop',
    services: [svc('shop', 'worker', 'stopped'), svc('shop', 'api'), svc('shop', 'db', 'unhealthy'), svc('shop', 'Cache', 'partial')],
  },
  { project: '', services: [svc('', 'mdb_10'), svc('', 'mdb_2', 'restarting')] },
  { project: 'blog', services: [svc('blog', 'web'), svc('blog', 'db')] },
]

const shape = (sorted: ServiceGroup[]) => sorted.map((g) => `${g.project || '-'}: ${g.services.map((s) => s.name).join(' ')}`)
const sorted = (sort: Sort) => shape(sortGroups(groups, sort))

test('by name: projects and their services from A to Z, containers outside a project last', () => {
  // Case is ignored, and mdb_2 comes before mdb_10 as a person would count.
  expect(sorted({ by: 'name', dir: 'asc' })).toEqual(['blog: db web', 'shop: api Cache db worker', '-: mdb_2 mdb_10'])
})

test('by name, descending: the same, from Z to A', () => {
  expect(sorted({ by: 'name', dir: 'desc' })).toEqual(['shop: worker db Cache api', 'blog: web db', '-: mdb_10 mdb_2'])
})

test('by status: what needs attention comes first, in each project and among projects', () => {
  // restarting, then unhealthy, partial, stopped, and running last.
  expect(sorted({ by: 'status', dir: 'asc' })).toEqual(['-: mdb_2 mdb_10', 'shop: db Cache worker api', 'blog: db web'])
})

test('by status, descending: running first', () => {
  expect(sorted({ by: 'status', dir: 'desc' })).toEqual(['blog: db web', 'shop: api worker Cache db', '-: mdb_10 mdb_2'])
})

test('sorting leaves the list it was given as it was', () => {
  const before = shape(groups)
  sortGroups(groups, { by: 'status', dir: 'asc' })
  sortGroups(groups, { by: 'name', dir: 'desc' })
  expect(shape(groups)).toEqual(before)
})

test('choosing the same column again reverses it; another column starts ascending', () => {
  expect(nextSort({ by: 'name', dir: 'asc' }, 'name')).toEqual({ by: 'name', dir: 'desc' })
  expect(nextSort({ by: 'name', dir: 'desc' }, 'name')).toEqual({ by: 'name', dir: 'asc' })
  expect(nextSort({ by: 'name', dir: 'desc' }, 'status')).toEqual({ by: 'status', dir: 'asc' })
})

test('a remembered sort is read back, and anything else falls back to by name', () => {
  expect(parseSort('status:desc')).toEqual({ by: 'status', dir: 'desc' })
  expect(parseSort('name:asc')).toEqual({ by: 'name', dir: 'asc' })
  for (const junk of [null, '', 'status', 'size:asc', 'name:sideways', '{"by":"name"}']) {
    expect(parseSort(junk)).toEqual(DEFAULT_SORT)
  }
  expect(DEFAULT_SORT).toEqual({ by: 'name', dir: 'asc' })
})

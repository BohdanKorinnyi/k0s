import { expect, test } from 'vitest'
import type { ServiceGroup } from '../api/types'
import { container, service } from '../test-utils'
import { countServices, filterGroups } from './filterServices'

// Each service is recognisable by different words, so a filter can single one out.
const groups: ServiceGroup[] = [
  {
    project: 'shop',
    services: [
      service({ key: 'c~shop~api', name: 'api' }),
      service({
        key: 'c~shop~worker',
        name: 'worker',
        image: 'ghcr.io/acme/worker:develop',
        images: ['ghcr.io/acme/worker:develop'],
        containers: [container({ name: 'shop-worker-1', image: 'ghcr.io/acme/worker:develop' })],
      }),
      service({
        key: 'c~shop~db',
        name: 'db',
        image: 'postgres:16.4',
        images: ['postgres:16.4'],
        state: 'stopped',
        running: 0,
        containers: [container({ name: 'shop-db-1', state: 'exited', image: 'postgres:16.4' })],
      }),
    ],
  },
  {
    project: '',
    services: [
      service({
        key: 's~redis-cache',
        name: 'redis-cache',
        project: '',
        image: 'redis:7.4',
        images: ['redis:7.4'],
        containers: [container({ name: 'redis-cache', image: 'redis:7.4' })],
      }),
    ],
  },
]

const keys = (gs: ServiceGroup[]) => gs.flatMap((g) => g.services.map((s) => s.key))

test('no filter keeps everything', () => {
  expect(filterGroups(groups, '', false)).toEqual(groups)
  expect(countServices(groups)).toBe(4)
})

test('the filter matches the service name, in any case', () => {
  expect(keys(filterGroups(groups, 'API', false))).toEqual(['c~shop~api'])
})

test('it also matches the image, a container name and the project', () => {
  expect(keys(filterGroups(groups, 'postgres', false))).toEqual(['c~shop~db'])
  expect(keys(filterGroups(groups, 'worker-1', false))).toEqual(['c~shop~worker'])
  expect(keys(filterGroups(groups, 'shop', false))).toEqual(['c~shop~api', 'c~shop~worker', 'c~shop~db'])
})

test('groups left with no services are dropped', () => {
  const result = filterGroups(groups, 'redis', false)
  expect(result).toHaveLength(1)
  expect(result[0]!.project).toBe('')
})

test('hide stopped removes services with nothing running', () => {
  expect(keys(filterGroups(groups, '', true))).toEqual(['c~shop~api', 'c~shop~worker', 's~redis-cache'])
  expect(keys(filterGroups(groups, 'shop', true))).toEqual(['c~shop~api', 'c~shop~worker'])
})

test('surrounding spaces in the filter are ignored', () => {
  expect(keys(filterGroups(groups, '  api  ', false))).toEqual(['c~shop~api'])
})

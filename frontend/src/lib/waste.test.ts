import { describe, expect, it } from 'vitest'
import { completeWaste, type WasteWorkload } from '../api/waste'
import {
  cpuQuantity,
  formatMoney,
  memoryQuantity,
  parseRate,
  rateLines,
  rightsizingCommand,
  rightsizingPatch,
  sinceWindow,
  volumeExplanation,
  wasteCostSentence,
} from './waste'

const MI = 1024 * 1024

function workload(overrides: Partial<WasteWorkload> = {}): WasteWorkload {
  return {
    kind: 'Deployment',
    namespace: 'shop',
    name: 'web',
    replicas: 2,
    since: '2026-10-01T00:00:00Z',
    reclaimable: { cpu: 1710, memory: 0 },
    containers: [
      {
        name: 'app',
        cpu: { request: 1000, observed: 120, recommended: 145, over: true },
        memory: { request: 256 * MI, observed: 200 * MI, recommended: 240 * MI, over: false },
      },
    ],
    ...overrides,
  }
}

describe('quantities', () => {
  it('writes whole cores bare and the rest in millicores', () => {
    expect(cpuQuantity(2000)).toBe('2')
    expect(cpuQuantity(145)).toBe('145m')
  })

  it('writes whole gibibytes as Gi and rounds the rest up to Mi', () => {
    expect(memoryQuantity(2 * 1024 * MI)).toBe('2Gi')
    expect(memoryQuantity(240 * MI)).toBe('240Mi')
    expect(memoryQuantity(240 * MI + 1)).toBe('241Mi')
  })
})

describe('rightsizingPatch', () => {
  it('changes only the over-asked request, leaving the rest of the manifest alone', () => {
    expect(rightsizingPatch(workload())).toBe(
      [
        'spec:',
        '  template:',
        '    spec:',
        '      containers:',
        '        - name: app',
        '          resources:',
        '            requests:',
        '              cpu: 145m',
        '',
      ].join('\n'),
    )
  })

  it('patches a native sidecar under initContainers', () => {
    const patch = rightsizingPatch(
      workload({
        containers: [
          {
            name: 'proxy',
            sidecar: true,
            cpu: { request: 500, observed: 20, recommended: 25, over: true },
            memory: { request: 512 * MI, observed: 30 * MI, recommended: 36 * MI, over: true },
          },
          {
            name: 'app',
            cpu: { request: 100, observed: 90, recommended: 110, over: false },
            memory: { over: false },
          },
        ],
      }),
    )
    expect(patch).toContain('      initContainers:\n        - name: proxy')
    expect(patch).toContain('              memory: 36Mi')
    expect(patch).not.toContain('containers:\n        - name: app')
  })

  it('is null when nothing would change', () => {
    expect(
      rightsizingPatch(
        workload({
          containers: [{ name: 'app', cpu: { over: false }, memory: { over: false } }],
        }),
      ),
    ).toBeNull()
  })

  it('names the object the patch is for', () => {
    expect(rightsizingCommand(workload({ kind: 'StatefulSet', name: 'db' }))).toBe(
      'kubectl -n shop patch statefulset db --patch-file rightsize.yaml',
    )
  })
})

describe('completeWaste', () => {
  it('fills the lists and leaves missing readings absent rather than zero', () => {
    const complete = completeWaste({
      workloads: [
        {
          ...workload(),
          containers: [{ name: 'app', cpu: { over: false }, memory: {} as never }],
        },
      ],
    })
    expect(complete.volumes).toEqual([])
    expect(complete.load_balancers).toEqual([])
    expect(complete.summary.unused_storage).toBe(0)
    expect(complete.workloads_available).toBe(false)
    const container = complete.workloads[0].containers[0]
    expect(container.memory.over).toBe(false)
    expect(container.memory.observed).toBeUndefined()
    expect(container.cpu.request).toBeUndefined()
  })

  it('leaves money absent where the server sent none', () => {
    const complete = completeWaste({})
    expect(complete.cost).toBeUndefined()
    expect(complete.summary.monthly).toBeUndefined()
    expect(complete.reserved.namespaces).toEqual([])
    expect(complete.reserved.monthly).toBeUndefined()
  })
})

describe('money', () => {
  it('writes cents below 100 and whole units above', () => {
    expect(formatMoney(12.5, 'USD', 'en-US')).toBe('$12.50')
    expect(formatMoney(1204.37, 'USD', 'en-US')).toBe('$1,204')
    expect(formatMoney(0, 'EUR', 'en-US')).toBe('€0.00')
  })

  it('keeps a rate’s fraction of a cent', () => {
    const lines = rateLines(
      { currency: 'USD', cpu_core_hour: 0.0316, memory_gib_hour: 0.004237, load_balancer_hour: 0 },
      'en-US',
    )
    expect(lines.map((line) => line.value)).toEqual([
      '$0.0316 per core-hour',
      '$0.004237 per GiB-hour',
      '$0.00 per hour',
    ])
  })

  it('reads a rate field: empty is unpriced, a comma is a decimal point, anything else is refused', () => {
    expect(parseRate('')).toBeUndefined()
    expect(parseRate(' 0,04 ')).toBe(0.04)
    expect(parseRate('0')).toBe(0)
    expect(parseRate('-1')).toBeNull()
    expect(parseRate('1e3')).toBeNull()
    expect(parseRate('$5')).toBeNull()
  })
})

describe('wasteCostSentence', () => {
  const priced = (monthly: Partial<NonNullable<ReturnType<typeof completeWaste>['summary']['monthly']>>) =>
    completeWaste({
      workloads_available: true,
      cost: { currency: 'USD', rates: { currency: 'USD', cpu_core_hour: 1, memory_gib_hour: 1 }, hours_per_month: 730 },
      summary: {
        reclaimable_cpu: 0,
        reclaimable_memory: 0,
        unused_storage: 0,
        idle_load_balancers: 0,
        workloads_checked: 0,
        workloads_short_history: 0,
        workloads_unmeasured: 0,
        monthly: { reclaimable: 40, total: 40, unpriced: [], ...monthly },
      },
    })

  it('is absent without rates', () => {
    expect(wasteCostSentence(completeWaste({}))).toBeNull()
  })

  it('breaks the total down and names what it leaves out', () => {
    const sentence = wasteCostSentence(
      priced({ idle_load_balancers: 18.25, total: 58.25, unpriced: ['storage'] }),
      'en-US',
    )
    expect(sentence).toBe(
      'At this cluster’s rates, what this page finds unused costs about $58.25 a month: ' +
        '$40.00 in over-asked requests, $18.25 in load balancers. ' +
        'Storage has no rate, so it is not counted.',
    )
  })

  it('says over-asked requests are missing without history', () => {
    const report = { ...priced({ reclaimable: 0, total: 0 }), workloads_available: false }
    expect(wasteCostSentence(report, 'en-US')).toMatch(/need usage history, so they are not counted/)
  })
})

describe('volumeExplanation', () => {
  it('says a scaled-down StatefulSet keeps its claim on purpose', () => {
    expect(
      volumeExplanation({
        kind: 'PersistentVolumeClaim',
        name: 'data-db-3',
        capacity: 0,
        state: 'unmounted',
        statefulset: 'db',
        scaled_down: true,
        created_at: '',
      }),
    ).toMatch(/Kept by StatefulSet db after it scaled down/)
  })

  it('names the claim a released volume belonged to', () => {
    expect(
      volumeExplanation({
        kind: 'PersistentVolume',
        name: 'pv-1',
        capacity: 0,
        state: 'released',
        claim: 'shop/gone',
        reclaim_policy: 'Retain',
        created_at: '',
      }),
    ).toBe('Its claim shop/gone was deleted and the data kept (Retain).')
  })
})

describe('sinceWindow', () => {
  it('marks a workload that started inside the window', () => {
    expect(sinceWindow(workload({ since: '2026-10-10T00:00:00Z' }), '2026-10-07T00:00:00Z')).toBe(true)
    expect(sinceWindow(workload({ since: '2026-10-01T00:00:00Z' }), '2026-10-07T00:00:00Z')).toBe(false)
  })
})

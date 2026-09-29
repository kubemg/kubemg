import { describe, expect, it } from 'vitest'

import type { Cluster } from '../api/types'
import { agentsBehind, developerFigures, operatorFigures } from './fleetStrip'
import type { StripCounts } from './fleetStrip'

function cluster(over: Partial<Cluster> = {}): Cluster {
  return {
    id: 1,
    name: 'edge-us',
    environment: 'prod',
    api_url: '',
    status: 'healthy',
    created_at: '2026-01-01T00:00:00Z',
    k8s_role: 'edit',
    namespaces: [],
    connection_mode: 'agent',
    agent_attached: true,
    ...over,
  }
}

const read: StripCounts = { pending: 2, refused: 14, expiring: 3 }

describe('operatorFigures', () => {
  it('is the four decisions, and none of the readings it replaced', () => {
    const figures = operatorFigures(read, [cluster()])
    expect(figures.map((figure) => figure.key)).toEqual(['requests', 'refused', 'expiring', 'behind'])
    const labels = figures.map((figure) => figure.label).join(' ')
    for (const gone of ['environment', 'reachable', 'unreachable', 'tunnel', 'clusters']) {
      expect(labels).not.toContain(gone)
    }
  })

  it('opens each figure on the rows it counted, with the filter applied', () => {
    const byKey = Object.fromEntries(
      operatorFigures(read, [cluster()]).map((figure) => [figure.key, figure.to]),
    )
    expect(byKey.requests).toBe('/admin/access-requests')
    // Refusals over the same window the count was read over, never the whole trail.
    expect(byKey.refused).toBe('/admin/audit?range=24h&failed=true')
    expect(byKey.expiring).toBe('/admin/credentials?expiring=24h')
    expect(byKey.behind).toBe('/admin/clusters?agent=behind')
  })

  it('keeps a failed read unknown and a pending read pending — never zero', () => {
    const figures = operatorFigures({ pending: null, refused: undefined, expiring: 0 }, [])
    const byKey = Object.fromEntries(figures.map((figure) => [figure.key, figure.value]))
    expect(byKey.requests).toBeNull()
    expect(byKey.refused).toBeUndefined()
    expect(byKey.expiring).toBe(0)
  })
})

describe('agentsBehind', () => {
  it('measures drift against the newest version running in the fleet', () => {
    const fleet = [
      cluster({ id: 1, agent_version: '0.11.1' }),
      cluster({ id: 2, agent_version: '0.11.0' }),
      cluster({ id: 3, agent_version: '0.10.2' }),
      cluster({ id: 4, agent_version: undefined }),
    ]
    expect(agentsBehind(fleet).map((c) => c.id)).toEqual([2, 3])
    expect(operatorFigures(read, fleet).find((f) => f.key === 'behind')?.value).toBe(2)
  })

  it('reads a fleet on one version as nothing behind, whatever that version is', () => {
    const fleet = [
      cluster({ id: 1, agent_version: '0.1.0' }),
      cluster({ id: 2, agent_version: '0.1.0' }),
    ]
    expect(agentsBehind(fleet)).toEqual([])
    expect(agentsBehind([cluster({ agent_version: undefined })])).toEqual([])
  })
})

describe('developerFigures', () => {
  it('draws only what a developer can act on, each onto their own page', () => {
    const figures = developerFigures(read)
    expect(figures.map((figure) => figure.key)).toEqual(['requests', 'expiring'])
    expect(figures.every((figure) => figure.to.startsWith('/me/'))).toBe(true)
    expect(figures.find((figure) => figure.key === 'expiring')?.to).toBe(
      '/me/credentials?expiring=24h',
    )
  })
})

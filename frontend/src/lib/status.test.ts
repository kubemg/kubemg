import { describe, expect, it } from 'vitest'

import type { Cluster } from '../api/types'
import { clusterStateLabel, clusterTone, linkState, phaseTone } from './status'
import type { Tone } from './status'

describe('phaseTone', () => {
  it('reads a status word the same way wherever it is drawn', () => {
    // It moved out of one table because a namespace's row and a namespace's own
    // page both draw it now, and one phase cannot have two tones.
    expect(phaseTone('Active')).toBe('ok')
    expect(phaseTone('Bound')).toBe('ok')
    expect(phaseTone('Pending')).toBe('warn')
    expect(phaseTone('Failed')).toBe('bad')
    expect(phaseTone('Lost')).toBe('bad')
    expect(phaseTone('Terminating')).toBe('idle')
  })

  it("treats the namespace list's own word for a grant as an active namespace", () => {
    // A scoped grant is answered from the grant rather than from the cluster,
    // so the row says `Granted` and never `Active` — reading that as idle would
    // grey out every namespace a scoped developer has.
    expect(phaseTone('Granted')).toBe('ok')
  })

  it('is idle for a word it has never heard of, not bad', () => {
    expect(phaseTone('SomethingNew')).toBe('idle')
    expect(phaseTone('')).toBe('idle')
  })
})

describe('a cluster’s reachability', () => {
  /*
   * The pill and the glyph are two drawings of one fact, so they are derived
   * from one function. They used to disagree: the pill read the stored check
   * and the glyph read the live tunnel, and a dashboard said `Reachable ·
   * checked 9m ago` beside a rail chip that was red.
   */
  const agent = (over: Partial<Cluster>): Cluster =>
    ({
      id: 1,
      name: 'prod-eu-west-1',
      connection_mode: 'agent',
      status: 'pending',
      agent_attached: false,
      ...over,
    }) as Cluster

  it('answers from the tunnel for an agent cluster, not from the last probe', () => {
    // The stored check says unhealthy and the tunnel is open: the tunnel wins,
    // because it is the reading that is true now.
    const stale = agent({ status: 'unhealthy', agent_attached: true })
    expect(linkState(stale)).toBe('live')
    expect(clusterTone(stale)).toBe('ok')
    expect(clusterStateLabel(stale)).toBe('Reachable')
  })

  it('never renders a tone the glyph would not', () => {
    const cases: Cluster[] = [
      agent({ agent_attached: true }),
      agent({ status: 'unhealthy' }),
      agent({}),
      { connection_mode: 'direct', status: 'healthy' } as Cluster,
      { connection_mode: 'direct', status: 'unhealthy' } as Cluster,
      { connection_mode: 'direct', status: 'pending' } as Cluster,
    ]
    const expected: Record<string, Tone> = { live: 'ok', direct: 'ok', down: 'bad', idle: 'idle' }
    for (const cluster of cases) {
      expect(clusterTone(cluster)).toBe(expected[linkState(cluster)])
    }
  })

  it('tells an agent that has not dialled in from a direct cluster nobody probed', () => {
    expect(clusterStateLabel(agent({}))).toBe('Waiting to dial in')
    expect(
      clusterStateLabel({ connection_mode: 'direct', status: 'pending' } as Cluster),
    ).toBe('Never checked')
  })
})

import { describe, expect, it } from 'vitest'

import { phaseTone } from './status'

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

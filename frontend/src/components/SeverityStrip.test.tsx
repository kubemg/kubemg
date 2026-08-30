/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { PostureFinding } from '../api/types'
import { severityDistribution } from '../lib/posture'
import { SeverityStrip } from './SeverityStrip'

/*
 * The strip is what turned 36 rows at identical weight into a list somebody can
 * work through, and two of its properties are the ones that make it that rather
 * than a decoration: every band is always drawn (so its shape does not move
 * between scans), and pressing one filters (so the number is a way in).
 */

function finding(over: Partial<PostureFinding> = {}): PostureFinding {
  return {
    rule: 'privileged_container',
    title: 'Privileged container',
    permits: 100,
    kind: 'Deployment',
    name: 'web',
    namespace: 'shop',
    field: 'spec.containers[0].securityContext.privileged',
    message: 'Runs privileged.',
    pss_covered: true,
    acknowledged: false,
    ...over,
  }
}

afterEach(cleanup)

describe('SeverityStrip', () => {
  it('draws every band, including the empty ones', () => {
    // A band that vanished when empty would make the strip's shape move between
    // scans, which is the one thing a distribution must not do.
    render(
      <SeverityStrip
        distribution={severityDistribution([finding()])}
        selected={null}
        onSelect={() => {}}
      />,
    )
    for (const label of ['Critical', 'High', 'Medium', 'Low']) {
      expect(screen.getByText(label)).toBeTruthy()
    }
  })

  it('separates the shape of the cluster from the work left in it', () => {
    render(
      <SeverityStrip
        distribution={severityDistribution([finding(), finding({ name: 'api', acknowledged: true })])}
        selected={null}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText('2')).toBeTruthy()
    // A fully triaged cluster showing only totals would look permanently
    // alarming, which is how a security page stops being read.
    expect(screen.getByText('1 open')).toBeTruthy()
  })

  it('says so when a band is fully acknowledged', () => {
    render(
      <SeverityStrip
        distribution={severityDistribution([finding({ acknowledged: true })])}
        selected={null}
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText('all acknowledged')).toBeTruthy()
  })

  it('is the filter: pressing a band asks for it', () => {
    const onSelect = vi.fn()
    render(
      <SeverityStrip
        distribution={severityDistribution([finding()])}
        selected={null}
        onSelect={onSelect}
      />,
    )
    fireEvent.click(screen.getByText('Critical'))
    expect(onSelect).toHaveBeenCalledWith('critical')
  })

  it('offers nothing for a band with no findings', () => {
    // A tile that filters to an empty list is a dead end.
    render(
      <SeverityStrip
        distribution={severityDistribution([finding()])}
        selected={null}
        onSelect={() => {}}
      />,
    )
    const high = screen.getByText('High').closest('button')
    expect(high?.disabled).toBe(true)
  })

  it('shows which band is lit', () => {
    render(
      <SeverityStrip
        distribution={severityDistribution([finding()])}
        selected="critical"
        onSelect={() => {}}
      />,
    )
    expect(screen.getByText('Critical').closest('button')?.getAttribute('aria-pressed')).toBe('true')
  })
})

/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it } from 'vitest'

import { qosSegments } from '../lib/capacityView'
import { AllocationLine, CapacityRing, SegmentBar } from './CapacityVisuals'

afterEach(cleanup)

const dimension = {
  allocatable: 8000,
  requested: 1330,
  limited: 1440,
  used: 424,
  requested_percent: 16.6,
  limited_percent: 18,
  used_percent: 5.3,
  unlimited_containers: 0,
}

describe('CapacityRing', () => {
  it('reads the reserved share and carries the full sentence', () => {
    render(
      <CapacityRing
        label="CPU"
        reservedPercent={16.6}
        usedPercent={5.3}
        measured
        amount="1.33 / 8.00 cores"
        title="CPU: 1.33 cores of 8.00 cores reserved, 424m in use"
      />,
    )
    const meter = screen.getByRole('meter', { name: 'CPU reserved' })
    expect(meter.getAttribute('aria-valuenow')).toBe('17')
    expect(meter.getAttribute('aria-valuetext')).toContain('424m in use')
  })

  it('says unknown, not zero, when nothing is allocatable', () => {
    render(
      <CapacityRing
        label="Memory"
        reservedPercent={0}
        usedPercent={null}
        measured={false}
        amount="no allocatable"
        title="Memory: no allocatable capacity reported"
      />,
    )
    expect(screen.getByRole('meter').getAttribute('aria-valuenow')).toBeNull()
    expect(screen.getByText('—')).toBeTruthy()
  })
})

describe('AllocationLine', () => {
  it('leaves live use out of the sentence when it is not measured', () => {
    render(
      <AllocationLine
        label="CPU"
        dimension={dimension}
        format={(value) => `${value}m`}
        usageAvailable={false}
      />,
    )
    expect(screen.getByRole('meter').getAttribute('aria-valuetext')).toContain(
      'live use not measured',
    )
  })
})

describe('SegmentBar', () => {
  it('names every class in the legend and only the non-empty ones in the label', () => {
    render(
      <SegmentBar label="QoS" segments={qosSegments({ guaranteed: 0, burstable: 24, best_effort: 6 })} />,
    )
    expect(screen.getByRole('img').getAttribute('aria-label')).toBe(
      'QoS: 24 Burstable · 6 BestEffort',
    )
    expect(screen.getByText('Guaranteed')).toBeTruthy()
  })
})

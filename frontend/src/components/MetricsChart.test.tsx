/**
 * @vitest-environment jsdom
 */
import { cleanup, render } from '@testing-library/react'
import { afterEach, beforeAll, describe, expect, it } from 'vitest'

import type { MetricResult } from '../api/types'
import { Plot } from './MetricsChart'

/*
 * The plot's drawing contract: every series is a line on a fading area of its
 * own slot colour, the latest sample of each is marked, and the time axis is
 * written along the bottom — while at rest, with no pointer over it.
 */

beforeAll(() => {
  // jsdom has no layout; the plot measures its frame and falls back without one.
  globalThis.ResizeObserver ??= class {
    observe() {}
    unobserve() {}
    disconnect() {}
  } as unknown as typeof ResizeObserver
})

afterEach(cleanup)

function result(seriesCount: number): MetricResult {
  const start = Date.parse('2026-01-01T00:00:00Z')
  return {
    kind: 'cluster_cpu',
    unit: 'millicores',
    start: new Date(start).toISOString(),
    end: new Date(start + 4 * 60_000).toISOString(),
    step_seconds: 60,
    query: 'sum(rate(container_cpu_usage_seconds_total[5m]))',
    series: Array.from({ length: seriesCount }, (_, index) => ({
      name: `series-${index}`,
      points: Array.from({ length: 5 }, (_, step) => ({
        at: new Date(start + step * 60_000).toISOString(),
        value: 100 * (index + 1) + step * 10,
      })),
    })),
  }
}

describe('Plot', () => {
  it('draws each series as a line over its own fading area', () => {
    const { container } = render(<Plot result={result(2)} />)

    const gradients = container.querySelectorAll('linearGradient')
    expect(gradients).toHaveLength(2)
    expect(gradients[0].getAttribute('class')).toContain('text-chart-1')
    expect(gradients[1].getAttribute('class')).toContain('text-chart-2')

    const areas = [...container.querySelectorAll('path')].filter((path) =>
      path.getAttribute('fill')?.startsWith('url(#'),
    )
    expect(areas).toHaveLength(2)
    // Closed down to the baseline.
    expect(areas[0].getAttribute('d')).toMatch(/Z$/)
  })

  it('marks the latest sample of every series while nothing is hovered', () => {
    const { container } = render(<Plot result={result(3)} />)
    // A halo and a dot per series.
    expect(container.querySelectorAll('g circle')).toHaveLength(6)
  })

  it('writes the time along the bottom, and leaves it off when asked', () => {
    const full = render(<Plot result={result(1)} />)
    const times = [...full.container.querySelectorAll('text')].filter((text) =>
      text.getAttribute('y')?.startsWith(String(220 - 7)),
    )
    expect(times).toHaveLength(5)
    cleanup()

    const bare = render(<Plot result={result(1)} axisLabels={false} />)
    const none = [...bare.container.querySelectorAll('text')].filter((text) =>
      text.getAttribute('y')?.startsWith(String(220 - 7)),
    )
    expect(none).toHaveLength(0)
  })
})

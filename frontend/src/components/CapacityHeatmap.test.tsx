/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen, within } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { CapacityDimension, NodeCapacity } from '../api/types'
import { CapacityHeatmap } from './CapacityHeatmap'

/*
 * The arithmetic of the shades is pinned in `lib/capacityHeatmap.test.ts`; what
 * is pinned here is what only the rendered table can get wrong — a cell that
 * cannot answer drawing as a dash rather than 0%, a shade landing on the cell
 * it describes, the sort reaching the rows, and a node name opening its row.
 */

afterEach(cleanup)

function dimension(over: Partial<CapacityDimension> = {}): CapacityDimension {
  return {
    allocatable: 4000,
    requested: 0,
    limited: 0,
    used: 0,
    requested_percent: 0,
    limited_percent: 0,
    used_percent: 0,
    unlimited_containers: 0,
    ...over,
  }
}

function node(name: string, over: Partial<NodeCapacity> = {}): NodeCapacity {
  return {
    name,
    roles: ['worker'],
    ready: true,
    schedulable: true,
    taints: [],
    placeable: true,
    cpu: dimension(),
    memory: dimension({ allocatable: 8 << 30 }),
    pods: { allocatable: 110, scheduled: 11, percent: 10, without_requests: 0 },
    qos: { guaranteed: 0, burstable: 2, best_effort: 1 },
    headroom: { cpu: 4000, memory: 8 << 30, pods: 99 },
    concerns: [],
    severity: 'ok',
    top_requests: [],
    borrowing_pods: 0,
    top_borrowers: [],
    ...over,
  }
}

function rowOf(name: string): HTMLElement {
  const row = screen.getByRole('button', { name }).closest('tr')
  if (!row) throw new Error(`no row for ${name}`)
  return row
}

describe('CapacityHeatmap', () => {
  it('draws an unmeasured usage cell as a dash, never as 0%', () => {
    render(<CapacityHeatmap nodes={[node('n1')]} usageAvailable={false} onSelect={() => {}} />)
    const cells = within(rowOf('n1')).getAllByRole('cell')
    // Reserved, in use, limits — CPU first.
    expect(cells[1].textContent).toBe('—')
    expect(cells[1].getAttribute('title')).toContain('not measured')
    expect(cells[0].textContent).toBe('0%')
  })

  it('shades the cell past a threshold, and only that cell', () => {
    const full = node('full', {
      cpu: dimension({ requested: 4000, requested_percent: 100, limited_percent: 120 }),
    })
    render(<CapacityHeatmap nodes={[full]} usageAvailable={false} onSelect={() => {}} />)
    const cells = within(rowOf('full')).getAllByRole('cell')
    expect(cells[0].className).toContain('bg-heat-danger')
    // 120% CPU limits is below the 200% line: denser, never amber.
    expect(cells[2].className).toContain('bg-heat-2')
    expect(cells[2].className).not.toContain('bg-heat-warn')
  })

  it('leads with the worst node, and re-sorts when a column is asked for', () => {
    const nodes = [
      node('a-quiet', { cpu: dimension({ requested_percent: 70 }) }),
      node('b-broken', { severity: 'danger', cpu: dimension({ requested_percent: 20 }) }),
    ]
    render(<CapacityHeatmap nodes={nodes} usageAvailable={false} onSelect={() => {}} />)
    const names = () =>
      screen.getAllByRole('row').flatMap((row) => {
        const header = row.querySelector('th[scope="row"] .font-data')
        return header ? [header.textContent] : []
      })
    expect(names()).toEqual(['b-broken', 'a-quiet'])

    fireEvent.click(screen.getAllByRole('button', { name: 'Reserved' })[0])
    expect(names()).toEqual(['a-quiet', 'b-broken'])
  })

  it('separates the control plane from the workers, and says why a node takes nothing', () => {
    const plane = node('cp1', {
      roles: ['control-plane'],
      placeable: false,
      taints: ['node-role.kubernetes.io/control-plane:NoSchedule'],
    })
    render(<CapacityHeatmap nodes={[node('w1'), plane]} usageAvailable={false} onSelect={() => {}} />)
    expect(screen.getByText('control-plane · 1')).toBeTruthy()
    expect(screen.getByText('worker · 1')).toBeTruthy()
    expect(within(rowOf('cp1')).getByText('not placeable')).toBeTruthy()
  })

  it('opens the node it names', () => {
    const onSelect = vi.fn()
    render(<CapacityHeatmap nodes={[node('n1')]} usageAvailable onSelect={onSelect} />)
    fireEvent.click(screen.getByRole('button', { name: 'n1' }))
    expect(onSelect).toHaveBeenCalledWith('n1')
  })
})

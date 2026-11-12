/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { useMemo, useState } from 'react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Pod, Workload } from '../api/types'
import type { SelectedRow } from '../lib/selection'
import { ResourceView } from './ResourceTables'
import type { LoadedResource, OpenRowAction, RowSelection } from './ResourceTables'

/*
 * A 2000-row list redrew every row on each tick of a box and on every render of
 * the page around it (#309). These tests count row renders through the tone
 * each row reads as it draws: a pod row asks podTone twice, a workload row asks
 * workloadTone once.
 */
const renders = vi.hoisted(() => ({ pod: 0, workload: 0 }))
vi.mock('../lib/status', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../lib/status')>()
  return {
    ...actual,
    podTone: (...args: Parameters<typeof actual.podTone>) => {
      renders.pod++
      return actual.podTone(...args)
    },
    workloadTone: (...args: Parameters<typeof actual.workloadTone>) => {
      renders.workload++
      return actual.workloadTone(...args)
    },
  }
})

afterEach(cleanup)

const created = '2026-01-01T00:00:00Z'
const pods: Pod[] = Array.from({ length: 60 }, (_, i) => ({
  name: `pod-${i}`,
  namespace: 'team',
  phase: 'Running',
  node: 'node-1',
  ready: 1,
  total: 1,
  restarts: 0,
  created_at: created,
  containers: [],
  ephemeral_containers: [],
}))
const workloads: Workload[] = Array.from({ length: 60 }, (_, i) => ({
  kind: 'Deployment',
  name: `service-${i}`,
  namespace: 'team',
  ready: 1,
  desired: 1,
  images: [],
  created_at: created,
}))

/**
 * Page holds the selection and writes its handlers inline, as Explore does, so
 * every handler is new on every render.
 */
function Page({ loaded, onDelete }: { loaded: LoadedResource; onDelete: OpenRowAction }) {
  const [selected, setSelected] = useState<SelectedRow[]>([])
  const [, setTick] = useState(0)
  const keys = useMemo(() => new Set(selected.map((row) => row.key)), [selected])
  const selection: RowSelection = useMemo(
    () => ({
      has: (key) => keys.has(key),
      toggle: (row) =>
        setSelected((current) =>
          current.some((entry) => entry.key === row.key)
            ? current.filter((entry) => entry.key !== row.key)
            : [...current, row],
        ),
      setMany: (rows, checked) =>
        setSelected((current) => {
          const touched = new Set(rows.map((row) => row.key))
          const rest = current.filter((entry) => !touched.has(entry.key))
          return checked ? [...rest, ...rows] : rest
        }),
    }),
    [keys],
  )
  return (
    <MemoryRouter>
      <button type="button" onClick={() => setTick((tick) => tick + 1)}>
        render the page
      </button>
      <ResourceView
        loaded={loaded}
        selection={selection}
        onSelectPod={(pod) => pod}
        onManifest={(name) => name}
        onAction={(name) => name}
        onDelete={(row) => onDelete(row)}
      />
    </MemoryRouter>
  )
}

function checkbox(name: string) {
  return screen.getByRole<HTMLInputElement>('checkbox', { name: `Select ${name}` })
}

describe('a long resource list', () => {
  it('redraws only the pod row whose box was ticked', () => {
    render(<Page loaded={{ kind: 'pods', rows: pods, usage: null }} onDelete={() => {}} />)
    renders.pod = 0

    fireEvent.click(checkbox('pod-30'))

    expect(checkbox('pod-30').checked).toBe(true)
    expect(renders.pod).toBe(2)
  })

  it('redraws only the workload row whose box was ticked', () => {
    render(<Page loaded={{ kind: 'workloads', rows: workloads }} onDelete={() => {}} />)
    renders.workload = 0

    fireEvent.click(checkbox('service-30'))

    expect(checkbox('service-30').checked).toBe(true)
    expect(renders.workload).toBe(1)
  })

  it('redraws no row when the page renders around unchanged data', () => {
    render(<Page loaded={{ kind: 'pods', rows: pods, usage: null }} onDelete={() => {}} />)
    renders.pod = 0

    fireEvent.click(screen.getByRole('button', { name: 'render the page' }))

    expect(renders.pod).toBe(0)
  })

  it('still selects every row shown from the header', () => {
    render(<Page loaded={{ kind: 'workloads', rows: workloads }} onDelete={() => {}} />)

    fireEvent.click(screen.getByRole('checkbox', { name: 'Select every row shown' }))

    expect(screen.getAllByRole<HTMLInputElement>('checkbox').every((box) => box.checked)).toBe(true)
  })

  it('calls the handler the page passed last, though the row did not redraw', () => {
    const first = vi.fn()
    const second = vi.fn()
    const loaded: LoadedResource = { kind: 'pods', rows: pods, usage: null }
    const { rerender } = render(<Page loaded={loaded} onDelete={first} />)
    rerender(<Page loaded={loaded} onDelete={second} />)

    fireEvent.click(screen.getByRole('button', { name: 'Actions for pod-7' }))
    fireEvent.click(screen.getByRole('menuitem', { name: 'Delete' }))

    expect(first).not.toHaveBeenCalled()
    expect(second).toHaveBeenCalledWith(expect.objectContaining({ name: 'pod-7', kind: 'pods' }))
  })
})

/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, describe, expect, it } from 'vitest'

import { completeWaste, type ClusterWaste, type WasteWorkload } from '../api/waste'
import { WorkloadSheet, WorkloadsSection } from './Waste'

/*
 * The page's one reading that needs history must say when it has none, and a
 * reading it could not make must be a dash with a reason — never a zero, which
 * on a page about unused capacity is the one wrong answer that looks right.
 */

const MI = 1024 * 1024

const workload: WasteWorkload = {
  kind: 'Deployment',
  namespace: 'shop',
  name: 'web',
  replicas: 2,
  since: '2026-09-01T00:00:00Z',
  reclaimable: { cpu: 1710, memory: 0 },
  containers: [
    {
      name: 'app',
      cpu: { request: 1000, observed: 120, recommended: 145, over: true },
      memory: { request: 256 * MI, observed: 200 * MI, recommended: 240 * MI, over: false },
    },
    {
      name: 'log',
      cpu: { over: false },
      memory: { over: false },
      note: 'No usage was recorded for this container in the window.',
    },
  ],
}

function report(over: Partial<ClusterWaste>): ClusterWaste {
  return completeWaste({
    window: { span: '7d', start: '2026-10-07T00:00:00Z', end: '2026-10-14T00:00:00Z' },
    workloads_available: true,
    ...over,
  })
}

function section(data: ClusterWaste, isAdmin: boolean) {
  return render(
    <MemoryRouter>
      <WorkloadsSection
        report={data}
        span="7d"
        busy={false}
        isAdmin={isAdmin}
        clusterId={4}
        onOpen={() => {}}
      />
    </MemoryRouter>,
  )
}

afterEach(cleanup)

describe('the workloads section', () => {
  it('says why there is no sizing, and offers an admin the way to fix it', () => {
    const data = report({
      workloads_available: false,
      workloads_reason: 'This cluster has no metrics datasource registered.',
    })
    section(data, true)
    expect(screen.getByText(/no metrics datasource registered/)).toBeTruthy()
    const link = screen.getByRole('link', { name: /register a metrics datasource/i })
    expect(link.getAttribute('href')).toBe('/clusters/4/dashboard')
  })

  it('tells a non-admin who can fix it rather than linking a page they cannot use', () => {
    section(report({ workloads_available: false, workloads_reason: 'No datasource.' }), false)
    expect(screen.queryByRole('link', { name: /register/i })).toBeNull()
    expect(screen.getByText(/An administrator can register/)).toBeTruthy()
  })

  it('explains an empty list by what was checked and what was not sized', () => {
    const data = report({
      summary: {
        reclaimable_cpu: 0,
        reclaimable_memory: 0,
        unused_storage: 0,
        idle_load_balancers: 0,
        workloads_checked: 12,
        workloads_short_history: 2,
        workloads_unmeasured: 1,
      },
    })
    const { container } = section(data, false)
    expect(container.textContent).toMatch(/Checked 12 workloads/)
    expect(container.textContent).toMatch(/2 have less than a day of history/)
    expect(container.textContent).toMatch(/1 had no usage recorded/)
  })

  it('lists what each workload gives back, with a dash where it gives back nothing', () => {
    section(report({ workloads: [workload], workloads_total: 1 }), false)
    expect(screen.getByRole('button', { name: /web/ })).toBeTruthy()
    expect(screen.getByText('1.71 cores')).toBeTruthy()
    expect(screen.getAllByText('—').length).toBeGreaterThan(0)
  })
})

describe('the workload sheet', () => {
  it('draws an unmeasured reading as a dash carrying its reason, never a zero', () => {
    render(<WorkloadSheet workload={workload} report={report({})} onClose={() => {}} />)
    const dashes = screen.getAllByTitle('No usage was recorded for this container in the window.')
    expect(dashes.length).toBe(2)
    expect(screen.getAllByTitle('No request is declared').length).toBe(2)
  })

  it('shows the patch for the over-asked request only', () => {
    render(<WorkloadSheet workload={workload} report={report({})} onClose={() => {}} />)
    const patch = screen.getByText(/cpu: 145m/)
    expect(patch.textContent).not.toMatch(/memory:/)
    expect(screen.getByText(/kubectl -n shop patch deployment web/)).toBeTruthy()
  })

  it('says when a rollout is in progress', () => {
    render(
      <WorkloadSheet workload={{ ...workload, rollout: true }} report={report({})} onClose={() => {}} />,
    )
    expect(screen.getByText(/a rollout is in progress/)).toBeTruthy()
  })
})

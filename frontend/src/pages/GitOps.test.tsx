/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import { completeGitopsRecord } from '../api/gitops'
import { RecordSheet, RecordsTable } from './GitOps'

vi.mock('../state/inventory-context', () => ({ useInventory: () => ({ discovered: [], categories: [] }) }))

afterEach(cleanup)

const drifted = completeGitopsRecord({
  tool: 'argocd', namespace: 'argocd', name: 'shop', sync: 'out_of_sync', health: 'healthy',
  resources: [
    { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'web', sync: 'out_of_sync' },
    { group: '', kind: 'Service', namespace: 'shop', name: 'web-svc', sync: 'synced' },
    { group: 'example.com', kind: 'Widget', namespace: 'shop', name: 'gizmo', sync: 'synced' },
  ],
  resources_hidden: 2,
})
const flux = completeGitopsRecord({
  tool: 'flux-kustomization', namespace: 'flux-system', name: 'apps', sync: 'synced', health: 'healthy',
  resources: [{ group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'api', sync: 'unknown' }],
})

describe('RecordsTable', () => {
  it("counts Argo CD's drifted objects and leaves Flux's count a dash", () => {
    render(<RecordsTable records={[drifted, flux]} onOpen={() => {}} />)
    const rows = screen.getAllByRole('row').slice(1)
    expect(rows[0].textContent).toContain('Out of sync')
    expect(rows[0].lastElementChild?.textContent).toBe('1')
    expect(rows[1].lastElementChild?.textContent).toBe('—')
  })

  it('says so when nothing is visible, rather than drawing an empty table', () => {
    render(<RecordsTable records={[]} onOpen={() => {}} />)
    expect(screen.getByText(/No record manages anything/)).toBeTruthy()
  })
})

describe('RecordSheet', () => {
  it('opens a known kind, leaves an unknown one as text, and counts what the grant hides', () => {
    const opened: string[] = []
    render(
      <RecordSheet record={drifted} onClose={() => {}} onOpenObject={(target) => opened.push(`${target.kind}/${target.name}`)} />,
    )

    fireEvent.click(screen.getByRole('button', { name: 'web' }))
    expect(opened).toEqual(['deployments/web'])
    expect(screen.queryByRole('button', { name: 'gizmo' })).toBeNull()
    expect(screen.getByText('2 objects are outside your grant and not shown.')).toBeTruthy()
  })

  it('never offers to open another cluster’s objects', () => {
    render(
      <RecordSheet record={{ ...drifted, remote: 'edge-eu' }} onClose={() => {}} onOpenObject={() => {}} />,
    )
    expect(screen.queryByRole('button', { name: 'web' })).toBeNull()
  })
})

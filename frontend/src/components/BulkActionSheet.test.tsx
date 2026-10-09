/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { afterEach, describe, expect, it, vi } from 'vitest'

import type { Cluster } from '../api/types'
import type { SelectedRow } from '../lib/selection'
import { BulkActionSheet } from './BulkActionSheet'

/*
 * The managed-by notice on a selection. A selection has no describe behind it,
 * so what manages each row comes off the list row; asserted here is that it is
 * said before the click, against the rows it is about, and only for the acts
 * the manager actually runs into.
 */

vi.mock('../api/client', () => ({
  deleteResourceObject: vi.fn(),
  restartWorkload: vi.fn(),
  runCronJob: vi.fn(),
  setNodeSchedulable: vi.fn(),
  suspendWorkload: vi.fn(),
  errorMessage: (_err: unknown, fallback: string) => fallback,
}))

const cluster = { id: 7, name: 'prod' } as Cluster

const rows: SelectedRow[] = [
  {
    key: 'deployments/shop/api',
    kind: 'deployments',
    label: 'Deployment',
    name: 'api',
    namespace: 'shop',
    managedBy: { manager: 'argocd', name: 'shop', reverts: false },
  },
  {
    key: 'deployments/shop/worker',
    kind: 'deployments',
    label: 'Deployment',
    name: 'worker',
    namespace: 'shop',
  },
]

afterEach(cleanup)

describe('BulkActionSheet', () => {
  it('says which selected rows something else manages before a delete', () => {
    render(<BulkActionSheet cluster={cluster} action="delete" rows={rows} onClose={() => {}} />)

    expect(screen.getByText(/1 of the 2 selected may be recreated/)).toBeTruthy()
    expect(screen.getByText('Managed by Argo CD application shop')).toBeTruthy()
    // The unmanaged row is not marked.
    expect(screen.getAllByText(/^Managed by/)).toHaveLength(1)
  })

  it('says nothing about a restart a GitOps tool leaves alone', () => {
    render(<BulkActionSheet cluster={cluster} action="restart" rows={rows} onClose={() => {}} />)

    expect(screen.queryByText(/selected/)).toBeNull()
    expect(screen.queryByText(/^Managed by/)).toBeNull()
  })
})

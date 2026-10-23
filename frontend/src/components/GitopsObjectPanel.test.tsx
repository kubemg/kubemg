/**
 * @vitest-environment jsdom
 */
import { cleanup, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { completeGitopsObject, completeGitopsRecord, type GitopsObject } from '../api/gitops'
import type { Cluster } from '../api/types'
import { invalidateQueries } from '../lib/query'
import { GitopsObjectPanel } from './GitopsObjectPanel'

// The drawer's GitOps panel says whether this object still matches Git in the
// controller's own words — and for Flux, that the per-object answer is unknown
// rather than a pass.

let answer: GitopsObject

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return { ...actual, fetchGitopsObject: () => Promise.resolve(answer) }
})

const cluster = { id: 4, name: 'edge' } as Cluster

function draw() {
  render(
    <MemoryRouter>
      <GitopsObjectPanel cluster={cluster} kind="deployments" name="web" namespace="shop" />
    </MemoryRouter>,
  )
}

beforeEach(() => invalidateQueries())
afterEach(cleanup)

describe('GitopsObjectPanel', () => {
  it("shows the object's own drift and what self-heal will do", async () => {
    answer = completeGitopsObject({
      tracked: true,
      record: completeGitopsRecord({
        tool: 'argocd', namespace: 'argocd', name: 'shop', sync: 'synced', health: 'healthy', auto_heal: false,
        sources: [{ repo: 'https://git.example.com/shop.git', path: 'prod' }],
      }),
      entry: { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'web', sync: 'out_of_sync', health: 'healthy' },
    })
    draw()

    // The object's own line wins over the record's.
    expect(await screen.findByText('Out of sync')).toBeTruthy()
    expect(screen.queryByText('Synced')).toBeNull()
    expect(screen.getByText(/a hand edit stays until the next sync/)).toBeTruthy()
    expect(screen.getByText('argocd/shop')).toBeTruthy()
  })

  it('says Flux does not record per-object drift instead of claiming a pass', async () => {
    answer = completeGitopsObject({
      tracked: true,
      record: completeGitopsRecord({
        tool: 'flux-kustomization', namespace: 'flux-system', name: 'apps', sync: 'synced', health: 'healthy', auto_heal: true,
      }),
      entry: { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'web', sync: 'unknown' },
    })
    draw()

    expect(await screen.findByText('Unknown')).toBeTruthy()
    expect(screen.getByText(/corrects drift rather than recording it/)).toBeTruthy()
  })

  it('explains a record that is not in this cluster', async () => {
    answer = completeGitopsObject({
      tracked: true,
      reason: 'no Application named shop in this cluster — Argo CD may run in another cluster and deploy here',
    })
    draw()

    expect(await screen.findByText(/may run in another cluster/)).toBeTruthy()
  })
})

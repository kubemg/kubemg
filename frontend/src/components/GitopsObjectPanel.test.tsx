/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react'
import { MemoryRouter } from 'react-router'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import {
  completeGitopsDiff,
  completeGitopsObject,
  completeGitopsRecord,
  type GitopsDiff,
  type GitopsObject,
} from '../api/gitops'
import type { Cluster } from '../api/types'
import { invalidateQueries } from '../lib/query'
import { GitopsObjectPanel } from './GitopsObjectPanel'

// The drawer's GitOps panel says whether this object still matches Git in the
// controller's own words — and for Flux, that the per-object answer is unknown
// rather than a pass.

let answer: GitopsObject
let diffAnswer: GitopsDiff
const diffCalls = vi.fn()

vi.mock('../api/client', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../api/client')>()
  return {
    ...actual,
    fetchGitopsObject: () => Promise.resolve(answer),
    fetchGitopsDiff: (...args: unknown[]) => {
      diffCalls(...args)
      return Promise.resolve(diffAnswer)
    },
  }
})

function argoObject(
  sync: 'synced' | 'out_of_sync',
  diff?: GitopsObject['diff'],
  consoleURL?: string,
): GitopsObject {
  return completeGitopsObject({
    tracked: true,
    record: completeGitopsRecord({
      tool: 'argocd', namespace: 'argocd', name: 'shop', sync, health: 'healthy', console_url: consoleURL,
    }),
    entry: { group: 'apps', kind: 'Deployment', namespace: 'shop', name: 'web', sync, health: 'healthy' },
    diff,
  })
}

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

  it('offers the diff on an out-of-sync object and opens it live against desired', async () => {
    answer = argoObject('out_of_sync', { available: true })
    diffAnswer = completeGitopsDiff({
      available: true,
      modified: true,
      application: { namespace: 'argocd', name: 'shop' },
      diff: { changes: [{ path: 'spec.replicas', kind: 'changed', old: 3, new: 1 }], truncated: false },
      live: 'spec:\n  replicas: 3\n',
      desired: 'spec:\n  replicas: 1\n',
    })
    draw()

    fireEvent.click(await screen.findByRole('button', { name: /Show diff/ }))
    expect(await screen.findByText('spec.replicas')).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: 'Live' })).toBeTruthy()
    expect(screen.getByRole('columnheader', { name: 'Desired' })).toBeTruthy()
    expect(diffCalls).toHaveBeenCalledWith(4, 'deployments', 'web', 'shop')
  })

  it('leaves the button out and says why without a source', async () => {
    answer = argoObject('out_of_sync', { available: false, reason: 'no Argo CD datasource is registered for this cluster' })
    draw()

    expect(await screen.findByText(/no Argo CD datasource is registered/)).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Show diff/ })).toBeNull()
  })

  it('offers no diff for an object in sync', async () => {
    answer = argoObject('synced', { available: true })
    draw()

    expect(await screen.findByText('Synced')).toBeTruthy()
    expect(screen.queryByRole('button', { name: /Show diff/ })).toBeNull()
  })

  it('names the reason when Argo CD cannot answer', async () => {
    answer = argoObject('out_of_sync', { available: true })
    diffAnswer = completeGitopsDiff({ available: false, reason: 'Argo CD no longer accepts the stored token' })
    draw()

    fireEvent.click(await screen.findByRole('button', { name: /Show diff/ }))
    expect(await screen.findByText(/no longer accepts the stored token/)).toBeTruthy()
  })

  it('links the record to its page in Argo CD, and says where Sync lives', async () => {
    answer = argoObject('synced', undefined, 'https://argo.example.com/applications/argocd/shop')
    draw()

    const link = await screen.findByRole('link', { name: /Open in Argo CD/ })
    expect(link.getAttribute('href')).toBe('https://argo.example.com/applications/argocd/shop')
    expect(link.getAttribute('target')).toBe('_blank')
    expect(screen.getByText(/kubemg does not sync/)).toBeTruthy()
  })

  it('draws no link without a console, and no link at all for Flux', async () => {
    answer = argoObject('synced')
    draw()
    expect(await screen.findByText(/link it under the cluster’s consoles/)).toBeTruthy()
    expect(screen.queryByRole('link', { name: /Open in Argo CD/ })).toBeNull()
    cleanup()
    invalidateQueries()

    answer = completeGitopsObject({
      tracked: true,
      record: completeGitopsRecord({ tool: 'flux-kustomization', namespace: 'flux-system', name: 'apps', sync: 'synced' }),
    })
    draw()
    expect(await screen.findByText(/Flux puts a hand edit back/)).toBeTruthy()
    expect(screen.queryByRole('link', { name: /Argo CD/ })).toBeNull()
    expect(screen.queryByText(/kubemg does not sync/)).toBeNull()
  })

  it('carries the link into the diff sheet and names the last change through kubemg', async () => {
    answer = argoObject('out_of_sync', { available: true }, 'https://argo.example.com/applications/argocd/shop')
    diffAnswer = completeGitopsDiff({
      available: true,
      modified: true,
      diff: { changes: [], truncated: false },
      attribution: {
        change: { username: 'alice', verb: 'patch', at: new Date().toISOString() },
        since: '2026-10-24T10:00:00Z',
        scoped_to_self: false,
      },
    })
    draw()

    fireEvent.click(await screen.findByRole('button', { name: /Show diff/ }))
    const sheet = await screen.findByRole('region', { name: 'Last change through kubemg' })
    expect(sheet.textContent).toContain('alice')
    expect(sheet.textContent).toContain('patch')
    expect(sheet.textContent).toContain('outside kubemg')
    expect(screen.getAllByRole('link', { name: /Open in Argo CD/ })).toHaveLength(2)
  })

  it('says a change outside kubemg leaves no record, rather than that nobody changed it', async () => {
    answer = argoObject('out_of_sync', { available: true })
    diffAnswer = completeGitopsDiff({
      available: true,
      modified: true,
      diff: { changes: [], truncated: false },
      attribution: {
        change: null,
        scoped_to_self: true,
        note: 'You made no write to this object through KubeMG since Argo CD last synced it. Writes by others are shown to an administrator only. A change made outside KubeMG leaves no record here.',
      },
    })
    draw()

    fireEvent.click(await screen.findByRole('button', { name: /Show diff/ }))
    expect(await screen.findByText(/Writes by others are shown to an administrator only/)).toBeTruthy()
    expect(screen.queryByRole('link', { name: /Open in Argo CD/ })).toBeNull()
  })
})

import { errorMessage, fetchGitopsDiff } from '../api/client'
import type { Cluster } from '../api/types'
import { queryKey, useCachedQuery } from '../lib/query'
import { ManifestDiffView } from './ManifestDiffView'
import { Notice, Sheet } from './primitives'

/*
 * Live against desired, for one object Argo CD tracks. The desired side is
 * Argo CD's own — the object as a sync would leave it, normalized the way its
 * diff view shows it — so what reads as a difference here is what Argo CD
 * itself counts as one. The table reads live → desired: what a sync would
 * change.
 */

export function GitopsDiffSheet({
  cluster,
  kind,
  name,
  namespace,
  onClose,
}: {
  cluster: Cluster
  kind: string
  name: string
  namespace?: string
  onClose: () => void
}) {
  const state = useCachedQuery(queryKey('gitops-diff', cluster.id, kind, namespace, name), () =>
    fetchGitopsDiff(cluster.id, kind, name, namespace),
  )
  const view = state.data

  return (
    <Sheet
      width="xl"
      eyebrow="Live against desired"
      title={
        <>
          Diff for <span className="font-data text-accent">{name}</span>
        </>
      }
      onClose={onClose}
    >
      {state.error ? (
        <Notice tone="warn">{errorMessage(state.error, 'Could not compare this object.')}</Notice>
      ) : state.loading && !view ? (
        <p className="text-[12.5px] text-muted">Asking Argo CD for the desired state…</p>
      ) : !view?.available ? (
        <Notice tone="warn">{view?.reason ?? 'The desired state could not be read.'}</Notice>
      ) : (
        <>
          <p className="text-[12.5px] text-muted">
            {view.modified
              ? 'Argo CD counts this object out of sync. The table is what a sync would change.'
              : 'Argo CD counts this object in sync.'}
            {view.application ? (
              <>
                {' '}
                Desired state from{' '}
                <span className="font-data text-fg">
                  {view.application.namespace}/{view.application.name}
                </span>
                .
              </>
            ) : null}
          </p>
          {view.diff ? (
            <ManifestDiffView
              diff={view.diff}
              beforeLabel="Live"
              afterLabel="Desired"
              emptyLabel="Live and desired state match, once Argo CD's normalization is applied."
            />
          ) : null}
          <ManifestSide title="Live" yaml={view.live} truncated={view.live_truncated} />
          <ManifestSide title="Desired" yaml={view.desired} truncated={view.desired_truncated} />
        </>
      )}
    </Sheet>
  )
}

function ManifestSide({ title, yaml, truncated }: { title: string; yaml: string; truncated: boolean }) {
  if (!yaml) return null
  return (
    <details className="rounded-control border border-line-soft">
      <summary className="cursor-pointer px-3 py-2 text-[12.5px] font-semibold text-fg">
        {title} manifest
      </summary>
      <pre className="max-h-96 overflow-auto border-t border-line-soft bg-raised p-3 font-mono text-[12px] text-fg">
        {yaml}
      </pre>
      {truncated ? (
        <p className="border-t border-line-soft px-3 py-2 text-[12px] text-muted">
          Cut at 256 KiB — the rest of this side is not shown.
        </p>
      ) : null}
    </details>
  )
}

import { ChevronDown } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router-dom'
import { useClusters } from '../state/clusters-context'
import { EnvironmentDot } from './primitives'

/* The selector sits on ink in the rail and on paper in the collapsed top bar,
   so it carries both palettes rather than being duplicated. */
const TONE = {
  ink: {
    shell: 'border-ink-line bg-ink-raised hover:border-ink-muted/40',
    text: 'text-ink-fg',
    muted: 'text-ink-muted',
    option: 'bg-ink-raised',
  },
  paper: {
    shell: 'border-line bg-surface hover:border-faint',
    text: 'text-fg',
    muted: 'text-muted',
    option: 'bg-surface',
  },
}

/**
 * ClusterSelector jumps straight to a cluster. It reflects the cluster you are
 * looking at, so it never shows a selection the page does not act on.
 */
export function ClusterSelector({ tone = 'paper' }: { tone?: keyof typeof TONE }) {
  const { clusters, loading, select } = useClusters()
  const navigate = useNavigate()
  const { pathname } = useLocation()
  const skin = TONE[tone]

  const match = /^\/clusters\/(\d+)$/.exec(pathname)
  const currentId = match ? Number(match[1]) : null
  const current = clusters.find((cluster) => cluster.id === currentId) ?? null

  if (clusters.length === 0) {
    return (
      <p className={`rounded-[5px] border px-2.5 py-1.5 text-[12px] ${skin.shell} ${skin.muted}`}>
        {loading ? 'Loading clusters…' : 'No clusters'}
      </p>
    )
  }

  return (
    <div
      className={`relative flex items-center gap-2 rounded-[5px] border px-2.5 transition-colors focus-within:border-primary ${skin.shell}`}
    >
      {current ? (
        <EnvironmentDot environment={current.environment} />
      ) : (
        <span aria-hidden="true" className="inline-block size-1.5 shrink-0 rounded-full bg-faint" />
      )}
      <select
        aria-label="Jump to cluster"
        value={current?.id ?? ''}
        onChange={(event) => {
          const id = Number(event.target.value)
          select(id)
          navigate(`/clusters/${id}`)
        }}
        className={`w-full cursor-pointer appearance-none bg-transparent py-1.5 pr-5 font-mono text-[12.5px] focus:outline-none ${skin.text}`}
      >
        {current ? null : (
          <option value="" disabled>
            Select a cluster
          </option>
        )}
        {clusters.map((cluster) => (
          <option key={cluster.id} value={cluster.id} className={`${skin.option} ${skin.text}`}>
            {cluster.name}
          </option>
        ))}
      </select>
      <ChevronDown
        aria-hidden="true"
        className={`pointer-events-none absolute right-2 size-3.5 ${skin.muted}`}
      />
    </div>
  )
}

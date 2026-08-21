import { useEffect, useRef, useState } from 'react'
import { ChevronDown } from 'lucide-react'
import { useLocation, useNavigate } from 'react-router'
import type { Cluster } from '../api/types'
import { clusterSlotHref, currentClusterSlot } from '../lib/navigation'
import { useClusters } from '../state/clusters-context'
import { ClusterMenu } from './ClusterMenu'
import { EnvironmentDot } from './primitives'

/**
 * ClusterSwitcher stands in for the header's plain title once a cluster is
 * open: a cluster is a place, not a page, and the fastest way out of one is
 * into another rather than back through the fleet list first.
 *
 * The list itself is `ClusterMenu`, shared with the tree's own switcher — the
 * header is the trigger, not a second inventory. Switching keeps the slot that
 * is open, so Pods stays Pods on the cluster you land on.
 */
export function ClusterSwitcher({ cluster }: { cluster: Cluster }) {
  const { clusters } = useClusters()
  const { pathname } = useLocation()
  const navigate = useNavigate()

  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement | null>(null)

  const slot = currentClusterSlot(pathname, cluster.id)

  useEffect(() => {
    if (!open) return
    function onOutside(event: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(event.target as Node)) setOpen(false)
    }
    window.addEventListener('mousedown', onOutside)
    return () => window.removeEventListener('mousedown', onOutside)
  }, [open])

  return (
    <div ref={rootRef} className="relative min-w-0">
      <button
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        onClick={() => setOpen((current) => !current)}
        className="flex min-w-0 items-center gap-1.5 rounded-control py-1 pr-1.5 pl-1 text-[15px] font-semibold text-fg transition-colors hover:bg-raised"
      >
        <EnvironmentDot environment={cluster.environment} />
        <span className="min-w-0 truncate font-mono">{cluster.name}</span>
        <ChevronDown
          aria-hidden="true"
          className={`size-3.5 shrink-0 text-faint transition-transform ${open ? 'rotate-180' : ''}`}
        />
      </button>

      {open ? (
        <ClusterMenu
          clusters={clusters}
          currentId={cluster.id}
          onPick={(target) => {
            setOpen(false)
            navigate(clusterSlotHref(target, slot))
          }}
          onFleet={() => {
            setOpen(false)
            navigate('/')
          }}
          onClose={() => setOpen(false)}
          className="absolute top-full left-0 z-20 mt-1.5"
        />
      ) : null}
    </div>
  )
}

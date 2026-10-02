import { useCallback, useEffect, useMemo, useState } from 'react'
import type { KeyboardEvent } from 'react'
import {
  Box,
  Boxes,
  Cloud,
  Ellipsis,
  Globe,
  Network,
  RefreshCw,
  Route,
  Waypoints,
} from 'lucide-react'
import { errorMessage, fetchTrafficMap } from '../api/client'
import type { Cluster, TrafficMap, TrafficNode, TrafficState } from '../api/types'
import type { ResourceKey } from '../lib/resources'
import { resourceItem, resourceSingular } from '../lib/resources'
import {
  NODE_HEIGHT,
  NODE_WIDTH,
  PADDING,
  isProblem,
  layoutTraffic,
  tracePath,
  trafficProblems,
} from '../lib/trafficMap'
import { useInventory } from '../state/inventory-context'
import type { DetailTarget } from './ResourceDetailDrawer'
import { IconButton, Notice, Pill } from './primitives'

/**
 * Where this object's traffic goes, drawn: entry → route → Service → workload →
 * pods, with every join that fails drawn as the failure it is.
 *
 * Pointing at a hop lights its whole path — everything upstream and downstream
 * of it — and fades the rest, which is how a route with six backends stays
 * readable. Every hop that is an object opens in this same drawer.
 *
 * Nothing on it moves. The deck's rule is that nothing loops, so there is no
 * travelling packet along the edges: the map is impressive because it is
 * clear, and the only change of state is the fade when a hop is pointed at,
 * which honours reduced motion.
 */
export function TrafficMapTab({
  cluster,
  kind,
  name,
  namespace,
  onOpen,
}: {
  cluster: Cluster
  kind: ResourceKey
  name: string
  namespace: string
  onOpen?: (target: DetailTarget) => void
}) {
  const { discovered } = useInventory()
  const [map, setMap] = useState<TrafficMap | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [focus, setFocus] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setMap(await fetchTrafficMap(cluster.id, kind, name, namespace))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not draw where this traffic goes.'))
    } finally {
      setLoading(false)
    }
  }, [cluster.id, kind, name, namespace])

  useEffect(() => {
    void load()
  }, [load])

  /** The drawer target a node opens, or null for a node that is not an object
      this console can address. */
  const targetOf = useCallback(
    (node: TrafficNode): DetailTarget | null => {
      if (!node.resource) return null
      let key = node.resource as ResourceKey
      if (node.api_group) {
        const item = discovered
          .flatMap((category) => category.items)
          .find((entry) => entry.custom?.group === node.api_group && entry.custom?.plural === node.resource)
        if (!item) return null
        key = item.key
      }
      const item = resourceItem(key, discovered)
      if (!item) return null
      return {
        kind: key,
        label: resourceSingular(item),
        name: node.name,
        namespace: node.namespace,
        pod: node.pod,
      }
    },
    [discovered],
  )

  if (loading && !map) return <p className="text-[13px] text-muted">Following the traffic…</p>
  if (error && !map) return <Notice tone="error">{error}</Notice>
  if (!map) return null

  return (
    <TrafficMapView
      map={map}
      focus={focus}
      onFocus={setFocus}
      loading={loading}
      onRefresh={() => void load()}
      targetOf={targetOf}
      onOpen={onOpen}
      error={error}
    />
  )
}

const KIND_ICON: Record<string, typeof Box> = {
  Host: Globe,
  Gateway: Waypoints,
  Mesh: Waypoints,
  Ingress: Route,
  HTTPRoute: Route,
  VirtualService: Route,
  Service: Network,
  External: Cloud,
  Pod: Box,
  More: Ellipsis,
}

/** The box's edge and its state stripe, per state. Dashed is "not known". */
const NODE_TONE: Record<TrafficState, { edge: string; stripe: string; dashed: boolean }> = {
  ok: { edge: 'stroke-line', stripe: 'fill-ok', dashed: false },
  warn: { edge: 'stroke-warn', stripe: 'fill-warn', dashed: false },
  bad: { edge: 'stroke-danger', stripe: 'fill-danger', dashed: false },
  unchecked: { edge: 'stroke-line', stripe: 'fill-line', dashed: true },
  denied: { edge: 'stroke-faint', stripe: 'fill-faint', dashed: true },
  outside: { edge: 'stroke-faint', stripe: 'fill-faint', dashed: true },
}

const EDGE_TONE: Record<TrafficState, { stroke: string; marker: string; dashed: boolean }> = {
  ok: { stroke: 'stroke-faint', marker: 'traffic-arrow', dashed: false },
  warn: { stroke: 'stroke-warn', marker: 'traffic-arrow-warn', dashed: false },
  bad: { stroke: 'stroke-danger', marker: 'traffic-arrow-bad', dashed: false },
  unchecked: { stroke: 'stroke-faint', marker: 'traffic-arrow', dashed: true },
  denied: { stroke: 'stroke-faint', marker: 'traffic-arrow', dashed: true },
  outside: { stroke: 'stroke-faint', marker: 'traffic-arrow', dashed: true },
}

const PILL_TONE: Record<TrafficState, 'ok' | 'warn' | 'bad' | 'idle'> = {
  ok: 'ok',
  warn: 'warn',
  bad: 'bad',
  unchecked: 'idle',
  denied: 'idle',
  outside: 'idle',
}

const STATE_WORD: Record<TrafficState, string> = {
  ok: 'ok',
  warn: 'degraded',
  bad: 'broken',
  unchecked: 'not checked',
  denied: 'refused',
  outside: 'outside your access',
}

/** Cuts a string to fit a box, by an average Inter glyph width. */
function clip(text: string, width: number, size: number): string {
  const max = Math.floor(width / (size * 0.56))
  return text.length > max ? `${text.slice(0, Math.max(1, max - 1))}…` : text
}

/** The drawing and the list under it, without the read — so it is testable. */
export function TrafficMapView({
  map,
  focus,
  onFocus,
  loading,
  onRefresh,
  targetOf,
  onOpen,
  error,
}: {
  map: TrafficMap
  focus: string | null
  onFocus: (id: string | null) => void
  loading?: boolean
  onRefresh?: () => void
  targetOf: (node: TrafficNode) => DetailTarget | null
  onOpen?: (target: DetailTarget) => void
  error?: string | null
}) {
  const layout = useMemo(() => layoutTraffic(map), [map])
  const lit = useMemo(() => (focus ? tracePath(map, focus) : null), [map, focus])
  const problems = useMemo(() => trafficProblems(map), [map])
  const byId = useMemo(() => new Map(map.nodes.map((node) => [node.id, node])), [map])

  const open = (node: TrafficNode | undefined) => {
    if (!node || !onOpen) return
    const target = targetOf(node)
    if (target) onOpen(target)
  }

  const onKey = (event: KeyboardEvent, node: TrafficNode) => {
    if (event.key === 'Enter' || event.key === ' ') {
      event.preventDefault()
      open(node)
    }
  }

  const broken = problems.filter((entry) => entry.state === 'bad').length

  return (
    <div className="flex min-w-0 flex-col gap-4">
      <div className="flex flex-wrap items-center gap-2">
        <p className="min-w-0 flex-1 text-[13px] text-muted">
          {problems.length === 0
            ? 'Every hop on this path answers.'
            : broken > 0
              ? `${broken} ${broken === 1 ? 'hop is' : 'hops are'} broken on this path.`
              : `${problems.length} ${problems.length === 1 ? 'hop needs' : 'hops need'} a look.`}{' '}
          Point at a hop to trace its path; open one to read it.
        </p>
        {onRefresh ? (
          <IconButton label="Read the path again" onClick={onRefresh} disabled={loading}>
            <RefreshCw aria-hidden="true" className="size-4" />
          </IconButton>
        ) : null}
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}

      <div
        className="overflow-x-auto rounded-card border border-line-soft bg-sunken"
        style={{
          backgroundImage: 'radial-gradient(var(--color-line-soft) 1px, transparent 1px)',
          backgroundSize: '16px 16px',
        }}
      >
        {/* Fitted to the drawer, down to four fifths of its own size — any
            smaller and the type stops being readable, so past that it scrolls. */}
        <svg
          role="img"
          aria-label="Traffic map"
          viewBox={`0 0 ${layout.width} ${layout.height}`}
          style={{ width: '100%', maxWidth: layout.width, minWidth: layout.width * 0.8 }}
          className="block h-auto"
        >
          <defs>
            {(
              [
                ['traffic-arrow', 'fill-faint'],
                ['traffic-arrow-warn', 'fill-warn'],
                ['traffic-arrow-bad', 'fill-danger'],
              ] as const
            ).map(([id, fill]) => (
              <marker
                key={id}
                id={id}
                viewBox="0 0 8 8"
                refX="7"
                refY="4"
                markerWidth="7"
                markerHeight="7"
                orient="auto-start-reverse"
              >
                <path d="M 0 0 L 8 4 L 0 8 z" className={fill} />
              </marker>
            ))}
          </defs>

          {layout.columns.map((column) => (
            <text
              key={column.title}
              x={column.x}
              y={PADDING + 12}
              className="fill-faint text-[11.5px] font-semibold"
            >
              {column.title}
            </text>
          ))}

          {layout.edges.map(({ edge, path }) => {
            const tone = EDGE_TONE[edge.state]
            const dim = lit && !lit.edges.has(edge)
            return (
              <path
                key={`${edge.from}->${edge.to}`}
                d={path}
                fill="none"
                strokeWidth={lit && !dim ? 2 : 1.5}
                strokeDasharray={tone.dashed ? '5 4' : undefined}
                markerEnd={`url(#${tone.marker})`}
                className={`${tone.stroke} transition-opacity duration-200 motion-reduce:transition-none ${dim ? 'opacity-20' : ''}`}
              >
                <title>{[...edge.labels, edge.problem].filter(Boolean).join('\n')}</title>
              </path>
            )
          })}

          {layout.edges.map(({ edge, labelX, labelY, labelWidth }) => {
            if (edge.labels.length === 0) return null
            const text = clip(
              edge.labels[0] + (edge.labels.length > 1 ? ` +${edge.labels.length - 1}` : ''),
              labelWidth - 10,
              11,
            )
            const width = Math.min(labelWidth, text.length * 6.4 + 12)
            const dim = lit && !lit.edges.has(edge)
            return (
              <g
                key={`label:${edge.from}->${edge.to}`}
                className={`transition-opacity duration-200 motion-reduce:transition-none ${dim ? 'opacity-20' : ''}`}
              >
                <title>{edge.labels.join('\n')}</title>
                <rect
                  x={labelX - width / 2}
                  y={labelY - 9}
                  width={width}
                  height={18}
                  rx={6}
                  className={`fill-surface ${edge.state === 'bad' ? 'stroke-danger' : 'stroke-line-soft'}`}
                />
                <text
                  x={labelX}
                  y={labelY + 4}
                  textAnchor="middle"
                  className={`font-data text-[11px] ${edge.state === 'bad' ? 'fill-danger' : 'fill-muted'}`}
                >
                  {text}
                </text>
              </g>
            )
          })}

          {layout.nodes.map(({ node, x, y }) => {
            const tone = NODE_TONE[node.state]
            const Icon = KIND_ICON[node.kind] ?? Boxes
            const target = onOpen ? targetOf(node) : null
            const root = node.id === map.root
            const dim = lit && !lit.nodes.has(node.id)
            const line = node.problem && isProblem(node.state, node.problem)
              ? node.problem
              : (node.detail[0] ?? '')
            const lineTone =
              node.problem && node.state === 'bad'
                ? 'fill-danger'
                : node.problem && node.state === 'warn'
                  ? 'fill-warn'
                  : 'fill-muted'
            return (
              <g
                key={node.id}
                role={target ? 'button' : undefined}
                tabIndex={target ? 0 : -1}
                aria-label={`${node.kind} ${node.name}, ${STATE_WORD[node.state]}${node.problem ? `: ${node.problem}` : ''}`}
                onMouseEnter={() => onFocus(node.id)}
                onMouseLeave={() => onFocus(null)}
                onFocus={() => onFocus(node.id)}
                onBlur={() => onFocus(null)}
                onClick={() => target && open(node)}
                onKeyDown={(event) => onKey(event, node)}
                className={`outline-none transition-opacity duration-200 motion-reduce:transition-none ${target ? 'cursor-pointer' : ''} ${dim ? 'opacity-25' : ''} [&:focus-visible>rect:first-of-type]:stroke-accent`}
              >
                <title>
                  {[
                    `${node.kind} ${node.namespace ? `${node.namespace}/` : ''}${node.name}`,
                    ...node.detail,
                    node.problem,
                  ]
                    .filter(Boolean)
                    .join('\n')}
                </title>
                <rect
                  x={x}
                  y={y}
                  width={NODE_WIDTH}
                  height={NODE_HEIGHT}
                  rx={10}
                  strokeWidth={root ? 2 : 1}
                  strokeDasharray={tone.dashed ? '5 4' : undefined}
                  className={`fill-surface ${root ? 'stroke-accent' : tone.edge} ${target ? 'hover:fill-raised' : ''}`}
                />
                <rect x={x + 1} y={y + 10} width={3} height={NODE_HEIGHT - 20} rx={1.5} className={tone.stripe} />
                <Icon x={x + 12} y={y + 9} width={14} height={14} className="text-muted" aria-hidden="true" />
                <text x={x + 32} y={y + 20} className="fill-faint text-[11px] font-medium">
                  {clip(node.kind === 'More' ? 'Pods' : node.kind, NODE_WIDTH - 44, 11)}
                </text>
                <text x={x + 12} y={y + 41} className="fill-fg font-data text-[13px] font-semibold">
                  {clip(node.name, NODE_WIDTH - 24, 13)}
                </text>
                <text x={x + 12} y={y + 57} className={`${lineTone} text-[11.5px]`}>
                  {clip(line, NODE_WIDTH - 24, 11.5)}
                </text>
              </g>
            )
          })}
        </svg>
      </div>

      <Legend />

      {problems.length > 0 ? (
        <section className="flex flex-col gap-2">
          <h3 className="label text-faint">What needs a look</h3>
          <ul className="flex flex-col divide-y divide-line-soft rounded-card border border-line-soft">
            {problems.map((entry, index) => {
              const node = byId.get(entry.id)
              const target = node && onOpen ? targetOf(node) : null
              return (
                <li
                  key={`${entry.id}-${index}`}
                  onMouseEnter={() => onFocus(entry.id)}
                  onMouseLeave={() => onFocus(null)}
                  className="flex flex-wrap items-baseline gap-x-2 gap-y-1 px-3 py-2 text-[13px]"
                >
                  <Pill tone={PILL_TONE[entry.state]}>{STATE_WORD[entry.state]}</Pill>
                  <span className="font-data text-fg">{entry.subject}</span>
                  <span className="min-w-0 flex-1 text-muted">{entry.problem}</span>
                  {target ? (
                    <button
                      type="button"
                      onClick={() => open(node)}
                      className="text-[12.5px] text-accent hover:underline"
                    >
                      Open
                    </button>
                  ) : null}
                </li>
              )
            })}
          </ul>
        </section>
      ) : null}

      {map.notes.length > 0 ? (
        <ul className="flex flex-col gap-1 text-[12px] leading-snug text-muted">
          {map.notes.map((note) => (
            <li key={note}>{note}</li>
          ))}
        </ul>
      ) : null}
    </div>
  )
}

function Legend() {
  const items: Array<{ label: string; className: string; dashed?: boolean }> = [
    { label: 'Answers', className: 'bg-ok' },
    { label: 'Degraded', className: 'bg-warn' },
    { label: 'Broken', className: 'bg-danger' },
    { label: 'Not checked, refused or outside your access', className: 'bg-faint', dashed: true },
  ]
  return (
    <ul className="flex flex-wrap gap-x-4 gap-y-1 text-[12px] text-muted">
      {items.map((item) => (
        <li key={item.label} className="flex items-center gap-1.5">
          <span aria-hidden="true" className={`inline-block h-2.5 w-1 rounded-full ${item.className}`} />
          {item.label}
        </li>
      ))}
    </ul>
  )
}

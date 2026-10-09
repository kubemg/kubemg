import { Fragment, useState } from 'react'
import { ArrowDown, ArrowUp } from 'lucide-react'
import type { NodeCapacity } from '../api/types'
import {
  HEAT_COLUMNS,
  groupNodes,
  heatLevel,
  heatValue,
  qosLine,
  sortNodes,
} from '../lib/capacityHeatmap'
import type { HeatColumn, HeatLevel, HeatSort } from '../lib/capacityHeatmap'
import { formatCPU, formatMemory } from '../lib/units'

/**
 * Every node as one row and every ceiling as one cell: a fleet of two hundred
 * nodes as one screen. The rows below this card carry the bars and the
 * server's sentences; this is where somebody finds which of those rows to
 * open. The shade is the verdict, the number is the reading, and the colour
 * logic lives in `lib/capacityHeatmap.ts` so it can be pinned.
 */

const LEVEL_CLASS: Record<HeatLevel, string> = {
  unknown: 'text-faint',
  plain: '',
  'heat-1': 'bg-heat-1',
  'heat-2': 'bg-heat-2',
  'heat-3': 'bg-heat-3',
  warn: 'bg-heat-warn font-semibold',
  danger: 'bg-heat-danger font-semibold',
}

const PERCENT = new Intl.NumberFormat(undefined, { style: 'percent', maximumFractionDigits: 0 })

const RESOURCE_SPANS = [
  { resource: 'CPU', span: 3 },
  { resource: 'Memory', span: 3 },
  { resource: 'Pods', span: 1 },
] as const

/** cellTitle says what a cell measures in units, for the pointer and the screen reader. */
function cellTitle(node: NodeCapacity, column: HeatColumn, value: number | null): string {
  const subject = `${node.name} — ${column.resource} ${column.label.toLowerCase()}`
  if (value === null) {
    return column.usage
      ? `${subject}: not measured — the Metrics API has no reading for this node`
      : `${subject}: the node reports no allocatable capacity`
  }
  if (column.key === 'pods') {
    return `${subject}: ${node.pods.scheduled} of ${node.pods.allocatable} slots`
  }
  const dimension = column.resource === 'CPU' ? node.cpu : node.memory
  const format = column.resource === 'CPU' ? formatCPU : formatMemory
  const amount =
    column.key.endsWith('_requested')
      ? dimension.requested
      : column.key.endsWith('_used')
        ? dimension.used
        : dimension.limited
  return `${subject}: ${format(amount)} of ${format(dimension.allocatable)}`
}

function SortHeader({
  label,
  sort,
  active,
  onSort,
  className = '',
}: {
  label: string
  sort: HeatSort
  active: HeatSort
  onSort: (sort: HeatSort) => void
  className?: string
}) {
  const current = active === sort
  const Arrow = sort === 'name' ? ArrowUp : ArrowDown
  return (
    <th
      scope="col"
      aria-sort={current ? (sort === 'name' ? 'ascending' : 'descending') : undefined}
      className={`px-2 py-1.5 font-semibold ${className}`}
    >
      <button
        type="button"
        onClick={() => onSort(sort)}
        className={`inline-flex items-center gap-1 rounded-chip hover:text-fg ${current ? 'text-fg' : ''}`}
      >
        {label}
        {current ? <Arrow aria-hidden="true" className="size-3" /> : null}
      </button>
    </th>
  )
}

export function CapacityHeatmap({
  nodes,
  usageAvailable,
  onSelect,
}: {
  nodes: readonly NodeCapacity[]
  usageAvailable: boolean
  /** Opens a node's row below, with its bars, concerns and borrowers. */
  onSelect: (name: string) => void
}) {
  const [sort, setSort] = useState<HeatSort>('severity')
  const groups = groupNodes(sortNodes(nodes, sort, usageAvailable))
  const grouped = groups.length > 1

  return (
    <div className="min-w-0">
      <div className="overflow-x-auto">
        <table className="w-full min-w-[760px] border-separate border-spacing-0 text-[12.5px]">
          <thead className="text-left text-muted">
            <tr>
              <th scope="col" className="px-2 pt-1" />
              {RESOURCE_SPANS.map(({ resource, span }) => (
                <th
                  key={resource}
                  scope="colgroup"
                  colSpan={span}
                  className="border-b border-line-soft px-2 pt-1 pb-1 text-center font-semibold text-fg"
                >
                  {resource}
                </th>
              ))}
              <th scope="col" className="px-2 pt-1" />
              <th scope="col" className="px-2 pt-1" />
            </tr>
            <tr>
              <SortHeader label="Node" sort="name" active={sort} onSort={setSort} />
              {HEAT_COLUMNS.map((column) => (
                <SortHeader
                  key={column.key}
                  label={column.label}
                  sort={column.key}
                  active={sort}
                  onSort={setSort}
                  className="text-right"
                />
              ))}
              <th scope="col" className="px-2 py-1.5 font-semibold">
                Unreserved
              </th>
              <th scope="col" className="px-2 py-1.5 font-semibold">
                QoS
              </th>
            </tr>
          </thead>
          <tbody>
            {groups.map((group) => (
              <Fragment key={group.role}>
                {grouped ? (
                  <tr>
                    <th
                      scope="colgroup"
                      colSpan={HEAT_COLUMNS.length + 3}
                      className="px-2 pt-3 pb-1 text-left text-[12px] font-semibold text-muted"
                    >
                      {group.role} · {group.nodes.length}
                    </th>
                  </tr>
                ) : null}
                {group.nodes.map((node) => (
                  <tr key={node.name} className="[&>td]:border-t [&>td]:border-line-soft">
                    <th scope="row" className="max-w-56 px-2 py-1.5 text-left font-normal">
                      <button
                        type="button"
                        onClick={() => onSelect(node.name)}
                        className="flex max-w-full min-w-0 items-center gap-1.5 text-left hover:underline"
                        title={`Open ${node.name} below`}
                      >
                        <span
                          aria-hidden="true"
                          className={`size-1.5 shrink-0 rounded-full ${
                            node.severity === 'danger'
                              ? 'bg-danger'
                              : node.severity === 'warn'
                                ? 'bg-warn'
                                : node.severity === 'note'
                                  ? 'bg-faint'
                                  : 'bg-ok'
                          }`}
                        />
                        <span className="truncate font-data text-fg">{node.name}</span>
                      </button>
                      {!node.ready || !node.schedulable || !node.placeable ? (
                        <span className="block pl-3 text-[11.5px] text-faint">
                          {!node.ready
                            ? 'not ready'
                            : !node.schedulable
                              ? 'cordoned'
                              : 'tainted — ordinary pods not placed'}
                        </span>
                      ) : null}
                    </th>
                    {HEAT_COLUMNS.map((column) => {
                      const value = heatValue(node, column.key, usageAvailable)
                      return (
                        <td
                          key={column.key}
                          title={cellTitle(node, column, value)}
                          className={`px-2 py-1.5 text-right font-data text-fg tabular-nums ${LEVEL_CLASS[heatLevel(value, column)]}`}
                        >
                          {value === null ? (
                            <span className="text-faint">—</span>
                          ) : (
                            PERCENT.format(value / 100)
                          )}
                        </td>
                      )
                    })}
                    <td className="px-2 py-1.5 font-data whitespace-nowrap text-muted tabular-nums">
                      {node.placeable ? (
                        `${formatCPU(node.headroom.cpu)} · ${formatMemory(node.headroom.memory)}`
                      ) : (
                        <span className="text-faint">not placeable</span>
                      )}
                    </td>
                    <td className="px-2 py-1.5 whitespace-nowrap text-muted">
                      {qosLine(node.qos) || <span className="text-faint">—</span>}
                    </td>
                  </tr>
                ))}
              </Fragment>
            ))}
          </tbody>
        </table>
      </div>

      {/* The legend states the thresholds in words rather than leaving the
          colours to explain themselves: a shade is only as good as the line
          it stands for. */}
      <div className="mt-3 flex flex-wrap items-center gap-x-4 gap-y-1.5 text-[12px] text-muted">
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden="true" className="inline-flex overflow-hidden rounded-chip border border-line-soft">
            <span className="size-3" />
            <span className="size-3 bg-heat-1" />
            <span className="size-3 bg-heat-2" />
            <span className="size-3 bg-heat-3" />
          </span>
          denser toward the warning line
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden="true" className="size-3 rounded-chip bg-heat-warn" />
          past it
        </span>
        <span className="inline-flex items-center gap-1.5">
          <span aria-hidden="true" className="size-3 rounded-chip bg-heat-danger" />
          exhausted
        </span>
        <span className="text-faint">
          Warning lines: reserved and slots 90%, memory in use 90%, CPU limits 200%, memory limits
          100%. CPU in use has none — every pod still gets the CPU it requested.
        </span>
      </div>
    </div>
  )
}

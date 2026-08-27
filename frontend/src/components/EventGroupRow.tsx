import { ChevronRight } from 'lucide-react'
import type { EventGroup } from '../api/types'
import { Button } from './primitives'
import { relativeAge } from '../lib/time'

/*
 * One row of a grouped events list, lifted out of the cluster-wide timeline so
 * a namespace's page can draw the same thing. It is the same row and not a
 * second rendering of one: the grouping is the server's, and two surfaces
 * folding the cluster's own words differently is exactly the disagreement a
 * shared component prevents.
 */

/**
 * One group: an object, and everything the cluster said about it. Collapsed it
 * is one line — the newest reason, the count and when it last happened, which is
 * what somebody scanning the page is reading. Opened it is the reasons, each
 * already folded across every Event object that carried it.
 */
export function EventGroupRow({
  group,
  open,
  onToggle,
  onExplore,
}: {
  group: EventGroup
  open: boolean
  onToggle: () => void
  onExplore?: () => void
}) {
  const warning = group.type === 'Warning'

  return (
    <li>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-start gap-3 px-4 py-3 text-left transition-colors hover:bg-raised/60"
      >
        <ChevronRight
          aria-hidden="true"
          className={`mt-0.5 size-3.5 shrink-0 text-faint transition-transform ${open ? 'rotate-90' : ''}`}
        />
        <span
          aria-hidden="true"
          className={`mt-1.5 size-1.5 shrink-0 rounded-full ${warning ? 'bg-warn' : 'bg-muted'}`}
        />

        <span className="flex min-w-0 flex-1 flex-col gap-0.5">
          <span className="flex flex-wrap items-baseline gap-x-2">
            <span className="text-[11px] text-faint">{group.object.kind}</span>
            <span className="truncate font-mono text-[13px] text-fg">
              {group.object.namespace ? (
                <span className="text-faint">{group.object.namespace}/</span>
              ) : null}
              {group.object.name}
            </span>
            <span className={`text-[12.5px] ${warning ? 'text-warn' : 'text-muted'}`}>
              {group.reason}
            </span>
          </span>
          {/* The newest message, which is the one describing the state now. */}
          <span className="truncate text-[12.5px] text-muted">{group.message}</span>
        </span>

        {/* A count of firings, not of rows: 41 means the cluster said it 41
            times, which is the number that tells a flake from a loop. */}
        {group.count > 1 ? (
          <span className="shrink-0 font-mono text-[12px] text-faint">×{group.count}</span>
        ) : null}
        <span className="shrink-0 text-[12px] text-muted">{relativeAge(group.last_seen)}</span>
      </button>

      {open ? (
        <div className="flex flex-col gap-2 border-t border-line-soft bg-raised/30 px-4 py-3 pl-10">
          {group.entries.map((entry) => (
            <div key={`${entry.type}/${entry.reason}`} className="flex flex-col gap-0.5">
              <span className="flex flex-wrap items-baseline gap-x-2">
                <span
                  className={`text-[12.5px] font-medium ${
                    entry.type === 'Warning' ? 'text-warn' : 'text-fg'
                  }`}
                >
                  {entry.reason}
                </span>
                {entry.count > 1 ? (
                  <span className="font-mono text-[11.5px] text-faint">×{entry.count}</span>
                ) : null}
                {entry.source ? (
                  <span className="text-[11.5px] text-faint">{entry.source}</span>
                ) : null}
                {/* First and last, because "started twenty minutes ago and is
                    still going" is a different problem from "happened once". */}
                <span className="text-[11.5px] text-faint">
                  {entry.first_seen && entry.first_seen !== entry.last_seen
                    ? `${relativeAge(entry.first_seen)} → ${relativeAge(entry.last_seen)}`
                    : relativeAge(entry.last_seen)}
                </span>
              </span>
              <span className="text-[12.5px] leading-relaxed text-muted">{entry.message}</span>
            </div>
          ))}

          {group.entries_truncated ? (
            <p className="text-[12px] text-faint">
              This object produced more distinct reasons than are shown; its own Describe tab reads
              only its events.
            </p>
          ) : null}

          {onExplore ? (
            <div>
              <Button size="sm" onClick={onExplore}>
                Open {group.object.namespace || 'the namespace'} in Explore
              </Button>
            </div>
          ) : null}
        </div>
      ) : null}
    </li>
  )
}

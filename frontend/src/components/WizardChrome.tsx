import type { ReactNode } from 'react'
import { Check } from 'lucide-react'
import { LinkStrand } from './LinkStrand'

/**
 * The shared chrome behind KubeMG's two wizards — first-run setup and cluster
 * registration.
 *
 * Both are genuinely sequences rather than tabbed forms: a step commits
 * something the next one acts on, and a step past the furthest one reached is
 * not yet meaningful. Numbering them says so, and the strand between markers is
 * the same device the fleet uses to draw a connection that has or has not been
 * made. It lives here rather than in either page because the two appear minutes
 * apart on a fresh install — setup hands straight over to registration — and
 * reading as one device is the whole point.
 */
export function Stepper({
  steps,
  current,
  furthest,
  onSelect,
}: {
  steps: readonly string[]
  current: number
  /** The highest step reachable so far; anything past it is not yet meaningful. */
  furthest: number
  onSelect: (step: number) => void
}) {
  return (
    <ol className="flex items-center gap-1.5">
      {steps.map((label, index) => {
        const done = index < current
        const active = index === current
        const reachable = index <= furthest

        return (
          <li key={label} className="flex min-w-0 flex-1 items-center gap-1.5">
            <button
              type="button"
              disabled={!reachable}
              onClick={() => onSelect(index)}
              className={`flex min-w-0 items-center gap-2 rounded-control px-1.5 py-1 transition-colors ${
                reachable ? 'hover:bg-raised' : 'cursor-not-allowed opacity-45'
              }`}
            >
              <span
                className={`grid size-6 shrink-0 place-items-center rounded-full font-mono text-[11.5px] font-semibold ${
                  done
                    ? 'bg-ok-soft text-ok'
                    : active
                      ? 'bg-accent text-on-accent'
                      : 'bg-raised text-muted'
                }`}
              >
                {done ? <Check aria-hidden="true" className="size-3.5" /> : index + 1}
              </span>
              <span
                className={`hidden truncate text-[13px] sm:block ${
                  active ? 'font-medium text-fg' : 'text-muted'
                }`}
              >
                {label}
              </span>
            </button>
            {index < steps.length - 1 ? (
              <LinkStrand state={done ? 'direct' : 'idle'} size="sm" className="min-w-4 flex-1" />
            ) : null}
          </li>
        )
      })}
    </ol>
  )
}

/** StepActions is the consistent footer every step ends with. */
export function StepActions({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center justify-end gap-2 border-t border-line-soft bg-raised/40 px-4 py-3">
      {children}
    </div>
  )
}

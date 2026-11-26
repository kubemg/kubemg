import { useEffect, useId, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Search } from 'lucide-react'

/** A set of verbs under one heading in the picker. */
export interface VerbGroup {
  label: string
  verbs: readonly string[]
}

/**
 * VerbPicker is the audit trail's verb filter as one control: a button that
 * says what is selected, and a panel of the verbs grouped by what they are, with
 * a search over them. It replaced a row of a chip per verb, two lines of them
 * above the table before a single record.
 *
 * The selection is still a set, and it is still the page's: this only draws it
 * and reports a change. The panel is portalled and placed from the trigger's
 * rect for the reason RowMenu is: the filter bar sits in a card that clips, and
 * an `absolute` panel would be cut off by it.
 */
export function VerbPicker({
  groups,
  selected,
  onChange,
}: {
  groups: readonly VerbGroup[]
  selected: string[]
  onChange: (next: string[]) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [rect, setRect] = useState<{ top: number; left: number } | null>(null)
  const triggerRef = useRef<HTMLButtonElement | null>(null)
  const panelRef = useRef<HTMLDivElement | null>(null)
  const searchRef = useRef<HTMLInputElement | null>(null)
  const panelId = useId()

  function place() {
    const box = triggerRef.current?.getBoundingClientRect()
    if (box) setRect({ top: box.bottom + 4, left: box.left })
  }

  function close() {
    setOpen(false)
    setQuery('')
    triggerRef.current?.focus()
  }

  // Focus goes to the search as the panel opens, without scrolling the page
  // under it: the panel is fixed, and a jump would move the filter bar away
  // from the panel it opened.
  useEffect(() => {
    if (open) searchRef.current?.focus({ preventScroll: true })
  }, [open, rect])

  useEffect(() => {
    if (!open) return
    function onPointerDown(event: MouseEvent) {
      const target = event.target as Node
      if (triggerRef.current?.contains(target) || panelRef.current?.contains(target)) return
      setOpen(false)
      setQuery('')
    }
    function onKeyDown(event: KeyboardEvent) {
      if (event.key === 'Escape') close()
    }
    document.addEventListener('mousedown', onPointerDown)
    document.addEventListener('keydown', onKeyDown)
    window.addEventListener('scroll', place, true)
    window.addEventListener('resize', place)
    return () => {
      document.removeEventListener('mousedown', onPointerDown)
      document.removeEventListener('keydown', onKeyDown)
      window.removeEventListener('scroll', place, true)
      window.removeEventListener('resize', place)
    }
  }, [open])

  const label =
    selected.length === 0 ? 'All verbs' : selected.length === 1 ? selected[0] : `${selected.length} verbs`
  const needle = query.trim().toLowerCase()
  const shown = groups
    .map((group) => ({ ...group, verbs: group.verbs.filter((verb) => verb.includes(needle)) }))
    .filter((group) => group.verbs.length > 0)

  function toggle(verb: string) {
    onChange(selected.includes(verb) ? selected.filter((entry) => entry !== verb) : [...selected, verb])
  }

  return (
    <>
      <button
        ref={triggerRef}
        type="button"
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-label={`Filter by verb: ${label}`}
        onClick={() => {
          place()
          setOpen((current) => !current)
        }}
        className={`inline-flex h-8 shrink-0 items-center gap-1.5 rounded-control border px-2.5 text-[13px] transition-colors ${
          selected.length > 0
            ? 'border-accent-line bg-accent-soft font-medium text-accent'
            : 'border-line bg-surface text-muted hover:bg-raised hover:text-fg'
        }`}
      >
        <span className={selected.length === 1 ? 'font-data text-[12.5px]' : undefined}>{label}</span>
        <ChevronDown aria-hidden="true" className="size-3.5" />
      </button>
      {open && rect
        ? createPortal(
            <div
              ref={panelRef}
              id={panelId}
              role="dialog"
              aria-label="Verbs"
              style={{ top: rect.top, left: rect.left }}
              className="pop-in card fixed z-40 flex max-h-[min(70vh,28rem)] w-64 flex-col p-1 lift"
            >
              <label className="relative m-1 flex items-center">
                <Search aria-hidden="true" className="pointer-events-none absolute left-2.5 size-3.5 text-faint" />
                <input
                  ref={searchRef}
                  type="search"
                  value={query}
                  onChange={(event) => setQuery(event.target.value)}
                  placeholder="Find a verb…"
                  aria-label="Find a verb"
                  className="h-8 w-full rounded-control border border-line bg-surface pr-2 pl-8 text-[13px] text-fg placeholder:text-faint focus:border-accent focus:outline-none"
                />
              </label>
              <div className="min-h-0 overflow-y-auto px-1 pb-1">
                {shown.map((group) => (
                  <fieldset key={group.label} className="mt-2 first:mt-1">
                    <legend className="label px-2 pb-1">{group.label}</legend>
                    {group.verbs.map((verb) => (
                      <label
                        key={verb}
                        className="flex cursor-pointer items-center gap-2 rounded-control px-2 py-1.5 text-[13px] text-fg hover:bg-raised"
                      >
                        <input
                          type="checkbox"
                          checked={selected.includes(verb)}
                          onChange={() => toggle(verb)}
                          className="size-3.5 accent-[var(--color-accent)]"
                        />
                        <span className="font-data text-[12.5px]">{verb}</span>
                      </label>
                    ))}
                  </fieldset>
                ))}
                {shown.length === 0 ? <p className="px-2 py-3 text-[12.5px] text-muted">No verb matches.</p> : null}
              </div>
              {selected.length > 0 ? (
                <button
                  type="button"
                  onClick={() => onChange([])}
                  className="m-1 rounded-control px-2 py-1.5 text-left text-[12.5px] text-accent hover:bg-raised"
                >
                  Clear - every verb
                </button>
              ) : null}
            </div>,
            document.body,
          )
        : null}
    </>
  )
}

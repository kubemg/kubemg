import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronRight, Eye, EyeOff, PanelLeft, SlidersHorizontal } from 'lucide-react'
import { errorMessage, fetchCRDVisibility, fetchCRDs, saveCRDVisibility } from '../api/client'
import type { Cluster, CustomResourceDefinition } from '../api/types'
import { useInventory } from '../state/inventory-context'
import { IntegrationGroup, IntegrationTile } from './IntegrationTile'
import { Button, Notice, Panel, Pill, SearchInput, Segmented, Sheet, Switch } from './primitives'

/**
 * Which of this cluster's custom resources the Explore sidebar offers.
 *
 * The sidebar's custom-resource sections are derived from the cluster's own CRD
 * list, which is the only way to browse an operator nobody here has heard of.
 * The cost of deriving them is that a cluster running three operators declares a
 * hundred kinds, and most of them are one operator talking to itself — a lock,
 * an internal revision, a generated certificate request. They are reachable, and
 * nobody browses them.
 *
 * So an administrator curates the list and everybody on the cluster gets what
 * they curated. Two things this panel has to keep saying out loud:
 *
 *   - **Hiding is not refusing.** A kind off this list is off the navigation and
 *     nothing else — what may actually be read is the cluster's own RBAC to
 *     decide, and `kubectl get` disproves any other reading in one command.
 *   - **The default is shown.** What is stored is the hidden set, so an operator
 *     installed tomorrow arrives in the sidebar rather than silently missing
 *     from it.
 *
 * The editor is a `Sheet` rather than the panel body because the list is as long
 * as the cluster is busy: the panel says how much has been curated, and the
 * sheet is where it is done.
 */
export function CrdVisibilityPanel({
  cluster,
  className,
  bare = false,
}: {
  cluster: Cluster
  className?: string
  /** Drawn as a group inside somebody else's panel rather than as its own. */
  bare?: boolean
}) {
  const inventory = useInventory()
  const [crds, setCrds] = useState<CustomResourceDefinition[] | null>(null)
  const [hidden, setHidden] = useState<string[]>([])
  const [editable, setEditable] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState(false)

  const load = useCallback(async () => {
    try {
      // Two reads and not one: the CRD list is what the cluster serves *now*,
      // and the stored set is what was curated — which may still name a kind
      // whose operator is currently uninstalled. Saving carries those forward
      // rather than dropping them, or reinstalling an operator would silently
      // put its internals back in everybody's sidebar.
      const [list, visibility] = await Promise.all([
        fetchCRDs(cluster.id),
        fetchCRDVisibility(cluster.id),
      ])
      setCrds(list)
      setHidden(visibility.hidden)
      setEditable(visibility.editable)
      setError(null)
    } catch (err) {
      setCrds([])
      setError(errorMessage(err, 'Could not load this cluster’s custom resources.'))
    }
  }, [cluster.id])

  useEffect(() => {
    void load()
  }, [load])

  // Nothing to curate and no way to curate it is not worth a panel — but a read
  // that *failed* is not the same as one that answered "nothing here", and this
  // panel is only ever drawn for an administrator in the first place. Hiding it
  // on an error is how a server that predates this route, or a cluster whose
  // agent cannot list CRDs, reads as a feature that was never shipped: nothing
  // on the page, and nothing saying why. So an error keeps the panel and says so.
  if (error === null && (!editable || (crds !== null && crds.length === 0 && hidden.length === 0))) {
    return null
  }

  const total = crds?.length ?? 0
  const hiddenHere = (crds ?? []).filter((crd) => hidden.includes(resourceKey(crd))).length
  const groups = groupByAPIGroup(crds ?? [], '')
    .map(({ group, items }) => ({
      group,
      total: items.length,
      shown: items.filter((crd) => !hidden.includes(resourceKey(crd))).length,
    }))
    .sort((a, b) => b.total - a.total || a.group.localeCompare(b.group))
  const visibleGroups = groups.slice(0, GROUP_PREVIEW)

  const chooseButton = (
    <Button
      variant="secondary"
      size="sm"
      disabled={!editable || crds === null}
      onClick={() => setEditing(true)}
    >
      <SlidersHorizontal className="size-3.5" />
      Choose
    </Button>
  )

  const tile = (
    <IntegrationTile
      icon={PanelLeft}
      title="Custom resources"
      wired
      meta={
        crds === null || error ? undefined : (
          <>
            <span className="font-data text-fg tabular-nums">{total - hiddenHere}</span> of{' '}
            <span className="font-data tabular-nums">{total}</span> in the sidebar ·{' '}
            <span className="font-data tabular-nums">{groups.length}</span>{' '}
            {groups.length === 1 ? 'API group' : 'API groups'}
          </>
        )
      }
      state={
        crds === null || error ? undefined : hiddenHere === 0 ? (
          <Pill tone="idle">All shown</Pill>
        ) : (
          <Pill tone="idle">{hiddenHere} hidden</Pill>
        )
      }
      link={
        <span className="text-[12px] text-faint">
          Navigation only — the cluster’s RBAC still decides what can be read.
        </span>
      }
      actions={chooseButton}
    >
      {error ? (
        <Notice tone="error">{error}</Notice>
      ) : crds === null ? (
        <p className="text-muted">Loading…</p>
      ) : (
        <ul className="flex flex-wrap gap-1.5" aria-label="Custom resources by API group">
          {visibleGroups.map(({ group, shown, total: count }) => (
            <li
              key={group}
              title={`${shown} of ${count} shown`}
              className={`inline-flex max-w-full items-center gap-1.5 rounded-chip border px-2 py-0.5 ${
                shown === 0 ? 'border-dashed border-line text-faint' : 'border-line-soft bg-surface text-fg'
              }`}
            >
              <span className="truncate font-data text-[12px]">{group}</span>
              <span className="shrink-0 font-data text-[11.5px] text-faint tabular-nums">
                {shown === count ? count : `${shown}/${count}`}
              </span>
            </li>
          ))}
          {groups.length > visibleGroups.length ? (
            <li className="inline-flex items-center px-1 text-[12px] text-faint">
              +{groups.length - visibleGroups.length} more
            </li>
          ) : null}
        </ul>
      )}
    </IntegrationTile>
  )

  const sheet = editing ? (
    <CrdVisibilitySheet
      clusterName={cluster.name}
      crds={crds ?? []}
      hidden={hidden}
      onClose={() => setEditing(false)}
      onSaved={(next) => {
        setHidden(next)
        setEditing(false)
        // The tree is drawn from a session cache, so a curation that only
        // took effect on the next reload would read as one that did not save.
        inventory.refresh()
      }}
      save={(next) => saveCRDVisibility(cluster.id, next).then((result) => result.hidden)}
    />
  ) : null

  if (bare) {
    return (
      <IntegrationGroup
        title="Explore sidebar"
        description="Which of this cluster’s CRDs everybody browsing it is offered."
      >
        {tile}
        {sheet}
      </IntegrationGroup>
    )
  }

  return (
    <>
      <Panel
        eyebrow="Explore"
        title="Custom resources in the sidebar"
        description={DESCRIPTION}
        className={className}
        bodyClassName="p-4"
      >
        {tile}
      </Panel>
      {sheet}
    </>
  )
}

const DESCRIPTION =
  'Which of this cluster’s CRDs everybody browsing it is offered. This is what the navigation shows — it is not a permission.'

/** How many API groups the summary names before it says how many more. */
const GROUP_PREVIEW = 8

/** resourceKey is how a resource is named unambiguously, here and on the wire. */
function resourceKey(crd: CustomResourceDefinition): string {
  return `${crd.plural}.${crd.group}`
}

type SheetFilter = 'all' | 'shown' | 'hidden'

/**
 * Past this many custom resources the groups start folded: a cluster running a
 * dozen operators is a hundred rows, and the group headers — each with its own
 * count and its own switch — are the overview. A filter opens every group it
 * matches, since a search that answers behind a fold has not answered.
 */
const FOLD_ABOVE = 24

function CrdVisibilitySheet({
  clusterName,
  crds,
  hidden,
  onClose,
  onSaved,
  save,
}: {
  clusterName: string
  crds: CustomResourceDefinition[]
  hidden: string[]
  onClose: () => void
  onSaved: (next: string[]) => void
  save: (next: string[]) => Promise<string[]>
}) {
  const [draft, setDraft] = useState<Set<string>>(() => new Set(hidden))
  const [filter, setFilter] = useState('')
  const [view, setView] = useState<SheetFilter>('all')
  const [open, setOpen] = useState<Set<string>>(() =>
    crds.length > FOLD_ABOVE ? new Set() : new Set(crds.map((crd) => crd.group)),
  )
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Entries naming a kind this cluster no longer serves are carried through
  // untouched: they are somebody's decision about an operator that may come
  // back, and this editor cannot show a row for a CRD that is not installed.
  const served = useMemo(() => new Set(crds.map(resourceKey)), [crds])
  const absent = useMemo(() => hidden.filter((key) => !served.has(key)), [hidden, served])

  // The Shown/Hidden views filter on the draft as it stood when the view was
  // picked, not on every toggle — a row that vanished the moment it was
  // switched would be impossible to switch back.
  const [frozen, setFrozen] = useState<Set<string>>(() => new Set(hidden))

  const groups = useMemo(() => {
    const matched = groupByAPIGroup(crds, filter)
    if (view === 'all') return matched
    return matched
      .map(({ group, items }) => ({
        group,
        items: items.filter((crd) => frozen.has(resourceKey(crd)) === (view === 'hidden')),
      }))
      .filter(({ items }) => items.length > 0)
  }, [crds, filter, view, frozen])

  const searching = filter.trim() !== '' || view !== 'all'

  function setKeys(keys: string[], show: boolean) {
    setDraft((current) => {
      const next = new Set(current)
      for (const key of keys) {
        if (show) next.delete(key)
        else next.add(key)
      }
      return next
    })
  }

  function toggleOpen(group: string) {
    setOpen((current) => {
      const next = new Set(current)
      if (next.has(group)) next.delete(group)
      else next.add(group)
      return next
    })
  }

  async function submit() {
    setBusy(true)
    try {
      const next = [...draft].filter((key) => served.has(key)).concat(absent)
      onSaved(await save(next))
    } catch (err) {
      setError(errorMessage(err, 'Could not save which resources the sidebar offers.'))
      setBusy(false)
    }
  }

  const hiddenCount = crds.filter((crd) => draft.has(resourceKey(crd))).length
  const shownCount = crds.length - hiddenCount
  const changed =
    crds.some((crd) => draft.has(resourceKey(crd)) !== hidden.includes(resourceKey(crd)))
  const allKeys = groups.flatMap(({ items }) => items.map(resourceKey))

  return (
    <Sheet
      eyebrow={clusterName}
      title="Custom resources in the sidebar"
      onClose={onClose}
      width="lg"
      footer={
        <>
          <span className="mr-auto text-[12.5px] text-muted">
            <span className="font-data text-fg tabular-nums">{shownCount}</span> of{' '}
            <span className="font-data text-fg tabular-nums">{crds.length}</span> shown
          </span>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy || !changed}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      <p className="text-[12.5px] leading-relaxed text-muted">
        A resource you turn off leaves the Explore sidebar for everybody on this cluster, including
        you. It is still there — the manifest editor, a link somebody saved and{' '}
        <span className="font-mono">kubectl</span> all reach it exactly as before, and what may be read
        stays the cluster’s own RBAC to decide.
      </p>

      <div className="flex flex-wrap items-center gap-2">
        <SearchInput
          value={filter}
          onChange={setFilter}
          label="Filter custom resources"
          placeholder="Filter by kind or API group…"
          className="min-w-48 flex-1"
        />
        <Segmented<SheetFilter>
          ariaLabel="Which custom resources to list"
          value={view}
          onChange={(next) => {
            setFrozen(new Set(draft))
            setView(next)
          }}
          options={[
            { value: 'all', label: 'All', count: crds.length },
            { value: 'shown', label: 'Shown', count: shownCount },
            { value: 'hidden', label: 'Hidden', count: hiddenCount },
          ]}
        />
      </div>

      {groups.length > 0 ? (
        <div className="flex items-center gap-1 text-[12.5px]">
          <Button variant="ghost" size="sm" onClick={() => setKeys(allKeys, true)}>
            <Eye className="size-3.5" />
            Show {searching ? 'these' : 'all'}
          </Button>
          <Button variant="ghost" size="sm" onClick={() => setKeys(allKeys, false)}>
            <EyeOff className="size-3.5" />
            Hide {searching ? 'these' : 'all'}
          </Button>
          <span className="ml-auto flex items-center gap-1">
            <Button
              variant="ghost"
              size="sm"
              onClick={() => setOpen(new Set(groups.map(({ group }) => group)))}
            >
              Expand all
            </Button>
            <Button variant="ghost" size="sm" onClick={() => setOpen(new Set())}>
              Collapse all
            </Button>
          </span>
        </div>
      ) : null}

      {groups.length === 0 ? (
        <p className="text-[13px] text-muted">Nothing matches that filter.</p>
      ) : (
        <div className="flex flex-col gap-2">
          {groups.map(({ group, items }) => {
            const keys = items.map(resourceKey)
            const shownHere = keys.filter((key) => !draft.has(key)).length
            const expanded = searching || open.has(group)
            const panelId = `crd-group-${group.replace(/[^a-z0-9-]/gi, '-')}`
            return (
              <section key={group} className="overflow-hidden rounded-card border border-line">
                <header className="flex items-center gap-3 bg-raised/50 px-3 py-2">
                  <button
                    type="button"
                    aria-expanded={expanded}
                    aria-controls={panelId}
                    onClick={() => toggleOpen(group)}
                    disabled={searching}
                    className="flex min-w-0 flex-1 cursor-pointer items-center gap-2 text-left disabled:cursor-default"
                  >
                    <ChevronRight
                      aria-hidden="true"
                      className={`size-4 shrink-0 text-faint ${expanded ? 'rotate-90' : ''}`}
                    />
                    <span className="truncate font-data text-[13px] font-medium text-fg">{group}</span>
                    <span className="shrink-0 font-data text-[12px] text-faint tabular-nums">
                      {shownHere}/{items.length}
                    </span>
                  </button>
                  <Switch
                    checked={shownHere === items.length}
                    onChange={(show) => setKeys(keys, show)}
                    label={`Show every ${group} resource in the sidebar`}
                  />
                </header>
                {expanded ? (
                  <ul id={panelId} className="-mb-px grid border-t border-line-soft sm:grid-cols-2">
                    {items.map((crd) => {
                      const key = resourceKey(crd)
                      const shown = !draft.has(key)
                      return (
                        <li
                          key={key}
                          className="flex items-center justify-between gap-3 border-b border-line-soft px-3 py-2 sm:odd:border-r"
                        >
                          <div className="min-w-0">
                            <p className={`truncate text-[13px] ${shown ? 'text-fg' : 'text-faint'}`}>
                              {crd.kind}
                            </p>
                            <p className="truncate font-data text-[11.5px] text-faint">{crd.plural}</p>
                          </div>
                          <Switch
                            checked={shown}
                            onChange={(show) => setKeys([key], show)}
                            label={`Show ${crd.kind} (${key}) in the sidebar`}
                          />
                        </li>
                      )
                    })}
                  </ul>
                ) : null}
              </section>
            )
          })}
        </div>
      )}

      {absent.length > 0 ? (
        <Notice tone="info">
          {absent.length === 1 ? 'One resource' : `${absent.length} resources`} this cluster no
          longer serves are still on the hidden list, and are kept there in case the operator comes
          back.
        </Notice>
      ) : null}
    </Sheet>
  )
}

/**
 * groupByAPIGroup lays the list out the way an operator installs one: by API
 * group, alphabetically, kinds within it alphabetically. The sidebar buckets by
 * the group *family* — several groups under one domain are one operator — but
 * this is the surface where somebody turns off exactly `cert-manager.io`'s
 * internals and not `acme.cert-manager.io`'s, so the real group is the row.
 */
function groupByAPIGroup(
  crds: CustomResourceDefinition[],
  filter: string,
): { group: string; items: CustomResourceDefinition[] }[] {
  const needle = filter.trim().toLowerCase()
  const buckets = new Map<string, CustomResourceDefinition[]>()

  for (const crd of crds) {
    if (
      needle &&
      !crd.kind.toLowerCase().includes(needle) &&
      !crd.group.toLowerCase().includes(needle) &&
      !crd.plural.toLowerCase().includes(needle)
    ) {
      continue
    }
    const bucket = buckets.get(crd.group)
    if (bucket) bucket.push(crd)
    else buckets.set(crd.group, [crd])
  }

  return [...buckets.entries()]
    .map(([group, items]) => ({
      group,
      items: items.sort((a, b) => a.kind.localeCompare(b.kind)),
    }))
    .sort((a, b) => a.group.localeCompare(b.group))
}

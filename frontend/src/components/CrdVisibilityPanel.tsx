import { useCallback, useEffect, useMemo, useState } from 'react'
import { Eye, EyeOff, SlidersHorizontal } from 'lucide-react'
import { errorMessage, fetchCRDVisibility, fetchCRDs, saveCRDVisibility } from '../api/client'
import type { Cluster, CustomResourceDefinition } from '../api/types'
import { useInventory } from '../state/inventory-context'
import { Button, Chip, Notice, Panel, SearchInput, Sheet } from './primitives'

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
}: {
  cluster: Cluster
  className?: string
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

  // Nothing to curate and no way to curate it is not worth a panel.
  if (!editable || (crds !== null && crds.length === 0 && hidden.length === 0)) return null

  const total = crds?.length ?? 0
  const hiddenHere = (crds ?? []).filter((crd) => hidden.includes(resourceKey(crd))).length

  return (
    <>
      <Panel
        eyebrow="Explore"
        title="Custom resources in the sidebar"
        description="Which of this cluster’s CRDs everybody browsing it is offered. This is what the navigation shows — it is not a permission, and the cluster’s own RBAC still decides what can be read."
        className={className}
        actions={
          <Button variant="secondary" size="sm" onClick={() => setEditing(true)}>
            <SlidersHorizontal className="size-3.5" />
            Choose
          </Button>
        }
        bodyClassName="p-4"
      >
        {error ? <Notice tone="error">{error}</Notice> : null}
        {crds === null ? (
          <p className="text-[13px] text-muted">Loading…</p>
        ) : (
          <p className="text-[13px] text-muted">
            {hiddenHere === 0 ? (
              <>
                All <span className="font-mono text-fg">{total}</span> custom resources this cluster
                serves are in the sidebar.
              </>
            ) : (
              <>
                <span className="font-mono text-fg">{hiddenHere}</span> of{' '}
                <span className="font-mono text-fg">{total}</span> custom resources are kept out of the
                sidebar.
              </>
            )}
          </p>
        )}
      </Panel>

      {editing ? (
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
      ) : null}
    </>
  )
}

/** resourceKey is how a resource is named unambiguously, here and on the wire. */
function resourceKey(crd: CustomResourceDefinition): string {
  return `${crd.plural}.${crd.group}`
}

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
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Entries naming a kind this cluster no longer serves are carried through
  // untouched: they are somebody's decision about an operator that may come
  // back, and this editor cannot show a row for a CRD that is not installed.
  const served = useMemo(() => new Set(crds.map(resourceKey)), [crds])
  const absent = useMemo(() => hidden.filter((key) => !served.has(key)), [hidden, served])

  const groups = useMemo(() => groupByAPIGroup(crds, filter), [crds, filter])

  function toggle(key: string) {
    setDraft((current) => {
      const next = new Set(current)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  function setGroup(keys: string[], hide: boolean) {
    setDraft((current) => {
      const next = new Set(current)
      for (const key of keys) {
        if (hide) next.add(key)
        else next.delete(key)
      }
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

  const shownCount = crds.length - crds.filter((crd) => draft.has(resourceKey(crd))).length

  return (
    <Sheet
      eyebrow={clusterName}
      title="Custom resources in the sidebar"
      onClose={onClose}
      width="lg"
      footer={
        <>
          <span className="mr-auto text-[12.5px] text-muted">
            <span className="font-mono text-fg">{shownCount}</span> of{' '}
            <span className="font-mono text-fg">{crds.length}</span> shown
          </span>
          <Button variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button variant="primary" onClick={() => void submit()} disabled={busy}>
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

      <SearchInput
        value={filter}
        onChange={setFilter}
        label="Filter custom resources"
        placeholder="Filter by kind or API group"
        className="w-full"
      />

      {groups.length === 0 ? (
        <p className="text-[13px] text-muted">Nothing matches that filter.</p>
      ) : (
        <div className="flex flex-col gap-4">
          {groups.map(({ group, items }) => {
            const keys = items.map(resourceKey)
            const allHidden = keys.every((key) => draft.has(key))
            return (
              <section key={group} className="card overflow-hidden">
                <header className="flex flex-wrap items-center justify-between gap-2 border-b border-line-soft px-3 py-2">
                  <p className="font-mono truncate text-[12.5px] text-fg">{group}</p>
                  <Button
                    variant="ghost"
                    size="sm"
                    onClick={() => setGroup(keys, !allHidden)}
                  >
                    {allHidden ? 'Show all' : 'Hide all'}
                  </Button>
                </header>
                <ul className="flex flex-col">
                  {items.map((crd) => {
                    const key = resourceKey(crd)
                    const shown = !draft.has(key)
                    return (
                      <li
                        key={key}
                        className="flex items-center justify-between gap-3 border-t border-line-soft px-3 py-2 first:border-t-0"
                      >
                        <div className="min-w-0">
                          <p className="truncate text-[13px] text-fg">{crd.kind}</p>
                          <p className="font-mono truncate text-[11.5px] text-faint">{crd.plural}</p>
                        </div>
                        <Chip
                          active={shown}
                          onClick={() => toggle(key)}
                          title={shown ? 'In the sidebar' : 'Kept out of the sidebar'}
                        >
                          {shown ? <Eye className="size-3.5" /> : <EyeOff className="size-3.5" />}
                          {shown ? 'Shown' : 'Hidden'}
                        </Chip>
                      </li>
                    )
                  })}
                </ul>
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

import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { BarChart3, ExternalLink, GitBranch, Pencil, Plus, Trash2 } from 'lucide-react'
import {
  deleteClusterConsole,
  errorMessage,
  fetchClusterConsoles,
  saveClusterConsole,
} from '../api/client'
import type { Cluster, ClusterConsole, ClusterConsolesResponse, ConsoleKind } from '../api/types'
import { CONSOLES, CONSOLE_KINDS, datasourceUILabel } from '../lib/consoles'
import { KIND_LABEL } from '../lib/datasources'
import { Button, Field, IconButton, Notice, Panel, Pill, Sheet, TextInput } from './primitives'

/**
 * The other consoles this cluster is operated from.
 *
 * A cluster already says where its metrics and logs live, and KubeMG draws
 * charts from them — but the moment a question outgrows the fixed catalogue, the
 * answer is in a Grafana somebody has to go and find, in another tab, at a URL
 * nobody wrote down. The same holds for the GitOps tool that owns half the
 * workloads in Explore.
 *
 * These are **links, never embeds**. An iframe would inherit this console's
 * origin and its session; proxying another application through the agent tunnel
 * would mean carrying its routing, assets and websockets inside a transport
 * built for the Kubernetes API. KubeMG stores an address and no session, and the
 * operator signs in to the other tool as themselves — which is exactly why
 * everybody the cluster is granted to may see the address, and only an admin may
 * set it.
 */
export function ConsolesPanel({
  cluster,
  className,
}: {
  cluster: Cluster
  className?: string
}) {
  const [state, setState] = useState<ClusterConsolesResponse | null>(null)
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<ConsoleKind | null>(null)
  const [busyKind, setBusyKind] = useState<ConsoleKind | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      setState(await fetchClusterConsoles(cluster.id))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load this cluster’s consoles.'))
    } finally {
      setLoading(false)
    }
  }, [cluster.id])

  useEffect(() => {
    void load()
  }, [load])

  async function remove(kind: ConsoleKind) {
    setBusyKind(kind)
    try {
      await deleteClusterConsole(cluster.id, kind)
      await load()
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not remove that console.'))
    } finally {
      setBusyKind(null)
    }
  }

  const editable = state?.editable ?? false
  const registered = new Map((state?.consoles ?? []).map((console) => [console.kind, console]))
  const datasourceUIs = state?.datasource_uis ?? []

  // Nothing registered and nothing to register it with is not worth a panel:
  // a reader would be looking at two empty rows they cannot act on.
  if (!loading && !editable && registered.size === 0 && datasourceUIs.length === 0) return null

  return (
    <Panel
      eyebrow="Elsewhere"
      title="Other consoles"
      description="Where this cluster is operated from outside KubeMG. These are links — KubeMG stores no session for them and you sign in as yourself."
      className={className}
    >
      <div className="flex flex-col">
        {error ? (
          <div className="p-4 pb-0">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        {loading ? (
          <p className="px-4 py-6 text-[13px] text-muted">Loading…</p>
        ) : (
          CONSOLE_KINDS.map((kind) => (
            <ConsoleRow
              key={kind}
              kind={kind}
              link={registered.get(kind)}
              editable={editable}
              busy={busyKind === kind}
              onEdit={() => setEditing(kind)}
              onRemove={() => remove(kind)}
            />
          ))
        )}

        {datasourceUIs.length > 0 ? (
          <div className="border-t border-line-soft px-4 py-3">
            <p className="label mb-2">The datasource’s own UI</p>
            <div className="flex flex-wrap items-center gap-2">
              {datasourceUIs.map((ui) => (
                <a
                  key={ui.kind}
                  href={ui.url}
                  target="_blank"
                  rel="noreferrer noopener"
                  className="inline-flex items-center gap-1.5 rounded-chip border border-line bg-raised px-2 py-1 font-mono text-[12px] text-muted transition-colors hover:text-fg"
                  title={ui.url}
                >
                  <ExternalLink aria-hidden="true" className="size-3.5" />
                  {KIND_LABEL[ui.kind]} · {datasourceUILabel(ui.provider)}
                </a>
              ))}
            </div>
            {/* Derived, never stored: it is the address the cluster already
                declared with the provider's UI path on the end. */}
            <p className="mt-2 text-[12px] leading-relaxed text-muted">
              Built from the datasource address registered above. A datasource reached through the
              agent tunnel has no such address — it is proxied by the cluster’s API server, not
              opened by a browser.
            </p>
          </div>
        ) : null}
      </div>

      {editing ? (
        <ConsoleSheet
          cluster={cluster}
          kind={editing}
          link={registered.get(editing) ?? null}
          onClose={() => setEditing(null)}
          onSaved={async () => {
            setEditing(null)
            await load()
          }}
        />
      ) : null}
    </Panel>
  )
}

const CONSOLE_ICON = { grafana: BarChart3, argocd: GitBranch } as const

/** ConsoleRow is one console: where it is, or the offer to say where it is. */
function ConsoleRow({
  kind,
  link,
  editable,
  busy,
  onEdit,
  onRemove,
}: {
  kind: ConsoleKind
  link: ClusterConsole | undefined
  editable: boolean
  busy: boolean
  onEdit: () => void
  onRemove: () => void
}) {
  const info = CONSOLES[kind]
  const Icon = CONSOLE_ICON[kind]

  // A reader with nothing to open and no way to register one has nothing here.
  if (!link && !editable) return null

  return (
    <div className="flex flex-wrap items-center gap-3 border-t border-line-soft px-4 py-3 first:border-t-0">
      <Icon aria-hidden="true" className="size-4 shrink-0 text-muted" />

      <div className="min-w-0 flex-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-[13.5px] font-medium text-fg">{info.label}</span>
          {link?.ref ? (
            <Pill tone="idle" dot={false}>
              {link.ref}
            </Pill>
          ) : null}
          {!link ? (
            <Pill tone="idle" dot={false}>
              Not registered
            </Pill>
          ) : null}
        </div>

        {link ? (
          <a
            href={link.url}
            target="_blank"
            rel="noreferrer noopener"
            className="mt-1 inline-flex max-w-full items-center gap-1.5 font-mono text-[12px] text-accent transition-colors hover:text-accent-hover"
          >
            <span className="truncate">{link.url}</span>
            <ExternalLink aria-hidden="true" className="size-3 shrink-0" />
          </a>
        ) : (
          <p className="mt-1 max-w-2xl text-[12px] leading-relaxed text-muted">{info.purpose}</p>
        )}
      </div>

      {editable ? (
        <div className="flex shrink-0 items-center gap-2">
          {link ? (
            <>
              <IconButton label={`Edit the ${info.label} address`} onClick={onEdit} disabled={busy}>
                <Pencil aria-hidden="true" className="size-3.5" />
              </IconButton>
              <IconButton
                label={`Remove the ${info.label} address`}
                onClick={onRemove}
                disabled={busy}
                tone="danger"
              >
                <Trash2 aria-hidden="true" className="size-3.5" />
              </IconButton>
            </>
          ) : (
            <Button size="sm" onClick={onEdit}>
              <Plus aria-hidden="true" className="size-3.5" />
              Add
            </Button>
          )}
        </div>
      ) : null}
    </div>
  )
}

/** ConsoleSheet is the whole editor, which is one address and at most one name. */
function ConsoleSheet({
  cluster,
  kind,
  link,
  onClose,
  onSaved,
}: {
  cluster: Cluster
  kind: ConsoleKind
  link: ClusterConsole | null
  onClose: () => void
  onSaved: () => Promise<void>
}) {
  const info = CONSOLES[kind]
  const [url, setUrl] = useState(link?.url ?? '')
  const [ref, setRef] = useState(link?.ref ?? '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setSaving(true)
    try {
      await saveClusterConsole(cluster.id, kind, {
        url: url.trim(),
        ref: info.refLabel ? ref.trim() : undefined,
      })
      setError(null)
      await onSaved()
    } catch (err) {
      // The server's own refusal is the useful one — it is what explains that a
      // password in the address is not stored here.
      setError(errorMessage(err, 'Could not save that address.'))
    } finally {
      setSaving(false)
    }
  }

  return (
    <Sheet
      title={`${info.label} for ${cluster.name}`}
      eyebrow="Other consoles"
      onClose={onClose}
      onSubmit={submit}
      footer={
        <>
          <Button type="button" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={saving || url.trim() === ''}>
            {saving ? 'Saving…' : 'Save'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 p-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <p className="text-[13px] leading-relaxed text-muted">{info.purpose}</p>

        <Field
          label="Address"
          htmlFor="console-url"
          hint="The console’s own base address, as you would open it."
        >
          <TextInput
            id="console-url"
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder={info.placeholder}
            autoFocus
            spellCheck={false}
          />
        </Field>

        {info.refLabel ? (
          <Field label={info.refLabel} htmlFor="console-ref" hint={info.refHint}>
            <TextInput
              id="console-ref"
              value={ref}
              onChange={(event) => setRef(event.target.value)}
              spellCheck={false}
            />
          </Field>
        ) : null}

        <Notice tone="info">
          KubeMG stores this address and nothing else — no session, no credential, and no proxy to
          it. Anyone this cluster is granted to can see the link; they still sign in to{' '}
          {info.label} as themselves. Leave any username and password out of the address.
        </Notice>
      </div>
    </Sheet>
  )
}

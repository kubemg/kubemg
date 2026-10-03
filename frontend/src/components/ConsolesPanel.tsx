import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { BarChart3, ExternalLink, GitBranch, Pencil, Plus, ShieldAlert, Trash2 } from 'lucide-react'
import {
  deleteClusterConsole,
  errorMessage,
  fetchClusterConsoles,
  saveClusterConsole,
} from '../api/client'
import type { Cluster, ClusterConsole, ClusterConsolesResponse, ConsoleKind } from '../api/types'
import { CONSOLES, CONSOLE_KINDS, datasourceUILabel } from '../lib/consoles'
import { KIND_LABEL } from '../lib/datasources'
import { INTEGRATION_GRID, IntegrationGroup, IntegrationTile } from './IntegrationTile'
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
  bare = false,
}: {
  cluster: Cluster
  className?: string
  /** Drawn as a group inside somebody else's panel rather than as its own. */
  bare?: boolean
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

  // A reader with nothing to open and no way to register one has no tile.
  const shown = CONSOLE_KINDS.filter((kind) => registered.has(kind) || editable)

  const body = (
    <>
      {error ? <Notice tone="error">{error}</Notice> : null}

      {loading ? (
        <p className="py-2 text-[13px] text-muted">Loading…</p>
      ) : shown.length > 0 ? (
        <div className={INTEGRATION_GRID}>
          {shown.map((kind) => (
            <ConsoleTile
              key={kind}
              kind={kind}
              link={registered.get(kind)}
              editable={editable}
              busy={busyKind === kind}
              onEdit={() => setEditing(kind)}
              onRemove={() => remove(kind)}
            />
          ))}
        </div>
      ) : null}

      {/* Beside the datasource tiles these links are already on each tile, so
          the group form leaves them out rather than drawing them twice. */}
      {!bare && datasourceUIs.length > 0 ? (
        <div className="flex flex-col gap-2">
          <p className="label">The datasource’s own UI</p>
          <div className="flex flex-wrap items-center gap-2">
            {datasourceUIs.map((ui) => (
              <a
                key={ui.kind}
                href={ui.url}
                target="_blank"
                rel="noreferrer noopener"
                className="inline-flex items-center gap-1.5 rounded-chip border border-line bg-raised px-2 py-1 font-data text-[12px] text-muted transition-colors hover:text-fg"
                title={ui.url}
              >
                <ExternalLink aria-hidden="true" className="size-3.5" />
                {KIND_LABEL[ui.kind]} · {datasourceUILabel(ui.provider)}
              </a>
            ))}
          </div>
          {/* Derived, never stored: it is the address the cluster already
              declared with the provider's UI path on the end. */}
          <p className="text-[12px] leading-relaxed text-muted">
            Built from the cluster’s registered datasource address. A datasource reached through
            the agent tunnel has no such address — it is proxied by the cluster’s API server, not
            opened by a browser.
          </p>
        </div>
      ) : null}
    </>
  )

  const sheet = editing ? (
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
  ) : null

  if (bare) {
    return (
      <IntegrationGroup
        title="Other consoles"
        description="Links only — kubemg stores no session for them, and you sign in as yourself."
      >
        {body}
        {sheet}
      </IntegrationGroup>
    )
  }

  return (
    <Panel
      eyebrow="Elsewhere"
      title="Other consoles"
      description={DESCRIPTION}
      className={className}
      bodyClassName="flex flex-col gap-3 p-4"
    >
      {body}
      {sheet}
    </Panel>
  )
}

const DESCRIPTION =
  'Where this cluster is operated from outside kubemg. These are links — kubemg stores no session for them and you sign in as yourself.'

const CONSOLE_ICON = { grafana: BarChart3, argocd: GitBranch, registry: ShieldAlert } as const

/** ConsoleTile is one console: where it is, or the offer to say where it is. */
function ConsoleTile({
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

  if (!link) {
    return (
      <IntegrationTile
        icon={CONSOLE_ICON[kind]}
        title={info.label}
        wired={false}
        state={
          <Pill tone="idle" dot={false}>
            Not registered
          </Pill>
        }
        actions={
          editable ? (
            <Button size="sm" onClick={onEdit}>
              <Plus aria-hidden="true" className="size-3.5" />
              Add
            </Button>
          ) : null
        }
      >
        <p className="text-muted">{info.purpose}</p>
      </IntegrationTile>
    )
  }

  return (
    <IntegrationTile
      icon={CONSOLE_ICON[kind]}
      title={info.label}
      wired
      meta={link.ref ? <span className="font-data">{link.ref}</span> : undefined}
      state={
        // Only an address is stored and nothing is probed, so the state is
        // neutral: "linked" says where it is, never that it is up.
        <Pill tone="idle">Linked</Pill>
      }
      link={
        <a
          href={link.url}
          target="_blank"
          rel="noreferrer noopener"
          className="inline-flex max-w-full items-center gap-1.5 text-[12.5px] font-medium text-accent transition-colors hover:text-accent-hover"
          title={link.url}
        >
          <span className="truncate">Open {info.label}</span>
          <ExternalLink aria-hidden="true" className="size-3.5 shrink-0" />
        </a>
      }
      actions={
        editable ? (
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
        ) : null
      }
    >
      <p className="truncate font-data text-fg" title={link.url}>
        {link.url}
      </p>
    </IntegrationTile>
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
          kubemg stores this address and nothing else — no session, no credential, and no proxy to
          it. Anyone this cluster is granted to can see the link; they still sign in to{' '}
          {info.label} as themselves. Leave any username and password out of the address.
        </Notice>
      </div>
    </Sheet>
  )
}

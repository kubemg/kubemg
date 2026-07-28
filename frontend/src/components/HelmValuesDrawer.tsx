import { useCallback, useEffect, useState } from 'react'
import { Check, Copy, Pencil, RefreshCw, Undo2 } from 'lucide-react'
import { errorMessage, fetchHelmValues, updateHelmValues } from '../api/client'
import type { Cluster, HelmRelease, HelmValues } from '../api/types'
import { Button, Notice, Pill, Sheet, Spinner } from './primitives'
import { YamlView } from './YamlView'

/**
 * A Helm release's values, read and written where Helm keeps them.
 *
 * This is deliberately not the YamlDrawer. That drawer edits an object: it reads
 * one manifest and writes the same one back, and the cluster reconciles what
 * changed. A release is not an object — it is a Secret holding a compressed blob
 * — and what is worth editing inside it is only the values.
 *
 * The limit of the write is the important part and is stated on the surface
 * rather than buried: saving appends a Helm revision recording the values Helm
 * will start from, and renders nothing. The cluster keeps running exactly what
 * the previous revision rendered until someone runs `helm upgrade`. The backend
 * sends that warning with every response so a client that ignores this component
 * is still told; this shows it before the first keystroke, not after the save.
 */
export function HelmValuesDrawer({
  cluster,
  release,
  editing: startEditing = false,
  onClose,
  onApplied,
}: {
  cluster: Cluster
  release: HelmRelease
  editing?: boolean
  onClose: () => void
  onApplied?: () => Promise<void> | void
}) {
  const [values, setValues] = useState<HelmValues | null>(null)
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(startEditing)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [applied, setApplied] = useState(false)
  const [copied, setCopied] = useState(false)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const result = await fetchHelmValues(cluster.id, release.name, release.namespace)
      setValues(result)
      setDraft(result.yaml)
      setError(null)
    } catch (err) {
      // Reading a release means reading a Secret, and the built-in view role
      // excludes those — the cluster's own refusal is the useful message.
      setError(errorMessage(err, 'Could not read this release from the cluster.'))
      setValues(null)
    } finally {
      setLoading(false)
    }
  }, [cluster.id, release.name, release.namespace])

  useEffect(() => {
    void load()
  }, [load])

  const dirty = values !== null && draft !== values.yaml

  const close = useCallback(() => {
    if (dirty && !window.confirm('Discard your unsaved changes to these values?')) return
    onClose()
  }, [dirty, onClose])

  async function save() {
    if (!values) return

    setSaving(true)
    setApplied(false)
    try {
      const result = await updateHelmValues(cluster.id, release.name, release.namespace, draft)
      setValues(result)
      setDraft(result.yaml)
      setEditing(false)
      setApplied(true)
      setError(null)
      await onApplied?.()
    } catch (err) {
      setError(errorMessage(err, 'The cluster did not accept these values.'))
    } finally {
      setSaving(false)
    }
  }

  async function copy() {
    try {
      await navigator.clipboard.writeText(draft)
      setCopied(true)
      window.setTimeout(() => setCopied(false), 1600)
    } catch {
      // Clipboard access is denied outside a secure context; the values are
      // selectable on screen either way.
    }
  }

  const current = values?.release ?? release

  return (
    <Sheet
      width="lg"
      eyebrow={`${cluster.name} · ${release.namespace} · Helm release`}
      title={<span className="font-mono">{release.name}</span>}
      onClose={close}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={close}>
            Close
          </Button>
          {editing ? (
            <>
              <Button
                type="button"
                onClick={() => setDraft(values?.yaml ?? '')}
                disabled={!dirty || saving}
              >
                <Undo2 aria-hidden="true" className="size-4" />
                Revert
              </Button>
              <Button
                type="button"
                variant="primary"
                onClick={() => void save()}
                disabled={!dirty || saving}
              >
                {saving ? (
                  <Spinner className="size-4" />
                ) : (
                  <Check aria-hidden="true" className="size-4" />
                )}
                Save as revision {current.revision + 1}
              </Button>
            </>
          ) : (
            <Button
              type="button"
              variant="primary"
              onClick={() => setEditing(true)}
              disabled={!values || loading}
            >
              <Pencil aria-hidden="true" className="size-4" />
              Edit values
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-wrap items-center gap-2">
        <Pill tone="idle" dot={false}>
          {current.chart_name || 'chart'}
          {current.chart_version ? ` ${current.chart_version}` : ''}
        </Pill>
        {current.app_version ? (
          <Pill tone="idle" dot={false}>
            app {current.app_version}
          </Pill>
        ) : null}
        <Pill tone="idle" dot={false}>
          revision {current.revision}
        </Pill>
        {editing ? (
          <Pill tone={dirty ? 'warn' : 'accent'} dot={false}>
            {dirty ? 'edited' : 'editing'}
          </Pill>
        ) : null}

        <div className="ml-auto flex items-center gap-2">
          <Button type="button" size="sm" onClick={() => void copy()} disabled={!values}>
            {copied ? (
              <Check aria-hidden="true" className="size-3.5 text-ok" />
            ) : (
              <Copy aria-hidden="true" className="size-3.5" />
            )}
            {copied ? 'Copied' : 'Copy'}
          </Button>
          <Button
            type="button"
            size="sm"
            onClick={() => void load()}
            disabled={loading || saving}
            title={dirty ? 'Re-reading the release replaces your edits' : undefined}
          >
            <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
            Reload
          </Button>
        </div>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {applied && !error ? (
        <Notice tone="ok">
          Saved as revision {current.revision}. {values?.warning}
        </Notice>
      ) : null}
      {/* Shown while editing rather than only after saving: what this does and
          does not do is something to know before typing, not afterwards. */}
      {editing && !applied && values ? <Notice tone="info">{values.warning}</Notice> : null}

      {loading && !values ? (
        <p className="text-[13px] text-muted">Reading the release…</p>
      ) : (
        <YamlView
          value={draft}
          onChange={editing ? setDraft : undefined}
          className="min-h-[320px] flex-1"
        />
      )}

      <p className="text-[12px] text-muted">
        {editing
          ? 'Saving appends a Helm revision through the agent tunnel under your own identity, exactly as an upgrade would. The cluster’s RBAC decides whether you may, and the change is in the audit trail.'
          : 'These are the values supplied at install or upgrade — what helm get values shows — not the chart’s defaults merged into them.'}
      </p>
    </Sheet>
  )
}

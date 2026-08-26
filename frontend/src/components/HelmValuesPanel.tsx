import { useCallback, useEffect, useState } from 'react'
import { Check, CircleArrowUp, Copy, Pencil, RefreshCw, Undo2 } from 'lucide-react'
import { errorMessage, fetchHelmValues, updateHelmValues, upgradeHelmRelease } from '../api/client'
import type { Cluster, HelmRelease, HelmValues, HelmWriteResult } from '../api/types'
import { HelmChartPicker } from './HelmChartPicker'
import type { HelmChartSelection } from './HelmChartPicker'
import { HelmObjectReport } from './HelmObjectReport'
import { Button, Notice, Pill, Spinner } from './primitives'
import { YamlView } from './YamlView'

/**
 * A Helm release's values, read and written where Helm keeps them.
 *
 * This is a tab rather than a surface of its own, and it is the *only* tab a
 * release has. A release is not an API object — it is a Secret holding a
 * compressed blob — so there is no manifest for the YAML tab to address and no
 * describe to read: the values are the whole of what KubeMG can show. It sits
 * beside the object tabs anyway, because reaching a release and reaching a
 * Deployment should not be two different motions.
 *
 * Its own writes stay in the panel rather than moving to the drawer's footer.
 * Edit / Revert / Save are a mode this panel is in, and a footer shared with
 * every other tab would have to grow and shrink as tabs changed under it.
 *
 * A release Helm wrote carries its own chart, so saving values here renders
 * and applies them the same way `helm upgrade` would — `HelmObjectReport` below
 * the editor is the same per-object report an install answers with. Changing
 * the chart itself, rather than only its values, is the second mode this panel
 * offers: **Upgrade chart** picks a version from a repository's catalogue the
 * way the install sheet does, and applies it the same way.
 */
export function HelmValuesPanel({
  cluster,
  release,
  editing: startEditing = false,
  onDirtyChange,
  onApplied,
}: {
  cluster: Cluster
  release: HelmRelease
  editing?: boolean
  /** Lets the drawer guard a close on a half-typed edit, as the YAML tab does. */
  onDirtyChange?: (dirty: boolean) => void
  onApplied?: () => Promise<void> | void
}) {
  const [values, setValues] = useState<HelmValues | null>(null)
  const [draft, setDraft] = useState('')
  const [editing, setEditing] = useState(startEditing)
  const [upgrading, setUpgrading] = useState(false)
  const [selection, setSelection] = useState<HelmChartSelection>({
    repository: '',
    chart: '',
    version: '',
  })
  const [reuseValues, setReuseValues] = useState(true)

  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [written, setWritten] = useState<HelmWriteResult | null>(null)
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

  const dirty = (editing && values !== null && draft !== values.yaml) || upgrading
  const current = values?.release ?? release

  useEffect(() => {
    onDirtyChange?.(dirty)
  }, [dirty, onDirtyChange])

  // A tab that unmounts with a half-typed edit has nothing left to discard, so
  // it must not leave the drawer guarding a close against an edit that is gone.
  useEffect(() => () => onDirtyChange?.(false), [onDirtyChange])

  async function save() {
    if (!values) return

    setSaving(true)
    setWritten(null)
    try {
      const result = await updateHelmValues(cluster.id, release.name, release.namespace, draft)
      setWritten(result)
      setEditing(false)
      setError(null)
      if (result.applied !== false) {
        await load()
        await onApplied?.()
      }
    } catch (err) {
      setError(errorMessage(err, 'The cluster did not accept these values.'))
    } finally {
      setSaving(false)
    }
  }

  async function upgrade() {
    if (!selection.repository || !selection.chart || !selection.version) return

    setSaving(true)
    setWritten(null)
    try {
      const result = await upgradeHelmRelease(cluster.id, release.name, release.namespace, {
        repository: selection.repository,
        chart: selection.chart,
        version: selection.version,
        yaml: reuseValues ? undefined : draft,
        reuseValues,
      })
      setWritten(result)
      setUpgrading(false)
      setError(null)
      if (result.applied !== false) {
        await load()
        await onApplied?.()
      }
    } catch (err) {
      setError(errorMessage(err, 'The cluster did not accept this upgrade.'))
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

  return (
    <>
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
          <Pill tone={draft !== (values?.yaml ?? '') ? 'warn' : 'accent'} dot={false}>
            {draft !== (values?.yaml ?? '') ? 'edited' : 'editing'}
          </Pill>
        ) : null}
        {upgrading ? (
          <Pill tone="warn" dot={false}>
            changing chart
          </Pill>
        ) : null}

        <div className="ml-auto flex items-center gap-2">
          {!editing && !upgrading ? (
            <>
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
              >
                <RefreshCw aria-hidden="true" className={`size-3.5 ${loading ? 'animate-spin' : ''}`} />
                Reload
              </Button>
              <Button
                type="button"
                size="sm"
                onClick={() => {
                  setUpgrading(true)
                  setWritten(null)
                }}
                disabled={!values || loading}
              >
                <CircleArrowUp aria-hidden="true" className="size-3.5" />
                Upgrade chart
              </Button>
              <Button
                type="button"
                size="sm"
                variant="primary"
                onClick={() => {
                  setEditing(true)
                  setWritten(null)
                }}
                disabled={!values || loading}
              >
                <Pencil aria-hidden="true" className="size-3.5" />
                Edit values
              </Button>
            </>
          ) : null}

          {editing ? (
            <>
              <Button
                type="button"
                size="sm"
                onClick={() => setDraft(values?.yaml ?? '')}
                disabled={draft === (values?.yaml ?? '') || saving}
              >
                <Undo2 aria-hidden="true" className="size-3.5" />
                Revert
              </Button>
              <Button type="button" size="sm" onClick={() => setEditing(false)} disabled={saving}>
                Cancel
              </Button>
              <Button
                type="button"
                size="sm"
                variant="primary"
                onClick={() => void save()}
                disabled={draft === (values?.yaml ?? '') || saving}
              >
                {saving ? (
                  <Spinner className="size-3.5" />
                ) : (
                  <Check aria-hidden="true" className="size-3.5" />
                )}
                Save as revision {current.revision + 1}
              </Button>
            </>
          ) : null}

          {upgrading ? (
            <>
              <Button type="button" size="sm" onClick={() => setUpgrading(false)} disabled={saving}>
                Cancel
              </Button>
              <Button
                type="button"
                size="sm"
                variant="primary"
                onClick={() => void upgrade()}
                disabled={!selection.repository || !selection.chart || !selection.version || saving}
              >
                {saving ? (
                  <Spinner className="size-3.5" />
                ) : (
                  <Check aria-hidden="true" className="size-3.5" />
                )}
                Upgrade to revision {current.revision + 1}
              </Button>
            </>
          ) : null}
        </div>
      </div>

      {error ? <Notice tone="error">{error}</Notice> : null}
      {/* The read carries a warning only for a release that cannot be
          re-rendered, so this states the limit before the first keystroke
          rather than in the receipt. Every other release says nothing here,
          because a write against it genuinely applies. */}
      {values?.warning ? <Notice tone="warn">{values.warning}</Notice> : null}
      {written ? <HelmObjectReport result={written} /> : null}

      {upgrading ? (
        <div className="flex flex-col gap-3 rounded-card border border-accent/40 bg-accent-soft/40 p-3">
          <HelmChartPicker selection={selection} onChange={setSelection} />
          <label className="flex items-center gap-2.5">
            <input
              type="checkbox"
              className="size-4 accent-[var(--color-accent)]"
              checked={reuseValues}
              onChange={(event) => setReuseValues(event.target.checked)}
            />
            <span className="text-[13px] text-fg">
              Keep this release's current values — otherwise the values below are sent with the new
              chart.
            </span>
          </label>
        </div>
      ) : null}

      {loading && !values ? (
        <p className="text-[13px] text-muted">Reading the release…</p>
      ) : (
        <YamlView
          value={draft}
          onChange={editing || (upgrading && !reuseValues) ? setDraft : undefined}
          className="min-h-[320px] flex-1"
        />
      )}

      <p className="text-[12px] text-muted">
        {editing || upgrading
          ? 'Rendered and written through the agent tunnel under your own identity, exactly as helm upgrade would. The cluster’s RBAC decides whether you may, and every object it writes is its own row in the audit trail.'
          : 'These are the values supplied at install or upgrade — what helm get values shows — not the chart’s defaults merged into them.'}
      </p>
    </>
  )
}

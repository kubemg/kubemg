import { useState } from 'react'
import { Check, ShieldAlert } from 'lucide-react'
import type { GuardrailRefusal } from '../api/client'
import { errorMessage, guardrailRefusal, installHelmRelease } from '../api/client'
import type { Cluster, HelmWriteResult } from '../api/types'
import { HelmChartPicker } from './HelmChartPicker'
import type { HelmChartSelection } from './HelmChartPicker'
import { HelmObjectReport } from './HelmObjectReport'
import { Button, Field, Notice, Select, Sheet, Spinner, TextInput } from './primitives'
import { YamlView } from './YamlView'

const RELEASE_NAME = /^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$/

/**
 * Installing a chart onto this cluster: pick a repository's chart and version,
 * name the release, write the values, install.
 *
 * There is no diff step, the same argument `CreateResourceSheet` makes for a
 * hand-typed manifest — against a release that does not exist yet, the answer
 * to "what does this change" is the values already on screen. What is different
 * here is that a chart, unlike a manifest, is not one object: the write goes
 * down the tunnel one object at a time, impersonated and audited per object,
 * which is what `HelmObjectReport` renders once the cluster has answered.
 */
export function HelmInstallSheet({
  cluster,
  namespace,
  namespaces,
  preset,
  onClose,
  onInstalled,
}: {
  cluster: Cluster
  /** The namespace the list is open on, or '' for "all namespaces". */
  namespace: string
  namespaces: string[]
  /**
   * A chart somebody else already chose — the vulnerability-alerts sheet's
   * scanner. Its namespace may not exist yet; when it does not, the install
   * creates it first (Helm's --create-namespace), and says so.
   */
  preset?: HelmChartSelection & { name: string; namespace: string }
  onClose: () => void
  onInstalled: () => Promise<void> | void
}) {
  const [selection, setSelection] = useState<HelmChartSelection>({
    repository: preset?.repository ?? '',
    chart: preset?.chart ?? '',
    version: preset?.version ?? '',
  })
  const [name, setName] = useState(preset?.name ?? '')
  const [target, setTarget] = useState(preset?.namespace || namespace || (namespaces[0] ?? ''))
  // Only a preset can name a namespace the cluster does not have: the picker
  // below lists the ones it does.
  const missingNamespace = target !== '' && !namespaces.includes(target)
  const namespaceOptions = missingNamespace ? [target, ...namespaces] : namespaces
  const [yaml, setYaml] = useState('{}\n')

  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [guardrail, setGuardrail] = useState<GuardrailRefusal | null>(null)
  const [result, setResult] = useState<HelmWriteResult | null>(null)

  const nameValid = RELEASE_NAME.test(name)
  const ready = selection.repository && selection.chart && selection.version && nameValid && target

  async function install() {
    if (!ready) return
    setBusy(true)
    setError(null)
    setGuardrail(null)
    try {
      const written = await installHelmRelease(cluster.id, {
        repository: selection.repository,
        chart: selection.chart,
        version: selection.version,
        name: name.trim(),
        namespace: target,
        yaml,
        create_namespace: missingNamespace,
      })
      setResult(written)
      if (written.applied !== false) await onInstalled()
    } catch (err) {
      const refusal = guardrailRefusal(err)
      if (refusal) setGuardrail(refusal)
      else setError(errorMessage(err, 'The cluster did not accept this install.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      width="xl"
      eyebrow={cluster.name}
      title="Install a chart"
      onClose={onClose}
      footer={
        result ? (
          <Button type="button" variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button type="button" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button type="button" variant="primary" onClick={() => void install()} disabled={busy || !ready}>
              {busy ? <Spinner className="size-3.5" /> : <Check aria-hidden="true" className="size-3.5" />}
              Install
            </Button>
          </>
        )
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {guardrail ? (
        <Notice tone="error">
          <span className="inline-flex items-start gap-1.5">
            <ShieldAlert aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
            <span>
              Blocked by the guardrail policy <strong>{guardrail.policy || 'unnamed'}</strong>
              {guardrail.scope ? ` (${guardrail.scope})` : ''}. {guardrail.message}
            </span>
          </span>
        </Notice>
      ) : null}

      {result ? (
        <HelmObjectReport result={result} />
      ) : (
        <>
          <HelmChartPicker selection={selection} onChange={setSelection} />

          <div className="grid gap-4 sm:grid-cols-2">
            <Field
              label="Release name"
              htmlFor="helm_install_name"
              error={name && !nameValid ? 'Lowercase letters, digits, dashes and dots.' : undefined}
            >
              <TextInput
                id="helm_install_name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                placeholder="ingress-nginx"
              />
            </Field>

            <Field
              label="Namespace"
              htmlFor="helm_install_namespace"
              hint={
                missingNamespace
                  ? 'Not in this cluster yet - the install creates it first. It is not recorded on the release, so uninstalling leaves it.'
                  : undefined
              }
            >
              <Select
                id="helm_install_namespace"
                value={target}
                disabled={namespaceOptions.length === 0}
                onChange={(event) => setTarget(event.target.value)}
              >
                {namespaceOptions.length === 0 ? <option value="">No namespaces</option> : null}
                {namespaceOptions.map((entry) => (
                  <option key={entry} value={entry}>
                    {entry}
                    {missingNamespace && entry === target ? ' (new)' : ''}
                  </option>
                ))}
              </Select>
            </Field>
          </div>

          <div className="flex flex-col gap-1.5">
            <span className="label">Values</span>
            <YamlView value={yaml} onChange={setYaml} className="min-h-[280px] flex-1" numbered />
            <p className="text-[12px] text-muted">
              Merged into the chart's own defaults - leave this as <span className="font-mono">{'{}'}</span>{' '}
              to install with nothing overridden. Rendered and written through the agent tunnel under
              your own identity, one object at a time; the cluster’s RBAC decides what may actually be
              created, and every object is its own row in the audit trail.
            </p>
          </div>
        </>
      )}
    </Sheet>
  )
}

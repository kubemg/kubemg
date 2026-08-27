import { useState } from 'react'
import { Loader2, Trash2 } from 'lucide-react'
import { errorMessage, uninstallHelmRelease } from '../api/client'
import type { Cluster, HelmRelease, HelmUninstallResult } from '../api/types'
import { HelmObjectTable } from './HelmObjectReport'
import { Button, Notice, Sheet } from './primitives'

/*
 * Removing a Helm release.
 *
 * The console could read a release, edit its values, see its history and roll
 * it back, and could not remove it — the one lifecycle verb every other list
 * has. This is the confirmation for it, and it exists as its own surface rather
 * than as a row in the bulk sheet for one reason: an uninstall is not one
 * delete, it is every object the release recorded, and the two limits it comes
 * with have to be read *before* the button rather than discovered afterwards.
 *
 * Both limits come off the response (`hook_notice`), not from anything written
 * here, so a build that narrows them changes what this says without a frontend
 * release. They are stated up front all the same, because "the chart's
 * pre-delete hook did not run" is not a thing to learn from a report.
 */

/** What the sheet says before anything has been removed. */
const UNINSTALL_BLURB =
  'Uninstalling removes every object this release recorded in its manifest, one call at a time ' +
  'and in reverse of the order they were installed — each answered by the cluster’s own RBAC and ' +
  'each its own line in the audit trail. The release’s own records go last, and only if ' +
  'everything else went: if something cannot be removed, the release is left in place so it ' +
  'still describes what is there.'

/**
 * The two things an uninstall here does not do. Said before the click rather
 * than in the report, because neither is recoverable by reading afterwards.
 */
const UNINSTALL_LIMITS =
  'Chart delete hooks are not run, and anything the chart did not render — a volume a ' +
  'StatefulSet expanded, anything a controller created, and any CustomResourceDefinition the ' +
  'chart shipped — is not in the manifest and stays behind.'

export function HelmUninstallSheet({
  cluster,
  release,
  onClose,
  onDone,
}: {
  cluster: Cluster
  release: HelmRelease
  onClose: () => void
  /** Re-reads the list behind this sheet, which is now describing the past. */
  onDone?: () => Promise<void> | void
}) {
  const [result, setResult] = useState<HelmUninstallResult | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function run() {
    if (busy) return
    setBusy(true)
    setError(null)
    try {
      const answer = await uninstallHelmRelease(cluster.id, release.name, release.namespace)
      setResult(answer)
      await onDone?.()
    } catch (err) {
      // The cluster's own words, or KubeMG's own refusal. A scoped grant is
      // told before the first delete which object it may not remove, and
      // flattening that into "something went wrong" would lose the only part
      // an operator can act on.
      setError(errorMessage(err, `${release.name} could not be uninstalled.`))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      onClose={onClose}
      eyebrow={`Revision ${release.revision} in ${release.namespace}`}
      title={`Uninstall ${release.name}`}
      width="lg"
      footer={
        result ? (
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose} disabled={busy}>
              Cancel
            </Button>
            <Button variant="danger" onClick={run} disabled={busy}>
              {busy ? (
                <Loader2 aria-hidden="true" className="size-4 animate-spin" />
              ) : (
                <Trash2 aria-hidden="true" className="size-4" />
              )}
              Uninstall
            </Button>
          </>
        )
      }
    >
      <div className="flex flex-col gap-3">
        {result ? (
          <>
            {result.removed ? (
              <Notice tone="ok">{result.message}</Notice>
            ) : (
              <Notice tone="error">{result.error ?? result.message}</Notice>
            )}
            <Notice tone="info">{result.hook_notice}</Notice>
            {result.objects ? <HelmObjectTable objects={result.objects} /> : null}
          </>
        ) : (
          <>
            {error ? <Notice tone="error">{error}</Notice> : null}
            <p className="text-[13px] leading-relaxed text-muted">{UNINSTALL_BLURB}</p>
            <Notice tone="warn">{UNINSTALL_LIMITS}</Notice>
          </>
        )}
      </div>
    </Sheet>
  )
}

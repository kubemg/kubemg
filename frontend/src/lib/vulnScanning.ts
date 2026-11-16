import type { VulnDetection, VulnRecommendation, VulnReportKind, VulnScanning } from '../api/types'
import type { Tone } from './status'

/**
 * Vulnerability alerts, PR 1: what the administrator's tile and sheet say.
 *
 * KubeMG scans nothing — it reads what the cluster's own scanner wrote — so the
 * sheet's one job is to walk an administrator to a connected scanner: add the
 * chart's repository, install the chart, connect. Which of those is offered is
 * decided here, from the server's own answers, so it can be asserted without a
 * DOM.
 */

/** The tile's state pill. A connection is a stored decision; its health is
    what the last look at the cluster found. */
export function vulnTileState(scanning: VulnScanning | null): { tone: Tone; label: string } {
  if (!scanning) return { tone: 'idle', label: 'Not connected' }
  if (!scanning.enabled) return { tone: 'idle', label: 'Paused' }
  if (scanning.last_status === 'healthy') return { tone: 'ok', label: 'Reading' }
  if (scanning.last_status === 'unhealthy') return { tone: 'bad', label: 'Needs a look' }
  return { tone: 'idle', label: 'Never checked' }
}

/**
 * The next step the sheet offers, in the order an administrator walks them.
 *
 *   - connected       — a row exists; the sheet manages it.
 *   - connect         — the scanner's reports are served; connect reads them.
 *   - add-repository  — nothing installed and no catalogue entry serves the
 *                       chart's repository; adding it comes first.
 *   - sync-repository — the repository is declared but its last sync recorded
 *                       no such chart yet (never synced, or failed).
 *   - install         — the catalogue holds the chart; install it.
 */
export type VulnNextStep = 'connected' | 'connect' | 'add-repository' | 'sync-repository' | 'install'

export function vulnNextStep(
  scanning: VulnScanning | null,
  detection: VulnDetection | null,
  recommendation: VulnRecommendation,
): VulnNextStep {
  if (scanning) return 'connected'
  if (detection?.installed) return 'connect'
  if (!recommendation.catalogue) return 'add-repository'
  if (!recommendation.catalogue.chart_version) return 'sync-repository'
  return 'install'
}

/** One report kind's line in the sheet: never a 0 for "not served". */
export function reportKindLabel(kind: VulnReportKind): { text: string; tone: Tone } {
  if (!kind.served) return { text: 'Not served by this cluster', tone: 'idle' }
  if (kind.reason) return { text: 'Refused', tone: 'bad' }
  if (kind.count === undefined) return { text: 'Served', tone: 'ok' }
  const noun = kind.count === 1 ? 'report' : 'reports'
  return { text: `${kind.approximate ? 'about ' : ''}${kind.count} ${noun}`, tone: 'ok' }
}

/** Whether the operator found is one that will refresh what it wrote. */
export function operatorState(detection: VulnDetection): { text: string; tone: Tone } {
  const operator = detection.operator
  if (!operator) {
    return detection.operator_reason
      ? { text: 'Could not be read', tone: 'warn' }
      : { text: 'No labelled Deployment found', tone: 'idle' }
  }
  if (operator.scanner_disabled) return { text: 'Image scanning turned off', tone: 'bad' }
  if (operator.ready_replicas === 0) return { text: 'Not running', tone: 'bad' }
  return { text: `Running ${operator.ready_replicas}/${operator.replicas}`, tone: 'ok' }
}

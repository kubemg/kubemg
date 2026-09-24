import type { AgentInstall } from '../api/types'
import { formatInstant } from './time'

/**
 * What the install URL in the agent's commands is good for.
 *
 * The URL carries a single-use download ticket, not the cluster's tunnel
 * credential: whichever form is fetched first — the manifest or the Kustomize
 * archive — spends it, and it dies unused at its expiry. Rendering the install
 * package again mints another, which is what the body's "New URL" does. This is the sentence that says so, derived once
 * so the wizard and the dashboard sheet cannot word it differently.
 */
export interface DownloadLinkState {
  expired: boolean
  note: string
}

export function downloadLinkState(expiresAt: string | undefined, now: number): DownloadLinkState {
  const at = expiresAt ? new Date(expiresAt).getTime() : NaN
  if (!Number.isFinite(at)) {
    return {
      expired: false,
      note: 'The URL in these commands works once.',
    }
  }
  if (at <= now) {
    return {
      expired: true,
      note: 'The URL in these commands has expired unused — mint a fresh one before running them.',
    }
  }
  return {
    expired: false,
    note:
      `The URL in these commands works once, until ${formatInstant(expiresAt)} — ` +
      'fetching either form spends it.',
  }
}

/**
 * saveManifest hands the reader the rendered manifest as a file, from the copy
 * already in memory. It never fetches the install URL: a browser download
 * would spend the single-use ticket the command on screen still needs.
 */
export function saveManifest(install: AgentInstall) {
  const blob = new Blob([install.manifest], { type: 'application/yaml' })
  const href = URL.createObjectURL(blob)
  const link = document.createElement('a')
  link.href = href
  link.download = `${install.cluster}-kubemg-agent.yaml`
  link.click()
  URL.revokeObjectURL(href)
}

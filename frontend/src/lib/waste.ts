import type {
  ClusterWaste,
  CostRates,
  UnpricedCategory,
  WasteContainer,
  WasteLoadBalancer,
  WasteVolume,
  WasteWorkload,
} from '../api/waste'

/**
 * What the waste page derives from the report: the YAML that applies a
 * recommendation, and the words for each state.
 *
 * **The recommendation is text, never a write.** It is a strategic-merge patch
 * that changes only the requests the report found over-asked — every other
 * field, limits included, is left exactly as the manifest has it — and it is
 * shown for an operator to apply where the manifest is kept. Usually that is
 * Git, and a button that wrote it to the cluster would be undone by the next
 * sync. Written by hand rather than with a YAML library, in `objectForm.ts`'s
 * style: the shape is fixed and four levels deep.
 */

/** cpuQuantity writes millicores the way a manifest would: whole cores bare, else `m`. */
export function cpuQuantity(millicores: number): string {
  return millicores % 1000 === 0 ? String(millicores / 1000) : `${millicores}m`
}

const MI = 1024 * 1024
const GI = 1024 * MI

/** memoryQuantity writes bytes as `Gi` when whole, else `Mi`, rounding up. */
export function memoryQuantity(bytes: number): string {
  if (bytes > 0 && bytes % GI === 0) return `${bytes / GI}Gi`
  return `${Math.ceil(bytes / MI)}Mi`
}

/** The containers a recommendation changes: those with at least one over-asked request. */
export function changedContainers(workload: WasteWorkload): WasteContainer[] {
  return workload.containers.filter(
    (container) =>
      (container.cpu.over && container.cpu.recommended !== undefined) ||
      (container.memory.over && container.memory.recommended !== undefined),
  )
}

function containerEntry(container: WasteContainer, indent: string): string[] {
  const lines = [`${indent}- name: ${container.name}`, `${indent}  resources:`, `${indent}    requests:`]
  if (container.cpu.over && container.cpu.recommended !== undefined) {
    lines.push(`${indent}      cpu: ${cpuQuantity(container.cpu.recommended)}`)
  }
  if (container.memory.over && container.memory.recommended !== undefined) {
    lines.push(`${indent}      memory: ${memoryQuantity(container.memory.recommended)}`)
  }
  return lines
}

/**
 * rightsizingPatch renders the recommendation as a strategic-merge patch, or
 * null when nothing would change. A native sidecar is an init container, so it
 * is patched under `initContainers` — strategic merge matches both lists by
 * `name`, so the patch touches those containers and no others.
 */
export function rightsizingPatch(workload: WasteWorkload): string | null {
  const changed = changedContainers(workload)
  if (changed.length === 0) return null

  const lines = ['spec:', '  template:', '    spec:']
  const regular = changed.filter((container) => !container.sidecar)
  const sidecars = changed.filter((container) => container.sidecar)
  if (regular.length > 0) {
    lines.push('      containers:')
    for (const container of regular) lines.push(...containerEntry(container, '        '))
  }
  if (sidecars.length > 0) {
    lines.push('      initContainers:')
    for (const container of sidecars) lines.push(...containerEntry(container, '        '))
  }
  return `${lines.join('\n')}\n`
}

/** rightsizingCommand is how the patch is applied by hand, saved as `rightsize.yaml`. */
export function rightsizingCommand(workload: WasteWorkload): string {
  return `kubectl -n ${workload.namespace} patch ${workload.kind.toLowerCase()} ${workload.name} --patch-file rightsize.yaml`
}

export const VOLUME_STATE_LABEL: Record<WasteVolume['state'], string> = {
  unmounted: 'Not mounted',
  released: 'Released',
  available: 'Never bound',
}

/** volumeExplanation says what a row means, and what deleting it would cost. */
export function volumeExplanation(volume: WasteVolume): string {
  switch (volume.state) {
    case 'unmounted':
      if (volume.statefulset && volume.scaled_down) {
        return `Kept by StatefulSet ${volume.statefulset} after it scaled down. It is reattached if it scales back up.`
      }
      if (volume.statefulset) {
        return `Belongs to StatefulSet ${volume.statefulset}, whose pod is not running.`
      }
      return 'Bound to a volume, and no running pod mounts it.'
    case 'released':
      return volume.claim
        ? `Its claim ${volume.claim} was deleted and the data kept (${volume.reclaim_policy || 'Retain'}).`
        : `Its claim was deleted and the data kept (${volume.reclaim_policy || 'Retain'}).`
    case 'available':
      return 'Provisioned and not bound to any claim.'
  }
}

export const BALANCER_STATE_LABEL: Record<WasteLoadBalancer['state'], string> = {
  'no-ready-endpoints': 'No ready endpoint',
  'no-selector': 'No selector',
}

/** sinceWindow reports a workload started inside the window: its reading covers less of it. */
export function sinceWindow(workload: WasteWorkload, windowStart: string): boolean {
  if (!windowStart) return false
  return Date.parse(workload.since) > Date.parse(windowStart)
}

/*
 * Money. Every figure is the server's — requests times the cluster's rates —
 * and the page only writes it down. A figure the server did not send is not
 * priced, and is never drawn as a zero.
 */

/**
 * formatMoney writes a monthly figure: cents below 100, whole units above,
 * because nobody reading "€1,204.37 a month" of an estimate acts on the 37.
 */
export function formatMoney(amount: number, currency: string, locale?: string): string {
  const digits = Math.abs(amount) >= 100 ? 0 : 2
  try {
    return new Intl.NumberFormat(locale, {
      style: 'currency',
      currency,
      minimumFractionDigits: digits,
      maximumFractionDigits: digits,
    }).format(amount)
  } catch {
    // A code Intl does not know is still the operator's: write it beside the figure.
    return `${amount.toFixed(digits)} ${currency}`
  }
}

/** formatRate writes a unit price, which is often a fraction of a cent. */
export function formatRate(amount: number, currency: string, locale?: string): string {
  try {
    return new Intl.NumberFormat(locale, {
      style: 'currency',
      currency,
      minimumFractionDigits: 2,
      maximumFractionDigits: 6,
    }).format(amount)
  } catch {
    return `${amount} ${currency}`
  }
}

/** The rate lines as the page states them, absent rates left out. */
export function rateLines(rates: CostRates, locale?: string): { term: string; value: string }[] {
  const lines = [
    { term: 'CPU', value: `${formatRate(rates.cpu_core_hour, rates.currency, locale)} per core-hour` },
    {
      term: 'Memory',
      value: `${formatRate(rates.memory_gib_hour, rates.currency, locale)} per GiB-hour`,
    },
  ]
  if (rates.storage_gib_month !== undefined) {
    lines.push({
      term: 'Storage',
      value: `${formatRate(rates.storage_gib_month, rates.currency, locale)} per GiB-month`,
    })
  }
  if (rates.load_balancer_hour !== undefined) {
    lines.push({
      term: 'Load balancer',
      value: `${formatRate(rates.load_balancer_hour, rates.currency, locale)} per hour`,
    })
  }
  return lines
}

export const UNPRICED_LABEL: Record<UnpricedCategory, string> = {
  storage: 'storage',
  load_balancers: 'load balancers',
}

/**
 * wasteCostSentence states what the page finds unused costs a month, and what
 * the figure leaves out. Null when nothing is priced.
 */
export function wasteCostSentence(report: ClusterWaste, locale?: string): string | null {
  const monthly = report.summary.monthly
  const currency = report.cost?.currency
  if (!monthly || !currency) return null
  const money = (amount: number) => formatMoney(amount, currency, locale)

  const parts: string[] = []
  if (report.workloads_available) parts.push(`${money(monthly.reclaimable)} in over-asked requests`)
  if (monthly.unused_storage !== undefined) parts.push(`${money(monthly.unused_storage)} in storage`)
  if (monthly.idle_load_balancers !== undefined) {
    parts.push(`${money(monthly.idle_load_balancers)} in load balancers`)
  }
  let sentence = `At this cluster’s rates, what this page finds unused costs about ${money(monthly.total)} a month`
  sentence += parts.length > 1 ? `: ${parts.join(', ')}.` : '.'
  if (monthly.unpriced.length > 0) {
    const names = monthly.unpriced.map((category) => UNPRICED_LABEL[category])
    sentence += ` ${capitalise(names.join(' and '))} ${names.length === 1 ? 'has' : 'have'} no rate, so ${names.length === 1 ? 'it is' : 'they are'} not counted.`
  }
  if (!report.workloads_available) {
    sentence += ' Over-asked requests need usage history, so they are not counted either.'
  }
  return sentence
}

function capitalise(text: string): string {
  return text.charAt(0).toUpperCase() + text.slice(1)
}

/**
 * parseRate reads one rate field: empty is "not priced", anything else must
 * be a plain non-negative number. A comma is taken as a decimal point, since
 * half the world writes 0,04.
 */
export function parseRate(text: string): number | undefined | null {
  const trimmed = text.trim().replace(',', '.')
  if (trimmed === '') return undefined
  if (!/^\d+(\.\d+)?$/.test(trimmed)) return null
  return Number(trimmed)
}

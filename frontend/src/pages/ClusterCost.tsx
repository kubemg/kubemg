import { useCallback, useEffect, useState } from 'react'
import type { FormEvent } from 'react'
import { Link, useParams } from 'react-router'
import { ChevronDown, Coins, RefreshCw, Settings2, Tag } from 'lucide-react'
import {
  clearClusterRateCard,
  errorMessage,
  fetchClusterCost,
  fetchClusterRateCard,
  fetchClusterRightsizing,
  fetchClusterWaste,
  saveClusterRateCard,
  unconfigured,
} from '../api/client'
import type {
  CostedWorkload,
  RateCard,
  RatePreset,
  SizingFinding,
  WasteFinding,
} from '../api/types'
import { AppShell } from '../components/AppShell'
import { AllocationSplit, NamespaceTreemap, ReservedVersusUsed } from '../components/CostCharts'
import { Button, EmptyState, Notice, Pill, Sheet, Spinner } from '../components/primitives'
import { RateCardFields, RatePresetChips } from '../components/RateCardForm'
import {
  BLANK_RATE_DRAFT,
  draftIsPriced,
  draftOfRateCard,
  inputOfDraft,
} from '../lib/ratecard'
import type { RateDraft } from '../lib/ratecard'
import { TableSkeleton } from '../components/SkeletonLoader'
import { queryKey, useCachedQuery } from '../lib/query'
import { formatCPU, formatMemory, formatMoney, formatRate } from '../lib/units'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useTimeRange } from '../state/timerange-context'

/**
 * What this cluster costs — and the three separate questions that turns into.
 *
 * **What does it cost.** Nodes, at rates an operator typed in. KubeMG calls no
 * billing API and holds no cloud credential, so every figure here is an
 * estimate and the rate card that produced it is on screen beside it. The two
 * totals deliberately do not match: a cluster buys nodes, not pods, so the
 * fleet's cost and the workloads' reservations are a whole and a part of it,
 * and the gap is reported as its own line. Spreading unallocated capacity over
 * the teams is what makes a showback number move when a *different* team scales
 * down, which is how a cost report stops being trusted.
 *
 * **What should it have cost.** Right-sizing, which needs history and therefore
 * needs a metrics datasource — metrics-server keeps two minutes, and a
 * recommendation from two minutes is a recommendation about two minutes. On a
 * cluster with no datasource this section refuses rather than degrades.
 *
 * **What is nobody using.** Orphaned volumes and load balancers: not oversized,
 * abandoned. Every finding carries the ordinary reason it might look like that,
 * because all three shapes have a false positive and somebody is going to
 * delete one of them.
 *
 * Nothing on this page writes to the cluster.
 */

const SEVERITY_TONE = {
  ok: 'ok',
  note: 'idle',
  warn: 'warn',
  danger: 'bad',
} as const

export function ClusterCost() {
  const { clusters, loading: clustersLoading } = useClusters()
  const { user } = useAuth()
  const params = useParams<{ id: string }>()
  const clusterId = Number(params.id)
  const { range } = useTimeRange()
  const [ratesOpen, setRatesOpen] = useState(false)

  const cluster =
    clusters.find(
      (entry) =>
        entry.id === clusterId && entry.connection_mode === 'agent' && entry.agent_attached,
    ) ?? null
  const unreachable = cluster ? null : (clusters.find((entry) => entry.id === clusterId) ?? null)

  const cost = useCachedQuery(cluster ? queryKey('cost', cluster.id) : null, async () => {
    if (!cluster) throw new Error('no cluster is selected')
    return fetchClusterCost(cluster.id)
  })
  const waste = useCachedQuery(cluster ? queryKey('waste', cluster.id) : null, async () => {
    if (!cluster) throw new Error('no cluster is selected')
    return fetchClusterWaste(cluster.id)
  })
  // Keyed on the window as well as the cluster: this is the one read here that
  // carries the console's time range, and an answer cached without it would
  // serve an hour's evidence to somebody who asked for a week's.
  const sizing = useCachedQuery(
    cluster ? queryKey('rightsizing', cluster.id, range) : null,
    async () => {
      if (!cluster) throw new Error('no cluster is selected')
      return fetchClusterRightsizing(cluster.id, range)
    },
  )

  if (!clustersLoading && !cluster) {
    return (
      <AppShell title="Cost">
        <div className="card">
          <EmptyState
            icon={<Coins aria-hidden="true" className="size-5" />}
            title={
              unreachable
                ? `${unreachable.name} has no live connection`
                : 'That cluster is not registered'
            }
          >
            {unreachable ? (
              <>
                Costs are computed from what the cluster reports right now, read through the agent
                tunnel, so a cluster without one has nothing to price.{' '}
                <Link
                  to={`/clusters/${unreachable.id}/summary`}
                  className="text-accent hover:underline"
                >
                  Open the cluster
                </Link>{' '}
                to check its connection.
              </>
            ) : (
              <>Pick a cluster from the fleet list to read its costs.</>
            )}
          </EmptyState>
        </div>
      </AppShell>
    )
  }

  const loaded = cost.data
  const loading = cost.loading || cost.revalidating
  const currency = loaded?.currency ?? 'USD'

  function refreshAll() {
    void cost.refresh()
    void waste.refresh()
    void sizing.refresh()
  }

  return (
    <AppShell
      title="Cost"
      fullWidth
      actions={
        <>
          <Button onClick={() => setRatesOpen(true)}>
            <Tag aria-hidden="true" className="size-4" />
            Rates
          </Button>
          <Button onClick={refreshAll} disabled={loading}>
            <RefreshCw aria-hidden="true" className={`size-4 ${loading ? 'animate-spin' : ''}`} />
            Refresh
          </Button>
        </>
      }
    >
      <div className="flex min-w-0 flex-col gap-4">
        {cost.error ? (
          <Notice tone="error">
            {errorMessage(cost.error, 'Could not read this cluster’s costs.')}
          </Notice>
        ) : null}

        {/* The ordinary state of a fresh install, and an answer rather than a
            failure: there is nothing to cost anything against yet. */}
        {loaded && !loaded.priced ? (
          <div className="card p-4">
            <EmptyState
              icon={<Settings2 aria-hidden="true" className="size-5" />}
              title="No rates are configured"
            >
              {loaded.reason}
              <span className="mt-3 block">
                <Link to="/settings" className="text-accent hover:underline">
                  Enter your rates in Settings
                </Link>
                , or{' '}
                <button
                  type="button"
                  onClick={() => setRatesOpen(true)}
                  className="text-accent hover:underline"
                >
                  price this cluster on its own
                </button>{' '}
                if it does not run on the same hardware as the rest of the fleet.
              </span>
            </EmptyState>
          </div>
        ) : null}

        {loading && !loaded ? <TableSkeleton columns={3} rows={4} label="Costing the cluster" /> : null}

        {loaded?.priced ? (
          <>
            <div className="grid min-w-0 gap-4 lg:grid-cols-[minmax(0,1fr)_minmax(0,1fr)]">
              <section className="card min-w-0 p-4">
                <AllocationSplit summary={loaded.summary} currency={currency} />
                <p className="mt-4 border-t border-line-soft pt-3 text-[12px] leading-relaxed text-muted">
                  An estimate over{' '}
                  <button
                    type="button"
                    onClick={() => setRatesOpen(true)}
                    className="text-accent hover:underline"
                  >
                    {rateSentence(loaded)}
                  </button>{' '}
                  — not a bill. A node is paid for whole
                  whether or not anything runs on it, so the fleet's cost and what the workloads
                  reserved are a total and a part of it rather than two numbers that should agree.
                </p>
              </section>

              <section className="card min-w-0 p-4">
                <h2 className="text-[14px] font-semibold text-fg">Where it goes</h2>
                <p className="mt-1 mb-3 text-[12px] text-muted">
                  Reserved capacity by namespace. Area is share of the total.
                </p>
                <NamespaceTreemap namespaces={loaded.namespaces} currency={currency} />
              </section>
            </div>

            {loaded.summary.idle_monthly.total > 0 ? (
              <Notice tone="info">
                {formatMoney(loaded.summary.idle_monthly.total, currency)} a month is reserved and
                not being spent. That is not money already saved — the reservation is what blocks
                other work from being scheduled, so it becomes a smaller bill only once the
                requests come down and a node can be given back.
              </Notice>
            ) : null}

            {!loaded.usage_available ? (
              <Notice tone="info">{loaded.usage_reason}</Notice>
            ) : null}

            <WorkloadTable
              workloads={loaded.workloads}
              total={loaded.workloads_total}
              currency={currency}
            />
          </>
        ) : null}

        <RightsizingSection
          query={sizing}
          clusterId={clusterId}
          fallbackCurrency={currency}
        />

        <WasteSection query={waste} />

        <p className="text-[12px] leading-relaxed text-muted">
          Every figure here is an estimate over rates entered by hand: KubeMG calls no cloud
          billing API and holds no cloud credential, so it reports what these reservations cost at
          those rates and never what was invoiced. Nothing on this page changes anything on the
          cluster.
        </p>
      </div>

      {ratesOpen ? (
        <RateCardSheet
          clusterId={clusterId}
          clusterName={cluster?.name ?? ''}
          canEdit={user?.role === 'admin'}
          onClose={() => setRatesOpen(false)}
          onSaved={refreshAll}
        />
      ) : null}
    </AppShell>
  )
}

/* ------------------------------------------------------------- the rates --- */

/**
 * The rates this cluster is priced at, and — for an administrator — the override
 * that replaces them.
 *
 * The override exists because a fleet is very often not one cloud. An
 * installation default is the right answer for the fleet and the wrong one for
 * the rack in the basement, and pricing an on-prem cluster at EC2's list price
 * is worse than pricing it at nothing.
 *
 * `inherited` is the distinction this surface exists to make visible: "priced at
 * the installation default" and "has a card of its own that happens to match it"
 * look identical on the numbers, and only one of them changes when the default
 * does. So the sheet says which, and clearing an override is offered as
 * returning to the default rather than as a delete.
 *
 * Anyone the cluster is granted to can read it, because every cost figure they
 * are shown is computed from it and an estimate whose rates are off screen is
 * one nobody can argue with. Writing it is an administrator's, like the default
 * it overrides.
 */
function RateCardSheet({
  clusterId,
  clusterName,
  canEdit,
  onClose,
  onSaved,
}: {
  clusterId: number
  clusterName: string
  canEdit: boolean
  onClose: () => void
  onSaved: () => void
}) {
  const [presets, setPresets] = useState<RatePreset[]>([])
  const [stored, setStored] = useState<RateCard | null>(null)
  const [draft, setDraft] = useState<RateDraft>(BLANK_RATE_DRAFT)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const response = await fetchClusterRateCard(clusterId)
      setPresets(response.presets)
      setStored(response.rate_card)
      setDraft(draftOfRateCard(response.rate_card))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read this cluster’s rates.'))
    } finally {
      setLoading(false)
    }
  }, [clusterId])

  useEffect(() => {
    void load()
  }, [load])

  function set<K extends keyof RateDraft>(field: K, value: RateDraft[K]) {
    setDraft((current) => ({ ...current, [field]: value }))
  }

  async function save(event: FormEvent) {
    event.preventDefault()
    setSaving(true)
    try {
      await saveClusterRateCard(clusterId, inputOfDraft(draft))
      setError(null)
      await load()
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save this cluster’s rates.'))
    } finally {
      setSaving(false)
    }
  }

  async function revert() {
    setSaving(true)
    try {
      await clearClusterRateCard(clusterId)
      setError(null)
      await load()
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not return this cluster to the installation default.'))
    } finally {
      setSaving(false)
    }
  }

  // An override exists when a card came back and it is this cluster's own. The
  // form is seeded from whichever card is in force, so overriding an inherited
  // one starts from the rates already on screen rather than from a blank sheet.
  const overridden = stored !== null && !stored.inherited
  const priced = draftIsPriced(draft)

  return (
    <Sheet
      eyebrow={clusterName}
      title="Rates"
      width="lg"
      onClose={onClose}
      onSubmit={canEdit ? save : undefined}
      footer={
        canEdit ? (
          <>
            {overridden ? (
              <Button variant="ghost" type="button" onClick={() => void revert()} disabled={saving}>
                Use the installation default
              </Button>
            ) : null}
            <Button variant="ghost" type="button" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" type="submit" disabled={saving || loading}>
              {overridden ? 'Save this cluster’s rates' : 'Override for this cluster'}
            </Button>
          </>
        ) : (
          <Button type="button" onClick={onClose}>
            Close
          </Button>
        )
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      {loading ? (
        <div className="flex items-center gap-2 text-[13px] text-muted">
          <Spinner /> Reading the rates
        </div>
      ) : (
        <>
          <p className="text-[12.5px] leading-relaxed text-muted">
            {stored === null ? (
              <>
                Nothing prices this cluster yet, so its costs are reported as unpriced rather than
                as zeroes.
              </>
            ) : stored.inherited ? (
              <>
                This cluster is priced at the <span className="text-fg">installation default</span>
                . Override it when the cluster does not run on the same hardware as the rest of the
                fleet — another cloud, or a rack somebody owns — because pricing an on-prem cluster
                at a cloud list price is worse than pricing it at nothing. An override stops
                following the default: a later change there will not reach this cluster.
              </>
            ) : (
              <>
                This cluster has <span className="text-fg">rates of its own</span>, which the
                installation default no longer reaches. Returning it to the default is one button
                below.
              </>
            )}
          </p>

          {!canEdit ? (
            <>
              <Notice tone="info">
                These are the rates every cost figure on this page is computed from. Changing them
                is an administrator's, like the installation default they override.
              </Notice>
              <RateCardReadout card={stored} />
            </>
          ) : (
            <>
              <RatePresetChips
                presets={presets}
                active={draft.provider}
                idPrefix={`cluster-${clusterId}-rate`}
                onApply={setDraft}
              />

              <RateCardFields
                draft={draft}
                onChange={set}
                idPrefix={`cluster-${clusterId}-rate`}
              />

              {!priced ? (
                <Notice tone="info">
                  With no rate set, this cluster is reported as unpriced rather than shown zeroes.
                  Idle volumes and load balancers are still found — what nothing is using is worth
                  finding either way.
                </Notice>
              ) : null}

              <p className="text-[11.5px] leading-relaxed text-faint">
                KubeMG calls no cloud billing API and holds no cloud credential, so these are
                entered rather than discovered, and everything computed from them is an estimate
                rather than a bill.
              </p>
            </>
          )}
        </>
      )}
    </Sheet>
  )
}

/** The rates as prose, for a reader who cannot change them. */
function RateCardReadout({ card }: { card: RateCard | null }) {
  if (!card) return null
  return (
    <dl className="grid gap-x-6 gap-y-1.5 sm:grid-cols-2">
      <RateLine label="CPU, per vCPU-hour" value={card.cpu_core_hour} currency={card.currency} />
      <RateLine label="Memory, per GiB-hour" value={card.memory_gib_hour} currency={card.currency} />
      <RateLine
        label="Storage, per GiB-month"
        value={card.storage_gib_month}
        currency={card.currency}
      />
      <RateLine
        label="Load balancer, per month"
        value={card.load_balancer_month}
        currency={card.currency}
      />
      {card.note ? (
        <div className="sm:col-span-2">
          <dt className="label text-faint">What these rates are</dt>
          <dd className="mt-1 text-[12.5px] leading-relaxed text-muted">{card.note}</dd>
        </div>
      ) : null}
    </dl>
  )
}

function RateLine({
  label,
  value,
  currency,
}: {
  label: string
  value: number
  currency: string
}) {
  return (
    <div className="flex items-baseline justify-between gap-3 border-b border-line-soft py-1.5">
      <dt className="text-[12.5px] text-muted">{label}</dt>
      <dd className="font-mono text-[12.5px] text-fg tabular-nums">
        {value > 0 ? formatRate(value, currency) : 'not set'}
      </dd>
    </div>
  )
}

/** rateSentence names the rates in one clause, so the estimate carries its own provenance. */
function rateSentence(cost: { rate_card: { provider: string; inherited: boolean } | null }): string {
  if (!cost.rate_card) return 'the configured rates'
  const label = cost.rate_card.provider === 'custom' ? 'your own rates' : `${cost.rate_card.provider.toUpperCase()} rates`
  return cost.rate_card.inherited ? `${label}, inherited from the installation default` : label
}

/* ------------------------------------------------------------- workloads --- */

function WorkloadTable({
  workloads,
  total,
  currency,
}: {
  workloads: CostedWorkload[]
  total: number
  currency: string
}) {
  if (workloads.length === 0) return null

  return (
    <section className="card min-w-0 overflow-hidden">
      <div className="flex flex-wrap items-center gap-3 border-b border-line-soft px-4 py-3">
        <h2 className="text-[14px] font-semibold text-fg">What is reserving it</h2>
        {total > workloads.length ? (
          <span className="text-[12px] text-muted">
            the {workloads.length} costliest of {total}
          </span>
        ) : null}
      </div>

      <ul className="divide-y divide-line-soft">
        {workloads.map((workload) => (
          <li key={`${workload.namespace}/${workload.kind}/${workload.name}`} className="min-w-0 px-4 py-3">
            <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
              <span className="label text-faint">{workload.kind}</span>
              <span className="min-w-0 truncate font-mono text-[13px] text-fg">
                {workload.name}
              </span>
              <span className="font-mono text-[11.5px] text-faint">{workload.namespace}</span>
              <span className="ml-auto font-mono text-[13px] font-semibold text-fg tabular-nums">
                {formatMoney(workload.monthly.total, currency)}
              </span>
            </div>

            <div className="mt-2 grid gap-3 sm:grid-cols-2">
              <ReservedVersusUsed
                label={`${workload.name} CPU`}
                reservedPercent={100}
                usedPercent={sharePercent(workload.used_cpu_millicores, workload.cpu_millicores)}
                detail={
                  workload.used
                    ? `CPU ${formatCPU(workload.cpu_millicores)} reserved · ${formatCPU(workload.used_cpu_millicores)} used`
                    : `CPU ${formatCPU(workload.cpu_millicores)} reserved · not measured`
                }
              />
              <ReservedVersusUsed
                label={`${workload.name} memory`}
                reservedPercent={100}
                usedPercent={sharePercent(workload.used_memory_bytes, workload.memory_bytes)}
                detail={
                  workload.used
                    ? `Memory ${formatMemory(workload.memory_bytes)} reserved · ${formatMemory(workload.used_memory_bytes)} used`
                    : `Memory ${formatMemory(workload.memory_bytes)} reserved · not measured`
                }
              />
            </div>

            <p className="mt-1.5 font-mono text-[11px] text-faint tabular-nums">
              {workload.pods} {workload.pods === 1 ? 'pod' : 'pods'}
              {workload.idle_monthly.total > 0
                ? ` · ${formatMoney(workload.idle_monthly.total, currency)} of it reserved and unspent`
                : ''}
            </p>
          </li>
        ))}
      </ul>
    </section>
  )
}

function sharePercent(part: number, whole: number): number {
  if (whole <= 0) return 0
  return (part / whole) * 100
}

/* ---------------------------------------------------------- right-sizing --- */

interface Query<T> {
  data: T | null
  error: unknown
  loading: boolean
  revalidating: boolean
}

function RightsizingSection({
  query,
  clusterId,
  fallbackCurrency,
}: {
  query: Query<import('../api/types').ClusterRightsizing>
  clusterId: number
  fallbackCurrency: string
}) {
  // The refusal this whole feature is built on, and the console says what to do
  // about it rather than showing an error.
  if (unconfigured(query.error)) {
    return (
      <section className="card p-4">
        <EmptyState
          icon={<Coins aria-hidden="true" className="size-5" />}
          title="No metrics datasource, so no recommendations"
        >
          Right-sizing needs a window of history. The cluster's Metrics API keeps about two
          minutes, and a recommendation from two minutes is a recommendation about two minutes —
          a nightly job sampled at midday uses nothing. So KubeMG offers none here rather than one
          nobody should follow. The costs above are unaffected: they are reservations, which the
          pod specs already state.
          <span className="mt-3 block">
            <Link to={`/clusters/${clusterId}/summary`} className="text-accent hover:underline">
              Register a metrics datasource
            </Link>
          </span>
        </EmptyState>
      </section>
    )
  }

  const report = query.data
  if (query.error) {
    return (
      <Notice tone="error">
        {errorMessage(query.error, 'Could not read right-sizing recommendations.')}
      </Notice>
    )
  }
  if (!report) return null

  const currency = report.currency || fallbackCurrency
  const { summary } = report

  return (
    <section className="card min-w-0 overflow-hidden">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line-soft px-4 py-3">
        <h2 className="text-[14px] font-semibold text-fg">What they should have asked for</h2>
        {report.priced && summary.monthly_saving > 0 ? (
          <Pill tone="ok">{formatMoney(summary.monthly_saving, currency)} a month</Pill>
        ) : null}
        {summary.under_reserved > 0 ? (
          <Pill tone="warn">{summary.under_reserved} under-reserved</Pill>
        ) : null}
        <span className="ml-auto text-[11.5px] text-faint">{report.coverage}</span>
      </div>

      {report.findings.length === 0 ? (
        <p className="px-4 py-8 text-center text-[13px] text-muted">
          {summary.workloads === 0
            ? 'Nothing to measure on this cluster yet.'
            : `${summary.right_sized} of ${summary.workloads} workloads are sized about right, and ` +
              `${summary.unmeasured} had too little history in this window to judge.`}
        </p>
      ) : (
        <ul className="divide-y divide-line-soft">
          {report.findings.map((finding) => (
            <SizingRow
              key={`${finding.namespace}/${finding.kind}/${finding.name}`}
              finding={finding}
              currency={currency}
              priced={report.priced}
            />
          ))}
        </ul>
      )}

      <p className="border-t border-line-soft px-4 py-3 text-[12px] leading-relaxed text-muted">
        Recommendations are for <span className="text-fg">requests only</span>. A request is a
        reservation — it decides scheduling and it is what this can price. A limit is a
        reliability decision about what should happen to a container that misbehaves, and the
        right value for it is not visible in a usage series. CPU is sized from the sustained mean
        and memory from the observed peak, because CPU over its share is throttled and memory over
        its share is killed. {summary.unmeasured > 0
          ? `${summary.unmeasured} workloads were excluded for want of evidence over this window.`
          : ''}
      </p>
    </section>
  )
}

function SizingRow({
  finding,
  currency,
  priced,
}: {
  finding: SizingFinding
  currency: string
  priced: boolean
}) {
  const [open, setOpen] = useState(false)

  return (
    <li className="min-w-0 px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <Pill tone={SEVERITY_TONE[finding.severity]}>{finding.title}</Pill>
        <span className="label text-faint">{finding.kind}</span>
        <span className="min-w-0 truncate font-mono text-[13px] text-fg">{finding.name}</span>
        <span className="font-mono text-[11.5px] text-faint">{finding.namespace}</span>
        {priced && finding.monthly_saving > 0 ? (
          <span className="ml-auto font-mono text-[13px] font-semibold text-ok tabular-nums">
            −{formatMoney(finding.monthly_saving, currency)}
          </span>
        ) : null}
      </div>

      <p className="mt-1.5 text-[12.5px] leading-relaxed text-muted">{finding.detail}</p>

      <dl className="mt-2 grid gap-x-6 gap-y-1 sm:grid-cols-2">
        <AdviceLine
          label="CPU"
          advice={finding.cpu}
          format={formatCPU}
        />
        <AdviceLine
          label="Memory"
          advice={finding.memory}
          format={formatMemory}
        />
      </dl>

      {finding.patch ? (
        <>
          <button
            type="button"
            aria-expanded={open}
            onClick={() => setOpen((current) => !current)}
            className="mt-2 inline-flex items-center gap-1.5 text-[12px] text-accent hover:underline"
          >
            <ChevronDown
              aria-hidden="true"
              className={`size-3.5 transition-transform ${open ? 'rotate-180' : ''}`}
            />
            {open ? 'Hide the patch' : 'Show the patch'}
          </button>

          {open ? (
            <div className="mt-2 rounded-control border border-line-soft bg-sunken p-3">
              <pre className="overflow-x-auto font-mono text-[11.5px] leading-relaxed text-fg">
                {finding.patch}
              </pre>
              <p className="mt-2 text-[11.5px] leading-relaxed text-faint">
                A strategic-merge patch against the pod template, requests only.{' '}
                {finding.containers.length > 1
                  ? 'The measurement is per pod, so the split across containers follows what they ' +
                    'currently request — check it against what each one actually does before ' +
                    'applying. '
                  : ''}
                KubeMG does not apply this: it belongs wherever your manifests live.
              </p>
            </div>
          ) : null}
        </>
      ) : null}
    </li>
  )
}

function AdviceLine({
  label,
  advice,
  format,
}: {
  label: string
  advice: import('../api/types').SizeAdvice
  format: (value: number) => string
}) {
  if (!advice.measured) {
    return (
      <div className="flex items-baseline gap-2 font-mono text-[12px]">
        <dt className="text-faint">{label}</dt>
        <dd className="text-faint">not measured over this window</dd>
      </div>
    )
  }
  return (
    <div className="flex flex-wrap items-baseline gap-x-2 font-mono text-[12px] tabular-nums">
      <dt className="text-faint">{label}</dt>
      <dd className="text-muted">
        {format(advice.requested)} reserved · {format(advice.observed)} used
        {advice.recommended > 0 ? (
          <>
            {' '}
            <span className="text-fg">→ {format(advice.recommended)}</span>
          </>
        ) : null}
      </dd>
    </div>
  )
}

/* ------------------------------------------------------------ idle things --- */

function WasteSection({ query }: { query: Query<import('../api/types').ClusterWaste> }) {
  const report = query.data
  if (query.error) {
    return (
      <Notice tone="error">
        {errorMessage(query.error, 'Could not read this cluster’s idle resources.')}
      </Notice>
    )
  }
  if (!report) return null

  const currency = report.currency ?? 'USD'

  return (
    <section className="card min-w-0 overflow-hidden">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 border-b border-line-soft px-4 py-3">
        <h2 className="text-[14px] font-semibold text-fg">Nothing is using these</h2>
        {report.priced && report.summary.monthly > 0 ? (
          <Pill tone="warn">{formatMoney(report.summary.monthly, currency)} a month</Pill>
        ) : null}
        {!report.priced ? <span className="text-[12px] text-muted">{report.reason}</span> : null}
      </div>

      {report.findings.length === 0 ? (
        <p className="px-4 py-8 text-center text-[13px] text-muted">
          No orphaned volumes or load balancers on this cluster.
        </p>
      ) : (
        <ul className="divide-y divide-line-soft">
          {report.findings.map((finding) => (
            <WasteRow
              key={`${finding.code}/${finding.namespace ?? ''}/${finding.name}`}
              finding={finding}
              currency={currency}
              priced={report.priced}
            />
          ))}
        </ul>
      )}

      <p className="border-t border-line-soft px-4 py-3 text-[12px] leading-relaxed text-muted">
        These are findings to triage, not verdicts — each carries the ordinary reason it might look
        exactly like this, and KubeMG deletes none of them. What it cannot see is worth saying too:
        a cloud load balancer or an elastic IP that no Service ever owned is invisible from inside
        the cluster, and finding those needs the cloud account KubeMG deliberately has no
        credential for.
      </p>
    </section>
  )
}

function WasteRow({
  finding,
  currency,
  priced,
}: {
  finding: WasteFinding
  currency: string
  priced: boolean
}) {
  const [open, setOpen] = useState(false)

  return (
    <li className="min-w-0 px-4 py-3">
      <div className="flex flex-wrap items-baseline gap-x-2.5 gap-y-1">
        <Pill tone={SEVERITY_TONE[finding.severity]}>{finding.title}</Pill>
        <span className="label text-faint">{finding.kind}</span>
        <span className="min-w-0 truncate font-mono text-[13px] text-fg">{finding.name}</span>
        {finding.namespace ? (
          <span className="font-mono text-[11.5px] text-faint">{finding.namespace}</span>
        ) : null}
        <span className="ml-auto font-mono text-[12.5px] text-fg tabular-nums">
          {priced && finding.monthly > 0 ? formatMoney(finding.monthly, currency) : '—'}
        </span>
      </div>

      <p className="mt-1.5 text-[12.5px] leading-relaxed text-muted">{finding.detail}</p>

      <div className="mt-1 flex flex-wrap items-center gap-x-3">
        <span className="font-mono text-[11px] text-faint tabular-nums">
          {finding.age_days} {finding.age_days === 1 ? 'day' : 'days'} old
          {finding.bytes ? ` · ${formatMemory(finding.bytes)}` : ''}
        </span>
        <button
          type="button"
          aria-expanded={open}
          onClick={() => setOpen((current) => !current)}
          className="text-[12px] text-accent hover:underline"
        >
          {open ? 'Hide' : 'Why this might be fine'}
        </button>
      </div>

      {open ? (
        <p className="mt-2 rounded-control border border-line-soft bg-sunken p-3 text-[12.5px] leading-relaxed text-muted">
          {finding.caveat}
        </p>
      ) : null}
    </li>
  )
}

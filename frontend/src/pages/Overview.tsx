import { Link, useNavigate } from 'react-router-dom'
import { RefreshCw } from 'lucide-react'
import { useState } from 'react'
import { checkCluster, errorMessage } from '../api/client'
import type { Cluster, Environment } from '../api/types'
import { AppShell } from '../components/AppShell'
import { Button, EnvironmentTag, Notice, Pill } from '../components/primitives'
import { SPINE_TONE } from '../lib/status'
import { relativeAge } from '../lib/time'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'

/* Bands run prod first: the fleet is read top-down by how much a cluster matters. */
const BANDS: Array<{ environment: Environment; title: string }> = [
  { environment: 'prod', title: 'Production' },
  { environment: 'staging', title: 'Staging' },
  { environment: 'dev', title: 'Development' },
]

/** A check older than this reads as stale, and the ribbon bar drops to its floor. */
const STALE_AFTER_MS = 24 * 60 * 60 * 1000

export function Overview() {
  const { clusters, loading, error, reload } = useClusters()
  const { user } = useAuth()
  const [checking, setChecking] = useState(false)
  const [checkError, setCheckError] = useState<string | null>(null)

  const reachable = clusters.filter((cluster) => cluster.status === 'healthy').length
  const unreachable = clusters.filter((cluster) => cluster.status === 'unhealthy').length
  const unchecked = clusters.filter((cluster) => cluster.status === 'pending').length
  const lastChecked = clusters
    .map((cluster) => cluster.last_checked_at)
    .filter((value): value is string => Boolean(value))
    .sort()
    .at(-1)

  async function checkAll() {
    setChecking(true)
    setCheckError(null)
    try {
      const results = await Promise.allSettled(clusters.map((cluster) => checkCluster(cluster.id)))
      const failed = results.find((result) => result.status === 'rejected')
      if (failed && failed.status === 'rejected') {
        setCheckError(errorMessage(failed.reason, 'Some clusters could not be checked.'))
      }
      await reload()
    } finally {
      setChecking(false)
    }
  }

  return (
    <AppShell
      title="Fleet overview"
      actions={
        user?.role === 'admin' && clusters.length > 0 ? (
          <Button variant="primary" onClick={checkAll} disabled={checking}>
            <RefreshCw aria-hidden="true" className={`size-3.5 ${checking ? 'animate-spin' : ''}`} />
            {checking ? 'Checking…' : 'Check all clusters'}
          </Button>
        ) : null
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {checkError ? <Notice tone="error">{checkError}</Notice> : null}

        {clusters.length > 0 ? (
          <FleetRibbon
            clusters={clusters}
            reachable={reachable}
            unreachable={unreachable}
            unchecked={unchecked}
            lastChecked={lastChecked}
          />
        ) : null}

        {loading && clusters.length === 0 ? (
          <p className="text-[12px] text-muted">Loading fleet…</p>
        ) : null}

        {!loading && clusters.length === 0 ? (
          <div className="panel px-3 py-12 text-center">
            <p className="text-[14px] font-medium text-fg">Nothing to manage yet</p>
            <p className="mt-1 text-[12px] text-muted">
              {user?.role === 'admin'
                ? 'Register a cluster to bring it under KubeMG.'
                : 'Ask an administrator for access to a cluster.'}
            </p>
            {user?.role === 'admin' ? (
              <Link
                to="/clusters"
                className="mt-3 inline-flex rounded-[5px] border border-primary bg-primary px-2.5 py-1.5 text-[13px] font-medium text-white transition-[filter] hover:brightness-110"
              >
                Go to clusters
              </Link>
            ) : null}
          </div>
        ) : null}

        {BANDS.map(({ environment, title }) => {
          const band = clusters.filter((cluster) => cluster.environment === environment)
          if (band.length === 0) return null

          const bandFailing = band.filter((cluster) => cluster.status === 'unhealthy').length

          return (
            <section key={environment}>
              <div className="mb-2.5 flex items-center gap-2.5">
                <EnvironmentTag environment={environment} />
                <h2 className="text-[13px] font-semibold text-fg">{title}</h2>
                <span aria-hidden="true" className="h-px flex-1 bg-line" />
                <span className="text-[11.5px] text-muted">
                  {band.length} {band.length === 1 ? 'cluster' : 'clusters'}
                  {bandFailing > 0 ? (
                    <span className="text-danger"> · {bandFailing} failing</span>
                  ) : null}
                </span>
              </div>

              <ul className="grid gap-2.5 sm:grid-cols-2 xl:grid-cols-3">
                {band.map((cluster) => (
                  <li key={cluster.id}>
                    <ClusterTile cluster={cluster} />
                  </li>
                ))}
              </ul>
            </section>
          )
        })}
      </div>
    </AppShell>
  )
}

/** How fresh a cluster's last check is, as a 0–1 fraction of the staleness window. */
function freshness(cluster: Cluster): number {
  if (!cluster.last_checked_at) return 0
  const age = Date.now() - new Date(cluster.last_checked_at).getTime()
  if (!Number.isFinite(age)) return 0
  return Math.max(0, Math.min(1, 1 - age / STALE_AFTER_MS))
}

/**
 * FleetRibbon is the fleet in one glance: a bar per cluster, coloured by its
 * last known state and scaled by how recently that state was confirmed. A
 * failing or long-unchecked cluster is visible before you read a word.
 */
function FleetRibbon({
  clusters,
  reachable,
  unreachable,
  unchecked,
  lastChecked,
}: {
  clusters: Cluster[]
  reachable: number
  unreachable: number
  unchecked: number
  lastChecked?: string
}) {
  const navigate = useNavigate()

  return (
    <section className="panel p-3.5">
      <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1">
        <h2 className="text-[19px] font-semibold tracking-[-0.015em] text-fg">
          {reachable} of {clusters.length} {clusters.length === 1 ? 'cluster' : 'clusters'} reachable
        </h2>
        <p className="text-[13px] text-muted">
          {unreachable > 0 ? <span className="text-danger">{unreachable} failing · </span> : null}
          {unchecked > 0 ? <span>{unchecked} never checked · </span> : null}
          last swept {relativeAge(lastChecked)}
        </p>
      </div>

      <div className="mt-3 flex h-12 items-end gap-[3px]" role="group" aria-label="Fleet health">
        {clusters.map((cluster) => {
          const height = 26 + Math.round(freshness(cluster) * 74)
          const detail =
            cluster.status === 'pending'
              ? 'never checked'
              : `${cluster.status === 'healthy' ? 'healthy' : 'unreachable'} · checked ${relativeAge(cluster.last_checked_at)}`

          return (
            <button
              key={cluster.id}
              type="button"
              onClick={() => navigate(`/clusters/${cluster.id}`)}
              title={`${cluster.name} · ${detail}`}
              style={{ height: `${height}%` }}
              className={`min-w-1.5 flex-1 rounded-[2px] origin-bottom transition-transform hover:scale-y-110 ${SPINE_TONE[cluster.status]}`}
            >
              <span className="sr-only">
                {cluster.name}: {detail}
              </span>
            </button>
          )
        })}
      </div>

      <div className="mt-2.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-[11.5px] text-muted">
        <LegendKey tone="bg-ok">reachable</LegendKey>
        <LegendKey tone="bg-danger">unreachable</LegendKey>
        <LegendKey tone="bg-faint/40">never checked</LegendKey>
        <span className="ml-auto text-faint">bar height = how recently the check ran</span>
      </div>
    </section>
  )
}

function LegendKey({ tone, children }: { tone: string; children: string }) {
  return (
    <span className="inline-flex items-center gap-1.5">
      <span aria-hidden="true" className={`inline-block size-1.5 rounded-full ${tone}`} />
      {children}
    </span>
  )
}

function ClusterTile({ cluster }: { cluster: Cluster }) {
  const failing = cluster.status === 'unhealthy'

  return (
    <Link
      to={`/clusters/${cluster.id}`}
      className="relative block overflow-hidden rounded-panel border border-line bg-surface p-3 pl-3.5 transition-[border-color,box-shadow] hover:border-primary/45 hover:lift"
    >
      <span
        aria-hidden="true"
        className={`absolute inset-y-0 left-0 w-[3px] ${SPINE_TONE[cluster.status]}`}
      />

      <div className="flex items-center gap-2">
        <span className="min-w-0 flex-1 truncate font-mono text-[13.5px] font-semibold text-fg">
          {cluster.name}
        </span>
        <Pill tone={failing ? 'bad' : cluster.status === 'healthy' ? 'ok' : 'neutral'}>
          {failing ? 'Unreachable' : cluster.status === 'healthy' ? 'Healthy' : 'Unchecked'}
        </Pill>
      </div>

      <p className="mt-1.5 truncate font-mono text-[11.5px] text-muted" title={cluster.api_url}>
        {cluster.api_url}
      </p>

      <div className="mt-2.5 grid grid-cols-2 gap-x-3 gap-y-1.5 border-t border-line-soft pt-2.5">
        <div>
          <p className="label">Version</p>
          <p className="font-mono text-[12.5px] text-fg">{cluster.kubernetes_version ?? '—'}</p>
        </div>
        <div className="min-w-0">
          <p className="label">Your access</p>
          <p className="truncate font-mono text-[12.5px] text-fg">{cluster.k8s_role}</p>
        </div>
        <div className="col-span-2 min-w-0">
          <p className="label">{failing ? 'Last error' : 'Last check'}</p>
          <p
            className={`truncate font-mono text-[12.5px] ${failing ? 'text-danger' : 'text-fg'}`}
            title={cluster.status_message}
          >
            {failing
              ? (cluster.status_message ?? 'Unreachable')
              : cluster.status === 'pending'
                ? 'never'
                : relativeAge(cluster.last_checked_at)}
          </p>
        </div>
      </div>
    </Link>
  )
}

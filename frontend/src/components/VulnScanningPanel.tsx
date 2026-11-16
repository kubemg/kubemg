import { useCallback, useEffect, useState } from 'react'
import { ExternalLink, Plug, RefreshCw, Settings2, ShieldAlert } from 'lucide-react'
import {
  checkVulnScanning,
  deleteVulnScanning,
  detectVulnScanning,
  errorMessage,
  fetchNamespaces,
  fetchVulnScanning,
  putHelmRepository,
  putVulnScanning,
  syncHelmRepository,
} from '../api/client'
import type { Cluster, VulnDetection, VulnRecommendation, VulnScanning } from '../api/types'
import { relativeAge } from '../lib/time'
import { operatorState, reportKindLabel, vulnNextStep, vulnTileState } from '../lib/vulnScanning'
import { HelmInstallSheet } from './HelmInstallSheet'
import { IntegrationGroup, IntegrationTile } from './IntegrationTile'
import { Button, DetailList, IconButton, Notice, Pill, Sheet, Spinner, Switch } from './primitives'

/**
 * Vulnerability alerts for one cluster — the administrator's switch.
 *
 * kubemg scans nothing. A scanner already in the cluster (Trivy Operator) pulls
 * the images and writes what it found as custom resources; this tile connects
 * kubemg to those reports. When there is no scanner, the sheet walks to one
 * through the Helm catalogue that already exists here: add the repository,
 * install the chart, connect.
 *
 * The tile reads stored state only. The live look at the cluster happens when
 * the sheet opens — the dashboard this sits on is opened far more often than
 * the answer changes, and each look is four reads down the tunnel.
 */
export function VulnScanningPanel({ cluster, bare = false }: { cluster: Cluster; bare?: boolean }) {
  const [scanning, setScanning] = useState<VulnScanning | null>(null)
  const [recommendation, setRecommendation] = useState<VulnRecommendation | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [open, setOpen] = useState(false)
  const [checking, setChecking] = useState(false)

  const load = useCallback(async () => {
    try {
      const result = await fetchVulnScanning(cluster.id)
      setScanning(result.scanning)
      setRecommendation(result.recommendation)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not read this cluster’s vulnerability alerts.'))
    }
  }, [cluster.id])

  useEffect(() => {
    void load()
  }, [load])

  async function check() {
    setChecking(true)
    try {
      const result = await checkVulnScanning(cluster.id)
      setScanning(result.scanning)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'The check did not finish.'))
    } finally {
      setChecking(false)
    }
  }

  const state = vulnTileState(scanning)
  const tile = (
    <IntegrationTile
      icon={ShieldAlert}
      title="Vulnerability alerts"
      wired={scanning !== null}
      meta={
        scanning ? (
          <>
            <span className="font-data">Trivy Operator</span>
            {scanning.detected_version ? (
              <span className="font-data text-faint"> · {scanning.detected_version}</span>
            ) : null}
          </>
        ) : undefined
      }
      state={
        <Pill tone={state.tone} dot={scanning !== null} title={scanning?.last_message}>
          {state.label}
        </Pill>
      }
      actions={
        scanning ? (
          <>
            <IconButton label="Check the scanner now" onClick={() => void check()} disabled={checking}>
              <RefreshCw aria-hidden="true" className={`size-3.5 ${checking ? 'animate-spin' : ''}`} />
            </IconButton>
            <IconButton label="Manage vulnerability alerts" onClick={() => setOpen(true)}>
              <Settings2 aria-hidden="true" className="size-3.5" />
            </IconButton>
          </>
        ) : (
          <Button size="sm" onClick={() => setOpen(true)} disabled={!recommendation}>
            <Plug aria-hidden="true" className="size-3.5" />
            Set up
          </Button>
        )
      }
    >
      {error ? (
        <Notice tone="error">{error}</Notice>
      ) : scanning ? (
        <>
          <p className={scanning.last_status === 'unhealthy' ? 'text-warn' : 'text-fg'}>
            {scanning.last_message || 'Connected, not checked yet.'}
          </p>
          {scanning.last_checked_at ? (
            <p className="mt-0.5 text-faint">checked {relativeAge(scanning.last_checked_at)}</p>
          ) : null}
        </>
      ) : (
        <p className="text-muted">
          Read what a scanner already running in this cluster found in its images. kubemg scans
          nothing itself, and only this cluster&rsquo;s administrators see the findings.
        </p>
      )}
    </IntegrationTile>
  )

  const sheet =
    open && recommendation ? (
      <VulnScanningSheet
        cluster={cluster}
        scanning={scanning}
        recommendation={recommendation}
        onClose={() => setOpen(false)}
        onScanning={setScanning}
        onRecommendation={setRecommendation}
        reload={load}
      />
    ) : null

  if (bare) {
    return (
      <IntegrationGroup
        title="Security"
        description="Vulnerability alerts read from the scanner in this cluster - kubemg connects to it rather than scanning."
      >
        {tile}
        {sheet}
      </IntegrationGroup>
    )
  }
  return (
    <>
      {tile}
      {sheet}
    </>
  )
}

function VulnScanningSheet({
  cluster,
  scanning,
  recommendation,
  onClose,
  onScanning,
  onRecommendation,
  reload,
}: {
  cluster: Cluster
  scanning: VulnScanning | null
  recommendation: VulnRecommendation
  onClose: () => void
  onScanning: (next: VulnScanning | null) => void
  onRecommendation: (next: VulnRecommendation) => void
  reload: () => Promise<void>
}) {
  const [detection, setDetection] = useState<VulnDetection | null>(null)
  const [detecting, setDetecting] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [installing, setInstalling] = useState<string[] | null>(null)

  const detect = useCallback(async () => {
    setDetecting(true)
    try {
      setDetection(await detectVulnScanning(cluster.id))
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not look at the cluster.'))
    } finally {
      setDetecting(false)
    }
  }, [cluster.id])

  useEffect(() => {
    void detect()
  }, [detect])

  async function run(action: () => Promise<void>, fallback: string) {
    setBusy(true)
    setError(null)
    try {
      await action()
    } catch (err) {
      setError(errorMessage(err, fallback))
    } finally {
      setBusy(false)
    }
  }

  const refreshRecommendation = async () => {
    const result = await fetchVulnScanning(cluster.id)
    onRecommendation(result.recommendation)
  }

  const step = vulnNextStep(scanning, detection, recommendation)
  const catalogue = recommendation.catalogue

  return (
    <>
      <Sheet
        width="lg"
        eyebrow={cluster.name}
        title="Vulnerability alerts"
        onClose={onClose}
        footer={<Button onClick={onClose}>Close</Button>}
      >
        <p className="text-[13px] leading-relaxed text-muted">
          kubemg does not scan images. A scanner running in this cluster pulls them, matches them
          against its vulnerability database and writes what it found as resources in the
          cluster; kubemg reads those, as itself, through the agent. Only this cluster&rsquo;s
          administrators see the findings.
        </p>

        {error ? <Notice tone="error">{error}</Notice> : null}

        <section className="flex flex-col gap-3">
          <div className="flex items-center justify-between gap-3">
            <h3 className="text-[14px] font-semibold text-fg">What is in the cluster</h3>
            <Button size="sm" variant="ghost" onClick={() => void detect()} disabled={detecting}>
              <RefreshCw aria-hidden="true" className={`size-3.5 ${detecting ? 'animate-spin' : ''}`} />
              Look again
            </Button>
          </div>
          {detecting && !detection ? (
            <p className="flex items-center gap-2 text-[13px] text-muted">
              <Spinner className="size-3.5" /> Looking at the cluster…
            </p>
          ) : detection ? (
            <>
              <DetailList
                columns={2}
                rows={[
                  {
                    term: 'Trivy Operator',
                    value: (() => {
                      const state = operatorState(detection)
                      const where = detection.operator
                        ? ` · ${detection.operator.namespace}/${detection.operator.name}${
                            detection.operator.version ? ` ${detection.operator.version}` : ''
                          }`
                        : ''
                      return `${state.text}${where}`
                    })(),
                    tone: operatorState(detection).tone === 'bad' ? 'bad' : undefined,
                  },
                  ...detection.reports.map((kind) => {
                    const label = reportKindLabel(kind)
                    return {
                      term: kind.label,
                      value: kind.reason ? `${label.text}: ${kind.reason}` : label.text,
                      tone: label.tone === 'bad' ? ('bad' as const) : undefined,
                    }
                  }),
                ]}
              />
              <Notice tone={detection.status === 'healthy' ? 'ok' : detection.installed ? 'warn' : 'info'}>
                {detection.message}
              </Notice>
            </>
          ) : null}
        </section>

        {detection || scanning ? (
          <section className="flex flex-col gap-3 border-t border-line-soft pt-4">
            {step === 'connected' && scanning ? (
              <>
                <div className="flex items-center justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="text-[14px] font-semibold text-fg">Alerts on</h3>
                    <p className="text-[12.5px] text-muted">
                      Off keeps the connection and stops reading the reports.
                    </p>
                  </div>
                  <Switch
                    label="Read this cluster's vulnerability reports"
                    checked={scanning.enabled}
                    disabled={busy}
                    onChange={(enabled) =>
                      void run(async () => {
                        const result = await putVulnScanning(cluster.id, { provider: scanning.provider, enabled })
                        onScanning(result.scanning)
                        setDetection(result.detection)
                      }, 'The change was not saved.')
                    }
                  />
                </div>
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    size="sm"
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        const result = await checkVulnScanning(cluster.id)
                        onScanning(result.scanning)
                        setDetection(result.detection)
                      }, 'The check did not finish.')
                    }
                  >
                    <RefreshCw aria-hidden="true" className="size-3.5" />
                    Check and record
                  </Button>
                  <Button
                    size="sm"
                    variant="danger"
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        await deleteVulnScanning(cluster.id)
                        onScanning(null)
                      }, 'Could not disconnect.')
                    }
                  >
                    Disconnect
                  </Button>
                </div>
                <p className="text-[12px] text-faint">
                  Disconnecting leaves Trivy Operator installed - it is a Helm release in this
                  cluster, removed from Explore if you want it gone.
                </p>
              </>
            ) : step === 'connect' ? (
              <>
                <h3 className="text-[14px] font-semibold text-fg">Connect</h3>
                <p className="text-[13px] text-muted">
                  Trivy Operator&rsquo;s reports are served here. Connecting turns the alerts on for
                  this cluster; kubemg checks again before it saves.
                </p>
                <div>
                  <Button
                    variant="primary"
                    disabled={busy}
                    onClick={() =>
                      void run(async () => {
                        const result = await putVulnScanning(cluster.id, {
                          provider: recommendation.provider,
                          enabled: true,
                        })
                        onScanning(result.scanning)
                        setDetection(result.detection)
                      }, 'Could not connect.')
                    }
                  >
                    {busy ? <Spinner className="size-3.5" /> : <Plug aria-hidden="true" className="size-3.5" />}
                    Connect
                  </Button>
                </div>
              </>
            ) : (
              <>
                <div className="flex items-start justify-between gap-3">
                  <div className="min-w-0">
                    <h3 className="text-[14px] font-semibold text-fg">
                      Recommended: {recommendation.name}
                    </h3>
                    <p className="text-[12.5px] text-muted">
                      Open source ({recommendation.license}), by Aqua Security. Installed from the
                      chart catalogue into <span className="font-data">{recommendation.namespace}</span>.
                    </p>
                  </div>
                  <a
                    href={recommendation.docs_url}
                    target="_blank"
                    rel="noreferrer noopener"
                    className="inline-flex shrink-0 items-center gap-1.5 text-[12.5px] font-medium text-accent hover:text-accent-hover"
                  >
                    Its documentation
                    <ExternalLink aria-hidden="true" className="size-3.5" />
                  </a>
                </div>
                <ul className="flex list-disc flex-col gap-1 pl-5 text-[12.5px] leading-relaxed text-muted">
                  <li>
                    Its scan jobs run in this cluster and pull every image it runs, on this
                    cluster&rsquo;s CPU, memory and registry bandwidth.
                  </li>
                  <li>
                    It downloads its vulnerability database from a public registry. An air-gapped
                    cluster needs a mirror set in the chart&rsquo;s values before it can scan.
                  </li>
                </ul>

                {step === 'add-repository' ? (
                  <div className="flex flex-col gap-2">
                    <p className="text-[13px] text-muted">
                      The chart&rsquo;s repository is not in the catalogue yet:{' '}
                      <span className="font-data text-fg">{recommendation.repository_url}</span>
                    </p>
                    <div>
                      <Button
                        disabled={busy}
                        onClick={() =>
                          void run(async () => {
                            await putHelmRepository(recommendation.repository_name, {
                              url: recommendation.repository_url,
                              description: `${recommendation.name} - the scanner kubemg's vulnerability alerts read.`,
                              username: '',
                            })
                            await syncHelmRepository(recommendation.repository_name)
                            await refreshRecommendation()
                          }, 'The repository could not be added.')
                        }
                      >
                        {busy ? <Spinner className="size-3.5" /> : null}
                        Add the {recommendation.repository_name} repository
                      </Button>
                    </div>
                  </div>
                ) : step === 'sync-repository' && catalogue ? (
                  <div className="flex flex-col gap-2">
                    <p className="text-[13px] text-muted">
                      The <span className="font-data text-fg">{catalogue.repository}</span> repository
                      has no {recommendation.chart} chart recorded yet
                      {catalogue.status === 'error' ? ' - its last sync failed' : ''}.
                    </p>
                    <div>
                      <Button
                        disabled={busy}
                        onClick={() =>
                          void run(async () => {
                            await syncHelmRepository(catalogue.repository)
                            await refreshRecommendation()
                          }, 'The repository did not sync.')
                        }
                      >
                        {busy ? <Spinner className="size-3.5" /> : <RefreshCw aria-hidden="true" className="size-3.5" />}
                        Sync it now
                      </Button>
                    </div>
                  </div>
                ) : step === 'install' && catalogue ? (
                  <div>
                    <Button
                      variant="primary"
                      disabled={busy}
                      onClick={() =>
                        void run(async () => {
                          const { namespaces } = await fetchNamespaces(cluster.id)
                          setInstalling(namespaces.map((entry) => entry.name))
                        }, 'Could not read this cluster’s namespaces.')
                      }
                    >
                      Install {recommendation.name} {catalogue.chart_version}
                    </Button>
                  </div>
                ) : null}
              </>
            )}
          </section>
        ) : null}
      </Sheet>

      {installing && catalogue?.chart_version ? (
        <HelmInstallSheet
          cluster={cluster}
          namespace=""
          namespaces={installing}
          preset={{
            repository: catalogue.repository,
            chart: recommendation.chart,
            version: catalogue.chart_version,
            name: recommendation.release,
            namespace: recommendation.namespace,
          }}
          onClose={() => {
            setInstalling(null)
            void detect()
            void reload()
          }}
          onInstalled={() => detect()}
        />
      ) : null}
    </>
  )
}

import { useCallback, useEffect, useRef, useState } from 'react'
import type { ReactNode } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import {
  AlertTriangle,
  Boxes,
  CalendarClock,
  ChevronRight,
  KeyRound,
  Layers,
  PackageOpen,
  RefreshCw,
  RotateCcwKey,
  ScrollText,
  Server,
  Timer,
  Waypoints,
} from 'lucide-react'
import {
  checkCluster,
  errorMessage,
  fetchCluster,
  fetchNodeMetrics,
  rotateAgentToken,
} from '../api/client'
import type { AgentInstall, Cluster, Environment, NodeMetrics } from '../api/types'
import { AgentInstallSheet } from '../components/AgentInstallSheet'
import { AppShell } from '../components/AppShell'
import { ClusterWorkloadSummary } from '../components/ClusterWorkloadSummary'
import { ConnectionChain } from '../components/ConnectionChain'
import { ConsolesPanel } from '../components/ConsolesPanel'
import { CrdVisibilityPanel } from '../components/CrdVisibilityPanel'
import { DatasourcePanel } from '../components/DatasourcePanel'
import { MetricComparison } from '../components/MetricComparison'
import type { ComparisonKind } from '../components/MetricComparison'
import { MetricsChart } from '../components/MetricsChart'
import { JitRequestModal } from '../components/jit/JitRequestModal'
import { KubeconfigDrawer } from '../components/KubeconfigDrawer'
import {
  Age,
  Button,
  ClusterState,
  Disclosure,
  Meter,
  Notice,
  Panel,
  StatTile,
} from '../components/primitives'
import { CardSkeleton, MeterGridSkeleton } from '../components/SkeletonLoader'
import { useDisclosureState } from '../lib/disclosures'
import { isBehind, newestAgentVersion } from '../lib/fleet'
import { useLiveTick } from '../lib/live'
import { DEFAULT_RESOURCE, resourceHref } from '../lib/navigation'
import { queryKey, useCachedQuery } from '../lib/query'
import { formatInstant } from '../lib/time'
import { LINK_LABEL, linkState } from '../lib/status'
import { formatCPU, formatMemory } from '../lib/units'
import { useAuth } from '../state/auth-context'
import { useClusters } from '../state/clusters-context'
import { useConfirm } from '../state/confirm-context'

/*
 * What a cluster page ranks. Two readings of what it costs, three of what is
 * going wrong — and nothing else, because the pattern this is drawn from also
 * carries response time, throughput and error rate, which come from APM agent
 * instrumentation KubeMG does not collect. A column that would always read "no
 * data" is worse than one that is not there.
 *
 * CPU and memory break down per namespace rather than per pod: the top five pods
 * out of several thousand is a list of five strangers, while the top five
 * namespaces is the vocabulary the fleet is already organised by. The failure
 * readings stay per pod, because a pod is the thing that restarts.
 */
const CLUSTER_READINGS: ComparisonKind[] = [
  { kind: 'cluster_cpu_by_namespace', label: 'CPU' },
  { kind: 'cluster_memory_by_namespace', label: 'Memory' },
  { kind: 'pod_restarts', label: 'Restarts' },
  { kind: 'containers_not_ready', label: 'Not ready' },
  { kind: 'cpu_throttling', label: 'Throttled' },
]

export function ClusterSummary() {
  const { id } = useParams<{ id: string }>()
  const { user } = useAuth()
  const navigate = useNavigate()
  // A health check answers with the cluster as it now is, and that answer is
  // newer than anything cached — so it wins until the page moves to another
  // cluster.
  const [checked, setChecked] = useState<Cluster | null>(null)
  const [checkError, setCheckError] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const [drawerOpen, setDrawerOpen] = useState(false)
  // Re-reading the install package is not registering anything, so it is an
  // action on this cluster rather than a walk back into the wizard.
  const [installOpen, setInstallOpen] = useState(false)
  // A rotation answers with the package the agent now needs; the sheet shows
  // that one rather than fetching another.
  const [rotated, setRotated] = useState<AgentInstall | null>(null)
  const [rotating, setRotating] = useState(false)
  const confirm = useConfirm()
  // Asking for more access belongs on the cluster it is about: this page is where
  // somebody has just read what their grant is and found it is not enough.
  const [requesting, setRequesting] = useState(false)
  const [requested, setRequested] = useState(false)

  const clusterId = Number(id)
  const valid = Number.isFinite(clusterId)

  // Read through the query cache: coming back here from Explore or the fleet
  // list inside the window draws the cluster immediately instead of spending a
  // round trip on facts that have not moved.
  const query = useCachedQuery<Cluster>(
    valid ? queryKey('cluster', clusterId) : null,
    () => fetchCluster(clusterId),
    // A cluster registered a minute ago lands on this page and says its agent
    // has not dialled in yet. Re-reading is what turns that into the connected
    // state without a reload — the wizard's own handshake step waits the same
    // way, and this is where an operator ends up afterwards.
    { live: true },
  )

  useEffect(() => {
    setChecked(null)
    setCheckError(null)
  }, [clusterId])

  const cluster = checked ?? query.data
  const error = !valid
    ? 'That cluster id is not valid.'
    : (checkError ?? (query.error ? errorMessage(query.error, 'Could not load this cluster.') : null))

  async function check() {
    setChecking(true)
    try {
      setChecked(await checkCluster(clusterId))
      setCheckError(null)
    } catch (err) {
      setCheckError(errorMessage(err, 'Could not check this cluster.'))
    } finally {
      setChecking(false)
    }
  }

  /*
   * Rotating the tunnel credential. There is no grace window — the point of a
   * rotation is that the old token stops working — so the agent goes down the
   * moment this answers and stays down until the new package is applied. That
   * is said before the click, not discovered after it.
   */
  async function rotate() {
    if (!cluster) return
    const ok = await confirm({
      eyebrow: cluster.name,
      title: 'Rotate agent token',
      body: (
        <>
          A new registration token replaces the current one. The agent attached to {cluster.name}{' '}
          is disconnected now and the old token is refused from then on — every console session,
          kubectl call and shell on this cluster stops until the new install package is applied.
          Any install URL already handed out stops working too.
        </>
      ),
      confirmLabel: 'Rotate',
      tone: 'danger',
    })
    if (!ok) return
    setRotating(true)
    try {
      const result = await rotateAgentToken(cluster.id)
      setRotated(result.install)
      setInstallOpen(true)
      setCheckError(null)
    } catch (err) {
      setCheckError(errorMessage(err, 'Could not rotate the agent token.'))
    } finally {
      setRotating(false)
    }
  }

  const viaAgent = cluster?.connection_mode === 'agent'
  const username = user?.username ?? 'you'
  /*
   * Which dashboard this is.
   *
   * The two views answer to two jobs rather than to two privilege levels — but
   * the privilege is what says which job somebody is here to do. An
   * administrator is the person who registered this cluster and who acts on its
   * connection, its version and its capacity; everybody else is here to find out
   * whether what they deployed is running. So the role picks the body, and the
   * shell around it — the actions, the kubeconfig, the access request — is the
   * same for both, because those are things either of them came to do.
   *
   * It reads the coarse role rather than `system_role`: a super admin is an
   * administrator here, and this is the same reading every other admin-gated
   * surface in the console takes.
   */
  const admin = user?.role === 'admin'

  /*
   * What can be done to this cluster, drawn on the cluster's own slab rather
   * than in the header.
   *
   * The header is the console's chrome — where you are, the ⌘K jump, the shell,
   * the time window — and it is shared by every page. Four page-specific buttons
   * in it turned this one into a toolbar with a breadcrumb in it, and pushed the
   * time range and the shell out to the edge of a crowded row. These are all acts
   * *on this cluster*, and the slab is where its name, its state and "last probe
   * 2h ago" already are, so that is where they belong: beside the thing they act
   * on rather than above the page that happens to show it.
   */
  const actions = cluster ? (
    <>
      {/* The tree beside this page reaches every kind; this is the one the
          dashboard is a preamble to, named after what it opens. */}
      {viaAgent && cluster.agent_attached ? (
        <Button
          variant="primary"
          onClick={() => navigate(resourceHref(cluster.id, DEFAULT_RESOURCE))}
          className={SLAB_BUTTON}
        >
          <Layers aria-hidden="true" className="size-4" />
          Pods
        </Button>
      ) : null}
      <Button
        variant={viaAgent && cluster.agent_attached ? 'slab' : 'primary'}
        onClick={() => setDrawerOpen(true)}
        className={SLAB_BUTTON}
      >
        <KeyRound aria-hidden="true" className="size-4" />
        Generate kubeconfig
      </Button>
      <Button variant="slab" onClick={() => setRequesting(true)} className={SLAB_BUTTON}>
        <Timer aria-hidden="true" className="size-4" />
        Request access
      </Button>
      {admin && viaAgent ? (
        /* An agent is upgraded by applying its manifests again, and the wizard
           that first showed them cannot be walked back into. Offered whether or
           not the agent is attached: a tunnel that is down is exactly when
           somebody needs the command again. */
        <Button variant="slab" onClick={() => setInstallOpen(true)} className={SLAB_BUTTON}>
          <PackageOpen aria-hidden="true" className="size-4" />
          Agent install
        </Button>
      ) : null}
      {admin && viaAgent ? (
        <Button variant="slab" onClick={rotate} disabled={rotating} className={SLAB_BUTTON}>
          <RotateCcwKey aria-hidden="true" className="size-4" />
          {rotating ? 'Rotating…' : 'Rotate agent token'}
        </Button>
      ) : null}
      {admin ? (
        <Button variant="slab" onClick={check} disabled={checking} className={SLAB_BUTTON}>
          <RefreshCw aria-hidden="true" className={`size-4 ${checking ? 'animate-spin' : ''}`} />
          {checking ? 'Checking…' : 'Run check'}
        </Button>
      ) : null}
    </>
  ) : null

  return (
    // The cluster's name is the switcher beside this, so the heading names the
    // view instead — the way every other cluster page's does.
    <AppShell
      title="Dashboard"
      timeRange
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {/* Where the request went, and where the answer will appear. Without this
            the form closes and nothing visibly happened. */}
        {requested ? (
          <Notice tone="ok">
            Request submitted. It is waiting for an approver — follow it on{' '}
            <Link to="/me/access" className="text-accent hover:underline">
              access requests
            </Link>
            , where an approved elevation shows its countdown.
          </Notice>
        ) : null}
        {/* The card that is coming, at the size it will be: this page opens with
            a header, the path row and a four-row detail list, and drawing that shape
            keeps the whole page from shifting when the cluster arrives. */}
        {query.loading ? <CardSkeleton lines={4} label="Loading this cluster" /> : null}

        {cluster ? (
          <>
            {/* The path a call actually takes, said once at the top of the page
                it belongs to — the same drawing the fleet page opens on, so the
                two read as one product rather than two. Everything below this
                is quiet on purpose: a cluster's name, its environment and its
                last probe are still worth knowing, but none of them competes
                with this row for the eye. */}
            <ClusterSlab cluster={cluster} admin={admin} actions={actions} />
            <ConnectionChain cluster={cluster} username={username} />

            {admin ? (
              <AdminDashboard cluster={cluster} username={username} />
            ) : (
              <WorkloadDashboard cluster={cluster} username={username} />
            )}
          </>
        ) : null}
      </div>

      {drawerOpen && cluster ? (
        <KubeconfigDrawer cluster={cluster} onClose={() => setDrawerOpen(false)} />
      ) : null}

      {installOpen && cluster ? (
        <AgentInstallSheet
          cluster={cluster}
          rotated={rotated ?? undefined}
          onClose={() => {
            setInstallOpen(false)
            setRotated(null)
          }}
        />
      ) : null}

      {requesting && cluster ? (
        <JitRequestModal
          cluster={cluster}
          onClose={() => setRequesting(false)}
          onCreated={() => {
            setRequesting(false)
            setRequested(true)
          }}
        />
      ) : null}
    </AppShell>
  )
}

/* A button on the slab: the template's pill shape, a touch taller. */
const SLAB_BUTTON = 'h-10 rounded-full px-4'

/* The environment on the dark plate, in the slab's own state tones: the
   page's EnvironmentTag is text on nothing, and its light-deck rust and amber
   are taken dark for a white page, not for this one. */
const SLAB_ENVIRONMENT: Record<Environment, string> = {
  prod: 'border-slab-danger/50 text-slab-danger',
  staging: 'border-slab-warn/50 text-slab-warn',
  dev: 'border-slab-muted/50 text-slab-muted',
}

/**
 * The cluster's masthead: its name, its environment, whether it can be reached
 * and how old that reading is, what it is for, and what can be done to it.
 * Everything below it is a reading of the cluster; this is the cluster.
 */
function ClusterSlab({
  cluster,
  admin,
  actions,
}: {
  cluster: Cluster
  admin: boolean
  actions: ReactNode
}) {
  return (
    <section aria-labelledby="cluster-slab-name" className="slab rounded-card px-5 py-6 sm:px-7">
      <p className="text-[12.5px] font-semibold text-slab-muted">
        {cluster.connection_mode === 'agent' ? 'Cluster · agent tunnel' : 'Cluster · direct'}
      </p>
      <h2
        id="cluster-slab-name"
        className="mt-1 truncate font-mono text-[24px] font-bold tracking-normal text-slab-text sm:text-[28px]"
        translate="no"
      >
        {cluster.name}
      </h2>
      <div className="mt-2.5 flex flex-wrap items-center gap-2 text-[13px] text-slab-muted">
        <span
          className={`inline-flex items-center rounded-chip border px-1.5 py-px font-mono text-[11px] tracking-wide uppercase ${SLAB_ENVIRONMENT[cluster.environment]}`}
        >
          {cluster.environment}
        </span>
        <ClusterState cluster={cluster} />
        <span title={formatInstant(cluster.last_checked_at, { seconds: true })}>
          last probe <Age iso={cluster.last_checked_at} />
        </span>
        {admin && cluster.api_url ? (
          <span className="min-w-0 truncate font-mono text-[12.5px]" translate="no">
            · {cluster.api_url}
          </span>
        ) : null}
      </div>
      {cluster.description ? (
        <p className="mt-3 max-w-2xl text-[14px] leading-relaxed text-slab-muted">
          {cluster.description}
        </p>
      ) : null}
      {actions ? <div className="mt-5 flex flex-wrap items-center gap-2">{actions}</div> : null}
    </section>
  )
}

/**
 * The administrator's dashboard: this cluster as an installation.
 *
 * Everything here is a fact about the *connection* — the path traffic takes,
 * which API server it is, what its agent runs, what its nodes are consuming,
 * where its history is stored — and every one of them is something an
 * administrator can act on. That is exactly why it is not what a developer is
 * shown: see WorkloadDashboard below.
 */
function AdminDashboard({ cluster, username }: { cluster: Cluster; username: string }) {
  const { user } = useAuth()
  const { clusters } = useClusters()
  const viaAgent = cluster.connection_mode === 'agent'
  const link = linkState(cluster)
  // Drift is measured against the newest agent the fleet runs, never a
  // hard-coded release — the same reading the fleet's own "agents behind" takes.
  const newest = newestAgentVersion(clusters)
  const behind =
    viaAgent && cluster.agent_version && newest ? isBehind(cluster.agent_version, newest) : false
  const [adminExplainerOpen, setAdminExplainerOpen] = useDisclosureState(
    'cluster-summary.admin.connection',
    user?.id ?? null,
  )

  return (
    <>
      {/* The installation's facts, one tile each. Only the two that are a
          state — the link and the agent's version against the fleet — take a
          colour, and only when they are asking for something. */}
      <section aria-label="This installation" className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile
          icon={Waypoints}
          label="Link"
          mono={false}
          value={LINK_LABEL[link]}
          sub={viaAgent ? 'outbound agent tunnel' : 'kubemg dials the API server'}
          tone={link === 'live' ? 'ok' : link === 'down' ? 'danger' : 'neutral'}
        />
        <StatTile
          icon={Boxes}
          label="Kubernetes"
          value={cluster.kubernetes_version ?? 'unknown'}
          dim={!cluster.kubernetes_version}
        />
        {viaAgent ? (
          <StatTile
            icon={Server}
            label="Agent"
            value={cluster.agent_version ?? 'not seen yet'}
            dim={!cluster.agent_version}
            sub={behind && newest ? `behind ${newest}` : undefined}
            tone={behind ? 'warn' : 'neutral'}
          />
        ) : (
          <StatTile icon={Server} label="Connection" mono={false} value="Direct API access" />
        )}
        <StatTile
          icon={CalendarClock}
          label="Registered"
          value={<Age iso={cluster.created_at} />}
          title={formatInstant(cluster.created_at)}
        />
      </section>

      {cluster.status === 'unhealthy' && cluster.status_message ? (
        <Notice tone="error">{cluster.status_message}</Notice>
      ) : null}

      {/* Capacity only exists for a cluster KubeMG can actually read
          through, which is the agent path. */}
      {viaAgent ? <Capacity cluster={cluster} /> : null}

      {/* Capacity above is a live sample and nothing more; this is where
          the history behind it comes from, wired per cluster. */}
      <DatasourcePanel cluster={cluster} />

      {/* And where the questions this console does not answer are
          answered. It sits under the datasource because that is what most
          of it is derived from — and it is a link rather than an embed on
          purpose: KubeMG stores no session for another tool. */}
      <ConsolesPanel cluster={cluster} />

      {/* And what everybody browsing this cluster is offered when they get
          there. It only exists for a cluster KubeMG can read the CRDs of,
          which is the agent path — and it curates the navigation, never the
          access: the cluster's own RBAC still answers every read. */}
      {viaAgent ? <CrdVisibilityPanel cluster={cluster} /> : null}

      {/* And this is that history, once there is somewhere to read it
          from. It sits directly under the datasource that answers it, so
          a chart that says "no datasource" is next to the form that fixes
          that rather than on some other page. */}
      {viaAgent ? (
        <section className="flex flex-col gap-3">
          {/* CPU and memory are read together — a spike in one is only worth
              anything beside the other at the same instant — so where there is
              room they sit side by side rather than a screen apart. Below xl
              they stack: half of a narrow column is not a chart. */}
          <div className="grid gap-3 xl:grid-cols-2">
            <MetricsChart
              cluster={cluster}
              title="Cluster CPU"
              metric="cluster_cpu"
            />
            <MetricsChart
              cluster={cluster}
              title="Cluster memory"
              metric="cluster_memory"
            />
          </div>
          {/* The charts say what shape the cluster is in. This says what
              is worst inside it and whether that is new, which is the
              question somebody opening a cluster page arrived with — and
              it is a table because reading a rank off a chart with forty
              lines is not reading. */}
          <MetricComparison cluster={cluster} kinds={CLUSTER_READINGS} />
        </section>
      ) : null}

      <AccessPath cluster={cluster} username={username} />

      {/* Why the connection works the way it does — folded behind one line
          rather than left open ahead of the charts, and remembered per
          person from here on. Every word survives; see Disclosure. */}
      {viaAgent ? (
        <Disclosure
          open={adminExplainerOpen}
          onOpenChange={setAdminExplainerOpen}
          summary="How this cluster is reached"
        >
          <div className="flex flex-col gap-3">
            <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
              An agent inside this cluster holds an outbound tunnel to kubemg, and every proxied
              call is replayed under your own identity using Kubernetes impersonation. The
              cluster&rsquo;s own RBAC decides what that identity may do — the grant above decides
              which cluster and namespaces kubemg will carry you to. Every call is written to the
              audit trail.
            </p>
            {/* "The cluster decides" is only honest if the console can show
                what the cluster decided. This is that view. */}
            <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
              <Link
                to={`/clusters/${cluster.id}/explore/clusterroles`}
                className="text-accent hover:underline"
              >
                Read this cluster&rsquo;s own RBAC
              </Link>{' '}
              to see the Roles and bindings behind that, and to ask the cluster directly whether
              an identity may do something.
            </p>
            {/* Privileged containers, hostPath mounts and the rest are
                fields these same reads already carry — this is where
                they are turned into a ranked list rather than left for
                someone to notice by eye. */}
            <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
              <Link to={`/clusters/${cluster.id}/security`} className="text-accent hover:underline">
                Check this cluster&rsquo;s workload security posture
              </Link>{' '}
              for privileged containers, hostPath mounts, missing NetworkPolicies and the rest —
              read from these same manifests, not a scan of your images.
            </p>
          </div>
        </Disclosure>
      ) : (
        <Disclosure
          open={adminExplainerOpen}
          onOpenChange={setAdminExplainerOpen}
          summary="What a kubeconfig for this cluster does"
        >
          <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
            kubemg issues a short-lived token for this cluster&rsquo;s kubemg service account
            through the Kubernetes TokenRequest API. It creates no RoleBinding, so the grant
            above decides what you see in kubemg — not what the cluster lets you do. Register
            the cluster in agent mode to have kubemg bind these roles for real.
          </p>
        </Disclosure>
      )}
    </>
  )
}

/**
 * What a developer's dashboard is instead: what is running, what is wrong, and
 * how much of the namespace it is using. The identity card above it keeps only
 * the facts that decide what they can do — the version they are talking to and
 * the grant they hold — and the connection's own mechanics are left to the
 * administrator's view rather than repeated here as prose nobody can act on.
 */
function WorkloadDashboard({ cluster, username }: { cluster: Cluster; username: string }) {
  const { user } = useAuth()
  const viaAgent = cluster.connection_mode === 'agent'
  const [kubeconfigExplainerOpen, setKubeconfigExplainerOpen] = useDisclosureState(
    'cluster-summary.developer.kubeconfig',
    user?.id ?? null,
  )

  return (
    <>
      {/* The facts that decide what this person can do here, one tile each.
          A kubeconfig that is not proxied is the one gap, so it is the one
          tile that takes a colour. */}
      <section aria-label="Your access" className="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
        <StatTile icon={KeyRound} label="Your role" value={cluster.k8s_role} />
        <StatTile
          icon={Layers}
          label="Namespaces"
          value={cluster.namespaces.length > 0 ? cluster.namespaces.length : 'All'}
          sub={cluster.namespaces.length > 0 ? cluster.namespaces.join(', ') : 'every namespace'}
          title={cluster.namespaces.length > 0 ? cluster.namespaces.join(', ') : undefined}
        />
        <StatTile
          icon={Boxes}
          label="Kubernetes"
          value={cluster.kubernetes_version ?? 'unknown'}
          dim={!cluster.kubernetes_version}
        />
        <StatTile
          icon={ScrollText}
          label="Every call"
          mono={false}
          value={viaAgent ? 'Proxied and audited' : 'Not proxied'}
          sub={viaAgent ? undefined : 'calls made with a kubeconfig'}
          tone={viaAgent ? 'neutral' : 'warn'}
        />
      </section>

      {cluster.status === 'unhealthy' && cluster.status_message ? (
        <Notice tone="error">{cluster.status_message}</Notice>
      ) : null}

      <ClusterWorkloadSummary cluster={cluster} />

      {/* The questions this console does not answer, where the cluster says
          they are answered. A link rather than an embed, as everywhere. */}
      <ConsolesPanel cluster={cluster} />

      {/* And where their own access stops, which is the one piece of the
          connection's story a developer does act on — it is what a request for
          more access is written against. */}
      <AccessPath cluster={cluster} username={username} />

      {/* The one piece of the administrator's prose that is kept here, because
          this page offers the kubeconfig and a direct-mode file does not mean
          what the chain above it appears to say. Folded and remembered per
          person, the same as everywhere else this shared primitive is used. */}
      {viaAgent ? null : (
        <Disclosure
          open={kubeconfigExplainerOpen}
          onOpenChange={setKubeconfigExplainerOpen}
          summary="What a kubeconfig for this cluster does"
        >
          <p className="max-w-3xl text-[13px] leading-relaxed text-muted">
            kubemg issues a short-lived token for this cluster&rsquo;s kubemg service account. It
            creates no RoleBinding, so the grant above decides what you see in kubemg — not what
            the cluster lets you do, and calls made with that file are not proxied or audited here.
          </p>
        </Disclosure>
      )}
    </>
  )
}

/**
 * Capacity is what the cluster is actually using, read from its own Metrics
 * API through the same audited tunnel as everything else. It leads with the
 * cluster total, because the first question is whether the cluster has room;
 * the per-node rows underneath answer the second one, which is whether that
 * room is where the work is.
 *
 * There is no chart here on purpose: metrics-server keeps a sliding window of
 * a couple of minutes, so there is no series to draw and pretending otherwise
 * would invent history the cluster does not have.
 */
function Capacity({ cluster }: { cluster: Cluster }) {
  const [metrics, setMetrics] = useState<NodeMetrics | null>(null)
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(true)

  // Which cluster the newest sample belongs to, so an answer that lands after
  // the page has moved on is dropped rather than drawn under another name.
  const active = useRef(cluster.id)

  const read = useCallback(
    async (quiet = false) => {
      const target = cluster.id
      active.current = target
      try {
        const next = await fetchNodeMetrics(target)
        if (active.current !== target) return
        setMetrics(next)
        setError(null)
      } catch (err) {
        if (active.current !== target) return
        // A background sample that fails leaves the last one on screen: this
        // panel is a reading, and taking the numbers away because one poll
        // missed says less than the numbers did.
        if (!quiet) setError(errorMessage(err, 'Could not read this cluster’s usage.'))
      } finally {
        if (active.current === target) setLoading(false)
      }
    },
    [cluster.id],
  )

  useEffect(() => {
    setLoading(true)
    void read()
  }, [read])

  // metrics-server samples every 15s or so; matching it keeps the panel live
  // without spending tunnel round trips on numbers that have not moved — and it
  // stops entirely behind a hidden tab, because a cluster should not be sampled
  // for a panel nobody is looking at.
  useLiveTick(useCallback(() => read(true), [read]))

  const summary = metrics?.summary

  return (
    <Panel
      title="Capacity"
      eyebrow="Live"
      description="Current consumption against allocatable capacity, read from the cluster's Metrics API."
      bodyClassName="flex flex-col gap-4 p-4"
    >
      {error ? <Notice tone="error">{error}</Notice> : null}
      {!error && metrics && !metrics.available ? (
        <Notice tone="info">{metrics.reason}</Notice>
      ) : null}
      {/* Two meters, at the height two meters occupy: the panel does not grow
          when the first sample lands. */}
      {loading && !metrics ? <MeterGridSkeleton count={2} /> : null}

      {metrics?.available && summary ? (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Meter
              label={`CPU across ${summary.nodes} ${summary.nodes === 1 ? 'node' : 'nodes'}`}
              value={formatCPU(summary.cpu_millicores)}
              percent={summary.cpu_percent}
              capacity={formatCPU(summary.cpu_capacity_millicores)}
            />
            <Meter
              label="Memory"
              value={formatMemory(summary.memory_bytes)}
              percent={summary.memory_percent}
              capacity={formatMemory(summary.memory_capacity_bytes)}
            />
          </div>

          <ul className="flex flex-col gap-3 border-t border-line-soft pt-4">
            {metrics.nodes.map((node) => (
              <li key={node.name} className="flex flex-col gap-2">
                <span className="truncate font-mono text-[13px] text-fg" title={node.name}>
                  {node.name}
                </span>
                <div className="grid gap-3 sm:grid-cols-2">
                  <Meter
                    label="CPU"
                    value={formatCPU(node.cpu_millicores)}
                    percent={node.cpu_percent}
                    capacity={formatCPU(node.cpu_capacity_millicores)}
                  />
                  <Meter
                    label="Memory"
                    value={formatMemory(node.memory_bytes)}
                    percent={node.memory_percent}
                    capacity={formatMemory(node.memory_capacity_bytes)}
                  />
                </div>
              </li>
            ))}
          </ul>

          {/* Consumption is half the story and the half that explains least: a
              node idle here can still be one the scheduler will not place
              anything on. The page that answers that is one hop away rather
              than folded in, because it is a different question. */}
          <p className="border-t border-line-soft pt-4 text-[12px] leading-relaxed text-muted">
            This is what the cluster is using.{' '}
            <Link to={`/clusters/${cluster.id}/capacity`} className="text-accent hover:underline">
              Capacity
            </Link>{' '}
            shows what has already been reserved, which is what decides whether anything more will
            schedule.
          </p>
        </>
      ) : null}
    </Panel>
  )
}

/**
 * AccessPath is the chain that decides what access to this cluster can do: who
 * you are, what KubeMG granted you, and where that grant stops. In direct mode
 * the last hop is amber on purpose — no RoleBinding is created in the cluster,
 * and the UI says so where the decision is made, not in a footnote.
 */
function AccessPath({ cluster, username }: { cluster: Cluster; username: string }) {
  // An agent cluster closes the chain: the installed manifests bind the
  // kubemg:* groups to real ClusterRoles, so the last hop is no longer a gap.
  const viaAgent = cluster.connection_mode === 'agent'

  const hops = [
    { label: 'Identity', value: username, gap: false },
    { label: 'Grant in kubemg', value: cluster.k8s_role, gap: false },
    {
      label: 'Namespaces',
      value: cluster.namespaces.length > 0 ? cluster.namespaces.join(', ') : 'all',
      gap: false,
    },
    viaAgent
      ? { label: 'Cluster RBAC', value: `kubemg:${cluster.k8s_role}`, gap: false }
      : { label: 'Cluster RBAC', value: 'no RoleBinding', gap: true },
  ]

  return (
    <Panel title="How your access is derived" eyebrow="Chain">
      <ol className="flex flex-col md:flex-row">
        {hops.map((hop, index) => (
          <li
            key={hop.label}
            className={`relative flex min-w-0 flex-1 flex-col gap-1 border-b border-line-soft px-5 pt-4 pb-3.5 last:border-b-0 md:border-r md:border-b-0 md:last:border-r-0 ${
              hop.gap ? 'bg-warn-soft' : ''
            }`}
          >
            <span className="label">{hop.label}</span>
            <span
              className={`flex items-center gap-1.5 truncate font-mono text-[13.5px] ${
                hop.gap ? 'text-warn' : 'text-fg'
              }`}
              title={hop.value}
            >
              {hop.gap ? <AlertTriangle aria-hidden="true" className="size-3.5 shrink-0" /> : null}
              {hop.value}
            </span>
            {index < hops.length - 1 ? (
              <ChevronRight
                aria-hidden="true"
                className="absolute top-1/2 right-0 hidden size-4 -translate-y-1/2 translate-x-1/2 rounded-full bg-surface text-faint md:block"
              />
            ) : null}
          </li>
        ))}
      </ol>
    </Panel>
  )
}

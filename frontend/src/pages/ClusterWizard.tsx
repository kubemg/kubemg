import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { useNavigate } from 'react-router'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  Download,
  Plug,
  RefreshCw,
  Server,
  Trash2,
} from 'lucide-react'
import {
  assignPermission,
  checkCluster,
  createCluster,
  errorMessage,
  fetchAgentInstall,
  fetchCluster,
  fetchGroups,
  fetchPermissions,
  fetchUsers,
  revokePermission,
} from '../api/client'
import type {
  AgentInstall,
  Cluster,
  ConnectionMode,
  Environment,
  Group,
  K8sRole,
  Permission,
  SubjectType,
  User,
} from '../api/types'
import { AppShell } from '../components/AppShell'
import { DatasourcePanel } from '../components/DatasourcePanel'
import { LinkStrand, StrandNode } from '../components/LinkStrand'
import {
  Button,
  ClusterState,
  CodeBlock,
  DetailList,
  EnvironmentTag,
  Field,
  IconButton,
  Notice,
  Panel,
  Pill,
  Select,
  TextArea,
  TextInput,
} from '../components/primitives'
import { YamlView } from '../components/YamlView'
import { useClusters } from '../state/clusters-context'

const ENVIRONMENTS: Environment[] = ['prod', 'staging', 'dev']
const K8S_ROLES: K8sRole[] = ['cluster-admin', 'edit', 'view']

/* Polling is the whole point of step three: the operator has just pasted a
   command into a terminal somewhere else, and this screen is how they learn it
   worked. Three seconds is fast enough to feel live without hammering the API. */
const POLL_INTERVAL_MS = 3000

const STEPS = ['Identity', 'Connection', 'Handshake', 'Observability', 'Access'] as const
type StepIndex = 0 | 1 | 2 | 3 | 4

const BLANK_IDENTITY = {
  name: '',
  environment: 'dev' as Environment,
  description: '',
}

const BLANK_DIRECT = {
  api_url: '',
  ca_cert_data: '',
  service_account_token: '',
}

export function ClusterWizard() {
  const navigate = useNavigate()
  const { reload } = useClusters()

  const [step, setStep] = useState<StepIndex>(0)
  const [identity, setIdentity] = useState(BLANK_IDENTITY)
  const [mode, setMode] = useState<ConnectionMode>('agent')
  const [direct, setDirect] = useState(BLANK_DIRECT)

  // Once the cluster exists the earlier steps are history: its name and mode
  // are fixed, and re-submitting them would only create a duplicate.
  const [cluster, setCluster] = useState<Cluster | null>(null)
  const [install, setInstall] = useState<AgentInstall | null>(null)

  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function register(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (cluster) {
      setStep(2)
      return
    }

    setBusy(true)
    setError(null)
    try {
      const created = await createCluster({
        name: identity.name.trim(),
        environment: identity.environment,
        description: identity.description.trim() || undefined,
        connection_mode: mode,
        ...(mode === 'direct'
          ? {
              api_url: direct.api_url.trim(),
              ca_cert_data: direct.ca_cert_data.trim(),
              service_account_token: direct.service_account_token.trim(),
            }
          : {}),
      })
      setCluster(created)

      if (mode === 'agent') {
        setInstall(await fetchAgentInstall(created.id))
      }
      await reload()
    } catch (err) {
      setError(errorMessage(err, 'Could not register the cluster.'))
    } finally {
      setBusy(false)
    }
  }

  const identityReady = identity.name.trim().length > 0

  return (
    <AppShell
      title="Register cluster"
      parent={{ label: 'Clusters', to: '/clusters' }}
      actions={
        cluster ? (
          <Button variant="primary" onClick={() => navigate(`/clusters/${cluster.id}`)}>
            Finish
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        ) : null
      }
    >
      <div className="flex min-w-0 max-w-4xl flex-col gap-5">
        <Stepper
          current={step}
          furthest={cluster ? STEPS.length - 1 : identityReady ? 1 : 0}
          onSelect={setStep}
        />

        {error ? <Notice tone="error">{error}</Notice> : null}

        {step === 0 ? (
          <IdentityStep
            value={identity}
            locked={cluster !== null}
            onChange={setIdentity}
            onNext={() => setStep(1)}
          />
        ) : null}

        {step === 1 ? (
          <ConnectionStep
            mode={mode}
            onModeChange={setMode}
            direct={direct}
            onDirectChange={setDirect}
            cluster={cluster}
            install={install}
            busy={busy}
            onSubmit={register}
            onBack={() => setStep(0)}
            onNext={() => setStep(2)}
          />
        ) : null}

        {step === 2 && cluster ? (
          <HandshakeStep
            cluster={cluster}
            install={install}
            onCluster={setCluster}
            onBack={() => setStep(1)}
            onNext={() => setStep(3)}
          />
        ) : null}

        {step === 3 && cluster ? (
          <ObservabilityStep
            cluster={cluster}
            onBack={() => setStep(2)}
            onNext={() => setStep(4)}
          />
        ) : null}

        {step === 4 && cluster ? (
          <AccessStep
            cluster={cluster}
            onBack={() => setStep(3)}
            onDone={() => navigate(`/clusters/${cluster.id}`)}
          />
        ) : null}

        {step > 1 && !cluster ? (
          <Notice tone="info">Register the cluster first — the remaining steps act on it.</Notice>
        ) : null}
      </div>
    </AppShell>
  )
}

/**
 * Stepper numbers the steps because registration really is a sequence: the
 * cluster is created on leaving step two, and steps three and four act on the
 * record. The strand between markers is the same device used everywhere else.
 */
function Stepper({
  current,
  furthest,
  onSelect,
}: {
  current: StepIndex
  /** The highest step reachable so far; anything past it is not yet meaningful. */
  furthest: number
  onSelect: (step: StepIndex) => void
}) {
  return (
    <ol className="flex items-center gap-1.5">
      {STEPS.map((label, index) => {
        const done = index < current
        const active = index === current
        const reachable = index <= furthest

        return (
          <li key={label} className="flex min-w-0 flex-1 items-center gap-1.5">
            <button
              type="button"
              disabled={!reachable}
              onClick={() => onSelect(index as StepIndex)}
              className={`flex min-w-0 items-center gap-2 rounded-control px-1.5 py-1 transition-colors ${
                reachable ? 'hover:bg-raised' : 'cursor-not-allowed opacity-45'
              }`}
            >
              <span
                className={`grid size-6 shrink-0 place-items-center rounded-full font-mono text-[11.5px] font-semibold ${
                  done
                    ? 'bg-ok-soft text-ok'
                    : active
                      ? 'bg-accent text-on-accent'
                      : 'bg-raised text-muted'
                }`}
              >
                {done ? <Check aria-hidden="true" className="size-3.5" /> : index + 1}
              </span>
              <span
                className={`hidden truncate text-[13px] sm:block ${
                  active ? 'font-medium text-fg' : 'text-muted'
                }`}
              >
                {label}
              </span>
            </button>
            {index < STEPS.length - 1 ? (
              <LinkStrand
                state={done ? 'direct' : 'idle'}
                size="sm"
                className="min-w-4 flex-1"
              />
            ) : null}
          </li>
        )
      })}
    </ol>
  )
}

/** StepActions is the consistent footer every step ends with. */
function StepActions({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center justify-end gap-2 border-t border-line-soft bg-raised/40 px-4 py-3">
      {children}
    </div>
  )
}

function IdentityStep({
  value,
  locked,
  onChange,
  onNext,
}: {
  value: typeof BLANK_IDENTITY
  /** Once the cluster is registered its identity is fixed. */
  locked: boolean
  onChange: (next: typeof BLANK_IDENTITY) => void
  onNext: () => void
}) {
  function update<K extends keyof typeof BLANK_IDENTITY>(
    key: K,
    next: (typeof BLANK_IDENTITY)[K],
  ) {
    onChange({ ...value, [key]: next })
  }

  return (
    <form
      onSubmit={(event) => {
        event.preventDefault()
        onNext()
      }}
    >
      <Panel eyebrow="Step 1" title="What is this cluster?">
        <div className="flex flex-col gap-4 p-4">
          <Field
            label="Name"
            htmlFor="name"
            hint="How the cluster is referred to everywhere in KubeMG, and in generated kubeconfig contexts."
          >
            <TextInput
              id="name"
              required
              autoFocus
              disabled={locked}
              placeholder="prod-eu"
              className="font-mono"
              value={value.name}
              onChange={(event) => update('name', event.target.value)}
            />
          </Field>

          <Field
            label="Environment"
            htmlFor="environment"
            hint="Drives how loudly the fleet flags it. Production reads as production everywhere."
          >
            <div className="flex flex-wrap gap-2">
              {ENVIRONMENTS.map((environment) => (
                <button
                  key={environment}
                  type="button"
                  disabled={locked}
                  onClick={() => update('environment', environment)}
                  aria-pressed={value.environment === environment}
                  className={`flex h-9 items-center gap-2 rounded-control border px-3 transition-colors disabled:cursor-not-allowed disabled:opacity-60 ${
                    value.environment === environment
                      ? 'border-accent-line bg-accent-soft'
                      : 'border-line bg-surface hover:bg-raised'
                  }`}
                >
                  <EnvironmentTag environment={environment} />
                </button>
              ))}
            </div>
          </Field>

          <Field
            label="Description"
            htmlFor="description"
            hint="Optional. What runs here, or who owns it."
          >
            <TextInput
              id="description"
              disabled={locked}
              placeholder="Customer-facing workloads, EU region"
              value={value.description}
              onChange={(event) => update('description', event.target.value)}
            />
          </Field>
        </div>

        <StepActions>
          <Button type="submit" variant="primary" disabled={value.name.trim().length === 0}>
            Continue
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </Panel>
    </form>
  )
}

function ConnectionStep({
  mode,
  onModeChange,
  direct,
  onDirectChange,
  cluster,
  install,
  busy,
  onSubmit,
  onBack,
  onNext,
}: {
  mode: ConnectionMode
  onModeChange: (mode: ConnectionMode) => void
  direct: typeof BLANK_DIRECT
  onDirectChange: (next: typeof BLANK_DIRECT) => void
  cluster: Cluster | null
  install: AgentInstall | null
  busy: boolean
  onSubmit: (event: FormEvent<HTMLFormElement>) => void
  onBack: () => void
  onNext: () => void
}) {
  function updateDirect<K extends keyof typeof BLANK_DIRECT>(
    key: K,
    next: (typeof BLANK_DIRECT)[K],
  ) {
    onDirectChange({ ...direct, [key]: next })
  }

  return (
    <form onSubmit={onSubmit} className="flex flex-col gap-5">
      <Panel eyebrow="Step 2" title="How should KubeMG reach it?">
        <div className="grid gap-3 p-4 sm:grid-cols-2">
          <ModeCard
            mode="agent"
            selected={mode === 'agent'}
            locked={cluster !== null}
            onSelect={onModeChange}
            icon={Plug}
            title="Agent-based"
            tagline="Recommended"
            strand="live"
            points={[
              'The cluster dials out to KubeMG — no inbound firewall rule, no exposed API server.',
              'KubeMG stores no credential for the cluster.',
              'kubectl traffic is proxied and audited, and acts under your own identity.',
            ]}
          />
          <ModeCard
            mode="direct"
            selected={mode === 'direct'}
            locked={cluster !== null}
            onSelect={onModeChange}
            icon={Server}
            title="Direct API access"
            tagline="Requires reachability"
            strand="direct"
            points={[
              'KubeMG dials the API server itself, so it must be routable from here.',
              'A service account token is stored in KubeMG.',
              'Issues kubeconfigs only — no proxy and no audit trail.',
            ]}
          />
        </div>

        {mode === 'direct' && !cluster ? (
          <div className="flex flex-col gap-4 border-t border-line-soft p-4">
            <Field label="API server URL" htmlFor="api_url">
              <TextInput
                id="api_url"
                type="url"
                required
                className="font-mono text-[12.5px]"
                placeholder="https://prod-eu.example.com:6443"
                value={direct.api_url}
                onChange={(event) => updateDirect('api_url', event.target.value)}
              />
            </Field>

            <Field
              label="CA certificate"
              htmlFor="ca_cert_data"
              hint="PEM or base64-encoded PEM. Leave empty if the API server uses a publicly trusted certificate."
            >
              <TextArea
                id="ca_cert_data"
                rows={4}
                placeholder="-----BEGIN CERTIFICATE-----"
                value={direct.ca_cert_data}
                onChange={(event) => updateDirect('ca_cert_data', event.target.value)}
              />
            </Field>

            <Field
              label="Service account token"
              htmlFor="service_account_token"
              hint="Needs permission to create service accounts and request tokens."
            >
              <TextArea
                id="service_account_token"
                rows={4}
                required
                value={direct.service_account_token}
                onChange={(event) => updateDirect('service_account_token', event.target.value)}
              />
            </Field>
          </div>
        ) : null}

        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          {cluster ? (
            <Button type="button" variant="primary" onClick={onNext}>
              Continue
              <ArrowRight aria-hidden="true" className="size-4" />
            </Button>
          ) : (
            <Button type="submit" variant="primary" disabled={busy}>
              {busy
                ? 'Registering…'
                : mode === 'agent'
                  ? 'Register & generate installer'
                  : 'Register cluster'}
            </Button>
          )}
        </StepActions>
      </Panel>

      {install ? <AgentInstaller install={install} /> : null}
    </form>
  )
}

function ModeCard({
  mode,
  selected,
  locked,
  onSelect,
  icon: Icon,
  title,
  tagline,
  strand,
  points,
}: {
  mode: ConnectionMode
  selected: boolean
  locked: boolean
  onSelect: (mode: ConnectionMode) => void
  icon: typeof Plug
  title: string
  tagline: string
  strand: 'live' | 'direct'
  points: string[]
}) {
  return (
    <button
      type="button"
      disabled={locked}
      onClick={() => onSelect(mode)}
      aria-pressed={selected}
      className={`flex flex-col gap-3 rounded-card border p-4 text-left transition-colors disabled:cursor-not-allowed ${
        selected
          ? 'border-accent-line bg-accent-soft/40'
          : 'border-line bg-surface hover:bg-raised disabled:opacity-50'
      }`}
    >
      <div className="flex items-center gap-2">
        <Icon
          aria-hidden="true"
          className={`size-4 shrink-0 ${selected ? 'text-accent' : 'text-muted'}`}
        />
        <span className="text-[14px] font-semibold text-fg">{title}</span>
        <span className="ml-auto">
          <Pill tone={selected ? 'accent' : 'idle'} dot={false}>
            {tagline}
          </Pill>
        </span>
      </div>

      <LinkStrand state={strand} />

      <ul className="flex flex-col gap-1.5">
        {points.map((point) => (
          <li key={point} className="text-[12.5px] leading-snug text-muted">
            {point}
          </li>
        ))}
      </ul>
    </button>
  )
}

/**
 * AgentInstaller is the handoff: one command to run somewhere else. The token is
 * shown separately and masked, because it is the cluster's only credential and it
 * is about to live in a Kubernetes Secret.
 */
function AgentInstaller({ install }: { install: AgentInstall }) {
  const [showManifest, setShowManifest] = useState(false)

  return (
    <Panel
      eyebrow="Handoff"
      title="Install the agent"
      description={`Run this against ${install.cluster} with a kubeconfig that can create resources in ${install.namespace}. The agent dials back out to KubeMG — nothing needs to be opened inbound.`}
      actions={
        <a
          href={install.manifest_url}
          download
          className="inline-flex h-9 items-center gap-2 rounded-control border border-line px-3 text-[13px] text-muted transition-colors hover:text-fg"
        >
          <Download aria-hidden="true" className="size-4" />
          Download YAML
        </a>
      }
      bodyClassName="flex flex-col gap-4 p-4"
    >
      <CodeBlock label="Install command" value={install.apply_command} />

      <details className="group">
        <summary className="cursor-pointer text-[12.5px] text-muted transition-colors hover:text-fg">
          Prefer Kustomize?
        </summary>
        <div className="mt-2.5 flex flex-col gap-2">
          <p className="text-[12px] leading-snug text-muted">
            Kustomize only accepts local paths and Git specs as remote targets, so the package is
            fetched and extracted first.
          </p>
          <CodeBlock value={install.kustomize_command} />
        </div>
      </details>

      <div className="border-t border-line-soft pt-4">
        <DetailList
          columns={2}
          rows={[
            { term: 'Bastion', value: install.bastion_url },
            { term: 'Namespace', value: install.namespace },
            { term: 'Image', value: install.image },
          ]}
        />
      </div>

      <CodeBlock label="Registration token" value={install.agent_token} secret />
      <p className="flex items-start gap-1.5 text-[12px] leading-snug text-warn">
        <AlertTriangle aria-hidden="true" className="mt-0.5 size-3.5 shrink-0" />
        This token authenticates the tunnel for this cluster. It is embedded in the command above —
        treat both like a credential.
      </p>

      <div>
        <button
          type="button"
          onClick={() => setShowManifest((current) => !current)}
          className="text-[12.5px] text-muted transition-colors hover:text-fg"
        >
          {showManifest ? 'Hide' : 'Review'} the manifest before applying
        </button>
        {/* Reviewing a manifest before applying it to a cluster is reading, so it
            gets the same painted surface the object editor does. */}
        {showManifest ? (
          <YamlView value={install.manifest} className="mt-2.5 h-80" />
        ) : null}
      </div>
    </Panel>
  )
}

/**
 * HandshakeStep watches the cluster come up. For an agent cluster that means
 * waiting for the tunnel to arrive; for a direct one it means probing the API
 * server. Either way the operator is looking at this screen while something
 * happens elsewhere, so it polls rather than making them press refresh.
 */
function HandshakeStep({
  cluster,
  install,
  onCluster,
  onBack,
  onNext,
}: {
  cluster: Cluster
  install: AgentInstall | null
  onCluster: (cluster: Cluster) => void
  onBack: () => void
  onNext: () => void
}) {
  const [error, setError] = useState<string | null>(null)
  const [checking, setChecking] = useState(false)
  const [polling, setPolling] = useState(cluster.connection_mode === 'agent')

  const viaAgent = cluster.connection_mode === 'agent'
  const connected = viaAgent ? cluster.agent_attached : cluster.status === 'healthy'

  // onCluster identity is stable enough in practice, but the poller reads it on
  // every tick — hold it in a ref so changing it never restarts the interval.
  const report = useRef(onCluster)
  report.current = onCluster

  const clusterId = cluster.id

  useEffect(() => {
    if (!polling) return

    let cancelled = false
    const timer = window.setInterval(async () => {
      try {
        const next = await fetchCluster(clusterId)
        if (!cancelled) report.current(next)
      } catch {
        // A transient failure while waiting is not worth interrupting the wait
        // for; the next tick will say the same thing if it is real.
      }
    }, POLL_INTERVAL_MS)

    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [clusterId, polling])

  // Stop polling the moment it lands, rather than keeping a timer alive behind a
  // screen that has nothing left to report.
  useEffect(() => {
    if (connected) setPolling(false)
  }, [connected])

  const runCheck = useCallback(async () => {
    setChecking(true)
    setError(null)
    try {
      report.current(await checkCluster(clusterId))
    } catch (err) {
      setError(errorMessage(err, 'Could not check this cluster.'))
    } finally {
      setChecking(false)
    }
  }, [clusterId])

  return (
    <Panel eyebrow="Step 3" title={connected ? 'Connected' : 'Waiting for the cluster'}>
      <div className="flex flex-col gap-4 p-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        {/* The wait is the strand filling in: nothing else on this screen says
            as directly whether the cluster has found us. */}
        <div className="flex flex-col gap-4 rounded-card border border-line-soft bg-raised/50 p-4 sm:flex-row sm:items-end">
          <StrandNode label="Cluster" value={cluster.name} tone={connected ? 'ok' : 'idle'} />
          <span className="min-w-16 flex-1 pb-2">
            <LinkStrand
              state={connected ? 'live' : viaAgent ? 'idle' : 'direct'}
              size="lg"
              className={connected ? '' : 'breathe'}
            />
            <span className="mt-1.5 block font-mono text-[11px] text-faint">
              {connected
                ? viaAgent
                  ? 'tunnel open'
                  : 'API server answered'
                : viaAgent
                  ? 'waiting for the agent to dial in'
                  : 'not probed yet'}
            </span>
          </span>
          <StrandNode label="KubeMG" value="bastion" tone="accent" />
          <span className="shrink-0 pb-2 sm:pb-0">
            <ClusterState cluster={cluster} />
          </span>
        </div>

        <p className="text-[13px] leading-relaxed text-muted">
          {connected
            ? viaAgent
              ? 'The agent is connected and the tunnel is open. Anything KubeMG does on this cluster now travels along it.'
              : 'The API server answered. KubeMG can issue kubeconfigs for this cluster.'
            : viaAgent
              ? 'No agent has dialled in yet. Run the install command against the cluster and this screen updates on its own.'
              : 'The cluster has not been probed yet. Run a check to confirm KubeMG can reach the API server.'}
        </p>

        {cluster.status_message ? (
          <Notice tone={connected ? 'info' : 'warn'}>{cluster.status_message}</Notice>
        ) : null}

        <DetailList
          columns={2}
          rows={[
            { term: 'Mode', value: cluster.connection_mode },
            { term: 'Kubernetes', value: cluster.kubernetes_version ?? 'unknown' },
            ...(viaAgent
              ? [{ term: 'Agent', value: cluster.agent_version ?? 'not seen yet' }]
              : []),
          ]}
        />

        {!connected && install ? (
          <CodeBlock label="Install command" value={install.apply_command} />
        ) : null}
      </div>

      <StepActions>
        <Button type="button" variant="ghost" onClick={onBack}>
          <ArrowLeft aria-hidden="true" className="size-4" />
          Back
        </Button>
        {viaAgent ? (
          <Button
            type="button"
            onClick={() => setPolling((current) => !current)}
            disabled={connected}
            className="mr-auto"
          >
            <RefreshCw
              aria-hidden="true"
              className={`size-4 ${polling && !connected ? 'animate-spin' : ''}`}
            />
            {connected ? 'Connected' : polling ? 'Watching…' : 'Resume watching'}
          </Button>
        ) : (
          <Button type="button" onClick={runCheck} disabled={checking} className="mr-auto">
            <RefreshCw aria-hidden="true" className={`size-4 ${checking ? 'animate-spin' : ''}`} />
            {checking ? 'Checking…' : 'Run check'}
          </Button>
        )}
        <Button type="button" variant="primary" onClick={onNext}>
          {connected ? 'Continue' : 'Skip for now'}
          <ArrowRight aria-hidden="true" className="size-4" />
        </Button>
      </StepActions>
    </Panel>
  )
}

/**
 * ObservabilityStep wires the cluster's series backends while the operator is
 * still here. KubeMG's live meters read the cluster's own Metrics API, which
 * keeps about two minutes — so a cluster registered without a datasource has no
 * history at all, and the moment to notice that is now rather than the first
 * time someone asks what happened last night. It is optional on purpose: a
 * cluster is usable without one, and the step says so instead of blocking.
 */
function ObservabilityStep({
  cluster,
  onBack,
  onNext,
}: {
  cluster: Cluster
  onBack: () => void
  onNext: () => void
}) {
  return (
    <div className="flex flex-col gap-5">
      <DatasourcePanel cluster={cluster} eyebrow="Step 4" />

      <div className="card overflow-hidden">
        <p className="px-4 py-3 text-[13px] leading-relaxed text-muted">
          Optional. Skip it and the cluster still works — you just see the last couple of minutes
          the cluster keeps itself, and nothing before that. It can be connected later from the
          cluster page.
        </p>
        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button type="button" variant="primary" onClick={onNext}>
            Continue
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </div>
    </div>
  )
}

/**
 * AccessStep grants the first permissions on the new cluster. It is the same
 * decision the permissions matrix makes, narrowed to one cluster so the last step
 * of registration is "who can use this" rather than "go and find the matrix".
 */
function AccessStep({
  cluster,
  onBack,
  onDone,
}: {
  cluster: Cluster
  onBack: () => void
  onDone: () => void
}) {
  const [users, setUsers] = useState<User[]>([])
  const [groups, setGroups] = useState<Group[]>([])
  const [granted, setGranted] = useState<Permission[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  const [subjectType, setSubjectType] = useState<SubjectType>('group')
  const [subjectId, setSubjectId] = useState('')
  const [role, setRole] = useState<K8sRole>('view')
  const [namespaces, setNamespaces] = useState('')

  const load = useCallback(async () => {
    try {
      const [matrix, nextUsers, nextGroups] = await Promise.all([
        fetchPermissions(),
        fetchUsers(),
        fetchGroups(),
      ])
      setGranted(
        [...matrix.user_permissions, ...matrix.group_permissions].filter(
          (permission) => permission.cluster_id === cluster.id,
        ),
      )
      setUsers(nextUsers)
      setGroups(nextGroups)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load users and groups.'))
    } finally {
      setLoading(false)
    }
  }, [cluster.id])

  useEffect(() => {
    void load()
  }, [load])

  const subjects = useMemo(
    () =>
      subjectType === 'user'
        ? users.map((user) => ({ id: user.id, label: user.username }))
        : groups.map((group) => ({ id: group.id, label: group.name })),
    [subjectType, users, groups],
  )

  async function grant(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!subjectId) return

    setBusy(true)
    setError(null)
    try {
      await assignPermission({
        subject_type: subjectType,
        subject_id: Number(subjectId),
        cluster_id: cluster.id,
        k8s_role: role,
        namespaces: namespaces
          .split(',')
          .map((value) => value.trim())
          .filter(Boolean),
      })
      setSubjectId('')
      setNamespaces('')
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not grant access.'))
    } finally {
      setBusy(false)
    }
  }

  async function revoke(permission: Permission) {
    setBusy(true)
    setError(null)
    try {
      await revokePermission(permission.subject_type, permission.subject_id, cluster.id)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not revoke access.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="flex flex-col gap-5">
      <Panel eyebrow="Step 5" title={`Who can use ${cluster.name}?`}>
        <form onSubmit={grant} className="flex flex-col gap-4 p-4">
          {error ? <Notice tone="error">{error}</Notice> : null}

          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Grant to" htmlFor="subject_type">
              <Select
                id="subject_type"
                value={subjectType}
                onChange={(event) => {
                  setSubjectType(event.target.value as SubjectType)
                  setSubjectId('')
                }}
              >
                <option value="group">A group</option>
                <option value="user">One user</option>
              </Select>
            </Field>

            <Field
              label={subjectType === 'user' ? 'User' : 'Group'}
              htmlFor="subject_id"
              hint={subjectType === 'group' ? 'Every member inherits the grant.' : undefined}
            >
              <Select
                id="subject_id"
                value={subjectId}
                onChange={(event) => setSubjectId(event.target.value)}
              >
                <option value="">Select…</option>
                {subjects.map((subject) => (
                  <option key={subject.id} value={subject.id}>
                    {subject.label}
                  </option>
                ))}
              </Select>
            </Field>

            <Field
              label="Kubernetes role"
              htmlFor="k8s_role"
              hint="cluster-admin grants full control of the cluster."
            >
              <Select
                id="k8s_role"
                value={role}
                onChange={(event) => setRole(event.target.value as K8sRole)}
              >
                {K8S_ROLES.map((value) => (
                  <option key={value} value={value}>
                    {value}
                  </option>
                ))}
              </Select>
            </Field>

            <Field
              label="Namespaces"
              htmlFor="namespaces"
              hint="Comma-separated. Leave empty for every namespace the role allows."
            >
              <TextInput
                id="namespaces"
                placeholder="team-a, team-b"
                value={namespaces}
                onChange={(event) => setNamespaces(event.target.value)}
              />
            </Field>
          </div>

          <div className="flex justify-end">
            <Button type="submit" disabled={busy || !subjectId}>
              Grant access
            </Button>
          </div>
        </form>

        {granted.length > 0 ? (
          <ul className="border-t border-line-soft">
            {granted.map((permission) => (
              <li
                key={`${permission.subject_type}:${permission.subject_id}`}
                className="flex items-center gap-3 border-b border-line-soft px-4 py-2.5 last:border-b-0"
              >
                <span className="label w-14 shrink-0">{permission.subject_type}</span>
                <span className="min-w-0 flex-1 truncate font-mono text-[13px] text-fg">
                  {permission.subject_name}
                </span>
                <span className="font-mono text-[12.5px] text-muted">{permission.k8s_role}</span>
                <span className="hidden truncate font-mono text-[12px] text-faint sm:block">
                  {permission.namespaces.length > 0
                    ? permission.namespaces.join(', ')
                    : 'all namespaces'}
                </span>
                <IconButton
                  label={`Revoke ${permission.subject_name}`}
                  tone="danger"
                  onClick={() => revoke(permission)}
                  disabled={busy}
                >
                  <Trash2 aria-hidden="true" className="size-3.5" />
                </IconButton>
              </li>
            ))}
          </ul>
        ) : (
          <p className="border-t border-line-soft px-4 py-8 text-center text-[13px] text-muted">
            {loading ? 'Loading…' : 'No one has access yet. Admins can always reach every cluster.'}
          </p>
        )}

        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-4" />
            Back
          </Button>
          <Button type="button" variant="primary" onClick={onDone}>
            Done
            <ArrowRight aria-hidden="true" className="size-4" />
          </Button>
        </StepActions>
      </Panel>

      {cluster.connection_mode === 'direct' ? (
        <Notice tone="warn">
          <strong className="font-semibold">These grants govern KubeMG, not the cluster.</strong> In
          direct mode KubeMG issues a token but creates no RoleBinding, so a grant decides what the
          kubeconfig claims — not what the cluster allows. Agent-based clusters bind these roles for
          real.
        </Notice>
      ) : null}
    </div>
  )
}

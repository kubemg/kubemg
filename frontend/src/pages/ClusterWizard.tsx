import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { FormEvent, ReactNode } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  AlertTriangle,
  ArrowLeft,
  ArrowRight,
  Check,
  Download,
  Loader2,
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
import {
  Button,
  CodeBlock,
  EnvironmentTag,
  Field,
  Notice,
  Panel,
  Pill,
  Select,
  StatusDot,
  TextArea,
  TextInput,
} from '../components/primitives'
import { useClusters } from '../state/clusters-context'

const ENVIRONMENTS: Environment[] = ['prod', 'staging', 'dev']
const K8S_ROLES: K8sRole[] = ['cluster-admin', 'edit', 'view']

/* Polling is the whole point of step three: the operator has just pasted a
   command into a terminal somewhere else, and this screen is how they learn it
   worked. Three seconds is fast enough to feel live without hammering the API. */
const POLL_INTERVAL_MS = 3000

const STEPS = ['Identity', 'Connection', 'Handshake', 'Access'] as const
type StepIndex = 0 | 1 | 2 | 3

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
            <ArrowRight aria-hidden="true" className="size-3.5" />
          </Button>
        ) : null
      }
    >
      <div className="flex min-w-0 max-w-4xl flex-col gap-4">
        <Stepper current={step} furthest={cluster ? 3 : identityReady ? 1 : 0} onSelect={setStep} />

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
          <AccessStep
            cluster={cluster}
            onBack={() => setStep(2)}
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
 * Stepper carries progress as form as well as colour: a spine on the leading
 * edge of each cell, and a mark rather than a number once a step is behind you.
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
    <ol className="panel flex flex-col overflow-hidden sm:flex-row">
      {STEPS.map((label, index) => {
        const done = index < current
        const active = index === current
        const reachable = index <= furthest
        const spine = active ? 'bg-primary' : done ? 'bg-ok' : 'bg-faint/40'

        return (
          <li key={label} className="min-w-0 flex-1">
            <button
              type="button"
              disabled={!reachable}
              onClick={() => onSelect(index as StepIndex)}
              className={`flex w-full items-center gap-2.5 border-b border-line-soft py-2.5 pr-3 text-left transition-colors last:border-b-0 sm:border-r sm:border-b-0 ${
                reachable ? 'hover:bg-raised' : 'cursor-not-allowed opacity-50'
              } ${active ? 'bg-primary-soft/40' : ''}`}
            >
              <span aria-hidden="true" className={`h-8 w-[3px] rounded-r-[2px] ${spine}`} />
              <span
                className={`grid size-5 shrink-0 place-items-center rounded-full font-mono text-[11px] ${
                  done
                    ? 'bg-ok-soft text-ok'
                    : active
                      ? 'bg-primary text-white'
                      : 'bg-raised text-muted'
                }`}
              >
                {done ? <Check aria-hidden="true" className="size-3" /> : index + 1}
              </span>
              <span
                className={`truncate text-[12.5px] ${active ? 'font-medium text-fg' : 'text-muted'}`}
              >
                {label}
              </span>
            </button>
          </li>
        )
      })}
    </ol>
  )
}

/** StepActions is the consistent footer every step ends with. */
function StepActions({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center justify-end gap-2 border-t border-line-soft px-3.5 py-3">
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
      <Panel title="What is this cluster?">
        <div className="flex flex-col gap-3.5 p-3.5">
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
              value={value.name}
              onChange={(event) => update('name', event.target.value)}
            />
          </Field>

          <Field
            label="Environment"
            htmlFor="environment"
            hint="Drives how loudly the fleet overview flags it. Production reads as production everywhere."
          >
            <div className="flex flex-wrap gap-1.5">
              {ENVIRONMENTS.map((environment) => (
                <button
                  key={environment}
                  type="button"
                  disabled={locked}
                  onClick={() => update('environment', environment)}
                  className={`flex items-center gap-2 rounded-[5px] border px-2.5 py-1.5 text-[12.5px] transition-colors disabled:cursor-not-allowed disabled:opacity-60 ${
                    value.environment === environment
                      ? 'border-primary bg-primary-soft text-primary'
                      : 'border-line bg-surface text-muted hover:border-faint hover:text-fg'
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
            <ArrowRight aria-hidden="true" className="size-3.5" />
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
    <form onSubmit={onSubmit} className="flex flex-col gap-4">
      <Panel title="How should KubeMG reach it?">
        <div className="grid gap-2 p-3.5 sm:grid-cols-2">
          <ModeCard
            mode="agent"
            selected={mode === 'agent'}
            locked={cluster !== null}
            onSelect={onModeChange}
            icon={Plug}
            title="Agent-based"
            tagline="Recommended"
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
            points={[
              'KubeMG dials the API server itself, so it must be routable from here.',
              'A service account token is stored in KubeMG.',
              'Issues kubeconfigs only — no proxy and no audit trail.',
            ]}
          />
        </div>

        {mode === 'direct' && !cluster ? (
          <div className="flex flex-col gap-3.5 border-t border-line-soft p-3.5">
            <Field label="API server URL" htmlFor="api_url">
              <TextInput
                id="api_url"
                type="url"
                required
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
            <ArrowLeft aria-hidden="true" className="size-3.5" />
            Back
          </Button>
          {cluster ? (
            <Button type="button" variant="primary" onClick={onNext}>
              Continue
              <ArrowRight aria-hidden="true" className="size-3.5" />
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
  points,
}: {
  mode: ConnectionMode
  selected: boolean
  locked: boolean
  onSelect: (mode: ConnectionMode) => void
  icon: typeof Plug
  title: string
  tagline: string
  points: string[]
}) {
  return (
    <button
      type="button"
      disabled={locked}
      onClick={() => onSelect(mode)}
      aria-pressed={selected}
      className={`flex flex-col gap-2 rounded-[5px] border p-3 text-left transition-colors disabled:cursor-not-allowed ${
        selected
          ? 'border-primary bg-primary-soft/30'
          : 'border-line bg-surface hover:border-faint disabled:opacity-50'
      }`}
    >
      <div className="flex items-center gap-2">
        <Icon
          aria-hidden="true"
          className={`size-4 shrink-0 ${selected ? 'text-primary' : 'text-muted'}`}
        />
        <span className="text-[13px] font-semibold text-fg">{title}</span>
        <span className="ml-auto">
          <Pill tone={selected ? 'accent' : 'neutral'} dot={false}>
            {tagline}
          </Pill>
        </span>
      </div>
      <ul className="flex flex-col gap-1">
        {points.map((point) => (
          <li key={point} className="text-[11.5px] leading-snug text-muted">
            {point}
          </li>
        ))}
      </ul>
    </button>
  )
}

/**
 * AgentInstaller is the handoff: one command to run somewhere else. The token
 * is shown separately and masked, because it is the cluster's only credential
 * and it is about to live in a Kubernetes Secret.
 */
function AgentInstaller({ install }: { install: AgentInstall }) {
  const [showManifest, setShowManifest] = useState(false)

  return (
    <Panel
      title="Install the agent"
      actions={
        <a
          href={install.manifest_url}
          download
          className="inline-flex items-center gap-1.5 text-[12px] text-muted transition-colors hover:text-primary"
        >
          <Download aria-hidden="true" className="size-3.5" />
          Download YAML
        </a>
      }
    >
      <div className="flex flex-col gap-3.5 p-3.5">
        <p className="text-[12.5px] leading-relaxed text-muted">
          Run this against <span className="font-mono text-fg">{install.cluster}</span> with a
          kubeconfig that can create resources in{' '}
          <span className="font-mono text-fg">{install.namespace}</span>. The agent dials back out
          to KubeMG — nothing needs to be opened inbound.
        </p>

        <CodeBlock label="Install command" value={install.apply_command} />

        <details className="group">
          <summary className="cursor-pointer text-[12px] text-muted transition-colors hover:text-fg">
            Prefer Kustomize?
          </summary>
          <div className="mt-2 flex flex-col gap-2">
            <p className="text-[11.5px] leading-snug text-muted">
              Kustomize only accepts local paths and Git specs as remote targets, so the package is
              fetched and extracted first.
            </p>
            <CodeBlock value={install.kustomize_command} />
          </div>
        </details>

        <dl className="grid grid-cols-[112px_minmax(0,1fr)] gap-x-3 gap-y-2 border-t border-line-soft pt-3 text-[12.5px]">
          <dt className="text-muted">Bastion</dt>
          <dd className="truncate font-mono text-fg">{install.bastion_url}</dd>
          <dt className="text-muted">Namespace</dt>
          <dd className="truncate font-mono text-fg">{install.namespace}</dd>
          <dt className="text-muted">Image</dt>
          <dd className="truncate font-mono text-fg">{install.image}</dd>
        </dl>

        <CodeBlock label="Registration token" value={install.agent_token} secret />
        <p className="text-[11.5px] leading-snug text-warn">
          <AlertTriangle aria-hidden="true" className="mr-1 inline size-3" />
          This token authenticates the tunnel for this cluster. It is embedded in the command above
          — treat both like a credential.
        </p>

        <div>
          <button
            type="button"
            onClick={() => setShowManifest((current) => !current)}
            className="text-[12px] text-muted transition-colors hover:text-fg"
          >
            {showManifest ? 'Hide' : 'Review'} the manifest before applying
          </button>
          {showManifest ? (
            <pre className="mt-2 max-h-80 overflow-auto rounded-[5px] border border-ink-line bg-ink px-2.5 py-2 font-mono text-[11.5px] leading-relaxed text-ink-fg">
              {install.manifest}
            </pre>
          ) : null}
        </div>
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

  const connected = cluster.connection_mode === 'agent' ? cluster.agent_attached : cluster.status === 'healthy'

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
        // A transient failure while waiting is not worth interrupting the
        // wait for; the next tick will say the same thing if it is real.
      }
    }, POLL_INTERVAL_MS)

    return () => {
      cancelled = true
      window.clearInterval(timer)
    }
  }, [clusterId, polling])

  // Stop polling the moment it lands, rather than keeping a timer alive behind
  // a screen that has nothing left to report.
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
    <Panel title={connected ? 'Connected' : 'Waiting for the cluster'}>
      <div className="flex flex-col gap-3.5 p-3.5">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <div className="flex flex-wrap items-center gap-2.5 rounded-[5px] border border-line bg-bg px-3 py-2.5">
          <span aria-hidden="true" className={`h-8 w-[3px] rounded-r-[2px] ${connected ? 'bg-ok' : 'bg-warn'}`} />
          {connected ? (
            <Check aria-hidden="true" className="size-4 shrink-0 text-ok" />
          ) : (
            <Loader2 aria-hidden="true" className="size-4 shrink-0 animate-spin text-warn" />
          )}
          <div className="min-w-0 flex-1">
            <p className="text-[13px] text-fg">
              {connected
                ? cluster.connection_mode === 'agent'
                  ? 'The agent is connected and the tunnel is open.'
                  : 'The API server answered.'
                : cluster.connection_mode === 'agent'
                  ? 'No agent has dialled in yet. Run the install command and this will update on its own.'
                  : 'The cluster has not been probed yet.'}
            </p>
            {cluster.status_message ? (
              <p className="mt-0.5 text-[11.5px] text-muted">{cluster.status_message}</p>
            ) : null}
          </div>
          <StatusDot status={cluster.status} message={cluster.status_message} />
        </div>

        <dl className="grid grid-cols-[112px_minmax(0,1fr)] gap-x-3 gap-y-2 text-[12.5px]">
          <dt className="text-muted">Cluster</dt>
          <dd className="truncate font-mono text-fg">{cluster.name}</dd>
          <dt className="text-muted">Mode</dt>
          <dd className="font-mono text-fg">{cluster.connection_mode}</dd>
          <dt className="text-muted">Kubernetes</dt>
          <dd className="font-mono text-fg">{cluster.kubernetes_version ?? 'unknown'}</dd>
          {cluster.connection_mode === 'agent' ? (
            <>
              <dt className="text-muted">Agent</dt>
              <dd className="font-mono text-fg">{cluster.agent_version ?? 'not seen yet'}</dd>
            </>
          ) : null}
        </dl>

        {!connected && install ? (
          <CodeBlock label="Install command" value={install.apply_command} />
        ) : null}
      </div>

      <StepActions>
        <Button type="button" variant="ghost" onClick={onBack}>
          <ArrowLeft aria-hidden="true" className="size-3.5" />
          Back
        </Button>
        {cluster.connection_mode === 'agent' ? (
          <Button
            type="button"
            onClick={() => setPolling((current) => !current)}
            disabled={connected}
            className="mr-auto"
          >
            <RefreshCw
              aria-hidden="true"
              className={`size-3.5 ${polling && !connected ? 'animate-spin' : ''}`}
            />
            {connected ? 'Connected' : polling ? 'Watching…' : 'Resume watching'}
          </Button>
        ) : (
          <Button type="button" onClick={runCheck} disabled={checking} className="mr-auto">
            <RefreshCw aria-hidden="true" className={`size-3.5 ${checking ? 'animate-spin' : ''}`} />
            {checking ? 'Checking…' : 'Run check'}
          </Button>
        )}
        <Button type="button" variant="primary" onClick={onNext}>
          {connected ? 'Continue' : 'Skip for now'}
          <ArrowRight aria-hidden="true" className="size-3.5" />
        </Button>
      </StepActions>
    </Panel>
  )
}

/**
 * AccessStep grants the first permissions on the new cluster. It is the same
 * decision the permissions matrix makes, narrowed to one cluster so the last
 * step of registration is "who can use this" rather than "go and find the
 * matrix".
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
    <div className="flex flex-col gap-4">
      <Panel title={`Who can use ${cluster.name}?`}>
        <form onSubmit={grant} className="flex flex-col gap-3.5 p-3.5">
          {error ? <Notice tone="error">{error}</Notice> : null}

          <div className="grid gap-3 sm:grid-cols-2">
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
                className="flex items-center gap-2.5 border-b border-line-soft px-3.5 py-2 last:border-b-0"
              >
                <span className="label w-12 shrink-0">{permission.subject_type}</span>
                <span className="min-w-0 flex-1 truncate font-mono text-[12.5px] text-fg">
                  {permission.subject_name}
                </span>
                <span className="font-mono text-[12px] text-muted">{permission.k8s_role}</span>
                <span className="hidden truncate font-mono text-[11.5px] text-faint sm:block">
                  {permission.namespaces.length > 0
                    ? permission.namespaces.join(', ')
                    : 'all namespaces'}
                </span>
                <button
                  type="button"
                  onClick={() => revoke(permission)}
                  disabled={busy}
                  title={`Revoke ${permission.subject_name}`}
                  className="rounded-sm border border-transparent p-1 text-muted transition-colors hover:border-danger/40 hover:text-danger disabled:opacity-50"
                >
                  <Trash2 aria-hidden="true" className="size-3.5" />
                  <span className="sr-only">Revoke {permission.subject_name}</span>
                </button>
              </li>
            ))}
          </ul>
        ) : (
          <p className="border-t border-line-soft px-3.5 py-6 text-center text-[12px] text-muted">
            {loading
              ? 'Loading…'
              : 'No one has access yet. Admins can always reach every cluster.'}
          </p>
        )}

        <StepActions>
          <Button type="button" variant="ghost" onClick={onBack}>
            <ArrowLeft aria-hidden="true" className="size-3.5" />
            Back
          </Button>
          <Button type="button" variant="primary" onClick={onDone}>
            Done
            <ArrowRight aria-hidden="true" className="size-3.5" />
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

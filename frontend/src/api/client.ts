import axios from 'axios'
import type { CustomResourceRef, ResourceKey } from '../lib/resources'
import { ALL_NAMESPACES } from '../lib/resources'
import type { TimeRangeId } from '../lib/timerange'
import type {
  AccessReviewQuestion,
  AccessReviewResult,
  AgentInstall,
  AlarmChannel,
  AlarmChannelInput,
  AlarmChannelList,
  AlarmChannelTest,
  AlarmRule,
  AlarmRuleInput,
  AlarmRuleList,
  GuardrailPolicy,
  GuardrailPolicyInput,
  GuardrailPolicyList,
  GuardrailTemplate,
  AuditPage,
  AuditQuery,
  AuditSummary,
  Cluster,
  ClusterCapacity,
  ClusterListResponse,
  ClusterNode,
  ConfigEntry,
  CronJob,
  CustomResource,
  CustomResourceDefinition,
  CRDVisibility,
  ClusterConsole,
  ClusterConsolesResponse,
  ConsoleInput,
  ConsoleKind,
  DatasourceCandidate,
  DatasourceCheck,
  DatasourceInput,
  DatasourceKind,
  DeploymentPosture,
  ClusterRoleEntry,
  EventTimeline,
  GrantIdentity,
  Group,
  IssuedMachineToken,
  MachineAccount,
  MachineToken,
  NewMachineToken,
  HelmHistory,
  HelmRelease,
  HelmValues,
  Ingress,
  JitRequest,
  JitRequestInput,
  JitRequestList,
  JitStatus,
  Job,
  Kubeconfig,
  KubeconfigPolicy,
  ServerVersion,
  LogQueryResponse,
  LoginResponse,
  ManifestDiff,
  Namespace,
  NetworkPolicy,
  NetworkPolicyCoverage,
  NetworkPolicyReachability,
  NewCluster,
  ObservabilityResponse,
  ObservabilitySource,
  NewUser,
  NodeMetrics,
  OptionalList,
  ResourceCount,
  MetricKind,
  MetricCompareResponse,
  MetricQueryResponse,
  Permission,
  PermissionGrant,
  PermissionMatrix,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  PodListMetrics,
  PodMetrics,
  PostureAcknowledgement,
  PostureRule,
  PostureScan,
  ResourceDescribeResult,
  ResourceManifest,
  RoleBindingEntry,
  Route,
  ServiceAccountEntry,
  Service,
  SettingsPatch,
  SettingsResponse,
  SetupPreflight,
  SetupState,
  SSOGroupMapping,
  SSOGroupMappingInput,
  SSOProvider,
  SSOProviderCheck,
  SSOProviderInput,
  SSOProviderListResponse,
  SSOProviderSummary,
  StorageClass,
  SubjectType,
  RecordingPolicy,
  TerminalSession,
  TerminalSessionPage,
  TerminalSessionQuery,
  User,
  UserPatch,
  Workload,
  DeleteResult,
  WorkloadActionResult,
  WorkloadPods,
} from './types'

const TOKEN_KEY = 'kubemg.token'

/*
 * The API origin defaults to *this* page's origin, because a browser reaching
 * KubeMG from another machine cannot resolve the server's own loopback address.
 * Set VITE_API_BASE_URL only when the API genuinely lives on a different host;
 * left empty, requests stay same-origin and the dev server (or a reverse proxy
 * in front of the built assets) forwards /api to the backend.
 */
const apiOrigin = (import.meta.env.VITE_API_BASE_URL ?? '').replace(/\/+$/, '')

const baseURL = `${apiOrigin}/api/v1`

export const http = axios.create({ baseURL })

export function readToken(): string | null {
  return localStorage.getItem(TOKEN_KEY)
}

export function writeToken(token: string | null) {
  if (token === null) {
    localStorage.removeItem(TOKEN_KEY)
    return
  }
  localStorage.setItem(TOKEN_KEY, token)
}

// The provider registers a callback so an expired token drops the session
// instead of leaving the UI in a half-signed-in state.
let onUnauthorized: (() => void) | null = null

export function setUnauthorizedHandler(handler: (() => void) | null) {
  onUnauthorized = handler
}

/*
 * Asking the cluster rather than a cache.
 *
 * KubeMG holds a recently-answered read in memory on the server for a few
 * seconds, keyed to the caller, so the console's own navigation does not cost a
 * tunnel round trip per click. `Cache-Control: no-cache` is how a caller opts
 * out of that, and Refresh is what sends it: an operator asking again explicitly
 * is asking the cluster.
 *
 * It is a depth counter around a block rather than an argument on forty fetch
 * functions, because a refresh is usually several reads at once — a pod list and
 * its usage, a manifest and its events — and threading a flag through each of
 * them would be a change to every signature for one header. Anything else that
 * happens to be requested inside the block is sent uncached too; that costs one
 * round trip and cannot return anything wrong.
 */
let freshDepth = 0

export async function withFreshReads<T>(run: () => Promise<T>): Promise<T> {
  freshDepth += 1
  try {
    return await run()
  } finally {
    freshDepth -= 1
  }
}

/*
 * What a read could not finish.
 *
 * A list is paged and bounded on the server: past its budget the read stops and
 * the response carries `truncated`, because a table showing 2000 rows of a
 * cluster's 12000 with nothing to mark the difference is worse than one that
 * says so. That flag has to reach the surface rendering the rows.
 *
 * It is collected around a block rather than returned from each of forty list
 * functions, for the reason withFreshReads above is written the same way: a
 * loader often makes several reads for one view — a pod list and its usage, a
 * list and the CRDs behind it — and threading a second return value through
 * every signature would be a change to all of them for one field almost none of
 * them will ever set. The block reports the *largest* bound any read inside it
 * hit, which is the one the operator needs to know about.
 */
let readReport: { truncatedAt?: number } | null = null

export interface ReadReport {
  /**
   * The bound a read stopped at, absent when every read inside the block
   * returned the cluster's whole answer.
   */
  truncatedAt?: number
}

export async function withReadReport<T>(
  run: () => Promise<T>,
): Promise<{ value: T; report: ReadReport }> {
  const previous = readReport
  const report: { truncatedAt?: number } = {}
  readReport = report
  try {
    return { value: await run(), report }
  } finally {
    readReport = previous
  }
}

/** noteTruncation folds one response's bound into the block's report. */
function noteTruncation(body: unknown) {
  if (readReport === null || typeof body !== 'object' || body === null) return
  const payload = body as { truncated?: boolean; truncated_at?: number }
  if (payload.truncated !== true) return
  const at = typeof payload.truncated_at === 'number' ? payload.truncated_at : 0
  readReport.truncatedAt = Math.max(readReport.truncatedAt ?? 0, at)
}

http.interceptors.request.use((config) => {
  const token = readToken()
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  if (freshDepth > 0) {
    config.headers['Cache-Control'] = 'no-cache'
  }
  return config
})

http.interceptors.response.use(
  (response) => {
    noteTruncation(response.data)
    return response
  },
  (error: unknown) => {
    if (axios.isAxiosError(error) && error.response?.status === 401) {
      onUnauthorized?.()
    }
    return Promise.reject(error)
  },
)

/** errorMessage turns an API failure into something worth showing a person. */
export function errorMessage(error: unknown, fallback: string): string {
  if (axios.isAxiosError(error)) {
    const detail = (error.response?.data as { error?: string } | undefined)?.error
    if (detail) return detail
    if (error.code === 'ERR_NETWORK') return 'Cannot reach the kubemg server.'
  }
  return fallback
}

/**
 * What a KubeMG guardrail said, as opposed to the cluster's own RBAC. The two
 * read identically as a bare 403 — both `guardAPIRequest` in the gateway and
 * the manifest write path stamp the same three fields on a refusal that came
 * from a policy rather than from the tunnel — so this is the one place that
 * tells them apart. `null` covers both "this was not a 403" and "it was a
 * 403 the cluster itself sent back", which the confirmation step treats the
 * same way: as the cluster's own answer, not KubeMG's.
 */
export interface GuardrailRefusal {
  message: string
  policy: string
  scope: string
}

export function guardrailRefusal(error: unknown): GuardrailRefusal | null {
  if (!axios.isAxiosError(error)) return null
  const data = error.response?.data as
    | { guardrail_blocked?: boolean; error?: string; policy?: string; scope?: string }
    | undefined
  if (!data?.guardrail_blocked) return null
  return { message: data.error ?? 'Blocked by a guardrail policy.', policy: data.policy ?? '', scope: data.scope ?? '' }
}

export async function login(username: string, password: string): Promise<LoginResponse> {
  const { data } = await http.post<LoginResponse>('/auth/login', { username, password })
  return data
}

export async function fetchMe(): Promise<User> {
  const { data } = await http.get<User>('/auth/me')
  return data
}

/* ---------------------------------------------------------------- sso --- */

/**
 * The providers the login page offers. It is the one call made with no session
 * at all, so a failure here is not worth surfacing as an error: an install with
 * no identity provider configured answers an empty list, and one whose server is
 * unreachable will fail the sign-in itself a moment later with a better message.
 */
export async function fetchSSOProviders(): Promise<SSOProviderSummary[]> {
  const { data } = await http.get<{ providers: SSOProviderSummary[] }>('/auth/sso/providers')
  return data.providers ?? []
}

/**
 * Where the browser goes to start an interactive sign-in. The console's own
 * origin travels with it so the server knows where to hand the session back —
 * and refuses any origin it was not configured to trust.
 */
export function ssoLoginURL(providerId: number): string {
  const redirect = encodeURIComponent(window.location.origin)
  return `${baseURL}/auth/sso/providers/${providerId}/login?redirect_uri=${redirect}`
}

/** LDAP has no redirect: the credentials are checked against the directory here. */
export async function ssoPasswordLogin(
  providerId: number,
  username: string,
  password: string,
): Promise<LoginResponse> {
  const { data } = await http.post<LoginResponse>(`/auth/sso/providers/${providerId}/login`, {
    username,
    password,
  })
  return data
}

export async function fetchSSOAdminProviders(): Promise<SSOProviderListResponse> {
  const { data } = await http.get<SSOProviderListResponse>('/admin/sso/providers')
  return { providers: data.providers ?? [], console_origins: data.console_origins ?? [] }
}

export async function createSSOProvider(input: SSOProviderInput): Promise<SSOProvider> {
  const { data } = await http.post<SSOProvider>('/admin/sso/providers', input)
  return data
}

export async function updateSSOProvider(
  id: number,
  input: SSOProviderInput,
): Promise<SSOProvider> {
  const { data } = await http.put<SSOProvider>(`/admin/sso/providers/${id}`, input)
  return data
}

export async function deleteSSOProvider(id: number): Promise<void> {
  await http.delete(`/admin/sso/providers/${id}`)
}

/** Proves the configuration reaches the directory, and records the verdict. */
export async function checkSSOProvider(id: number): Promise<SSOProviderCheck> {
  const { data } = await http.post<SSOProviderCheck>(`/admin/sso/providers/${id}/check`)
  return data
}

export async function fetchSSOMappings(providerId?: number): Promise<SSOGroupMapping[]> {
  const { data } = await http.get<{ mappings: SSOGroupMapping[] }>('/admin/sso/mappings', {
    params: providerId ? { provider_id: providerId } : undefined,
  })
  return (data.mappings ?? []).map((mapping) => ({ ...mapping, namespaces: mapping.namespaces ?? [] }))
}

export async function createSSOMapping(input: SSOGroupMappingInput): Promise<SSOGroupMapping> {
  const { data } = await http.post<SSOGroupMapping>('/admin/sso/mappings', input)
  return data
}

export async function updateSSOMapping(
  id: number,
  input: SSOGroupMappingInput,
): Promise<SSOGroupMapping> {
  const { data } = await http.put<SSOGroupMapping>(`/admin/sso/mappings/${id}`, input)
  return data
}

export async function deleteSSOMapping(id: number): Promise<void> {
  await http.delete(`/admin/sso/mappings/${id}`)
}

export async function fetchClusters(): Promise<Cluster[]> {
  const { data } = await http.get<ClusterListResponse>('/clusters')
  return data.clusters ?? []
}

export async function fetchCluster(id: number): Promise<Cluster> {
  const { data } = await http.get<Cluster>(`/clusters/${id}`)
  return data
}

/** checkCluster probes the target cluster and returns its refreshed record. */
export async function checkCluster(id: number): Promise<Cluster> {
  const { data } = await http.post<Cluster>(`/clusters/${id}/check`)
  return data
}

export async function createCluster(input: NewCluster): Promise<Cluster> {
  const { data } = await http.post<Cluster>('/clusters', input)
  return data
}

export async function deleteCluster(id: number): Promise<void> {
  await http.delete(`/clusters/${id}`)
}

/** fetchAgentInstall returns the rendered agent installation package. */
export async function fetchAgentInstall(clusterId: number): Promise<AgentInstall> {
  const { data } = await http.get<AgentInstall>(`/clusters/${clusterId}/kustomize`)
  return data
}

export async function fetchUsers(): Promise<User[]> {
  const { data } = await http.get<{ users: User[] }>('/users')
  return data.users ?? []
}

export async function createUser(input: NewUser): Promise<User> {
  const { data } = await http.post<User>('/users', input)
  return data
}

export async function updateUser(id: number, patch: UserPatch): Promise<User> {
  const { data } = await http.put<User>(`/users/${id}`, patch)
  return data
}

export async function setUserStatus(id: number, isActive: boolean): Promise<User> {
  const { data } = await http.patch<User>(`/users/${id}/status`, { is_active: isActive })
  return data
}

export async function deleteUser(id: number): Promise<void> {
  await http.delete(`/users/${id}`)
}

/* Programmatic access. A machine account's whole surface is administrative:
   creating one is creating an account, and issuing its credential is handing out
   standing access to a cluster. */

export async function fetchMachineAccounts(): Promise<MachineAccount[]> {
  const { data } = await http.get<{ machine_accounts: MachineAccount[] }>('/machine-accounts')
  return data.machine_accounts ?? []
}

export async function createMachineAccount(username: string, email: string): Promise<MachineAccount> {
  const { data } = await http.post<MachineAccount>('/machine-accounts', { username, email })
  return data
}

export async function setMachineAccountStatus(id: number, isActive: boolean): Promise<MachineAccount> {
  const { data } = await http.patch<MachineAccount>(`/machine-accounts/${id}/status`, {
    is_active: isActive,
  })
  return data
}

export async function deleteMachineAccount(id: number): Promise<void> {
  await http.delete(`/machine-accounts/${id}`)
}

export async function fetchMachineTokens(accountId: number): Promise<MachineToken[]> {
  const { data } = await http.get<{ tokens: MachineToken[] }>(`/machine-accounts/${accountId}/tokens`)
  return data.tokens ?? []
}

export async function issueMachineToken(
  accountId: number,
  input: NewMachineToken,
): Promise<IssuedMachineToken> {
  const { data } = await http.post<IssuedMachineToken>(
    `/machine-accounts/${accountId}/tokens`,
    input,
  )
  return data
}

export async function revokeMachineToken(accountId: number, tokenId: number): Promise<MachineToken> {
  const { data } = await http.delete<MachineToken>(
    `/machine-accounts/${accountId}/tokens/${tokenId}`,
  )
  return data
}

export async function fetchGroups(): Promise<Group[]> {
  const { data } = await http.get<{ groups: Group[] }>('/groups')
  return data.groups ?? []
}

export async function createGroup(name: string, description: string): Promise<Group> {
  const { data } = await http.post<Group>('/groups', { name, description })
  return data
}

export async function deleteGroup(id: number): Promise<void> {
  await http.delete(`/groups/${id}`)
}

export async function addGroupMember(groupId: number, userId: number): Promise<void> {
  await http.post(`/groups/${groupId}/members`, { user_id: userId })
}

export async function removeGroupMember(groupId: number, userId: number): Promise<void> {
  await http.delete(`/groups/${groupId}/members/${userId}`)
}

export async function fetchPermissions(): Promise<PermissionMatrix> {
  const { data } = await http.get<PermissionMatrix>('/permissions')
  return {
    user_permissions: data.user_permissions ?? [],
    group_permissions: data.group_permissions ?? [],
  }
}

export async function assignPermission(grant: PermissionGrant): Promise<Permission> {
  const { data } = await http.post<Permission>('/permissions/assign', grant)
  return data
}

export async function revokePermission(
  subjectType: SubjectType,
  subjectId: number,
  clusterId: number,
): Promise<void> {
  await http.post('/permissions/revoke', {
    subject_type: subjectType,
    subject_id: subjectId,
    cluster_id: clusterId,
  })
}

export async function fetchAudit(query: AuditQuery = {}): Promise<AuditPage> {
  const params: Record<string, string> = {}
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === '' || value === false) continue
    // A verb filter is a set as often as it is one value; the API reads both a
    // comma-separated list and repeated parameters, and a comma keeps the URL
    // short enough to still be a link somebody pastes into a ticket.
    if (Array.isArray(value)) {
      if (value.length === 0) continue
      params[key] = value.join(',')
      continue
    }
    params[key] = String(value)
  }
  const { data } = await http.get<AuditPage>('/audit', { params })
  return {
    events: data.events ?? [],
    total: data.total ?? 0,
    limit: data.limit ?? 0,
    offset: data.offset ?? 0,
    scoped_to_self: data.scoped_to_self ?? false,
  }
}

export async function fetchAuditSummary(): Promise<AuditSummary> {
  const { data } = await http.get<AuditSummary>('/audit/summary')
  return data
}

export async function fetchTerminalSessions(
  query: TerminalSessionQuery = {},
): Promise<TerminalSessionPage> {
  const params: Record<string, string> = {}
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === '' || value === false) continue
    params[key] = String(value)
  }
  const { data } = await http.get<TerminalSessionPage>('/audit/terminal-sessions', { params })
  return {
    sessions: data.sessions ?? [],
    total: data.total ?? 0,
    limit: data.limit ?? 0,
    offset: data.offset ?? 0,
    recording_enabled: data.recording_enabled ?? false,
    scoped_to_self: data.scoped_to_self ?? false,
  }
}

/**
 * What this server records. It is a policy read, not a data read: it says whether
 * recording is on, whether keystrokes are part of it, whether files are encrypted
 * at rest and how long they are kept — which is what an operator is owed before
 * typing into a shell that is being captured.
 */
export async function fetchRecordingPolicy(): Promise<RecordingPolicy> {
  const { data } = await http.get<RecordingPolicy>('/audit/recording-policy')
  return data
}

export async function fetchTerminalSession(id: number): Promise<TerminalSession> {
  const { data } = await http.get<TerminalSession>(`/audit/terminal-sessions/${id}`)
  return data
}

/**
 * The recording itself, as an asciinema v2 stream: a header line followed by one
 * event per line. It comes back as text and is left untransformed — axios would
 * otherwise try to parse the first line as JSON and hand back an object.
 */
export async function fetchTerminalSessionCast(id: number): Promise<string> {
  const { data } = await http.get<string>(`/audit/terminal-sessions/${id}/stream`, {
    responseType: 'text',
    transformResponse: (raw: string) => raw,
  })
  return data
}

export async function deleteTerminalSession(id: number): Promise<void> {
  await http.delete(`/audit/terminal-sessions/${id}`)
}

export async function fetchNamespaces(
  clusterId: number,
): Promise<{ namespaces: Namespace[]; scoped: boolean }> {
  const { data } = await http.get<{ namespaces: Namespace[]; scoped: boolean }>(
    `/clusters/${clusterId}/resources/namespaces`,
  )
  return { namespaces: data.namespaces ?? [], scoped: data.scoped ?? false }
}

export function fetchWorkloads(clusterId: number, namespace: string): Promise<Workload[]> {
  return fetchList<Workload>(clusterId, 'workloads', 'workloads', namespace)
}

export function fetchPods(clusterId: number, namespace: string): Promise<Pod[]> {
  return fetchList<Pod>(clusterId, 'pods', 'pods', namespace)
}

/*
 * The inventory reads. Each one is a normalised list from the backend, read live
 * through the agent tunnel under the caller's own identity — a scoped grant gets
 * a refusal here exactly as it would from kubectl.
 */

/** resourceURL builds a path onto a cluster's on-demand resource surface. */
function resourceURL(clusterId: number, resource: string): string {
  return `/clusters/${clusterId}/resources/${resource}`
}

/**
 * scopeParams turns a namespace selection into query parameters. The
 * all-namespaces sentinel becomes a flag rather than a namespace, because the
 * backend resolves it against the caller's grant.
 */
function scopeParams(namespace: string | undefined): Record<string, string> | undefined {
  if (!namespace) return undefined
  if (namespace === ALL_NAMESPACES) return { all_namespaces: 'true' }
  return { namespace }
}

async function fetchList<T>(
  clusterId: number,
  resource: string,
  key: string,
  namespace?: string,
): Promise<T[]> {
  const { data } = await http.get<Record<string, T[]>>(resourceURL(clusterId, resource), {
    params: scopeParams(namespace),
  })
  return data[key] ?? []
}

/**
 * fetchOptionalList reads a CRD-backed list. A cluster without the CRD installed
 * answers with an empty list and says so, which the UI reports rather than
 * showing as a failure.
 */
async function fetchOptionalList<T>(
  clusterId: number,
  resource: string,
  key: string,
  namespace: string,
): Promise<OptionalList<T>> {
  const { data } = await http.get<Record<string, unknown>>(resourceURL(clusterId, resource), {
    params: scopeParams(namespace),
  })
  return {
    items: (data[key] as T[] | undefined) ?? [],
    available: data.available !== false,
    reason: data.reason as string | undefined,
  }
}

/**
 * How many objects of each kind the cluster holds.
 *
 * One request for every kind the sidebar wants a number for, because a nav
 * count must not cost what opening the list costs: the server reads each at
 * `limit=1`, so the price is flat in the size of the cluster rather than
 * proportional to it, and batching keeps a column of thirty numbers to one round
 * trip and one cache entry.
 *
 * A kind the caller may not list, or one this cluster does not serve, comes back
 * unavailable with a reason rather than as a failure — the sidebar simply shows
 * no number there, which is the honest reading of "we cannot know".
 */
export async function fetchResourceCounts(
  clusterId: number,
  keys: string[],
  namespace: string,
): Promise<Record<string, ResourceCount>> {
  const { data } = await http.get<{ counts?: Record<string, ResourceCount> }>(
    resourceURL(clusterId, 'counts'),
    { params: { ...scopeParams(namespace), keys: keys.join(',') } },
  )
  return data.counts ?? {}
}

export function fetchDeployments(clusterId: number, namespace: string): Promise<Workload[]> {
  return fetchList<Workload>(clusterId, 'deployments', 'workloads', namespace)
}

export function fetchStatefulSets(clusterId: number, namespace: string): Promise<Workload[]> {
  return fetchList<Workload>(clusterId, 'statefulsets', 'workloads', namespace)
}

export function fetchDaemonSets(clusterId: number, namespace: string): Promise<Workload[]> {
  return fetchList<Workload>(clusterId, 'daemonsets', 'workloads', namespace)
}

export function fetchJobs(clusterId: number, namespace: string): Promise<Job[]> {
  return fetchList<Job>(clusterId, 'jobs', 'jobs', namespace)
}

export function fetchCronJobs(clusterId: number, namespace: string): Promise<CronJob[]> {
  return fetchList<CronJob>(clusterId, 'cronjobs', 'cronjobs', namespace)
}

export function fetchServices(clusterId: number, namespace: string): Promise<Service[]> {
  return fetchList<Service>(clusterId, 'services', 'services', namespace)
}

export function fetchIngresses(clusterId: number, namespace: string): Promise<Ingress[]> {
  return fetchList<Ingress>(clusterId, 'ingresses', 'ingresses', namespace)
}

export function fetchNetworkPolicies(
  clusterId: number,
  namespace: string,
): Promise<NetworkPolicy[]> {
  return fetchList<NetworkPolicy>(clusterId, 'networkpolicies', 'networkpolicies', namespace)
}

/**
 * A workload's own view of the policies that select it. It is read on demand
 * from the detail drawer's tab rather than with the list, on the same rule
 * every other per-object read here follows: the reachability question is about
 * one object, so it is asked about one object.
 */
export async function fetchNetworkPolicyReachability(
  clusterId: number,
  kind: string,
  name: string,
  namespace: string,
): Promise<NetworkPolicyReachability> {
  const { data } = await http.get<NetworkPolicyReachability>(
    resourceURL(clusterId, 'networkpolicies/reachability'),
    { params: { kind, name, namespace } },
  )
  return data
}

/** The namespace-level summary of what is and is not covered. */
export async function fetchNetworkPolicyCoverage(
  clusterId: number,
  namespace: string,
): Promise<NetworkPolicyCoverage> {
  const { data } = await http.get<NetworkPolicyCoverage>(
    resourceURL(clusterId, 'networkpolicies/coverage'),
    { params: { namespace } },
  )
  return data
}

/**
 * Workload security posture — the seven fixed rules over fields Explore's own
 * lists already fetch. Scoped exactly like every other resource list: one
 * namespace, or every namespace the caller's grant covers.
 */
export async function fetchClusterPosture(
  clusterId: number,
  namespace: string,
): Promise<PostureScan> {
  const { data } = await http.get<PostureScan>(resourceURL(clusterId, 'posture'), {
    params: scopeParams(namespace),
  })
  return data
}

/**
 * Marking a finding as reviewed and accepted. It stays in the next scan,
 * marked — see PostureFinding.acknowledged — rather than disappearing, and a
 * reason is required by the server for the same audit-ability reason.
 */
export async function acknowledgePostureFinding(
  clusterId: number,
  finding: { kind: string; namespace?: string; name: string; rule: PostureRule },
  reason: string,
): Promise<PostureAcknowledgement> {
  const { data } = await http.post<PostureAcknowledgement>(resourceURL(clusterId, 'posture/ack'), {
    kind: finding.kind,
    namespace: finding.namespace ?? '',
    name: finding.name,
    rule: finding.rule,
    reason,
  })
  return data
}

/** Reversing an acknowledgement, so the finding reads as unreviewed again. */
export async function unacknowledgePostureFinding(
  clusterId: number,
  finding: { kind: string; namespace?: string; name: string; rule: PostureRule },
): Promise<void> {
  await http.delete(resourceURL(clusterId, 'posture/ack'), {
    params: {
      kind: finding.kind,
      namespace: finding.namespace ?? '',
      name: finding.name,
      rule: finding.rule,
    },
  })
}

export function fetchHTTPRoutes(
  clusterId: number,
  namespace: string,
): Promise<OptionalList<Route>> {
  return fetchOptionalList<Route>(clusterId, 'httproutes', 'httproutes', namespace)
}

export function fetchVirtualServices(
  clusterId: number,
  namespace: string,
): Promise<OptionalList<Route>> {
  return fetchOptionalList<Route>(clusterId, 'virtualservices', 'virtualservices', namespace)
}

export function fetchPersistentVolumes(clusterId: number): Promise<PersistentVolume[]> {
  return fetchList<PersistentVolume>(clusterId, 'persistentvolumes', 'persistentvolumes')
}

export function fetchPersistentVolumeClaims(
  clusterId: number,
  namespace: string,
): Promise<PersistentVolumeClaim[]> {
  return fetchList<PersistentVolumeClaim>(
    clusterId,
    'persistentvolumeclaims',
    'persistentvolumeclaims',
    namespace,
  )
}

export function fetchStorageClasses(clusterId: number): Promise<StorageClass[]> {
  return fetchList<StorageClass>(clusterId, 'storageclasses', 'storageclasses')
}

export function fetchConfigMaps(clusterId: number, namespace: string): Promise<ConfigEntry[]> {
  return fetchList<ConfigEntry>(clusterId, 'configmaps', 'configmaps', namespace)
}

export function fetchSecrets(clusterId: number, namespace: string): Promise<ConfigEntry[]> {
  return fetchList<ConfigEntry>(clusterId, 'secrets', 'secrets', namespace)
}

export function fetchCRDs(clusterId: number): Promise<CustomResourceDefinition[]> {
  return fetchList<CustomResourceDefinition>(clusterId, 'crds', 'crds')
}

export function fetchNodes(clusterId: number): Promise<ClusterNode[]> {
  return fetchList<ClusterNode>(clusterId, 'nodes', 'nodes')
}

/*
 * The cluster's own RBAC. Read like every other list — impersonated, scoped and
 * audited — which is also why a `view` grant sees exactly as much of it as
 * `kubectl get roles` would show that same identity.
 */

export function fetchRoles(clusterId: number, namespace: string): Promise<ClusterRoleEntry[]> {
  return fetchList<ClusterRoleEntry>(clusterId, 'roles', 'roles', namespace)
}

export function fetchClusterRoles(clusterId: number): Promise<ClusterRoleEntry[]> {
  return fetchList<ClusterRoleEntry>(clusterId, 'clusterroles', 'clusterroles')
}

export function fetchRoleBindings(
  clusterId: number,
  namespace: string,
): Promise<RoleBindingEntry[]> {
  return fetchList<RoleBindingEntry>(clusterId, 'rolebindings', 'rolebindings', namespace)
}

export function fetchClusterRoleBindings(clusterId: number): Promise<RoleBindingEntry[]> {
  return fetchList<RoleBindingEntry>(clusterId, 'clusterrolebindings', 'clusterrolebindings')
}

export function fetchServiceAccounts(
  clusterId: number,
  namespace: string,
): Promise<ServiceAccountEntry[]> {
  return fetchList<ServiceAccountEntry>(clusterId, 'serviceaccounts', 'serviceaccounts', namespace)
}

/**
 * Asks the cluster's authorizer about a named subject. It is a POST because a
 * SubjectAccessReview is a `create` in RBAC's eyes even though it changes
 * nothing — so a caller whose grant does not carry it is refused *by the
 * cluster*, and that refusal is the answer rather than a broken page.
 */
export async function askAccessReview(
  clusterId: number,
  question: AccessReviewQuestion,
): Promise<AccessReviewResult> {
  const { data } = await http.post<AccessReviewResult>(
    resourceURL(clusterId, 'access-review'),
    question,
  )
  return data
}

/** The verbs the server will accept, so the form cannot offer one it refuses. */
export async function fetchAccessReviewVerbs(clusterId: number): Promise<string[]> {
  const { data } = await http.get<{ verbs: string[] }>(
    resourceURL(clusterId, 'access-review/verbs'),
  )
  return data.verbs ?? []
}

/**
 * Who KubeMG impersonates for you on this cluster. It is what makes the review
 * answer the question people actually open it with — not "what may some user
 * do", but "what is my own grant worth here".
 */
export async function fetchGrantIdentity(clusterId: number): Promise<GrantIdentity> {
  const { data } = await http.get<GrantIdentity>(
    resourceURL(clusterId, 'access-review/identity'),
  )
  return data
}

/**
 * The cluster-wide events timeline. Narrowing to one object is what the pilot
 * header's alerts link into — the two components are validated server-side
 * rather than escaped, since they end up in a `fieldSelector`.
 *
 * A refusal comes back as `events_available: false` with the cluster's own
 * reason rather than as a thrown error: events are their own resource with
 * their own RBAC, and the page says so instead of failing.
 */
export async function fetchClusterEvents(
  clusterId: number,
  namespace: string,
  options: {
    type?: 'Warning' | 'Normal'
    kind?: string
    name?: string
    /**
     * The preset id, resolved against the *server's* clock like every other
     * ranged surface — the browser never computes the boundary, so "the last
     * fifteen minutes" names the same instant here as in the audit trail.
     */
    range?: TimeRangeId
  } = {},
): Promise<EventTimeline> {
  const { data } = await http.get<EventTimeline>(resourceURL(clusterId, 'events'), {
    params: {
      ...scopeParams(namespace),
      ...(options.type ? { type: options.type } : {}),
      ...(options.kind ? { kind: options.kind } : {}),
      ...(options.name ? { name: options.name } : {}),
      ...(options.range ? { range: options.range } : {}),
    },
  })
  return {
    ...data,
    groups: data.groups ?? [],
    events_available: data.events_available !== false,
    total_groups: data.total_groups ?? 0,
  }
}

/**
 * fetchCustomResources reads a list served by one of the cluster's own CRDs. The
 * API is named rather than picked from a table — that is the whole point, since
 * which CRDs exist is discovered per cluster — but the backend builds the path
 * from the three components rather than taking one, and the read is impersonated
 * and audited like every other. A CRD uninstalled since the sidebar was built
 * comes back unavailable rather than as a failure.
 */
export function fetchCustomResources(
  clusterId: number,
  ref: CustomResourceRef,
  namespace: string,
): Promise<OptionalList<CustomResource>> {
  const scope =
    ref.scope === 'cluster' ? { scope: 'cluster' } : scopeParams(namespace)
  return http
    .get<Record<string, unknown>>(resourceURL(clusterId, 'custom'), {
      params: { group: ref.group, version: ref.version, plural: ref.plural, ...scope },
    })
    .then(({ data }) => ({
      items: (data.items as CustomResource[] | undefined) ?? [],
      available: data.available !== false,
      reason: data.reason as string | undefined,
    }))
}

/*
 * Helm releases. Helm keeps a release as a labelled Secret and nothing else, so
 * these are the same impersonated reads as everything above — the cluster's RBAC
 * decides, and a grant that may not read Secrets is refused here, which is the
 * right answer rather than a bug.
 */

export function fetchHelmReleases(clusterId: number, namespace: string): Promise<HelmRelease[]> {
  return fetchList<HelmRelease>(clusterId, 'helm/releases', 'releases', namespace)
}

/** helmValuesURL addresses one release's values. */
function helmValuesURL(clusterId: number, name: string): string {
  return `${resourceURL(clusterId, 'helm/releases')}/${encodeURIComponent(name)}/values`
}

export async function fetchHelmValues(
  clusterId: number,
  name: string,
  namespace: string,
): Promise<HelmValues> {
  const { data } = await http.get<HelmValues>(helmValuesURL(clusterId, name), {
    params: { namespace },
  })
  return data
}

/**
 * updateHelmValues appends a Helm revision carrying the new values. It records
 * what the next `helm upgrade` starts from; it does not re-render the chart, so
 * nothing running changes — the response carries that warning and the drawer
 * shows it.
 */
export async function updateHelmValues(
  clusterId: number,
  name: string,
  namespace: string,
  yaml: string,
): Promise<HelmValues> {
  const { data } = await http.put<HelmValues>(
    helmValuesURL(clusterId, name),
    { yaml },
    { params: { namespace } },
  )
  return data
}

/** helmReleaseURL addresses one release, for the calls below its name. */
function helmReleaseURL(clusterId: number, name: string): string {
  return `${resourceURL(clusterId, 'helm/releases')}/${encodeURIComponent(name)}`
}

/** fetchHelmHistory reads every revision Helm has stored for one release. */
export async function fetchHelmHistory(
  clusterId: number,
  name: string,
  namespace: string,
): Promise<HelmHistory> {
  const { data } = await http.get<HelmHistory>(`${helmReleaseURL(clusterId, name)}/history`, {
    params: { namespace },
  })
  return data
}

/**
 * rollbackHelmRelease restores an earlier revision's values as a new revision.
 *
 * It is deliberately less than `helm rollback`, and the difference is the thing
 * to know before calling it: the values come back, and the chart, the rendered
 * manifest and everything running do not — KubeMG has no chart to render and
 * applying a stored manifest would mean reimplementing Helm's three-way merge.
 * The next `helm upgrade` renders from these values and converges. The response
 * carries that caveat, and so does the history read that offers the action.
 */
export async function rollbackHelmRelease(
  clusterId: number,
  name: string,
  namespace: string,
  revision: number,
): Promise<HelmValues> {
  const { data } = await http.post<HelmValues>(
    `${helmReleaseURL(clusterId, name)}/rollback`,
    { revision },
    { params: { namespace } },
  )
  return data
}

/*
 * One object as YAML. Both calls address the object by the same key the Explore
 * sidebar uses, so the browser never names an API path — the backend builds it
 * from a fixed table, and the write goes down the impersonated tunnel like every
 * other call.
 */

/**
 * fetchResourceDescribe reads one object plus the events the cluster recorded
 * against it. It addresses the object by the same key as the manifest calls, so
 * the browser still never names an API path.
 */
export async function fetchResourceDescribe(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace?: string,
): Promise<ResourceDescribeResult> {
  const { data } = await http.get<ResourceDescribeResult>(resourceURL(clusterId, 'describe'), {
    params: { kind, name, namespace: namespace || undefined },
  })
  return data
}

export async function fetchResourceYaml(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace?: string,
): Promise<ResourceManifest> {
  const { data } = await http.get<ResourceManifest>(resourceURL(clusterId, 'object'), {
    params: { kind, name, namespace: namespace || undefined },
  })
  return data
}

export async function updateResourceYaml(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string | undefined,
  yaml: string,
): Promise<ResourceManifest> {
  const { data } = await http.put<ResourceManifest>(
    resourceURL(clusterId, 'object'),
    { yaml },
    { params: { kind, name, namespace: namespace || undefined } },
  )
  return data
}

/**
 * createResourceObject posts a manifest to the collection its kind is served
 * by — `kubectl create -f`, addressed by the same sidebar key as every other
 * resource call, so the browser still never names an API path. There is no
 * name in the address because there is no object yet: the manifest declares it,
 * or carries a `generateName` and lets the cluster decide.
 *
 * The namespace *is* in the address, and is the only place it belongs: it is
 * checked against the caller's grant exactly as a read's is, and a manifest that
 * names a different one is refused rather than quietly redirected.
 */
export async function createResourceObject(
  clusterId: number,
  kind: ResourceKey,
  namespace: string | undefined,
  yaml: string,
): Promise<ResourceManifest> {
  const { data } = await http.post<ResourceManifest>(
    resourceURL(clusterId, 'object'),
    { yaml },
    { params: { kind, namespace: namespace || undefined } },
  )
  return data
}

/**
 * previewResourceObjectDiff answers what a manifest would change against the
 * cluster's current object, without writing anything. It is a fresh read
 * every time — not the object the editor opened on — because the confirmation
 * step it powers is asking about the cluster as it stands right now, not as
 * it stood when the tab was opened.
 */
export async function previewResourceObjectDiff(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string | undefined,
  yaml: string,
): Promise<ManifestDiff> {
  const { data } = await http.post<ManifestDiff>(
    `${resourceURL(clusterId, 'object')}/diff`,
    { yaml },
    { params: { kind, name, namespace: namespace || undefined } },
  )
  return data
}

/*
 * The two workload writes that are not worth opening a manifest for. They name
 * the object the same way every other resource call does — by the sidebar's own
 * key — so the browser still never names an API path, and the backend still
 * builds one from its fixed table.
 */

/**
 * scaleWorkload sets a replica count. Zero is a real answer: it is how a
 * workload is stopped without being deleted.
 */
export async function scaleWorkload(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string | undefined,
  replicas: number,
): Promise<WorkloadActionResult> {
  const { data } = await http.post<WorkloadActionResult>(resourceURL(clusterId, 'scale'), {
    kind,
    name,
    namespace,
    replicas,
  })
  return data
}

/**
 * suspendWorkload turns a CronJob's schedule off, or back on. It is the one
 * control a CronJob has: it owns Jobs rather than pods, so there is nothing to
 * scale and nothing to roll, and deleting it to stop tonight's run would lose
 * the object.
 */
export async function suspendWorkload(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string | undefined,
  suspend: boolean,
): Promise<WorkloadActionResult> {
  const { data } = await http.post<WorkloadActionResult>(resourceURL(clusterId, 'suspend'), {
    kind,
    name,
    namespace,
    suspend,
  })
  return data
}

/**
 * deleteResourceObject removes one object. It is addressed exactly like reading
 * or writing that object — the sidebar's own kind key, a name, a namespace — and
 * there is no bulk route on purpose: a selection of eight is eight calls, so
 * each one is its own audit record and a partial failure is a real answer rather
 * than a shape a single call would have to invent.
 */
export async function deleteResourceObject(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string | undefined,
): Promise<DeleteResult> {
  const { data } = await http.delete<DeleteResult>(resourceURL(clusterId, 'object'), {
    params: { kind, name, namespace: namespace || undefined },
  })
  return data
}

/**
 * restartWorkload rolls a workload's pods by stamping its pod template, the same
 * way `kubectl rollout restart` does. Nothing about the workload's spec changes.
 */
export async function restartWorkload(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string | undefined,
): Promise<WorkloadActionResult> {
  const { data } = await http.post<WorkloadActionResult>(resourceURL(clusterId, 'restart'), {
    kind,
    name,
    namespace,
  })
  return data
}

/**
 * The pods a workload owns. The browser never sends a label selector — it names
 * the workload the same way every other resource call does, and the backend
 * derives the selector from the object.
 */
export async function fetchWorkloadPods(
  clusterId: number,
  kind: ResourceKey,
  name: string,
  namespace: string,
): Promise<WorkloadPods> {
  const { data } = await http.get<WorkloadPods>(resourceURL(clusterId, 'workload/pods'), {
    params: { kind, name, namespace },
  })
  return {
    pods: data.pods ?? [],
    namespace: data.namespace ?? namespace,
    kind: data.kind ?? '',
    selector: data.selector ?? '',
    containers: data.containers ?? [],
    truncated: data.truncated ?? false,
  }
}

export async function fetchPodLogs(
  clusterId: number,
  namespace: string,
  pod: string,
  container: string,
  tail = 200,
): Promise<string> {
  const { data } = await http.get<{ log: string }>(
    `/clusters/${clusterId}/resources/pods/${encodeURIComponent(pod)}/logs`,
    { params: { namespace, container: container || undefined, tail } },
  )
  return data.log ?? ''
}

/*
 * Live utilisation. A cluster without metrics-server is not an error case: the
 * response says it does not serve the Metrics API and the UI says so too, so
 * these never reject for a missing add-on.
 */

export async function fetchNodeMetrics(clusterId: number): Promise<NodeMetrics> {
  const { data } = await http.get<NodeMetrics>(`/clusters/${clusterId}/metrics/nodes`)
  return {
    available: data.available ?? false,
    reason: data.reason,
    nodes: data.nodes ?? [],
    summary: data.summary ?? EMPTY_USAGE,
  }
}

/**
 * Usage for a whole list, read with the same scope the pod list itself was read
 * with so the two line up row for row. A grant that cannot read metrics.k8s.io
 * is refused here and nowhere else, which is why the caller treats this as an
 * annotation on the list rather than as part of loading it.
 */
export async function fetchPodListMetrics(
  clusterId: number,
  namespace: string,
): Promise<PodListMetrics> {
  const { data } = await http.get<PodListMetrics>(`/clusters/${clusterId}/metrics/pods`, {
    params: scopeParams(namespace),
  })
  return { available: data.available ?? false, reason: data.reason, pods: data.pods ?? [] }
}

export async function fetchPodMetrics(
  clusterId: number,
  namespace: string,
  pod: string,
): Promise<PodMetrics> {
  const { data } = await http.get<PodMetrics>(
    `/clusters/${clusterId}/metrics/pods/${encodeURIComponent(pod)}`,
    { params: { namespace } },
  )
  return { available: data.available ?? false, reason: data.reason, pod: data.pod ?? null }
}

const EMPTY_USAGE = {
  nodes: 0,
  cpu_millicores: 0,
  cpu_capacity_millicores: 0,
  cpu_percent: 0,
  memory_bytes: 0,
  memory_capacity_bytes: 0,
  memory_percent: 0,
}

const EMPTY_DIMENSION = {
  allocatable: 0,
  requested: 0,
  limited: 0,
  used: 0,
  requested_percent: 0,
  limited_percent: 0,
  used_percent: 0,
  unlimited_containers: 0,
}

const EMPTY_CAPACITY_SUMMARY = {
  nodes: 0,
  ready: 0,
  schedulable: 0,
  cpu: EMPTY_DIMENSION,
  memory: EMPTY_DIMENSION,
  pods: { allocatable: 0, scheduled: 0, percent: 0, without_requests: 0 },
  severity_counts: {},
}

/**
 * Allocation against capacity, per node. Unlike the metrics reads above this
 * one is whole without metrics-server — `available` marks the missing live
 * column, not a missing answer — so a failure here is a real failure.
 */
export async function fetchClusterCapacity(clusterId: number): Promise<ClusterCapacity> {
  const { data } = await http.get<ClusterCapacity>(`/clusters/${clusterId}/metrics/capacity`)
  return {
    available: data.available ?? false,
    reason: data.reason,
    nodes: data.nodes ?? [],
    summary: data.summary ?? EMPTY_CAPACITY_SUMMARY,
    unscheduled: data.unscheduled ?? [],
    unscheduled_pods: data.unscheduled_pods ?? 0,
  }
}

/**
 * proxyURL builds an absolute URL onto a cluster's kubectl proxy. Streaming
 * calls (logs -f, watch, exec) bypass axios and go straight to fetch or a
 * WebSocket, so they need the URL and the token in hand.
 */
export function proxyURL(clusterId: number, path: string, protocol: 'http' | 'ws' = 'http'): string {
  // fetch and WebSocket both need an absolute URL, so a same-origin baseURL is
  // resolved against the page — which is also what makes wss: follow https:.
  const origin = apiOrigin || window.location.origin
  const base = `${origin}/api/v1/clusters/${clusterId}/proxy${path.startsWith('/') ? path : `/${path}`}`
  if (protocol === 'ws') {
    return base.replace(/^http/, 'ws')
  }
  return base
}

export async function generateKubeconfig(
  clusterId: number,
  ttlSeconds: number,
  namespace: string,
): Promise<Kubeconfig> {
  const { data } = await http.post<Kubeconfig>(`/clusters/${clusterId}/kubeconfig/generate`, {
    ttl_seconds: ttlSeconds,
    namespace: namespace || undefined,
  })
  return data
}

/** Which release this is, for the footer. Read once per session and held: the
    version of a running process does not change under it, so a hook that
    re-read it on every mount would be a request per navigation for an answer
    that cannot have moved. */
let versionRead: Promise<ServerVersion> | null = null

export function fetchServerVersion(): Promise<ServerVersion> {
  versionRead ??= http
    .get<ServerVersion>('/version')
    .then((response) => response.data)
    .catch((error: unknown) => {
      // A failed read must not be the answer forever — the next mount asks
      // again, which is what makes signing in after a cold console work.
      versionRead = null
      throw error
    })
  return versionRead
}

/** The window a kubeconfig may be issued for. Not cached: it is read once when
    the sheet opens, and an admin who has just raised the ceiling should see it. */
export async function fetchKubeconfigPolicy(): Promise<KubeconfigPolicy> {
  const { data } = await http.get<KubeconfigPolicy>('/kubeconfig/policy')
  return data
}

export async function fetchSettings(): Promise<SettingsResponse> {
  const { data } = await http.get<SettingsResponse>('/settings')
  return { ...data, warnings: data.warnings ?? [] }
}

export async function updateSettings(patch: SettingsPatch): Promise<SettingsResponse> {
  const { data } = await http.put<SettingsResponse>('/settings', patch)
  return { ...data, warnings: data.warnings ?? [] }
}

/** What this install was started with. Read-only, because every value in it was
    settled at boot from an environment no request can rewrite — the settings
    pages next to it are where the writable ones live. */
export async function fetchDeploymentPosture(): Promise<DeploymentPosture> {
  const { data } = await http.get<DeploymentPosture>('/settings/deployment')
  return { checks: data?.checks ?? [], attention: data?.attention ?? 0 }
}

/* --------------------------------------------------------------- setup --- */

/** Whether this server still needs first-run setup.
 *
 * Called before there is a session, so a failure has to mean something: it
 * resolves to "already set up". That is the safe direction — the wizard
 * overrides the whole console, and a server that cannot be reached must not
 * drop an established install back into it. */
export async function fetchSetupState(): Promise<SetupState> {
  try {
    const { data } = await http.get<SetupState>('/setup/state')
    return { required: Boolean(data?.required) }
  } catch {
    return { required: false }
  }
}

export async function fetchSetupPreflight(): Promise<SetupPreflight> {
  const { data } = await http.get<SetupPreflight>('/setup/preflight')
  return {
    admin_password_pristine: Boolean(data?.admin_password_pristine),
    checks: data?.checks ?? [],
    warnings: data?.warnings ?? [],
  }
}

export async function completeSetup(): Promise<SetupState> {
  const { data } = await http.post<SetupState>('/setup/complete')
  return { required: Boolean(data?.required) }
}

/* -------------------------------------------------------------- alarms --- */

export async function fetchAlarmChannels(): Promise<AlarmChannelList> {
  const { data } = await http.get<AlarmChannelList>('/alarms/channels')
  return { channels: data.channels ?? [], kinds: data.kinds ?? [] }
}

export async function createAlarmChannel(input: AlarmChannelInput): Promise<AlarmChannel> {
  const { data } = await http.post<AlarmChannel>('/alarms/channels', input)
  return data
}

export async function updateAlarmChannel(
  id: number,
  input: AlarmChannelInput,
): Promise<AlarmChannel> {
  const { data } = await http.put<AlarmChannel>(`/alarms/channels/${id}`, input)
  return data
}

export async function deleteAlarmChannel(id: number): Promise<void> {
  await http.delete(`/alarms/channels/${id}`)
}

/** testAlarmChannel asks whether the endpoint accepts KubeMG's payload. A
    refusal comes back as `ok: false` with the endpoint's own words rather than
    as a thrown error — the operator asked a question and that is the answer. */
export async function testAlarmChannel(id: number): Promise<AlarmChannelTest> {
  const { data } = await http.post<AlarmChannelTest>(`/alarms/channels/${id}/test`)
  return data
}

export async function fetchAlarmRules(): Promise<AlarmRuleList> {
  const { data } = await http.get<AlarmRuleList>('/alarms/rules')
  return {
    rules: data.rules ?? [],
    triggers: data.triggers ?? [],
    severities: data.severities ?? [],
    suggested_reasons: data.suggested_reasons ?? [],
    cluster_events_available: data.cluster_events_available ?? false,
    dispatcher_running: data.dispatcher_running ?? false,
  }
}

export async function createAlarmRule(input: AlarmRuleInput): Promise<AlarmRule> {
  const { data } = await http.post<AlarmRule>('/alarms/rules', input)
  return data
}

export async function updateAlarmRule(id: number, input: AlarmRuleInput): Promise<AlarmRule> {
  const { data } = await http.put<AlarmRule>(`/alarms/rules/${id}`, input)
  return data
}

export async function deleteAlarmRule(id: number): Promise<void> {
  await http.delete(`/alarms/rules/${id}`)
}

/* -------------------------------------------------- command guardrails --- */

export async function fetchGuardrailPolicies(clusterID?: number): Promise<GuardrailPolicyList> {
  // 0 is a real scope — the fleet-wide rules — so the parameter is sent whenever
  // one was named, and omitted only when nothing was.
  const params = clusterID === undefined ? undefined : { cluster_id: clusterID }
  const { data } = await http.get<GuardrailPolicyList>('/guardrails', { params })
  return {
    policies: data.policies ?? [],
    targets: data.targets ?? [],
    actions: data.actions ?? [],
    enforcing: data.enforcing ?? 0,
  }
}

export async function fetchGuardrailTemplates(): Promise<GuardrailTemplate[]> {
  const { data } = await http.get<{ templates: GuardrailTemplate[] }>('/guardrails/templates')
  return data.templates ?? []
}

export async function createGuardrailPolicy(
  input: GuardrailPolicyInput,
): Promise<GuardrailPolicy> {
  const { data } = await http.post<GuardrailPolicy>('/guardrails', input)
  return data
}

export async function updateGuardrailPolicy(
  id: number,
  input: GuardrailPolicyInput,
): Promise<GuardrailPolicy> {
  const { data } = await http.put<GuardrailPolicy>(`/guardrails/${id}`, input)
  return data
}

export async function deleteGuardrailPolicy(id: number): Promise<void> {
  await http.delete(`/guardrails/${id}`)
}

/* ------------------------------------------------------- observability --- */

/** fetchObservability returns the datasources registered for a cluster. */
export async function fetchObservability(clusterId: number): Promise<ObservabilityResponse> {
  const { data } = await http.get<ObservabilityResponse>(`/clusters/${clusterId}/observability`)
  return { ...data, sources: data.sources ?? [] }
}

export async function saveDatasource(
  clusterId: number,
  kind: DatasourceKind,
  input: DatasourceInput,
): Promise<{ source: ObservabilitySource; check: DatasourceCheck }> {
  const { data } = await http.put<{ source: ObservabilitySource; check: DatasourceCheck }>(
    `/clusters/${clusterId}/observability/sources/${kind}`,
    input,
  )
  return data
}

export async function deleteDatasource(clusterId: number, kind: DatasourceKind): Promise<void> {
  await http.delete(`/clusters/${clusterId}/observability/sources/${kind}`)
}

/** testDatasource checks a draft that has not been saved yet. */
export async function testDatasource(
  clusterId: number,
  kind: DatasourceKind,
  input: DatasourceInput,
): Promise<DatasourceCheck> {
  const { data } = await http.post<DatasourceCheck>(
    `/clusters/${clusterId}/observability/sources/${kind}/test`,
    input,
  )
  return data
}

/** checkDatasource re-checks the stored source and records the verdict. */
export async function checkDatasource(
  clusterId: number,
  kind: DatasourceKind,
): Promise<{ source: ObservabilitySource; check: DatasourceCheck }> {
  const { data } = await http.post<{ source: ObservabilitySource; check: DatasourceCheck }>(
    `/clusters/${clusterId}/observability/sources/${kind}/check`,
  )
  return data
}

/** discoverDatasources looks for a backend that is already running in-cluster. */
export async function discoverDatasources(clusterId: number): Promise<DatasourceCandidate[]> {
  const { data } = await http.get<{ candidates: DatasourceCandidate[] }>(
    `/clusters/${clusterId}/observability/discover`,
  )
  return data.candidates ?? []
}

/*
 * The other consoles a cluster is operated from. Reading them follows the
 * datasource rule — anyone the cluster is granted to can see where its Grafana
 * is, since you cannot be sent to look at a dashboard in a place you are not
 * allowed to know exists — and registering one is administrative.
 */
export async function fetchClusterConsoles(clusterId: number): Promise<ClusterConsolesResponse> {
  const { data } = await http.get<ClusterConsolesResponse>(`/clusters/${clusterId}/consoles`)
  return data
}

export async function saveClusterConsole(
  clusterId: number,
  kind: ConsoleKind,
  input: ConsoleInput,
): Promise<ClusterConsole> {
  const { data } = await http.put<{ console: ClusterConsole }>(
    `/clusters/${clusterId}/consoles/${kind}`,
    input,
  )
  return data.console
}

/*
 * Which custom resources this cluster's sidebar offers. Reading is as wide as
 * the cluster is granted — a developer has to be able to tell "no Istio here"
 * from "somebody took Istio off the list" — and writing is administrative.
 *
 * The write is the whole set, because that is the shape of the decision: an
 * administrator looks at what the cluster serves and says which of it is worth
 * showing.
 */

export async function fetchCRDVisibility(clusterId: number): Promise<CRDVisibility> {
  const { data } = await http.get<CRDVisibility>(`/clusters/${clusterId}/crd-visibility`)
  return data
}

export async function saveCRDVisibility(
  clusterId: number,
  hidden: string[],
): Promise<CRDVisibility> {
  const { data } = await http.put<CRDVisibility>(`/clusters/${clusterId}/crd-visibility`, {
    hidden,
  })
  return data
}

export async function deleteClusterConsole(clusterId: number, kind: ConsoleKind): Promise<void> {
  await http.delete(`/clusters/${clusterId}/consoles/${kind}`)
}

/*
 * The query path. Note what is *not* here: a query parameter. The browser names
 * a chart from the server's catalogue and the Kubernetes names to narrow it to,
 * and the server writes the PromQL around the caller's own namespace scope —
 * because a metrics backend has never heard of the caller and would answer
 * anything it was asked.
 */

/**
 * MetricQueryOptions narrows a chart. An absent range is the server's default.
 *
 * `range` is a preset the *server* resolves; `start`/`end` are explicit
 * boundaries and beat it. The browser deliberately does not turn a preset into
 * a pair of instants — see `lib/timerange.ts`.
 */
export interface MetricQueryOptions {
  namespace?: string
  pod?: string
  container?: string
  range?: TimeRangeId
  start?: Date
  end?: Date
}

export async function queryMetrics(
  clusterId: number,
  metric: MetricKind,
  options: MetricQueryOptions = {},
): Promise<MetricQueryResponse> {
  const { data } = await http.get<MetricQueryResponse>(
    `/clusters/${clusterId}/observability/metrics/query`,
    {
      params: {
        metric,
        namespace: options.namespace || undefined,
        pod: options.pod || undefined,
        container: options.container || undefined,
        range: options.range || undefined,
        start: options.start?.toISOString(),
        end: options.end?.toISOString(),
      },
    },
  )
  return data
}

/**
 * CompareOptions narrows a comparison table. `topk` is how many rows to rank;
 * the server defaults it to five and caps it, because past that it stops being a
 * comparison and becomes a listing.
 */
export interface CompareOptions {
  namespace?: string
  pod?: string
  container?: string
  topk?: number
  range?: TimeRangeId
}

export async function compareMetrics(
  clusterId: number,
  metric: MetricKind,
  options: CompareOptions = {},
): Promise<MetricCompareResponse> {
  const { data } = await http.get<MetricCompareResponse>(
    `/clusters/${clusterId}/observability/metrics/compare`,
    {
      params: {
        metric,
        namespace: options.namespace || undefined,
        pod: options.pod || undefined,
        container: options.container || undefined,
        topk: options.topk || undefined,
        range: options.range || undefined,
      },
    },
  )
  return data
}

export interface LogQueryOptions {
  namespace?: string
  pod?: string
  container?: string
  /**
   * Free text to look for in the message. This is the one value that is not a
   * Kubernetes name, and the server quotes it into the query as a literal
   * rather than validating it — an operator searching logs needs to be able to
   * type a quote or a brace.
   */
  filter?: string
  /** A preset the server resolves. `start`/`end` beat it. */
  range?: TimeRangeId
  start?: Date
  end?: Date
  limit?: number
}

export async function queryLogs(
  clusterId: number,
  options: LogQueryOptions = {},
): Promise<LogQueryResponse> {
  const { data } = await http.get<LogQueryResponse>(
    `/clusters/${clusterId}/observability/logs/query`,
    {
      params: {
        namespace: options.namespace || undefined,
        pod: options.pod || undefined,
        container: options.container || undefined,
        filter: options.filter || undefined,
        range: options.range || undefined,
        start: options.start?.toISOString(),
        end: options.end?.toISOString(),
        limit: options.limit || undefined,
      },
    },
  )
  return data
}

/**
 * unconfigured reports whether a query failed because the cluster has no
 * datasource rather than because the query was wrong. The two need different
 * words on screen: one is "set this up", the other is "this went wrong".
 */
export function unconfigured(err: unknown): boolean {
  return Boolean(
    axios.isAxiosError(err) &&
      (err.response?.data as { unconfigured?: boolean } | undefined)?.unconfigured,
  )
}

/**
 * queryError turns an observability failure into something that names the actual
 * problem.
 *
 * The generic fallback is wrong in one specific and costly case: a **404**. Every
 * handled error from these endpoints carries a JSON `error` field, so a response
 * without one did not come from the handler at all — it came from the router,
 * which means this page is talking to a server that does not have these routes.
 * Reporting that as "could not read metrics for this window" points at the time
 * range, which is the one thing that is not wrong, and sends whoever reads it to
 * change a filter that was never the problem.
 */
export function queryError(err: unknown, fallback: string): string {
  if (axios.isAxiosError(err) && !(err.response?.data as { error?: string } | undefined)?.error) {
    if (err.response?.status === 404) {
      return (
        'This kubemg server has no history endpoint — it is running a build older ' +
        'than this page. Restart the backend so it picks up the current code.'
      )
    }
    if (err.response && err.response.status >= 500) {
      return `The kubemg server failed on this query (HTTP ${err.response.status}). Check its logs.`
    }
  }
  return errorMessage(err, fallback)
}

/* ------------------------------------------- just-in-time elevated access --- */

/**
 * The requests this caller may see. A non-admin is narrowed to their own by the
 * server — the `user` filter here cannot widen that, exactly as it cannot on the
 * audit trail — so the response says which of the two happened (`scoped_to_me`)
 * rather than leaving the page to guess from the role.
 */
export async function fetchJitRequests(query?: {
  status?: JitStatus[]
  clusterID?: number
  userID?: number
}): Promise<JitRequestList> {
  const params: Record<string, string> = {}
  if (query?.status?.length) params.status = query.status.join(',')
  if (query?.clusterID) params.cluster_id = String(query.clusterID)
  if (query?.userID) params.user_id = String(query.userID)
  const { data } = await http.get<JitRequestList>('/jit/requests', { params })
  return data
}

export async function createJitRequest(input: JitRequestInput): Promise<JitRequest> {
  const { data } = await http.post<JitRequest>('/jit/requests', input)
  return data
}

export async function approveJitRequest(id: string, comment?: string): Promise<JitRequest> {
  const { data } = await http.post<JitRequest>(`/jit/requests/${id}/approve`, { comment })
  return data
}

export async function rejectJitRequest(id: string, comment?: string): Promise<JitRequest> {
  const { data } = await http.post<JitRequest>(`/jit/requests/${id}/reject`, { comment })
  return data
}

export async function revokeJitRequest(id: string, comment?: string): Promise<JitRequest> {
  const { data } = await http.post<JitRequest>(`/jit/requests/${id}/revoke`, { comment })
  return data
}

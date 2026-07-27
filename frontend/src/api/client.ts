import axios from 'axios'
import { ALL_NAMESPACES } from '../lib/resources'
import type {
  AgentInstall,
  AuditPage,
  AuditQuery,
  AuditSummary,
  Cluster,
  ClusterListResponse,
  ClusterNode,
  ConfigEntry,
  CronJob,
  CustomResourceDefinition,
  Group,
  Ingress,
  Job,
  Kubeconfig,
  LoginResponse,
  Namespace,
  NewCluster,
  NewUser,
  OptionalList,
  Permission,
  PermissionGrant,
  PermissionMatrix,
  PersistentVolume,
  PersistentVolumeClaim,
  Pod,
  Route,
  Service,
  SettingsPatch,
  SettingsResponse,
  StorageClass,
  SubjectType,
  User,
  UserPatch,
  Workload,
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

http.interceptors.request.use((config) => {
  const token = readToken()
  if (token) {
    config.headers.Authorization = `Bearer ${token}`
  }
  return config
})

http.interceptors.response.use(
  (response) => response,
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
    if (error.code === 'ERR_NETWORK') return 'Cannot reach the KubeMG server.'
  }
  return fallback
}

export async function login(username: string, password: string): Promise<LoginResponse> {
  const { data } = await http.post<LoginResponse>('/auth/login', { username, password })
  return data
}

export async function fetchMe(): Promise<User> {
  const { data } = await http.get<User>('/auth/me')
  return data
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

export async function fetchSettings(): Promise<SettingsResponse> {
  const { data } = await http.get<SettingsResponse>('/settings')
  return { ...data, warnings: data.warnings ?? [] }
}

export async function updateSettings(patch: SettingsPatch): Promise<SettingsResponse> {
  const { data } = await http.put<SettingsResponse>('/settings', patch)
  return { ...data, warnings: data.warnings ?? [] }
}

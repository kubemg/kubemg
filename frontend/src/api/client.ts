import axios from 'axios'
import type {
  AgentInstall,
  AuditPage,
  AuditQuery,
  AuditSummary,
  Cluster,
  Namespace,
  Pod,
  Workload,
  ClusterListResponse,
  Group,
  Kubeconfig,
  LoginResponse,
  NewCluster,
  NewUser,
  Permission,
  PermissionGrant,
  PermissionMatrix,
  SettingsPatch,
  SettingsResponse,
  SubjectType,
  User,
  UserPatch,
} from './types'

const TOKEN_KEY = 'kubemg.token'

const baseURL = `${import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080'}/api/v1`

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

export async function fetchWorkloads(clusterId: number, namespace: string): Promise<Workload[]> {
  const { data } = await http.get<{ workloads: Workload[] }>(
    `/clusters/${clusterId}/resources/workloads`,
    { params: { namespace } },
  )
  return data.workloads ?? []
}

export async function fetchPods(clusterId: number, namespace: string): Promise<Pod[]> {
  const { data } = await http.get<{ pods: Pod[] }>(`/clusters/${clusterId}/resources/pods`, {
    params: { namespace },
  })
  return data.pods ?? []
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
  const base = `${baseURL}/clusters/${clusterId}/proxy${path.startsWith('/') ? path : `/${path}`}`
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

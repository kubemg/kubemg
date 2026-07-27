export type Role = 'admin' | 'user'

/** The administrative tier shown in the IAM screens. */
export type SystemRole = 'superadmin' | 'admin' | 'user'

export type Environment = 'prod' | 'staging' | 'dev'

export interface User {
  id: number
  username: string
  email?: string
  /** Coarse privilege carried in the JWT; derived from system_role. */
  role: Role
  system_role: SystemRole
  is_active: boolean
  last_login_at?: string
  created_at: string
}

export interface NewUser {
  username: string
  email: string
  password: string
  system_role: SystemRole
}

export interface UserPatch {
  username?: string
  email?: string
  password?: string
  system_role?: SystemRole
}

export interface Group {
  id: number
  name: string
  description?: string
  member_ids: number[]
  created_at: string
}

/** A permission targets either one account or every member of a group. */
export type SubjectType = 'user' | 'group'

export type K8sRole = 'cluster-admin' | 'edit' | 'view'

export interface Permission {
  subject_type: SubjectType
  subject_id: number
  subject_name: string
  cluster_id: number
  cluster_name: string
  k8s_role: string
  namespaces: string[]
}

export interface PermissionMatrix {
  user_permissions: Permission[]
  group_permissions: Permission[]
}

export interface PermissionGrant {
  subject_type: SubjectType
  subject_id: number
  cluster_id: number
  k8s_role: K8sRole
  namespaces: string[]
}

export type ClusterStatus = 'healthy' | 'unhealthy' | 'pending'

/** How KubeMG reaches a cluster: through an in-cluster agent, or by dialling it. */
export type ConnectionMode = 'agent' | 'direct'

export interface Cluster {
  id: number
  name: string
  environment: Environment
  description?: string
  api_url: string
  status: ClusterStatus
  /** Why the cluster is not healthy. Absent when it is. */
  status_message?: string
  /** Version the API server reported at the last check. */
  kubernetes_version?: string
  last_checked_at?: string
  created_at: string
  k8s_role: string
  namespaces: string[]
  connection_mode: ConnectionMode
  agent_version?: string
  agent_connected_at?: string
  /** Live tunnel state, not the stored status: is an agent talking to us now. */
  agent_attached: boolean
}

export interface LoginResponse {
  token: string
  expires_at: string
  user: User
}

export interface ClusterListResponse {
  clusters: Cluster[]
}

export interface NewCluster {
  name: string
  environment: Environment
  description?: string
  connection_mode: ConnectionMode
  /** Required for a direct connection, omitted for an agent-based one. */
  api_url?: string
  ca_cert_data?: string
  service_account_token?: string
}

export interface AuditEvent {
  id: number
  at: string
  user_id: number
  username: string
  cluster_id: number
  cluster: string
  verb: string
  method: string
  path: string
  namespace?: string
  resource?: string
  impersonated_user?: string
  impersonated_groups: string[]
  status: number
  duration_ms: number
  /** A long-lived call: exec, attach, watch, logs -f. Recorded on open and close. */
  streaming: boolean
  phase?: 'open' | 'close'
  bytes_out?: number
  bytes_in?: number
  error?: string
}

export interface AuditPage {
  events: AuditEvent[]
  total: number
  limit: number
  offset: number
  /** True when the caller is not an admin and is seeing only their own actions. */
  scoped_to_self: boolean
}

export interface AuditSummary {
  total: number
  failed: number
  streams: number
  window_hours: number
}

export interface AuditQuery {
  cluster_id?: number
  user_id?: number
  verb?: string
  namespace?: string
  streaming?: boolean
  failed?: boolean
  q?: string
  limit?: number
  offset?: number
}

/** Live cluster state, read on demand through the agent tunnel. */
export interface Namespace {
  name: string
  status: string
  created_at?: string
  granted: boolean
}

export interface Workload {
  kind: 'Deployment' | 'StatefulSet' | 'DaemonSet'
  name: string
  namespace: string
  ready: number
  desired: number
  images: string[]
  created_at: string
}

export interface PodContainer {
  name: string
  image: string
  ready: boolean
  restarts: number
  state: string
}

export interface Pod {
  name: string
  namespace: string
  phase: string
  node: string
  pod_ip?: string
  ready: number
  total: number
  restarts: number
  created_at: string
  containers: PodContainer[]
}

/** Everything needed to install the agent into a freshly registered cluster. */
export interface AgentInstall {
  cluster_id: number
  cluster: string
  namespace: string
  image: string
  bastion_url: string
  package_dir: string
  agent_token: string
  manifest_url: string
  archive_url: string
  apply_command: string
  kustomize_command: string
  manifest: string
  files: Record<string, string>
}

export interface Kubeconfig {
  cluster: string
  context: string
  namespace: string
  ttl_seconds: number
  expires_at: string
  filename: string
  kubeconfig: string
  k8s_role: string
  service_account: string
}

/**
 * Server-wide settings. `effective` is what the backend uses right now,
 * `overrides` is what is stored, and `defaults` is the environment behind it —
 * clearing a field restores the default rather than emptying it.
 */
export interface RuntimeSettings {
  public_url: string
  agent_image: string
  agent_namespace: string
}

export interface SettingsResponse {
  effective: RuntimeSettings
  overrides: RuntimeSettings
  defaults: RuntimeSettings
  warnings: string[]
}

export type SettingsPatch = Partial<RuntimeSettings>

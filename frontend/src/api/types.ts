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
  /**
   * The container's declared resource contract, in the same units as the
   * metrics endpoints. Zero means the container declares none — a real answer,
   * and the reason a usage bar sometimes has no denominator to draw against.
   */
  cpu_request_millicores: number
  cpu_limit_millicores: number
  memory_request_bytes: number
  memory_limit_bytes: number
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

/*
 * The rest of the inventory the Explore sidebar browses. Every list is
 * normalised by the backend into the columns a list view shows — the browser
 * never parses a raw Kubernetes object — so these are the response shapes, not
 * the cluster's.
 */

export interface Job {
  name: string
  namespace: string
  created_at: string
  completions: number
  succeeded: number
  failed: number
  active: number
  state: string
  images: string[]
}

export interface CronJob {
  name: string
  namespace: string
  created_at: string
  schedule: string
  suspended: boolean
  active: number
  last_schedule_at?: string
}

export interface Service {
  name: string
  namespace: string
  created_at: string
  type: string
  cluster_ip: string
  external_ips: string[]
  ports: string[]
}

export interface Ingress {
  name: string
  namespace: string
  created_at: string
  class: string
  hosts: string[]
  addresses: string[]
  rules: number
}

/** A Gateway API HTTPRoute or an Istio VirtualService, which read the same way. */
export interface Route {
  name: string
  namespace: string
  created_at: string
  hostnames: string[]
  /** The gateways the route attaches to. */
  parents: string[]
  rules: number
}

/**
 * An optional list: the resource is a CRD that may not be installed, so the
 * response says whether the cluster serves it at all.
 */
export interface OptionalList<T> {
  items: T[]
  available: boolean
  reason?: string
}

export interface PersistentVolume {
  name: string
  created_at: string
  capacity: string
  access_modes: string[]
  reclaim_policy: string
  status: string
  claim?: string
  storage_class?: string
}

export interface PersistentVolumeClaim {
  name: string
  namespace: string
  created_at: string
  status: string
  capacity: string
  access_modes: string[]
  storage_class?: string
  volume?: string
}

export interface StorageClass {
  name: string
  created_at: string
  provisioner: string
  reclaim_policy: string
  binding_mode: string
  default: boolean
}

/**
 * A ConfigMap or a Secret. Only the keys travel — a value is never in a
 * response, so no secret lands in a browser cache because someone opened a list.
 */
export interface ConfigEntry {
  name: string
  namespace: string
  created_at: string
  /** Set for secrets: the Kubernetes secret type. */
  type?: string
  keys: string[]
  immutable?: boolean
}

export interface CustomResourceDefinition {
  name: string
  created_at: string
  group: string
  kind: string
  plural: string
  scope: string
  versions: string[]
}

export interface ClusterNode {
  name: string
  created_at: string
  ready: boolean
  status: string
  roles: string[]
  version: string
  internal_ip?: string
  os_image?: string
  cpu?: string
  memory?: string
  unschedulable?: boolean
}

/*
 * Live utilisation, read from the cluster's own Metrics API through the same
 * tunnel as everything else. metrics-server is optional, so every metrics
 * response says whether the cluster serves it at all — `available: false` with
 * a reason is an answer, not a failure.
 */

export interface ContainerUsage {
  name: string
  cpu_millicores: number
  memory_bytes: number
}

export interface PodUsage {
  name: string
  namespace: string
  cpu_millicores: number
  memory_bytes: number
  containers: ContainerUsage[]
}

export interface NodeUsage {
  name: string
  cpu_millicores: number
  cpu_capacity_millicores: number
  cpu_percent: number
  memory_bytes: number
  memory_capacity_bytes: number
  memory_percent: number
}

/** One cluster's total consumption against its total allocatable capacity. */
export interface UsageSummary {
  nodes: number
  cpu_millicores: number
  cpu_capacity_millicores: number
  cpu_percent: number
  memory_bytes: number
  memory_capacity_bytes: number
  memory_percent: number
}

export interface NodeMetrics {
  available: boolean
  reason?: string
  nodes: NodeUsage[]
  summary: UsageSummary
}

export interface PodMetrics {
  available: boolean
  reason?: string
  pod: PodUsage | null
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
  /** Empty in agent mode: the bastion impersonates the caller instead. */
  service_account: string
  connection_mode: ConnectionMode
  /** What kubectl dials — the API server directly, or KubeMG's proxy. */
  server: string
  /** Set when the kubeconfig is valid but cannot work as configured. */
  warning?: string
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
  /** Retention window for the audit trail. 0 in `overrides` means unset. */
  audit_retention_days: number
}

export interface SettingsResponse {
  effective: RuntimeSettings
  overrides: RuntimeSettings
  defaults: RuntimeSettings
  warnings: string[]
}

export type SettingsPatch = Partial<RuntimeSettings>

/**
 * One object in full, as the YAML an operator already knows how to read.
 * `editable` says whether KubeMG will write the manifest back — it is not a
 * statement about the caller's cluster RBAC, which is only settled by trying.
 */
export interface ResourceManifest {
  yaml: string
  kind: string
  api_version: string
  name: string
  namespace?: string
  resource_version?: string
  editable: boolean
  reason?: string
}

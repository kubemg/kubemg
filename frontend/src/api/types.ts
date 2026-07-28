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
  /**
   * Where this account's credentials live: `local` for a password stored by
   * KubeMG, or the protocol of the identity provider that vouches for it. A
   * federated account has no password here, so the editor offers none.
   */
  auth_source: AuthSource
  last_login_at?: string
  created_at: string
}

/** `local`, or the federation protocol that authenticates the account. */
export type AuthSource = 'local' | SSOProtocol

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

/**
 * One object of a kind served by a CRD. A CRD's spec is whatever its author
 * decided, so the only fields that hold for all of them are the ones every
 * Kubernetes object carries — which is why this list has the columns it has.
 */
export interface CustomResource {
  name: string
  namespace: string
  created_at: string
  kind?: string
  api_version?: string
}

/**
 * One Kubernetes Event recorded against an object. This is the part of describe
 * that neither a list nor a manifest has: a spec is what was asked for, and only
 * an event says why it did not happen.
 */
export interface K8sEvent {
  type: string
  reason: string
  message: string
  count: number
  /** What reported it — the scheduler, a kubelet on a named node, a controller. */
  source?: string
  first_seen?: string
  last_seen?: string
}

/** One entry of an object's `status.conditions`. */
export interface ResourceCondition {
  type: string
  status: string
  reason?: string
  message?: string
  last_transition_at?: string
}

/** One line of a flattened spec or status: a dotted path and its value. */
export interface ResourceField {
  path: string
  value: string
}

/**
 * `kubectl describe`, generically. KubeMG has no per-kind describer — kubectl
 * hand-writes one for every kind, which would be the wrong shape here and
 * impossible for a CRD nobody has heard of — so what comes back is the three
 * things that hold for every Kubernetes object: its metadata, its conditions,
 * and a bounded flatten of spec and status. The YAML tab is the complete view,
 * and `spec_truncated` / `status_truncated` say when the flatten stopped short.
 */
export interface ResourceDescribeResult {
  kind: string
  api_version?: string
  name: string
  namespace?: string
  created_at: string
  labels?: Record<string, string>
  annotations?: Record<string, string>
  conditions: ResourceCondition[]
  spec_summary: ResourceField[]
  spec_truncated?: boolean
  status_summary: ResourceField[]
  status_truncated?: boolean
  events: K8sEvent[]
  /**
   * Events are their own resource with their own RBAC. A grant that can read a
   * Deployment but not the events in its namespace still gets the Deployment —
   * so a refusal narrows the answer and says so, rather than showing an empty
   * table that reads as "nothing happened".
   */
  events_available: boolean
  events_reason?: string
}

/**
 * One Helm release, at the revision that is current. Helm 3 has no API of its
 * own — a release is a labelled Secret holding a compressed blob — so this is
 * what the backend decoded out of that blob, never the blob itself: the release
 * object also carries the chart's rendered manifest, which for many charts holds
 * generated passwords, and it does not leave the cluster.
 */
export interface HelmRelease {
  name: string
  namespace: string
  chart_name: string
  chart_version: string
  app_version: string
  revision: number
  status: string
  description?: string
  updated_at: string
}

/**
 * A release's values, as `helm get values` shows them: what the operator
 * supplied, not the chart's defaults merged into it. `warning` is the standing
 * caveat on writing them — a saved revision records what Helm will start from
 * and renders nothing — and it comes from the backend so a client that ignores
 * it is still told.
 */
export interface HelmValues {
  release: HelmRelease
  yaml: string
  warning: string
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

/**
 * What a workload action did. Both routes are read-modify-writes down the same
 * impersonated tunnel as every other call, so a refusal here is the cluster's
 * own RBAC answering — and `message` is what the console says afterwards, in the
 * cluster's terms rather than in HTTP's.
 */
export interface WorkloadActionResult {
  kind: string
  name: string
  namespace: string
  replicas?: number
  restarted_at?: string
  message: string
}

/* ------------------------------------------------ observability queries --- */

/**
 * The charts KubeMG can draw. It is a closed set because each one is a
 * hand-written PromQL template on the server — the browser never sends a query,
 * because a metrics backend has never heard of the caller and would answer
 * anything it was asked, so the namespace scope has to be enforced by KubeMG
 * being the one that writes the query.
 */
export type MetricKind =
  | 'pod_cpu'
  | 'pod_memory'
  | 'namespace_cpu'
  | 'namespace_memory'
  | 'cluster_cpu'
  | 'cluster_memory'

/** The two units the server normalises to, the same pair the meters use. */
export type MetricUnit = 'millicores' | 'bytes'

export interface MetricPoint {
  at: string
  value: number
}

export interface MetricSeries {
  name: string
  labels?: Record<string, string>
  points: MetricPoint[]
}

export interface MetricResult {
  kind: MetricKind
  unit: MetricUnit
  series: MetricSeries[]
  start: string
  end: string
  step_seconds: number
  /** More series existed than were sent; the chart says so rather than lying. */
  truncated?: boolean
  /**
   * The PromQL KubeMG built. An empty chart is almost always a backend that
   * labels its series differently, and there is no way to see that without
   * seeing the query — so it is shown rather than hidden.
   */
  query: string
  description?: string
}

export interface MetricQueryResponse {
  result: MetricResult
  provider: MetricsProvider
  endpoint: string
}

export interface LogEntry {
  at: string
  message: string
  namespace?: string
  pod?: string
  container?: string
  truncated?: boolean
}

export interface LogQueryResult {
  entries: LogEntry[]
  start: string
  end: string
  /** The page hit its limit — there is more inside this window, not none. */
  limited?: boolean
  query: string
}

export interface LogQueryResponse {
  result: LogQueryResult
  provider: LogsProvider
  endpoint: string
}

/* ------------------------------------------------------- observability --- */

/**
 * Where a cluster's series actually live. The Metrics API read answers "right
 * now"; a datasource is what answers "since when", and it belongs to the
 * cluster rather than to the server — two clusters have two Prometheuses.
 */
export type DatasourceKind = 'metrics' | 'logs'

export type MetricsProvider = 'victoriametrics' | 'prometheus' | 'thanos' | 'mimir'
export type LogsProvider = 'victorialogs' | 'loki'
export type DatasourceProvider = MetricsProvider | LogsProvider

/** How KubeMG reaches it: down the agent tunnel, or dialled from here. */
export type DatasourceAccess = 'in-cluster' | 'direct'

export type DatasourceAuth = 'none' | 'bearer' | 'basic'

export interface ObservabilitySource {
  kind: DatasourceKind
  provider: DatasourceProvider
  provider_label: string
  access_mode: DatasourceAccess
  url?: string
  service_namespace?: string
  service_name?: string
  service_port?: string
  service_scheme?: string
  path_prefix?: string
  auth_mode: DatasourceAuth
  username?: string
  /** Whether a credential is stored. The value itself never leaves the server. */
  has_credential: boolean
  insecure_skip_verify: boolean
  enabled: boolean
  /** The address this resolves to, rendered for display. */
  endpoint: string
  last_status: ClusterStatus
  last_message?: string
  detected_version?: string
  last_checked_at?: string
  updated_at: string
}

/**
 * The datasource form. `credential` omitted keeps the stored secret, so editing
 * a port does not mean re-typing a token; sent empty, it is cleared.
 */
export interface DatasourceInput {
  provider: DatasourceProvider
  access_mode: DatasourceAccess
  url?: string
  service_namespace?: string
  service_name?: string
  service_port?: string
  service_scheme?: string
  path_prefix?: string
  auth_mode?: DatasourceAuth
  username?: string
  credential?: string
  insecure_skip_verify?: boolean
  enabled?: boolean
}

/** The verdict of one datasource check, written for the person who typed it. */
export interface DatasourceCheck {
  reachable: boolean
  message: string
  version?: string
  endpoint: string
  path: string
}

export interface ObservabilityResponse {
  sources: ObservabilitySource[]
  agent_attached: boolean
  connection_mode: ConnectionMode
  editable: boolean
}

/** A datasource KubeMG believes is already running in the cluster. */
export interface DatasourceCandidate {
  kind: DatasourceKind
  provider: DatasourceProvider
  service_namespace: string
  service_name: string
  service_port: string
  service_scheme: string
  path_prefix?: string
  /** 2 when it matched on the provider's own port, 1 when only on its name. */
  score: number
  reason: string
}

/*
 * Enterprise sign-in.
 *
 * Two shapes, because the server has two: the public list a login page reads
 * before anyone is authenticated, which carries a name and nothing else, and the
 * administrative configuration behind it. No secret is ever in either — a stored
 * one is represented by its `has_*` flag, and sending the field back is how it is
 * changed.
 */

export type SSOProtocol = 'oidc' | 'saml' | 'ldap'

/** One provider as the login page sees it. */
export interface SSOProviderSummary {
  id: number
  name: string
  protocol: SSOProtocol
  /** False for LDAP, which takes credentials on KubeMG's own form. */
  interactive: boolean
}

/** One provider as an administrator sees it. */
export interface SSOProvider {
  id: number
  name: string
  protocol: SSOProtocol
  enabled: boolean

  issuer_url?: string
  client_id?: string
  scopes?: string

  saml_metadata_url?: string
  saml_entity_id?: string

  ldap_host?: string
  ldap_port?: number
  ldap_use_tls: boolean
  ldap_start_tls: boolean
  ldap_skip_verify: boolean
  ldap_bind_dn?: string
  ldap_base_dn?: string
  ldap_user_filter?: string
  ldap_user_attribute?: string
  ldap_email_attribute?: string
  ldap_group_attribute?: string
  ldap_group_filter?: string
  ldap_group_base_dn?: string
  ldap_group_name_attribute?: string

  username_claim?: string
  email_claim?: string
  groups_claim?: string

  allow_jit: boolean
  default_system_role: 'user' | 'admin'

  last_status: 'pending' | 'healthy' | 'unhealthy'
  last_message?: string
  last_checked_at?: string

  has_client_secret: boolean
  has_bind_password: boolean

  /** What has to be registered in the IdP: redirect URI / assertion consumer. */
  redirect_url: string
  entity_id?: string
  metadata_url?: string
}

/**
 * The provider form. Every secret is optional on the way in: omitted keeps the
 * stored one, so changing a port never means re-typing a credential.
 */
export interface SSOProviderInput {
  name: string
  protocol: SSOProtocol
  enabled?: boolean

  issuer_url?: string
  client_id?: string
  client_secret?: string
  scopes?: string

  saml_metadata_url?: string
  saml_metadata_xml?: string
  saml_entity_id?: string

  ldap_host?: string
  ldap_port?: number
  ldap_use_tls?: boolean
  ldap_start_tls?: boolean
  ldap_skip_verify?: boolean
  ldap_bind_dn?: string
  ldap_bind_password?: string
  ldap_base_dn?: string
  ldap_user_filter?: string
  ldap_user_attribute?: string
  ldap_email_attribute?: string
  ldap_group_attribute?: string
  ldap_group_filter?: string
  ldap_group_base_dn?: string
  ldap_group_name_attribute?: string

  username_claim?: string
  email_claim?: string
  groups_claim?: string

  allow_jit?: boolean
  default_system_role?: 'user' | 'admin'
}

export interface SSOProviderListResponse {
  providers: SSOProvider[]
  /** Origins the server will hand a finished sign-in back to. */
  console_origins: string[]
}

export interface SSOProviderCheck {
  status: 'healthy' | 'unhealthy'
  message: string
}

/**
 * What one external group is worth. A rule can put someone in a local group,
 * grant a Kubernetes role across an environment, or elevate the account itself —
 * and must do at least one of the three, since a rule that confers nothing looks
 * exactly like a rule whose pattern is wrong.
 */
export interface SSOGroupMapping {
  id: number
  provider_id: number
  external_group_pattern: string
  target_group_id?: number
  target_k8s_role?: K8sRole
  environment_filter?: Environment
  namespaces: string[]
  target_system_role?: 'user' | 'admin'
  created_at: string
  updated_at: string
}

export interface SSOGroupMappingInput {
  provider_id: number
  external_group_pattern: string
  target_group_id?: number
  target_k8s_role?: K8sRole | ''
  environment_filter?: Environment | ''
  namespaces?: string[]
  target_system_role?: 'user' | 'admin' | ''
}

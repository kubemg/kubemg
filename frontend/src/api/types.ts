import type { TimeRangeId } from '../lib/timerange'

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
  /**
   * Whether this account may replay *other people's* terminal recordings. It is
   * a capability of its own rather than part of the admin role: a recording holds
   * everything that crossed a production shell, so "may administer KubeMG" is not
   * the same claim as "may watch what a colleague typed". Own sessions are always
   * readable and are not governed by it, a super admin holds it implicitly, and
   * only a super admin may grant it.
   */
  can_view_recordings: boolean
  /**
   * Whether this row is a person or a machine. A machine account — a CI
   * pipeline's release stage, a release bot — is a User because every grant,
   * every namespace scope and the audit trail are keyed on a user id; what
   * differs is that it holds no password and can never be an administrator.
   */
  account_type: AccountType
  last_login_at?: string
  created_at: string
}

export type AccountType = 'user' | 'service'

/** `local`, or the federation protocol that authenticates the account. */
export type AuthSource = 'local' | SSOProtocol

export interface NewUser {
  username: string
  email: string
  password: string
  system_role: SystemRole
  /** Super-admin-only, like every other way of setting it. */
  can_view_recordings?: boolean
}

export interface UserPatch {
  username?: string
  email?: string
  password?: string
  system_role?: SystemRole
  can_view_recordings?: boolean
}

/** A machine account as the console lists it: the account, what it can reach,
    and enough about its credentials to decide whether it is still in use. */
export interface MachineAccount extends User {
  token_count: number
  active_tokens: number
  /** The newest use across every credential the account holds. It is what
      replaces an expiry as a control for a token that has none. */
  last_used_at?: string
  access: MachineAccountAccess[]
}

export interface MachineAccountAccess {
  cluster_id: number
  cluster_name: string
  k8s_role: K8sRole
  namespaces: string[]
}

/** One issued credential, as it reads back. The secret is never here — it is
    returned once, on the response that created it. */
export interface MachineToken {
  id: number
  user_id: number
  name: string
  /** The token's opening characters, which is what matches a row to a value in
      a CI secret store. */
  hint: string
  cluster_id: number
  cluster_name?: string
  namespace?: string
  expires_at?: string
  revoked_at?: string
  last_used_at?: string
  created_at: string
  status: MachineTokenStatus
}

export type MachineTokenStatus = 'active' | 'expired' | 'revoked'

export interface NewMachineToken {
  name: string
  cluster_id: number
  namespace?: string
  ttl_seconds?: number
  /** A credential with no expiry. Separate from ttl_seconds so a client that
      omits everything cannot produce one by accident. */
  never_expires?: boolean
}

export interface IssuedMachineToken {
  token: MachineToken
  /** Shown once and never again: what is stored is a hash. */
  secret: string
  kubeconfig: string
  filename: string
  context: string
  server: string
  k8s_role: string
  /** States what cannot be read off the file — that it never expires, or that
      this server is not on TLS. */
  warning?: string
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
  /** Where the grant came from: `local` an administrator wrote it, `sso` a
      directory derives it, `jit` an approved request activated it. */
  source?: 'local' | 'sso' | 'jit'
  /** Set only on a temporary grant. The matrix has to show it, because a row that
      ends in forty minutes reviewed as though it were permanent is worse than not
      seeing it. */
  expires_at?: string
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
  /** Set on an interactive session; a recorded one is replayable by this id. */
  session_id?: string
  error?: string
  /**
   * The field-level diff of a manifest write (see ManifestDiff), present only
   * on an `update` row written while "record manifest diffs" was on for a
   * non-redacted kind. Its absence is not itself informative: it also means
   * the write was refused, the kind is redacted, or the setting was off —
   * see db.AuditEvent.Diff on the backend for the one place all of that is
   * decided.
   */
  diff?: ManifestDiff
}

/**
 * A recorded interactive session. The row is the index — who ran a shell where,
 * and for how long — and the recording itself is fetched separately, because a
 * list of sessions must not drag a megabyte of terminal output with it.
 */
export interface TerminalSession {
  id: number
  session_id: string
  user_id: number
  username: string
  cluster_id: number
  cluster: string
  namespace?: string
  pod_name?: string
  container_name?: string
  shell?: string
  verb?: string
  started_at: string
  ended_at?: string
  duration_seconds: number
  byte_count: number
  /** The session outgrew the per-recording cap; the replay stops before it did. */
  truncated: boolean
  /**
   * How this file was written, not how the server is configured now: a key can be
   * added to a server that already holds plain recordings, and keystroke
   * collection can be switched off. `input_recorded: false` is why an empty
   * keystroke view is not the same as a session in which nothing was typed.
   */
  encrypted: boolean
  input_recorded: boolean
  /** Still running: a shell somebody is in right now. */
  open: boolean
  error?: string
}

export interface TerminalSessionPage {
  sessions: TerminalSession[]
  total: number
  limit: number
  offset: number
  /**
   * Whether this server records sessions at all. Without it an empty list is
   * ambiguous — nobody opened a shell, or nobody was recording when they did.
   */
  recording_enabled: boolean
  scoped_to_self: boolean
}

/**
 * What this server captures. Readable by anyone, because anyone might be
 * recorded — a console that opens a shell has to be able to say what is kept
 * before a keystroke is typed into it.
 */
export interface RecordingPolicy {
  enabled: boolean
  input_recorded: boolean
  encrypted: boolean
  retention_days: number
}

export interface TerminalSessionQuery {
  cluster_id?: number
  user_id?: number
  namespace?: string
  pod?: string
  /** The correlation id an audit row carries, which is how a row finds its replay. */
  session_id?: string
  open?: boolean
  q?: string
  limit?: number
  offset?: number
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

/** The trail's window. It is the console's shared vocabulary rather than a set
    of its own — the whole point of resolving a preset server-side is that "the
    last hour" means one span in the trail, in a chart and in a pasted link, and
    two tables that agree by coincidence is how that stops being true. `all`
    clears the lower bound here. */
export type AuditRange = TimeRangeId

export interface AuditQuery {
  cluster_id?: number
  user_id?: number
  /** One verb, or several — the API accepts a comma-separated set. */
  verb?: string | string[]
  namespace?: string
  /** One exact HTTP status. `failed` is the broader "anything that went wrong". */
  status?: number
  streaming?: boolean
  failed?: boolean
  q?: string
  /** RFC 3339 bounds. An explicit `from` beats `range`. */
  from?: string
  to?: string
  range?: AuditRange
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

/**
 * The pods one workload owns, resolved from the workload's own selector. It is
 * what turns "this Deployment's logs" into a set of per-pod reads: the log itself
 * is still read one pod at a time, which is why this carries whole `Pod` rows
 * rather than names — a log view needs the containers too.
 */
export interface WorkloadPods {
  pods: Pod[]
  namespace: string
  kind: string
  /** The label selector the backend derived, so the read is explainable. */
  selector: string
  /** Every container name appearing in any of the pods. */
  containers: string[]
  /** Set when the workload has more pods than one read answers with. */
  truncated: boolean
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
  /**
   * When the schedule fires next, derived server-side because no Kubernetes
   * field reports it. It is absent for three distinct reasons and the row says
   * which: the CronJob is suspended, `schedule_error` names an expression this
   * build cannot read, or the schedule has no firing left at all.
   */
  next_schedule_at?: string
  /** `spec.timeZone`, shown beside the expression because it is what the expression means. */
  time_zone?: string
  schedule_error?: string
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

/**
 * How many of one kind a cluster holds, as the counts route answers it.
 *
 * `count` is absent for two different reasons and both are honest: the kind is
 * unavailable to this caller — not served by the cluster, or refused by its RBAC
 * — or the cluster paginated the list without reporting a remainder, which is
 * `available` with no total. Neither is drawn as a number, because a guess in a
 * nav column is worse than a blank.
 */
export interface ResourceCount {
  count?: number
  available: boolean
  reason?: string
  /**
   * Set when the total came from the API server's own remainingItemCount rather
   * than from a page holding the whole list. Kubernetes documents that number as
   * an estimate; with no selector on the read it is exact in practice, so the
   * sidebar prints it plainly and this is here for anyone reading the API.
   */
  approximate?: boolean
}

/**
 * A NetworkPolicy, reduced to what a list row can show. There is no single axis
 * like a Role's verbs-and-resources union — a peer list has no such summary —
 * so the rule counts are what the row states and the reachability view is
 * where a rule is actually worth reading.
 */
export interface NetworkPolicy {
  name: string
  namespace: string
  created_at: string
  pod_selector: string
  policy_types: string[]
  ingress_rules: number
  egress_rules: number
}

/**
 * One source (ingress) or destination (egress) a NetworkPolicy rule names.
 * `pod_selector`/`namespace_selector` are the selector text, and an *empty*
 * string is a real answer — an empty LabelSelector matches every pod or every
 * namespace — never a missing one.
 */
export interface NetworkPolicyPeer {
  kind: 'all' | 'namespace' | 'pod' | 'ip_block'
  pod_selector?: string
  namespace_selector?: string
  /** Set only for a bare podSelector, which is scoped to the policy's own namespace. */
  namespace?: string
  cidr?: string
  except?: string[]
}

/**
 * A workload's own view of the policies that select it — what may reach it,
 * what it may reach, and whether nothing selects it at all. This is a
 * derivation from the NetworkPolicy objects alone: `disclaimer` carries the
 * statement that it is not what the cluster's CNI enforces and does not trace
 * a live connection, and the console renders it rather than only the backend
 * knowing it.
 */
export interface NetworkPolicyReachability {
  kind: string
  name: string
  namespace: string
  pod_labels: Record<string, string>
  /** Whether `pod_labels` came off the object itself (a Pod) or its pod template. */
  label_source: 'pod' | 'pod template'

  ingress_covered: boolean
  ingress_policies: string[]
  ingress_peers: NetworkPolicyPeer[]
  /**
   * True when *some* policy in this namespace declares Ingress, whether or not
   * it selects this workload — what turns "not covered" into "wide open by
   * omission" rather than "nobody here uses NetworkPolicy at all".
   */
  namespace_has_ingress_policies: boolean

  egress_covered: boolean
  egress_policies: string[]
  egress_peers: NetworkPolicyPeer[]
  namespace_has_egress_policies: boolean

  /** False when the policy list itself could not be read — everything above is
      then unknown rather than "no policies", and `unavailable_reason` says why. */
  policies_available: boolean
  unavailable_reason?: string
  disclaimer: string
}

/** The namespace-level summary of what is and is not covered by a NetworkPolicy. */
export interface NetworkPolicyCoverage {
  namespace: string
  policy_count: number
  pod_count: number
  ingress_covered_pods: number
  ingress_uncovered_pods: number
  ingress_uncovered_examples?: string[]
  egress_covered_pods: number
  egress_uncovered_pods: number
  egress_uncovered_examples?: string[]
  available: boolean
  unavailable_reason?: string
  disclaimer: string
}

/**
 * One of the seven workload security posture rules. There is no eighth —
 * see the backend's postureRules for what each one means and why it ranks
 * where it does.
 */
export type PostureRule =
  | 'privileged_container'
  | 'host_namespace'
  | 'hostpath_volume'
  | 'namespace_no_network_policy'
  | 'automount_default_service_account'
  | 'no_nonroot_declaration'
  | 'no_resource_limits'

/**
 * One rule firing on one object (or, for the namespace rule, on the namespace
 * itself). `field` names the manifest path that produced it; `permits` is the
 * server's own ranking of what the finding permits, which is the order the
 * list sorts on rather than a count of how many fired.
 *
 * `pss_covered` says, unambiguously, whether this finding is a named
 * Kubernetes Pod Security Standards control: when true, `pss_profile` and
 * `pss_control` carry the citation; when false, `pss_note` carries the
 * one-line reason PSS does not govern this rule at all. Never infer either
 * state from a field being present or absent — read `pss_covered` itself.
 */
export interface PostureFinding {
  rule: PostureRule
  title: string
  permits: number

  kind: string
  name: string
  namespace?: string
  container?: string

  field: string
  message: string

  pss_covered: boolean
  pss_profile?: 'baseline' | 'restricted'
  pss_control?: string
  pss_note?: string

  acknowledged: boolean
  ack_reason?: string
  ack_by?: string
  ack_at?: string
}

export interface PostureReadGap {
  resource: string
  reason: string
}

/** The whole answer for one cluster or one namespace, ordered by permits. */
export interface PostureScan {
  namespace: string
  all_namespaces: boolean

  findings: PostureFinding[]

  scanned_workloads: number
  scanned_pods: number
  truncated: boolean
  findings_capped?: boolean

  unavailable?: PostureReadGap[]

  disclaimer: string
  non_goal_notice: string

  // pss_notice and pss_unchecked state, in the same register as
  // non_goal_notice, that citing Pod Security Standards on four of the seven
  // rules is not a claim of baseline or restricted compliance — pss_unchecked
  // is the list of baseline/restricted controls this scan does not evaluate
  // at all. See the backend's postureUncheckedPSSControls.
  pss_notice: string
  pss_unchecked: string[]
}

/** The stored record of one acknowledged finding, as the server returns it. */
export interface PostureAcknowledgement {
  id: number
  cluster_id: number
  kind: string
  namespace?: string
  name: string
  rule: PostureRule
  reason: string
  acked_by_id: number
  acked_by: string
  created_at: string
  updated_at: string
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
  /**
   * Whether an administrator has taken this kind off the cluster's sidebar. It
   * is only ever set for an administrator — everybody else is served the curated
   * list and never sees the row — so it doubles as "you may put this back".
   */
  hidden?: boolean
}

/**
 * Which of a cluster's custom resources its Explore sidebar offers.
 *
 * `hidden` is keyed `plural.group`, the way kubectl names a resource
 * unambiguously. It is the *hidden* set rather than the shown one, so a cluster
 * nobody has curated reads as empty and shows everything it serves — and an
 * operator installed tomorrow arrives in the sidebar rather than silently
 * missing from it.
 */
export interface CRDVisibility {
  hidden: string[]
  editable: boolean
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

/*
 * The target cluster's own RBAC.
 *
 * Everything else under Access in the console describes *KubeMG's* permission
 * model — who may open which cluster, in which namespaces, with which role.
 * These describe the model that actually decides: the cluster's, read through
 * the same impersonated tunnel as every other list. The two are separate on
 * purpose and the console says so; this is what makes "the cluster decides"
 * checkable rather than a claim.
 */

/** One rule of a Role or ClusterRole, on RBAC's own three axes. */
export interface PolicyRule {
  verbs: string[]
  api_groups?: string[]
  resources?: string[]
  /** Narrows a rule to named objects — "may delete *one* pod". */
  resource_names?: string[]
  /** The other kind of rule entirely: `/healthz`, `/metrics`. ClusterRoles only. */
  non_resource_urls?: string[]
}

/**
 * A Role or a ClusterRole. They are one type here because they are one type in
 * every way that matters to a reader: the same rules, differing only in whether
 * a namespace bounds them.
 */
export interface ClusterRoleEntry {
  name: string
  /** Absent on a ClusterRole, which is how the two are told apart in a table. */
  namespace?: string
  created_at: string
  /** A bounded prefix of the policy; `rule_count` is how many there really are. */
  rules: PolicyRule[]
  rule_count: number
  /** The union across every rule, which is what a single row can show. */
  verbs: string[]
  resources: string[]
  /** Assembled by the aggregation controller, so its rules are an output. */
  aggregated?: boolean
  /** One of Kubernetes' own (`kubernetes.io/bootstrapping`). */
  builtin?: boolean
  /** Holds a rule granting `*` verbs or `*` resources. */
  wildcard?: boolean
}

/** Who a binding binds. `namespace` is meaningful for a ServiceAccount only. */
export interface BindingSubject {
  kind: string
  name: string
  namespace?: string
}

/**
 * A RoleBinding or ClusterRoleBinding, resolved the way the question is asked —
 * who gets what — rather than printed as a roleRef the reader then has to look
 * up in two other lists.
 */
export interface RoleBindingEntry {
  name: string
  namespace?: string
  created_at: string
  /** `Role` or `ClusterRole`: a RoleBinding may reference either. */
  role_kind: string
  role_name: string
  /** A ClusterRoleBinding's grant covers every namespace at once. */
  cluster_scoped?: boolean
  subjects: BindingSubject[]
  subject_count: number
  kinds?: string[]
}

/**
 * A ServiceAccount — an identity, which is why it is under Access rather than
 * with the workloads that run as it.
 */
export interface ServiceAccountEntry {
  name: string
  namespace: string
  created_at: string
  secrets: number
  image_pull_secrets: number
  /** The explicit setting only; absent means the pod spec decides. */
  automount_token?: boolean
  default?: boolean
}

/** A question for the cluster's authorizer, about somebody who is not you. */
export interface AccessReviewQuestion {
  subject: string
  groups?: string[]
  verb: string
  group?: string
  resource: string
  subresource?: string
  name?: string
  namespace?: string
}

/**
 * The authorizer's own verdict. It is asked rather than derived because reading
 * bindings and reasoning about them is KubeMG guessing at an answer the cluster
 * will state — aggregation, wildcards, several bindings reaching one subject, and
 * authorizers that are not RBAC at all are each a way for the guess to be wrong.
 */
export interface AccessReviewResult {
  allowed: boolean
  /**
   * Not merely "not allowed": an explicit deny that no later authorizer can
   * overturn. It is the difference between "add a binding" and "that will not
   * help".
   */
  denied?: boolean
  /** The authorizer's own words, usually naming the binding that decided it. */
  reason?: string
  /** The authorizer could not finish. Not a denial, and never shown as one. */
  evaluation_error?: string
  subject: string
  verb: string
  resource: string
  namespace?: string
}

/** Who KubeMG impersonates for you on one cluster — the subject to ask about. */
export interface GrantIdentity {
  subject: string
  groups: string[]
  k8s_role: string
  namespaces: string[]
  cluster: string
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

/*
 * The cluster-wide events timeline.
 *
 * The same events the describe tab reads, without an object to ask about. What
 * makes it a timeline rather than a second table is the grouping, which has two
 * levels: one **group** per involved object, and one **entry** per reason inside
 * it. A failing Deployment emits events from the deployment controller, the
 * replica set and every pod it owns — as rows that is forty lines describing one
 * problem, and as a group it is one problem to open.
 */

/** One reason that fired against an object, with every firing folded together. */
export interface EventEntry {
  type: string
  reason: string
  /** The newest message for this reason — the one describing the state now. */
  message: string
  /** Every firing folded together, not the number of Event objects. */
  count: number
  source?: string
  first_seen?: string
  last_seen?: string
}

/** What an event was about. */
export interface EventObjectRef {
  kind: string
  name: string
  namespace?: string
}

/** One row of the timeline: everything the cluster has said about one object. */
export interface EventGroup {
  /** Stable across refreshes, so an expanded row stays expanded on a re-read. */
  key: string
  object: EventObjectRef
  /** The worst type in the group: one Warning among ten Normals is a warning. */
  type: string
  /** The newest entry's, which is what the collapsed row shows. */
  reason: string
  message: string
  /** Totals rather than row counts: 41 means the cluster said it 41 times. */
  count: number
  warnings: number
  first_seen?: string
  last_seen?: string
  entries: EventEntry[]
  entries_truncated?: boolean
}

export interface EventTimeline {
  groups: EventGroup[]
  namespace?: string
  all_namespaces: boolean
  /**
   * Events are their own resource with their own RBAC, so a refusal narrows the
   * answer rather than failing it. False means nothing could be read at all.
   */
  events_available: boolean
  reason?: string
  /**
   * The other half of that, and why the flag above is not enough: an
   * all-namespaces read is many reads, and some of them refusing is neither
   * available nor unavailable. Naming them stops a partial cluster being
   * presented as the whole one.
   */
  unreadable_namespaces?: string[]
  /**
   * The cluster had more to say than was read.
   *
   * This carries more weight than it looks. The API server pages a list in **key
   * order** — and an Event's name is `<object>.<hex>`, so a page is an
   * alphabetical slice by involved object, not the newest anything. Sorting that
   * slice by time gives the newest *of the sample*. So on a truncated answer,
   * "newest first" is a claim about what was read rather than about the cluster,
   * and the page has to say so instead of presenting a slice as the whole.
   */
  truncated?: boolean
  /** How many events were actually read and folded. */
  scanned: number
  /**
   * The API server's own count of how many there were, or 0 where it did not
   * offer one — it omits the count for a filtered list, so a narrowed timeline
   * has no honest denominator to show.
   */
  available?: number
  /**
   * The answer came from the cluster's own watch-fed buffer rather than from a
   * list. It is surfaced rather than kept as an implementation detail because it
   * is the difference between "newest first" being a fact about the cluster and
   * a claim about a sample — and because an operator comparing two clusters
   * deserves to know why one page is complete and the other says it is partial.
   */
  buffered?: boolean
  buffered_at?: string
  total_groups: number
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

/**
 * Every revision Helm has stored for one release, newest first — `helm history`.
 * The list route shows one row per release because that answers "what is
 * installed"; this is the other half of the same Secrets and a different
 * question: what this release has been, and what a rollback would go back to.
 *
 * `warning` is the standing caveat on rolling back, and it arrives with the
 * *read* rather than only with the write so the surface offering the action can
 * state its limit before the click.
 */
export interface HelmHistory {
  release: HelmRelease
  history: HelmRelease[]
  warning: string
}

/**
 * One stored revision of a workload's pod template — `kubectl rollout history`.
 * kube-controller-manager keeps one ReplicaSet per Deployment revision and one
 * ControllerRevision per StatefulSet/DaemonSet revision; this is that list,
 * decoded and ordered newest first. `replicas`/`ready` are absent rather than
 * zero when the cluster has already garbage-collected the object that would
 * answer them — a revision can outlive the ReplicaSet it counts.
 */
export interface WorkloadRevision {
  revision: number
  name: string
  created_at: string
  images: string[]
  change_cause: string
  replicas?: number
  ready?: number
  current: boolean
}

/**
 * Every revision the cluster still has for one workload, newest first — the
 * Deployment/StatefulSet/DaemonSet equivalent of `HelmHistory`, and read by the
 * same tab for the same reason: what this workload has been, and what a
 * rollback would go back to.
 */
export interface WorkloadHistory {
  kind: string
  name: string
  namespace: string
  revisions: WorkloadRevision[]
  truncated: boolean
}

/**
 * What a rollback did. It is the scale/restart/suspend read-modify-write
 * again: the target revision's pod template is written back with the live
 * object's `resourceVersion`, so the controller — not kubemg — performs the
 * rollout, and a concurrent change is the API server's own 409.
 */
export interface WorkloadRollbackResult {
  message: string
  revision: number
  kind?: string
  name?: string
  namespace?: string
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

/**
 * Every pod in a scope, which is what a list view needs: reading one pod at a
 * time would be a call per row. Same scope rules as the pod list beside it, so
 * the two answer for the same set of namespaces.
 */
export interface PodListMetrics {
  available: boolean
  reason?: string
  pods: PodUsage[]
}

/*
 * Allocation, which is a different question from consumption and has a
 * different answer. The metrics above say what a node is *using*; these say
 * what has already been promised away. A node can be idle and still refuse to
 * take another pod, because the scheduler places work against requests.
 *
 * `used` is the only part that needs metrics-server. A cluster without one
 * still gets the two numbers scheduling actually turns on, and `available`
 * marks which column is missing rather than failing the page.
 */

/** One resource on one node: millicores for CPU, bytes for memory. */
export interface CapacityDimension {
  allocatable: number
  requested: number
  limited: number
  used: number
  requested_percent: number
  limited_percent: number
  used_percent: number
  /**
   * Containers here declaring no limit for this resource. It travels with the
   * limit because it is what the limit means: a node whose containers mostly
   * declare nothing has a limit figure describing a minority of what runs.
   */
  unlimited_containers: number
}

/** The kubelet's own ceiling — the one nobody remembers until it binds. */
export interface PodSlots {
  allocatable: number
  scheduled: number
  percent: number
  without_requests: number
}

export type CapacitySeverity = 'ok' | 'note' | 'warn' | 'danger'

/** One thing a node's numbers say, in the words the server wrote for it. */
export interface CapacityConcern {
  code: string
  severity: CapacitySeverity
  title: string
  detail: string
}

/** A pod's share of the node it sits on — why that node reads as it does. */
export interface PodRequest {
  name: string
  namespace: string
  cpu_millicores: number
  memory_bytes: number
  share_percent: number
}

export interface NodeCapacity {
  name: string
  roles: string[]
  ready: boolean
  schedulable: boolean
  cpu: CapacityDimension
  memory: CapacityDimension
  pods: PodSlots
  concerns: CapacityConcern[]
  severity: CapacitySeverity
  top_requests: PodRequest[]
}

export interface CapacitySummary {
  nodes: number
  ready: number
  schedulable: number
  cpu: CapacityDimension
  memory: CapacityDimension
  pods: PodSlots
  severity_counts: Partial<Record<CapacitySeverity, number>>
}

/** A pod the scheduler has not placed, with its own explanation of why. */
export interface UnscheduledPod {
  name: string
  namespace: string
  reason?: string
}

export interface ClusterCapacity {
  /** Whether live usage could be read; the rest of the report never depends on it. */
  available: boolean
  reason?: string
  nodes: NodeCapacity[]
  summary: CapacitySummary
  /** A sample — the count beside it is exact. */
  unscheduled: UnscheduledPod[]
  unscheduled_pods: number
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
  /** Set when the kubeconfig is valid but cannot work as configured, or when
      the cluster's own API server granted less time than was asked for. */
  warning?: string
}

/** Which release this console is talking to, and the manual that describes it.
    Both come from the server rather than from the console's own build: an
    install serves the console out of the same binary, and a link to
    documentation for a different release is worse than no link. */
export interface ServerVersion {
  version: string
  docs_url: string
}

/** The window a caller may ask a kubeconfig to live for. Readable by anyone who
    may generate one, so the form offering the choice does not have to discover
    the ceiling by being refused. */
export interface KubeconfigPolicy {
  min_ttl_seconds: number
  default_ttl_seconds: number
  max_ttl_seconds: number
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
  /** Retention window for terminal recordings. 0 means "follow the audit
      window", which is also its ceiling — a replay must not outlive the trail
      saying the shell was opened. */
  session_recording_retention_days: number
  /** The verbs that reach the audit table. Empty with `audit_verbs_selected`
      false means every verb, which is the default. */
  audit_verbs: string[]
  audit_verbs_selected: boolean
  /** Whether interactive sessions are being recorded right now. */
  record_exec_sessions: boolean
  /** Whether this server *can* record at all. Without a recording directory the
      switch above can only ever be off. */
  recording_available: boolean
  /** Whether an `update` audit row carries the manifest diff it wrote. Off by
      default, unlike every other switch on this page: a manifest body can
      hold values as sensitive as a Secret's without the object being one, so
      this is a new class of retained data an operator opts into rather than
      one that starts happening quietly. There is no environment default for
      it — `defaults.record_manifest_diffs` is always false. */
  record_manifest_diffs: boolean
  /** The longest a generated kubeconfig may be asked to live, in hours. 0 in
      `overrides` means unset, which takes the build's own default of a day. It
      is hours rather than days because the setting has to move both ways: an
      install that hands out a quarter and one that refuses anything past a
      shift are the same decision. */
  kubeconfig_max_ttl_hours: number
}

export interface SettingsResponse {
  effective: RuntimeSettings
  overrides: RuntimeSettings
  defaults: RuntimeSettings
  warnings: string[]
}

export type SettingsPatch = Partial<RuntimeSettings>

/* -------------------------------------------------------------- setup --- */

/** Whether this server still needs first-run setup. The sign-in page reads it
    before anybody has a session, so the server answers this and nothing else —
    a configuration description served to a stranger would be reconnaissance. */
export interface SetupState {
  required: boolean
}

/** How loudly the wizard paints one preflight check. `blocked` is reserved for a
    state in which a documented feature cannot work at all, as opposed to one
    that works less safely. */
export type SetupCheckSeverity = 'ok' | 'warn' | 'blocked'

/** One thing about this install that no form can change: TLS material read off
    a volume at boot, the origin of the signing key, whether recordings are
    encrypted. `fix` is the literal line to set somewhere outside the console —
    a warning that does not say what to type is a warning somebody closes. */
export interface SetupCheck {
  key: string
  title: string
  severity: SetupCheckSeverity
  detail: string
  fix?: string
}

export interface SetupPreflight {
  /** The administrator seeded on first boot still has the password it was
      created with. Setup will not finish while this is true. */
  admin_password_pristine: boolean
  checks: SetupCheck[]
  /** The settings-level warnings, reused verbatim from the settings API so the
      two surfaces cannot drift into saying different things about one address. */
  warnings: string[]
}

/** The same reading of this install the wizard's last step shows, served to a
    console that has no wizard left: setup runs once, and a self-signed
    certificate or an unencrypted recording directory does not stop being true
    when it finishes. */
export interface DeploymentPosture {
  checks: SetupCheck[]
  /** How many checks are not `ok`, so a tab can carry a badge without
      re-deriving what that means. */
  attention: number
}

/* ------------------------------------------------------------- alarms --- */

export type AlarmChannelKind =
  | 'alertmanager'
  | 'slack'
  /** Teams takes an Adaptive Card inside an attachment envelope, which is neither
      Slack's blocks nor its attachments — hence a kind of its own. */
  | 'teams'
  | 'pagerduty'
  | 'servicenow'
  | 'webhook'

export type AlarmAuthMode = 'none' | 'bearer' | 'basic' | 'key'

export type AlarmTrigger = 'cluster_event' | 'audit'

export type AlarmSeverity = 'info' | 'warning' | 'critical'

/** One destination alarms are delivered to. The credential is never read back:
    `has_secret` is what stands in for it, which is what makes an empty token box
    mean "keep what is stored" rather than "there is nothing stored". */
export interface AlarmChannel {
  id: number
  name: string
  kind: AlarmChannelKind
  url: string
  auth_mode: AlarmAuthMode
  username?: string
  headers?: string
  enabled: boolean
  has_secret: boolean
  last_status?: string
  last_message?: string
  last_attempt_at?: string
  created_at: string
  updated_at: string
}

export interface AlarmChannelInput {
  name: string
  kind: AlarmChannelKind
  url: string
  auth_mode: AlarmAuthMode
  username?: string
  /** Omit to keep the stored credential. */
  secret?: string
  headers?: string
  enabled?: boolean
}

export interface AlarmChannelList {
  channels: AlarmChannel[]
  kinds: AlarmChannelKind[]
}

/** One condition worth sending somewhere. The matcher fields are stored as
    comma-separated lists, which is what the API returns. */
export interface AlarmRule {
  id: number
  name: string
  description?: string
  channel_id: number
  enabled: boolean
  trigger: AlarmTrigger
  /** 0 means every cluster, including ones registered later. */
  cluster_id: number
  namespaces?: string
  event_reasons?: string
  event_type?: string
  verbs?: string
  denied_only: boolean
  min_status?: number
  severity: AlarmSeverity
  cooloff_seconds?: number
  last_fired_at?: string
  fire_count: number
  created_at: string
  updated_at: string
}

export interface AlarmRuleInput {
  name: string
  description?: string
  channel_id: number
  enabled?: boolean
  trigger: AlarmTrigger
  cluster_id?: number
  namespaces?: string[]
  event_reasons?: string[]
  event_type?: string
  verbs?: string[]
  denied_only?: boolean
  min_status?: number
  severity: AlarmSeverity
  cooloff_seconds?: number
}

export interface AlarmRuleList {
  rules: AlarmRule[]
  triggers: AlarmTrigger[]
  severities: AlarmSeverity[]
  suggested_reasons: string[]
  /** False when this server has no proxy, so no tunnel to read events down. */
  cluster_events_available: boolean
  /** False when no dispatcher is running: rules would never fire, and the panel
      says so rather than looking configured. */
  dispatcher_running: boolean
}

export interface AlarmChannelTest {
  ok: boolean
  message: string
}

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
 * One field-level difference between two decoded objects, mirroring the
 * backend's pkg/objdiff.Change. `old`/`new` are the decoded value on each
 * side — absent rather than null on the side that does not apply, so an
 * "added" change carries no `old` at all instead of an explicit null that
 * would read as though the field used to hold one.
 */
export interface ManifestDiffChange {
  path: string
  kind: 'added' | 'removed' | 'changed'
  old?: unknown
  new?: unknown
  /** Set when a giant value was clipped to fit the diff's own size cap. */
  truncated?: boolean
}

/**
 * A complete structural diff — the confirmation step's payload before a write,
 * and what an `update` audit row carries when the setting to store one is on.
 * One shape, one renderer, for both.
 */
export interface ManifestDiff {
  changes: ManifestDiffChange[]
  /** The object carried more differences than the diff's own cap; what is
      here is a prefix of the real diff, not a sample of it. */
  truncated: boolean
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
  /** The state a schedule was left in. Absent for every action but suspend. */
  suspended?: boolean
  message: string
}

/**
 * What a delete did — or rather asked for. A delete is a request for removal,
 * not the removal itself: a pod with a termination grace period, or anything
 * carrying a finalizer, is still in the list when this comes back, which is why
 * `message` says "marked for deletion" rather than "deleted".
 */
export interface DeleteResult {
  kind: string
  name: string
  namespace?: string
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
  /* Broken down per namespace rather than per pod: the useful shape for a
     cluster's comparison table, where the top five pods out of thousands is a
     list of five strangers. */
  | 'cluster_cpu_by_namespace'
  | 'cluster_memory_by_namespace'
  /* The three readings that say something went wrong rather than what something
     costs. They are the reason the delta column exists. */
  | 'pod_restarts'
  | 'containers_not_ready'
  | 'cpu_throttling'

/** The units the server normalises to. `ratio` is a fraction of one, rendered
    here as a percentage — the backend sends the fraction because rounding it
    twice loses the small values that matter most. */
export type MetricUnit = 'millicores' | 'bytes' | 'count' | 'ratio'

/** Whether a rise in a reading means something got worse. It comes from the
    catalogue rather than being decided here: a namespace burning more CPU is a
    fact, a pod restarting more is a problem — and that is what decides whether
    a delta is allowed to spend colour. */
export type MetricTrend = 'neutral' | 'worse'

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
  /**
   * The same query, in the cluster's registered Grafana, over the same window.
   * It is built by the server for the same reason the query is: a browser
   * assembling its own Explore link would be a browser writing a query. Absent
   * when there is no Grafana registered or the datasource has no uid in it.
   */
  grafana_explore?: string
}

/** One ranked entity across two windows. `previous` is absent when the entity
    had no reading in the window before this one at all — which is "new", a
    different fact from "was zero", and never rendered as an increase. */
export interface CompareRow {
  name: string
  labels?: Record<string, string>
  current: number
  previous?: number
  delta?: number
  /** Absent when the previous reading was zero: everything is an infinite
      increase over nothing. */
  delta_ratio?: number
}

export interface CompareResult {
  kind: MetricKind
  unit: MetricUnit
  rise: MetricTrend
  /** What a row *is* — pod, namespace, container — so the first column can be
      headed with it rather than with "name". */
  legend?: string
  rows: CompareRow[]
  topk: number
  start: string
  end: string
  compare_start: string
  compare_end: string
  query: string
  compare_query: string
  /** Set when the second query failed. The current window is still the answer;
      what is lost is the deltas, and saying so beats showing every row as new. */
  compare_unavailable?: string
  description?: string
}

export interface MetricCompareResponse {
  result: CompareResult
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
  /** The same search in the cluster's Grafana — see MetricQueryResponse. */
  grafana_explore?: string
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
  /** This backend's uid in the cluster's Grafana; what an Explore deep link
      cannot be built without. */
  grafana_datasource?: string
  /** The backend's *own* query UI, where it has one a browser can reach. An
      in-cluster source has none by construction — it is reached by asking the
      API server to proxy to a Service, not by opening a URL. */
  ui_url?: string
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
  grafana_datasource?: string
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

/*
 * The other consoles a cluster is operated from.
 *
 * Deliberately a link and not an embed: an iframe would inherit this console's
 * origin and its session, and proxying a whole Grafana through the agent tunnel
 * would mean carrying another application's routing and websockets inside a
 * transport built for the Kubernetes API. KubeMG stores an address, holds no
 * session for either tool, and the operator signs in to them as themselves.
 */
export type ConsoleKind = 'grafana' | 'argocd' | 'registry'

export interface ClusterConsole {
  kind: ConsoleKind
  url: string
  /** The one identifier that opens the target on the right thing rather than on
      its front page. Optional — a console without one still gets a bare link. */
  ref?: string
  updated_at: string
}

export interface ConsoleInput {
  url: string
  ref?: string
}

/** A datasource's own query UI, derived from the address the cluster already
    declared rather than stored a second time. */
export interface DatasourceUI {
  kind: DatasourceKind
  provider: DatasourceProvider
  url: string
}

export interface ClusterConsolesResponse {
  consoles: ClusterConsole[]
  datasource_uis: DatasourceUI[]
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

/* ---------------------------------------------------- command guardrails --- */

/** Which subject a rule is matched against. `api_request` is "METHOD /path" on a
    proxied call; `terminal_exec` is a line typed into a container, or the argv of
    a non-interactive exec. */
export type GuardrailTarget = 'api_request' | 'terminal_exec' | 'both'

/** `warn` lets the call through and records the match, which is how a rule is
    rolled out before it is armed. */
export type GuardrailAction = 'block' | 'warn'

export interface GuardrailPolicy {
  id: number
  name: string
  description?: string
  /** 0 is fleet-wide, and covers clusters registered after the rule was written. */
  cluster_id: number
  /** Filled in for a cluster-scoped rule so a row can draw its badge without a
      second lookup. */
  cluster_name?: string
  pattern: string
  target: GuardrailTarget
  action: GuardrailAction
  enabled: boolean
  created_at: string
  updated_at: string
}

export interface GuardrailPolicyInput {
  name: string
  description?: string
  cluster_id: number
  pattern: string
  target: GuardrailTarget
  action: GuardrailAction
  enabled?: boolean
}

export interface GuardrailTemplate {
  key: string
  name: string
  description: string
  pattern: string
  target: GuardrailTarget
  action: GuardrailAction
}

export interface GuardrailPolicyList {
  policies: GuardrailPolicy[]
  targets: GuardrailTarget[]
  actions: GuardrailAction[]
  /** How many rules the gateway is actually enforcing. It can differ from the
      number of enabled rows — a pattern that stopped compiling is skipped — and
      an operator reading a list of armed rules deserves to know the count the
      gateway agrees with. */
  enforcing: number
}

/* ------------------------------------------- just-in-time elevated access --- */

/**
 * A request for a stronger Kubernetes role on one cluster, for a bounded window.
 *
 * `active` and `remaining_seconds` are resolved by the server rather than by the
 * browser: the countdown has to agree with the server that will refuse the call,
 * and a row whose status still reads `active` past its expiry is reported here as
 * inactive with nothing left — which is what the access resolver already believes.
 */
export interface JitRequest {
  id: string
  requester_id: number
  requester_username: string
  cluster_id: number
  cluster_name: string
  requested_role: K8sRole
  namespaces: string[]
  duration_minutes: number
  reason: string
  status: JitStatus
  approver_id?: number
  approver_username?: string
  approver_comment?: string
  approved_at?: string
  expires_at?: string
  active: boolean
  remaining_seconds: number
  created_at: string
  updated_at: string
}

/** `approved` and `active` are the same event in this build — an approval writes
    the grant in the same transaction — and both mean a live elevation. */
export type JitStatus = 'pending' | 'approved' | 'active' | 'rejected' | 'expired' | 'revoked'

export interface JitRequestInput {
  cluster_id: number
  requested_role: K8sRole
  namespaces: string[]
  duration_minutes: number
  reason: string
}

export interface JitRequestList {
  requests: JitRequest[]
  pending: number
  /** The windows the server offers, in minutes. Read from the API rather than
      hard-coded so a form can never offer one the API would refuse. */
  durations: number[]
  statuses: JitStatus[]
  roles: K8sRole[]
  /** Whether this caller may decide anything, answered by the server. */
  can_approve: boolean
  /** True when the list was narrowed to the caller's own requests. */
  scoped_to_me: boolean
}

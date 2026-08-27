import type { ResourceItem } from './resources'
import { canCreateResource } from './manifests'

/**
 * A form that writes the manifest.
 *
 * `CreateResourceSheet` states that its starters are "templates, not forms",
 * and that argument is still right about *every* kind: a per-kind form is a
 * schema KubeMG would have to hold for every kind including ones nobody here
 * has heard of, and it would cap what can be created at whatever fields
 * somebody thought of. It is wrong about the six or seven kinds people write by
 * hand every week, and "write the YAML yourself" is where a developer who has
 * never typed a `securityContext` stops — which is exactly the person the
 * developer dashboard exists for.
 *
 * Three rules hold this file together, and each of them is the safety argument
 * rather than a style preference:
 *
 *   - **It is a generator, not a mode.** The only output is YAML text, which the
 *     editor that is already there then posts through the create route that
 *     already exists. Nothing the form cannot express becomes unreachable, the
 *     form can never become the only way to create something, and every
 *     guardrail — the apiVersion check, the namespace-as-address refusal, the
 *     `notCreatable` deny list, the audit record — applies unchanged, because it
 *     is the same request.
 *
 *   - **It is one-way.** Filling the form rewrites the buffer; hand-editing the
 *     buffer never re-populates the form. A two-way binding would have to drop
 *     whatever the YAML holds that the form has no field for, which means a form
 *     that silently deletes somebody's `tolerations`, and a create surface that
 *     loses fields is worse than one that only ever adds them. The surface says
 *     so out loud (`CreateResourceSheet`); this module simply never parses.
 *
 *   - **The field set is the argument.** Per kind, the fields are the ones a
 *     person actually types plus the ones they should have: image and tag,
 *     replicas, ports, environment (including from a ConfigMap or Secret key),
 *     requests and limits, the probes, the ServiceAccount, and a non-root
 *     `securityContext` **on by default** — the posture rules
 *     (`backend/pkg/api/resources_posture.go`) already know what a good workload
 *     looks like, so the form should produce one that passes them rather than
 *     one that has to be fixed afterwards. Anything past that set is what the
 *     YAML tab is for.
 *
 * Everything below is pure: values in, one string out. That is where the logic
 * belongs anyway, and it is what makes the rendering assertable without a DOM
 * the moment the frontend has a test framework.
 *
 * Note what is absent from every rendering here, exactly as it is absent from
 * the starter manifests: `metadata.namespace`. The namespace is part of the
 * *address*, chosen above the editor and checked against the caller's grant.
 */

/* -------------------------------------------------------- which kinds ------ */

/**
 * The kinds a form is offered for. There is no eighth: adding one means adding
 * its value shape, its initial values and its rendering below, and the moment
 * this list stops being "what people write by hand every week" the per-kind
 * schema argument the file header rejects has quietly been accepted.
 */
export type ObjectFormKind =
  | 'pods'
  | 'deployments'
  | 'cronjobs'
  | 'services'
  | 'ingresses'
  | 'httproutes'
  | 'configmaps'

const FORM_KINDS = new Set<string>([
  'pods',
  'deployments',
  'cronjobs',
  'services',
  'ingresses',
  'httproutes',
  'configmaps',
])

/**
 * The form offered for this sidebar entry, or null where there is none.
 *
 * A kind KubeMG will not create at all never gets one — the deny list is asked
 * first rather than mirrored here, so a kind added to it loses its form in the
 * same edit. A discovered CRD never gets one either: its spec is whatever its
 * author decided, which is the case this file exists *not* to guess at.
 */
export function objectFormKind(item: ResourceItem): ObjectFormKind | null {
  if (item.custom !== undefined) return null
  if (!canCreateResource(item)) return null
  return FORM_KINDS.has(item.key) ? (item.key as ObjectFormKind) : null
}

/* ------------------------------------------------------------- values ------ */

/**
 * Every numeric field is held as the string the operator typed, not as a
 * number. A half-typed port is `""` and not `NaN`, and a field left alone is
 * left out of the manifest rather than rendered as a zero somebody has to
 * notice and delete.
 */

/** A row in one of the repeating lists, keyed so React can reorder it. */
interface Entry {
  id: string
}

export interface LabelEntry extends Entry {
  key: string
  value: string
}

export interface PortEntry extends Entry {
  name: string
  port: string
  protocol: 'TCP' | 'UDP'
}

export type EnvSource = 'value' | 'configmap' | 'secret'

export interface EnvEntry extends Entry {
  name: string
  from: EnvSource
  /** Used when `from` is `value`. */
  value: string
  /** The ConfigMap or Secret, and the key in it, when `from` is not `value`. */
  refName: string
  refKey: string
}

export type ProbeMode = 'none' | 'http' | 'tcp'

export interface ProbeValues {
  mode: ProbeMode
  path: string
  port: string
  initialDelaySeconds: string
  periodSeconds: string
}

export interface WorkloadFormValues {
  kind: 'pods' | 'deployments' | 'cronjobs'
  name: string
  containerName: string
  image: string
  tag: string
  /** Deployment only. */
  replicas: string
  /** CronJob only. */
  schedule: string
  /** CronJob only — without it the cluster's own zone decides. */
  timeZone: string
  ports: PortEntry[]
  env: EnvEntry[]
  cpuRequest: string
  memoryRequest: string
  cpuLimit: string
  memoryLimit: string
  serviceAccountName: string
  /**
   * On by default. Off renders no `securityContext` at all rather than one
   * declaring root — the posture rule reads an *absence*, and so does this.
   */
  nonRoot: boolean
  liveness: ProbeValues
  readiness: ProbeValues
}

export interface ServicePortEntry extends Entry {
  name: string
  port: string
  targetPort: string
  protocol: 'TCP' | 'UDP'
}

export interface ServiceFormValues {
  kind: 'services'
  name: string
  type: 'ClusterIP' | 'NodePort' | 'LoadBalancer'
  selector: LabelEntry[]
  ports: ServicePortEntry[]
}

export interface IngressRuleEntry extends Entry {
  host: string
  path: string
  pathType: 'Prefix' | 'Exact' | 'ImplementationSpecific'
  serviceName: string
  servicePort: string
}

export interface IngressFormValues {
  kind: 'ingresses'
  name: string
  className: string
  rules: IngressRuleEntry[]
  /** Set to terminate TLS; the hosts are taken from the rules themselves. */
  tlsSecretName: string
}

export interface HTTPRouteRuleEntry extends Entry {
  path: string
  pathType: 'PathPrefix' | 'Exact'
  backendName: string
  backendPort: string
}

export interface TextEntry extends Entry {
  value: string
}

export interface HTTPRouteFormValues {
  kind: 'httproutes'
  name: string
  gatewayName: string
  /** Left empty, the Gateway is looked for in this object's own namespace. */
  gatewayNamespace: string
  hostnames: TextEntry[]
  rules: HTTPRouteRuleEntry[]
}

export interface ConfigMapEntry extends Entry {
  key: string
  value: string
}

export interface ConfigMapFormValues {
  kind: 'configmaps'
  name: string
  entries: ConfigMapEntry[]
}

export type ObjectFormValues =
  | WorkloadFormValues
  | ServiceFormValues
  | IngressFormValues
  | HTTPRouteFormValues
  | ConfigMapFormValues

/* ------------------------------------------------------- initial values ---- */

let entrySeq = 0

/** A key for a repeating row. It never reaches the manifest. */
export function entryId(): string {
  entrySeq += 1
  return `e${entrySeq}`
}

function emptyProbe(): ProbeValues {
  return { mode: 'none', path: '/healthz', port: '', initialDelaySeconds: '', periodSeconds: '' }
}

/**
 * What the form opens on. Deliberately close to empty: the starter manifests
 * are full of `example` because a manifest with holes in it does not parse,
 * whereas a form field left blank is simply a field left blank, and a default
 * nobody meant is the thing that reaches a cluster by accident.
 *
 * The two exceptions are the ones where a blank is a worse answer than a
 * convention: the container is named `app`, and the non-root security context
 * is on.
 */
export function initialObjectForm(kind: ObjectFormKind): ObjectFormValues {
  switch (kind) {
    case 'pods':
    case 'deployments':
    case 'cronjobs':
      return {
        kind,
        name: '',
        containerName: 'app',
        image: '',
        tag: '',
        replicas: kind === 'deployments' ? '2' : '',
        schedule: kind === 'cronjobs' ? '0 3 * * *' : '',
        timeZone: '',
        ports: [],
        env: [],
        cpuRequest: '50m',
        memoryRequest: '64Mi',
        cpuLimit: '250m',
        memoryLimit: '128Mi',
        serviceAccountName: '',
        nonRoot: true,
        liveness: emptyProbe(),
        readiness: emptyProbe(),
      }
    case 'services':
      return {
        kind,
        name: '',
        type: 'ClusterIP',
        selector: [{ id: entryId(), key: 'app', value: '' }],
        ports: [{ id: entryId(), name: 'http', port: '80', targetPort: '80', protocol: 'TCP' }],
      }
    case 'ingresses':
      return {
        kind,
        name: '',
        className: '',
        rules: [
          {
            id: entryId(),
            host: '',
            path: '/',
            pathType: 'Prefix',
            serviceName: '',
            servicePort: '80',
          },
        ],
        tlsSecretName: '',
      }
    case 'httproutes':
      return {
        kind,
        name: '',
        gatewayName: '',
        gatewayNamespace: '',
        hostnames: [{ id: entryId(), value: '' }],
        rules: [
          {
            id: entryId(),
            path: '/',
            pathType: 'PathPrefix',
            backendName: '',
            backendPort: '80',
          },
        ],
      }
    case 'configmaps':
      return { kind, name: '', entries: [{ id: entryId(), key: '', value: '' }] }
  }
}

/* ---------------------------------------------------------- yaml writer ---- */

/*
 * A writer rather than a library. The console already carries a highlighter it
 * wrote itself for the same reason (`YamlView`): the heaviest thing in this app
 * is a terminal nobody loads unless they open a shell, and emitting the seven
 * shapes below is not worth a dependency on every session. What it has to get
 * right is narrow — nested maps, sequences of maps, and quoting a scalar that
 * would otherwise read as something else — and each of those is asserted by the
 * rendering tests rather than trusted.
 */

interface YamlMap {
  [key: string]: YamlValue | undefined
}

type YamlValue = string | number | boolean | YamlValue[] | YamlMap

/** A scalar YAML would resolve to something other than a string. */
const YAML_LITERAL = /^(?:true|false|yes|no|on|off|null|~|-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?)$/i
/** Leading characters that start structure rather than a value. */
const YAML_LEADING = /^[-?:,[\]{}#&*!|>'"%@`]/

function needsQuote(text: string): boolean {
  if (text === '') return true
  if (YAML_LITERAL.test(text)) return true
  if (YAML_LEADING.test(text)) return true
  if (text !== text.trim()) return true
  // A colon only opens a mapping when a space follows it, which is why an image
  // reference (`nginx:1.27-alpine`) needs no quotes; a comment only starts
  // where a space precedes the hash.
  if (text.includes(': ') || text.includes(' #')) return true
  // An asterisk is an alias only in first position, but a cron schedule reads
  // so much like structure that quoting it is what every manifest does.
  if (text.includes('*')) return true
  return false
}

function scalar(value: string | number | boolean): string {
  if (typeof value !== 'string') return String(value)
  return needsQuote(value) ? JSON.stringify(value) : value
}

function isMap(value: YamlValue): value is YamlMap {
  return typeof value === 'object' && !Array.isArray(value)
}

/** emitMap writes one mapping's lines at `indent` spaces. */
function emitMap(map: YamlMap, indent: number): string[] {
  const pad = ' '.repeat(indent)
  const lines: string[] = []
  for (const [key, value] of Object.entries(map)) {
    if (value === undefined) continue
    if (Array.isArray(value)) {
      if (value.length === 0) {
        lines.push(`${pad}${key}: []`)
        continue
      }
      lines.push(`${pad}${key}:`)
      // The dash is indented under its key rather than sitting level with it.
      // Both parse; only one of them matches every other manifest this console
      // writes or shows, and a manifest that reads as if it came from somewhere
      // else is the one people distrust.
      lines.push(...emitSeq(value, indent + 2))
      continue
    }
    if (isMap(value)) {
      const inner = emitMap(value, indent + 2)
      if (inner.length === 0) {
        lines.push(`${pad}${key}: {}`)
        continue
      }
      lines.push(`${pad}${key}:`)
      lines.push(...inner)
      continue
    }
    if (typeof value === 'string' && value.includes('\n')) {
      // Somebody else's file. A block scalar keeps it byte for byte, and `|-`
      // versus `|` is the difference between a file that ends in a newline and
      // one that does not — a distinction a config parser can care about.
      const body = value.endsWith('\n') ? value.slice(0, -1) : value
      lines.push(`${pad}${key}: ${value.endsWith('\n') ? '|' : '|-'}`)
      for (const line of body.split('\n')) {
        lines.push(line === '' ? '' : `${pad}  ${line}`)
      }
      continue
    }
    lines.push(`${pad}${key}: ${scalar(value)}`)
  }
  return lines
}

/** emitSeq writes a sequence's entries, each dash at `indent` spaces. */
function emitSeq(seq: YamlValue[], indent: number): string[] {
  const pad = ' '.repeat(indent)
  const lines: string[] = []
  for (const item of seq) {
    if (Array.isArray(item)) {
      const inner = emitSeq(item, indent + 2)
      lines.push(`${pad}- ${inner[0].trimStart()}`, ...inner.slice(1))
      continue
    }
    if (isMap(item)) {
      const inner = emitMap(item, indent + 2)
      if (inner.length === 0) {
        lines.push(`${pad}- {}`)
        continue
      }
      lines.push(`${pad}- ${inner[0].trimStart()}`, ...inner.slice(1))
      continue
    }
    lines.push(`${pad}- ${scalar(item)}`)
  }
  return lines
}

/** emitDocument renders one object, always ending in a newline. */
function emitDocument(doc: YamlMap): string {
  return `${emitMap(doc, 0).join('\n')}\n`
}

/* ------------------------------------------------------------- helpers ----- */

const text = (value: string): string | undefined => {
  const trimmed = value.trim()
  return trimmed === '' ? undefined : trimmed
}

/**
 * A field the manifest wants as a number. Anything that is not a whole number
 * is left out rather than rendered as `NaN` — the field is still on screen, and
 * a manifest the API server rejects for a missing port is a better answer than
 * one it rejects for a nonsense one.
 */
function integer(value: string): number | undefined {
  const trimmed = value.trim()
  if (trimmed === '' || !/^-?\d+$/.test(trimmed)) return undefined
  return Number(trimmed)
}

/**
 * A port as Kubernetes accepts it in the places that take either: the number
 * where it is one, and the container's port *name* where it is not.
 */
function portRef(value: string): number | string | undefined {
  const port = integer(value)
  if (port !== undefined) return port
  return text(value)
}

function labelsFrom(entries: LabelEntry[]): YamlMap | undefined {
  const out: YamlMap = {}
  let any = false
  for (const entry of entries) {
    const key = text(entry.key)
    const value = text(entry.value)
    if (key === undefined || value === undefined) continue
    out[key] = value
    any = true
  }
  return any ? out : undefined
}

/** A container image, with the tag the form holds separately re-attached. */
function imageRef(values: WorkloadFormValues): string | undefined {
  const image = text(values.image)
  if (image === undefined) return undefined
  const tag = text(values.tag)
  return tag === undefined ? image : `${image}:${tag}`
}

/* -------------------------------------------------------- the renderings --- */

function probe(values: ProbeValues, fallbackPort: string): YamlMap | undefined {
  if (values.mode === 'none') return undefined
  const port = portRef(values.port.trim() === '' ? fallbackPort : values.port)
  if (port === undefined) return undefined
  const out: YamlMap = {}
  if (values.mode === 'http') {
    out.httpGet = { path: text(values.path) ?? '/', port }
  } else {
    out.tcpSocket = { port }
  }
  out.initialDelaySeconds = integer(values.initialDelaySeconds)
  out.periodSeconds = integer(values.periodSeconds)
  return out
}

function envList(entries: EnvEntry[]): YamlValue[] | undefined {
  const out: YamlValue[] = []
  for (const entry of entries) {
    const name = text(entry.name)
    if (name === undefined) continue
    if (entry.from === 'value') {
      // An empty value is a legitimate environment variable, so this is the one
      // place a blank is rendered rather than dropped.
      out.push({ name, value: entry.value })
      continue
    }
    const refName = text(entry.refName)
    const refKey = text(entry.refKey)
    if (refName === undefined || refKey === undefined) continue
    const ref = { name: refName, key: refKey }
    out.push({
      name,
      valueFrom: entry.from === 'configmap' ? { configMapKeyRef: ref } : { secretKeyRef: ref },
    })
  }
  return out.length === 0 ? undefined : out
}

function containerPorts(entries: PortEntry[]): YamlValue[] | undefined {
  const out: YamlValue[] = []
  for (const entry of entries) {
    const port = integer(entry.port)
    if (port === undefined) continue
    out.push({
      name: text(entry.name),
      containerPort: port,
      protocol: entry.protocol === 'TCP' ? undefined : entry.protocol,
    })
  }
  return out.length === 0 ? undefined : out
}

function resources(values: WorkloadFormValues): YamlMap | undefined {
  const requests: YamlMap = { cpu: text(values.cpuRequest), memory: text(values.memoryRequest) }
  const limits: YamlMap = { cpu: text(values.cpuLimit), memory: text(values.memoryLimit) }
  const hasRequests = requests.cpu !== undefined || requests.memory !== undefined
  const hasLimits = limits.cpu !== undefined || limits.memory !== undefined
  if (!hasRequests && !hasLimits) return undefined
  return { requests: hasRequests ? requests : undefined, limits: hasLimits ? limits : undefined }
}

/**
 * The container, and the pod spec around it.
 *
 * The security context is the field set's whole argument in one place: with it
 * on, what comes out declares non-root and forbids privilege escalation, which
 * is what the `no_nonroot_declaration` posture rule reads — an *absence*, not a
 * claim about what the image's own `USER` does. `capabilities: drop: [ALL]` is
 * deliberately **not** written: it is the rest of the restricted profile, no
 * posture rule reads it, and it stops a stock image binding a low port, which
 * would make the safe default the one people turn off.
 */
function podSpec(values: WorkloadFormValues): YamlMap {
  const ports = containerPorts(values.ports)
  const firstPort = values.ports.find((entry) => integer(entry.port) !== undefined)?.port ?? ''

  const container: YamlMap = {
    name: text(values.containerName) ?? 'app',
    image: imageRef(values),
    ports,
    env: envList(values.env),
    resources: resources(values),
    livenessProbe: probe(values.liveness, firstPort),
    readinessProbe: probe(values.readiness, firstPort),
    securityContext: values.nonRoot ? { allowPrivilegeEscalation: false } : undefined,
  }

  return {
    serviceAccountName: text(values.serviceAccountName),
    // A Job's pod may not restart forever; the CronJob path is the only one
    // here that has to say so.
    restartPolicy: values.kind === 'cronjobs' ? 'OnFailure' : undefined,
    securityContext: values.nonRoot
      ? { runAsNonRoot: true, seccompProfile: { type: 'RuntimeDefault' } }
      : undefined,
    containers: [container],
  }
}

function renderWorkload(values: WorkloadFormValues): string {
  const name = text(values.name) ?? 'example'
  const labels = { app: name }

  if (values.kind === 'pods') {
    return emitDocument({
      apiVersion: 'v1',
      kind: 'Pod',
      metadata: { name, labels },
      spec: podSpec(values),
    })
  }

  if (values.kind === 'deployments') {
    return emitDocument({
      apiVersion: 'apps/v1',
      kind: 'Deployment',
      metadata: { name, labels },
      spec: {
        replicas: integer(values.replicas),
        selector: { matchLabels: labels },
        template: { metadata: { labels }, spec: podSpec(values) },
      },
    })
  }

  return emitDocument({
    apiVersion: 'batch/v1',
    kind: 'CronJob',
    metadata: { name, labels },
    spec: {
      schedule: text(values.schedule) ?? '0 3 * * *',
      timeZone: text(values.timeZone),
      concurrencyPolicy: 'Forbid',
      successfulJobsHistoryLimit: 3,
      failedJobsHistoryLimit: 1,
      jobTemplate: {
        spec: { template: { metadata: { labels }, spec: podSpec(values) } },
      },
    },
  })
}

function renderService(values: ServiceFormValues): string {
  const ports: YamlValue[] = []
  for (const entry of values.ports) {
    const port = integer(entry.port)
    if (port === undefined) continue
    ports.push({
      name: text(entry.name),
      port,
      targetPort: portRef(entry.targetPort),
      protocol: entry.protocol === 'TCP' ? undefined : entry.protocol,
    })
  }

  return emitDocument({
    apiVersion: 'v1',
    kind: 'Service',
    metadata: { name: text(values.name) ?? 'example' },
    spec: {
      type: values.type === 'ClusterIP' ? undefined : values.type,
      selector: labelsFrom(values.selector),
      ports: ports.length === 0 ? undefined : ports,
    },
  })
}

/**
 * An Ingress rule is a host plus its paths, but the form asks for one path at a
 * time — nobody thinks in "rules", they think in "this URL goes to that
 * service". So the rows are grouped back into hosts here, in the order the
 * hosts were first named, and rows with no host at all become the single
 * hostless rule that matches anything.
 */
function renderIngress(values: IngressFormValues): string {
  const order: string[] = []
  const byHost = new Map<string, YamlValue[]>()

  for (const rule of values.rules) {
    const serviceName = text(rule.serviceName)
    const port = portRef(rule.servicePort)
    if (serviceName === undefined || port === undefined) continue
    const host = text(rule.host) ?? ''
    if (!byHost.has(host)) {
      byHost.set(host, [])
      order.push(host)
    }
    byHost.get(host)?.push({
      path: text(rule.path) ?? '/',
      pathType: rule.pathType,
      backend: { service: { name: serviceName, port: { number: port } } },
    })
  }

  const rules: YamlValue[] = order.map((host) => ({
    host: host === '' ? undefined : host,
    http: { paths: byHost.get(host) ?? [] },
  }))

  const tlsSecret = text(values.tlsSecretName)
  const tlsHosts = order.filter((host) => host !== '')

  return emitDocument({
    apiVersion: 'networking.k8s.io/v1',
    kind: 'Ingress',
    metadata: { name: text(values.name) ?? 'example' },
    spec: {
      ingressClassName: text(values.className),
      tls:
        tlsSecret === undefined
          ? undefined
          : [{ hosts: tlsHosts.length === 0 ? undefined : tlsHosts, secretName: tlsSecret }],
      rules: rules.length === 0 ? undefined : rules,
    },
  })
}

function renderHTTPRoute(values: HTTPRouteFormValues): string {
  const hostnames: YamlValue[] = []
  for (const entry of values.hostnames) {
    const host = text(entry.value)
    if (host !== undefined) hostnames.push(host)
  }

  const rules: YamlValue[] = []
  for (const rule of values.rules) {
    const backendName = text(rule.backendName)
    if (backendName === undefined) continue
    rules.push({
      matches: [{ path: { type: rule.pathType, value: text(rule.path) ?? '/' } }],
      backendRefs: [{ name: backendName, port: integer(rule.backendPort) }],
    })
  }

  const gateway = text(values.gatewayName)

  return emitDocument({
    apiVersion: 'gateway.networking.k8s.io/v1',
    kind: 'HTTPRoute',
    metadata: { name: text(values.name) ?? 'example' },
    spec: {
      parentRefs:
        gateway === undefined
          ? undefined
          : [{ name: gateway, namespace: text(values.gatewayNamespace) }],
      hostnames: hostnames.length === 0 ? undefined : hostnames,
      rules: rules.length === 0 ? undefined : rules,
    },
  })
}

function renderConfigMap(values: ConfigMapFormValues): string {
  const data: YamlMap = {}
  let any = false
  for (const entry of values.entries) {
    const key = text(entry.key)
    if (key === undefined) continue
    // The value is taken verbatim — a whole file pasted into the box keeps its
    // own indentation, which is what the block scalar in the writer is for.
    data[key] = entry.value
    any = true
  }

  return emitDocument({
    apiVersion: 'v1',
    kind: 'ConfigMap',
    metadata: { name: text(values.name) ?? 'example' },
    data: any ? data : {},
  })
}

/**
 * The manifest these values describe. The one output of this whole module: text
 * for the editor that is already there, which the existing create route posts.
 */
export function renderObjectManifest(values: ObjectFormValues): string {
  switch (values.kind) {
    case 'pods':
    case 'deployments':
    case 'cronjobs':
      return renderWorkload(values)
    case 'services':
      return renderService(values)
    case 'ingresses':
      return renderIngress(values)
    case 'httproutes':
      return renderHTTPRoute(values)
    case 'configmaps':
      return renderConfigMap(values)
  }
}

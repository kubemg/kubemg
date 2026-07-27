import type {
  DatasourceKind,
  DatasourceProvider,
  ObservabilitySource,
} from '../api/types'
import type { Tone } from './status'

/**
 * What KubeMG knows about each series backend, in one place: the sidebar of
 * choices, the port a fresh form starts on, and the path prefix that is the
 * single most common reason a correct address answers 404.
 *
 * The backend validates all of this again — this table exists so the form can
 * fill itself in, not so the browser can decide what is allowed.
 */
export interface ProviderInfo {
  label: string
  kind: DatasourceKind
  /** The port the provider's own charts conventionally serve its query API on. */
  defaultPort: string
  /** Where that API lives when it is not at the root. */
  defaultPrefix: string
  /** What an operator should recognise, in one line. */
  hint: string
}

export const PROVIDERS: Record<DatasourceProvider, ProviderInfo> = {
  victoriametrics: {
    label: 'VictoriaMetrics',
    kind: 'metrics',
    defaultPort: '8428',
    defaultPrefix: '',
    hint: 'Single-node serves the Prometheus API at the root on 8428. A cluster install answers on vmselect:8481 under /select/0/prometheus.',
  },
  prometheus: {
    label: 'Prometheus',
    kind: 'metrics',
    defaultPort: '9090',
    defaultPrefix: '',
    hint: 'The Service kube-prometheus-stack installs, usually in the monitoring namespace.',
  },
  thanos: {
    label: 'Thanos',
    kind: 'metrics',
    defaultPort: '9090',
    defaultPrefix: '',
    hint: 'Point at the Querier, not at a sidecar or the store gateway.',
  },
  mimir: {
    label: 'Mimir',
    kind: 'metrics',
    defaultPort: '8080',
    defaultPrefix: '/prometheus',
    hint: 'Point at the query frontend or the gateway. A multi-tenant Mimir also needs its tenant header, which KubeMG does not send yet.',
  },
  victorialogs: {
    label: 'VictoriaLogs',
    kind: 'logs',
    defaultPort: '9428',
    defaultPrefix: '',
    hint: 'Single-node serves LogsQL on 9428.',
  },
  loki: {
    label: 'Loki',
    kind: 'logs',
    defaultPort: '3100',
    defaultPrefix: '',
    hint: 'Point at the gateway or the query frontend, not at an ingester.',
  },
}

export const METRICS_PROVIDERS: DatasourceProvider[] = [
  'victoriametrics',
  'prometheus',
  'thanos',
  'mimir',
]

export const LOGS_PROVIDERS: DatasourceProvider[] = ['victorialogs', 'loki']

export function providersFor(kind: DatasourceKind): DatasourceProvider[] {
  return kind === 'metrics' ? METRICS_PROVIDERS : LOGS_PROVIDERS
}

export const KIND_LABEL: Record<DatasourceKind, string> = {
  metrics: 'Metrics',
  logs: 'Logs',
}

/** What each kind is *for*, said once rather than in every panel. */
export const KIND_PURPOSE: Record<DatasourceKind, string> = {
  metrics:
    'History behind the live meters. Without it KubeMG can only show the last couple of minutes the cluster keeps itself.',
  logs: 'Searchable logs across pods. Without it a log is only readable while the pod that wrote it is still alive.',
}

export const DATASOURCE_KINDS: DatasourceKind[] = ['metrics', 'logs']

/** How a source's last check reads on the deck. */
export function sourceTone(source: ObservabilitySource): Tone {
  if (!source.enabled) return 'idle'
  if (source.last_status === 'healthy') return 'ok'
  if (source.last_status === 'unhealthy') return 'bad'
  return 'idle'
}

export function sourceStateLabel(source: ObservabilitySource): string {
  if (!source.enabled) return 'Paused'
  if (source.last_status === 'healthy') return 'Answering'
  if (source.last_status === 'unhealthy') return 'Not answering'
  return 'Never checked'
}

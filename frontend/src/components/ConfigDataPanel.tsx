import { useEffect, useState } from 'react'
import { errorMessage, fetchDataEntries } from '../api/client'
import type { Cluster, DataEntries, DataEntry } from '../api/types'
import { useAuth } from '../state/auth-context'
import { SecretKeyValues } from './SecretRevealSheet'
import { CodeBlock, Notice, Pill } from './primitives'

/**
 * What a ConfigMap or a Secret holds — the thing it is opened for, which its
 * metadata is not.
 *
 * A ConfigMap's values are drawn one key at a time, each in its own block: the
 * same values its YAML tab has always shown, read the way they are used.
 *
 * A Secret is drawn as its keys and their sizes, and nothing else arrives with
 * them. Each key reveals on its own through the audited route — for a caller a
 * super admin has granted that capability; anybody else is told who grants it
 * rather than offered a button that can only refuse.
 */
export function ConfigDataPanel({
  cluster,
  kind,
  name,
  namespace,
}: {
  cluster: Cluster
  kind: 'configmaps' | 'secrets'
  name: string
  namespace: string
}) {
  const { user } = useAuth()
  const [data, setData] = useState<DataEntries | null>(null)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    let live = true
    setError(null)
    fetchDataEntries(cluster.id, kind, name, namespace)
      .then((answer) => {
        if (live) setData(answer)
      })
      .catch((err) => {
        if (live) setError(errorMessage(err, 'Could not read what this object holds.'))
      })
    return () => {
      live = false
    }
  }, [cluster.id, kind, name, namespace])

  if (error) return <Notice tone="error">{error}</Notice>
  if (!data) return <p className="text-[13px] text-muted">Reading its keys…</p>

  return <ConfigDataView cluster={cluster} name={name} namespace={namespace} data={data} canReveal={Boolean(user?.can_reveal_secrets)} />
}

/** The entries without the read, so what is and is not drawn is testable. */
export function ConfigDataView({
  cluster,
  name,
  namespace,
  data,
  canReveal,
}: {
  cluster: Cluster
  name: string
  namespace: string
  data: DataEntries
  canReveal: boolean
}) {
  const summary = (
    <div className="flex flex-wrap items-center gap-2 text-[12.5px] text-muted">
      <span>
        {data.entries.length} {data.entries.length === 1 ? 'key' : 'keys'}
      </span>
      {data.type ? <Pill tone="idle" dot={false}>{data.type}</Pill> : null}
      {data.immutable ? <Pill tone="idle" dot={false}>immutable</Pill> : null}
    </div>
  )

  if (data.kind === 'Secret') {
    return (
      <div className="flex flex-col gap-3">
        {summary}
        {canReveal ? (
          <SecretKeyValues
            cluster={cluster}
            namespace={namespace}
            name={name}
            keys={data.entries.map((entry) => ({ key: entry.key, bytes: entry.bytes }))}
          />
        ) : (
          <>
            <Notice tone="info">
              Values are not shown. Revealing one needs the reveal-secrets capability, which a super
              admin grants — and every reveal is recorded in the audit trail.
            </Notice>
            <EntryList entries={data.entries} />
          </>
        )}
      </div>
    )
  }

  if (data.entries.length === 0) {
    return (
      <div className="flex flex-col gap-3">
        {summary}
        <p className="text-[13px] text-muted">This ConfigMap holds no data.</p>
      </div>
    )
  }

  return (
    <div className="flex flex-col gap-3">
      {summary}
      {data.entries.map((entry) => (
        <div key={entry.key} className="flex flex-col gap-1.5">
          <div className="flex items-center justify-between gap-3">
            <span className="truncate font-data text-[12.5px] font-semibold text-fg">{entry.key}</span>
            <span className="shrink-0 text-[12px] text-faint">{entry.bytes} bytes</span>
          </div>
          {entry.binary ? (
            <p className="text-[12.5px] text-muted">Binary data — not shown as text.</p>
          ) : (
            <CodeBlock value={entry.value ?? ''} wrap />
          )}
          {entry.truncated ? (
            <p className="text-[12px] text-muted">
              Cut at 64 KiB here; the YAML tab has the whole value.
            </p>
          ) : null}
        </div>
      ))}
    </div>
  )
}

function EntryList({ entries }: { entries: DataEntry[] }) {
  return (
    <ul className="flex flex-col divide-y divide-line-soft rounded-card border border-line-soft">
      {entries.map((entry) => (
        <li key={entry.key} className="flex items-center justify-between gap-3 px-3 py-2">
          <span className="truncate font-data text-[12.5px] text-fg">{entry.key}</span>
          <span className="shrink-0 text-[12px] text-faint">{entry.bytes} bytes</span>
        </li>
      ))}
    </ul>
  )
}

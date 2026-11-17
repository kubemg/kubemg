import { useRef, useState } from 'react'
import type { ChangeEvent } from 'react'
import { RefreshCw, Upload } from 'lucide-react'
import { errorMessage, fetchVulnIntel, putVulnIntel, syncVulnIntel, uploadVulnIntel } from '../api/client'
import type { VulnIntel, VulnIntelKind, VulnIntelSource } from '../api/types'
import { queryKey, useCachedQuery } from '../lib/query'
import { Age, Button, Field, Notice, Pill, Sheet, Switch, TextInput } from './primitives'

const COPY: Record<VulnIntelKind, { title: string; body: string; file: string }> = {
  kev: {
    title: 'Known exploited (CISA KEV)',
    body:
      'CISA’s catalogue of vulnerabilities being exploited in the wild. A finding on it makes its workload red, whatever its severity, and a workload that starts running one can raise the vulnerability_kev alarm.',
    file: 'known_exploited_vulnerabilities.json',
  },
  epss: {
    title: 'Exploit prediction (FIRST EPSS)',
    body:
      'The probability each CVE is exploited in the next 30 days. It orders the findings in a workload’s panel; it colours nothing. Scores under 1% are not kept.',
    file: 'epss_scores-current.csv.gz',
  },
}

/**
 * The server-wide exploit-intelligence lists. Both are off until an
 * administrator turns them on, because on is this server reaching the
 * internet; an air-gapped install uploads the file instead. Nothing about any
 * cluster leaves — the whole list comes down and is matched here.
 */
export function VulnIntelSheet({ onClose }: { onClose: () => void }) {
  const state = useCachedQuery(queryKey('vuln-intel'), fetchVulnIntel)
  const [current, setCurrent] = useState<VulnIntel | null>(null)
  const data = current ?? state.data ?? null

  return (
    <Sheet title="Exploit intelligence" eyebrow="Vulnerability alerts" width="lg" onClose={onClose}>
      <div className="flex flex-col gap-5">
        <p className="text-[13px] text-muted">
          Two public lists decide which findings come first. They apply to every cluster with vulnerability alerts.
          Turning one on lets this server download it once a day; an uploaded file is used whether or not that is on.
        </p>
        {state.error ? <Notice tone="error">{errorMessage(state.error, 'Could not read the lists.')}</Notice> : null}
        {data
          ? data.sources.map((source) => <IntelSource key={source.kind} source={source} onChange={setCurrent} />)
          : null}
      </div>
    </Sheet>
  )
}

function IntelSource({ source, onChange }: { source: VulnIntelSource; onChange: (next: VulnIntel) => void }) {
  const copy = COPY[source.kind]
  const [url, setUrl] = useState(source.url)
  const [busy, setBusy] = useState<'save' | 'sync' | 'upload' | null>(null)
  const [error, setError] = useState<string | null>(null)
  const file = useRef<HTMLInputElement>(null)

  async function run(kind: 'save' | 'sync' | 'upload', action: () => Promise<VulnIntel>) {
    setBusy(kind)
    setError(null)
    try {
      onChange(await action())
    } catch (err) {
      setError(errorMessage(err, 'That did not work.'))
    } finally {
      setBusy(null)
    }
  }

  function pick(event: ChangeEvent<HTMLInputElement>) {
    const chosen = event.target.files?.[0]
    event.target.value = ''
    if (chosen) void run('upload', () => uploadVulnIntel(source.kind, chosen))
  }

  const failed = source.status === 'unhealthy'
  return (
    <section className="card flex flex-col gap-3 px-4 py-4">
      <header className="flex flex-wrap items-center gap-3">
        <h3 className="text-[14px] font-semibold text-fg">{copy.title}</h3>
        {source.entries > 0 ? (
          <Pill tone={failed ? 'warn' : 'ok'}>
            {source.entries.toLocaleString()} {source.kind === 'kev' ? 'entries' : 'scores'}
          </Pill>
        ) : (
          <Pill tone="idle">no list</Pill>
        )}
        <span className="ml-auto flex items-center gap-2 text-[12.5px] text-muted">
          Download daily
          <Switch
            checked={source.enabled}
            disabled={busy !== null}
            label={`Download ${copy.title} daily`}
            onChange={(next) => void run('save', () => putVulnIntel(source.kind, next, url))}
          />
        </span>
      </header>
      <p className="text-[12.5px] text-muted">{copy.body}</p>
      {source.synced_at ? (
        <p className="text-[12.5px] text-muted">
          {source.origin === 'upload' ? 'Uploaded' : 'Downloaded'} <Age iso={source.synced_at} />
          {source.version ? <> - list version {source.version}</> : null}.
        </p>
      ) : null}
      {source.message && (failed || !source.synced_at) ? (
        <Notice tone={failed ? 'warn' : 'info'}>
          {source.message}
          {failed && source.entries > 0 ? ' The list already held is still used.' : ''}
        </Notice>
      ) : null}
      {error ? <Notice tone="error">{error}</Notice> : null}
      <Field label="Address" htmlFor={`intel-url-${source.kind}`} hint="Leave empty for the default. KEV defaults to CISA's GitHub publication of the catalogue; the cisa.gov feed is the same file.">
        <div className="flex flex-wrap gap-2">
          <TextInput
            id={`intel-url-${source.kind}`}
            className="min-w-0 flex-1 font-mono text-[12.5px]"
            value={url}
            onChange={(event) => setUrl(event.target.value)}
          />
          <Button
            variant="secondary"
            disabled={busy !== null || url.trim() === source.url}
            onClick={() => void run('save', () => putVulnIntel(source.kind, source.enabled, url.trim()))}
          >
            Save address
          </Button>
        </div>
      </Field>
      <div className="flex flex-wrap items-center gap-2">
        <Button variant="secondary" disabled={busy !== null} onClick={() => void run('sync', () => syncVulnIntel(source.kind))}>
          <RefreshCw aria-hidden="true" className={`size-4 ${busy === 'sync' ? 'animate-spin' : ''}`} />
          {busy === 'sync' ? 'Downloading…' : 'Download now'}
        </Button>
        <Button variant="secondary" disabled={busy !== null} onClick={() => file.current?.click()}>
          <Upload aria-hidden="true" className="size-4" />
          {busy === 'upload' ? 'Uploading…' : 'Upload a file'}
        </Button>
        <span className="text-[12px] text-muted">
          e.g. <span className="font-mono">{copy.file}</span>
        </span>
        <input ref={file} type="file" className="sr-only" onChange={pick} aria-label={`Upload ${copy.title}`} />
      </div>
    </section>
  )
}

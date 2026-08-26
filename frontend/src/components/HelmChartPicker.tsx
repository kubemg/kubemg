import { useEffect, useState } from 'react'
import { errorMessage, fetchHelmCharts, fetchHelmRepositories } from '../api/client'
import type { HelmChart, HelmRepository } from '../api/types'
import { Field, Notice, SearchInput, Select } from './primitives'

/** A chart, at one of its published versions — what an install or a chart
    upgrade addresses. */
export interface HelmChartSelection {
  repository: string
  chart: string
  version: string
}

/**
 * Repository, then chart, then version — the three questions an install or a
 * chart upgrade has to answer, in the order a repository's catalogue actually
 * narrows them.
 *
 * The catalogue is read from the server-wide repositories every signed-in user
 * may browse (`GET /helm/repositories*`, open regardless of role — see
 * `pkg/db/helm_models.go`), never from the cluster: which charts exist has
 * nothing to do with which cluster the install is headed for.
 */
export function HelmChartPicker({
  selection,
  onChange,
}: {
  selection: HelmChartSelection
  onChange: (next: HelmChartSelection) => void
}) {
  const [repositories, setRepositories] = useState<HelmRepository[] | null>(null)
  const [charts, setCharts] = useState<HelmChart[] | null>(null)
  const [query, setQuery] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [loading, setLoading] = useState(false)

  useEffect(() => {
    void fetchHelmRepositories()
      .then((result) => {
        setRepositories(result.repositories)
        // The first repository with a catalogue is worth landing on; one that
        // has never synced has nothing to search yet.
        if (!selection.repository) {
          const first = result.repositories.find((repository) => repository.chart_count > 0)
          if (first) onChange({ ...selection, repository: first.name })
        }
      })
      .catch((err) => setError(errorMessage(err, 'Could not read the chart repositories.')))
    // Read once: the catalogue a picker searches does not change while it is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    if (!selection.repository) {
      setCharts(null)
      return
    }
    let live = true
    setLoading(true)
    const handle = window.setTimeout(() => {
      void fetchHelmCharts(selection.repository, query)
        .then((result) => {
          if (!live) return
          setCharts(result.charts)
          setError(null)
        })
        .catch((err) => {
          if (!live) return
          setError(errorMessage(err, 'Could not read this repository’s charts.'))
          setCharts(null)
        })
        .finally(() => {
          if (live) setLoading(false)
        })
      // A short debounce: searching a catalogue of a few hundred charts on every
      // keystroke is a round trip nobody typing a name needs.
    }, 250)
    return () => {
      live = false
      window.clearTimeout(handle)
    }
  }, [selection.repository, query])

  const chart = charts?.find((entry) => entry.name === selection.chart) ?? null

  return (
    <div className="flex flex-col gap-4">
      {error ? <Notice tone="error">{error}</Notice> : null}

      <Field label="Repository" htmlFor="helm_repository">
        <Select
          id="helm_repository"
          value={selection.repository}
          onChange={(event) =>
            onChange({ repository: event.target.value, chart: '', version: '' })
          }
        >
          {!repositories ? <option value="">Reading repositories…</option> : null}
          {repositories?.length === 0 ? <option value="">No repositories configured</option> : null}
          {repositories?.map((repository) => (
            <option key={repository.name} value={repository.name}>
              {repository.name}
              {repository.status === 'error' ? ' (last sync failed)' : ''}
            </option>
          ))}
        </Select>
      </Field>

      <Field label="Chart" htmlFor="helm_chart_search" hint={chart?.description}>
        <SearchInput
          value={query}
          onChange={setQuery}
          placeholder="Search this repository’s charts"
          label="Search charts"
        />
        <div className="mt-2 max-h-52 overflow-y-auto rounded-control border border-line">
          {loading ? <p className="px-3 py-2 text-[12.5px] text-muted">Searching…</p> : null}
          {!loading && charts?.length === 0 ? (
            <p className="px-3 py-2 text-[12.5px] text-muted">No charts matched.</p>
          ) : null}
          {!loading &&
            charts?.map((entry) => (
              <button
                key={entry.name}
                type="button"
                onClick={() =>
                  onChange({
                    repository: selection.repository,
                    chart: entry.name,
                    version: entry.latest_version ?? entry.versions[0]?.version ?? '',
                  })
                }
                className={`flex w-full flex-col gap-0.5 border-b border-line-soft px-3 py-2 text-left last:border-b-0 transition-colors hover:bg-raised ${
                  entry.name === selection.chart ? 'bg-accent-soft' : ''
                }`}
              >
                <span className="truncate text-[13px] font-medium text-fg">
                  {entry.name}
                  {entry.deprecated ? (
                    <span className="ml-1.5 text-[11px] text-warn">deprecated</span>
                  ) : null}
                </span>
                {entry.description ? (
                  <span className="truncate text-[12px] text-muted">{entry.description}</span>
                ) : null}
              </button>
            ))}
        </div>
      </Field>

      {chart ? (
        <Field label="Version" htmlFor="helm_chart_version">
          <Select
            id="helm_chart_version"
            value={selection.version}
            onChange={(event) =>
              onChange({ ...selection, version: event.target.value })
            }
          >
            {chart.versions.map((version) => (
              <option key={version.version} value={version.version}>
                {version.version}
                {version.app_version ? ` (app ${version.app_version})` : ''}
                {version.deprecated ? ' — deprecated' : ''}
              </option>
            ))}
          </Select>
        </Field>
      ) : null}
    </div>
  )
}

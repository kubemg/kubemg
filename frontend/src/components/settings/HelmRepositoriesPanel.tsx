import { useCallback, useEffect, useState } from 'react'
import { CheckCircle2, Pencil, Plus, RefreshCw, Trash2, XCircle } from 'lucide-react'
import {
  deleteHelmRepository,
  errorMessage,
  fetchHelmRepositories,
  putHelmRepository,
  syncHelmRepository,
} from '../../api/client'
import type { HelmRepository, HelmRepositoryInput } from '../../api/types'
import { relativeAge } from '../../lib/time'
import { Button, Field, IconButton, Notice, Panel, Pill, Sheet, TextInput } from '../primitives'
import { useConfirm } from '../../state/confirm-context'
import { useResult } from '../../state/result-context'

/**
 * Where charts may be installed from.
 *
 * A repository is server-wide rather than per-cluster — what this installation
 * may reach out to and download templates from is a fact about the
 * installation, not about any one cluster, and duplicating it per cluster would
 * mean an operator adding a mirror once per cluster and a fleet where half the
 * clusters can install cert-manager. See `pkg/db/helm_models.go`.
 *
 * Adding one is an outbound-egress decision, the same class of act as
 * registering an alarm channel, so writing is admin-only; reading the
 * catalogue is open to anyone the console is open to, because an install form
 * cannot discover the list by being refused.
 */
export function HelmRepositoriesPanel() {
  const confirm = useConfirm()
  const report = useResult()
  const [repositories, setRepositories] = useState<HelmRepository[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [editing, setEditing] = useState<HelmRepository | 'new' | null>(null)
  const [syncing, setSyncing] = useState<string | null>(null)

  const load = useCallback(async () => {
    try {
      const result = await fetchHelmRepositories()
      setRepositories(result.repositories)
      setError(null)
    } catch (err) {
      setError(errorMessage(err, 'Could not load the chart repositories.'))
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => {
    void load()
  }, [load])

  async function sync(repository: HelmRepository) {
    setSyncing(repository.name)
    try {
      await syncHelmRepository(repository.name)
      await load()
    } catch (err) {
      setError(errorMessage(err, 'Could not sync this repository.'))
    } finally {
      setSyncing(null)
    }
  }

  async function remove(repository: HelmRepository) {
    const confirmed = await confirm({
      eyebrow: 'Chart repository',
      title: `Remove “${repository.name}”?`,
      body: 'Its cached chart list goes with it, so nothing from this repository can be installed until it is added again. Releases already installed from it keep working.',
      confirmLabel: 'Remove',
    })
    if (!confirmed) return
    try {
      await deleteHelmRepository(repository.name)
      await load()
      report({
        tone: 'ok',
        title: `Removed ${repository.name}`,
        body: 'Its cached chart list went with it. Releases installed from it keep working.',
      })
    } catch (err) {
      const message = errorMessage(err, 'Could not remove this repository.')
      setError(message)
      report({ tone: 'error', title: `${repository.name} was not removed`, body: message })
    }
  }

  return (
    <>
      <Panel
        eyebrow="Helm"
        title="Chart repositories"
        description="Where an install or a chart upgrade may pull a chart from. Reading the catalogue is open to anyone signed in; adding a repository is an outbound-egress decision, so only an admin may."
        bodyClassName="flex flex-col"
        actions={
          <Button size="sm" onClick={() => setEditing('new')}>
            <Plus aria-hidden="true" className="size-3.5" />
            Add repository
          </Button>
        }
      >
        {error ? (
          <div className="px-4 pt-4">
            <Notice tone="error">{error}</Notice>
          </div>
        ) : null}

        {loading ? <p className="px-4 py-6 text-[13px] text-muted">Loading…</p> : null}

        {!loading && repositories.length === 0 ? (
          <p className="px-4 py-6 text-[13px] text-muted">
            No repositories configured. Add one and its charts become installable from Explore.
          </p>
        ) : null}

        <ul className="divide-y divide-line-soft">
          {repositories.map((repository) => (
            <li key={repository.name} className="flex flex-wrap items-start gap-3 px-4 py-3">
              <div className="min-w-0 flex-1">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="truncate text-[13.5px] font-medium text-fg">
                    {repository.name}
                  </span>
                  <StatusPill repository={repository} />
                  {repository.seeded ? <Pill tone="idle">seeded</Pill> : null}
                  {repository.has_credential ? <Pill tone="ok">credential stored</Pill> : null}
                  <Pill tone="idle" dot={false}>
                    {repository.chart_count} chart{repository.chart_count === 1 ? '' : 's'}
                  </Pill>
                </div>
                <p className="mt-1 truncate font-mono text-[12px] text-muted">{repository.url}</p>
                {repository.description ? (
                  <p className="mt-1 text-[12px] text-muted">{repository.description}</p>
                ) : null}
                {repository.status === 'error' && repository.status_message ? (
                  <p className="mt-1 text-[12px] text-danger">{repository.status_message}</p>
                ) : null}
                <p className="mt-1 text-[12px] text-faint">
                  {repository.synced_at
                    ? `Last synced ${relativeAge(repository.synced_at)}`
                    : 'Never synced'}
                </p>
              </div>

              <div className="flex shrink-0 items-center gap-0.5">
                <IconButton
                  type="button"
                  label={`Sync ${repository.name} now`}
                  onClick={() => void sync(repository)}
                  disabled={syncing === repository.name}
                >
                  <RefreshCw
                    aria-hidden="true"
                    className={`size-4 ${syncing === repository.name ? 'animate-spin' : ''}`}
                  />
                </IconButton>
                <IconButton
                  type="button"
                  label={`Edit ${repository.name}`}
                  onClick={() => setEditing(repository)}
                >
                  <Pencil aria-hidden="true" className="size-4" />
                </IconButton>
                <IconButton
                  type="button"
                  tone="danger"
                  label={`Remove ${repository.name}`}
                  onClick={() => void remove(repository)}
                >
                  <Trash2 aria-hidden="true" className="size-4" />
                </IconButton>
              </div>
            </li>
          ))}
        </ul>
      </Panel>

      {editing ? (
        <RepositorySheet
          repository={editing === 'new' ? null : editing}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null)
            void load()
          }}
        />
      ) : null}
    </>
  )
}

function StatusPill({ repository }: { repository: HelmRepository }) {
  if (repository.status === 'ok') {
    return (
      <Pill tone="ok" dot={false}>
        <CheckCircle2 aria-hidden="true" className="size-3" />
        synced
      </Pill>
    )
  }
  if (repository.status === 'error') {
    return (
      <Pill tone="bad" dot={false}>
        <XCircle aria-hidden="true" className="size-3" />
        sync failed
      </Pill>
    )
  }
  return <Pill tone="idle">pending</Pill>
}

function RepositorySheet({
  repository,
  onClose,
  onSaved,
}: {
  repository: HelmRepository | null
  onClose: () => void
  onSaved: () => void
}) {
  const [name, setName] = useState(repository?.name ?? '')
  const [url, setUrl] = useState(repository?.url ?? '')
  const [description, setDescription] = useState(repository?.description ?? '')
  const [username, setUsername] = useState(repository?.username ?? '')
  // Always blank: the stored credential is never read back, and leaving it
  // empty is what keeps it.
  const [credential, setCredential] = useState('')
  const [clearCredential, setClearCredential] = useState(false)

  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [warning, setWarning] = useState<string | null>(null)

  async function save() {
    setBusy(true)
    setError(null)
    setWarning(null)
    try {
      const input: HelmRepositoryInput = {
        url: url.trim(),
        description: description.trim(),
        username: username.trim(),
        // A typed credential replaces the stored one; the "clear" checkbox
        // sends '' to remove it; leaving both alone omits the field entirely,
        // which is what keeps whatever is already stored.
        credential: credential.trim() ? credential.trim() : clearCredential ? '' : undefined,
      }
      const result = await putHelmRepository(name.trim(), input)
      if (result.warning) {
        setWarning(result.warning)
        return
      }
      onSaved()
    } catch (err) {
      setError(errorMessage(err, 'Could not save this repository.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      width="lg"
      eyebrow="Helm"
      title={repository ? `Edit ${repository.name}` : 'New chart repository'}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            {warning ? 'Close' : 'Cancel'}
          </Button>
          {warning ? null : (
            <Button variant="primary" disabled={busy || !name.trim() || !url.trim()} onClick={() => void save()}>
              {busy ? 'Saving…' : 'Save repository'}
            </Button>
          )}
        </>
      }
    >
      <div className="flex flex-col gap-4">
        {error ? <Notice tone="error">{error}</Notice> : null}
        {warning ? (
          <Notice tone="warn">Saved, but the chart list could not be read: {warning}</Notice>
        ) : null}

        <Field
          label="Name"
          htmlFor="helm_repo_name"
          hint={repository ? 'The address — renaming means adding a new repository.' : 'What an install names as its source.'}
        >
          <TextInput
            id="helm_repo_name"
            value={name}
            disabled={repository !== null}
            onChange={(event) => setName(event.target.value)}
            placeholder="ingress-nginx"
          />
        </Field>

        <Field
          label="URL"
          htmlFor="helm_repo_url"
          hint="The repository's index, read by the bastion on a schedule and on every Sync."
        >
          <TextInput
            id="helm_repo_url"
            className="font-mono text-[12.5px]"
            value={url}
            onChange={(event) => setUrl(event.target.value)}
            placeholder="https://charts.example.com"
          />
        </Field>

        <Field label="Description" htmlFor="helm_repo_description">
          <TextInput
            id="helm_repo_description"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>

        <Field label="Username" htmlFor="helm_repo_username" hint="Leave blank for an anonymous repository.">
          <TextInput
            id="helm_repo_username"
            value={username}
            onChange={(event) => setUsername(event.target.value)}
          />
        </Field>

        <Field
          label="Credential"
          htmlFor="helm_repo_credential"
          hint={
            repository?.has_credential
              ? 'A credential is stored. Leave this empty to keep it — it is never read back out.'
              : 'Stored and never returned by the API.'
          }
        >
          <TextInput
            id="helm_repo_credential"
            type="password"
            autoComplete="new-password"
            className="font-mono text-[12.5px]"
            value={credential}
            disabled={clearCredential}
            onChange={(event) => setCredential(event.target.value)}
            placeholder={repository?.has_credential ? '••••••••' : ''}
          />
        </Field>

        {repository?.has_credential ? (
          <label className="flex items-center gap-2.5">
            <input
              type="checkbox"
              className="size-4 accent-[var(--color-accent)]"
              checked={clearCredential}
              onChange={(event) => setClearCredential(event.target.checked)}
            />
            <span className="text-[13px] text-fg">Clear the stored credential</span>
          </label>
        ) : null}
      </div>
    </Sheet>
  )
}

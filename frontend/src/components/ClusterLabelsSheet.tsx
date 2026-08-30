import { useState } from 'react'
import type { FormEvent } from 'react'
import { errorMessage, updateClusterLabels } from '../api/client'
import type { Cluster, Environment } from '../api/types'
import { MAX_SHORT_NAME, deriveChip, normalizeShortName, railChip } from '../lib/branding'
import { Button, EnvironmentTag, Field, Notice, Sheet, TextInput } from './primitives'

const ENVIRONMENTS: Environment[] = ['prod', 'staging', 'dev']

/**
 * Editing what a cluster is called.
 *
 * These three fields are the whole of what a registered cluster can be edited
 * into, and the boundary is deliberate: an API URL, a CA or a stored token is
 * this cluster's identity as far as every kubeconfig, grant and audit record
 * already pointing at the row is concerned, so changing one in place would
 * silently re-aim all of them. That is a delete and a fresh registration, and it
 * should look like one.
 *
 * What was wrong before is that the *labels* were equally frozen. An operator
 * who mistyped an environment at registration had to delete the cluster — and
 * with it every grant on it — to correct a coloured dot, and a fleet that
 * predates the rail chip had no way to acquire one at all.
 */
export function ClusterLabelsSheet({
  cluster,
  onClose,
  onSaved,
}: {
  cluster: Cluster
  onClose: () => void
  onSaved: () => Promise<void> | void
}) {
  const [shortName, setShortName] = useState(cluster.short_name ?? '')
  const [environment, setEnvironment] = useState<Environment>(cluster.environment)
  const [description, setDescription] = useState(cluster.description ?? '')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  async function save(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    setBusy(true)
    setError(null)
    try {
      // Sent whether or not it changed, including empty: an emptied chip means
      // "go back to the derivation", which the server can only be told by an
      // explicit empty rather than by an omitted field.
      await updateClusterLabels(cluster.id, {
        short_name: shortName,
        environment,
        description,
      })
      await onSaved()
      onClose()
    } catch (err) {
      setError(errorMessage(err, `Could not update ${cluster.name}.`))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Cluster"
      title={cluster.name}
      onClose={onClose}
      onSubmit={save}
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy}>
            {busy ? 'Saving…' : 'Save'}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-4 p-4">
        {error ? <Notice tone="error">{error}</Notice> : null}

        <Notice tone="info">
          The name and the connection are fixed. Every kubeconfig, grant and audit record already
          points at this cluster by them — changing one here would re-aim all of them silently.
          Register a new cluster instead, and remove this one when nothing needs it.
        </Notice>

        <Field
          label="Rail chip"
          htmlFor="cluster-short-name"
          hint={`Up to ${MAX_SHORT_NAME} characters. Empty falls back to ${deriveChip(cluster.name)}, derived from the name.`}
        >
          <div className="flex items-center gap-3">
            <TextInput
              id="cluster-short-name"
              autoFocus
              maxLength={MAX_SHORT_NAME}
              placeholder={deriveChip(cluster.name)}
              className="max-w-24 font-mono uppercase"
              value={shortName}
              onChange={(event) => setShortName(normalizeShortName(event.target.value))}
            />
            <span
              aria-hidden="true"
              className="grid size-10 shrink-0 place-items-center rounded-control border border-line bg-rail font-mono text-[10.5px] font-semibold text-rail-fg"
            >
              {railChip({ name: cluster.name, short_name: shortName })}
            </span>
          </div>
        </Field>

        <Field
          label="Environment"
          htmlFor="cluster-environment"
          hint="Drives how loudly the fleet flags it, and the tint on the rail's edge."
        >
          <div className="flex flex-wrap gap-2" id="cluster-environment">
            {ENVIRONMENTS.map((option) => (
              <button
                key={option}
                type="button"
                onClick={() => setEnvironment(option)}
                aria-pressed={environment === option}
                className={`flex h-9 items-center gap-2 rounded-control border px-3 transition-colors ${
                  environment === option
                    ? 'border-accent-line bg-accent-soft'
                    : 'border-line bg-surface hover:bg-raised'
                }`}
              >
                <EnvironmentTag environment={option} />
              </button>
            ))}
          </div>
        </Field>

        <Field
          label="Description"
          htmlFor="cluster-description"
          hint="Optional. What runs here, or who owns it."
        >
          <TextInput
            id="cluster-description"
            placeholder="Customer-facing workloads, EU region"
            value={description}
            onChange={(event) => setDescription(event.target.value)}
          />
        </Field>
      </div>
    </Sheet>
  )
}

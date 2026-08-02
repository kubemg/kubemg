import { useState } from 'react'
import type { FormEvent } from 'react'
import { Clock, ShieldAlert } from 'lucide-react'
import { createJitRequest, errorMessage } from '../../api/client'
import type { Cluster, JitRequest, K8sRole } from '../../api/types'
import { formatWindow } from '../../lib/time'
import { Button, Chip, Field, Notice, Select, Sheet, TextArea, TextInput } from '../primitives'

/**
 * Asking for elevated access on a cluster, for a while.
 *
 * The form is built around the two fields that are usually treated as
 * afterthoughts and are the whole point here.
 *
 * The **window** is a row of presets rather than a number box, because the honest
 * default for "how long do you need cluster-admin in production" is "as briefly as
 * possible" and a text field invites 480. The presets come from the API — the
 * server that will refuse an out-of-range window is the one that says which
 * windows exist — and the shortest is first.
 *
 * The **reason** is required, minimum-length, and the form says who reads it and
 * how long it is kept. That is not friction for its own sake: an approver deciding
 * in thirty seconds from a Slack card has nothing else to go on, and six months
 * later the reason is the only thing that explains the elevation at all.
 *
 * It also says what the elevation does *not* do. A grant here is carried by the
 * same impersonated, audited path as a standing one, so the cluster's own RBAC
 * still decides — telling somebody that up front is cheaper than having them
 * discover it as a refusal at 03:00.
 */

/** The three roles, ordered by privilege, with what each is actually for. */
const ROLE_HINT: Record<K8sRole, string> = {
  view: 'Read everything the cluster lets you read. Rarely worth a request unless your standing access is namespace-scoped.',
  edit: 'Change workloads: scale, restart, edit manifests. No RBAC, no cluster-scoped resources.',
  'cluster-admin':
    'Everything, cluster-wide, including secrets and RBAC. Ask for the shortest window that will do.',
}

/** Fallback windows, used only if the API list has not arrived yet. The server
    validates the value either way, so this can never widen what is allowed. */
const FALLBACK_DURATIONS = [30, 60, 120, 240, 480]

const MIN_REASON = 10

export function JitRequestModal({
  cluster,
  clusters,
  durations = FALLBACK_DURATIONS,
  onClose,
  onCreated,
}: {
  /** The cluster to request on. When given, the picker is fixed to it — asking from
      a cluster's own page should not offer to ask about a different one. */
  cluster?: Cluster
  /** The clusters this caller can see, for the picker when no cluster is fixed. */
  clusters?: Cluster[]
  durations?: number[]
  onClose: () => void
  onCreated: (request: JitRequest) => void
}) {
  const options = clusters ?? []
  const [clusterID, setClusterID] = useState<number>(cluster?.id ?? options[0]?.id ?? 0)
  const [role, setRole] = useState<K8sRole>('edit')
  const [minutes, setMinutes] = useState<number>(durations[0] ?? 30)
  const [namespaces, setNamespaces] = useState('')
  const [reason, setReason] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string | null>(null)

  const target = cluster ?? options.find((entry) => entry.id === clusterID)
  const reasonTooShort = reason.trim().length < MIN_REASON
  const valid = clusterID > 0 && !reasonTooShort

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!valid) return
    setBusy(true)
    setError(null)
    try {
      const created = await createJitRequest({
        cluster_id: clusterID,
        requested_role: role,
        // A blank box means every namespace the role allows, which is what an
        // empty list means to the API too.
        namespaces: namespaces
          .split(',')
          .map((entry) => entry.trim())
          .filter(Boolean),
        duration_minutes: minutes,
        reason: reason.trim(),
      })
      onCreated(created)
    } catch (err) {
      setError(errorMessage(err, 'Could not submit the request.'))
    } finally {
      setBusy(false)
    }
  }

  return (
    <Sheet
      eyebrow="Just-in-time access"
      title="Request elevated access"
      onClose={onClose}
      onSubmit={submit}
      width="lg"
      footer={
        <>
          <Button type="button" variant="ghost" onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" disabled={busy || !valid}>
            {busy ? 'Submitting…' : 'Submit request'}
          </Button>
        </>
      }
    >
      {error ? <Notice tone="error">{error}</Notice> : null}

      {cluster ? (
        <p className="flex flex-wrap items-baseline gap-2 rounded-control bg-raised px-3 py-2">
          <span className="label">Cluster</span>
          <span className="font-mono text-[12.5px] text-fg">{cluster.name}</span>
          <span className="label">now</span>
          <span className="font-mono text-[12.5px] text-fg">{cluster.k8s_role}</span>
        </p>
      ) : (
        <Field label="Cluster" htmlFor="jit-cluster">
          <Select
            id="jit-cluster"
            value={clusterID}
            onChange={(event) => setClusterID(Number(event.target.value))}
          >
            {options.length === 0 ? <option value={0}>No clusters available</option> : null}
            {options.map((entry) => (
              <option key={entry.id} value={entry.id}>
                {entry.name} · {entry.environment} · now {entry.k8s_role}
              </option>
            ))}
          </Select>
        </Field>
      )}

      <Field label="Role" htmlFor="jit-role" hint={ROLE_HINT[role]}>
        <Select
          id="jit-role"
          value={role}
          onChange={(event) => setRole(event.target.value as K8sRole)}
        >
          <option value="view">view</option>
          <option value="edit">edit</option>
          <option value="cluster-admin">cluster-admin</option>
        </Select>
      </Field>

      {/* The window as chips: a click, not a number somebody types larger. */}
      <div className="flex flex-col gap-1.5">
        <p className="label">Window</p>
        <div className="flex flex-wrap gap-2">
          {durations.map((option) => (
            <Chip key={option} active={option === minutes} onClick={() => setMinutes(option)}>
              <Clock aria-hidden="true" className="size-3.5" />
              <span className="font-mono text-[12.5px]">{formatWindow(option)}</span>
            </Chip>
          ))}
        </div>
        <p className="text-[12px] leading-snug text-muted">
          Access ends by itself when the window does — nobody has to remember to take it back. You
          can hand it back earlier from the access requests page.
        </p>
      </div>

      <Field
        label="Namespaces"
        htmlFor="jit-namespaces"
        hint="Comma-separated. Leave empty for every namespace the role allows — which is usually what cluster-admin is for, and rarely what edit needs."
      >
        <TextInput
          id="jit-namespaces"
          className="font-mono text-[12.5px]"
          placeholder="payments, checkout"
          value={namespaces}
          onChange={(event) => setNamespaces(event.target.value)}
        />
      </Field>

      <Field
        label="Reason"
        htmlFor="jit-reason"
        error={
          reason.length > 0 && reasonTooShort
            ? `A few more words — at least ${MIN_REASON} characters.`
            : undefined
        }
        hint="Read by whoever approves this, and kept with the record. Name the incident, ticket or change it is for."
      >
        <TextArea
          id="jit-reason"
          rows={3}
          value={reason}
          onChange={(event) => setReason(event.target.value)}
          placeholder="INC-4821: checkout pods are crash-looping and I need to read the OOM events and restart the deployment."
        />
      </Field>

      {/* What the elevation is not. Said before the request rather than
          discovered as a refusal later. */}
      <Notice tone="info">
        <span className="inline-flex items-baseline gap-1.5">
          <ShieldAlert aria-hidden="true" className="size-3.5 translate-y-0.5 shrink-0" />
          <span>
            An approval grants <strong>{role}</strong> on{' '}
            <strong>{target ? target.name : 'the cluster'}</strong> for{' '}
            {formatWindow(minutes)}. Every call still goes down the audited tunnel under your own
            identity, so the cluster&rsquo;s own RBAC decides what that role can do — and you cannot
            approve this yourself.
          </span>
        </span>
      </Notice>
    </Sheet>
  )
}

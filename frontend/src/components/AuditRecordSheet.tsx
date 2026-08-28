import type { ReactNode } from 'react'
import { FileDiff, PlayCircle } from 'lucide-react'

import type { AuditEvent } from '../api/types'
import { formatInstant, relativeAge } from '../lib/time'
import { Button, CodeBlock, DetailList, Pill, Sheet } from './primitives'
import type { Tone } from '../lib/status'

/*
 * One audit record, opened.
 *
 * The trail was a good screen and not yet evidence, and this is most of the
 * difference. A row could not be opened, so four things the record already
 * carried had nowhere to be read: the **full path** (the cell truncates it with
 * an ellipsis, and the path is frequently the whole question — which object, in
 * which namespace, with which query), the **guardrail decision** (a policy that
 * matched and whether it blocked or only warned, which is the entire point of a
 * warn-mode rollout), the **crossed identities** (the KubeMG account on one side
 * and the Kubernetes subject KubeMG asserted on the other — the crux of the
 * record, and the one thing that ties a person to what the cluster actually
 * saw), and **where the call came from**.
 *
 * Nothing here is fetched. Every field is already on the row the table drew, so
 * opening a record is free and works offline of the server that wrote it — which
 * also means this sheet cannot drift from the list: they are the same object.
 *
 * The order is the order an access review is walked: when, who, from where,
 * against what, and what happened. Consequences last, because a replay and a
 * diff are things you do *after* reading the record, not instead of.
 */

/** Absent is how this sheet says a field was not recorded, rather than blank. */
function Absent({ children = 'not recorded' }: { children?: ReactNode }) {
  return <span className="text-faint italic">{children}</span>
}

function statusTone(event: AuditEvent): Tone {
  if (event.error || event.status >= 500) return 'bad'
  if (event.status >= 400) return 'warn'
  return 'ok'
}

/** The word the pill carries, matching the table's so a row and its record agree. */
function statusLabel(event: AuditEvent): string {
  if (event.error) return 'refused'
  if (event.streaming && event.phase === 'open') return 'open'
  return String(event.status)
}

export function AuditRecordSheet({
  event,
  onClose,
  onReplay,
  onViewDiff,
}: {
  event: AuditEvent
  onClose: () => void
  /** Offered only where the row names a recorded session. */
  onReplay?: () => void
  /** Offered only where the row carries a stored manifest diff. */
  onViewDiff?: () => void
}) {
  const groups = event.impersonated_groups ?? []

  return (
    <Sheet
      eyebrow="Audit record"
      title={
        <span className="flex items-center gap-2">
          <span className="font-mono">{event.verb}</span>
          <Pill tone={statusTone(event)}>{statusLabel(event)}</Pill>
        </span>
      }
      width="lg"
      onClose={onClose}
      footer={
        onReplay || onViewDiff ? (
          <>
            {onViewDiff ? (
              <Button type="button" variant="secondary" onClick={onViewDiff}>
                <FileDiff aria-hidden="true" className="size-4" />
                Manifest diff
              </Button>
            ) : null}
            {onReplay ? (
              <Button type="button" variant="primary" onClick={onReplay}>
                <PlayCircle aria-hidden="true" className="size-4" />
                Replay session
              </Button>
            ) : null}
          </>
        ) : undefined
      }
    >
      <section>
        <h3 className="label mb-2">When and who</h3>
        <DetailList
          columns={2}
          rows={[
            {
              term: 'At',
              // The absolute instant is the reading here, with the relative age
              // beside it: a list is scanned in "13h ago" and a record is filed
              // in an instant that means the same thing in every timezone.
              value: (
                <span title={relativeAge(event.at)}>{formatInstant(event.at, { seconds: true })}</span>
              ),
            },
            { term: 'Age', value: relativeAge(event.at) },
            { term: 'Account', value: event.username || <Absent /> },
            { term: 'Duration', value: `${event.duration_ms} ms` },
          ]}
        />
      </section>

      <section>
        {/* The crossed identities. This is the record's crux — the KubeMG
            account on one side, the Kubernetes subject KubeMG asserted to the
            API server on the other — and it had nowhere to be read at all. */}
        <h3 className="label mb-2">Identities crossed</h3>
        <DetailList
          columns={2}
          rows={[
            { term: 'KubeMG account', value: event.username || <Absent /> },
            { term: 'Impersonated as', value: event.impersonated_user || <Absent /> },
            {
              term: 'Groups asserted',
              value: groups.length > 0 ? groups.join(', ') : <Absent>none</Absent>,
            },
          ]}
        />
      </section>

      <section>
        {/* "From where" is the second question in any SOC 2 or ISO 27001
            walkthrough after "who", and the schema could not answer it until
            these two columns existed. A row written before they did says so. */}
        <h3 className="label mb-2">From where</h3>
        <DetailList
          columns={2}
          rows={[
            { term: 'Source address', value: event.source_addr || <Absent /> },
            { term: 'Client', value: event.user_agent || <Absent /> },
          ]}
        />
        {!event.source_addr && !event.user_agent ? (
          <p className="mt-2 text-[12px] leading-relaxed text-faint">
            Either this server did it on its own — an expiring grant closed out, an alarm poll —
            or the record predates the columns that hold it. Neither can be filled in afterwards.
          </p>
        ) : null}
      </section>

      <section>
        <h3 className="label mb-2">What was called</h3>
        <DetailList
          columns={2}
          rows={[
            { term: 'Cluster', value: event.cluster || <Absent /> },
            { term: 'Namespace', value: event.namespace || <Absent>cluster-scoped</Absent> },
            { term: 'Resource', value: event.resource || <Absent /> },
            { term: 'Method', value: event.method || <Absent /> },
          ]}
        />
        {/* The whole path, wrapped and copyable. In the table it is one
            truncated line, and the truncated half is usually the object. */}
        <div className="mt-3">
          <CodeBlock label="Path" value={event.path} wrap />
        </div>
      </section>

      {event.guardrail_policy ? (
        <section>
          {/* A warn-mode rollout is read here: the rule matched, and this says
              whether the call was refused or merely noted. */}
          <h3 className="label mb-2">Guardrail</h3>
          <DetailList
            columns={2}
            rows={[
              { term: 'Policy', value: event.guardrail_policy },
              {
                term: 'Action',
                value: event.guardrail_action || <Absent />,
                tone: event.guardrail_action === 'block' ? 'bad' : 'warn',
              },
            ]}
          />
        </section>
      ) : null}

      <section>
        <h3 className="label mb-2">What happened</h3>
        <DetailList
          columns={2}
          rows={[
            { term: 'Status', value: String(event.status), tone: event.status >= 400 ? 'warn' : 'default' },
            { term: 'Streaming', value: event.streaming ? (event.phase ?? 'yes') : 'no' },
            ...(event.streaming
              ? [
                  { term: 'Bytes out', value: String(event.bytes_out ?? 0) },
                  { term: 'Bytes in', value: String(event.bytes_in ?? 0) },
                ]
              : []),
            {
              term: 'Session',
              value: event.session_id || <Absent>not a session</Absent>,
            },
          ]}
        />
        {event.error ? (
          <p className="mt-3 rounded-control border border-danger/35 bg-danger-soft px-3 py-2.5 text-[12.5px] leading-relaxed text-danger">
            {event.error}
          </p>
        ) : null}
      </section>
    </Sheet>
  )
}
